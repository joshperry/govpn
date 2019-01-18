package main

import (
	"encoding/json"
	"log"
	"math"
	"net"
	"sync"
	"time"

	"github.com/micro/go-config"
	"github.com/songgao/water"
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
	Time     time.Time `json:"time"`
	Name     string    `json:"name"`
	IP       string    `json:"ip"`
	PublicIP string    `json:"publicip"`
	Pending  bool      `json:"pending"`
}

// A list of connections!
type Connections []Connection

// Marshal function to format Connection Time fields as ISO8601 json strings
func (c Connection) MarshalJSON() ([]byte, error) {
	type Alias Connection
	return json.Marshal(&struct {
		Alias
		Time string `json:"time"`
	}{
		Alias: (Alias)(c),
		Time:  c.Time.UTC().Format(time.RFC3339),
	})
}

// Accept connections and spawn a goroutine to serve() each one.
// Stop listening if anything is received on the done channel.
// tuntx: channel to write packets from the client to the tun adapter
// tunrx: channel to read packets for the clients from the tun adapter
func (s *Service) Serve(listener net.Listener, tun *water.Interface, bufpool *sync.Pool, servernet *net.IPNet) {
	defer func() {
		s.shutdownGroup.Done()
		// Close the listener when the server stops
		listener.Close()
		log.Print("service(perm): au revoir")
	}()

	// A channel for subscribing the client state stream
	statesub := make(chan ClientStateSub)

	// A channel for messages exiting via the tun interface
	tuntxchan := make(chan *message)
	defer close(tuntxchan)

	// A channel for the packet routers
	routers := make(chan *message)

	// Start up multiple routers
	for _ = range [4]int{} {
		// Routes packets from the tun adapter to the appropriate client
		// Keeps the route info updated from the client state chan
		// Exits when state channel is closed
		s.shutdownGroup.Add(1)
		go route(routers, tuntxchan, statesub, bufpool, s.shutdownGroup)
	}

	// Producer that reads packets off of the tun interface and delivers them to the router channel
	go tunrx(tun, routers, bufpool)
	// Consumer that reads packets off the tuntxchan and puts them on the tun interface
	go tuntx(tuntxchan, tun, bufpool)

	// Start up multiple readers/writers with separate queues
	for _ = range [7]int{} {
		tunconfig := water.Config{DeviceType: water.TUN, PlatformSpecificParams: water.PlatformSpecificParams{MultiQueue: true}}
		tunconfig.Name = config.Get("tun", "name").String("tun_govpn")
		if tun, err := water.New(tunconfig); nil != err {
			log.Fatalln("server: unable to allocate additional TUN interface queue:", err)
		} else {
			defer tun.Close()

			go tunrx(tun, routers, bufpool)
			go tuntx(tuntxchan, tun, bufpool)
		}
	}

	// Calculate netblock info
	netmasklen, networksize := servernet.Mask.Size()
	hostcount := uint32(math.Pow(2, float64(networksize-netmasklen)))

	// Implements a channel that delivers unused IP addresses when read
	// And returns IPs to the pool when a client disconnects
	// Set the buffer size to the host count - 3 (network address, server address, and broadcast address)
	// Exits when netblockstate is closed
	netblock := make(chan net.IP, hostcount-3)
	go runblock(netblock, statesub, ip2int(servernet.IP))

	// Channel to request contrack reports
	reportchan := make(chan chan<- Connections)

	// Track client connection lifetimes for reporting and enforcement
	// Exits when contrackstate channel is closed
	go contrack(statesub, reportchan)

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

	// Start metrics http server
	go metrics(reportchan)

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
			go s.serve(conn, tuntxchan, clientstate, bufpool, netblock)

			acceptedmetric.Inc()
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
