package main

import (
	"strings"
	"testing"

	swarmapi "github.com/docker/docker/api/types/swarm"
	dockerapi "github.com/fsouza/go-dockerclient"
)

func TestInspectServiceNoManagerAddress(t *testing.T) {
	docker, err := dockerapi.NewClient("unix:///tmp/registrator-missing-docker.sock")
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	resolver := newSwarmPortResolver(docker, swarmRuntime{Role: "worker"}, "", "", 2375)

	_, err = resolver.inspectService("service-id")
	if err == nil {
		t.Fatalf("expected inspectService error")
	}
	if !strings.Contains(err.Error(), "no manager node address discovered") {
		t.Fatalf("expected actionable manager discovery error, got: %v", err)
	}
}

func TestManagerStatusAddrStripsPort(t *testing.T) {
	if got := managerStatusAddr("10.0.0.10:2377"); got != "10.0.0.10" {
		t.Fatalf("expected host without port, got %q", got)
	}
}

func TestServiceNetworkInfoUsesServiceVIPNetworks(t *testing.T) {
	container := &dockerapi.Container{
		NetworkSettings: &dockerapi.NetworkSettings{
			Networks: map[string]dockerapi.ContainerNetwork{
				"ingress": {IPAddress: "10.0.9.10", NetworkID: "ingress-id"},
				"app":     {IPAddress: "10.0.1.20", NetworkID: "app-id"},
			},
		},
	}
	service := &swarmapi.Service{
		Endpoint: swarmapi.Endpoint{
			VirtualIPs: []swarmapi.EndpointVirtualIP{
				{Addr: "10.0.1.2/24", NetworkID: "app-id"},
			},
		},
	}

	ip, names := serviceNetworkInfo(container, service)
	if ip != "10.0.1.20" {
		t.Fatalf("expected app network IP, got %q", ip)
	}
	if len(names) != 1 || names[0] != "app" {
		t.Fatalf("expected app network name, got %v", names)
	}
}
