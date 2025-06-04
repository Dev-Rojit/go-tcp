package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	LoginProtocolNum = 0x01
	StartBit1        = 0x78
	ReadTimeout      = 30 * time.Second
	WriteTimeout     = 10 * time.Second
)

type LoginPacket struct {
	IMEI         string
	SerialNumber uint16
}

type Message struct {
	From    string
	Payload string
	Conn    net.Conn
}

type Server struct {
	listenAddr string
	ln         net.Listener
	wg         sync.WaitGroup
	msgCh      chan Message
	shutdownCh chan struct{}
	ctx        context.Context
	cancel     context.CancelFunc
	logger     *log.Logger
}

func NewServer(listenAddr string) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		listenAddr: listenAddr,
		msgCh:      make(chan Message, 100),
		shutdownCh: make(chan struct{}),
		ctx:        ctx,
		cancel:     cancel,
		logger:     log.New(os.Stdout, "GT06: ", log.LstdFlags|log.Lshortfile),
	}
}

func (s *Server) Start() error {
	var err error
	s.ln, err = net.Listen("tcp", s.listenAddr)
	if err != nil {
		return fmt.Errorf("failed to start listener: %w", err)
	}
	defer s.ln.Close()

	s.logger.Printf("Server started on %s", s.listenAddr)
	s.wg.Add(1)
	go s.processMessages()
	go s.handleShutdown()

	err = s.acceptLoop()
	if err != nil {
		s.cancel()
		return err
	}

	s.wg.Wait()
	close(s.msgCh)
	s.logger.Println("Server shutdown complete")
	return nil
}

func (s *Server) handleShutdown() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigCh:
		s.logger.Println("Received shutdown signal")
	case <-s.ctx.Done():
		s.logger.Println("Server context cancelled")
	case <-s.shutdownCh:
		s.logger.Println("Internal shutdown triggered")
	}

	s.cancel()
	s.ln.Close()
}

func (s *Server) acceptLoop() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				return nil
			default:
				return fmt.Errorf("accept error: %w", err)
			}
		}

		s.wg.Add(1)
		go s.handleConnection(conn)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer func() {
		conn.Close()
		s.wg.Done()
	}()

	remoteAddr := conn.RemoteAddr().String()
	s.logger.Printf("New connection from %s", remoteAddr)

	for {
		if err := conn.SetReadDeadline(time.Now().Add(ReadTimeout)); err != nil {
			s.logger.Printf("SetReadDeadline error for %s: %v", remoteAddr, err)
			return
		}

		buf := make([]byte, 2048)
		n, err := conn.Read(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) || err.Error() == "EOF" {
				s.logger.Printf("Connection %s closed", remoteAddr)
			} else if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				s.logger.Printf("Read timeout for %s", remoteAddr)
			} else {
				s.logger.Printf("Read error for %s: %v", remoteAddr, err)
			}
			return
		}

		select {
		case s.msgCh <- Message{
			From:    remoteAddr,
			Payload: string(buf[:n]),
			Conn:    conn,
		}:
		case <-s.ctx.Done():
			s.logger.Printf("Dropping message from %s during shutdown", remoteAddr)
			return
		}
	}
}

func (s *Server) processMessages() {
	defer s.wg.Done()

	for {
		select {
		case msg, ok := <-s.msgCh:
			if !ok {
				s.logger.Println("Message channel closed")
				return
			}

			if err := s.handleMessage(msg); err != nil {
				s.logger.Printf("Error handling message from %s: %v", msg.From, err)
			}

		case <-s.ctx.Done():
			s.logger.Println("Stopping message processor")
			return
		}
	}
}

func (s *Server) handleMessage(msg Message) error {
	data, err := hex.DecodeString(msg.Payload)
	if err != nil {
		return fmt.Errorf("failed to decode it from hex %s", msg.Payload)
	}
	if len(data) < 17 {
		return fmt.Errorf("message too short (%d bytes)", len(data))
	}

	if !(data[0] == 0x78 && data[1] == 0x78) {
		return fmt.Errorf("invalid start bits")
	}

	protocolNum := data[3]
	if protocolNum == LoginProtocolNum {
		login, err := parseLoginPacket(data)
		if err != nil {
			return fmt.Errorf("login parse error: %w", err)
		}
		s.logger.Printf("Login from %s - IMEI: %s", msg.From, login.IMEI)
		return s.sendLoginAck(msg.Conn, login.SerialNumber)
	} else {
		s.logger.Printf("Unknown protocol 0x%x from %s", protocolNum, msg.From)
	}

	return nil
}

func parseLoginPacket(data []byte) (*LoginPacket, error) {
	if len(data) < 17 {
		return nil, fmt.Errorf("packet too short")
	}

	imeiBytes := data[4:12]
	imei, err := bcdToIMEI(imeiBytes)
	if err != nil {
		return nil, err
	}

	serial := binary.BigEndian.Uint16(data[12:14])
	checksum := data[14]
	calculatedChecksum := calculateChecksum(data[2:14])
	if checksum != calculatedChecksum {
		return nil, fmt.Errorf("checksum mismatch: got 0x%x, expected 0x%x", checksum, calculatedChecksum)
	}

	if !(data[15] == 0x0D && data[16] == 0x0A) {
		return nil, errors.New("invalid stop bits")
	}

	return &LoginPacket{
		IMEI:         imei,
		SerialNumber: serial,
	}, nil
}

func bcdToIMEI(bcd []byte) (string, error) {
	imei := ""
	for _, b := range bcd {
		high := (b >> 4) & 0x0F
		low := b & 0x0F
		if high > 9 || low > 9 {
			return "", fmt.Errorf("invalid BCD digit: 0x%x", b)
		}
		imei += fmt.Sprintf("%d%d", high, low)
	}
	if len(imei) > 15 {
		imei = imei[:15]
	}
	return imei, nil
}

func calculateChecksum(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum ^= b
	}
	return sum
}

func (s *Server) sendLoginAck(conn net.Conn, serialNumber uint16) error {
	if conn == nil {
		return errors.New("nil connection")
	}
	if err := conn.SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil {
		return fmt.Errorf("set write deadline failed: %w", err)
	}

	ack := createLoginAckPacket(serialNumber)
	_, err := conn.Write(ack)
	return err
}

func createLoginAckPacket(serialNumber uint16) []byte {
	var buf bytes.Buffer
	buf.WriteByte(StartBit1)
	buf.WriteByte(0x05)
	buf.WriteByte(LoginProtocolNum)
	binary.Write(&buf, binary.BigEndian, serialNumber)
	buf.WriteByte(calculateChecksum(buf.Bytes()[1:]))
	buf.Write([]byte{0x0D, 0x0A})
	return buf.Bytes()
}

func main() {
	server := NewServer(":5000")
	if err := server.Start(); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
