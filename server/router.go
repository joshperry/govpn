package main

import (
	"log"

	drw "github.com/jonhoo/drwmutex"
)

type routecache struct {
	drw.DRWMutex
	routes map[uint32]filterstack
}

func newRouteCache() *routecache {
	return &routecache{
		DRWMutex: drw.New(),
		routes:   make(map[uint32]filterstack),
	}
}

// Keeps the route cache updated from client state events
func route(cache *routecache, subchan chan<- ClientStateSub) {
	log.Print("server: router: starting")

	// Channel to receive client state
	statechan := make(chan ClientState)

	// Subscribe to client state stream
	subchan <- ClientStateSub{name: "router", subchan: statechan}

	// State messages update the routes map state
	// Data routing consumes the routes map state
	for state := range statechan {
		// uint32 keys are used for the route map
		ipint := ip2int(state.client.ip)
		if state.transition == Connect {
			log.Printf("server: route: got client connect %s %s-%#x", state.client.ip, state.client.name, state.client.id)
			// Add an item to the routing table
			cache.Lock() // write lock
			cache.routes[ipint] = state.client.txstack
			cache.Unlock()
		} else if state.transition == Disconnect {
			log.Printf("server: route: got client disconnect %s %s-%#x", state.client.ip, state.client.name, state.client.id)
			// Remove the client from the routing table and then close the client tx channel
			// Once the disconnect message is recieved, the client handler has exited
			if _, ok := cache.routes[ipint]; ok { // no read lock because we're the writer
				cache.Lock() // write lock
				delete(cache.routes, ipint)
				cache.Unlock()
			} else {
				// Didn't find a connection in the routes for this client... shouldn't happen
				log.Printf("server: route(perm): close no open connection %s %s-%#x", state.client.ip, state.client.name, state.client.id)
				panic("close no open connection")
			}
		} else {
			log.Printf("server: route(perm): unhandled client transition state: %d", state.transition)
			panic("unhandled client transition state")
		}
	}

	log.Print("server: route(term): statechan closed")
}
