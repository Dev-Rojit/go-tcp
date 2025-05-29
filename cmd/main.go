package main

import (
	"log"
	"net"
	"time"

	"github.com/freman/gps2mqtt/mqtt"
	"github.com/freman/gps2mqtt/protocol/gt06"
	"github.com/rs/zerolog"
)

const (
	serverAddr = ":8888" // You can change this port as needed
)

type CustomGT06Listener struct {
	*gt06.Listener
}

func NewCustomGT06Listener() *CustomGT06Listener {
	return &CustomGT06Listener{
		Listener: &gt06.Listener{
			Listen: serverAddr,
		},
	}
}

func (l *CustomGT06Listener) handleClientConnection(conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovered from panic in connection handler: %v", r)
		}
		if err := conn.Close(); err != nil {
			log.Printf("Error closing connection: %v", err)
		}
	}()

	log.Printf("New connection from %s", conn.RemoteAddr())

	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		log.Printf("Failed to set read deadline: %v", err)
		return
	}

	packetChan := make(chan mqtt.Identifier)

	logger := zerolog.New(log.Writer()).With().Timestamp().Logger()

	go func() {
		for msg := range packetChan {
			if msg == nil {
				log.Println("Received nil message from channel")
				continue
			}

			if pkt, ok := msg.(*gt06.Packet); ok {
				onPacketReceived(pkt)
			} else {
				log.Printf("Received unknown MQTT Identifier type: %T", msg)
			}
		}
	}()

	l.HandleConnection(conn, packetChan, logger)
}

func onPacketReceived(pkt *gt06.Packet) {
	log.Printf("[GPS Data] Device: %s | Time: %s | Lat: %.6f | Lon: %.6f | Speed: %.2f km/h | Satellites: %d",
		pkt.DeviceID,
		pkt.Timestamp.Format(time.RFC3339),
		pkt.Latitude,
		pkt.Longitude,
		pkt.Speed,
		pkt.Satelites,
	)
}

func main() {
	listener := NewCustomGT06Listener()

	// Start TCP listener
	log.Printf("Starting GT06 TCP server on %s", listener.Listen)
	netListener, err := net.Listen("tcp", listener.Listen)
	if err != nil {
		log.Fatalf("Failed to start TCP server: %v", err)
	}
	defer netListener.Close()

	log.Println("Server is running... Waiting for connections.")

	for {
		conn, err := netListener.Accept()
		if err != nil {
			log.Printf("Error accepting connection: %v", err)
			continue
		}

		go listener.handleClientConnection(conn)
	}

}
