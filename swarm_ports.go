package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/cenkalti/backoff"
	swarmapi "github.com/docker/docker/api/types/swarm"
	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/registrator/bridge"
)

const (
	defaultDockerAPIVersion = "1.41"
	managerRetryTimeout     = 5 * time.Second
)

type swarmPortResolver struct {
	docker            *dockerapi.Client
	runtime           swarmRuntime
	advertiseMode     string
	advertiseOverride string
	managerAPIPort    int
}

func newSwarmPortResolver(docker *dockerapi.Client, runtime swarmRuntime, advertiseMode, advertiseOverride string, managerAPIPort int) *swarmPortResolver {
	return &swarmPortResolver{
		docker:            docker,
		runtime:           runtime,
		advertiseMode:     advertiseMode,
		advertiseOverride: advertiseOverride,
		managerAPIPort:    managerAPIPort,
	}
}

func (r *swarmPortResolver) ResolveSwarmPorts(container *dockerapi.Container) ([]bridge.ServicePort, error) {
	if container == nil || container.Config == nil {
		return nil, nil
	}
	serviceID := container.Config.Labels["com.docker.swarm.service.id"]
	if serviceID == "" {
		return nil, nil
	}
	service, err := r.inspectService(serviceID)
	if err != nil {
		return nil, err
	}
	var ports []swarmapi.PortConfig
	if service.Spec.EndpointSpec != nil {
		ports = service.Spec.EndpointSpec.Ports
	}
	if len(ports) == 0 {
		ports = service.Endpoint.Ports
	}
	out := make([]bridge.ServicePort, 0, len(ports))
	for _, p := range ports {
		if p.PublishedPort == 0 && p.TargetPort == 0 {
			continue
		}
		hostIP := r.advertisedIP(service)
		if hostIP == "" {
			hostIP = r.runtime.NodeAddr
		}
		portType := "tcp"
		if string(p.Protocol) != "" {
			portType = string(p.Protocol)
		}
		out = append(out, bridge.NewResolvedServicePort(
			container,
			hostIP,
			fmt.Sprintf("%d", p.PublishedPort),
			fmt.Sprintf("%d", p.TargetPort),
			portType,
		))
	}
	return out, nil
}

func (r *swarmPortResolver) inspectService(serviceID string) (*swarmapi.Service, error) {
	if r.runtime.Role == "manager" {
		return r.docker.InspectService(serviceID)
	}
	managers := r.managerNodeAddrs()
	var service *swarmapi.Service
	op := func() error {
		for _, addr := range managers {
			client, err := dockerapi.NewVersionedClient(fmt.Sprintf("tcp://%s:%d", addr, r.managerAPIPort), defaultDockerAPIVersion)
			if err != nil {
				continue
			}
			service, err = client.InspectService(serviceID)
			if err == nil {
				return nil
			}
		}
		return fmt.Errorf("unable to inspect service %s from manager list", serviceID)
	}
	exp := backoff.NewExponentialBackOff()
	exp.MaxElapsedTime = managerRetryTimeout
	err := backoff.Retry(op, exp)
	return service, err
}

func (r *swarmPortResolver) managerNodeAddrs() []string {
	nodes, err := r.docker.ListNodes(dockerapi.ListNodesOptions{})
	if err != nil {
		return nil
	}
	addrs := make([]string, 0)
	for _, node := range nodes {
		if node.ManagerStatus != nil && node.Status.Addr != "" {
			addrs = append(addrs, node.Status.Addr)
		}
	}
	sort.Strings(addrs)
	return addrs
}

func (r *swarmPortResolver) advertisedIP(service *swarmapi.Service) string {
	switch r.advertiseMode {
	case "custom":
		return r.advertiseOverride
	case "service-vip":
		if len(service.Endpoint.VirtualIPs) == 0 {
			return ""
		}
		addr := service.Endpoint.VirtualIPs[0].Addr
		if idx := strings.Index(addr, "/"); idx >= 0 {
			return addr[:idx]
		}
		return addr
	default:
		if r.advertiseOverride != "" {
			return r.advertiseOverride
		}
		return r.runtime.NodeAddr
	}
}
