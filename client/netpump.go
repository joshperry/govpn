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
	msg.wirepacket = msg.buf[:]
	msg.packet = msg.buf[4:]
	msg.len = MTU
}

func (msg *message) set(n int) {
	if n != msg.len {
		msg.len = n
		msg.wirepacket = msg.buf[:msg.len+4]
		msg.packet = msg.buf[4:]
		binary.BigEndian.PutUint32(msg.wirepacket, uint32(msg.len))
	}
}

func (msg *message) eset() error {
	packetlen := int(binary.BigEndian.Uint32(msg.wirepacket))
	if packetlen > MTU {
		return errors.New(fmt.Sprintf("connrx(term): packetlen %d MTU too small or lost framing sync", packetlen))
	}

	if packetlen != msg.len {
		msg.len = packetlen
		msg.wirepacket = msg.buf[:msg.len+4]
		msg.packet = msg.wirepacket[4:]
	}

	return nil
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
	for {
		msg := bufpool.Get().(*message)
		msg.clr()

		fatal := func(logmsg string, err error) {
			log.Printf("connrx(term): %s %s", logmsg, err)
			bufpool.Put(msg)
		}

		if n, err := rdr.Read(msg.wirepacket[:4]); nil != err {
			fatal("error while reading header", err)
		} else if n < 4 {
			fatal("", errors.New("short read"))
			return
		}

		// Setup message slices from embedded length
		if err := msg.eset(); nil != err {
			fatal("", err)
			return
		}

		//log.Print("connrx: waiting")
		// This ends when the connection is closed locally or remotely
		// Read int header
		if n, err := rdr.Read(msg.packet); nil != err {
			// Read failed, pumpexit the handler
			fatal("error while reading body", err)
			return
		} else if n < msg.len {
			fatal("", errors.New("short read"))
			return
		}

		//log.Printf("connrx: read %d bytes", msg.len)
		// Send the packet to the tx stack
		if err := txstack.next(msg); nil != err {
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
		msg := bufpool.Get().(*message)
		msg.clr()
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

		// Send the message through the filter stack
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
