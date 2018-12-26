package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io/ioutil"
	"log"
	"math"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/songgao/water"
	"golang.org/x/net/ipv4"
)

const (
	MTU = 1300
)

// The VPN server service
type Service struct {
	done          chan bool       // A channel to signal shutdown of the service
	shutdownGroup *sync.WaitGroup // A waitgroup to syncronize graceful shutdown
}

// Make a new Service
func NewService() *Service {
	s := &Service{
		done:          make(chan bool),
		shutdownGroup: &sync.WaitGroup{},
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
	// Close the listener when the server stops
	defer listener.Close()

	// Channel to send new tls connections to
	connchan := make(chan net.Conn)

	// Channel to send client connection state changes to
	clientstate := make(chan ClientState)
	// No more client states are sent when the server exits
	// signals the router, contrack, and netblock to end
	defer close(clientstate)

	// Goroutine to pump the accept loop into a handler channel
	// Exits when Accept fails on deferred listener.Close()
	go acceptor(listener, connchan, s.shutdownGroup)

	// Route packets bound for clients as they come in the tunrx channel
	// Uses clientstate channel to keep internal routing table up to date
	// Exits when tunrx or clientstate channels are closed
	routestate := make(chan ClientState)
	go route(tunrx, routestate)

	// Calculate netblock info
	netmasklen, networksize := servernet.Mask.Size()
	hostcount := uint32(math.Pow(2, float64(networksize-netmasklen)))

	// Implements a channel that delivers unused IP addresses when read
	// And returns IPs to the pool when a client disconnects
	// Set the buffer size to the host count - 3 (network address, server address, and broadcast address)
	// Exits when netblockstate is closed
	netblock := make(chan net.IP, hostcount-3)
	netblockstate := make(chan ClientState)
	go runblock(netblock, netblockstate, hostcount, ip2int(servernet.IP))

	// Track client connection lifetimes for reporting and enforcement
	// Exits when contrackstate channel is closed
	contrackstate := make(chan ClientState)
	go contrack(contrackstate)

	// Publish clientstate
	// Exits when clientstate closes
	go publishstate(clientstate, []chan<- ClientState{routestate, contrackstate, netblockstate})

	// Forever select on the done channel, and the client connection handler channel
	for {
		select {
		case <-s.done:
			log.Print("server: got done signal", listener.Addr())
			return

		case conn := <-connchan:
			log.Print("server:", conn.RemoteAddr(), "connected")
			// Add a client to the waitgroup, and handle it in a goroutine
			s.shutdownGroup.Add(1)
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

// Client handler function for :443
func (s *Service) serve(conn net.Conn, tuntx chan<- []byte, clientstate chan<- ClientState, netblock <-chan net.IP) {
	// Leave the shutdown group when handler exits
	defer s.shutdownGroup.Done()

	// Close connection when handler exits
	defer conn.Close()

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

func main() {
	log.SetFlags(log.Lshortfile)

	// Load the server's PKI keypair
	// TODO: Load from config
	cer, err := tls.LoadX509KeyPair("server.crt", "server.key")
	if err != nil {
		log.Fatalf("server: failed to load server PKI material: %s", err)
	}

	// Load client CA cert chain
	certpool := x509.NewCertPool()
	// TODO: Load from config
	pem, err := ioutil.ReadFile("ca.pem")
	if err != nil {
		log.Fatalf("server: failed to read client certificate authority: %v", err)
	}
	if !certpool.AppendCertsFromPEM(pem) {
		log.Fatalf("server: failed to parse client certificate authority")
	}

	// Create tls config with PKI material
	// TODO: Can this handle a client CRL?
	// TODO: Load from config
	tlsconfig := &tls.Config{
		Certificates:             []tls.Certificate{cer},
		MinVersion:               tls.VersionTLS12,
		CurvePreferences:         []tls.CurveID{tls.X25519, tls.CurveP384, tls.CurveP256}, // Last two for browser compat?
		PreferServerCipherSuites: true,
		CipherSuites:             []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256},
		ClientAuth:               tls.VerifyClientCertIfGiven,
		ClientCAs:                certpool,
	}
	tlsconfig.BuildNameToCertificate()

	// TODO: Get from config
	serverip, servernet, _ := net.ParseCIDR("192.168.0.1/21")

	// Create tun interface
	tunconfig := water.Config{DeviceType: water.TUN}
	tunconfig.Name = "tun_govpn"
	iface, err := water.New(tunconfig)
	if nil != err {
		log.Fatalln("server: unable to allocate TUN interface:", err)
	}

	// TODO: Set tun adapter address
	log.Printf("server: setting TUN adapter address to %s", serverip)

	// Waitgroup for waiting on main services to stop
	mainwait := &sync.WaitGroup{}

	// Producer that reads packets off of the tun interface and pushes them on the tunrx channel
	// Packets put on the tunrx channel are read by the data router that decides where to send them
	// If this stops pumping then client handler writes to the tuntx channel will stall
	// TODO: Read ends when tun interface is closed/stopped?
	mainwait.Add(1)
	tunrxchan := make(chan []byte)
	go tunrx(iface, tunrxchan, mainwait)

	// Consumer that reads packets off of the tuntx channel and writes them to the tun interface
	// Any packet received on the client tls socket is written to the tuntx channel by a goroutine in `serve()`
	// TODO: Exits on write failure when tun interface is closed/stopped or when txchan is closed?
	mainwait.Add(1)
	tuntxchan := make(chan []byte)
	go tuntx(tuntxchan, iface, mainwait)

	// Listen on tcp:443
	listener, err := tls.Listen("tcp", ":443", tlsconfig)
	if err != nil {
		log.Fatalf("server: listen failed: %s", err)
	}

	// Create an instance of the VPN server service
	// Hand it the active listener to accept connections in a goroutine
	service := NewService()
	go service.Serve(listener, tuntxchan, tunrxchan, servernet)

	// Handle SIGINT and SIGTERM
	sigs := make(chan os.Signal)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// Block waiting for a signal
	log.Println(<-sigs)

	// Stop the service and disconnect clients gracefully
	service.Stop()

	// Close the tun tx channel
	log.Print("server: tuntx: closing tuntx channel")
	close(tuntxchan)

	// Close the tun interface
	iface.Close()

	// Wait for main pumps to stop
	mainwait.Wait()
}
