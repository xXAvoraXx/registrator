package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	swarmapi "github.com/docker/docker/api/types/swarm"
	dockerapi "github.com/fsouza/go-dockerclient"
)

func TestInspectServiceNoManagerAddress(t *testing.T) {
	docker, err := dockerapi.NewClient("unix:///tmp/registrator-missing-docker.sock")
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	resolver := newSwarmPortResolver(docker, swarmRuntime{Role: "worker"}, "", "", 2375, "")

	_, err = resolver.inspectService("service-id")
	if err == nil {
		t.Fatalf("expected inspectService error")
	}
	if !strings.Contains(err.Error(), "no manager node address discovered") {
		t.Fatalf("expected actionable manager discovery error, got: %v", err)
	}
}

func TestManagerStatusAddrStripsPort(t *testing.T) {
	if got := managerStatusAddr("10.0.0.10:2377"); got != "10.0.0.10" {
		t.Fatalf("expected host without port, got %q", got)
	}
}

func TestServiceNetworksInfoUsesServiceVIPNetworks(t *testing.T) {
	container := &dockerapi.Container{
		NetworkSettings: &dockerapi.NetworkSettings{
			Networks: map[string]dockerapi.ContainerNetwork{
				"ingress": {IPAddress: "10.0.9.10", NetworkID: "ingress-id"},
				"app":     {IPAddress: "10.0.1.20", NetworkID: "app-id"},
			},
		},
	}
	service := &swarmapi.Service{
		Endpoint: swarmapi.Endpoint{
			VirtualIPs: []swarmapi.EndpointVirtualIP{
				{Addr: "10.0.1.2/24", NetworkID: "app-id"},
			},
		},
	}

	networks := serviceNetworksInfo(container, service)
	if len(networks) != 1 {
		t.Fatalf("expected one network result, got %v", networks)
	}
	if networks[0].name != "app" || networks[0].ip != "10.0.1.20" {
		t.Fatalf("expected app network info, got %+v", networks[0])
	}
}

func TestManagerAddrsFromNodesUsesManagerRoleWhenManagerStatusMissing(t *testing.T) {
	nodes := []swarmapi.Node{
		{
			Spec:   swarmapi.NodeSpec{Role: swarmapi.NodeRoleManager},
			Status: swarmapi.NodeStatus{Addr: "10.0.1.10"},
		},
		{
			Spec:   swarmapi.NodeSpec{Role: swarmapi.NodeRoleWorker},
			Status: swarmapi.NodeStatus{Addr: "10.0.1.20"},
		},
	}

	addrs := managerAddrsFromNodes(nodes)
	if len(addrs) != 1 || addrs[0] != "10.0.1.10" {
		t.Fatalf("expected manager address from manager role, got %+v", addrs)
	}
}

func TestInspectServiceWorkerLocalFirstThenManagerFallback(t *testing.T) {
	var serviceCalls int32
	localDocker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/version"):
			_ = json.NewEncoder(w).Encode(map[string]string{"ApiVersion": "1.41"})
		case strings.Contains(r.URL.Path, "/services/service-id"):
			atomic.AddInt32(&serviceCalls, 1)
			service := swarmapi.Service{
				ID: "service-id",
				Spec: swarmapi.ServiceSpec{
					EndpointSpec: &swarmapi.EndpointSpec{},
				},
			}
			_ = json.NewEncoder(w).Encode(service)
		case strings.Contains(r.URL.Path, "/nodes"):
			nodes := []swarmapi.Node{
				{
					Spec:   swarmapi.NodeSpec{Role: swarmapi.NodeRoleManager},
					Status: swarmapi.NodeStatus{Addr: "127.0.0.1"},
				},
			}
			_ = json.NewEncoder(w).Encode(nodes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer localDocker.Close()

	peerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/swarm/service/service-id" {
			http.NotFound(w, r)
			return
		}
		service := swarmapi.Service{
			ID: "service-id",
			Spec: swarmapi.ServiceSpec{
				EndpointSpec: &swarmapi.EndpointSpec{
					Ports: []swarmapi.PortConfig{
						{PublishedPort: 5432, TargetPort: 5432, Protocol: swarmapi.PortConfigProtocolTCP},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(service)
	}))
	defer peerServer.Close()

	u, err := url.Parse(peerServer.URL)
	if err != nil {
		t.Fatalf("failed to parse server URL: %v", err)
	}
	peerPort := u.Port()
	port, err := strconv.Atoi(peerPort)
	if err != nil {
		t.Fatalf("failed to parse server port: %v", err)
	}
	docker, err := dockerapi.NewVersionedClient(localDocker.URL, defaultDockerAPIVersion)
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	resolver := newSwarmPortResolver(docker, swarmRuntime{Role: "worker"}, "", "", port, peerPort)
	var buf bytes.Buffer
	previousLogWriter := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(previousLogWriter)
	})

	service, err := resolver.inspectService("service-id")
	if err != nil {
		t.Fatalf("expected manager fallback inspect success, got: %v", err)
	}
	if service.Spec.EndpointSpec == nil || len(service.Spec.EndpointSpec.Ports) != 1 {
		t.Fatalf("expected manager fallback to return service with ports, got: %+v", service.Spec.EndpointSpec)
	}
	if got := atomic.LoadInt32(&serviceCalls); got != 1 {
		t.Fatalf("expected local inspect only once before peer fallback, got %d", got)
	}
	logOutput := buf.String()
	attemptLog := fmt.Sprintf("swarm manager handshake: attempting manager peer 127.0.0.1:%s for service service-id", peerPort)
	if !strings.Contains(logOutput, attemptLog) {
		t.Fatalf("expected handshake attempt log, got: %s", logOutput)
	}
	successLog := fmt.Sprintf("swarm manager handshake: manager peer 127.0.0.1:%s reachable for service service-id", peerPort)
	if !strings.Contains(logOutput, successLog) {
		t.Fatalf("expected handshake success log, got: %s", logOutput)
	}
}

