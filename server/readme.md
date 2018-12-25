# VPN Server

## Main

Sets up crypto

Sets up tun interface

Creates channels for writing packets into the tun interface and reading packets out of it

-> Producer goroutine for tunrx channel that reads packets from the tun interface and writes them to the channel
   exits when tun interface is closed
   defers tunrx channel close
   defers leaving main waitgroup

-> Consumer goroutine for tuntx channel that reads packets from the channel and Write()s them to the tun interface
   exits when tuntx channel is closed
   defers leaving main waitgroup

Creates TLS listener

Creates the Service 

-> Executes the main Service handler function with the tls listener, tunrx channel, tuntx channel, and server network information

Waits for SIGINT or SIGTERM before signaling the Server to shut down its connections

service.Stop() blocks until all Server handlers and pumps have stopped

Closes the tuntx channel which releases the tuntx goroutine

Closes the tun adapter to release the tunrx goroutine

Waits on the main waitgroup before exiting

## Serve

This is the main Service execution logic

Defers close of listener to end of server

Creates connchan channel to hold accepted but not handled client connections

Creates clientstate channel to hold ClientState change messages, defers close to end of server

-> Producer that accepts client connections from the listener and puts them on connchan
   exits when listen.Accept() fails (when deferred listener close runs)
   defers leaving shutdown waitgroup
   defers close of connchan

-> Consumer that accepts packets from the tunrx channel and delivers them to the appropriate client's distinct tunrx channel, uses clientstate messages to update internal routing table
   exits when tunrx or clientstate channels close
   closes client.tunrx channel when client disconnects

Create a netblock channel with a buffer the size of the client netblock

-> Producer/Consumer that fills the netblock channel with available IP addresses and pushes addresses of disconnected clients (from clientstate messages) back onto the channel
   exits when clientstate channel is closed (happens in deferral at end of server)
   defers close of netblock channel

Loops reading new connections from connchan and waiting for the done channel to signal shutdown

For each new connection from connchan
   Add a refcount to the shutdown waitgroup
   -> Run the client handler with the client connection, tuntx (writeonly) channel, clientstate (writeonly) channel, and the (readonly) netblock channel

## serve

This is the client connection handler

Defers close of conn, the client connection until handler end

Defers leaving shutdown waitgroup until handler end

Casts the connection to tlscon, a tls connection

Executes the TLS handshake

Creates a Client from the successful handshake

Creates a clientrx channel for holding packets received from the client tls connection

-> A producer that takes packets from the client tls connection and pushes them to clientrx
   exits when Read fails or returns a zero-length read
   defers leaving the shutdown waitgroup
   defers closing the clientrx channel

Creates a writeerr channel to signal a failure in the write-side pump

-> A consumer that reads packets from the client.tunrx channel and Writes them to the client tls connection
   continues pumping even after a write failure to keep router from stalling
   exits when client.tunrx is closed (by the router after it receives the ClientState disconnect message)
   defers leaving the shutdown waitgroup

Attempts to read the first packet from the clientrx channel to get the client information packet

Decodes the json client information packet

Allocates an ip off the netblock channel

Creates a client settings json packet and pushes it on the client.tunrx channel

Defer sending the disconnect client state to the clientstate channel until handler end

Send the connect client state to the clientstate channel

Loops on the done channel, writeerr channel, and clientrx channel

On a clientrx packet
  The packet headers are parsed
  The src IP is checked to match the client ip, drop the packet if not
  Write the buffer to the tuntx channel for delivery to the tun adapter

The handler is exited if writeerr or done channel receive, or the clientrx channel is closed
