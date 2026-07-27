package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
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
	info, err := inspectDockerInfo(docker)
	if err != nil {
		return swarmRuntime{}
	}
	sw := swarmRuntime{
		Enabled:  string(info.Swarm.LocalNodeState) == "active",
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

const statusTokenHeader = "X-Registrator-Token"

type backendProber interface {
	Ping() error
}

type reconcileBackend interface {
	backendProber
	Sync(bool) error
}

type runtimeMetrics struct {
	eventsProcessed                uint64
	reconcileRuns                  uint64
	reconcileFailures              uint64
	lastSuccessfulReconcileSeconds uint64
	reconcileStartedSeconds        uint64
	backendReady                   uint32
	reconcileInProgress            uint32
}

func (m *runtimeMetrics) setBackendReady(ready bool) {
	var value uint32
	if ready {
		value = 1
	}
	atomic.StoreUint32(&m.backendReady, value)
}

func (m *runtimeMetrics) checkBackend(backend backendProber) error {
	err := backend.Ping()
	m.setBackendReady(err == nil)
	return err
}

func (m *runtimeMetrics) reconcile(b reconcileBackend, quiet bool) bool {
	if !atomic.CompareAndSwapUint32(&m.reconcileInProgress, 0, 1) {
		log.Println("reconcile skipped because another reconcile is still running")
		return false
	}
	atomic.StoreUint64(&m.reconcileStartedSeconds, uint64(time.Now().Unix()))
	defer atomic.StoreUint32(&m.reconcileInProgress, 0)

	if err := m.checkBackend(b); err != nil {
		atomic.AddUint64(&m.reconcileFailures, 1)
		log.Printf("reconcile skipped because backend is not ready: %v", err)
		return false
	}
	if err := b.Sync(quiet); err != nil {
		atomic.AddUint64(&m.reconcileFailures, 1)
		log.Printf("reconcile failed: %v", err)
		return false
	}
	atomic.AddUint64(&m.reconcileRuns, 1)
	atomic.StoreUint64(&m.lastSuccessfulReconcileSeconds, uint64(time.Now().Unix()))
	return true
}

func reconcileStaleAfter(resyncIntervalSeconds int) time.Duration {
	if resyncIntervalSeconds <= 0 {
		return 0
	}
	maxAge := 2 * time.Duration(resyncIntervalSeconds) * time.Second
	if maxAge < time.Minute {
		return time.Minute
	}
	return maxAge
}

func (m *runtimeMetrics) checkReconcileFreshness(maxAge time.Duration) error {
	if maxAge <= 0 {
		return nil
	}
	now := time.Now()
	lastSuccess := atomic.LoadUint64(&m.lastSuccessfulReconcileSeconds)
	if lastSuccess == 0 {
		return fmt.Errorf("no successful reconcile completed")
	}
	if age := now.Sub(time.Unix(int64(lastSuccess), 0)); age > maxAge {
		return fmt.Errorf("last successful reconcile is stale: %s", age.Round(time.Second))
	}
	if atomic.LoadUint32(&m.reconcileInProgress) == 1 {
		started := atomic.LoadUint64(&m.reconcileStartedSeconds)
		if started > 0 {
			if age := now.Sub(time.Unix(int64(started), 0)); age > maxAge {
				return fmt.Errorf("reconcile has been running too long: %s", age.Round(time.Second))
			}
		}
	}
	return nil
}

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

func serveStatus(addr string, b *bridge.Bridge, runtime swarmRuntime, docker *dockerapi.Client, metrics *runtimeMetrics, reconcileMaxAge time.Duration, statusToken string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/readyz", readinessHandler(b, metrics, reconcileMaxAge))
	mux.HandleFunc("/metrics", requireStatusToken(statusToken, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "registrator_registered_services %d\n", b.ServiceCount())
		_, _ = fmt.Fprintf(w, "registrator_events_processed_total %d\n", atomic.LoadUint64(&metrics.eventsProcessed))
		_, _ = fmt.Fprintf(w, "registrator_reconcile_runs_total %d\n", atomic.LoadUint64(&metrics.reconcileRuns))
		_, _ = fmt.Fprintf(w, "registrator_reconcile_failures_total %d\n", atomic.LoadUint64(&metrics.reconcileFailures))
		_, _ = fmt.Fprintf(w, "registrator_reconcile_in_progress %d\n", atomic.LoadUint32(&metrics.reconcileInProgress))
		_, _ = fmt.Fprintf(w, "registrator_backend_ready %d\n", atomic.LoadUint32(&metrics.backendReady))
		_, _ = fmt.Fprintf(w, "registrator_last_successful_reconcile_timestamp_seconds %d\n", atomic.LoadUint64(&metrics.lastSuccessfulReconcileSeconds))
	}))
	mux.HandleFunc("/peerinfo", requireStatusToken(statusToken, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(runtime.peerInfo())
	}))
	mux.HandleFunc("/swarm/service/", requireStatusToken(statusToken, func(w http.ResponseWriter, req *http.Request) {
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
		service, err := inspectSwarmService(docker, serviceID)
		if err != nil {
			log.Printf("status swarm service inspect failed for %s: %v", serviceID, err)
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(service)
	}))
	warnIfUnprotectedStatus(addr, statusToken)
	log.Printf("Serving status endpoints on %s", addr)
	startPeerDiscovery(runtime, addr, statusToken, func(info peerInfo) {
		if info.Role != "manager" {
			return
		}
		metrics.reconcile(b, true)
	})
	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Printf("status server stopped: %v", err)
	}
}

