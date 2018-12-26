package main

import (
	"log"
	"net"
	"sync"
)

func acceptor(listener net.Listener, connhandler chan<- net.Conn, wait *sync.WaitGroup) {
	// Exit the wait group when the accept pump exits
	defer wait.Done()
	defer close(connhandler)

	log.Print("server: acceptor: starting")

	for {
		// Block waiting for a client connection
		// ends on deferred listener.Close()
		conn, err := listener.Accept()
		if nil != err {
			// log the error and leave the accept loop
			log.Printf("server: acceptor: accept failed: %s", err)
			return
		}

		// Send the connection for handling
		connhandler <- conn
	}
}
