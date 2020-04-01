# GoVPN

It's a VPN written in golang.

It's meant to be fast and highly-concurrent, while supporting thousands of clients and multi-node clustering in a modern cloud environment.

## Configuration

The configuration values can be set via a `config.yaml` file in the `cwd`, via environment variables, or via command line options.

### Environment

The config file path can be specified in `GOVPN_CONFIG_FILE`.

Variables must be uppercase, with each part separated by underscore, and prefixed with `GOVPN` (e.g. `GOVPN_LISTEN_ADDRESS`).

### Server

- tun.name (tun_govpn): The device name for the tun adapter.

- listen.address (0.0.0.0): The address to listen for client connections on.
- listen.port (443): TCP port to listen for client connections on.

- tls.cert (server.crt): The server cert chain in PEM format.
- tls.key (server.key): The server private key in PEM format.
- tls.ca (ca.pem): The CA chain used for authenticating client certificates.

- secnet.netblock (192.168.0.1/21): CIDR format netblock to allocate client IPs from. The server uses the first address in the block.

### Client

- server (server:443): The hostname:port of the VPN server.

- tun.name (tun_govpnc): The device name for the tun adapter.

- tls.cert (client.crt): The client cert chain in PEM format.
- tls.key (client.key): The client private key in PEM format.
- tls.ca (ca.pem): The CA chain used for authenticating server certificates.

## Testing Stack

Running the compose stack will bring up the server and 3 clients using embedded test certificates for auth.

    $ docker-compose up

Running a command from the server or clients can be used to do tests, the container has iperf and tcpdump available.

    $ docker exec -it govpn_server_1 iperf -s
    $ docker exec -it govpn_client_1 iperf -c 192.168.0.1

## Profiling

Profiling is an important way to guide optimization and golang has an excellent profiler in pprof, we won't hide it under a bushel.

To run a profile on the server instance:

    $ go tool pprof http://localhost:6060/debug/pprof/profile?seconds=6

After profile is complete, you'll be dropped into pprof ready for analysis.
