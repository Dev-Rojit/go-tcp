package main

import (
	"bytes"
	"context"
	"encoding/binary"
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
	StartBit2        = 0x79
	ReadTimeout      = 30 * time.Second
	WriteTimeout     = 10 * time.Second
)

type LoginPacket struct {
	IMEI         string
	SerialNumber uint16
}

type Message struct {
	From    string
	Payload []byte
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

	// Handle graceful shutdown
	go s.handleShutdown()

	// Start accept loop
	err = s.acceptLoop()
	if err != nil {
		s.cancel() // Signal shutdown on error
		return err
	}

	// Wait for all goroutines to finish
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
	s.ln.Close() // This will cause acceptLoop to exit
}

func (s *Server) acceptLoop() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.ctx.Done():
				// Expected error during shutdown
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
		// Set read deadline to prevent hanging connections
		if err := conn.SetReadDeadline(time.Now().Add(ReadTimeout)); err != nil {
			s.logger.Printf("SetReadDeadline error for %s: %v", remoteAddr, err)
			return
		}

		buf := make([]byte, 2048)
		n, err := conn.Read(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
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
			Payload: bytes.Clone(buf[:n]),
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
	// Basic validation
	if len(msg.Payload) < 4 {
		return fmt.Errorf("message too short (%d bytes)", len(msg.Payload))
	}

	// Check protocol number (byte 2 in GT06 protocol)
	protocolNum := msg.Payload[2]

	switch protocolNum {
	case LoginProtocolNum:
		login, err := parseLoginPacket(msg.Payload)
		if err != nil {
			return fmt.Errorf("login parse error: %w", err)
		}
		s.logger.Printf("Login from %s - IMEI: %s", msg.From, login.IMEI)
		if err := s.sendLoginAck(msg.Conn, login.SerialNumber); err != nil {
			return fmt.Errorf("failed to send ack: %w", err)
		}
	default:
		s.logger.Printf("Unknown protocol 0x%x from %s", protocolNum, msg.From)
	}

	return nil
}

func parseLoginPacket(data []byte) (*LoginPacket, error) {
	if len(data) < 23 {
		return nil, fmt.Errorf("packet too short (%d bytes)", len(data))
	}

	if data[0] != StartBit1 && data[0] != StartBit2 {
		return nil, fmt.Errorf("invalid start bit: 0x%x", data[0])
	}

	packetLen := data[1]
	if packetLen < 0x11 || packetLen > 0x12 {
		return nil, fmt.Errorf("invalid login packet length: 0x%x", packetLen)
	}

	imei := string(data[3:18])
	if len(imei) != 15 {
		return nil, fmt.Errorf("invalid IMEI length: %d", len(imei))
	}

	serialNumber := binary.BigEndian.Uint16(data[18:20])

	calculatedChecksum := calculateChecksum(data[1 : len(data)-3])
	receivedChecksum := data[len(data)-3]
	if calculatedChecksum != receivedChecksum {
		return nil, fmt.Errorf("checksum mismatch (calc: 0x%x, recv: 0x%x)",
			calculatedChecksum, receivedChecksum)
	}

	return &LoginPacket{
		IMEI:         imei,
		SerialNumber: serialNumber,
	}, nil
}

func (s *Server) sendLoginAck(conn net.Conn, serialNumber uint16) error {
	if conn == nil {
		return errors.New("nil connection")
	}

	if err := conn.SetWriteDeadline(time.Now().Add(WriteTimeout)); err != nil {
		return fmt.Errorf("set write deadline failed: %w", err)
	}

	ack := createLoginAckPacket(serialNumber)
	if _, err := conn.Write(ack); err != nil {
		return fmt.Errorf("write failed: %w", err)
	}

	return nil
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

func calculateChecksum(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum ^= b
	}
	return sum
}

func main() {
	server := NewServer(":5000")

	if err := server.Start(); err != nil {
		server.logger.Fatalf("Server failed: %v", err)
	}
}
