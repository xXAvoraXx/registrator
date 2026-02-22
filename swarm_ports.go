package main

import (
	"fmt"
	"net"
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
	networkIP, networkNames := serviceNetworkInfo(container, service)
	for _, p := range ports {
		if p.PublishedPort == 0 && p.TargetPort == 0 {
			continue
		}
		hostIP := r.advertisedIP(service, networkIP)
		if hostIP == "" {
			hostIP = r.runtime.NodeAddr
		}
		portType := "tcp"
		if string(p.Protocol) != "" {
			portType = string(p.Protocol)
		}
		resolved := bridge.NewResolvedServicePort(
			container,
			hostIP,
			fmt.Sprintf("%d", p.PublishedPort),
			fmt.Sprintf("%d", p.TargetPort),
			portType,
		)
		resolved.NetworkNames = networkNames
		out = append(out, resolved)
	}
	return out, nil
}

func (r *swarmPortResolver) inspectService(serviceID string) (*swarmapi.Service, error) {
	if r.runtime.Role == "manager" {
		return r.docker.InspectService(serviceID)
	}
	managers := r.managerNodeAddrs()
	if len(managers) == 0 {
		return nil, fmt.Errorf("unable to inspect service %s from manager list: no manager node address discovered (check swarm manager availability and Docker API access)", serviceID)
	}
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
		return fmt.Errorf("unable to inspect service %s from manager list (worker needs manager Docker API reachability on port %d)", serviceID, r.managerAPIPort)
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
	addrSet := make(map[string]struct{})
	for _, node := range nodes {
		if node.ManagerStatus == nil {
			continue
		}
		if node.Status.Addr != "" {
			addrSet[node.Status.Addr] = struct{}{}
		}
		if mgrAddr := managerStatusAddr(node.ManagerStatus.Addr); mgrAddr != "" {
			addrSet[mgrAddr] = struct{}{}
		}
	}
	addrs := make([]string, 0, len(addrSet))
	for addr := range addrSet {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	return addrs
}

func managerStatusAddr(addr string) string {
	if addr == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}
	return addr
}

func serviceNetworkInfo(container *dockerapi.Container, service *swarmapi.Service) (string, []string) {
	if container == nil || container.NetworkSettings == nil || len(container.NetworkSettings.Networks) == 0 {
		return "", nil
	}
	wantedIDs := make(map[string]struct{})
	if service != nil {
		for _, vip := range service.Endpoint.VirtualIPs {
			if vip.NetworkID != "" {
				wantedIDs[vip.NetworkID] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(container.NetworkSettings.Networks))
	for name, network := range container.NetworkSettings.Networks {
		if network.IPAddress == "" {
			continue
		}
		if len(wantedIDs) > 0 {
			if _, ok := wantedIDs[network.NetworkID]; !ok {
				continue
			}
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", nil
	}
	return container.NetworkSettings.Networks[names[0]].IPAddress, names
}

func (r *swarmPortResolver) advertisedIP(service *swarmapi.Service, preferredIP string) string {
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
		if preferredIP != "" {
			return preferredIP
		}
		return r.runtime.NodeAddr
	}
}
