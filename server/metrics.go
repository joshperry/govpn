package main

import (
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	accepted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_client_accept",
		Help: "Number of clients accepted",
	})

	connectcount = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_client_connect",
		Help: "Number of times a client's other connection was terminated for too many connections.",
	})
	failcount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "vpn_client_fail",
			Help: "Number of times a client fails the TLS handshake.",
		},
		[]string{"reason"},
	)

	clientcount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "vpn_clients_tracked",
			Help: "Number of currently connected clients.",
		},
		[]string{"table"},
	)
	enforcecount = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_client_enforced",
		Help: "Number of times a client's other connection was terminated for too many connections.",
	})

	ipusecount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "vpn_ip_usage",
			Help: "Client IP utilisation, free and allocated counts.",
		},
		[]string{"table"},
	)
)

func metrics() {
	// Register metrics
	prometheus.MustRegister(accepted)

	prometheus.MustRegister(connectcount)
	prometheus.MustRegister(failcount)

	prometheus.MustRegister(clientcount)
	prometheus.MustRegister(enforcecount)

	prometheus.MustRegister(ipusecount)

	msrv := http.NewServeMux()
	// Expose the registered metrics via HTTP.
	msrv.Handle("/metrics", promhttp.Handler())
	log.Fatal(http.ListenAndServe("localhost:9000", msrv))
}
