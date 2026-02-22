package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"
)

const (
	flagRegistratorDiscoveryMode = "REGISTRATOR_DISCOVERY_MODE"
	flagServiceDiscoveryMode     = "SERVICE_DISCOVERY_MODE"
)

type AppConfig struct {
	Discovery struct {
		Provider         string `json:"provider" yaml:"provider"`
		Mode             string `json:"mode" yaml:"mode"`
		Address          string `json:"address" yaml:"address"`
		Port             int    `json:"port" yaml:"port"`
		ServiceName      string `json:"serviceName" yaml:"serviceName"`
		UseDockerResolve bool   `json:"useDockerResolve" yaml:"useDockerResolve"`
	} `json:"discovery" yaml:"discovery"`
	Service struct {
		NameSource string `json:"nameSource" yaml:"nameSource"`
		LabelKey   string `json:"labelKey" yaml:"labelKey"`
		IDFormat   string `json:"idFormat" yaml:"idFormat"`
	} `json:"service" yaml:"service"`
	Docker struct {
		Endpoint  string `json:"endpoint" yaml:"endpoint"`
		SwarmMode bool   `json:"swarmMode" yaml:"swarmMode"`
	} `json:"docker" yaml:"docker"`
	Logging struct {
		Level string `json:"level" yaml:"level"`
	} `json:"logging" yaml:"logging"`
	Runtime struct {
		HostIP              string `json:"hostIP" yaml:"hostIP"`
		Internal            bool   `json:"internal" yaml:"internal"`
		Explicit            bool   `json:"explicit" yaml:"explicit"`
		UseIPFromLabel      string `json:"useIPFromLabel" yaml:"useIPFromLabel"`
		ForceTags           string `json:"forceTags" yaml:"forceTags"`
		RefreshTTL          int    `json:"refreshTTL" yaml:"refreshTTL"`
		RefreshInterval     int    `json:"refreshInterval" yaml:"refreshInterval"`
		DeregisterCheck     string `json:"deregisterCheck" yaml:"deregisterCheck"`
		Cleanup             bool   `json:"cleanup" yaml:"cleanup"`
		RetryAttempts       int    `json:"retryAttempts" yaml:"retryAttempts"`
		RetryIntervalMs     int    `json:"retryIntervalMs" yaml:"retryIntervalMs"`
		ResyncInterval      int    `json:"resyncInterval" yaml:"resyncInterval"`
		StatusAddr          string `json:"statusAddr" yaml:"statusAddr"`
		AdvertiseMode       string `json:"advertiseMode" yaml:"advertiseMode"`
		AdvertiseIPOverride string `json:"advertiseIPOverride" yaml:"advertiseIPOverride"`
		ManagerAPIPort      int    `json:"managerAPIPort" yaml:"managerAPIPort"`
	} `json:"runtime" yaml:"runtime"`
}

func defaultAppConfig() AppConfig {
	cfg := AppConfig{}
	cfg.Discovery.Provider = "consul"
	cfg.Discovery.Mode = "local"
	cfg.Discovery.Port = 8500
	cfg.Discovery.ServiceName = "consul"
	cfg.Discovery.UseDockerResolve = true
	cfg.Service.NameSource = "service.name"
	cfg.Service.LabelKey = "service.name"
	cfg.Service.IDFormat = "{hostname}:{name}:{port}"
	cfg.Docker.Endpoint = "unix:///var/run/docker.sock"
	cfg.Docker.SwarmMode = true
	cfg.Logging.Level = "info"
	cfg.Runtime.DeregisterCheck = "always"
	cfg.Runtime.Cleanup = true
	cfg.Runtime.RetryAttempts = 10
	cfg.Runtime.RetryIntervalMs = 2000
	cfg.Runtime.ResyncInterval = 30
	cfg.Runtime.AdvertiseMode = "node-ip"
	cfg.Runtime.ManagerAPIPort = 2375
	return cfg
}

func loadAppConfig() (AppConfig, error) {
	cfg := defaultAppConfig()
	configPath := os.Getenv("REGISTRATOR_CONFIG")
	if configPath == "" {
		configPath = "/etc/registrator/config.yaml"
	}
	if _, err := os.Stat(configPath); err == nil {
		b, err := os.ReadFile(configPath)
		if err != nil {
			return cfg, err
		}
		switch strings.ToLower(filepath.Ext(configPath)) {
		case ".json":
			if err := json.Unmarshal(b, &cfg); err != nil {
				return cfg, err
			}
		default:
			if err := yaml.Unmarshal(b, &cfg); err != nil {
				return cfg, err
			}
		}
	}
	applyEnvOverrides(&cfg)
	return cfg, nil
}

