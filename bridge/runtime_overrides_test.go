package bridge

import (
	"errors"
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
)

func TestApplyRuntimeOverridesLabelPrecedence(t *testing.T) {
	base := map[string]string{
		"name": "from-env",
	}
	labels := map[string]string{
		"service.name":              "from-label",
		"service.discovery.mode":    "local",
		"service.discovery.address": "10.0.0.9",
	}
	overridden := applyRuntimeOverrides(base, labels, true)
	assert.Equal(t, "from-label", overridden["name"])
	assert.Equal(t, "local", overridden["service.discovery.mode"])
	assert.Equal(t, "10.0.0.9", overridden["service.discovery.address"])
}

func TestApplyRuntimeOverridesCanDisableDiscoveryLabels(t *testing.T) {
	base := map[string]string{}
	labels := map[string]string{
		"service.name":              "from-label",
		"service.discovery.mode":    "local",
		"service.discovery.address": "10.0.0.9",
	}
	overridden := applyRuntimeOverrides(base, labels, false)
	assert.Equal(t, "from-label", overridden["name"])
	assert.Empty(t, overridden["service.discovery.mode"])
	assert.Empty(t, overridden["service.discovery.address"])
}

func TestIsSwarmManagerOnlyError(t *testing.T) {
	managerOnlyErr := "This node is not a swarm manager"
	assert.True(t, isSwarmManagerOnlyError(errors.New(managerOnlyErr)))
	assert.False(t, isSwarmManagerOnlyError(errors.New("other error")))
	assert.False(t, isSwarmManagerOnlyError(nil))
}

func TestNewServiceDropsScriptChecksWhenDisabled(t *testing.T) {
	container := &dockerapi.Container{
		ID:   "abc123",
		Name: "/api.1.taskid",
		Config: &dockerapi.Config{
			Image: "api:latest",
			Env: []string{
				"SERVICE_CHECK_SCRIPT=curl http://example.invalid",
				"SERVICE_CHECK_CMD=/bin/check",
			},
		},
		HostConfig:      &dockerapi.HostConfig{},
		NetworkSettings: &dockerapi.NetworkSettings{IPAddress: "10.0.0.5"},
	}

	b := &Bridge{config: Config{Internal: true, AllowCheckScripts: false}}
	service := b.newService(ServicePort{
		ExposedIP:   "10.0.0.5",
		ExposedPort: "8080",
		PortType:    "tcp",
		container:   container,
	}, false)

	assert.NotNil(t, service)
	assert.Empty(t, service.Attrs["check_script"])
	assert.Empty(t, service.Attrs["check_cmd"])
}
