package consul

import (
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
	consulapi "github.com/hashicorp/consul/api"
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
