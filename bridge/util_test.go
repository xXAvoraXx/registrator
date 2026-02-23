package bridge

import (
	"sort"
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
)

func TestEscapedComma(t *testing.T) {
	cases := []struct {
		Tag      string
		Expected []string
	}{
		{
			Tag:      "",
			Expected: []string{},
		},
		{
			Tag:      "foobar",
			Expected: []string{"foobar"},
		},
		{
			Tag:      "foo,bar",
			Expected: []string{"foo", "bar"},
		},
		{
			Tag:      "foo\\,bar",
			Expected: []string{"foo,bar"},
		},
		{
			Tag:      "foo,bar\\,baz",
			Expected: []string{"foo", "bar,baz"},
		},
		{
			Tag:      "\\,foobar\\,",
			Expected: []string{",foobar,"},
		},
		{
			Tag:      ",,,,foo,,,bar,,,",
			Expected: []string{"foo", "bar"},
		},
		{
			Tag:      ",,,,",
			Expected: []string{},
		},
		{
			Tag:      ",,\\,,",
			Expected: []string{","},
		},
	}

	for _, c := range cases {
		results := recParseEscapedComma(c.Tag)
		sort.Strings(c.Expected)
		sort.Strings(results)
		assert.EqualValues(t, c.Expected, results)
	}
}

func TestEnsureTag(t *testing.T) {
	tags := ensureTag([]string{"keygen", "db"}, registratorManagedTag)
	assert.EqualValues(t, []string{"keygen", "db", "registrator"}, tags)

	alreadyTagged := ensureTag([]string{"production", "Registrator"}, registratorManagedTag)
	assert.EqualValues(t, []string{"production", "Registrator"}, alreadyTagged)
}

func TestIsRegistratorManagedService(t *testing.T) {
	assert.True(t, isRegistratorManagedService(&Service{Tags: []string{"db", "registrator"}}))
	assert.True(t, isRegistratorManagedService(&Service{Tags: []string{"Registrator"}}))
	assert.True(t, isRegistratorManagedService(&Service{Name: "registrator", ID: "worker:registrator.1.taskid:8080"}))
	assert.True(t, isRegistratorManagedService(&Service{Name: "Registrator", ID: "worker:Registrator.1.taskid:8080"}))
	assert.False(t, isRegistratorManagedService(&Service{Tags: []string{"db"}}))
	assert.False(t, isRegistratorManagedService(&Service{Name: "registrator", ID: "worker:custom-service:8080"}))
	assert.False(t, isRegistratorManagedService(nil))
}

func TestServicePortIncludesNetworkNames(t *testing.T) {
	container := &dockerapi.Container{
		Config:     &dockerapi.Config{},
		HostConfig: &dockerapi.HostConfig{NetworkMode: "bridge"},
		NetworkSettings: &dockerapi.NetworkSettings{
			IPAddress: "172.18.0.4",
			Networks: map[string]dockerapi.ContainerNetwork{
				"dokploy-network": {IPAddress: "10.0.1.44"},
				"registrator":     {IPAddress: "10.0.1.45"},
			},
		},
	}

	port := servicePort(container, dockerapi.Port("3000/tcp"), nil)
	assert.ElementsMatch(t, []string{"dokploy-network", "registrator"}, port.NetworkNames)
}

func TestServiceMetaDataAppliesGenericCheckToAllPorts(t *testing.T) {
	config := &dockerapi.Config{
		Env: []string{
			"SERVICE_CHECK_HTTP=/health",
			"SERVICE_CHECK_INTERVAL=15s",
			"SERVICE_80_CHECK_HTTP=/health-80",
			"SERVICE_80_CHECK_TIMEOUT=1s",
		},
	}

	meta80, _ := serviceMetaData(config, "80")
	assert.Equal(t, "/health-80", meta80["check_http"])
	assert.Equal(t, "15s", meta80["check_interval"])
	assert.Equal(t, "1s", meta80["check_timeout"])

	meta443, _ := serviceMetaData(config, "443")
	assert.Equal(t, "/health", meta443["check_http"])
	assert.Equal(t, "15s", meta443["check_interval"])
	assert.Empty(t, meta443["check_timeout"])
}
