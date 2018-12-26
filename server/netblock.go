package main

import (
	"log"
	"net"
)

func runblock(blockchan chan<- net.IP, statechan <-chan ClientState, hostcount uint32, netip uint32) {
	// Requests to the netblock chan will stop working if the statechan loop exits
	defer close(blockchan)

	log.Printf("server: netblock: starting with %d host addresses", hostcount-3)

	// Pump the netblock channel full of available addresses
	for i := uint32(2); i < hostcount; i++ {
		blockchan <- int2ip(netip + i)
	}

	for state := range statechan {
		// When a client disconnects, add its IP back to the block
		if state.transition == Disconnect {
			log.Printf("server: netblock: recovered ip %s, %d unallocated ips remain", state.client.ip, len(blockchan))
			blockchan <- state.client.ip
		}
	}
	log.Print("server: netblock(term): statechan closed")
}
