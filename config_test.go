package main

import (
	"testing"

	testassert "github.com/stretchr/testify/assert"
)

func TestEnvOverridesConfigDefaults(t *testing.T) {
	t.Setenv("REGISTRATOR_DISCOVERY_PROVIDER", "consul")
	t.Setenv("REGISTRATOR_DISCOVERY_MODE", "service")
	t.Setenv("REGISTRATOR_DISCOVERY_SERVICE_NAME", "consul-agent")
	t.Setenv("REGISTRATOR_DISCOVERY_PORT", "8600")
	t.Setenv("REGISTRATOR_SERVICE_NAME_SOURCE", "container.name")

	cfg := defaultAppConfig()
	applyEnvOverrides(&cfg)

	testassert.Equal(t, "service", cfg.Discovery.Mode)
	testassert.Equal(t, "consul-agent", cfg.Discovery.ServiceName)
	testassert.Equal(t, 8600, cfg.Discovery.Port)
	testassert.Equal(t, "container.name", cfg.Service.NameSource)
	testassert.Equal(t, "consul://consul-agent:8600", buildRegistryURI(cfg))
}

func TestLoadConfigWithoutFileUsesDefaults(t *testing.T) {
	t.Setenv("REGISTRATOR_CONFIG", "/tmp/does-not-exist-"+t.Name())
	cfg, err := loadAppConfig()
	testassert.NoError(t, err)
	testassert.Equal(t, "consul", cfg.Discovery.Provider)
	testassert.Equal(t, "unix:///var/run/docker.sock", cfg.Docker.Endpoint)
}

func TestRuntimeEnvOverrides(t *testing.T) {
	t.Setenv("REGISTRATOR_RUNTIME_HOST_IP", "10.10.0.1")
	t.Setenv("REGISTRATOR_RUNTIME_INTERNAL", "true")
	t.Setenv("REGISTRATOR_RUNTIME_EXPLICIT", "true")
	t.Setenv("REGISTRATOR_RUNTIME_FORCE_TAGS", "blue,canary")
	t.Setenv("REGISTRATOR_RUNTIME_REFRESH_TTL", "15")
	t.Setenv("REGISTRATOR_RUNTIME_REFRESH_INTERVAL", "30")
	t.Setenv("REGISTRATOR_RUNTIME_DEREGISTER_CHECK", "on-success")
	t.Setenv("REGISTRATOR_RUNTIME_CLEANUP", "false")
	t.Setenv("REGISTRATOR_RUNTIME_RETRY_ATTEMPTS", "-1")
	t.Setenv("REGISTRATOR_RUNTIME_RETRY_INTERVAL_MS", "500")
	t.Setenv("REGISTRATOR_RUNTIME_RESYNC_INTERVAL", "60")

	cfg := defaultAppConfig()
	applyEnvOverrides(&cfg)

	testassert.Equal(t, "10.10.0.1", cfg.Runtime.HostIP)
	testassert.True(t, cfg.Runtime.Internal)
	testassert.True(t, cfg.Runtime.Explicit)
	testassert.Equal(t, "blue,canary", cfg.Runtime.ForceTags)
	testassert.Equal(t, 15, cfg.Runtime.RefreshTTL)
	testassert.Equal(t, 30, cfg.Runtime.RefreshInterval)
	testassert.Equal(t, "on-success", cfg.Runtime.DeregisterCheck)
	testassert.False(t, cfg.Runtime.Cleanup)
	testassert.Equal(t, -1, cfg.Runtime.RetryAttempts)
	testassert.Equal(t, 500, cfg.Runtime.RetryIntervalMs)
	testassert.Equal(t, 60, cfg.Runtime.ResyncInterval)
}

func TestCLIOverridesEnvAndConfig(t *testing.T) {
	t.Setenv("REGISTRATOR_RUNTIME_INTERNAL", "false")
	t.Setenv("REGISTRATOR_RUNTIME_RETRY_ATTEMPTS", "10")
	t.Setenv("REGISTRATOR_DISCOVERY_MODE", "local")

	cfg := defaultAppConfig()
	applyEnvOverrides(&cfg)

	err := applyCLIOverrides(&cfg, []string{"-REGISTRATOR_RUNTIME_INTERNAL=true", "-REGISTRATOR_RUNTIME_RETRY_ATTEMPTS=2", "-REGISTRATOR_DISCOVERY_MODE=service"})
	testassert.NoError(t, err)
	testassert.True(t, cfg.Runtime.Internal)
	testassert.Equal(t, 2, cfg.Runtime.RetryAttempts)
	testassert.Equal(t, "service", cfg.Discovery.Mode)
}

func TestCLIOverridesRejectEntrypointArgument(t *testing.T) {
	cfg := defaultAppConfig()
	err := applyCLIOverrides(&cfg, []string{"/bin/registrator", "-REGISTRATOR_DISCOVERY_ADDRESS=consul"})
	testassert.Error(t, err)
}

func TestCLIOverridesWithoutPositionalArguments(t *testing.T) {
	cfg := defaultAppConfig()
	err := applyCLIOverrides(&cfg, []string{"-REGISTRATOR_DISCOVERY_ADDRESS=consul"})
	testassert.NoError(t, err)
	testassert.Equal(t, "consul", cfg.Discovery.Address)
}

func TestCLIOverridesSupportServiceDiscoveryModeAlias(t *testing.T) {
	cfg := defaultAppConfig()
	cfg.Discovery.Mode = "local"
	err := applyCLIOverrides(&cfg, []string{"-SERVICE_DISCOVERY_MODE=service"})
	testassert.NoError(t, err)
	testassert.Equal(t, "service", cfg.Discovery.Mode)
}

func TestCLIOverridesPreferRegistratorDiscoveryModeOverAlias(t *testing.T) {
	cfg := defaultAppConfig()
	err := applyCLIOverrides(&cfg, []string{"-REGISTRATOR_DISCOVERY_MODE=local", "-SERVICE_DISCOVERY_MODE=service"})
	testassert.NoError(t, err)
	testassert.Equal(t, "local", cfg.Discovery.Mode)
}

func TestCLIOverridesRejectLegacyPositionalURI(t *testing.T) {
	cfg := defaultAppConfig()
	err := applyCLIOverrides(&cfg, []string{"consul://consul:8500"})
	testassert.Error(t, err)
}
