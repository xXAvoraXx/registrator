package consul

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/registrator/bridge"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-cleanhttp"
)

const (
	DefaultInterval         = "10s"
	missingServiceCheckAttr = "registrator_check_missing"
	consulRequestTimeout    = 10 * time.Second
)

func init() {
	f := new(Factory)
	bridge.Register(f, "consul")
	bridge.Register(f, "consul-tls")
	bridge.Register(f, "consul-unix")
}

func (r *ConsulAdapter) interpolateService(script string, service *bridge.Service) string {
	withIp := strings.Replace(script, "$SERVICE_IP", service.IP, -1)
	withPort := strings.Replace(withIp, "$SERVICE_PORT", strconv.Itoa(service.Port), -1)
	return withPort
}

type Factory struct{}

type RuntimeConfig struct {
	Mode              string
	Address           string
	Port              int
	ServiceName       string
	UseDockerResolve  bool
	RequireLocalAgent bool
	LocalNodeAddress  string
}

var runtimeDockerClient *dockerapi.Client
var runtimeConfig RuntimeConfig

const agentMemberAlive = 1

func ConfigureRuntime(docker *dockerapi.Client, cfg RuntimeConfig) {
	runtimeDockerClient = docker
	runtimeConfig = cfg
}

func (f *Factory) New(uri *url.URL) bridge.RegistryAdapter {
	config := consulapi.DefaultConfig()
	if uri.Scheme == "consul-unix" {
		config.Address = strings.TrimPrefix(uri.String(), "consul-")
	} else if uri.Scheme == "consul-tls" {
		tlsConfigDesc := &consulapi.TLSConfig{
			Address:            uri.Host,
			CAFile:             os.Getenv("CONSUL_CACERT"),
			CertFile:           os.Getenv("CONSUL_CLIENT_CERT"),
			KeyFile:            os.Getenv("CONSUL_CLIENT_KEY"),
			InsecureSkipVerify: false,
		}
		tlsConfig, err := consulapi.SetupTLSConfig(tlsConfigDesc)
		if err != nil {
			log.Fatal("Cannot set up Consul TLSConfig", err)
		}
		config.Scheme = "https"
		transport := cleanhttp.DefaultPooledTransport()
		transport.TLSClientConfig = tlsConfig
		config.Transport = transport
		config.Address = uri.Host
	} else if uri.Host != "" {
		config.Address = uri.Host
	}
	httpClient := config.HttpClient
	if httpClient == nil {
		httpClient = &http.Client{Transport: config.Transport}
	}
	httpClient.Timeout = consulRequestTimeout
	config.HttpClient = httpClient
	return &ConsulAdapter{baseConfig: config}
}

type ConsulAdapter struct {
	baseConfig *consulapi.Config
}

// Ping will try to connect to consul by attempting to retrieve the current leader.
func (r *ConsulAdapter) Ping() error {
	client, err := r.client(nil)
	if err != nil {
		return err
	}
	status := client.Status()
	leader, err := status.Leader()
	if err != nil {
		return err
	}
	if strings.TrimSpace(leader) == "" {
		return fmt.Errorf("consul: no current leader")
	}
	if runtimeConfig.Mode == "local" && runtimeConfig.RequireLocalAgent {
		if err := validateLocalAgentReadiness(client, runtimeConfig.LocalNodeAddress); err != nil {
			return err
		}
	}

	return nil
}

