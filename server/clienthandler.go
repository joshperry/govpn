package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/textproto"
	"strconv"
	"time"

	"github.com/davecgh/go-spew/spew"
	"golang.org/x/net/ipv4"
)

// Info that the client sends in its first packet after connection
// encoded as json
type ClientInfo struct {
	Time    string `json:"time"`
	Version string `json:"version"`
}

// Settings to send json encoded as the first packet to the client after reading
// its first packet which contains ClientInfo
type ClientSettings struct {
	Time    string `json:"time"`
	Version string `json:"version"`
	IP      string `json:"ip"`
}

// Client handler function for :443
func (s *Service) serve(conn net.Conn, tuntx chan<- []byte, clientstate chan<- ClientState, netblock <-chan net.IP) {
	// Leave the shutdown group when handler exits
	defer s.clientGroup.Done()

	// Close connection when handler exits
	defer conn.Close()

	// Metrics to track
	tlsfail := client_failmetric.WithLabelValues("tls")
	nocertfail := client_failmetric.WithLabelValues("nocert")

	// Get a random connection id
	id, err := randUint64()
	if err != nil {
		log.Print("server: conn(term): unknown error getting random")
		return
	}

	pid := &id
	cprint := func(s interface{}) {
		log.Printf("server: conn(%#X): %s", uint64(*pid), s)
	}

	cprintf := func(s string, args ...interface{}) {
		log.Printf("server: conn(%#X): %s", uint64(*pid), fmt.Sprintf(s, args...))
	}

	cprint("starting")

	// Get a TLS connection from our plain net.Conn
	tlscon, ok := conn.(*tls.Conn)
	if !ok {
		cprint("(term): not a TLS connection")
		return
	}

	// Progress to the tls handshake
	if err := tlscon.Handshake(); err != nil {
		cprintf("(term): TLS handshake failed: %s", err)
		tlsfail.Inc()
		return
	} else {
		cprint("TLS handshake completed")
	}

	// Validate this connection as a valid new client
	client, err := NewClient(tlscon)
	if err != nil {
		nocertfail.Inc()
		cprintf("(term): error validating client: %s", err)
		//TODO: Send HTTP 403 response
		//tlscon.Write()
		return
	}
	client.id = id

	// Create buffered reader for connection
	bufrx := bufio.NewReader(conn)

	// Application-Layer Handshake
	// Read first packet from client
	// This is ugly because we're not in channel-land yet
	{
		// Get headers
		tp := textproto.NewReader(bufrx)
		request, err := tp.ReadLine()
		if err != nil {
			cprintf("(term): error reading request line: %s", err)
			return
		}
		cprint(string(request))

		headers, err := tp.ReadMIMEHeader()
		if err != nil {
			cprintf("(term): error reading request headers: %s", err)
			return
		}
		cprint("got headers")
		cprint(headers)

		// Get body
		bodylen, err := strconv.ParseInt(headers["Content-Length"][0], 10, 64)
		if err != nil {
			cprint("(term): error parsing content-length header")
		}

		// TODO: Protect for content too large

		body := make([]byte, bodylen)
		n, err := bufrx.Read(body)
		if err != nil {
			cprint("(term): error reading request body")
			return
		}
		cprint("got body")
		cprint(string(body))

		// Decode client info struct from json in the first packet, delimited with newline
		var info ClientInfo
		if err := json.Unmarshal(body, &info); err != nil {
			cprint("(term): error decoding client info packet")
			return
		}

		if bufrx.Buffered() != 0 {
			panic("Didn't read all buffered bytes")
		}

		// TODO: Validate client info

		// Allocate client IP address
		client.ip = <-netblock

		// Create client settings to send
		settings := ClientSettings{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Version: "0.1.0",
			IP:      client.ip.String(),
		}

		// Encode client settings struct to newline delimited json and send as first packet
		settingsbuf, err := json.Marshal(settings)
		if err != nil {
			cprint("(term): error encoding client settings packet")
			return
		}

		// Write http response and headers
		conn.Write([]byte("HTTP/1.0 200 OK\n"))
		conn.Write([]byte("Content-Type: application/json\n"))
		conn.Write([]byte(fmt.Sprintf("Content-Length: %d\n", len(settingsbuf))))
		conn.Write([]byte("\n"))

		// Write the settings buffer
		n, err = conn.Write(settingsbuf)
		cprintf("sent client settings bytes: %d", n)
		if err != nil {
			cprintf("(term): error sending client settings: %s", err)
			return
		}
	}

	// Increment connect count metric here
	client_connectmetric.Inc()
	defer client_disconnectmetric.Inc()

	// Enter channel-land! Ye blessed routine

	// Producer that pumps the read-side of the client connection into the clientrx channel
	// Exits on failing read after deferred conn.Close() or zero-length read from client close
	s.clientGroup.Add(1)
	clientrx := make(chan []byte)
	go connrx(bufrx, clientrx, s.clientGroup)

	// A channel to signal a write error to the client
	writeerr := make(chan bool, 2)

	// Pipe that pumps packets from the client rx channel into the client connection
	// This needs to continue pumping until the txchan is closed or the router could stall
	// Exits after `close(client.tx)` by router
	s.clientGroup.Add(1)
	go conntx(client.tx, conn, writeerr, s.clientGroup)

	// Defer client cleanup to when leaving the handler
	defer func() {
		// Record disconnect time in client
		client.disconnected = time.Now()

		cprint("sending disconnect client state")

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

	cprint("entering main loop")

	// Forever select on the done channel, the rwerr channel, the clientrx read producer channel, and the control channel
	// until a read or write operation fails, the done signal is received, or a control command terminates the connection
	for {
		select {
		// Disconnect if we're told to shut down shop
		case <-s.done:
			cprint("(term): got done signal")
			return

		// Disconnect if we error writing to client
		case <-writeerr:
			cprint("(term): encountered client write error")
			return

		// Consumes packets from the clientrx channel then sends them into the tuntx channel
		case buf, ok := <-clientrx:
			if !ok {
				cprint("(term): clientrx chan closed")
				return
			}

			// Grab the packet ip header
			headers, _ := ipv4.ParseHeader(buf)

			//cprintf("received packet %s", spew.Sdump(headers))

			// Drop any packets with a source address different than the one allocated to the client
			if !headers.Src.Equal(client.ip) {
				cprintf("dropped bogon %s", spew.Sdump(headers))
				continue
			}

			// Push the received packet to the tun tx channel
			tuntx <- buf

		// Handle client control messages
		case msg := <-client.control:
			// Leave the loop if we are to disconnect
			if msg == "disconnect" {
				cprint("(term): received disconnect control")
				return
			}
		}
	}
}
