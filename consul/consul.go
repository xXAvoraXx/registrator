package consul

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/registrator/bridge"
	consulapi "github.com/hashicorp/consul/api"
	"github.com/hashicorp/go-cleanhttp"
)

const DefaultInterval = "10s"

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
	Mode             string
	Address          string
	Port             int
	ServiceName      string
	UseDockerResolve bool
}

var runtimeDockerClient *dockerapi.Client
var runtimeConfig RuntimeConfig

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
	log.Println("consul: current leader ", leader)

	return nil
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
		check.HTTP = fmt.Sprintf("http://%s:%d%s", service.IP, service.Port, path)
		if timeout := service.Attrs["check_timeout"]; timeout != "" {
			check.Timeout = timeout
		}
		if method := service.Attrs["check_http_method"]; method != "" {
			check.Method = method
		}
	} else if path := service.Attrs["check_https"]; path != "" {
		check.HTTP = fmt.Sprintf("https://%s:%d%s", service.IP, service.Port, path)
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
	out := make([]*bridge.Service, len(services))
	i := 0
	for _, v := range services {
		s := &bridge.Service{
			ID:   v.ID,
			Name: v.Service,
			Port: v.Port,
			Tags: v.Tags,
			IP:   v.Address,
		}
		out[i] = s
		i++
	}
	return out, nil
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
			return r.baseConfig.Address, nil
		}
		resolved, err := resolveLocalAgentAddress(runtimeDockerClient, service)
		if err != nil {
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
