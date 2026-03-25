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

func TestResolvedSwarmPortUsesTargetPortForAddressWhenInternal(t *testing.T) {
	previousHostname := Hostname
	Hostname = "vps-74f5f77e"
	defer func() { Hostname = previousHostname }()

	container := &dockerapi.Container{
		ID:   "abc123",
		Name: "/applications-keygen-zrf594_keygen-api.1.0he9h1ksvgydzexi84w6lkpog",
		Config: &dockerapi.Config{
			Image: "keygen/api:latest",
		},
		HostConfig:      &dockerapi.HostConfig{},
		NetworkSettings: &dockerapi.NetworkSettings{IPAddress: "10.0.1.44"},
	}
	port := NewResolvedServicePort(container, "10.0.0.10", "6000", "3000", "tcp")

	b := &Bridge{config: Config{Internal: true}}
	service := b.newService(port, false)

	assert.NotNil(t, service)
	assert.Equal(t, "vps-74f5f77e:applications-keygen-zrf594_keygen-api.1.0he9h1ksvgydzexi84w6lkpog:3000", service.ID)
	assert.Equal(t, 3000, service.Port)
	assert.Equal(t, "10.0.1.44", service.IP)
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
	assert.ElementsMatch(t, []string{"app-net", "ingress", "registrator"}, service.Tags)
}

func TestSwarmUsesLocalEngineHostnameWhenContainerNodeNameMissing(t *testing.T) {
	previousHostname := Hostname
	Hostname = "ephemeral-container-id"
	defer func() { Hostname = previousHostname }()

	container := &dockerapi.Container{
		ID:   "abc123",
		Name: "/svc.1.taskid",
		Config: &dockerapi.Config{
			Image: "postgres:16",
		},
		HostConfig:      &dockerapi.HostConfig{},
		NetworkSettings: &dockerapi.NetworkSettings{IPAddress: "10.0.0.20"},
	}

	b := &Bridge{
		config:        Config{},
		localHostname: "worker-hostname",
	}
	service := b.newService(ServicePort{
		HostIP:      "10.0.0.10",
		HostPort:    "5432",
		ExposedIP:   "10.0.0.20",
		ExposedPort: "5432",
		PortType:    "tcp",
		container:   container,
	}, false)

	assert.NotNil(t, service)
	assert.Equal(t, "worker-hostname:svc.1.taskid:5432", service.ID)
	assert.NotContains(t, service.ID, "ephemeral-container-id")
}

func TestSwarmNetworkServiceNameAndIDStayStable(t *testing.T) {
	previousHostname := Hostname
	Hostname = "worker-hostname"
	defer func() { Hostname = previousHostname }()

	container := &dockerapi.Container{
		ID:   "abc123",
		Name: "/registrator.1.taskid",
		Config: &dockerapi.Config{
			Image: "registrator:latest",
		},
		HostConfig:      &dockerapi.HostConfig{},
		NetworkSettings: &dockerapi.NetworkSettings{IPAddress: "10.0.0.20"},
	}

	b := &Bridge{config: Config{}}
	service := b.newService(ServicePort{
		HostIP:       "10.0.0.10",
		HostPort:     "2375",
		ExposedIP:    "10.0.0.20",
		ExposedPort:  "2375",
		PortType:     "tcp",
		NetworkNames: []string{"dokploy-network"},
		container:    container,
	}, true)

	assert.NotNil(t, service)
	assert.Equal(t, "registrator", service.Name)
	assert.Equal(t, "worker-hostname:registrator.1.taskid:2375", service.ID)
	assert.ElementsMatch(t, []string{"dokploy-network", "registrator"}, service.Tags)
}

func TestAppendServiceIDNameSuffix(t *testing.T) {
	assert.Equal(t,
		"worker:taskid.dokploy-network.all:2375",
		appendServiceIDNameSuffix("worker:taskid.dokploy-network:2375", ".all"),
	)
	assert.Equal(t,
		"worker:taskid.dokploy-network.all:53:udp",
		appendServiceIDNameSuffix("worker:taskid.dokploy-network:53:udp", ".all"),
	)
}

func TestGenericCheckHTTPUsesStableLowestPort(t *testing.T) {
	container := &dockerapi.Container{
		ID:   "abc123",
		Name: "/api.1.taskid",
		Config: &dockerapi.Config{
			Image: "api:latest",
			Env:   []string{"SERVICE_CHECK_HTTP=/healthz"},
			ExposedPorts: map[dockerapi.Port]struct{}{
				"8080/tcp": {},
				"8090/tcp": {},
				"9090/tcp": {},
			},
		},
		HostConfig: &dockerapi.HostConfig{},
		NetworkSettings: &dockerapi.NetworkSettings{
			IPAddress: "10.0.0.5",
		},
	}

	b := &Bridge{config: Config{Internal: true}}
	service := b.newService(ServicePort{
		ExposedIP:   "10.0.0.5",
		ExposedPort: "9090",
		PortType:    "tcp",
		container:   container,
	}, true)

	assert.NotNil(t, service)
	assert.Equal(t, "/healthz", service.Attrs["check_http"])
	assert.Equal(t, "8080", service.Attrs["check_http_port"])
}
