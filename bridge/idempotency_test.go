package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
)

type countingRegistryAdapter struct {
	mu            sync.Mutex
	registerCalls int
	registeredIPs []string
}

func (c *countingRegistryAdapter) Ping() error { return nil }
func (c *countingRegistryAdapter) Register(service *Service) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.registerCalls++
	c.registeredIPs = append(c.registeredIPs, service.IP)
	return nil
}
func (c *countingRegistryAdapter) Deregister(service *Service) error { return nil }
func (c *countingRegistryAdapter) Refresh(service *Service) error    { return nil }
func (c *countingRegistryAdapter) Services() ([]*Service, error)     { return nil, nil }

func TestServiceHashStableForTagOrder(t *testing.T) {
	svc1 := &Service{
		ID:    "id-1",
		Name:  "svc",
		IP:    "10.0.0.1",
		Port:  8080,
		Tags:  []string{"b", "a"},
		Attrs: map[string]string{"k1": "v1", "k2": "v2"},
		TTL:   10,
	}
	svc2 := &Service{
		ID:    "id-1",
		Name:  "svc",
		IP:    "10.0.0.1",
		Port:  8080,
		Tags:  []string{"a", "b"},
		Attrs: map[string]string{"k2": "v2", "k1": "v1"},
		TTL:   10,
	}

	assert.Equal(t, serviceHash(svc1), serviceHash(svc2))
}

func TestServiceHashChangesWhenServiceChanges(t *testing.T) {
	svc1 := &Service{ID: "id-1", Name: "svc", IP: "10.0.0.1", Port: 8080}
	svc2 := &Service{ID: "id-1", Name: "svc", IP: "10.0.0.1", Port: 9090}

	assert.NotEqual(t, serviceHash(svc1), serviceHash(svc2))
}

func TestDuplicateServiceIDsRemovesDuplicateBySignature(t *testing.T) {
	services := []*Service{
		{ID: "id-1", Name: "svc", IP: "10.0.0.1", Port: 8080, Tags: []string{"a"}},
		{ID: "id-2", Name: "svc", IP: "10.0.0.1", Port: 8080, Tags: []string{"a"}},
		{ID: "id-3", Name: "svc", IP: "10.0.0.1", Port: 9090, Tags: []string{"a"}},
	}

	duplicates := duplicateServiceIDs(services, map[string]struct{}{})
	assert.Equal(t, []string{"id-2"}, duplicates)
}

func TestDuplicateServiceIDsPrefersKnownLocalServiceID(t *testing.T) {
	services := []*Service{
		{ID: "old-id", Name: "svc", IP: "10.0.0.1", Port: 8080, Tags: []string{"a"}},
		{ID: "current-id", Name: "svc", IP: "10.0.0.1", Port: 8080, Tags: []string{"a"}},
	}

	duplicates := duplicateServiceIDs(services, map[string]struct{}{"current-id": {}})
	assert.Equal(t, []string{"old-id"}, duplicates)
}

func TestSeedServiceHashesPruneStaleHashEntries(t *testing.T) {
	adapter := &countingRegistryAdapter{}
	b := &Bridge{
		registry:      adapter,
		serviceHashes: map[string]string{},
	}
	service := &Service{ID: "svc-1", Name: "svc", IP: "10.0.0.1", Port: 8080}
	b.serviceHashes[service.ID] = serviceHash(service)
	b.serviceHashes["stale"] = "stale-hash"

	b.seedServiceHashes(nil)
	assert.Empty(t, b.serviceHashes)

	assert.NoError(t, b.registerService(service))
	assert.Equal(t, 1, adapter.registerCalls)
}

func TestAddRebuildsExistingContainerServices(t *testing.T) {
	containerID := "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcd"
	currentIP := "10.1.0.2"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/containers/"+containerID+"/json" {
			http.NotFound(w, r)
			return
		}
		container := dockerapi.Container{
			ID:   containerID,
			Name: "/demo",
			Config: &dockerapi.Config{
				Image:        "demo:latest",
				ExposedPorts: map[dockerapi.Port]struct{}{"80/tcp": {}},
			},
			HostConfig: &dockerapi.HostConfig{NetworkMode: "bridge"},
			NetworkSettings: &dockerapi.NetworkSettings{
				IPAddress: currentIP,
				Ports: map[dockerapi.Port][]dockerapi.PortBinding{
					"80/tcp": {{HostIP: "0.0.0.0", HostPort: "8080"}},
				},
				Networks: map[string]dockerapi.ContainerNetwork{
					"bridge": {IPAddress: currentIP},
				},
			},
		}
		_ = json.NewEncoder(w).Encode(container)
	}))
	defer server.Close()

	docker, err := dockerapi.NewClient(server.URL)
	assert.NoError(t, err)

	adapter := &countingRegistryAdapter{}
	b := &Bridge{
		registry:      adapter,
		docker:        docker,
		config:        Config{Internal: true},
		services:      make(map[string][]*Service),
		serviceHashes: make(map[string]string),
	}

	b.add(containerID, false)
	assert.Equal(t, []string{"10.1.0.2"}, adapter.registeredIPs)

	currentIP = "10.1.0.99"
	b.add(containerID, false)
	assert.Equal(t, []string{"10.1.0.2", "10.1.0.99"}, adapter.registeredIPs)
}
