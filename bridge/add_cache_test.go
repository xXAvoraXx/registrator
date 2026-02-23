package bridge

import (
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
)

const testContainerID = "1234567890123456789012345678901234567890123456789012345678901234"

func newUnreachableDockerClient(t *testing.T) *dockerapi.Client {
	t.Helper()
	client, err := dockerapi.NewVersionedClient("unix:///var/run/nonexistent.sock", "1.41")
	if err != nil {
		t.Fatalf("unable to create docker client: %v", err)
	}
	return client
}

func TestAddDoesNotReuseDeadContainerCachedServices(t *testing.T) {
	stale := &Service{ID: "svc-1", Name: "svc", IP: "10.0.0.10", Port: 8080}
	b := &Bridge{
		docker:         newUnreachableDockerClient(t),
		services:       map[string][]*Service{},
		serviceHashes:  map[string]string{},
		deadContainers: map[string]*DeadContainer{testContainerID: &DeadContainer{TTL: 30, Services: []*Service{stale}}},
	}

	b.add(testContainerID, true)

	assert.Nil(t, b.services[testContainerID])
	assert.Nil(t, b.deadContainers[testContainerID])
}

func TestAddRebuildsWhenContainerAlreadyCached(t *testing.T) {
	stale := &Service{ID: "svc-1", Name: "svc", IP: "10.0.0.10", Port: 8080}
	b := &Bridge{
		docker:         newUnreachableDockerClient(t),
		services:       map[string][]*Service{testContainerID: []*Service{stale}},
		serviceHashes:  map[string]string{},
		deadContainers: map[string]*DeadContainer{},
	}

	b.add(testContainerID, true)

	assert.Nil(t, b.services[testContainerID])
}
