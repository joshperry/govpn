package main

import (
	"encoding/binary"
	"encoding/json"
	"errors"
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
	defer s.shutdownGroup.Done()
	// Close the listener when the server stops
	defer listener.Close()

	routecache := newRouteCache()
	statesub := make(chan ClientStateSub)

	// Keeps the route info updated from the client state chan
	go route(routecache, statesub)

	rxstack := filterstack{
		// Router logic
		func(msg *message, stack filterstack) error {
			// Get destination IP from packet
			clientip := binary.BigEndian.Uint32(msg.packet[16:20])

			// Get the client route
			lock := routecache.RLock() // read lock
			clientstack, ok := routecache.routes[clientip]
			lock.Unlock()

			// Record metrics
			//tx_packetsmetric.Inc()
			//tx_bytesmetric.Add(float64(msg.len))

			// Send the message to the client stack
			if ok {
				return clientstack.next(msg)
			} else {
				return errors.New("no client route found")
			}
		},
	}

	for _ = range [20]int{} {
		//s.clientGroup.Add(1) // hacked out because read does not end (see above todo), process termination does
		go tunrx(tun, rxstack, s.clientGroup, bufpool)
	}

	// Start up multiple readers with separate queues
	for _ = range [7]int{} {
		tunconfig := water.Config{DeviceType: water.TUN, PlatformSpecificParams: water.PlatformSpecificParams{MultiQueue: true}}
		tunconfig.Name = config.Get("tun", "name").String("tun_govpn")
		iface, err := water.New(tunconfig)
		if nil != err {
			log.Fatalln("server: unable to allocate TUN interface:", err)
		}
		defer iface.Close()

		// Multiple pumps per tun queue
		for _ = range [20]int{} {
			// Producer that reads packets off of the tun interface delivers them to the appropriate client
			// Packet source IP is used to query the route cache to find the client
			// TODO: Read does not end when tun interface is closed, hacking to let process termination close this routine, remember to uncomment wait.Done in tunrx impl
			//s.clientGroup.Add(1) // hacked out because read does not end (see above todo), process termination does
			go tunrx(iface, rxstack, s.clientGroup, bufpool)
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

	// Set up the tx filter stack
	txstack := filterstack{tuntx(tun)}

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
			go s.serve(conn, txstack, bufpool, clientstate, netblock)
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
