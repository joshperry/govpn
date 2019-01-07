package main

import (
	"encoding/binary"
	"log"
	"net"
	"sync"

	"github.com/songgao/water"
)

type message struct {
	buf [MTU + 4]byte
	len int
}

func (msg *message) packet() []byte {
	return msg.buf[4 : msg.len+4]
}

func (msg *message) wirepacket() []byte {
	return msg.buf[:msg.len+4]
}

func connrx(rdr net.Conn, rxchan chan<- *message, wait *sync.WaitGroup, bufpool chan *message) {
	// Leave the wait group when the read pump exits
	defer wait.Done()
	defer func() {
		log.Print("connrx: closing rxchan")
		close(rxchan)
	}()

	log.Print("connrx: starting")

	// Forever read
	headerbuf := make([]byte, 4)
	for {
		//log.Print("connrx: waiting")
		// This ends when the connection is closed locally or remotely
		// Read int header
		n, err := rdr.Read(headerbuf)
		if nil != err {
			// Read failed, pumpexit the handler
			log.Printf("connrx(term): error while reading header: %s", err)
			return
		} else if n < len(headerbuf) {
			log.Print("connrx(term): short read")
			return
		}

		// Get the packet length as an int
		packetlen := binary.BigEndian.Uint32(headerbuf)
		if packetlen > MTU {
			log.Printf("connrx(term): packetlen %d MTU too small or lost framing sync", packetlen)
			return
		}

		msg := <-bufpool
		msg.len = int(packetlen)

		// Read packet
		//log.Printf("connrx: reading %d byte packet", packetlen)
		n, err = rdr.Read(msg.packet())
		if nil != err {
			// Read failed, pumpexit the handler
			log.Printf("connrx(term): error while reading packet: %s", err)
			bufpool <- msg // return the message
			return
		} else if n < msg.len {
			log.Printf("connrx(term): short read %d expected %d", n, msg.len)
			bufpool <- msg // return the message
			return
		}

		// Send the packet to the rx channel
		rxchan <- msg
	}
}

func conntx(txchan <-chan *message, conn net.Conn, writeerr chan<- bool, wait *sync.WaitGroup, bufpool chan<- *message) {
	// Signal shutdown waitgroup that we're done
	defer wait.Done()

	log.Print("conntx: starting")

	// A simple one-shot flag is used to skip the write after failure
	failed := false

	// Pump the transmit channel until it is closed
	for msg := range txchan {
		//log.Print("conntx: sending packet")

		if !failed {
			//TODO: Any processing on packet from tun adapter

			// Write packet
			buf := msg.wirepacket()
			n, err := conn.Write(buf)
			//log.Printf("conntx: wrote %d bytes", n)
			if err != nil {
				log.Printf("conntx(term): error while writing: %s", err)
				// If the write errors, signal the rwerr channel
				writeerr <- true
				failed = true
			} else if n < len(buf) {
				log.Print("conntx(term): short write")
				writeerr <- true
				failed = true
			}

			bufpool <- msg
		}
	}
}

func tunrx(tun *water.Interface, rxchan chan<- *message, wait *sync.WaitGroup, bufpool chan *message) {
	// Close channel when read loop ends to signal end of traffic
	// Used by client data router to know when to stop reading
	defer close(rxchan)
	//defer wait.Done() // skipped for now since tun.Close() does not kill the sleepinig read, see tunrx callsite for more

	log.Print("tunrx: starting")

	for {
		// Make room at the beginning for the packet length
		msg := <-bufpool
		msg.len = MTU
		n, err := tun.Read(msg.packet())

		//log.Printf("tunrx: got %d byte packet", n)

		if err != nil {
			// Stop pumping if read returns error
			log.Printf("tunrx(term): error reading %s", err)
			bufpool <- msg // put the buffer back
			return
		}

		msg.len = n

		// Put the packet length at the beginning of the packet
		binary.BigEndian.PutUint32(msg.wirepacket(), uint32(n))
		rxchan <- msg
	}
}

func tuntx(txchan <-chan *message, tun *water.Interface, wait *sync.WaitGroup, bufpool chan<- *message) {
	defer wait.Done()

	log.Print("tuntx: starting")

	// Read the channel until it is closed
	for msg := range txchan {
		//log.Printf("tuntx: got %d bytes to write", len(tunbuf))
		// Write the buffer to the tun interface
		n, err := tun.Write(msg.packet())

		if n != msg.len {
			// Stop pumping if read returns error
			log.Printf("tuntx(term): short write %d of %d", n, msg.len)
			bufpool <- msg
			return
		}

		bufpool <- msg

		//log.Printf("tuntx: wrote %d bytes", n)
		if err != nil {
			log.Printf("tuntx(term): error writing %s", err)
			return
		}
	}
	log.Print("tuntx(term): txchan closed")
}
