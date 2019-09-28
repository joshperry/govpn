# govpn
A highly-concurrent TLS VPN for cloud deployment, written in golang

## VPN Architecture

This diagram details the architectural design in a visual fashion.

On the client and server we have two main gophers to initiate/accept and babysit long-lived TLS connections used to pipe data between the two.

On the client side we can see a single instance of the data pumping mechanism.
Because a client does not need to route packets or scale like a server, we have a fixed count of 3 async tasks: the manager, the tx path, and the rx path.

This makes the client full-duplex and should allow it to move packets to the server at close to the full link speed.
Backpressure is signalled to the OS by reading packets from the TUN adapter.

I rely on the TCP stack to handle flow control.

https://www.lucidchart.com/invitations/accept/26bbe165-5628-43cf-aa5d-1d2081d1b346)
