package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type captureRegistryAdapter struct {
	registered []*Service
}

func (c *captureRegistryAdapter) Ping() error { return nil }
func (c *captureRegistryAdapter) Register(service *Service) error {
	c.registered = append(c.registered, service)
	return nil
}
func (c *captureRegistryAdapter) Deregister(service *Service) error { return nil }
func (c *captureRegistryAdapter) Refresh(service *Service) error    { return nil }
func (c *captureRegistryAdapter) Services() ([]*Service, error)     { return nil, nil }

func TestAddRegistersSwarmPortPerNetwork(t *testing.T) {
	containerID := "1234567890123456789012345678901234567890123456789012345678901234"
	dockerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/version"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"ApiVersion": "1.41",
			})
		case strings.Contains(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode([]map[string]interface{}{
				{
					"Id":     containerID,
					"Image":  "registrator:latest",
					"State":  "running",
					"Status": "Up",
					"Names":  []string{"/registrator.1.taskid"},
				},
			})
		case strings.Contains(r.URL.Path, "/containers/"+containerID+"/json"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"Id":   containerID,
				"Name": "/registrator.1.taskid",
				"Config": map[string]interface{}{
					"Image":  "registrator:latest",
					"Labels": map[string]interface{}{},
				},
				"HostConfig": map[string]interface{}{
					"NetworkMode": "bridge",
				},
				"NetworkSettings": map[string]interface{}{
					"IPAddress": "10.0.1.200",
					"Networks": map[string]interface{}{
						"dokploy-network": map[string]interface{}{"IPAddress": "10.0.1.200"},
						"registrator-network": map[string]interface{}{"IPAddress": "10.0.2.200"},
					},
					"Ports": map[string]interface{}{},
				},
				"State": map[string]interface{}{
					"Running": true,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer dockerServer.Close()

	docker, err := dockerapi.NewVersionedClient(dockerServer.URL, "1.41")
	require.NoError(t, err)

	registry := &captureRegistryAdapter{}
	b := &Bridge{
		docker:   docker,
		registry: registry,
		config: Config{
			ResolveSwarm: func(container *dockerapi.Container) ([]ServicePort, error) {
				first := NewResolvedServicePort(container, "10.0.0.10", "2375", "2375", "tcp")
				first.ExposedIP = "10.0.1.200"
				first.NetworkNames = []string{"dokploy-network"}

				second := NewResolvedServicePort(container, "10.0.0.10", "2375", "2375", "tcp")
				second.ExposedIP = "10.0.2.200"
				second.NetworkNames = []string{"registrator-network"}

				return []ServicePort{first, second}, nil
			},
		},
		services:       map[string][]*Service{},
		serviceHashes:  map[string]string{},
		deadContainers: map[string]*DeadContainer{},
	}

	b.Sync(true)

	require.Len(t, registry.registered, 2)
	require.Len(t, b.services[containerID], 2)

	networkTagCounts := map[string]int{
		"dokploy-network":    0,
		"registrator-network": 0,
	}
	ids := make(map[string]struct{}, len(registry.registered))
	for _, service := range registry.registered {
		ids[service.ID] = struct{}{}
		assert.Contains(t, service.Tags, registratorManagedTag)
		for network := range networkTagCounts {
			if hasTag(service.Tags, network) {
				assert.Contains(t, service.ID, "."+network)
				networkTagCounts[network]++
			}
		}
	}
	assert.Len(t, ids, 2)
	assert.Equal(t, 1, networkTagCounts["dokploy-network"])
	assert.Equal(t, 1, networkTagCounts["registrator-network"])
}
