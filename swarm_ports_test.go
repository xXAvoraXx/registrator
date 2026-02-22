package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
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
	resolver := newSwarmPortResolver(docker, swarmRuntime{Role: "worker"}, "", "", 2375)

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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/version"):
			_, _ = w.Write([]byte(`{"ApiVersion":"1.41"}`))
		case strings.Contains(r.URL.Path, "/services/service-id"):
			call := atomic.AddInt32(&serviceCalls, 1)
			service := swarmapi.Service{
				ID: "service-id",
				Spec: swarmapi.ServiceSpec{
					EndpointSpec: &swarmapi.EndpointSpec{},
				},
			}
			if call > 1 {
				service.Spec.EndpointSpec.Ports = []swarmapi.PortConfig{
					{PublishedPort: 5432, TargetPort: 5432, Protocol: swarmapi.PortConfigProtocolTCP},
				}
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
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("failed to parse server URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("failed to parse server port: %v", err)
	}
	docker, err := dockerapi.NewVersionedClient(server.URL, defaultDockerAPIVersion)
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	resolver := newSwarmPortResolver(docker, swarmRuntime{Role: "worker"}, "", "", port)

	service, err := resolver.inspectService("service-id")
	if err != nil {
		t.Fatalf("expected manager fallback inspect success, got: %v", err)
	}
	if service.Spec.EndpointSpec == nil || len(service.Spec.EndpointSpec.Ports) != 1 {
		t.Fatalf("expected manager fallback to return service with ports, got: %+v", service.Spec.EndpointSpec)
	}
	if got := atomic.LoadInt32(&serviceCalls); got != 2 {
		t.Fatalf("expected local inspect then manager inspect (2 calls), got %d", got)
	}
}
