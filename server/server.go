package main

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"
	"log"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"syscall"

	sysctl "github.com/lorenzosaino/go-sysctl"
	"github.com/micro/go-micro/v2/config"
	"github.com/micro/go-micro/v2/config/source/env"
	"github.com/micro/go-micro/v2/config/source/file"
	"github.com/songgao/water"
	"github.com/vishvananda/netlink"
)

const (
	MTU = 1400
)

/**
* See service.go for main vpn service loop guts
* See clienthandler.go for vpn service client handler cogs/gears
* See client.go for client data structs/defines
* See various files for the goroutine functions the sprout up
 */

func main() {
	log.SetFlags(log.Lshortfile)

	log.Print("server: loading config")

	// Find path to config file before loading config
	// Get config path from the env
	configfile := os.Getenv("GOVPN_CONFIG_FILE")
	// A default value for the config path
	if configfile == "" {
		configfile = "config.yaml"
	}
	log.Printf("config file: %s", configfile)
	config.Load(
		// base config from file
		file.NewSource(
			file.WithPath(configfile),
		),
		// override file with env
		env.NewSource(env.WithStrippedPrefix("GOVPN")),
	)

	confmap := config.Map()
	log.Print(confmap)

	// Load the server's PKI keypair
	cer, err := tls.LoadX509KeyPair(
		config.Get("tls", "cert").String("cert.pem"),
		config.Get("tls", "key").String("key.pem"),
	)
	if err != nil {
		log.Fatalf("server: failed to load server PKI material: %s", err)
	}

	// Load client CA cert chain
	certpool := x509.NewCertPool()
	pem, err := ioutil.ReadFile(config.Get("tls", "ca").String("ca.pem"))
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

	// Parse the server address block
	servernet, _ := netlink.ParseAddr(config.Get("secnet", "netblock").String("192.168.0.1/21"))
	servernet.IP = int2ip(ip2int(servernet.IP.Mask(servernet.Mask)) + 1) // Set IP to first in the network

	// Create tun interface
	tunconfig := water.Config{DeviceType: water.TUN, PlatformSpecificParams: water.PlatformSpecificParams{MultiQueue: true}}
	tunconfig.Name = config.Get("tun", "name").String("tun_govpn")
	iface, err := water.New(tunconfig)
	if nil != err {
		log.Fatalln("server: unable to allocate TUN interface:", err)
	}

	// Set tun adapter settings and turn it up
	log.Printf("server: setting TUN adapter address to %s", servernet.IP)
	nlhand, _ := netlink.NewHandle()
	tunlink, _ := netlink.LinkByName(tunconfig.Name)
	netlink.AddrAdd(tunlink, servernet)
	nlhand.LinkSetMTU(tunlink, MTU)
	nlhand.LinkSetUp(tunlink)

	// Disable ipv6 on tun interface
	err = sysctl.Set("net.ipv6.conf.tun_govpn.disable_ipv6", "1")

	// Listen for clients
	listener, err := tls.Listen(
		"tcp",
		fmt.Sprintf(
			"%s:%d",
			config.Get("listen", "address").String("0.0.0.0"),
			config.Get("listen", "port").Int(443),
		),
		tlsconfig,
	)
	if err != nil {
		log.Fatalf("server: listen failed: %s", err)
	}
	log.Printf("server: listening on %s", listener.Addr().String())

	// Create pool of messages
	bufpool := sync.Pool{
		New: func() interface{} {
			return &message{}
		},
	}

	// Create an instance of the VPN server service
	// Run it 5 times with the active listener to accept connections, tun channels for tun comms, and server network info
	service := NewService()
	go service.Serve(listener, iface, &bufpool, servernet.IPNet)

	// Handle SIGINT and SIGTERM
	sigs := make(chan os.Signal)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	// Block waiting for a signal
	select {
	case <-sigs:
		// Stop the service and disconnect clients gracefully
		log.Print("server(term): got shutdown signal")
		service.Stop()
	case <-service.done:
		log.Print("server: saw done, waiting for shutdown")
		service.shutdownGroup.Wait()
	}

	// Close the tun interface
	log.Print("server: closing tun interface")
	iface.Close()

	log.Print("server(perm): goodbye")
}
