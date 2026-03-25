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

type updateAwareRegistryAdapter struct {
	registered   []*Service
	deregistered []*Service
}

func (a *updateAwareRegistryAdapter) Ping() error { return nil }
func (a *updateAwareRegistryAdapter) Register(service *Service) error {
	a.registered = append(a.registered, service)
	return nil
}
func (a *updateAwareRegistryAdapter) Deregister(service *Service) error {
	a.deregistered = append(a.deregistered, service)
	return nil
}
func (a *updateAwareRegistryAdapter) Refresh(service *Service) error { return nil }
func (a *updateAwareRegistryAdapter) Services() ([]*Service, error)  { return nil, nil }

func TestAddDeregistersPreviousServicesWhenContainerBecomesIgnored(t *testing.T) {
	containerID := "1234567890123456789012345678901234567890123456789012345678901234"
	inspectCount := 0

	dockerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/version"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ApiVersion": "1.41"})
		case strings.Contains(r.URL.Path, "/containers/"+containerID+"/json"):
			inspectCount++
			env := []string{}
			if inspectCount > 1 {
				env = []string{"SERVICE_IGNORE=1"}
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"Id":   containerID,
				"Name": "/example-app",
				"Config": map[string]interface{}{
					"Image": "example/app:latest",
					"ExposedPorts": map[string]interface{}{
						"8080/tcp": map[string]interface{}{},
					},
					"Env":    env,
					"Labels": map[string]interface{}{},
				},
				"HostConfig": map[string]interface{}{
					"NetworkMode": "bridge",
				},
				"NetworkSettings": map[string]interface{}{
					"IPAddress": "172.18.0.5",
					"Networks": map[string]interface{}{
						"bridge": map[string]interface{}{"IPAddress": "172.18.0.5"},
					},
					"Ports": map[string]interface{}{
						"8080/tcp": []map[string]interface{}{
							{"HostIP": "10.0.0.10", "HostPort": "8080"},
						},
					},
				},
				"State": map[string]interface{}{"Running": true},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer dockerServer.Close()

	docker, err := dockerapi.NewVersionedClient(dockerServer.URL, "1.41")
	require.NoError(t, err)

	registry := &updateAwareRegistryAdapter{}
	b := &Bridge{
		docker:         docker,
		registry:       registry,
		config:         Config{},
		services:       map[string][]*Service{},
		serviceHashes:  map[string]string{},
		deadContainers: map[string]*DeadContainer{},
	}

	b.add(containerID, true)
	require.Len(t, b.services[containerID], 1)
	require.Len(t, registry.registered, 1)

	b.add(containerID, true)
	assert.Empty(t, b.services[containerID])
	assert.Len(t, registry.deregistered, 1)
	assert.Equal(t, registry.registered[0].ID, registry.deregistered[0].ID)
}

func TestDeregisterAllRemovesLiveAndDeadContainerServices(t *testing.T) {
	registry := &updateAwareRegistryAdapter{}
	live := &Service{ID: "live-service", Name: "svc", IP: "10.0.0.10", Port: 8080}
	dead := &Service{ID: "dead-service", Name: "svc", IP: "10.0.0.11", Port: 8081}

	b := &Bridge{
		registry: registry,
		services: map[string][]*Service{
			testContainerID: []*Service{live},
		},
		serviceHashes: map[string]string{
			live.ID: serviceHash(live),
			dead.ID: serviceHash(dead),
		},
		deadContainers: map[string]*DeadContainer{
			"abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd": {
				TTL:      10,
				Services: []*Service{dead},
			},
		},
	}

	b.DeregisterAll()

	assert.Len(t, registry.deregistered, 2)
	assert.Empty(t, b.services)
	assert.Empty(t, b.deadContainers)
	assert.Empty(t, b.serviceHashes)
}