func TestInspectServiceFallsBackToManagerPeerWhenDockerAPIUnavailable(t *testing.T) {
	peerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/swarm/service/service-id" {
			http.NotFound(w, r)
			return
		}
		service := swarmapi.Service{
			ID: "service-id",
			Spec: swarmapi.ServiceSpec{
				EndpointSpec: &swarmapi.EndpointSpec{
					Ports: []swarmapi.PortConfig{
						{PublishedPort: 5432, TargetPort: 5432, Protocol: swarmapi.PortConfigProtocolTCP},
					},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(service)
	}))
	defer peerServer.Close()

	peerURL, err := url.Parse(peerServer.URL)
	if err != nil {
		t.Fatalf("failed to parse peer server URL: %v", err)
	}
	_, peerPort, err := net.SplitHostPort(peerURL.Host)
	if err != nil {
		t.Fatalf("failed to parse peer server port: %v", err)
	}

	localDocker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/version"):
			_ = json.NewEncoder(w).Encode(map[string]string{"ApiVersion": "1.41"})
		case strings.Contains(r.URL.Path, "/services/service-id"):
			service := swarmapi.Service{
				ID: "service-id",
				Spec: swarmapi.ServiceSpec{
					EndpointSpec: &swarmapi.EndpointSpec{},
				},
			}
			_ = json.NewEncoder(w).Encode(service)
		case strings.Contains(r.URL.Path, "/nodes"):
			nodes := []swarmapi.Node{
				{
					Spec:   swarmapi.NodeSpec{Role: swarmapi.NodeRoleManager},
					Status: swarmapi.NodeStatus{Addr: "127.0.0.1"},
				},
			}
			_ = json.NewEncoder(w).Encode(nodes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer localDocker.Close()

	docker, err := dockerapi.NewVersionedClient(localDocker.URL, defaultDockerAPIVersion)
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	resolver := newSwarmPortResolver(docker, swarmRuntime{Role: "worker"}, "", "", 0, peerPort)
	service, err := resolver.inspectService("service-id")
	if err != nil {
		t.Fatalf("expected peer fallback inspect success, got: %v", err)
	}
	if service.Spec.EndpointSpec == nil || len(service.Spec.EndpointSpec.Ports) != 1 {
		t.Fatalf("expected manager peer fallback to return service with ports, got: %+v", service.Spec.EndpointSpec)
	}
}

func TestInspectServiceViaPeerReturnsErrorOnNonOKStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
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

	docker, err := dockerapi.NewClient("unix:///tmp/registrator-missing-docker.sock")
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	resolver := newSwarmPortResolver(docker, swarmRuntime{Role: "worker"}, "", "", 2375, port)
	_, err = resolver.inspectServiceViaPeer(host, "service-id")
	if err == nil || !strings.Contains(err.Error(), "status 502") {
		t.Fatalf("expected non-200 status error, got: %v", err)
	}
}

