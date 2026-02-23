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

type syncRegistryAdapter struct{}

func (s *syncRegistryAdapter) Ping() error                         { return nil }
func (s *syncRegistryAdapter) Register(service *Service) error     { return nil }
func (s *syncRegistryAdapter) Deregister(service *Service) error   { return nil }
func (s *syncRegistryAdapter) Refresh(service *Service) error      { return nil }
func (s *syncRegistryAdapter) Services() ([]*Service, error)       { return nil, nil }

func TestSyncRebuildsCachedContainerServicesFromFreshInspect(t *testing.T) {
	containerID := "1234567890123456789012345678901234567890123456789012345678901234"
	currentIP := "10.0.1.194"

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
					"Image":  "example/app:latest",
					"State":  "running",
					"Status": "Up",
					"Names":  []string{"/applications-keygen-zrf594_keygen-api.1.qtl800o3p6vhbzta8u4uipr86"},
				},
			})
		case strings.Contains(r.URL.Path, "/containers/"+containerID+"/json"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"Id":   containerID,
				"Name": "/applications-keygen-zrf594_keygen-api.1.qtl800o3p6vhbzta8u4uipr86",
				"Config": map[string]interface{}{
					"Image": "example/app:latest",
					"ExposedPorts": map[string]interface{}{
						"3000/tcp": map[string]interface{}{},
					},
					"Labels": map[string]interface{}{},
				},
				"HostConfig": map[string]interface{}{
					"NetworkMode": "bridge",
				},
				"NetworkSettings": map[string]interface{}{
					"IPAddress": currentIP,
					"Networks": map[string]interface{}{
						"dokploy-network": map[string]interface{}{
							"IPAddress": currentIP,
							"NetworkID": "net-id",
						},
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

	b := &Bridge{
		docker: docker,
		registry: &syncRegistryAdapter{},
		config: Config{
			Internal: true,
		},
		services: map[string][]*Service{
			containerID: {
				{ID: "stale:applications-keygen-zrf594_keygen-api.1.qtl800o3p6vhbzta8u4uipr86:3000", Name: "stale-service", IP: "10.0.1.44", Port: 3000},
			},
		},
		serviceHashes:  map[string]string{},
		deadContainers: map[string]*DeadContainer{},
	}

	b.Sync(true)

	require.NotEmpty(t, b.services[containerID])
	assert.Equal(t, "app", b.services[containerID][0].Name)
	assert.Equal(t, 3000, b.services[containerID][0].Port)
	assert.Contains(t, b.services[containerID][0].ID, ":3000")
	assert.Equal(t, currentIP, b.services[containerID][0].IP)
}
