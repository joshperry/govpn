package main

import (
	"log"
	"net"
	"time"
)

func runblock(blockchan chan<- net.IP, subchan chan<- ClientStateSub, hostcount uint32, netip uint32) {
	// Requests to the netblock chan will stop working if the statechan loop exits
	defer close(blockchan)

	// Channel to receive client state
	statechan := make(chan ClientState)

	// Subscribe to client state stream
	subchan <- ClientStateSub{name: "netblock", subchan: statechan}

	log.Printf("server: netblock: starting with %d host addresses", hostcount-3)

	// Pump the netblock channel full of available addresses
	for i := uint32(2); i < hostcount; i++ {
		blockchan <- int2ip(netip + i)
	}

	log.Print("server: netblock: starting main loop")
	for state := range statechan {
		// When a client disconnects, add its IP back to the block
		if state.transition == Disconnect {
			select {
			case blockchan <- state.client.ip:
				log.Printf("server: netblock: recovered ip %s, %d unallocated ips remain", state.client.ip, len(blockchan))

			case <-time.After(1 * time.Second):
				log.Printf("server: netblock(perm): timed out writing %s, %d unallocated ips remain", state.client.ip, len(blockchan))
				panic("timed out writing to netblock")
			}
		}
	}
	log.Print("server: netblock(term): statechan closed")
}