func validateLocalAgentReadiness(client *consulapi.Client, expectedAddress string) error {
	self, err := client.Agent().Self()
	if err != nil {
		return fmt.Errorf("consul: local agent self check failed: %w", err)
	}
	payload, err := json.Marshal(self)
	if err != nil {
		return fmt.Errorf("consul: local agent self payload could not be encoded: %w", err)
	}
	var local struct {
		Config struct {
			NodeName string `json:"NodeName"`
		} `json:"Config"`
		Member consulapi.AgentMember `json:"Member"`
	}
	if err := json.Unmarshal(payload, &local); err != nil {
		return fmt.Errorf("consul: local agent self payload could not be decoded: %w", err)
	}
	if local.Member.Name == "" {
		return fmt.Errorf("consul: local agent member name is empty")
	}
	if local.Config.NodeName != "" && local.Config.NodeName != local.Member.Name {
		return fmt.Errorf("consul: local agent node name mismatch: config=%q member=%q", local.Config.NodeName, local.Member.Name)
	}
	if local.Member.Status != agentMemberAlive {
		return fmt.Errorf("consul: local agent %q is not alive: status=%d", local.Member.Name, local.Member.Status)
	}
	if role := local.Member.Tags["role"]; role != "node" {
		return fmt.Errorf("consul: resolved agent %q has role %q, expected %q", local.Member.Name, role, "node")
	}
	if expectedAddress != "" && !sameAddress(local.Member.Addr, expectedAddress) {
		return fmt.Errorf("consul: local agent %q address %q does not match swarm node address %q", local.Member.Name, local.Member.Addr, expectedAddress)
	}

	catalogNode, _, err := client.Catalog().Node(local.Member.Name, nil)
	if err != nil {
		return fmt.Errorf("consul: catalog lookup for local agent %q failed: %w", local.Member.Name, err)
	}
	if catalogNode == nil || catalogNode.Node == nil {
		return fmt.Errorf("consul: local agent %q is not present in the catalog", local.Member.Name)
	}
	if catalogNode.Node.Node != local.Member.Name {
		return fmt.Errorf("consul: catalog returned node %q for local agent %q", catalogNode.Node.Node, local.Member.Name)
	}
	if expectedAddress != "" && !sameAddress(catalogNode.Node.Address, expectedAddress) {
		return fmt.Errorf("consul: catalog node %q address %q does not match swarm node address %q", local.Member.Name, catalogNode.Node.Address, expectedAddress)
	}
	return nil
}

func sameAddress(left, right string) bool {
	leftIP := net.ParseIP(strings.TrimSpace(left))
	rightIP := net.ParseIP(strings.TrimSpace(right))
	if leftIP != nil && rightIP != nil {
		return leftIP.Equal(rightIP)
	}
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

func (r *ConsulAdapter) Register(service *bridge.Service) error {
	client, err := r.client(service)
	if err != nil {
		return err
	}
	registration := new(consulapi.AgentServiceRegistration)
	registration.ID = service.ID
	registration.Name = service.Name
	registration.Port = service.Port
	registration.Tags = service.Tags
	registration.Address = service.IP
	registration.Check = r.buildCheck(service)
	registration.Meta = service.Attrs
	return client.Agent().ServiceRegister(registration)
}

func (r *ConsulAdapter) buildCheck(service *bridge.Service) *consulapi.AgentServiceCheck {
	check := new(consulapi.AgentServiceCheck)
	if status := service.Attrs["check_initial_status"]; status != "" {
		check.Status = status
	}
	if path := service.Attrs["check_http"]; path != "" {
		checkPort := service.Port
		if override := service.Attrs["check_http_port"]; override != "" {
			if parsed, err := strconv.Atoi(override); err == nil {
				checkPort = parsed
			}
		}
		check.HTTP = fmt.Sprintf("http://%s:%d%s", service.IP, checkPort, path)
		if timeout := service.Attrs["check_timeout"]; timeout != "" {
			check.Timeout = timeout
		}
		if method := service.Attrs["check_http_method"]; method != "" {
			check.Method = method
		}
	} else if path := service.Attrs["check_https"]; path != "" {
		checkPort := service.Port
		if override := service.Attrs["check_https_port"]; override != "" {
			if parsed, err := strconv.Atoi(override); err == nil {
				checkPort = parsed
			}
		}
		check.HTTP = fmt.Sprintf("https://%s:%d%s", service.IP, checkPort, path)
		if timeout := service.Attrs["check_timeout"]; timeout != "" {
			check.Timeout = timeout
		}
		if method := service.Attrs["check_https_method"]; method != "" {
			check.Method = method
		}
	} else if cmd := service.Attrs["check_cmd"]; cmd != "" {
		check.Args = []string{"check-cmd", service.Origin.ContainerID[:12], service.Origin.ExposedPort, cmd}
	} else if script := service.Attrs["check_script"]; script != "" {
		check.Args = []string{r.interpolateService(script, service)}
	} else if ttl := service.Attrs["check_ttl"]; ttl != "" {
		check.TTL = ttl
	} else if tcp := service.Attrs["check_tcp"]; tcp != "" {
		check.TCP = fmt.Sprintf("%s:%d", service.IP, service.Port)
		if timeout := service.Attrs["check_timeout"]; timeout != "" {
			check.Timeout = timeout
		}
	} else if grpc := service.Attrs["check_grpc"]; grpc != "" {
		check.GRPC = fmt.Sprintf("%s:%d", service.IP, service.Port)
		if timeout := service.Attrs["check_timeout"]; timeout != "" {
			check.Timeout = timeout
		}
		if useTLS := service.Attrs["check_grpc_use_tls"]; useTLS != "" {
			check.GRPCUseTLS = true
			if tlsSkipVerify := service.Attrs["check_tls_skip_verify"]; tlsSkipVerify != "" {
				check.TLSSkipVerify = true
			}
		}
	} else {
		return nil
	}
	if len(check.Args) != 0 || check.HTTP != "" || check.TCP != "" || check.GRPC != "" {
		if interval := service.Attrs["check_interval"]; interval != "" {
			check.Interval = interval
		} else {
			check.Interval = DefaultInterval
		}
	}
	if deregister_after := service.Attrs["check_deregister_after"]; deregister_after != "" {
		check.DeregisterCriticalServiceAfter = deregister_after
	}
	return check
}

func (r *ConsulAdapter) Deregister(service *bridge.Service) error {
	client, err := r.client(service)
	if err != nil {
		return err
	}
	return client.Agent().ServiceDeregister(service.ID)
}

func (r *ConsulAdapter) Refresh(service *bridge.Service) error {
	return nil
}

func (r *ConsulAdapter) Services() ([]*bridge.Service, error) {
	client, err := r.client(nil)
	if err != nil {
		return []*bridge.Service{}, err
	}
	services, err := client.Agent().Services()
	if err != nil {
		return []*bridge.Service{}, err
	}
	checks, err := client.Agent().Checks()
	if err != nil {
		log.Println("consul: unable to list agent checks during service reconciliation:", err)
		checks = map[string]*consulapi.AgentCheck{}
	}
	serviceChecks := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		if check == nil || check.ServiceID == "" {
			continue
		}
		serviceChecks[check.ServiceID] = struct{}{}
	}
	out := make([]*bridge.Service, len(services))
	i := 0
	for _, v := range services {
		attrs := make(map[string]string, len(v.Meta)+1)
		for key, value := range v.Meta {
			attrs[key] = value
		}
		if serviceWantsCheck(attrs) {
			if _, ok := serviceChecks[v.ID]; !ok {
				attrs[missingServiceCheckAttr] = "true"
			}
		}
		s := &bridge.Service{
			ID:    v.ID,
			Name:  v.Service,
			Port:  v.Port,
			Tags:  v.Tags,
			IP:    v.Address,
			Attrs: attrs,
		}
		out[i] = s
		i++
	}
	return out, nil
}

