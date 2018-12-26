package main

import (
	"crypto/tls"
	"encoding/json"
	"log"
	"net"
	"time"

	"golang.org/x/net/ipv4"
)

// Client handler function for :443
func (s *Service) serve(conn net.Conn, tuntx chan<- []byte, clientstate chan<- ClientState, netblock <-chan net.IP) {
	// Leave the shutdown group when handler exits
	defer s.shutdownGroup.Done()

	// Close connection when handler exits
	defer conn.Close()

	log.Print("server: conn: starting")

	// Get a TLS connection from our plain net.Conn
	tlscon, ok := conn.(*tls.Conn)
	if !ok {
		log.Print("server: conn(term): not a TLS connection")
		return
	}

	// Progress to the tls handshake
	err := tlscon.Handshake()
	if err != nil {
		log.Printf("server: conn(term): TLS handshake failed: %s", err)
		return
	} else {
		log.Print("server: conn: TLS handshake completed")
	}

	// Validate this connection as a valid new client
	client, err := NewClient(tlscon)
	if err != nil {
		log.Printf("server: conn(term): error validating client: %s", err)
		//TODO: Send HTTP 403 response
		//tlscon.Write()
		return
	}

	// Producer that pumps the read-side of the client connection into the clientrx channel
	// Exits on failing read after deferred conn.Close() or zero-length read from client close
	s.shutdownGroup.Add(1)
	clientrx := make(chan []byte)
	go connrx(tlscon, clientrx, s.shutdownGroup)

	// Application-Layer Handshake
	// Read first packet from client with a timeout
	select {
	case infobuf := <-clientrx:
		log.Print(infobuf)
		// Decode client info struct from json in the first packet, delimited with newline
		var info ClientInfo
		if err := json.Unmarshal(infobuf, &info); err != nil {
			log.Print("server: conn(term): error decoding client info packet")
			return
		}

		// TODO: Validate client info

		// Allocate client IP address
		client.ip = <-netblock

		// Create client settings to send
		settings := ClientSettings{
			time:    time.Now(),
			version: "0.1.0",
			ip:      client.ip.String(),
		}

		// Encode client settings struct to newline delimited json and send as first packet
		settingsbuf, err := json.Marshal(settings)
		if err != nil {
			log.Print("server: conn(term): error encoding client settings packet")
			return
		}

		// Write the settings buffer out to the client
		n, err := conn.Write(settingsbuf)
		log.Printf("server: conn: sent client settings bytes: %d", n)
		if err != nil {
			log.Printf("server: conn(term): error sending client settings: %s", err)
			return
		}

	case <-time.After(2 * time.Minute): // TODO: Define in config
		log.Print("server: conn(term): timed out waiting for client info")
		return
	}

	// A channel to signal a write error to the client
	writeerr := make(chan bool, 2)

	// Pipe that pumps packets from the client rx channel into the client connection
	// This needs to continue pumping until the txchan is closed or the router could stall
	// Exits after `close(client.tx)` by router
	s.shutdownGroup.Add(1)
	go conntx(client.tx, tlscon, writeerr, s.shutdownGroup)

	// Defer client cleanup to when leaving the handler
	defer func() {
		// Record disconnect time in client
		client.disconnected = time.Now()

		// Send disconnect client state change
		clientstate <- ClientState{
			happened:   time.Now(),
			transition: Disconnect,
			client:     client,
		}
	}()

	// Send client connect state change
	// This causes the client.tx channel to be mounted by the tun router and it will now receieve traffic
	clientstate <- ClientState{
		happened:   time.Now(),
		transition: Connect,
		client:     client,
	}

	// Forever select on the done channel, the rwerr channel, the clientrx read producer channel, and the control channel
	// until a read or write operation fails, the done signal is received, or a control command terminates the connection
	for {
		select {
		// Disconnect if we're told to shut down shop
		case <-s.done:
			log.Println("server: conn(term): got done signal", tlscon.RemoteAddr())
			return

		// Disconnect if we error writing to client
		case <-writeerr:
			log.Print("server: conn(term): encountered client write error")
			return

		// Consumes packets from the clientrx channel then sends them into the tuntx channel
		case buf, ok := <-clientrx:
			if !ok {
				log.Print("server: conn(term): clientrx chan closed")
				return
			}

			log.Print("server: conn: received packet from client")

			// Grab the packet ip header
			header, _ := ipv4.ParseHeader(buf)

			// Drop any packets with a source address different than the one allocated to the client
			if !header.Src.Equal(client.ip) {
				continue
			}

			// TODO: Process packet to work on tun adapter?

			// Push the received packet to the tun tx channel
			tuntx <- buf

		// Handle client control messages
		case msg := <-client.control:
			// Leave the loop if we are to disconnect
			if msg == "disconnect" {
				log.Print("server: conn(term): received disconnect control")
				return
			}
		}
	}
}
