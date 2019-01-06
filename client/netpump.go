package main

import (
	"bufio"
	"encoding/binary"
	"log"
	"net"
	"sync"

	"github.com/songgao/water"
)

func connrx(rdr *bufio.Reader, rxchan chan<- []byte, wait *sync.WaitGroup) {
	// Leave the wait group when the read pump exits
	defer wait.Done()
	defer func() {
		log.Print("connrx: closing rxchan")
		close(rxchan)
	}()

	log.Print("connrx: starting")

	// Forever read
	header := make([]byte, 4)
	for {
		//log.Print("connrx: waiting")
		// This ends when the connection is closed locally or remotely
		// Read int header
		n, err := rdr.Read(header)
		if nil != err {
			// Read failed, pumpexit the handler
			log.Printf("connrx(term): error while reading header: %s", err)
			return
		} else if n < len(header) {
			log.Print("connrx(term): short read")
			return
		}

		// Get the packet length as an int
		packetlen := binary.BigEndian.Uint32(header)
		if packetlen > MTU {
			log.Printf("connrx(term): packetlen %d MTU too small or lost framing sync", packetlen)
			return
		}

		buf := make([]byte, MTU) // TODO: Do same size allocations help with GC?
		packet := buf[:packetlen]

		// Read packet
		//log.Printf("connrx: reading %d byte packet", packetlen)
		n, err = rdr.Read(packet)
		if nil != err {
			// Read failed, pumpexit the handler
			log.Printf("connrx(term): error while reading packet: %s", err)
			return
		} else if n < len(packet) {
			log.Print("connrx(term): short read")
			return
		}

		// Send the packet to the rx channel
		rxchan <- packet
	}
}

func conntx(txchan <-chan []byte, conn net.Conn, writeerr chan<- bool, wait *sync.WaitGroup) {
	// Signal shutdown waitgroup that we're done
	defer wait.Done()

	log.Print("conntx: starting")

	// A simple one-shot flag is used to skip the write after failure
	failed := false

	headerbuf := make([]byte, 4)
	// Pump the transmit channel until it is closed
	for buf := range txchan {
		//log.Print("conntx: sending packet")

		if !failed {
			//TODO: Any processing on packet from tun adapter

			// packet length header
			binary.BigEndian.PutUint32(headerbuf, uint32(len(buf)))

			// Write packet
			n, err := conn.Write(append(headerbuf, buf...))
			//log.Printf("conntx: wrote %d bytes", n)
			if err != nil {
				log.Printf("conntx(term): error while writing: %s", err)
				// If the write errors, signal the rwerr channel
				writeerr <- true
				failed = true
			} else if n < len(buf)+4 {
				log.Print("conntx(term): short write")
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
	//defer wait.Done() // skipped for now since tun.Close() does not kill the sleepinig read, see tunrx callsite for more

	log.Print("tunrx: starting")

	for {
		tunbuf := make([]byte, MTU)
		n, err := tun.Read(tunbuf)

		//log.Printf("tunrx: got %d byte packet", n)

		if err != nil {
			// Stop pumping if read returns error
			log.Printf("tunrx(term): error reading %s", err)
			return
		}

		rxchan <- tunbuf[:n]
	}
}

func tuntx(txchan <-chan []byte, tun *water.Interface, wait *sync.WaitGroup) {
	defer wait.Done()

	log.Print("tuntx: starting")

	// Read the channel until it is closed
	for tunbuf := range txchan {
		//log.Printf("tuntx: got %d bytes to write", len(tunbuf))
		// Write the buffer to the tun interface
		n, err := tun.Write(tunbuf)
		//log.Printf("tuntx: wrote %d bytes", n)
		if err != nil {
			log.Printf("tuntx(term): error writing %s", err)
			return
		} else if n != len(tunbuf) {
			// Stop pumping if read returns error
			log.Printf("tuntx(term): short write %d of %d", n, len(tunbuf))
			return
		}
	}
	log.Print("tuntx(term): txchan closed")
}