func applyCLIOverrides(cfg *AppConfig, args []string) error {
	fs := flag.NewFlagSet("registrator", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	discoveryProvider := fs.String("REGISTRATOR_DISCOVERY_PROVIDER", cfg.Discovery.Provider, "")
	discoveryMode := fs.String(flagRegistratorDiscoveryMode, cfg.Discovery.Mode, "")
	serviceDiscoveryMode := fs.String(flagServiceDiscoveryMode, cfg.Discovery.Mode, "")
	discoveryAddress := fs.String("REGISTRATOR_DISCOVERY_ADDRESS", cfg.Discovery.Address, "")
	discoveryPort := fs.Int("REGISTRATOR_DISCOVERY_PORT", cfg.Discovery.Port, "")
	discoveryServiceName := fs.String("REGISTRATOR_DISCOVERY_SERVICE_NAME", cfg.Discovery.ServiceName, "")
	discoveryUseDockerResolve := fs.Bool("REGISTRATOR_DISCOVERY_USE_DOCKER_RESOLVE", cfg.Discovery.UseDockerResolve, "")
	serviceNameSource := fs.String("REGISTRATOR_SERVICE_NAME_SOURCE", cfg.Service.NameSource, "")
	serviceLabelKey := fs.String("REGISTRATOR_SERVICE_LABEL_KEY", cfg.Service.LabelKey, "")
	serviceIDFormat := fs.String("REGISTRATOR_SERVICE_ID_FORMAT", cfg.Service.IDFormat, "")
	dockerEndpoint := fs.String("REGISTRATOR_DOCKER_ENDPOINT", cfg.Docker.Endpoint, "")
	dockerSwarmMode := fs.Bool("REGISTRATOR_DOCKER_SWARM_MODE", cfg.Docker.SwarmMode, "")
	statusAddr := fs.String("REGISTRATOR_STATUS_ADDR", cfg.Runtime.StatusAddr, "")
	runtimeHostIP := fs.String("REGISTRATOR_RUNTIME_HOST_IP", cfg.Runtime.HostIP, "")
	runtimeInternal := fs.Bool("REGISTRATOR_RUNTIME_INTERNAL", cfg.Runtime.Internal, "")
	runtimeExplicit := fs.Bool("REGISTRATOR_RUNTIME_EXPLICIT", cfg.Runtime.Explicit, "")
	runtimeForceTags := fs.String("REGISTRATOR_RUNTIME_FORCE_TAGS", cfg.Runtime.ForceTags, "")
	runtimeRefreshTTL := fs.Int("REGISTRATOR_RUNTIME_REFRESH_TTL", cfg.Runtime.RefreshTTL, "")
	runtimeRefreshInterval := fs.Int("REGISTRATOR_RUNTIME_REFRESH_INTERVAL", cfg.Runtime.RefreshInterval, "")
	runtimeDeregisterCheck := fs.String("REGISTRATOR_RUNTIME_DEREGISTER_CHECK", cfg.Runtime.DeregisterCheck, "")
	runtimeCleanup := fs.Bool("REGISTRATOR_RUNTIME_CLEANUP", cfg.Runtime.Cleanup, "")
	runtimeRetryAttempts := fs.Int("REGISTRATOR_RUNTIME_RETRY_ATTEMPTS", cfg.Runtime.RetryAttempts, "")
	runtimeRetryIntervalMs := fs.Int("REGISTRATOR_RUNTIME_RETRY_INTERVAL_MS", cfg.Runtime.RetryIntervalMs, "")
	runtimeResyncInterval := fs.Int("REGISTRATOR_RUNTIME_RESYNC_INTERVAL", cfg.Runtime.ResyncInterval, "")
	runtimeManagerAPIPort := fs.Int("REGISTRATOR_RUNTIME_MANAGER_API_PORT", cfg.Runtime.ManagerAPIPort, "")

	if err := fs.Parse(args); err != nil {
		return err
	}
	discoveryModeFlagProvided := false
	serviceDiscoveryModeFlagProvided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == flagRegistratorDiscoveryMode {
			discoveryModeFlagProvided = true
		}
		if f.Name == flagServiceDiscoveryMode {
			serviceDiscoveryModeFlagProvided = true
		}
	})

	cfg.Discovery.Provider = *discoveryProvider
	cfg.Discovery.Mode = *discoveryMode
	cfg.Discovery.Address = *discoveryAddress
	cfg.Discovery.Port = *discoveryPort
	cfg.Discovery.ServiceName = *discoveryServiceName
	cfg.Discovery.UseDockerResolve = *discoveryUseDockerResolve
	if !discoveryModeFlagProvided && serviceDiscoveryModeFlagProvided {
		cfg.Discovery.Mode = *serviceDiscoveryMode
	}
	cfg.Service.NameSource = *serviceNameSource
	cfg.Service.LabelKey = *serviceLabelKey
	cfg.Service.IDFormat = *serviceIDFormat
	cfg.Docker.Endpoint = *dockerEndpoint
	cfg.Docker.SwarmMode = *dockerSwarmMode
	cfg.Runtime.StatusAddr = *statusAddr
	cfg.Runtime.HostIP = *runtimeHostIP
	cfg.Runtime.Internal = *runtimeInternal
	cfg.Runtime.Explicit = *runtimeExplicit
	cfg.Runtime.ForceTags = *runtimeForceTags
	cfg.Runtime.RefreshTTL = *runtimeRefreshTTL
	cfg.Runtime.RefreshInterval = *runtimeRefreshInterval
	cfg.Runtime.DeregisterCheck = *runtimeDeregisterCheck
	cfg.Runtime.Cleanup = *runtimeCleanup
	cfg.Runtime.RetryAttempts = *runtimeRetryAttempts
	cfg.Runtime.RetryIntervalMs = *runtimeRetryIntervalMs
	cfg.Runtime.ResyncInterval = *runtimeResyncInterval
	cfg.Runtime.ManagerAPIPort = *runtimeManagerAPIPort

	if extra := fs.Args(); len(extra) > 0 {
		return fmt.Errorf("unexpected argument: %s", extra[0])
	}

	return nil
}

