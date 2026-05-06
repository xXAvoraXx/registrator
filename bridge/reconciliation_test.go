package bridge

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type reconcileRegistryAdapter struct {
	services     []*Service
	registered   []*Service
	deregistered []*Service
}

func (r *reconcileRegistryAdapter) Ping() error { return nil }
func (r *reconcileRegistryAdapter) Register(service *Service) error {
	clone := cloneServiceForTest(service)
	r.registered = append(r.registered, clone)
	for i, existing := range r.services {
		if existing.ID == clone.ID {
			r.services[i] = clone
			return nil
		}
	}
	r.services = append(r.services, clone)
	return nil
}
func (r *reconcileRegistryAdapter) Deregister(service *Service) error {
	clone := cloneServiceForTest(service)
	r.deregistered = append(r.deregistered, clone)
	for i, existing := range r.services {
		if existing.ID == clone.ID {
			r.services = append(r.services[:i], r.services[i+1:]...)
			return nil
		}
	}
	return nil
}
func (r *reconcileRegistryAdapter) Refresh(service *Service) error { return nil }
func (r *reconcileRegistryAdapter) Services() ([]*Service, error) {
	out := make([]*Service, 0, len(r.services))
	for _, service := range r.services {
		out = append(out, cloneServiceForTest(service))
	}
	return out, nil
}

func cloneServiceForTest(service *Service) *Service {
	if service == nil {
		return nil
	}
	clone := *service
	if service.Tags != nil {
		clone.Tags = append([]string{}, service.Tags...)
	}
	if service.Attrs != nil {
		clone.Attrs = make(map[string]string, len(service.Attrs))
		for key, value := range service.Attrs {
			clone.Attrs[key] = value
		}
	}
	return &clone
}

func newReconcileDockerClient(t *testing.T, containers map[string]map[string]interface{}, listings []map[string]interface{}) (*dockerapi.Client, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/version"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ApiVersion": "1.41"})
		case strings.Contains(r.URL.Path, "/containers/json"):
			_ = json.NewEncoder(w).Encode(listings)
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			parts := strings.Split(r.URL.Path, "/")
			containerID := ""
			for i, part := range parts {
				if part == "containers" && i+1 < len(parts) {
					containerID = parts[i+1]
					break
				}
			}
			if containerID == "" {
				http.NotFound(w, r)
				return
			}
			payload, ok := containers[containerID]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	docker, err := dockerapi.NewVersionedClient(server.URL, "1.41")
	require.NoError(t, err)
	return docker, server.Close
}

func reconcileContainerPayload(id, name, image, ip, nodeID, nodeName string) map[string]interface{} {
	payload := map[string]interface{}{
		"Id":   id,
		"Name": "/" + name,
		"Config": map[string]interface{}{
			"Image": image,
			"ExposedPorts": map[string]interface{}{
				"3000/tcp": map[string]interface{}{},
			},
			"Labels": map[string]interface{}{},
		},
		"HostConfig": map[string]interface{}{
			"NetworkMode": "bridge",
		},
		"NetworkSettings": map[string]interface{}{
			"IPAddress": ip,
			"Networks": map[string]interface{}{
				"app": map[string]interface{}{"IPAddress": ip},
			},
			"Ports": map[string]interface{}{},
		},
		"State": map[string]interface{}{
			"Running": true,
		},
	}
	if nodeID != "" || nodeName != "" {
		payload["Node"] = map[string]interface{}{"ID": nodeID, "Name": nodeName}
	}
	return payload
}

func reconcileListing(id, name string) map[string]interface{} {
	return map[string]interface{}{
		"Id":     id,
		"Image":  "example/app:latest",
		"State":  "running",
		"Status": "Up",
		"Names":  []string{"/" + name},
		"Networks": map[string]interface{}{
			"Networks": map[string]interface{}{
				"app": map[string]interface{}{"IPAddress": "10.0.1.20"},
			},
		},
	}
}

