package main

import (
	"encoding/binary"
	"log"
	"time"
)

//====
// The outbound packet router
// Happy path packets inbound from the tun adapter on the rxchan channel, are parsed for their destination IP,
// then written to the client matching that address found in the routing table
// Internal routing table is kept in sync by reading events from the statechan channel
// Exits when statechan or rxchan are closed
func route(rxchan <-chan *message, subchan chan<- ClientStateSub, bufpool chan<- *message) {

	log.Print("server: router: starting")

	// routing table state used only in this goroutine
	// routes are a mapping from client ip to a client's distinct tunrx channel
	routes := make(map[uint32]chan<- *message)

	// Channel to receive client state
	statechan := make(chan ClientState)

	// Subscribe to client state stream
	subchan <- ClientStateSub{name: "router", subchan: statechan}

	for {
		// serializing the state updates and state consumption in the same
		// uses select for mutual exclusion to modify/read state

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
				log.Printf("server: route: got client connect %s %s-%#x", state.client.ip, state.client.name, state.client.id)
				// Add an item to the routing table
				routes[ipint] = state.client.tx
			} else if state.transition == Disconnect {
				log.Printf("server: route: got client disconnect %s %s-%#x", state.client.ip, state.client.name, state.client.id)
				// Remove the client from the routing table and then close the client tx channel
				// Once the disconnect message is recieved, the client handler has exited
				if tx, ok := routes[ipint]; ok {
					delete(routes, ipint)
					// We close this channel here to finally release the read side pump in response to the close
					close(tx)
				} else {
					// Didn't find a connection in the routes for this client... shouldn't happen
					log.Printf("server: route(perm): close no open connection %s %s-%#x", state.client.ip, state.client.name, state.client.id)
					panic("close no open connection")
				}
			} else {
				log.Printf("server: route(perm): unhandled client transition state: %d", state.transition)
				panic("unhandled client transition state")
			}

		// Route packet to appropriate client tx channel
		// Channel is closed when tun interface read loop exits (in main)
		case msg, ok := <-rxchan:
			if !ok { // If the receive channel is closed, exit the loop
				log.Print("server: route(term): rxchan closed")
				bufpool <- msg // return message to pool
				return
			}
			start := time.Now()

			// Get destination IP from packet
			clientip := binary.BigEndian.Uint32(msg.packet()[16:20])

			//log.Printf("server: route: got %d byte tun packet for %s", len(buf), clientip)

			// Lookup client in routing state
			if tx, ok := routes[clientip]; ok {
				// Send packet to client tx channel
				tx <- msg
			} else {
				//TODO: Send ICMP unreachable if no client found
				log.Printf("server: route: found no client for %d byte tun packet to %s", msg.len, int2ip(clientip))
				bufpool <- msg // return message to pool
				// TODO: metricize this
			}

			route_durationmetric.Observe(float64(time.Since(start).Nanoseconds()))
			tx_packetsmetric.Inc()
			tx_bytesmetric.Add(float64(msg.len))
		}
	}
}