func serviceWantsCheck(attrs map[string]string) bool {
	for _, key := range []string{
		"check_http",
		"check_https",
		"check_cmd",
		"check_script",
		"check_ttl",
		"check_tcp",
		"check_grpc",
	} {
		if attrs[key] != "" {
			return true
		}
	}
	return false
}

func (r *ConsulAdapter) client(service *bridge.Service) (*consulapi.Client, error) {
	config := *r.baseConfig
	address, err := r.resolveAddress(service)
	if err != nil {
		return nil, err
	}
	if address != "" {
		config.Address = address
	}
	return consulapi.NewClient(&config)
}

func (r *ConsulAdapter) resolveAddress(service *bridge.Service) (string, error) {
	mode := runtimeConfig.Mode
	if mode == "" {
		return r.baseConfig.Address, nil
	}
	if service != nil {
		if v := service.Attrs["service.discovery.mode"]; v != "" {
			mode = v
		}
		if v := service.Attrs["service.discovery.address"]; v != "" {
			return withDefaultPort(v), nil
		}
	}
	switch mode {
	case "service":
		name := runtimeConfig.ServiceName
		if service != nil {
			if v := service.Attrs["service.discovery.name"]; v != "" {
				name = v
			}
		}
		if name == "" {
			name = "consul"
		}
		return fmt.Sprintf("%s:%d", name, runtimeConfig.Port), nil
	case "local":
		if runtimeConfig.Address != "" {
			return withDefaultPort(runtimeConfig.Address), nil
		}
		if !runtimeConfig.UseDockerResolve || runtimeDockerClient == nil {
			if runtimeConfig.RequireLocalAgent {
				return "", fmt.Errorf("consul: strict local agent resolution requires Docker resolution")
			}
			return r.baseConfig.Address, nil
		}
		resolved, err := resolveLocalAgentAddress(runtimeDockerClient, service)
		if err != nil {
			if runtimeConfig.RequireLocalAgent {
				return "", fmt.Errorf("consul: strict local agent resolution failed: %w", err)
			}
			if r.baseConfig.Address == "" {
				return "", err
			}
			log.Printf("consul: local docker resolve failed, falling back to configured address %q: %v", r.baseConfig.Address, err)
			return r.baseConfig.Address, nil
		}
		return fmt.Sprintf("%s:%d", resolved, runtimeConfig.Port), nil
	default:
		if runtimeConfig.Address != "" {
			return withDefaultPort(runtimeConfig.Address), nil
		}
		return r.baseConfig.Address, nil
	}
}