func TestSyncRemovesCachedNonLocalContainerWithoutDeadlock(t *testing.T) {
	containerID := "1234567890123456789012345678901234567890123456789012345678901234"
	docker, closeServer := newReconcileDockerClient(t,
		map[string]map[string]interface{}{
			containerID: reconcileContainerPayload(containerID, "app.1.task", "example/app:latest", "10.0.1.20", "node-other", "worker-other"),
		},
		[]map[string]interface{}{reconcileListing(containerID, "app.1.task")},
	)
	defer closeServer()

	registry := &reconcileRegistryAdapter{}
	b := &Bridge{
		docker:   docker,
		registry: registry,
		config: Config{
			Cleanup:     true,
			Internal:    true,
			LocalNodeID: "node-local",
		},
		services: map[string][]*Service{
			containerID: {{ID: "worker-local:app.1.task:3000", Name: "app", IP: "10.0.1.20", Port: 3000, Tags: []string{registratorManagedTag}}},
		},
		serviceHashes:  map[string]string{},
		deadContainers: map[string]*DeadContainer{},
	}

	done := make(chan struct{})
	go func() {
		b.Sync(true)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Sync deadlocked while removing cached non-local services")
	}
	assert.Empty(t, b.services[containerID])
	assert.Equal(t, []string{"worker-local:app.1.task:3000"}, serviceIDsForTest(registry.deregistered))
}

func TestSyncRemovesLegacyStaleServiceUsingDockerHostIdentity(t *testing.T) {
	previousHostname := Hostname
	Hostname = "registrator-container-id"
	defer func() { Hostname = previousHostname }()

	containerID := "2234567890123456789012345678901234567890123456789012345678901234"
	docker, closeServer := newReconcileDockerClient(t,
		map[string]map[string]interface{}{
			containerID: reconcileContainerPayload(containerID, "app.1.newtask", "example/app:latest", "10.0.1.20", "", ""),
		},
		[]map[string]interface{}{reconcileListing(containerID, "app.1.newtask")},
	)
	defer closeServer()

	registry := &reconcileRegistryAdapter{
		services: []*Service{
			{ID: "worker-hostname:app.1.oldtask:3000", Name: "app", IP: "10.0.1.10", Port: 3000, Tags: []string{registratorManagedTag}},
		},
	}
	b := &Bridge{
		docker:         docker,
		localHostname:  "worker-hostname",
		registry:       registry,
		config:         Config{Cleanup: true, Internal: true},
		services:       map[string][]*Service{},
		serviceHashes:  map[string]string{},
		deadContainers: map[string]*DeadContainer{},
	}

	b.Sync(true)

	assert.Equal(t, []string{"worker-hostname:app.1.oldtask:3000"}, serviceIDsForTest(registry.deregistered))
	require.NotEmpty(t, b.services[containerID])
	assert.Equal(t, "worker-hostname:app.1.newtask:3000", b.services[containerID][0].ID)
}

func TestSyncRetainsExactDesiredServiceWithoutDuplicateRegister(t *testing.T) {
	containerID := "3234567890123456789012345678901234567890123456789012345678901234"
	containerName := "app.1.task"
	docker, closeServer := newReconcileDockerClient(t,
		map[string]map[string]interface{}{
			containerID: reconcileContainerPayload(containerID, containerName, "example/app:latest", "10.0.1.20", "", ""),
		},
		[]map[string]interface{}{reconcileListing(containerID, containerName)},
	)
	defer closeServer()

	existing := &Service{
		ID:    "worker-hostname:app.1.task:3000",
		Name:  "app",
		IP:    "10.0.1.20",
		Port:  3000,
		Tags:  []string{"app", registratorManagedTag},
		Attrs: ownershipAttrsForTest("worker-hostname", "", containerID, containerName),
	}
	registry := &reconcileRegistryAdapter{services: []*Service{existing}}
	b := &Bridge{
		docker:         docker,
		localHostname:  "worker-hostname",
		registry:       registry,
		config:         Config{Cleanup: true, Internal: true},
		services:       map[string][]*Service{},
		serviceHashes:  map[string]string{},
		deadContainers: map[string]*DeadContainer{},
	}

	b.Sync(true)

	assert.Empty(t, registry.registered)
	assert.Empty(t, registry.deregistered)
	require.NotEmpty(t, b.services[containerID])
	assert.Equal(t, existing.ID, b.services[containerID][0].ID)
}

