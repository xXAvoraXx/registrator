package consul

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/registrator/bridge"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/stretchr/testify/assert"
)

func TestSelectSharedNetworkIPReturnsSharedNetworkAddress(t *testing.T) {
	registrator := &dockerapi.Container{
		NetworkSettings: &dockerapi.NetworkSettings{
			Networks: map[string]dockerapi.ContainerNetwork{
				"edge": {IPAddress: "10.10.0.2"},
				"db":   {IPAddress: "10.20.0.2"},
			},
		},
	}
	candidate := &dockerapi.Container{
		NetworkSettings: &dockerapi.NetworkSettings{
			Networks: map[string]dockerapi.ContainerNetwork{
				"db":       {IPAddress: "10.20.0.9"},
				"internal": {IPAddress: "172.18.0.3"},
			},
		},
	}

	ip := selectSharedNetworkIP(containerNetworkNames(registrator), candidate)
	assert.Equal(t, "10.20.0.9", ip)
}

func TestSelectSharedNetworkIPReturnsEmptyWhenNoSharedNetwork(t *testing.T) {
	registrator := &dockerapi.Container{
		NetworkSettings: &dockerapi.NetworkSettings{
			Networks: map[string]dockerapi.ContainerNetwork{
				"edge": {IPAddress: "10.10.0.2"},
			},
		},
	}
	candidate := &dockerapi.Container{
		NetworkSettings: &dockerapi.NetworkSettings{
			Networks: map[string]dockerapi.ContainerNetwork{
				"internal": {IPAddress: "172.18.0.3"},
			},
		},
	}

	ip := selectSharedNetworkIP(containerNetworkNames(registrator), candidate)
	assert.Equal(t, "", ip)
}

func TestResolveAddressFallsBackWhenDockerResolveFails(t *testing.T) {
	originalRuntimeConfig := runtimeConfig
	originalRuntimeDockerClient := runtimeDockerClient
	defer func() {
		runtimeConfig = originalRuntimeConfig
		runtimeDockerClient = originalRuntimeDockerClient
	}()

	docker, err := dockerapi.NewClient("unix:///tmp/registrator-missing-docker.sock")
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}

	runtimeConfig = RuntimeConfig{
		Mode:             "local",
		Port:             8500,
		UseDockerResolve: true,
	}
	runtimeDockerClient = docker

	adapter := &ConsulAdapter{
		baseConfig: &consulapi.Config{Address: "127.0.0.1:8500"},
	}

	address, err := adapter.resolveAddress(nil)
	assert.NoError(t, err)
	assert.Equal(t, "127.0.0.1:8500", address)
}

func TestResolveAddressRejectsFallbackWhenLocalAgentIsRequired(t *testing.T) {
	originalRuntimeConfig := runtimeConfig
	originalRuntimeDockerClient := runtimeDockerClient
	defer func() {
		runtimeConfig = originalRuntimeConfig
		runtimeDockerClient = originalRuntimeDockerClient
	}()

	docker, err := dockerapi.NewClient("unix:///tmp/registrator-missing-docker.sock")
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}

	runtimeConfig = RuntimeConfig{
		Mode:              "local",
		Port:              8500,
		UseDockerResolve:  true,
		RequireLocalAgent: true,
	}
	runtimeDockerClient = docker

	adapter := &ConsulAdapter{
		baseConfig: &consulapi.Config{Address: "127.0.0.1:8500"},
	}

	address, err := adapter.resolveAddress(nil)
	assert.Error(t, err)
	assert.Empty(t, address)
	assert.Contains(t, err.Error(), "strict local agent resolution failed")
}

