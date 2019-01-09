package main

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

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

	// Create pool of messages
	bufpool := sync.Pool{
		New: func() interface{} {
			return &message{}
		},
	}

	// Connect to server
	tlscon, err := tls.Dial("tcp", "vpnserver:443", tlsconfig)
	if nil != err {
		log.Fatalln("client: connect failed", err)
	}

	// Filter stack for sending packets to the tun iface
	tuntxstack := filterstack{tuntx(iface)}

	done := make(chan bool)
	go service(tlscon, tuntxstack, &bufpool, done, mainwait)

	// Wait until the handshake goes well
	_, ok := <-done

	// If done was closed then there was an error negotiating the client
	if ok {
		// Put the conntx filter at the end of the tunrx stack
		tunrxstack := filterstack{conntx(tlscon)}

		// Pump packets from the tun adapter into a channel
		// mainwait.Add(1) // Not used for now because closing the tun interface doesn't break the read
		go tunrx(iface, tunrxstack, mainwait, &bufpool)

		// Handle SIGINT and SIGTERM
		sigs := make(chan os.Signal)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

		select {
		case sig := <-sigs:
			log.Printf("client(term): got signal %s", sig)
			close(done)
		case <-done:
		}

		log.Print("client: waiting for shutdown")
		mainwait.Wait()
	}
}
