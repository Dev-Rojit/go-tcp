package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

const (
	address = "0.0.0.0:9000" // change as needed
)

func main() {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatalf("Error starting TCP server: %v", err)
	}
	defer listener.Close()

	log.Printf("Server started on %s", address)

	// Graceful shutdown handling
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				log.Printf("Failed to accept connection: %v", err)
				continue
			}
			go handleConnection(conn)
		}
	}()

	// Wait for termination signal
	<-stopChan
	log.Println("Shutting down server...")
}

func handleConnection(conn net.Conn) {
	defer conn.Close()
	clientAddr := conn.RemoteAddr().String()
	log.Printf("New connection from %s", clientAddr)

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		msg := scanner.Text()
		log.Printf("Received from %s: %s", clientAddr, msg)

		if strings.ToLower(msg) == "exit" {
			fmt.Fprintf(conn, "Goodbye!\n")
			break
		}

		// Echo message back to client
		fmt.Fprintf(conn, "Echo: %s\n", msg)
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Error reading from %s: %v", clientAddr, err)
	}

	log.Printf("Connection closed: %s", clientAddr)
}
