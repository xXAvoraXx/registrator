package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sync/atomic"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/registrator/bridge"
)

type swarmRuntime struct {
	Enabled          bool
	NodeID           string
	Role             string
	NodeAddr         string
	RunningAsService bool
	SwarmServiceID   string
}

func detectSwarmRuntime(docker *dockerapi.Client) swarmRuntime {
	info, err := docker.Info()
	if err != nil {
		return swarmRuntime{}
	}
	sw := swarmRuntime{
		Enabled:  info.Swarm.LocalNodeState == "active",
		NodeID:   info.Swarm.NodeID,
		NodeAddr: info.Swarm.NodeAddr,
		Role:     "worker",
	}
	if info.Swarm.ControlAvailable {
		sw.Role = "manager"
	}
	hostname, err := os.Hostname()
	if err != nil {
		log.Printf("unable to get hostname for swarm task detection: %v", err)
		return sw
	}
	container, err := docker.InspectContainer(hostname)
	if err == nil && container != nil && container.Config != nil {
		labels := container.Config.Labels
		if labels["com.docker.swarm.service.id"] != "" {
			sw.RunningAsService = true
			sw.SwarmServiceID = labels["com.docker.swarm.service.id"]
		}
	}
	return sw
}

func serveStatus(addr string, b *bridge.Bridge, eventsProcessed *uint64, reconcileRuns *uint64) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if err := b.Ping(); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "registrator_registered_services %d\n", b.ServiceCount())
		_, _ = fmt.Fprintf(w, "registrator_events_processed_total %d\n", atomic.LoadUint64(eventsProcessed))
		_, _ = fmt.Fprintf(w, "registrator_reconcile_runs_total %d\n", atomic.LoadUint64(reconcileRuns))
	})
	log.Printf("Serving status endpoints on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("status server stopped: %v", err)
	}
}