func withDefaultPort(address string) string {
	if strings.Contains(address, ":") {
		return address
	}
	if runtimeConfig.Port == 0 {
		return address
	}
	return fmt.Sprintf("%s:%d", address, runtimeConfig.Port)
}

func resolveLocalAgentAddress(docker *dockerapi.Client, service *bridge.Service) (string, error) {
	registratorContainer, err := resolveRegistratorContainer(docker)
	if err != nil {
		return "", err
	}
	registratorNetworks := containerNetworkNames(registratorContainer)

	targetNodeID := ""
	if service != nil && service.Origin.ContainerID != "" {
		container, err := docker.InspectContainer(service.Origin.ContainerID)
		if err == nil && container.Node != nil {
			targetNodeID = container.Node.ID
		}
	}
	if targetNodeID == "" {
		info, err := docker.Info()
		if err == nil {
			targetNodeID = info.Swarm.NodeID
		}
	}
	containers, err := docker.ListContainers(dockerapi.ListContainersOptions{All: false})
	if err != nil {
		return "", err
	}
	checked := 0
	serviceName := runtimeConfig.ServiceName
	if serviceName == "" {
		serviceName = "consul"
	}
	for _, listing := range containers {
		checked++
		c, err := docker.InspectContainer(listing.ID)
		if err != nil || c.Config == nil || c.NetworkSettings == nil {
			continue
		}
		isAgent := c.Config.Labels["consul.agent"] == "true"
		if !isAgent {
			if c.Config.Labels["com.docker.swarm.service.name"] == serviceName || strings.Contains(strings.TrimPrefix(c.Name, "/"), serviceName) {
				isAgent = true
			}
		}
		if !isAgent {
			continue
		}
		if targetNodeID != "" && c.Node != nil && c.Node.ID != "" && c.Node.ID != targetNodeID {
			continue
		}
		ip := selectSharedNetworkIP(registratorNetworks, c)
		if ip != "" {
			return ip, nil
		}
	}
	return "", fmt.Errorf("unable to resolve local consul agent for node %s: no running container matched label consul.agent=true or service name %q on a shared network (checked %d containers)", targetNodeID, serviceName, checked)
}

func resolveRegistratorContainer(docker *dockerapi.Client) (*dockerapi.Container, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("unable to resolve registrator hostname: %w", err)
	}
	container, err := docker.InspectContainer(hostname)
	if err != nil {
		return nil, fmt.Errorf("unable to inspect registrator container %q: %w", hostname, err)
	}
	if container == nil || container.NetworkSettings == nil {
		return nil, fmt.Errorf("registrator container network settings not available for %q", hostname)
	}
	return container, nil
}

func containerNetworkNames(container *dockerapi.Container) map[string]struct{} {
	names := make(map[string]struct{})
	if container == nil || container.NetworkSettings == nil {
		return names
	}
	for networkName := range container.NetworkSettings.Networks {
		names[networkName] = struct{}{}
	}
	return names
}

func selectSharedNetworkIP(registratorNetworks map[string]struct{}, candidate *dockerapi.Container) string {
	if candidate == nil || candidate.NetworkSettings == nil {
		return ""
	}
	sharedNames := make([]string, 0)
	for networkName := range candidate.NetworkSettings.Networks {
		if _, shared := registratorNetworks[networkName]; shared {
			sharedNames = append(sharedNames, networkName)
		}
	}
	sort.Strings(sharedNames)
	for _, networkName := range sharedNames {
		network := candidate.NetworkSettings.Networks[networkName]
		if _, shared := registratorNetworks[networkName]; shared && network.IPAddress != "" {
			return network.IPAddress
		}
	}
	return ""
}
