package main

import (
	"log"

	"golang.org/x/net/ipv4"
)

//====
// The outbound packet router
// Happy path packets inbound from the tun adapter on the rxchan channel, are parsed for their destination IP,
// then written to the client matching that address found in the routing table
// Internal routing table is kept in sync by reading events from the statechan channel
// Exits when statechan or rxchan are closed
func route(rxchan <-chan []byte, statechan <-chan ClientState) {

	log.Print("server: router: starting")

	// routing table state used only in this goroutine
	// routes are a mapping from client ip to a client's distinct tunrx channel
	routes := make(map[uint32]chan<- []byte)

	for {
		// serializing the state updates and state consumption in the same
		// goroutine gives lock-free operation.

		// State messages update the routes map state
		// Data routing consumes the routes map state
		select {
		case state, ok := <-statechan:
			if !ok { // If the state channel is closed, exit the loop
				log.Print("server: route(term): statechan closed")
				return
			}

			// uint32 keys are used for the route map
			ipint := ip2int(state.client.ip)
			if state.transition == Connect {
				log.Printf("server: route: got client connect %s", state.client.name)
				// Add an item to the routing table
				routes[ipint] = state.client.tx
			} else if state.transition == Disconnect {
				log.Printf("server: route: got client disconnect %s", state.client.name)
				// Remove the client from the routing table and then close the client tx channel
				// Once the disconnect message is recieved, the client handler has exited
				tx := routes[ipint]
				delete(routes, ipint)
				// We close this channel here to finally release the read side pump in response to the close
				close(tx)
			} else {
				log.Printf("server: route: unhandled client transition state: %s", state.transition)
			}

		// Route packet to appropriate client tx channel
		// Channel is closed when tun interface read loop exits (in main)
		case buf, ok := <-rxchan:
			if !ok { // If the receive channel is closed, exit the loop
				log.Print("server: route(term): rxchan closed")
				return
			}

			// Get destination IP from packet
			header, err := ipv4.ParseHeader(buf)
			if err != nil {
				// If we couldn't parse IP headers, drop the packet
				log.Printf("serverrx(dropped): could not parse packet header: %s", err)
				continue
			}

			clientip := header.Dst

			log.Printf("serverrx: got %d byte tun packet for %s", len(buf), clientip)

			// Lookup client in routing state
			if clientrx, ok := routes[ip2int(clientip)]; ok {
				// Send packet to client tunrx channel
				clientrx <- buf
			} else {
				//TODO: Send ICMP unreachable if no client found
			}
		}
	}
}
