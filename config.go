package main

import (
	"encoding/json"
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
