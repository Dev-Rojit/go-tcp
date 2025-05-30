package main

import (
	"fmt"
	"net"
)

type Message struct {
	from    string
	payload []byte
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

func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return err
	}
	defer ln.Close()
	s.ln = ln
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
		}

		// conn.Write([]byte("writting to tcp client"))
	}

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
