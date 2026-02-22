package bridge

import dockerapi "github.com/fsouza/go-dockerclient"

func NewResolvedServicePort(container *dockerapi.Container, hostIP, hostPort, exposedPort, portType string) ServicePort {
	return ServicePort{
		HostPort:          hostPort,
		HostIP:            hostIP,
		ExposedPort:       exposedPort,
		ExposedIP:         container.NetworkSettings.IPAddress,
		PortType:          portType,
		ContainerID:       container.ID,
		ContainerHostname: container.Config.Hostname,
		container:         container,
	}
}
