package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/songgao/water"
)

type message struct {
	buf        [MTU + 4]byte
	packet     []byte
	wirepacket []byte
	len        int
}

func (msg *message) clr() {
	msg.len = MTU
	msg.wirepacket = msg.buf[:MTU+4]
	msg.packet = msg.wirepacket[4:]
}

func (msg *message) set(n int) {
	msg.len = n
	msg.wirepacket = msg.buf[:msg.len+4]
	msg.packet = msg.wirepacket[4:]
	binary.BigEndian.PutUint32(msg.wirepacket, uint32(msg.len))
}

func (msg *message) elen() int {
	return int(binary.BigEndian.Uint32(msg.wirepacket))
}

func (msg *message) eset() error {
	packetlen := msg.elen()
	if packetlen > MTU {
		return errors.New(fmt.Sprintf("connrx(term): packetlen %d MTU too small or lost framing sync", packetlen))
	}

	if packetlen != msg.len {
		msg.len = packetlen
		msg.wirepacket = msg.buf[:msg.len+4]
		msg.packet = msg.buf[4:]
	}

	return nil
}

type filterfunc func(*message, filterstack) error

type filterstack []filterfunc

func (stack filterstack) next(msg *message) error {
	return stack[0](msg, stack[1:])
}

type messagesender func(*message) error

func connrx(rdr net.Conn, routers chan<- *message, clientip uint32, readerr chan<- bool, wait *sync.WaitGroup, bufpool *sync.Pool) {
	// Leave the wait group when the read pump exits
	defer wait.Done()
	defer func() {
		log.Print("connrx: closing rxchan")
		close(readerr)
	}()

	log.Print("connrx: starting")

	// Forever read
	for {
		msg := bufpool.Get().(*message)
		msg.clr()

		fatal := func(logmsg string, err error) {
			log.Printf("connrx(term): %s %s", logmsg, err)
			bufpool.Put(msg)
		}

		//log.Print("connrx: waiting")
		if n, err := rdr.Read(msg.wirepacket[:4]); nil != err {
			fatal("error reading", err)
			return
		} else if n < 4 {
			fatal("", errors.New("short read"))
			return
		}

		if err := msg.eset(); nil != err {
			fatal("", err)
			return
		}

		// This ends when the connection is closed locally or remotely
		// Read int header
		if n, err := rdr.Read(msg.packet); nil != err {
			// Read failed, pumpexit the handler
			fatal("error while reading header", err)
			return
		} else if n < msg.len {
			fatal("", errors.New("short read"))
			return
		}

		// Grab the packet source ip
		srcip := binary.BigEndian.Uint32(msg.packet[12:16])

		//cprintf("received packet %s", spew.Sdump(headers))

		// Drop any packets with a source address different than the one allocated to the client
		if srcip != clientip {
			fatal("", errors.New(fmt.Sprintf("dropped bogon %s", int2ip(srcip))))
		}

		// Send the packet to the routers
		routers <- msg

		// Metrics
		rx_packetsmetric.Inc()
		rx_bytesmetric.Add(float64(msg.len))
	}
}

func conntx(messages <-chan *message, conn net.Conn, writeerr chan<- bool, wait *sync.WaitGroup, bufpool *sync.Pool) {
	defer func() {
		wait.Done()
		close(writeerr)
		log.Print("contx(term): message channel closed")
	}()

	for msg := range messages {
		//TODO: Any processing on packet from tun adapter

		// Write packet
		n, err := conn.Write(msg.wirepacket)
		//log.Printf("conntx: wrote %d bytes", n)

		// Put the message back
		bufpool.Put(msg)

		if nil != err {
			log.Printf("conntx(term): error while writing: %s", err)
			return
		} else if n < len(msg.wirepacket) {
			err = errors.New("short write")
		}

		// Metrics
		tx_packetsmetric.Inc()
		tx_bytesmetric.Add(float64(msg.len))
	}
}

// Take messages from the tun queue and put them on the txchan
func tunrx(tun *water.Interface, txchan chan<- *message, bufpool *sync.Pool) {
	log.Print("tunrx: starting")

	for {
		msg := bufpool.Get().(*message)
		msg.clr()

		if n, err := tun.Read(msg.packet); err != nil {
			// Stop pumping if read returns error
			log.Printf("tunrx(term): error reading %s", err)
			bufpool.Put(msg) // put the buffer back
			return
		} else {
			//log.Printf("tunrx: got %d byte packet", n)

			msg.set(n)

			// Send the message to the channel
			txchan <- msg
		}
	}
}

// Take messages off of rxchan and send them to the tun queue
func tuntx(rxchan <-chan *message, tun *water.Interface, bufpool *sync.Pool) {
	for msg := range rxchan {
		//log.Printf("tuntx: got %d bytes to write", len(tunbuf))
		// Write the buffer to the tun interface
		n, err := tun.Write(msg.packet)

		bufpool.Put(msg)

		//log.Printf("tuntx: wrote %d bytes", n)
		if n != msg.len {
			// Stop pumping if read returns error
			err = errors.New(fmt.Sprintf("short write %d of %d", n, msg.len))
		} else if err != nil {
			log.Printf("tuntx(term): error writing %s", err)
		}
	}
}