func TestStrictLocalReadiness(t *testing.T) {
	const agentMemberFailed = 4

	originalRuntimeConfig := runtimeConfig
	originalRuntimeDockerClient := runtimeDockerClient
	defer func() {
		runtimeConfig = originalRuntimeConfig
		runtimeDockerClient = originalRuntimeDockerClient
	}()

	tests := []struct {
		name           string
		leader         string
		role           string
		memberAddress  string
		memberStatus   int
		catalogNode    bool
		catalogAddress string
		errorContains  string
	}{
		{name: "ready", leader: "100.101.0.1:8300", role: "node", memberAddress: "100.101.0.3", memberStatus: agentMemberAlive, catalogNode: true, catalogAddress: "100.101.0.3"},
		{name: "empty leader", role: "node", memberAddress: "100.101.0.3", memberStatus: agentMemberAlive, catalogNode: true, catalogAddress: "100.101.0.3", errorContains: "no current leader"},
		{name: "server role", leader: "100.101.0.1:8300", role: "consul", memberAddress: "100.101.0.3", memberStatus: agentMemberAlive, catalogNode: true, catalogAddress: "100.101.0.3", errorContains: "expected \"node\""},
		{name: "wrong address", leader: "100.101.0.1:8300", role: "node", memberAddress: "100.101.0.2", memberStatus: agentMemberAlive, catalogNode: true, catalogAddress: "100.101.0.2", errorContains: "does not match swarm node address"},
		{name: "failed member", leader: "100.101.0.1:8300", role: "node", memberAddress: "100.101.0.3", memberStatus: agentMemberFailed, catalogNode: true, catalogAddress: "100.101.0.3", errorContains: "is not alive"},
		{name: "missing catalog node", leader: "100.101.0.1:8300", role: "node", memberAddress: "100.101.0.3", memberStatus: agentMemberAlive, errorContains: "is not present in the catalog"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v1/status/leader":
					_ = json.NewEncoder(w).Encode(tt.leader)
				case r.URL.Path == "/v1/agent/self":
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"Config": map[string]interface{}{"NodeName": "agent-node-3"},
						"Member": map[string]interface{}{
							"Name":   "agent-node-3",
							"Addr":   tt.memberAddress,
							"Status": tt.memberStatus,
							"Tags":   map[string]string{"role": tt.role},
						},
					})
				case r.URL.Path == "/v1/catalog/node/agent-node-3":
					if !tt.catalogNode {
						_ = json.NewEncoder(w).Encode(nil)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]interface{}{
						"Node": map[string]interface{}{
							"Node":    "agent-node-3",
							"Address": tt.catalogAddress,
						},
						"Services": map[string]interface{}{},
					})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			u, err := url.Parse(server.URL)
			if err != nil {
				t.Fatalf("failed to parse server URL: %v", err)
			}
			runtimeConfig = RuntimeConfig{
				Mode:              "local",
				Address:           u.Host,
				Port:              8500,
				RequireLocalAgent: true,
				LocalNodeAddress:  "100.101.0.3",
			}
			adapter := &ConsulAdapter{baseConfig: &consulapi.Config{Address: u.Host, Scheme: u.Scheme}}

			err = adapter.Ping()
			if tt.errorContains == "" {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.errorContains)
		})
	}
}

func TestBuildCheckUsesCheckHTTPPortOverride(t *testing.T) {
	adapter := &ConsulAdapter{}
	service := &bridge.Service{
		IP:   "10.0.0.5",
		Port: 9090,
		Attrs: map[string]string{
			"check_http":      "/healthz",
			"check_http_port": "8080",
		},
	}

	check := adapter.buildCheck(service)
	assert.Equal(t, "http://10.0.0.5:8080/healthz", check.HTTP)
}

func TestServicesPreservesAgentServiceMeta(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/agent/checks" {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
			return
		}
		if r.URL.Path != "/v1/agent/services" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"svc-1": map[string]interface{}{
				"ID":      "svc-1",
				"Service": "api",
				"Tags":    []string{"registrator"},
				"Address": "10.0.1.20",
				"Port":    3000,
				"Meta": map[string]string{
					"registrator_host": "worker-1",
				},
			},
		})
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("failed to parse server URL: %v", err)
	}
	adapter := &ConsulAdapter{baseConfig: &consulapi.Config{Address: u.Host, Scheme: u.Scheme}}

	services, err := adapter.Services()
	assert.NoError(t, err)
	if assert.Len(t, services, 1) {
		assert.Equal(t, "worker-1", services[0].Attrs["registrator_host"])
	}
}

func TestServicesMarksMissingServiceCheckForRepair(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agent/services":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"svc-1": map[string]interface{}{
					"ID":      "svc-1",
					"Service": "api",
					"Tags":    []string{"registrator"},
					"Address": "10.0.1.20",
					"Port":    3000,
					"Meta": map[string]string{
						"check_http": "/healthz",
					},
				},
			})
		case "/v1/agent/checks":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("failed to parse server URL: %v", err)
	}
	adapter := &ConsulAdapter{baseConfig: &consulapi.Config{Address: u.Host, Scheme: u.Scheme}}

	services, err := adapter.Services()
	assert.NoError(t, err)
	if assert.Len(t, services, 1) {
		assert.Equal(t, "true", services[0].Attrs[missingServiceCheckAttr])
	}
}

func TestServicesDoesNotMarkExistingServiceCheckForRepair(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/agent/services":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"svc-1": map[string]interface{}{
					"ID":      "svc-1",
					"Service": "api",
					"Tags":    []string{"registrator"},
					"Address": "10.0.1.20",
					"Port":    3000,
					"Meta": map[string]string{
						"check_tcp": "true",
					},
				},
			})
		case "/v1/agent/checks":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"service:svc-1": map[string]interface{}{
					"CheckID":   "service:svc-1",
					"ServiceID": "svc-1",
					"Status":    "passing",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("failed to parse server URL: %v", err)
	}
	adapter := &ConsulAdapter{baseConfig: &consulapi.Config{Address: u.Host, Scheme: u.Scheme}}

	services, err := adapter.Services()
	assert.NoError(t, err)
	if assert.Len(t, services, 1) {
		assert.Empty(t, services[0].Attrs[missingServiceCheckAttr])
	}
}
