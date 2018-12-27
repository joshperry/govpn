package main

import (
	"log"
	"time"
)

type ClientStateSub struct {
	name    string
	subchan chan<- ClientState
}

func publishstate(statechan <-chan ClientState, subchan <-chan ClientStateSub) {
	var subchans []ClientStateSub

	log.Print("statepublisher: starting")

	for {
		// Pump the source into the subscribers until it closes
		select {
		case state, ok := <-statechan:
			if !ok {
				log.Print("statepublisher(term): statechan closed")
				return
			}

			log.Printf("statepublisher: got %d message to publish", state.transition)
			for _, sub := range subchans {
				select {
				case sub.subchan <- state:
					log.Printf("statepublisher: published to %s", sub.name)
				case <-time.After(3 * time.Second):
					log.Printf("statepublisher(panic): timed out sending to %s", sub.name)
					panic("timed out sending")
				}
			}

		case sub := <-subchan:
			log.Printf("statepublisher: subscriber %s", sub.name)
			subchans = append(subchans, sub)
			defer close(sub.subchan)
		}
	}
}
