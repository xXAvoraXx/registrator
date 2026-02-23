package consul

import (
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/registrator/bridge"
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

func TestBuildCheckUsesCheckHTTPPortOverride(t *testing.T) {
	adapter := &ConsulAdapter{}
	service := &bridge.Service{
		IP:   "10.0.0.5",
		Port: 9090,
		Attrs: map[string]string{
			"check_http":      "/healthz",
			"check_http_port": "8080",
		},
	}

	check := adapter.buildCheck(service)
	assert.Equal(t, "http://10.0.0.5:8080/healthz", check.HTTP)
}
