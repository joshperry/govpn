package main

import (
	"encoding/binary"
	"log"
	"sync"

	drw "github.com/jonhoo/drwmutex"
)

type routemap map[uint32]chan<- *message

type routecache struct {
	drw.DRWMutex
	routes routemap
}

func newRouteCache() *routecache {
	return &routecache{
		routes:   make(routemap),
		DRWMutex: drw.New(),
	}
}

// Keeps the route cache updated from client state events
func route(rxchan <-chan *message, subchan chan<- ClientStateSub, bufpool *sync.Pool, wait *sync.WaitGroup) {
	log.Print("server: router: starting")
	defer func() {
		wait.Done()
		log.Print("server: route(perm): hasta")
	}()

	// Channel to receive client state
	statechan := make(chan ClientState)

	cache := newRouteCache()

	// Subscribe to client state stream
	subchan <- ClientStateSub{name: "router", subchan: statechan}

	// State messages update the routes map state
	// Data routing consumes the routes map state
	for {
		select {
		case msg := <-rxchan:
			clientip := binary.BigEndian.Uint32(msg.packet[16:20])
			if tx, ok := cache.routes[clientip]; ok {
				if len(tx) == cap(tx) {
					bufpool.Put(msg)
					//log.Printf("router(drop): max queue length for client %s", int2ip(clientip))
				} else {
					// Write the message to the client
					tx <- msg
				}
			} else {
				bufpool.Put(msg)
				log.Printf("router(drop): no route for client %s", int2ip(clientip))
			}

		case state, ok := <-statechan:
			if !ok {
				log.Print("server: router: statechan closed")
				return
			}

			// uint32 keys are used for the route map
			ipint := ip2int(state.client.ip)
			if state.transition == Connect {
				log.Printf("server: route: got client connect %s %s-%#x", state.client.ip, state.client.name, state.client.id)
				// Add an item to the routing table
				cache.routes[ipint] = state.client.tx
			} else if state.transition == Disconnect {
				log.Printf("server: route: got client disconnect %s %s-%#x", state.client.ip, state.client.name, state.client.id)
				// Remove the client from the routing table and then close the client tx channel
				// Once the disconnect message is recieved, the client handler has exited
				if _, ok := cache.routes[ipint]; ok { // no read lock because we're the writer
					delete(cache.routes, ipint)
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
	}
}
