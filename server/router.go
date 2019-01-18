package main

import (
	"encoding/binary"
	"log"
	"sync"
)

type routemap map[uint32]chan<- *message

// Keeps the route cache updated from client state events
func route(rxchan <-chan *message, tun chan<- *message, subchan chan<- ClientStateSub, bufpool *sync.Pool, wait *sync.WaitGroup) {
	log.Print("server: router: starting")
	defer func() {
		wait.Done()
		log.Print("server: route(perm): hasta")
	}()

	// Channel to receive client state
	statechan := make(chan ClientState)

	cache := make(routemap)

	// Subscribe to client state stream
	subchan <- ClientStateSub{name: "router", subchan: statechan}

	// State messages update the routes map state
	// Data routing consumes the routes map state
	for {
		select {
		case msg := <-rxchan:
			clientip := binary.BigEndian.Uint32(msg.packet[16:20])
			if tx, ok := cache[clientip]; ok {
				if len(tx) == cap(tx) {
					bufpool.Put(msg)
					//log.Printf("router(drop): max queue length for client %s", int2ip(clientip))
				} else {
					// Write the message to the client
					tx <- msg
				}
			} else {
				log.Printf("router(drop): no route for client %s", int2ip(clientip))

				/* TODO: If TCP packet, sent TCP RST
				if msg.packet[9] == 6 {
					header := ipv4.Header{
						ID:       0x1234,
						Version:  4,
						Len:      20,
						TotalLen: 20 + len(tcpbuf), // 20 bytes for IP, plus ICMP payload
						TTL:      64,
						Protocol: 1,                                                  // ICMP
						Dst:      int2ip(binary.BigEndian.Uint32(msg.packet[12:16])), // src ip from message
						Src:      int2ip(binary.BigEndian.Uint32(msg.packet[16:20])), // dst ip from message
						// Leave Checksum zero for the calculation
					}

					if headerbuf, err := header.Marshal(); nil != err {
						log.Print("router: could not marshal messag eheader")
					} else {
						// Calculate the header checksum
						header.Checksum = int(checksum(headerbuf))

						// Serialize and send the header and icmp payload
						if headerbuf, err := header.Marshal(); nil != err {
							log.Print("router: could not marshal final message header")
						} else {
							// Put the icmp packet in the message
							msg.clr()
							copy(msg.packet, headerbuf)
							msg.set(len(headerbuf) + len(icmpbuf))

							// Send the icmp message
							tun <- msg
						}
					}
				} else {
					// Otherwise send icmp unreachable

					// Get OP header and data lens
					hdrlen := (int(msg.packet[0]) & 0x0F) * 4 // IHL * 32 bits
					payloadlen := msg.len - hdrlen
					if payloadlen > 8 {
						payloadlen = 8
					}

					// Build the message
					icmp := icmp.Message{
						Type: ipv4.ICMPTypeDestinationUnreachable,
						Code: 1, // Host
						Body: &icmp.DstUnreach{
							// Data is the ip header plus 8 bytes of the payload
							Data: msg.packet[:hdrlen+payloadlen],
						},
					}

					if icmpbuf, err := icmp.Marshal(nil); nil != err {
						log.Print("router: could not marshal icmp message")
					} else {
						header := ipv4.Header{
							ID:       0x1234,
							Version:  4,
							Len:      20,
							TotalLen: 20 + len(icmpbuf), // 20 bytes for IP, plus ICMP payload
							TTL:      64,
							Protocol: 1,                                                  // ICMP
							Dst:      int2ip(binary.BigEndian.Uint32(msg.packet[12:16])), // src ip from message
							Src:      net.IPv4(192, 168, 0, 1),
							// Leave Checksum zero for the calculation
						}

						if headerbuf, err := header.Marshal(); nil != err {
							log.Print("router: could not marshal messag eheader")
						} else {
							// Calculate the header checksum
							header.Checksum = int(checksum(headerbuf))

							// Serialize and send the header and icmp payload
							if headerbuf, err := header.Marshal(); nil != err {
								log.Print("router: could not marshal final message header")
							} else {
								// Put the icmp packet in the message
								msg.clr()
								copy(msg.packet, append(headerbuf, icmpbuf...))
								msg.set(len(headerbuf) + len(icmpbuf))

								// Send the icmp message
								tun <- msg
							}
						}
					}
				}
				*/
			}

		case state, ok := <-statechan:
			if !ok {
				log.Print("server: router: statechan closed")
				return
			}

			// uint32 keys are used for the route map
			ipint := ip2int(state.client.ip)
			if state.transition == Connect {
				log.Printf("server: route: got client connect %s %s-%#x", state.client.ip, state.client.name, state.client.id)
				// Add an item to the routing table
				cache[ipint] = state.client.tx
			} else if state.transition == Disconnect {
				log.Printf("server: route: got client disconnect %s %s-%#x", state.client.ip, state.client.name, state.client.id)
				// Remove the client from the routing table and then close the client tx channel
				// Once the disconnect message is recieved, the client handler has exited
				if _, ok := cache[ipint]; ok { // no read lock because we're the writer
					delete(cache, ipint)
				} else {
					// Didn't find a connection in the routes for this client... shouldn't happen
					log.Printf("server: route(perm): close no open connection %s %s-%#x", state.client.ip, state.client.name, state.client.id)
					panic("close no open connection")
				}
			} else {
				log.Printf("server: route(perm): unhandled client transition state: %d", state.transition)
				panic("unhandled client transition state")
			}
		}
	}
}

func checksum(buf []byte) uint16 {
	sum := uint32(0)

	for ; len(buf) >= 2; buf = buf[2:] {
		sum += uint32(buf[0])<<8 | uint32(buf[1])
	}
	if len(buf) > 0 {
		sum += uint32(buf[0]) << 8
	}
	for sum > 0xffff {
		sum = (sum >> 16) + (sum & 0xffff)
	}
	csum := ^uint16(sum)
	/*
	 * From RFC 768:
	 * If the computed checksum is zero, it is transmitted as all ones (the
	 * equivalent in one's complement arithmetic). An all zero transmitted
	 * checksum value means that the transmitter generated no checksum (for
	 * debugging or for higher level protocols that don't care).
	 */
	if csum == 0 {
		csum = 0xffff
	}
	return csum
}
