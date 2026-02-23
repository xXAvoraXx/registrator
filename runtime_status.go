package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sort"
	"sync"
	"sync/atomic"
	"time"

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
	SwarmServiceName string
	SwarmTaskID      string
	Hostname         string
	OverlayIP        string
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
	sw.Hostname = hostname
	container, err := docker.InspectContainer(hostname)
	if err == nil && container != nil && container.Config != nil {
		labels := container.Config.Labels
		if labels["com.docker.swarm.service.id"] != "" {
			sw.RunningAsService = true
			sw.SwarmServiceID = labels["com.docker.swarm.service.id"]
			sw.SwarmServiceName = labels["com.docker.swarm.service.name"]
			sw.SwarmTaskID = labels["com.docker.swarm.task.id"]
			if sw.NodeID == "" {
				sw.NodeID = labels["com.docker.swarm.node.id"]
			}
		}
		if container.NetworkSettings != nil {
			for _, network := range container.NetworkSettings.Networks {
				if network.IPAddress != "" {
					sw.OverlayIP = network.IPAddress
					break
				}
			}
		}
	}
	return sw
}

type peerInfo struct {
	ServiceID   string `json:"serviceId"`
	ServiceName string `json:"serviceName"`
	TaskID      string `json:"taskId"`
	NodeID      string `json:"nodeId"`
	NodeAddr    string `json:"nodeAddr"`
	Hostname    string `json:"hostname"`
	OverlayIP   string `json:"overlayIP"`
	Role        string `json:"role"`
}

var peerDiscoveryLogState sync.Map
var discoveredManagerAddrState sync.Map

func (s swarmRuntime) peerInfo() peerInfo {
	return peerInfo{
		ServiceID:   s.SwarmServiceID,
		ServiceName: s.SwarmServiceName,
		TaskID:      s.SwarmTaskID,
		NodeID:      s.NodeID,
		NodeAddr:    s.NodeAddr,
		Hostname:    s.Hostname,
		OverlayIP:   s.OverlayIP,
		Role:        s.Role,
	}
}

func serveStatus(addr string, b *bridge.Bridge, runtime swarmRuntime, docker *dockerapi.Client, eventsProcessed *uint64, reconcileRuns *uint64) {
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
	mux.HandleFunc("/peerinfo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runtime.peerInfo())
	})
	mux.HandleFunc("/swarm/service/", func(w http.ResponseWriter, req *http.Request) {
		if docker == nil {
			http.Error(w, "docker unavailable", http.StatusServiceUnavailable)
			return
		}
		escapedServiceID := strings.TrimPrefix(req.URL.EscapedPath(), "/swarm/service/")
		serviceID, err := url.PathUnescape(escapedServiceID)
		if err != nil || serviceID == "" || strings.Contains(serviceID, "/") || strings.Contains(serviceID, "\\") || strings.Contains(serviceID, "..") {
			http.NotFound(w, req)
			return
		}
		service, err := docker.InspectService(serviceID)
		if err != nil {
			log.Printf("status swarm service inspect failed for %s: %v", serviceID, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(service)
	})
	log.Printf("Serving status endpoints on %s", addr)
	startPeerDiscovery(runtime, addr, func(info peerInfo) {
		if info.Role != "manager" {
			return
		}
		b.Sync(true)
		atomic.AddUint64(reconcileRuns, 1)
	})
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("status server stopped: %v", err)
	}
}

func startPeerDiscovery(runtime swarmRuntime, addr string, onPeerDiscovered func(peerInfo)) {
	if !runtime.RunningAsService || runtime.SwarmServiceName == "" {
		return
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	peerHost := "tasks." + runtime.SwarmServiceName
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		discoverPeers(peerHost, port, runtime.OverlayIP, onPeerDiscovered)
		for range ticker.C {
			discoverPeers(peerHost, port, runtime.OverlayIP, onPeerDiscovered)
		}
	}()
}

func discoverPeers(peerHost, port, selfOverlayIP string, onPeerDiscovered func(peerInfo)) {
	ips, err := net.LookupIP(peerHost)
	if err != nil {
		logPeerDiscoveryState("dns:"+peerHost, fmt.Sprintf("peer discovery DNS lookup failed for %s: %v", peerHost, err))
		return
	}
	peerDiscoveryLogState.Delete("dns:" + peerHost)
	client := &http.Client{Timeout: 2 * time.Second}
	for _, ip := range ips {
		peerIP := ip.String()
		if peerIP == selfOverlayIP {
			continue
		}
		url := "http://" + net.JoinHostPort(peerIP, port) + "/peerinfo"
		info, err := fetchPeerInfo(client, url)
		if err != nil {
			logPeerDiscoveryState("peererr:"+peerIP, fmt.Sprintf("peer discovery fetch failed for %s: %v", peerIP, err))
			continue
		}
		peerDiscoveryLogState.Delete("peererr:" + peerIP)
		signature := fmt.Sprintf("%s|%s|%s|%s|%s", info.ServiceName, info.TaskID, info.NodeID, info.OverlayIP, info.Role)
		key := "peerok:" + peerIP
		prev, seen := peerDiscoveryLogState.Load(key)
		if seen && prev == signature {
			continue
		}
		peerDiscoveryLogState.Store(key, signature)
		if info.Role == "manager" {
			rememberManagerAddr(info.OverlayIP)
			rememberManagerAddr(peerIP)
		}
		if onPeerDiscovered != nil {
			onPeerDiscovered(info)
		}
		log.Printf("discovered peer service=%s task=%s node=%s ip=%s role=%s", info.ServiceName, info.TaskID, info.NodeID, info.OverlayIP, info.Role)
	}
}

func rememberManagerAddr(addr string) {
	if addr == "" {
		return
	}
	discoveredManagerAddrState.Store(addr, struct{}{})
}

func forgetManagerAddr(addr string) {
	if addr == "" {
		return
	}
	discoveredManagerAddrState.Delete(addr)
}

func discoveredManagerAddrs() []string {
	addrs := make([]string, 0)
	discoveredManagerAddrState.Range(func(key, _ interface{}) bool {
		addr, ok := key.(string)
		if ok && addr != "" {
			addrs = append(addrs, addr)
		}
		return true
	})
	sort.Strings(addrs)
	return addrs
}

func logPeerDiscoveryState(key, message string) {
	if prev, seen := peerDiscoveryLogState.Load(key); seen && prev == message {
		return
	}
	peerDiscoveryLogState.Store(key, message)
	log.Print(message)
}

func fetchPeerInfo(client *http.Client, url string) (peerInfo, error) {
	resp, err := client.Get(url)
	if err != nil {
		return peerInfo{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return peerInfo{}, fmt.Errorf("peerinfo status %d", resp.StatusCode)
	}
	var info peerInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return peerInfo{}, err
	}
	if info.OverlayIP == "" {
		host, _, err := net.SplitHostPort(resp.Request.URL.Host)
		if err == nil {
			info.OverlayIP = host
		}
	}
	return info, nil
}
