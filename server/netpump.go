package main

import (
	"log"
	"net"
	"sync"

	"github.com/songgao/water"
)

func connrx(conn net.Conn, rxchan chan<- []byte, wait *sync.WaitGroup) {
	// Leave the wait group when the read pump exits
	defer wait.Done()
	defer close(rxchan)

	log.Print("server: connrx: starting")

	// Forever read
	buf := make([]byte, MTU)
	for {
		log.Print("server: connrx: waiting")
		// This ends when the connection is closed locally or remotely
		n, err := conn.Read(buf)
		if nil != err {
			// Read failed, pumpexit the handler
			log.Printf("server: connrx(term): error while reading: %s", err)
			return
		}

		if n == 0 {
			log.Print("server: connrx(term): remote client closed connection")
			return
		}

		// Send the packet to the rx channel
		rxchan <- buf[:n]
	}
}

func conntx(txchan <-chan []byte, conn net.Conn, writeerr chan<- bool, wait *sync.WaitGroup) {
	// Signal shutdown waitgroup that we're done
	defer wait.Done()

	log.Print("server: conntx: starting")

	// A simple one-shot flag is used to skip the write after failure
	failed := false
	// Pump the transmit channel until it is closed
	for buf := range txchan {
		log.Print("server: conntx: sending packet to client")

		if !failed {
			//TODO: Any processing on packet from tun adapter

			n, err := conn.Write(buf)
			log.Printf("server: conntx: wrote %d bytes", n)
			if err != nil {
				log.Printf("server: conntx(term): error while writing: %s", err)
				// If the write errors, signal the rwerr channel
				writeerr <- true
				failed = true
			}
		}
	}
}

func tunrx(tun *water.Interface, rxchan chan<- []byte, wait *sync.WaitGroup) {
	// Close channel when read loop ends to signal end of traffic
	// Used by client data router to know when to stop reading
	defer close(rxchan)
	defer wait.Done()

	log.Print("server: tunrx: starting")

	tunbuf := make([]byte, MTU)
	for {
		n, err := tun.Read(tunbuf)
		log.Printf("server: tunrx: read %d bytes", n)
		if err != nil {
			// Stop pumping if read returns error
			log.Printf("server: tunrx(term): error reading %s", err)
			return
		}
		rxchan <- tunbuf[n:]
	}
}

func tuntx(txchan <-chan []byte, tun *water.Interface, wait *sync.WaitGroup) {
	defer wait.Done()

	log.Print("server: tuntx: starting")

	// Read the channel until it is closed
	for tunbuf := range txchan {
		// Write the buffer to the tun interface
		n, err := tun.Write(tunbuf)
		log.Printf("server: tuntx: wrote %d bytes", n)
		if err != nil {
			log.Printf("server: tuntx(term): error writing %s", err)
			return
		}
	}
	log.Print("server: tuntx(term): txchan closed")
}
