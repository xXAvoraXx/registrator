package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
)

type fakeBackendProber struct {
	err error
}

func (f fakeBackendProber) Ping() error {
	return f.err
}

type fakeReconcileBackend struct {
	err       error
	syncCalls int
}

func (f *fakeReconcileBackend) Ping() error {
	return f.err
}

func (f *fakeReconcileBackend) Sync(bool) {
	f.syncCalls++
}

func TestDetectSwarmRuntimeReadsSwarmTaskLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/info"):
			_, _ = w.Write([]byte(`{"Swarm":{"LocalNodeState":"active","NodeID":"info-node","NodeAddr":"10.0.0.2","ControlAvailable":true}}`))
		case strings.HasPrefix(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = w.Write([]byte(`{"Config":{"Labels":{"com.docker.swarm.service.id":"svc-1","com.docker.swarm.service.name":"registrator","com.docker.swarm.task.id":"task-1","com.docker.swarm.node.id":"label-node"}},"NetworkSettings":{"Networks":{"ingress":{"IPAddress":"10.0.1.172"}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	docker, err := dockerapi.NewClient(server.URL)
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	runtime := detectSwarmRuntime(docker)
	if !runtime.Enabled || !runtime.RunningAsService {
		t.Fatalf("expected swarm service runtime, got %+v", runtime)
	}
	if runtime.SwarmServiceID != "svc-1" || runtime.SwarmServiceName != "registrator" || runtime.SwarmTaskID != "task-1" {
		t.Fatalf("unexpected swarm labels: %+v", runtime)
	}
	if runtime.NodeID != "info-node" {
		t.Fatalf("expected node id from swarm info, got %q", runtime.NodeID)
	}
	if runtime.OverlayIP != "10.0.1.172" {
		t.Fatalf("expected overlay ip from network settings, got %q", runtime.OverlayIP)
	}
	if runtime.Role != "manager" {
		t.Fatalf("expected manager role, got %q", runtime.Role)
	}
}

func TestFetchPeerInfoParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(peerInfo{
			ServiceID:   "svc-1",
			ServiceName: "registrator",
			TaskID:      "task-1",
			NodeID:      "node-1",
			Hostname:    "host-1",
			OverlayIP:   "10.0.1.172",
			Role:        "worker",
		})
	}))
	defer server.Close()

	info, err := fetchPeerInfo(server.Client(), server.URL+"/peerinfo", "")
	if err != nil {
		t.Fatalf("fetchPeerInfo returned error: %v", err)
	}
	if info.ServiceName != "registrator" || info.TaskID != "task-1" || info.Role != "worker" {
		t.Fatalf("unexpected peer info payload: %+v", info)
	}
}

func TestStatusTokenMiddlewareRequiresToken(t *testing.T) {
	handler := requireStatusToken("secret", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	resp := httptest.NewRecorder()
	handler(resp, req)
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized without token, got %d", resp.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set(statusTokenHeader, "secret")
	resp = httptest.NewRecorder()
	handler(resp, req)
	if resp.Code != http.StatusNoContent {
		t.Fatalf("expected accepted token, got %d", resp.Code)
	}
}

func TestReadinessHandlerTracksStrictBackendState(t *testing.T) {
	metrics := &runtimeMetrics{}
	handler := readinessHandler(fakeBackendProber{err: errors.New("local agent missing")}, metrics)

	resp := httptest.NewRecorder()
	handler(resp, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if resp.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected unavailable readiness, got %d", resp.Code)
	}
	if got := atomic.LoadUint32(&metrics.backendReady); got != 0 {
		t.Fatalf("expected backend_ready=0, got %d", got)
	}

	handler = readinessHandler(fakeBackendProber{}, metrics)
	resp = httptest.NewRecorder()
	handler(resp, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if resp.Code != http.StatusOK {
		t.Fatalf("expected ready status, got %d", resp.Code)
	}
	if got := atomic.LoadUint32(&metrics.backendReady); got != 1 {
		t.Fatalf("expected backend_ready=1, got %d", got)
	}
}

func TestReconcileMetricsTrackFailureAndRecovery(t *testing.T) {
	metrics := &runtimeMetrics{}
	backend := &fakeReconcileBackend{err: errors.New("local agent missing")}

	if metrics.reconcile(backend, true) {
		t.Fatal("expected reconcile to be skipped while backend is unavailable")
	}
	if backend.syncCalls != 0 {
		t.Fatalf("expected no sync while unavailable, got %d", backend.syncCalls)
	}
	if got := atomic.LoadUint64(&metrics.reconcileFailures); got != 1 {
		t.Fatalf("expected one reconcile failure, got %d", got)
	}

	backend.err = nil
	if !metrics.reconcile(backend, true) {
		t.Fatal("expected reconcile after backend recovery")
	}
	if backend.syncCalls != 1 {
		t.Fatalf("expected one sync after recovery, got %d", backend.syncCalls)
	}
	if got := atomic.LoadUint64(&metrics.reconcileRuns); got != 1 {
		t.Fatalf("expected one successful reconcile, got %d", got)
	}
	if got := atomic.LoadUint64(&metrics.lastSuccessfulReconcileSeconds); got == 0 {
		t.Fatal("expected successful reconcile timestamp")
	}
}

func TestFetchPeerInfoSendsStatusToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(statusTokenHeader) != "secret" {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(peerInfo{Role: "manager"})
	}))
	defer server.Close()

	info, err := fetchPeerInfo(server.Client(), server.URL+"/peerinfo", "secret")
	if err != nil {
		t.Fatalf("fetchPeerInfo returned error: %v", err)
	}
	if info.Role != "manager" {
		t.Fatalf("unexpected peer info payload: %+v", info)
	}
}

func TestDiscoverPeersCallsCallbackOncePerPeerSignature(t *testing.T) {
	previousState := peerDiscoveryLogState
	peerDiscoveryLogState = sync.Map{}
	t.Cleanup(func() {
		peerDiscoveryLogState = previousState
	})
	previousManagers := discoveredManagerAddrState
	discoveredManagerAddrState = sync.Map{}
	t.Cleanup(func() {
		discoveredManagerAddrState = previousManagers
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(peerInfo{
			ServiceID:   "svc-1",
			ServiceName: "registrator",
			TaskID:      "task-1",
			NodeID:      "node-1",
			Hostname:    "host-1",
			OverlayIP:   "10.0.1.172",
			Role:        "manager",
		})
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("failed to parse server URL: %v", err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("failed to parse host and port: %v", err)
	}

	var callbackCalls int32
	onPeerDiscovered := func(info peerInfo) {
		if info.Role == "manager" {
			atomic.AddInt32(&callbackCalls, 1)
		}
	}

	discoverPeers(host, port, "", "", onPeerDiscovered)
	discoverPeers(host, port, "", "", onPeerDiscovered)

	if got := atomic.LoadInt32(&callbackCalls); got != 1 {
		t.Fatalf("expected callback to run once for identical manager signature, got %d", got)
	}
	addrs := discoveredManagerAddrs()
	if len(addrs) != 2 {
		t.Fatalf("expected overlay and peer manager addresses to be recorded, got %+v", addrs)
	}
	if !(addrs[0] == "10.0.1.172" || addrs[1] == "10.0.1.172") || !(addrs[0] == host || addrs[1] == host) {
		t.Fatalf("unexpected discovered manager addresses: %+v", addrs)
	}
}

func TestForgetManagerAddrRemovesDiscoveredManager(t *testing.T) {
	previousManagers := discoveredManagerAddrState
	discoveredManagerAddrState = sync.Map{}
	t.Cleanup(func() {
		discoveredManagerAddrState = previousManagers
	})

	rememberManagerAddr("10.0.1.62")
	forgetManagerAddr("10.0.1.62")

	addrs := discoveredManagerAddrs()
	if len(addrs) != 0 {
		t.Fatalf("expected discovered manager cache to remove forgotten address, got %+v", addrs)
	}
}
