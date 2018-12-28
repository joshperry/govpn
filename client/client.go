package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/textproto"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/songgao/water"
)

const (
	MTU = 1300
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

func main() {
	log.SetFlags(log.Lshortfile)

	// Load the server's PKI keypair
	// TODO: Load from config
	cer, err := tls.LoadX509KeyPair("client.crt", "client.key")
	if err != nil {
		log.Fatalf("client: failed to load server PKI material: %s", err)
	}

	// Load server CA cert chain
	certpool := x509.NewCertPool()
	// TODO: Load from config
	pem, err := ioutil.ReadFile("ca.pem")
	if err != nil {
		log.Fatalf("client: failed to read server certificate authority: %v", err)
	}
	if !certpool.AppendCertsFromPEM(pem) {
		log.Fatalf("client: failed to parse server certificate authority")
	}

	// Create tls config with PKI material
	// TODO: Load from config
	tlsconfig := &tls.Config{
		InsecureSkipVerify:       true,
		Certificates:             []tls.Certificate{cer},
		MinVersion:               tls.VersionTLS12,
		CurvePreferences:         []tls.CurveID{tls.X25519}, // Last two for browser compat?
		PreferServerCipherSuites: true,
		CipherSuites:             []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
		RootCAs:                  certpool,
	}
	tlsconfig.BuildNameToCertificate()

	// Create tun interface
	tunconfig := water.Config{DeviceType: water.TUN}
	tunconfig.Name = "tun_govpnc"
	iface, err := water.New(tunconfig)
	if nil != err {
		log.Fatalln("client: unable to allocate TUN interface:", err)
	}

	// Waitgroup for waiting on main services to stop
	mainwait := &sync.WaitGroup{}

	// Pump packets from the tun adapter into a channel
	mainwait.Add(1)
	tunrxchan := make(chan []byte)
	go tunrx(iface, tunrxchan, mainwait)

	// Pump packets from a channel into the tun adapter
	mainwait.Add(1)
	tuntxchan := make(chan []byte)
	go tuntx(tuntxchan, iface, mainwait)

	// Connect to server
	tlscon, err := tls.Dial("tcp", "localhost:443", tlsconfig)
	if nil != err {
		log.Fatalln("client: connect failed", err)
	}

	defer tlscon.Close()

	if err := tlscon.Handshake(); nil != err {
		log.Printf("client(term): tls handshake failed: %s", err)
	} else {
		log.Print("client: tls handshake succeeded")
	}

	// Create buffered reader for connection
	bufrx := bufio.NewReader(tlscon)

	// Settings we get back from the server
	var settings ClientSettings

	// Application layer handshake
	{
		// Encode client settings struct to newline delimited json and send as first packet
		infobuf, err := json.Marshal(ClientInfo{
			Time:    time.Now().UTC().Format(time.RFC3339),
			Version: "0.1.0",
		})
		if err != nil {
			log.Print("(term): error encoding client info packet")
			return
		}

		// Write http response and headers
		tlscon.Write([]byte("POST / HTTP/1.0\n"))
		tlscon.Write([]byte("Content-Type: application/json\n"))
		tlscon.Write([]byte(fmt.Sprintf("Content-Length: %d\n", len(infobuf))))
		tlscon.Write([]byte("\n"))
		tlscon.Write(infobuf)

		// Process the response
		tp := textproto.NewReader(bufrx)
		request, err := tp.ReadLine()
		if err != nil {
			log.Printf("(term): error reading request line: %s", err)
			return
		}
		log.Print(string(request))

		// Get headers
		headers, err := tp.ReadMIMEHeader()
		if err != nil {
			log.Printf("(term): error reading request headers: %s", err)
			return
		}
		log.Print("got headers")
		log.Print(headers)

		// Get body
		bodylen, err := strconv.ParseInt(headers["Content-Length"][0], 10, 64)
		if err != nil {
			log.Print("(term): error parsing content-length header")
		}

		// TODO: Protect for content too large

		body := make([]byte, bodylen)
		n, err := bufrx.Read(body)
		if err != nil {
			log.Print("(term): error reading request body")
			return
		}
		log.Print("got body")
		log.Print(string(body))

		// Decode client settings struct from json in the respnse
		if err := json.Unmarshal(body[:n], &settings); err != nil {
			log.Print("(term): error decoding client settings")
			return
		}

		if bufrx.Buffered() != 0 {
			panic("Didn't read all buffered bytes")
		}

		// TODO: Set tun adapter IP address and state
	}

	// Channel for packets coming from the server
	mainwait.Add(1)
	rx := make(chan []byte)
	go connrx(bufrx, rx, mainwait)

	// A channel to signal a write error to the server
	writeerr := make(chan bool, 2)

	// Channel for packets bound to the server
	mainwait.Add(1)
	tx := make(chan []byte)
	go conntx(tx, tlscon, writeerr, mainwait)

	go func() {
		for packet := range rx {
			tuntxchan <- packet
		}
	}()

	go func() {
		for packet := range tunrxchan {
			tx <- packet
		}
	}()

	// Handle SIGINT and SIGTERM
	sigs := make(chan os.Signal)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// Block waiting for a signal
	log.Println(<-sigs)
}
