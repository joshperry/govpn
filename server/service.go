package main

import (
	"encoding/json"
	"log"
	"math"
	"net"
	"sync"
	"time"
)

// The VPN server service
type Service struct {
	done          chan bool       // A channel to signal shutdown of the service
	shutdownGroup *sync.WaitGroup // A waitgroup to syncronize graceful shutdown
	clientGroup   *sync.WaitGroup // A waitgroup to syncronize graceful client shutdown
}

// Make a new Service
func NewService() *Service {
	s := &Service{
		done:          make(chan bool),
		shutdownGroup: &sync.WaitGroup{},
		clientGroup:   &sync.WaitGroup{},
	}
	s.shutdownGroup.Add(1)
	return s
}

// Represents a tracked connection
type Connection struct {
	time     time.Time
	name     string
	ip       string
	publicip string
	pending  bool
}

// A list of connections!
type Connections []Connection

// Marshal function to format Connection Time fields as ISO8601 json strings
func (c Connection) MarshalJSON() ([]byte, error) {
	type Alias Connection
	return json.Marshal(&struct {
		Alias
		time string
	}{
		Alias: (Alias)(c),
		time:  c.time.Format(time.RFC3339),
	})
}

// Accept connections and spawn a goroutine to serve() each one.
// Stop listening if anything is received on the done channel.
// tuntx: channel to write packets from the client to the tun adapter
// tunrx: channel to read packets for the clients from the tun adapter
func (s *Service) Serve(listener net.Listener, tuntx chan<- []byte, tunrx <-chan []byte, servernet *net.IPNet) {
	defer s.shutdownGroup.Done()
	// Close the listener when the server stops
	defer listener.Close()

	statesub := make(chan ClientStateSub)

	// Route packets bound for clients as they come in the tunrx channel
	// Uses clientstate channel to keep internal routing table up to date
	// Exits when tunrx or clientstate channels are closed
	go route(tunrx, statesub)

	// Calculate netblock info
	netmasklen, networksize := servernet.Mask.Size()
	hostcount := uint32(math.Pow(2, float64(networksize-netmasklen)))

	// Implements a channel that delivers unused IP addresses when read
	// And returns IPs to the pool when a client disconnects
	// Set the buffer size to the host count - 3 (network address, server address, and broadcast address)
	// Exits when netblockstate is closed
	netblock := make(chan net.IP, hostcount-2)
	go runblock(netblock, statesub, hostcount, ip2int(servernet.IP))

	// Track client connection lifetimes for reporting and enforcement
	// Exits when contrackstate channel is closed
	go contrack(statesub)

	// Channel to send client connection state changes to
	clientstate := make(chan ClientState)
	// No more client states are sent when the server exits
	defer close(clientstate)

	// Publish clientstate
	// Exits when clientstate closes
	go publishstate(clientstate, statesub)

	// Goroutine to pump the accept loop into a handler channel
	// Exits when Accept fails on deferred listener.Close() or e.g. insufficient file handles
	// closes connchan when listen fails
	connchan := make(chan net.Conn)
	s.shutdownGroup.Add(1)
	go acceptor(listener, connchan, s.shutdownGroup)

	// Forever select on the done channel, and the client connection handler channel
	for {
		select {
		case <-s.done:
			log.Print("server: got done signal", listener.Addr())
			// Wait on the client waitgroup when leaving
			s.clientGroup.Wait()

			log.Print("server: client group done")
			return

		case conn, ok := <-connchan:
			if !ok {
				log.Print("server: connchan closed", listener.Addr())
				// Wait on the client waitgroup when leaving
				s.clientGroup.Wait()

				log.Print("server: client group done")
				return
			}

			log.Printf("server: %s connected", conn.RemoteAddr())
			// Add a client to the waitgroup, and handle it in a goroutine
			s.clientGroup.Add(1)
			go s.serve(conn, tuntx, clientstate, netblock)
		}
	}
}

// Stop the service by closing the done channel
// Block until the service and all clients have stopped
func (s *Service) Stop() {
	// Close the channel to signal done
	close(s.done)
	// Wait on the waitgroup to empty
	s.shutdownGroup.Wait()
}
