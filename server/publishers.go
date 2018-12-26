package main

import (
	"log"
)

func publishstate(statechan <-chan ClientState, subchans [](chan<- ClientState)) {

	log.Print("statepublisher: starting")

	// Defer closing each of the subscriber channels
	for _, sub := range subchans {
		defer close(sub)
	}

	// Pump the source into the subscribers until it closes
	for state := range statechan {
		for _, sub := range subchans {
			sub <- state
		}
	}
}
