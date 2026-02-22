package bridge

import (
	"testing"

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
	overridden := applyRuntimeOverrides(base, labels)
	assert.Equal(t, "from-label", overridden["name"])
	assert.Equal(t, "local", overridden["service.discovery.mode"])
	assert.Equal(t, "10.0.0.9", overridden["service.discovery.address"])
}
