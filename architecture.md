# govpn
A highly-concurrent TLS VPN for cloud deployment, written in golang

## VPN Architecture

This diagram details the architectural design in a visual fashion.

![govpn drawio](https://user-images.githubusercontent.com/298053/217902508-f80bfbe9-33eb-489b-aa51-54228fd1d752.svg)

On the client and server we have two main gophers to initiate/accept and babysit long-lived TLS connections used to pipe data between the two.

On the server the manager gopher listens for TLS connections from clients and manages their lifetime.

For each client connection the manager creates one gopher for each of the tx and rx data flow for the client.

It also starts a router gopher whose job is to inspect packets coming from the server TUN adapter, and deliver them to the proper client tx gopher channel.

The client tx and rx gophers use a mostly identical data pumping mechanism to deliver incoming packets.
The tx gopher pumps packets coming from the TUN adapter to the TLS connection.
The rx gopher pumps packets from the TLS connection into the TUN adapter.

On the client side we can see a single instance of the data pumping mechanism.
Because a client does not need to route packets or scale like a server, we have a fixed count of 3 async tasks: the manager, the tx path, and the rx path.

This makes the client full-duplex and should allow it to move packets to the server at close to the full link speed.
Backpressure is signalled to the OS by reading packets from the TUN adapter.

I rely on the TCP stack to handle flow control.