func applyEnvOverrides(cfg *AppConfig) {
	if v := os.Getenv("REGISTRATOR_DISCOVERY_PROVIDER"); v != "" {
		cfg.Discovery.Provider = v
	}
	if v := os.Getenv("REGISTRATOR_DISCOVERY_MODE"); v != "" {
		cfg.Discovery.Mode = v
	}
	if v := os.Getenv("REGISTRATOR_DISCOVERY_ADDRESS"); v != "" {
		cfg.Discovery.Address = v
	}
	if v := os.Getenv("REGISTRATOR_DISCOVERY_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			cfg.Discovery.Port = p
		}
	}
	if v := os.Getenv("REGISTRATOR_DISCOVERY_SERVICE_NAME"); v != "" {
		cfg.Discovery.ServiceName = v
	}
	if v := os.Getenv("REGISTRATOR_DISCOVERY_USE_DOCKER_RESOLVE"); v != "" {
		cfg.Discovery.UseDockerResolve = strings.EqualFold(v, "true")
	}
	if v := os.Getenv("REGISTRATOR_SERVICE_NAME_SOURCE"); v != "" {
		cfg.Service.NameSource = v
	}
	if v := os.Getenv("REGISTRATOR_SERVICE_LABEL_KEY"); v != "" {
		cfg.Service.LabelKey = v
	}
	if v := os.Getenv("REGISTRATOR_SERVICE_ID_FORMAT"); v != "" {
		cfg.Service.IDFormat = v
	}
	if v := os.Getenv("REGISTRATOR_DOCKER_ENDPOINT"); v != "" {
		cfg.Docker.Endpoint = v
	}
	if v := os.Getenv("REGISTRATOR_DOCKER_SWARM_MODE"); v != "" {
		cfg.Docker.SwarmMode = strings.EqualFold(v, "true")
	}
	if v := os.Getenv("REGISTRATOR_STATUS_ADDR"); v != "" {
		cfg.Runtime.StatusAddr = v
	}
	if v := os.Getenv("REGISTRATOR_RUNTIME_HOST_IP"); v != "" {
		cfg.Runtime.HostIP = v
	}
	if v := os.Getenv("REGISTRATOR_RUNTIME_INTERNAL"); v != "" {
		cfg.Runtime.Internal = strings.EqualFold(v, "true")
	}
	if v := os.Getenv("REGISTRATOR_RUNTIME_EXPLICIT"); v != "" {
		cfg.Runtime.Explicit = strings.EqualFold(v, "true")
	}
	if v := os.Getenv("REGISTRATOR_RUNTIME_FORCE_TAGS"); v != "" {
		cfg.Runtime.ForceTags = v
	}
	if v := os.Getenv("REGISTRATOR_RUNTIME_REFRESH_TTL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Runtime.RefreshTTL = n
		}
	}
	if v := os.Getenv("REGISTRATOR_RUNTIME_REFRESH_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Runtime.RefreshInterval = n
		}
	}
	if v := os.Getenv("REGISTRATOR_RUNTIME_DEREGISTER_CHECK"); v != "" {
		cfg.Runtime.DeregisterCheck = v
	}
	if v := os.Getenv("REGISTRATOR_RUNTIME_CLEANUP"); v != "" {
		cfg.Runtime.Cleanup = strings.EqualFold(v, "true")
	}
	if v := os.Getenv("REGISTRATOR_RUNTIME_RETRY_ATTEMPTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Runtime.RetryAttempts = n
		}
	}
	if v := os.Getenv("REGISTRATOR_RUNTIME_RETRY_INTERVAL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Runtime.RetryIntervalMs = n
		}
	}
	if v := os.Getenv("REGISTRATOR_RUNTIME_RESYNC_INTERVAL"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Runtime.ResyncInterval = n
		}
	}
	if v := os.Getenv("REGISTRATOR_RUNTIME_MANAGER_API_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.Runtime.ManagerAPIPort = n
		}
	}
}

func buildRegistryURI(cfg AppConfig) string {
	address := cfg.Discovery.Address
	if address == "" {
		switch cfg.Discovery.Mode {
		case "service":
			address = cfg.Discovery.ServiceName
		default:
			address = "127.0.0.1"
		}
	}
	return cfg.Discovery.Provider + "://" + address + ":" + strconv.Itoa(cfg.Discovery.Port)
}
