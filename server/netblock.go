package main

import (
	"log"
	"net"
	"time"
)

func runblock(blockchan chan<- net.IP, subchan chan<- ClientStateSub, hostcount uint32, netip uint32) {
	// Requests to the netblock chan will stop working if the statechan loop exits
	netblock := make(chan net.IP, hostcount-3)
	defer close(netblock)

	// Metrics to track
	alloccount := ipusecount.WithLabelValues("allocated")
	freecount := ipusecount.WithLabelValues("free")

	// Channel to receive client state
	statechan := make(chan ClientState)

	// Subscribe to client state stream
	subchan <- ClientStateSub{name: "netblock", subchan: statechan}

	log.Printf("server: netblock: starting with %d host addresses", hostcount-3)

	// Pump the netblock channel full of available addresses
	// Excluding network, gateway, and broadcast
	for i := uint32(1); i < hostcount-2; i++ {
		netblock <- int2ip(netip + i)
	}

	// Set the current free count metric
	freecount.Set(float64(len(netblock)))

	// Interposer to count allocations
	go func() {
		defer close(blockchan)
		for ip := range netblock {
			blockchan <- ip
			alloccount.Inc()
			freecount.Dec()
		}
	}()

	log.Print("server: netblock: starting main loop")
	for state := range statechan {
		// When a client disconnects, add its IP back to the block
		if state.transition == Disconnect {
			select {
			case netblock <- state.client.ip:
				log.Printf("server: netblock: recovered ip %s, %d unallocated ips remain", state.client.ip, len(netblock))
				freecount.Inc()
				alloccount.Dec()

			case <-time.After(1 * time.Second):
				log.Printf("server: netblock(perm): timed out writing %s, %d unallocated ips remain", state.client.ip, len(netblock))
				panic("timed out writing to netblock")
			}
		}
	}
	log.Print("server: netblock(term): statechan closed")
}
