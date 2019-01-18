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
	"sync"
	"time"
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
func (s *Service) serve(conn net.Conn, tun chan<- *message, clientstate chan<- ClientState, bufpool *sync.Pool, netblock <-chan net.IP) {
	defer func() {
		// Close connection when handler exits
		conn.Close()
		// Leave the shutdown group when handler exits
		s.clientGroup.Done()
		log.Print("client(perm): auf wiedersehen")
	}()

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
		log.Printf("server: conn(%#x): %s", uint64(*pid), s)
	}

	cprintf := func(s string, args ...interface{}) {
		log.Printf("server: conn(%#x): %s", uint64(*pid), fmt.Sprintf(s, args...))
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

		//Send HTTP 403 response
		conn.Write([]byte("HTTP/1.0 403 FORBIDDEN\n\n"))
		return
	}
	client.id = id

	// Application-Layer Handshake
	// Read first packet from client
	// This is ugly because we're not in channel-land yet
	{
		// Create buffered reader for connection
		bufrx := bufio.NewReader(conn)

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

		// TODO: Validate client info

		// Allocate client IP address
		client.ip = <-netblock
		client.intip = ip2int(client.ip)

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
		conn.Write([]byte(fmt.Sprintf("Content-Length: %d\n\n", len(settingsbuf))))

		// Write the settings buffer
		n, err = conn.Write(settingsbuf)
		cprintf("sent client settings bytes: %d", n)
		if err != nil {
			cprintf("(term): error sending client settings: %s", err)
			return
		}
		cprint(string(settingsbuf))

		// Ensure the buffered reader isn't holding extra data
		if bufrx.Buffered() != 0 {
			panic("Didn't read all buffered bytes")
		}
	}

	// Enter channel-land! Ye blessed routine

	// Defer client cleanup to when leaving the handler
	defer func() {
		// Record disconnect time in client
		client.disconnected = time.Now()
		client_disconnectmetric.Inc()

		cprint("sending disconnect client state")

		// Send disconnect client state change
		clientstate <- ClientState{
			happened:   time.Now(),
			transition: Disconnect,
			client:     client,
		}
	}()

	// Increment connect count metric here
	client_connectmetric.Inc()

	// Send client connect state change
	// This causes the client.tx channel to be mounted by the tun router and it will now receieve traffic
	clientstate <- ClientState{
		happened:   time.Now(),
		transition: Connect,
		client:     client,
	}

	// Producer that pumps the read-side of the client connection into the vpn tun adapter
	// Exits on failing read after deferred conn.Close() or client disconnect
	readerr := make(chan bool)
	s.clientGroup.Add(1)
	go connrx(conn, tun, client.intip, readerr, s.clientGroup, bufpool)

	// Consumer that pumps messages from the router into the client connection
	writeerr := make(chan bool)
	s.clientGroup.Add(1)
	go conntx(client.tx, conn, writeerr, s.clientGroup, bufpool)

	cprint("client connection established")

	// Forever select on the done channel, the rwerr channel, the clientrx read producer channel, and the control channel
	// until a read or write operation fails, the done signal is received, or a control command terminates the connection
	for {
		select {
		// Disconnect if we're told to shut down shop
		case <-s.done:
			cprint("(term): got done signal")
			return

		case <-readerr:
			cprint("(term): encountered client read error")
			return

		case <-writeerr:
			cprint("(term): encountered client write error")
			return

		// Handle client control messages
		case _, ok := <-client.control:
			// Leave the loop if we are to disconnect
			if !ok {
				cprint("(term): received disconnect control")
				return
			}
		}
	}
}
