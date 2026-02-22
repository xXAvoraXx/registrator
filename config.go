package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v2"
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
	cfg.Docker.Endpoint = "unix:///tmp/docker.sock"
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
	filteredArgs := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "/bin/registrator" || arg == "registrator" {
			continue
		}
		filteredArgs = append(filteredArgs, arg)
	}

	fs := flag.NewFlagSet("registrator", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	internal := fs.Bool("internal", cfg.Runtime.Internal, "")
	ip := fs.String("ip", cfg.Runtime.HostIP, "")
	resync := fs.Int("resync", cfg.Runtime.ResyncInterval, "")
	retryAttempts := fs.Int("retry-attempts", cfg.Runtime.RetryAttempts, "")
	retryInterval := fs.Int("retry-interval", cfg.Runtime.RetryIntervalMs, "")
	tags := fs.String("tags", cfg.Runtime.ForceTags, "")
	ttl := fs.Int("ttl", cfg.Runtime.RefreshTTL, "")
	ttlRefresh := fs.Int("ttl-refresh", cfg.Runtime.RefreshInterval, "")
	deregister := fs.String("deregister", cfg.Runtime.DeregisterCheck, "")
	cleanup := fs.Bool("cleanup", cfg.Runtime.Cleanup, "")
	useIPFromLabel := fs.String("useIpFromLabel", cfg.Runtime.UseIPFromLabel, "")

	if err := fs.Parse(filteredArgs); err != nil {
		return err
	}

	cfg.Runtime.Internal = *internal
	cfg.Runtime.HostIP = *ip
	cfg.Runtime.ResyncInterval = *resync
	cfg.Runtime.RetryAttempts = *retryAttempts
	cfg.Runtime.RetryIntervalMs = *retryInterval
	cfg.Runtime.ForceTags = *tags
	cfg.Runtime.RefreshTTL = *ttl
	cfg.Runtime.RefreshInterval = *ttlRefresh
	cfg.Runtime.DeregisterCheck = *deregister
	cfg.Runtime.Cleanup = *cleanup
	cfg.Runtime.UseIPFromLabel = *useIPFromLabel

	for _, arg := range fs.Args() {
		if strings.Contains(arg, "://") {
			if err := applyRegistryURIOverride(cfg, arg); err != nil {
				return err
			}
			continue
		}
		return fmt.Errorf("unexpected argument: %s", arg)
	}

	return nil
}

func applyRegistryURIOverride(cfg *AppConfig, registryURI string) error {
	parsed, err := url.Parse(registryURI)
	if err != nil {
		return err
	}
	if parsed.Scheme == "" {
		return fmt.Errorf("invalid registry uri: %s", registryURI)
	}
	cfg.Discovery.Provider = parsed.Scheme
	if host := parsed.Hostname(); host != "" {
		cfg.Discovery.Address = host
	}
	if port := parsed.Port(); port != "" {
		p, err := strconv.Atoi(port)
		if err != nil {
			return err
		}
		cfg.Discovery.Port = p
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
