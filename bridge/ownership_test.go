package bridge

import (
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
)

func TestOwnsContainerRequiresMatchingNodeID(t *testing.T) {
	b := &Bridge{config: Config{LocalNodeID: "node-local"}}
	assert.True(t, b.ownsContainer(&dockerapi.Container{Node: &dockerapi.SwarmNode{ID: "node-local"}}))
	assert.False(t, b.ownsContainer(&dockerapi.Container{Node: &dockerapi.SwarmNode{ID: "node-other"}}))
	assert.True(t, b.ownsContainer(&dockerapi.Container{}))
	assert.True(t, b.ownsContainer(&dockerapi.Container{
		Config: &dockerapi.Config{
			Labels: map[string]string{"com.docker.swarm.node.id": "node-local"},
		},
	}))
	assert.False(t, b.ownsContainer(&dockerapi.Container{
		Config: &dockerapi.Config{
			Labels: map[string]string{"com.docker.swarm.node.id": "node-other"},
		},
	}))
}
