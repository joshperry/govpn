package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log"
	"net"
	"strings"
	"time"
)

// An "enum" of the transition state
type Transition int

// Of type Transition
const (
	_ Transition = iota
	Connect
	Disconnect
)

// Info that the client sends in its first packet after connection
// encoded as json
type ClientInfo struct {
	time    time.Time
	version string
}

// Settings to send json encoded as the first packet to the client after reading
// its first packet which contains ClientInfo
type ClientSettings struct {
	time    time.Time
	version string
	ip      string
}

// Couples a transition state with the target client for delivery on the client state channel
// Any time a client changes state (connects or disconnects), a ClientState object representing the event is sent into the client state channel
// The connect message is sent in the client connection handler
// The disconnect message is sent from a deferral when the client connection handler exits
type ClientState struct {
	happened   time.Time
	transition Transition
	client     *Client
}

// Defines the state for an authenticated client connection
// Birthed in the client connection handler `func (s *Service) serve(/**/)` and used in messages sent for data route updates and ip address reaping
type Client struct {
	ip           net.IP // client tunnel ip
	id           int64  // A unique identifier for this client connection
	connected    time.Time
	disconnected time.Time
	publicip     net.IP // client public ip
	name         string // name of the authenticated client
	// A goroutine in the client connection handler reads packets from this channel and then writes them out the client tls socket
	// A goroutine in the router reads packets from the tun interface and writes any destined for this client ip, to this channel
	tx      chan []byte
	control chan string // A channel to send control messages to the client handler
}

// Creates a new Client given a tls connection
// Parses and validates the client certificate values
// Always returns (nil,error) when some step in validating the connection failed
func NewClient(tlscon *tls.Conn) (*Client, error) {
	// Grab connection state from the completed connection
	state := tlscon.ConnectionState()
	log.Print(state)

	// If client cert not provided, send back HTTP 403 response
	// TODO: Also send same error if curve preference is not met?
	if len(state.PeerCertificates) == 0 {
		return nil, errors.New("no peer cert provided")
	}

	log.Println("server: conn: client public key is:")
	for _, v := range state.PeerCertificates {
		log.Print(x509.MarshalPKIXPublicKey(v.PublicKey))
	}

	// TODO: Verify certificate parameters as vpn client and extract client name
	name := "10702AG"

	// TODO: Do we need to do anything special to get the real remote address behind loadbalancer?
	ipstring := tlscon.RemoteAddr().String()

	// Get a random connection id
	connectionid, err := randint64()
	if err != nil {
		return nil, errors.New("server: conn(term): unknown error getting random")
	}

	return &Client{
		name:      name,
		id:        connectionid,
		connected: time.Now(),
		publicip:  net.ParseIP(ipstring[0:strings.Index(ipstring, ":")]),
		tx:        make(chan []byte),
	}, nil
}