package main

import (
	"log"
)

func contrack(subchan chan<- ClientStateSub, reportchan <-chan chan<- Connections) {
	// Metrics to track
	delcount := clientcount.WithLabelValues("delwait")
	opencount := clientcount.WithLabelValues("open")

	log.Print("server: contrack: starting")

	contrack := make(map[string]*Client) // Like open
	deltrack := make(map[uint64]*Client) // Like close_wait

	// Channel to receive client state
	statechan := make(chan ClientState)

	// Subscribe to client state stream
	subchan <- ClientStateSub{name: "contrack", subchan: statechan}

	// TODO: Handle metrics/reporting

	// Pump the statechan for changes in client state and update our contrack tables
	for {
		select {
		case state, ok := <-statechan:
			if !ok {
				log.Print("server: contrack(term): statechan closed")
				return
			}

			if state.transition == Connect {
				// See if we have any existing client connections with this name
				other, ok := contrack[state.client.name]
				if ok {
					enforcecount.Inc()
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

					delcount.Inc()
				} else {
					opencount.Inc()
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

					delcount.Dec()
				} else {
					// If there is an existing contrack entry
					if client, ok := contrack[state.client.name]; ok && client.id == state.client.id {
						// With the same connection id
						log.Printf("server: contrack: closed last open for %s-%#X", state.client.name, state.client.id)
						// Remove the client from the connection tracking list
						delete(contrack, state.client.name)
						opencount.Dec()
					} else {
						log.Printf("server: contrack(perm): got disconnect with zero tracking matches %s-%#X", state.client.name, state.client.id)
						panic("zero tracking matches")
					}
				}
			} else {
				log.Printf("server: contrack(perm): unhandled client transition: %d", state.transition)
				panic("unhandled client state transition")
			}

		// Bundle up our connection info and send it over
		case req := <-reportchan:
			// Collection of report connections
			var cons Connections

			// Report active connections
			for _, v := range contrack {
				cons = append(cons, Connection{
					Time:     v.connected,
					Name:     v.name,
					IP:       v.ip.String(),
					PublicIP: v.publicip.String(),
					Pending:  false,
				})
			}

			// Report delwait connections
			for _, v := range deltrack {
				cons = append(cons, Connection{
					Time:     v.connected,
					Name:     v.name,
					IP:       v.ip.String(),
					PublicIP: v.publicip.String(),
					Pending:  true,
				})
			}

			// Send the list to the delivered channel
			req <- cons
		}
	}
}
