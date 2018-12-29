package main

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/songgao/water"
	"github.com/vishvananda/netlink"
)

const (
	MTU = 1300
)

/**
* See service.go for main vpn service loop guts
* See clienthandler.go for vpn service client handler cogs/gears
* See client.go for client data structs/defines
* See various files for the goroutine functions the sprout up
 */

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
		CipherSuites:             []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256, tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256},
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
	// Set tun adapter settings and turn it up
	nlhand, _ := netlink.NewHandle()
	tunlink, _ := netlink.LinkByName("tun_govpn")
	ipnet, _ := netlink.ParseAddr("192.168.0.1/21")
	netlink.AddrAdd(tunlink, ipnet)
	nlhand.LinkSetMTU(tunlink, 1300)
	nlhand.LinkSetUp(tunlink)

	// Waitgroup for waiting on main services to stop
	mainwait := &sync.WaitGroup{}

	// Producer that reads packets off of the tun interface and pushes them on the tunrx channel
	// Packets put on the tunrx channel are read by the data router that decides where to send them
	// If this stops pumping then client handler writes to the tuntx channel will stall
	// TODO: Read does not end when tun interface is closed, hacking to let process termination close this routine, remember to uncomment wait.Done in tunrx impl
	//mainwait.Add(1) // hacked out because read does not end (see above todo), process termination does
	tunrxchan := make(chan []byte)
	go tunrx(iface, tunrxchan, mainwait)

	// Consumer that reads packets off of the tuntx channel and writes them to the tun interface
	// Any packet received on the client tls socket is written to the tuntx channel by a goroutine in `serve()`
	// Exits when txchan is closed
	mainwait.Add(1)
	tuntxchan := make(chan []byte)
	go tuntx(tuntxchan, iface, mainwait)

	// Listen on tcp:443
	// TODO: Get from config
	listener, err := tls.Listen("tcp", ":443", tlsconfig)
	if err != nil {
		log.Fatalf("server: listen failed: %s", err)
	}
	log.Print("server: listening on 443")

	// Create an instance of the VPN server service
	// Run it 5 times with the active listener to accept connections, tun channels for tun comms, and server network info
	service := NewService()
	go service.Serve(listener, tuntxchan, tunrxchan, servernet)

	// Handle SIGINT and SIGTERM
	sigs := make(chan os.Signal)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// Block waiting for a signal
	log.Println(<-sigs)

	// Stop the service and disconnect clients gracefully
	log.Print("server: stopping")
	service.Stop()

	// Close the tun tx channel
	log.Print("server: closing tuntx channel")
	close(tuntxchan)

	// Close the tun interface
	log.Print("server: closing tun interface")
	iface.Close()

	// Wait for main pumps to stop
	log.Print("server: waiting for main loops")
	mainwait.Wait()
}
