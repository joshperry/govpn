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
	acceptedmetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_client_accept",
		Help: "Number of clients accepted",
	})

	// Client handler
	client_connectmetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_client_connect",
		Help: "Number of times a client has connected",
	})
	client_disconnectmetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_client_disconnect",
		Help: "Number of times a client has disconnected",
	})
	client_failmetric = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "vpn_client_fail",
			Help: "Number of times a client fails the TLS handshake.",
		},
		[]string{"reason"},
	)

	// Contrack
	contrack_trackedmetric = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "vpn_clients_tracked",
			Help: "Number of currently connected clients.",
		},
		[]string{"table"},
	)
	contrack_enforcedmetric = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "vpn_client_enforced",
		Help: "Number of times a client's other connection was terminated for too many connections.",
	})

	//Netblock
	netblock_usemetric = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "vpn_ip_usage",
			Help: "Client IP utilisation, free and allocated counts.",
		},
		[]string{"table"},
	)

	// Router
	route_durationmetric = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "vpn_router_seconds",
			Help:    "Router packet delivery time",
			Buckets: prometheus.ExponentialBuckets(0.0001, 2, 5),
		},
	)
)

func metrics(reportchan chan<- chan<- Connections) {
	log.Print("metrics: starting")

	// Register metrics
	// Service
	prometheus.MustRegister(acceptedmetric)

	// Client handler
	prometheus.MustRegister(client_connectmetric)
	prometheus.MustRegister(client_disconnectmetric)
	prometheus.MustRegister(client_failmetric)

	// Conntrack
	prometheus.MustRegister(contrack_trackedmetric)
	prometheus.MustRegister(contrack_enforcedmetric)

	// Netblock
	prometheus.MustRegister(netblock_usemetric)

	// Router
	prometheus.MustRegister(route_durationmetric)

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
	// TODO: get from config
	log.Print("metrics: http listen on 9000")
	log.Fatal(http.ListenAndServe("localhost:9000", msrv))
}
