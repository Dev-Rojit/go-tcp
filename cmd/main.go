package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
)

type LoginPacket struct {
	IMEI         string
	SerialNumber uint16
}

type Message struct {
	from    string
	payload []byte
	conn    net.Conn
}

type Server struct {
	listenAddr string
	ln         net.Listener
	quitCh     chan struct{}
	msgCh      chan Message
}

func NewServer(listenAddr string) *Server {
	return &Server{
		listenAddr: listenAddr,
		quitCh:     make(chan struct{}),
		msgCh:      make(chan Message, 10),
	}
}

const (
	LoginProtocolNum = 0x01
	StartBit1        = 0x78
	StartBit2        = 0x79
)

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return err
	}
	defer ln.Close()
	s.ln = ln
	go s.handleMessage()
	s.acceptLoop()
	<-s.quitCh
	close(s.msgCh)

	return nil
}

func (s *Server) acceptLoop() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			fmt.Println("Accept error", err)
			continue
		}
		fmt.Println("New connection to the server", conn.RemoteAddr())

		go s.readLoop(conn)
	}
}

func (s *Server) readLoop(conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 2048)

	for {
		n, err := conn.Read(buf)
		if err != nil {
			fmt.Println("Read error", err)
			continue
		}
		s.msgCh <- Message{
			from:    conn.RemoteAddr().String(),
			payload: buf[:n],
			conn:    conn,
		}

		// conn.Write([]byte("writting to tcp client"))
	}
}

func (s *Server) handleMessage() {
	for msg := range s.msgCh {
		if len(msg.payload) < 4 {
			fmt.Printf("Message too short for %s", msg.from)
			continue
		}

		protocolNumber := msg.payload[2]
		switch protocolNumber {
		case LoginProtocolNum:
			login, err := s.parseLoginPacket(msg.payload)
			if err != nil {
				fmt.Printf("Login parse error from %s: %v\n", msg.from, err)
				continue
			}
			fmt.Printf("Login received from %s - IMEI: %s\n", msg.from, login.IMEI)
			s.sendLoginAck(msg.conn, login.SerialNumber)
		default:
			fmt.Printf("Unknown protocol 0x%x from %s\n", protocolNumber, msg.from)
		}
	}
}

func (s *Server) sendLoginAck(conn net.Conn, serialNumber uint16) {
	if conn == nil {
		return
	}

	ack := createLoginAckPacket(serialNumber)
	_, err := conn.Write(ack)
	if err != nil {
		fmt.Printf("Failed to send login ack: %v\n", err)
	}
}
func createLoginAckPacket(serialNumber uint16) []byte {
	var buf bytes.Buffer
	buf.WriteByte(StartBit1)                           // Start bit
	buf.WriteByte(0x05)                                // Packet length
	buf.WriteByte(LoginProtocolNum)                    // Protocol number
	binary.Write(&buf, binary.BigEndian, serialNumber) // Serial number
	buf.WriteByte(calculateChecksum(buf.Bytes()[1:]))
	buf.Write([]byte{0x0D, 0x0A}) // Stop bits
	return buf.Bytes()
}

func calculateChecksum(data []byte) byte {
	var sum byte
	for _, b := range data {
		sum ^= b
	}
	return sum
}

func (s *Server) parseLoginPacket(data []byte) (*LoginPacket, error) {

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

func main() {
	server := NewServer(":5000")

	go func() {
		for msg := range server.msgCh {
			fmt.Printf("Received message from connectinn (%s): %s\n", msg.from, string(msg.payload))
		}
	}()
	server.Start()

}
