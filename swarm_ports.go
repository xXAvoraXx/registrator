package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/cenkalti/backoff"
	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/registrator/bridge"
	swarmapi "github.com/moby/moby/api/types/swarm"
	"github.com/moby/moby/api/types/system"
	mobyclient "github.com/moby/moby/client"
)

const (
	defaultDockerAPIVersion = "1.41"
	managerRetryTimeout     = 5 * time.Second
	peerInfoRequestTimeout  = 2 * time.Second
	swarmAPIRequestTimeout  = 10 * time.Second
)

var lookupIP = net.LookupIP

type swarmPortResolver struct {
	docker            *dockerapi.Client
	runtime           swarmRuntime
	advertiseMode     string
	advertiseOverride string
	managerAPIPort    int
	peerInfoPort      string
	statusToken       string
	swarmClient       *mobyclient.Client
}

type serviceNetwork struct {
	name string
	ip   string
}

func newSwarmPortResolver(docker *dockerapi.Client, runtime swarmRuntime, advertiseMode, advertiseOverride string, managerAPIPort int, peerInfoPort, statusToken string) *swarmPortResolver {
	swarmClient, err := newSwarmAPIClient(docker)
	if err != nil {
		log.Printf("swarm api client setup failed: %v", err)
	}
	return &swarmPortResolver{
		docker:            docker,
		runtime:           runtime,
		advertiseMode:     advertiseMode,
		advertiseOverride: advertiseOverride,
		managerAPIPort:    managerAPIPort,
		peerInfoPort:      peerInfoPort,
		statusToken:       statusToken,
		swarmClient:       swarmClient,
	}
}

func newSwarmAPIClient(docker *dockerapi.Client) (*mobyclient.Client, error) {
	if docker == nil {
		return nil, fmt.Errorf("docker client unavailable")
	}
	return mobyclient.NewClientWithOpts(
		mobyclient.WithHost(docker.Endpoint()),
		mobyclient.WithAPIVersion(defaultDockerAPIVersion),
	)
}

func inspectSwarmService(docker *dockerapi.Client, serviceID string) (*swarmapi.Service, error) {
	client, err := newSwarmAPIClient(docker)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), swarmAPIRequestTimeout)
	defer cancel()
	result, err := client.ServiceInspect(ctx, serviceID, mobyclient.ServiceInspectOptions{})
	if err != nil {
		return nil, err
	}
	return &result.Service, nil
}

func inspectDockerInfo(docker *dockerapi.Client) (*system.Info, error) {
	client, err := newSwarmAPIClient(docker)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), swarmAPIRequestTimeout)
	defer cancel()
	result, err := client.Info(ctx, mobyclient.InfoOptions{})
	if err != nil {
		return nil, err
	}
	return &result.Info, nil
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
			resolved.ExposedIP = network.ip
			resolved.NetworkNames = []string{network.name}
			out = append(out, resolved)
		}
	}
	return out, nil
}

func (r *swarmPortResolver) inspectService(serviceID string) (*swarmapi.Service, error) {
	if r.runtime.Role == "manager" {
		return r.inspectServiceLocal(serviceID)
	}
	service, err := r.inspectServiceLocal(serviceID)
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
			return nil, fmt.Errorf("unable to inspect service %s locally (%v) and from manager list: no manager node address discovered (check swarm manager availability and peer reachability)", serviceID, err)
		}
		return nil, fmt.Errorf("unable to inspect service %s: local inspection returned no published ports and no manager node address discovered (check swarm manager availability and peer reachability)", serviceID)
	}
	log.Printf("swarm manager fallback: querying manager peers for %s on port %s: %s", serviceID, r.peerInfoPort, strings.Join(managers, ","))
	op := func() error {
		currentManagers := r.managerNodeAddrs()
		if len(currentManagers) == 0 {
			return fmt.Errorf("no manager node addresses available for service inspection")
		}
		if r.peerInfoPort == "" {
			return fmt.Errorf("manager peer status port is not configured")
		}
		for _, addr := range currentManagers {
			log.Printf("swarm manager handshake: attempting manager peer %s:%s for service %s", addr, r.peerInfoPort, serviceID)
			service, err = r.inspectServiceViaPeer(addr, serviceID)
			if err == nil {
				log.Printf("swarm manager handshake: manager peer %s:%s reachable for service %s", addr, r.peerInfoPort, serviceID)
				return nil
			}
			forgetManagerAddr(addr)
			log.Printf("swarm manager fallback: manager peer inspect failed for %s via %s:%s: %v", serviceID, addr, r.peerInfoPort, err)
		}
		return fmt.Errorf("unable to inspect service %s from manager list (worker needs manager peer reachability on port %s)", serviceID, r.peerInfoPort)
	}
	exp := backoff.NewExponentialBackOff()
	exp.MaxElapsedTime = managerRetryTimeout
	retryErr := backoff.Retry(op, exp)
	return service, retryErr
}

func (r *swarmPortResolver) inspectServiceLocal(serviceID string) (*swarmapi.Service, error) {
	if r.swarmClient == nil {
		return nil, fmt.Errorf("swarm api client unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), swarmAPIRequestTimeout)
	defer cancel()
	result, err := r.swarmClient.ServiceInspect(ctx, serviceID, mobyclient.ServiceInspectOptions{})
	if err != nil {
		return nil, err
	}
	return &result.Service, nil
}

func (r *swarmPortResolver) inspectServiceViaPeer(addr, serviceID string) (*swarmapi.Service, error) {
	client := &http.Client{Timeout: peerInfoRequestTimeout}
	endpoint := "http://" + net.JoinHostPort(addr, r.peerInfoPort) + "/swarm/service/" + url.PathEscape(serviceID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	addStatusToken(req, r.statusToken)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manager peer service inspect status %d", resp.StatusCode)
	}
	var service swarmapi.Service
	if err := json.NewDecoder(resp.Body).Decode(&service); err != nil {
		return nil, err
	}
	return &service, nil
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
	if r.swarmClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), swarmAPIRequestTimeout)
		nodes, err := r.swarmClient.NodeList(ctx, mobyclient.NodeListOptions{})
		cancel()
		if err == nil {
			for _, addr := range managerAddrsFromNodes(nodes.Items) {
				addrSet[addr] = struct{}{}
			}
		} else {
			log.Printf("swarm manager fallback: unable to list nodes: %v", err)
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
		log.Printf("swarm manager fallback: task DNS lookup failed for tasks.%s: %v", r.runtime.SwarmServiceName, err)
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
	if r.peerInfoPort != "" {
		client := &http.Client{Timeout: peerInfoRequestTimeout}
		managerAddrSet := make(map[string]struct{})
		for _, addr := range addrs {
			if net.ParseIP(addr) == nil {
				continue
			}
			info, err := fetchPeerInfo(client, "http://"+net.JoinHostPort(addr, r.peerInfoPort)+"/peerinfo", r.statusToken)
			if err != nil || info.Role != "manager" {
				continue
			}
			if info.NodeAddr != "" {
				managerAddrSet[info.NodeAddr] = struct{}{}
			}
			if info.OverlayIP != "" {
				managerAddrSet[info.OverlayIP] = struct{}{}
			}
		}
		if len(managerAddrSet) > 0 {
			managerAddrs := make([]string, 0, len(managerAddrSet))
			for addr := range managerAddrSet {
				managerAddrs = append(managerAddrs, addr)
			}
			sort.Strings(managerAddrs)
			return managerAddrs
		}
	}
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
		if strings.EqualFold(name, "ingress") {
			continue
		}
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
		if !addr.IsValid() {
			return ""
		}
		return addr.Addr().String()
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