func TestServiceHasPublishedPorts(t *testing.T) {
	if serviceHasPublishedPorts(nil) {
		t.Fatalf("expected nil service to return false")
	}
	if !serviceHasPublishedPorts(&swarmapi.Service{
		Spec: swarmapi.ServiceSpec{
			EndpointSpec: &swarmapi.EndpointSpec{
				Ports: []swarmapi.PortConfig{{PublishedPort: 80, TargetPort: 8080}},
			},
		},
	}) {
		t.Fatalf("expected ports in Spec.EndpointSpec to return true")
	}
	if !serviceHasPublishedPorts(&swarmapi.Service{
		Endpoint: swarmapi.Endpoint{
			Ports: []swarmapi.PortConfig{{PublishedPort: 443, TargetPort: 8443}},
		},
	}) {
		t.Fatalf("expected ports in Endpoint to return true")
	}
}

func TestManagerNodeAddrsFallsBackToDiscoveredPeers(t *testing.T) {
	previousManagers := discoveredManagerAddrState
	discoveredManagerAddrState = sync.Map{}
	t.Cleanup(func() {
		discoveredManagerAddrState = previousManagers
	})
	rememberManagerAddr("10.0.1.44")

	docker, err := dockerapi.NewClient("unix:///tmp/registrator-missing-docker.sock")
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	resolver := newSwarmPortResolver(docker, swarmRuntime{Role: "worker"}, "", "", 2375, "")
	addrs := resolver.managerNodeAddrs()
	if len(addrs) != 1 || addrs[0] != "10.0.1.44" {
		t.Fatalf("expected discovered manager address fallback, got %+v", addrs)
	}
}

func TestManagerNodeAddrsFallsBackToTaskDNSWhenManagersUnknown(t *testing.T) {
	previousLookupIP := lookupIP
	lookupIP = func(host string) ([]net.IP, error) {
		if host != "tasks.registrator" {
			t.Fatalf("unexpected lookup host %q", host)
		}
		return []net.IP{
			net.ParseIP("10.0.1.56"),
			net.ParseIP("10.0.1.57"),
		}, nil
	}
	t.Cleanup(func() {
		lookupIP = previousLookupIP
	})

	docker, err := dockerapi.NewClient("unix:///tmp/registrator-missing-docker.sock")
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	resolver := newSwarmPortResolver(docker, swarmRuntime{
		Role:             "worker",
		RunningAsService: true,
		SwarmServiceName: "registrator",
		OverlayIP:        "10.0.1.57",
	}, "", "", 2375, "")
	addrs := resolver.managerNodeAddrs()
	if len(addrs) != 1 || addrs[0] != "10.0.1.56" {
		t.Fatalf("expected task DNS fallback manager candidates, got %+v", addrs)
	}
}

func TestManagerAddrsFromTaskDNSPrefersManagerNodeAddrFromPeerInfo(t *testing.T) {
	previousLookupIP := lookupIP
	lookupIP = func(host string) ([]net.IP, error) {
		if host != "tasks.registrator" {
			t.Fatalf("unexpected lookup host %q", host)
		}
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	t.Cleanup(func() {
		lookupIP = previousLookupIP
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(peerInfo{
			ServiceID:   "svc-1",
			ServiceName: "registrator",
			TaskID:      "task-1",
			NodeID:      "node-1",
			NodeAddr:    "100.101.0.70",
			Hostname:    "manager-1",
			OverlayIP:   "10.0.1.70",
			Role:        "manager",
		})
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("failed to parse server URL: %v", err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatalf("failed to parse host and port: %v", err)
	}

	docker, err := dockerapi.NewClient("unix:///tmp/registrator-missing-docker.sock")
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	resolver := newSwarmPortResolver(docker, swarmRuntime{
		Role:             "worker",
		RunningAsService: true,
		SwarmServiceName: "registrator",
	}, "", "", 2375, port)
	addrs := resolver.managerAddrsFromTaskDNS()
	if len(addrs) != 2 {
		t.Fatalf("expected manager node+overlay addresses from peer info, got %+v", addrs)
	}
	got := map[string]struct{}{}
	for _, addr := range addrs {
		got[addr] = struct{}{}
	}
	if _, ok := got["10.0.1.70"]; !ok {
		t.Fatalf("expected manager overlay address in result, got %+v", addrs)
	}
	if _, ok := got["100.101.0.70"]; !ok {
		t.Fatalf("expected manager node address in result, got %+v", addrs)
	}
}
