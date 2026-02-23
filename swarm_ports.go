package main

import (
	"fmt"
	"log"
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

var lookupIP = net.LookupIP

type swarmPortResolver struct {
	docker            *dockerapi.Client
	runtime           swarmRuntime
	advertiseMode     string
	advertiseOverride string
	managerAPIPort    int
}

type serviceNetwork struct {
	name string
	ip   string
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
	networks := serviceNetworksInfo(container, service)
	for _, p := range ports {
		if p.PublishedPort == 0 && p.TargetPort == 0 {
			continue
		}
		portType := "tcp"
		if string(p.Protocol) != "" {
			portType = string(p.Protocol)
		}
		if len(networks) == 0 {
			hostIP := r.advertisedIP(service, "")
			if hostIP == "" {
				hostIP = r.runtime.NodeAddr
			}
			out = append(out, bridge.NewResolvedServicePort(
				container,
				hostIP,
				fmt.Sprintf("%d", p.PublishedPort),
				fmt.Sprintf("%d", p.TargetPort),
				portType,
			))
			continue
		}
		for _, network := range networks {
			hostIP := r.advertisedIP(service, network.ip)
			if hostIP == "" {
				hostIP = r.runtime.NodeAddr
			}
			resolved := bridge.NewResolvedServicePort(
				container,
				hostIP,
				fmt.Sprintf("%d", p.PublishedPort),
				fmt.Sprintf("%d", p.TargetPort),
				portType,
			)
			resolved.NetworkNames = []string{network.name}
			out = append(out, resolved)
		}
	}
	return out, nil
}

func (r *swarmPortResolver) inspectService(serviceID string) (*swarmapi.Service, error) {
	if r.runtime.Role == "manager" {
		return r.docker.InspectService(serviceID)
	}
	service, err := r.docker.InspectService(serviceID)
	if err == nil && serviceHasPublishedPorts(service) {
		return service, nil
	}
	if err != nil {
		log.Printf("swarm manager fallback: local service inspect failed for %s: %v", serviceID, err)
	} else {
		log.Printf("swarm manager fallback: local service inspect for %s has no published ports, querying managers", serviceID)
	}
	managers := r.managerNodeAddrs()
	if len(managers) == 0 {
		if err != nil {
			return nil, fmt.Errorf("unable to inspect service %s locally (%v) and from manager list: no manager node address discovered (check swarm manager availability and Docker API access)", serviceID, err)
		}
		return nil, fmt.Errorf("unable to inspect service %s: local inspection returned no published ports and no manager node address discovered (check swarm manager availability and Docker API access)", serviceID)
	}
	log.Printf("swarm manager fallback: querying manager Docker APIs for %s on port %d: %s", serviceID, r.managerAPIPort, strings.Join(managers, ","))
	op := func() error {
		for _, addr := range managers {
			client, err := dockerapi.NewVersionedClient(fmt.Sprintf("tcp://%s:%d", addr, r.managerAPIPort), defaultDockerAPIVersion)
			if err != nil {
				log.Printf("swarm manager fallback: client init failed for manager %s service %s: %v", addr, serviceID, err)
				continue
			}
			log.Printf("swarm manager handshake: attempting manager %s:%d for service %s", addr, r.managerAPIPort, serviceID)
			service, err = client.InspectService(serviceID)
			if err == nil {
				log.Printf("swarm manager handshake: manager %s:%d reachable for service %s", addr, r.managerAPIPort, serviceID)
				return nil
			}
			log.Printf("swarm manager fallback: manager inspect failed for %s via %s:%d: %v", serviceID, addr, r.managerAPIPort, err)
		}
		return fmt.Errorf("unable to inspect service %s from manager list (worker needs manager Docker API reachability on port %d)", serviceID, r.managerAPIPort)
	}
	exp := backoff.NewExponentialBackOff()
	exp.MaxElapsedTime = managerRetryTimeout
	retryErr := backoff.Retry(op, exp)
	return service, retryErr
}

func serviceHasPublishedPorts(service *swarmapi.Service) bool {
	if service == nil {
		return false
	}
	if service.Spec.EndpointSpec != nil && len(service.Spec.EndpointSpec.Ports) > 0 {
		return true
	}
	return len(service.Endpoint.Ports) > 0
}

func (r *swarmPortResolver) managerNodeAddrs() []string {
	addrSet := make(map[string]struct{})
	nodes, err := r.docker.ListNodes(dockerapi.ListNodesOptions{})
	if err == nil {
		for _, addr := range managerAddrsFromNodes(nodes) {
			addrSet[addr] = struct{}{}
		}
	}
	for _, addr := range discoveredManagerAddrs() {
		addrSet[addr] = struct{}{}
	}
	if len(addrSet) == 0 {
		for _, addr := range r.managerAddrsFromTaskDNS() {
			addrSet[addr] = struct{}{}
		}
	}
	addrs := make([]string, 0, len(addrSet))
	for addr := range addrSet {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	return addrs
}

func (r *swarmPortResolver) managerAddrsFromTaskDNS() []string {
	if !r.runtime.RunningAsService || r.runtime.SwarmServiceName == "" {
		return nil
	}
	ips, err := lookupIP("tasks." + r.runtime.SwarmServiceName)
	if err != nil {
		return nil
	}
	addrSet := make(map[string]struct{})
	for _, ip := range ips {
		addr := ip.String()
		if addr == "" || addr == r.runtime.OverlayIP {
			continue
		}
		addrSet[addr] = struct{}{}
	}
	addrs := make([]string, 0, len(addrSet))
	for addr := range addrSet {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	return addrs
}

func managerAddrsFromNodes(nodes []swarmapi.Node) []string {
	addrSet := make(map[string]struct{})
	for _, node := range nodes {
		if node.ManagerStatus == nil && node.Spec.Role != swarmapi.NodeRoleManager {
			continue
		}
		if node.Status.Addr != "" {
			addrSet[node.Status.Addr] = struct{}{}
		}
		if node.ManagerStatus != nil {
			if mgrAddr := managerStatusAddr(node.ManagerStatus.Addr); mgrAddr != "" {
				addrSet[mgrAddr] = struct{}{}
			}
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

func serviceNetworksInfo(container *dockerapi.Container, service *swarmapi.Service) []serviceNetwork {
	if container == nil || container.NetworkSettings == nil || len(container.NetworkSettings.Networks) == 0 {
		return nil
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
	ips := make(map[string]string, len(container.NetworkSettings.Networks))
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
		ips[name] = network.IPAddress
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil
	}
	out := make([]serviceNetwork, 0, len(names))
	for _, name := range names {
		out = append(out, serviceNetwork{name: name, ip: ips[name]})
	}
	return out
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
