package bridge

import (
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
)

func newUnreachableDockerClient(t *testing.T) *dockerapi.Client {
	t.Helper()
	client, err := dockerapi.NewVersionedClient("unix:///var/run/nonexistent.sock", "1.41")
	if err != nil {
		t.Fatalf("unable to create docker client: %v", err)
	}
	return client
}

func TestAddDoesNotReuseDeadContainerCachedServices(t *testing.T) {
	containerID := "1234567890123456789012345678901234567890123456789012345678901234"
	stale := &Service{ID: "svc-1", Name: "svc", IP: "10.0.0.10", Port: 8080}
	b := &Bridge{
		docker:         newUnreachableDockerClient(t),
		services:       map[string][]*Service{},
		serviceHashes:  map[string]string{},
		deadContainers: map[string]*DeadContainer{containerID: &DeadContainer{TTL: 30, Services: []*Service{stale}}},
	}

	b.add(containerID, true)

	assert.Nil(t, b.services[containerID])
	assert.Nil(t, b.deadContainers[containerID])
}

func TestAddRebuildsWhenContainerAlreadyCached(t *testing.T) {
	containerID := "1234567890123456789012345678901234567890123456789012345678901234"
	stale := &Service{ID: "svc-1", Name: "svc", IP: "10.0.0.10", Port: 8080}
	b := &Bridge{
		docker:         newUnreachableDockerClient(t),
		services:       map[string][]*Service{containerID: []*Service{stale}},
		serviceHashes:  map[string]string{},
		deadContainers: map[string]*DeadContainer{},
	}

	b.add(containerID, true)

	assert.Nil(t, b.services[containerID])
}