func TestSyncRemovesDuplicateLocalServiceAndKeepsDesiredID(t *testing.T) {
	containerID := "4234567890123456789012345678901234567890123456789012345678901234"
	containerName := "app.1.task"
	docker, closeServer := newReconcileDockerClient(t,
		map[string]map[string]interface{}{
			containerID: reconcileContainerPayload(containerID, containerName, "example/app:latest", "10.0.1.20", "", ""),
		},
		[]map[string]interface{}{reconcileListing(containerID, containerName)},
	)
	defer closeServer()

	attrs := ownershipAttrsForTest("worker-hostname", "", containerID, containerName)
	registry := &reconcileRegistryAdapter{
		services: []*Service{
			{ID: "custom-old", Name: "app", IP: "10.0.1.20", Port: 3000, Tags: []string{"app", registratorManagedTag}, Attrs: attrs},
			{ID: "worker-hostname:app.1.task:3000", Name: "app", IP: "10.0.1.20", Port: 3000, Tags: []string{"app", registratorManagedTag}, Attrs: attrs},
		},
	}
	b := &Bridge{
		docker:         docker,
		localHostname:  "worker-hostname",
		registry:       registry,
		config:         Config{Cleanup: true, Internal: true},
		services:       map[string][]*Service{},
		serviceHashes:  map[string]string{},
		deadContainers: map[string]*DeadContainer{},
	}

	b.Sync(true)

	assert.Equal(t, []string{"custom-old"}, serviceIDsForTest(registry.deregistered))
	assert.NotContains(t, serviceIDsForTest(registry.deregistered), "worker-hostname:app.1.task:3000")
}

func TestSyncRemovesOwnedCustomIDButLeavesAmbiguousCustomID(t *testing.T) {
	docker, closeServer := newReconcileDockerClient(t, map[string]map[string]interface{}{}, nil)
	defer closeServer()

	registry := &reconcileRegistryAdapter{
		services: []*Service{
			{ID: "owned-custom", Name: "app", IP: "10.0.1.20", Port: 3000, Tags: []string{registratorManagedTag}, Attrs: ownershipAttrsForTest("worker-hostname", "", "", "")},
			{ID: "ambiguous-custom", Name: "app", IP: "10.0.1.21", Port: 3000, Tags: []string{registratorManagedTag}},
		},
	}
	b := &Bridge{
		docker:         docker,
		localHostname:  "worker-hostname",
		registry:       registry,
		config:         Config{Cleanup: true, Internal: true},
		services:       map[string][]*Service{},
		serviceHashes:  map[string]string{},
		deadContainers: map[string]*DeadContainer{},
	}

	b.Sync(true)

	assert.Equal(t, []string{"owned-custom"}, serviceIDsForTest(registry.deregistered))
}

func ownershipAttrsForTest(host, nodeID, containerID, containerName string) map[string]string {
	attrs := map[string]string{
		registratorManagedAttr: "true",
		registratorHostAttr:    host,
	}
	if nodeID != "" {
		attrs[registratorNodeIDAttr] = nodeID
	}
	if containerID != "" {
		attrs[registratorContainerIDAttr] = containerID
	}
	if containerName != "" {
		attrs[registratorContainerNameAttr] = containerName
	}
	return attrs
}

func serviceIDsForTest(services []*Service) []string {
	ids := make([]string, 0, len(services))
	for _, service := range services {
		ids = append(ids, service.ID)
	}
	return ids
}
