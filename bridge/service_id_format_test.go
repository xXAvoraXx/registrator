package bridge

import (
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
)

func TestServiceIDFormatPreservedForSwarmTaskName(t *testing.T) {
	previousHostname := Hostname
	Hostname = "vps-74f5f77e"
	defer func() { Hostname = previousHostname }()

	container := &dockerapi.Container{
		ID:   "abc123",
		Name: "/persistence-keygen-db-pj46xk.1.m69empeguslu19zx9fbmaqnz0",
		Config: &dockerapi.Config{
			Image: "postgres:16",
		},
		HostConfig:      &dockerapi.HostConfig{},
		NetworkSettings: &dockerapi.NetworkSettings{IPAddress: "10.0.0.20"},
	}

	b := &Bridge{config: Config{}}
	service := b.newService(ServicePort{
		HostIP:      "10.0.0.10",
		HostPort:    "5432",
		ExposedIP:   "10.0.0.20",
		ExposedPort: "5432",
		PortType:    "tcp",
		container:   container,
	}, false)

	assert.NotNil(t, service)
	assert.Equal(t, "vps-74f5f77e:persistence-keygen-db-pj46xk.1.m69empeguslu19zx9fbmaqnz0:5432", service.ID)
}

func TestResolvedSwarmPortKeepsServiceIDFormat(t *testing.T) {
	previousHostname := Hostname
	Hostname = "vps-74f5f77e"
	defer func() { Hostname = previousHostname }()

	container := &dockerapi.Container{
		ID:   "abc123",
		Name: "/persistence-keygen-db-pj46xk.1.m69empeguslu19zx9fbmaqnz0",
		Config: &dockerapi.Config{
			Image: "postgres:16",
		},
		HostConfig:      &dockerapi.HostConfig{},
		NetworkSettings: &dockerapi.NetworkSettings{IPAddress: "10.0.0.20"},
	}
	port := NewResolvedServicePort(container, "10.0.0.10", "5432", "5432", "tcp")

	b := &Bridge{config: Config{}}
	service := b.newService(port, false)

	assert.NotNil(t, service)
	assert.Equal(t, "vps-74f5f77e:persistence-keygen-db-pj46xk.1.m69empeguslu19zx9fbmaqnz0:5432", service.ID)
}

func TestSwarmUsesMachineHostnameInServiceID(t *testing.T) {
	previousHostname := Hostname
	Hostname = "ephemeral-container-id"
	defer func() { Hostname = previousHostname }()

	container := &dockerapi.Container{
		ID:   "abc123",
		Name: "/svc.1.taskid",
		Config: &dockerapi.Config{
			Image: "postgres:16",
		},
		Node:            &dockerapi.SwarmNode{Name: "worker-hostname"},
		HostConfig:      &dockerapi.HostConfig{},
		NetworkSettings: &dockerapi.NetworkSettings{IPAddress: "10.0.0.20"},
	}

	b := &Bridge{config: Config{}}
	service := b.newService(ServicePort{
		HostIP:       "10.0.0.10",
		HostPort:     "5432",
		ExposedIP:    "10.0.0.20",
		ExposedPort:  "5432",
		PortType:     "tcp",
		NetworkNames: []string{"app-net", "ingress"},
		container:    container,
	}, false)

	assert.NotNil(t, service)
	assert.Equal(t, "worker-hostname:svc.1.taskid:5432", service.ID)
	assert.ElementsMatch(t, []string{"app-net", "ingress"}, service.Tags)
}
