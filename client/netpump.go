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

func (msg *message) set(n int) {
	if n != msg.len {
		msg.len = n
		msg.packet = msg.buf[4 : msg.len+4]
		msg.wirepacket = msg.buf[:msg.len+4]
		binary.BigEndian.PutUint32(msg.wirepacket, uint32(msg.len))
	}
}

type filterfunc func(*message, filterstack) error

type filterstack []filterfunc

func (stack filterstack) next(msg *message) error {
	return stack[0](msg, stack[1:])
}

func connrx(rdr net.Conn, txstack filterstack, readerr chan<- bool, wait *sync.WaitGroup, bufpool *sync.Pool) {
	// Leave the wait group when the read pump exits
	defer wait.Done()
	defer func() {
		log.Print("connrx: closing rxchan")
		close(readerr)
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

		msg := bufpool.Get().(*message)
		msg.set(int(packetlen))

		fatal := func(logmsg string, err error) {
			log.Printf("connrx(term): %s %s", logmsg, err)
			bufpool.Put(msg) // return the message
		}

		// Read packet
		//log.Printf("connrx: reading %d byte packet", packetlen)
		if n, err = rdr.Read(msg.packet); nil != err {
			// Read failed, pumpexit the handler
			fatal("error while reading packet", err)
			return
		} else if n < msg.len {
			fatal("", errors.New("short read"))
			return
		}

		// Send the packet to the tx stack
		if err = txstack.next(msg); nil != err {
			fatal("error running filterstack", err)
			return
		}

		// Put message back on pool
		bufpool.Put(msg)
	}
}

func conntx(conn net.Conn) filterfunc {
	return func(msg *message, _ filterstack) (err error) {
		//TODO: Any processing on packet from tun adapter

		// Write packet
		n, err := conn.Write(msg.wirepacket)
		//log.Printf("conntx: wrote %d bytes", n)

		if err != nil {
			log.Printf("conntx(term): error while writing: %s", err)
		} else if n < len(msg.wirepacket) {
			err = errors.New("short write")
		}

		return
	}
}

func tunrx(tun *water.Interface, txstack filterstack, wait *sync.WaitGroup, bufpool *sync.Pool) {
	//defer wait.Done() // skipped for now since tun.Close() does not kill the sleepinig read, see tunrx callsite for more

	log.Print("tunrx: starting")

	for {
		// Make room at the beginning for the packet length
		msg := bufpool.Get().(*message)
		msg.set(MTU)
		n, err := tun.Read(msg.packet)

		//log.Printf("tunrx: got %d byte packet", n)

		if err != nil {
			// Stop pumping if read returns error
			log.Printf("tunrx(term): error reading %s", err)
			bufpool.Put(msg) // put the buffer back
			return
		}

		// Set message length
		msg.set(n)

		// Sent the message through the filter stack
		err = txstack.next(msg)
		if nil != err {
			log.Printf("tunrx(term): error running stack: %s", err)
		}

		// Put message back on pool
		bufpool.Put(msg)
	}
}

func tuntx(tun *water.Interface) filterfunc {
	return func(msg *message, _ filterstack) (err error) {
		//log.Printf("tuntx: got %d bytes to write", len(tunbuf))
		// Write the buffer to the tun interface
		n, err := tun.Write(msg.packet)

		//log.Printf("tuntx: wrote %d bytes", n)
		if n != msg.len {
			// Stop pumping if read returns error
			err = errors.New(fmt.Sprintf("short write %d of %d", n, msg.len))
		} else if err != nil {
			log.Printf("tuntx(term): error writing %s", err)
		}

		return
	}
}
