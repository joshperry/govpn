package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Service
	accepted = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_client_accept",
		Help: "Number of clients accepted",
	})

	// Client handler
	connectcount = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_client_connect",
		Help: "Number of times a client has connected",
	})
	disconnectcount = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_client_disconnect",
		Help: "Number of times a client has disconnected",
	})
	failcount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "vpn_client_fail",
			Help: "Number of times a client fails the TLS handshake.",
		},
		[]string{"reason"},
	)

	// Contrack
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

	//Netblock
	ipusecount = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "vpn_ip_usage",
			Help: "Client IP utilisation, free and allocated counts.",
		},
		[]string{"table"},
	)
)

func metrics(reportchan chan<- chan<- Connections) {
	// Register metrics
	// Service
	prometheus.MustRegister(accepted)

	// Client handler
	prometheus.MustRegister(connectcount)
	prometheus.MustRegister(disconnectcount)
	prometheus.MustRegister(failcount)

	// Conntrack
	prometheus.MustRegister(clientcount)
	prometheus.MustRegister(enforcecount)

	// Netblock
	prometheus.MustRegister(ipusecount)

	msrv := http.NewServeMux()
	// Expose the registered metrics via HTTP.
	msrv.Handle("/metrics", promhttp.Handler())
	msrv.HandleFunc("/clients", func(w http.ResponseWriter, req *http.Request) {
		// Request connection list from the contrack service
		respchan := make(chan Connections)
		reportchan <- respchan
		connections := <-respchan

		if respbuf, err := json.Marshal(connections); nil != err {
			log.Printf("server: contrack: report: error json enconding connection array: %s", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Write(respbuf)
		}
	})
	log.Fatal(http.ListenAndServe("localhost:9000", msrv))
}
