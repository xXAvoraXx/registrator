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
}
