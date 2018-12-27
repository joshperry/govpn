package main

import (
	"log"
)

func contrack(subchan chan<- ClientStateSub) {

	log.Print("server: contrack: starting")

	contrack := make(map[string]*Client) // Like open
	deltrack := make(map[uint64]*Client) // Like close_wait

	// Channel to receive client state
	statechan := make(chan ClientState)

	// Subscribe to client state stream
	subchan <- ClientStateSub{name: "contrack", subchan: statechan}

	// TODO: Handle metrics/reporting

	// Pump the statechan for changes in client state and update our contrack tables
	for state := range statechan {
		if state.transition == Connect {
			// See if we have any existing client connections with this name
			other, ok := contrack[state.client.name]
			if ok {
				/* this does not work under concurrency yet, other client can be disconnected before we send this even with disconnected check
				// Enforce single connection per client by disconnecting any existing connections for the client name
				if (other.disconnected == time.Time{}) {
					// Only send if it isn't disconnected already
					log.Printf("server: contrack: enforce disconnect on %s-%#X", other.name, other.id)
					other.control <- "disconnect"
				}
				*/
				// Save the disconnecting client into the deltrack list to await its final goodbye
				log.Printf("server: contrack: saving to deltrack %s-%#X", other.name, other.id)
				deltrack[other.id] = other
			}

			log.Printf("server: contrack: tracking %s-%#X", state.client.name, state.client.id)
			contrack[state.client.name] = state.client

		} else if state.transition == Disconnect {
			// When a client disconnects reap the client lists
			if _, ok := deltrack[state.client.id]; ok {
				log.Printf("server: contrack: deltrack closed %s-%#X", state.client.name, state.client.id)
				// If we are already waiting for disconnection
				// just remove it from the deltrack list
				delete(deltrack, state.client.id)
			} else {
				// If there is an existing contrack entry
				if client, ok := contrack[state.client.name]; ok && client.id == state.client.id {
					// With the same connection id
					log.Printf("server: contrack: closed last open for %s-%#X", state.client.name, state.client.id)
					// Remove the client from the connection tracking list
					delete(contrack, state.client.name)
				} else {
					log.Printf("server: contrack(perm): got disconnect with zero tracking matches %s-%#X", state.client.name, state.client.id)
					panic("zero tracking matches")
				}
			}
		} else {
			log.Printf("server: contrack(perm): unhandled client transition: %d", state.transition)
			panic("unhandled client state transition")
		}
	}
}