func readinessHandler(backend backendProber, metrics *runtimeMetrics, reconcileMaxAge time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if err := metrics.checkBackend(backend); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		if err := metrics.checkReconcileFreshness(reconcileMaxAge); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func requireStatusToken(token string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if token != "" && !statusTokenAuthorized(req, token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, req)
	}
}

func statusTokenAuthorized(req *http.Request, token string) bool {
	if constantTimeEqual(req.Header.Get(statusTokenHeader), token) {
		return true
	}
	auth := req.Header.Get("Authorization")
	const prefix = "Bearer "
	return strings.HasPrefix(auth, prefix) && constantTimeEqual(strings.TrimPrefix(auth, prefix), token)
}

func constantTimeEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func addStatusToken(req *http.Request, token string) {
	if token == "" {
		return
	}
	req.Header.Set(statusTokenHeader, token)
	req.Header.Set("Authorization", "Bearer "+token)
}

func warnIfUnprotectedStatus(addr, token string) {
	if token != "" {
		return
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		log.Printf("warning: status endpoints on %s are not token-protected", addr)
		return
	}
	if strings.EqualFold(host, "localhost") {
		return
	}
	if ip := net.ParseIP(host); ip != nil {
		if !ip.IsLoopback() {
			log.Printf("warning: status endpoints on %s are not token-protected", addr)
		}
		return
	}
	log.Printf("warning: status endpoints on %s are not token-protected", addr)
}

func startPeerDiscovery(runtime swarmRuntime, addr, statusToken string, onPeerDiscovered func(peerInfo)) {
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
		discoverPeers(peerHost, port, runtime.OverlayIP, statusToken, onPeerDiscovered)
		for range ticker.C {
			discoverPeers(peerHost, port, runtime.OverlayIP, statusToken, onPeerDiscovered)
		}
	}()
}

func discoverPeers(peerHost, port, selfOverlayIP, statusToken string, onPeerDiscovered func(peerInfo)) {
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
		info, err := fetchPeerInfo(client, url, statusToken)
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

func fetchPeerInfo(client *http.Client, url, statusToken string) (peerInfo, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return peerInfo{}, err
	}
	addStatusToken(req, statusToken)
	resp, err := client.Do(req)
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
