package main

import (
	"log"
	"net"
)

func runblock(netblock chan<- net.IP, subchan chan<- ClientStateSub, netip uint32) {
	// Metrics to track
	alloccount := netblock_usemetric.WithLabelValues("allocated")
	freecount := netblock_usemetric.WithLabelValues("free")

	// Channel to receive client state
	statechan := make(chan ClientState)

	// Subscribe to client state stream
	subchan <- ClientStateSub{name: "netblock", subchan: statechan}

	log.Printf("server: netblock: starting with %d host addresses", cap(netblock))

	// Set the current free count metric
	freecount.Set(float64(cap(netblock)))

	// Pump the netblock channel full of available addresses
	// Excluding network, gateway, and broadcast
	for i := uint32(1); i <= uint32(cap(netblock)); i++ {
		netblock <- int2ip(netip + i)
	}

	log.Print("server: netblock: starting main loop")
	for state := range statechan {
		if state.transition == Connect {
			// Record metrics
			alloccount.Inc()
			freecount.Dec()

			log.Printf("server: netblock: allocated ip %s, %d unallocated ips remain", state.client.ip, len(netblock))
		} else if state.transition == Disconnect {
			// Record metrics
			alloccount.Dec()
			freecount.Inc()

			netblock <- state.client.ip
			log.Printf("server: netblock: recovered ip %s, %d unallocated ips remain", state.client.ip, len(netblock))
		}
	}
	log.Print("server: netblock(term): statechan closed")
}
