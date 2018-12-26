package main

import (
	"log"
)

func contrack(statechan <-chan ClientState) {

	log.Print("server: contrack: starting")

	contrack := make(map[string]*Client) // Like open
	deltrack := make(map[int64]*Client)  // Like close_wait
	// TODO: Handle metrics/reporting

	// Pump the statechan for changes in client state and update our contrack tables
	for state := range statechan {
		if state.transition == Connect {
			// See if we have any existing client connections with this name
			other, ok := contrack[state.client.name]
			if ok {
				// Enforce single connection per client by disconnecting any existing connections for the client name
				other.control <- "disconnect"
				// Save the disconnecting client into the deltrack list to await its final goodbye
				deltrack[state.client.id] = contrack[state.client.name]
			}

			contrack[state.client.name] = state.client

		} else if state.transition == Disconnect {
			// When a client disconnects reap the client lists
			if _, ok := deltrack[state.client.id]; ok {
				// If we are already waiting for disconnection
				// just remove it from the deltrack list
				delete(deltrack, state.client.id)
			} else {
				// If there is an existing contrack entry
				if client, ok := contrack[state.client.name]; ok {
					// With the same connection id
					if client.id == state.client.id {
						// Remove the client from the connection tracking list
						delete(contrack, state.client.name)
					} else {
						log.Print("server: contrack: space oddity, no matching client for disconnect")
					}
				}
			}
		} else {
			log.Printf("server: contrack: unhandled client transition: %d", state.transition)
		}
	}
}
