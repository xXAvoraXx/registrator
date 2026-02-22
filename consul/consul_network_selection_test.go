package consul

import (
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
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
