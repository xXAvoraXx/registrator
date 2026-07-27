package bridge

import (
	"errors"
	"sort"
	"testing"
	"time"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
)

func TestRetryStopsWithinConfiguredWindow(t *testing.T) {
	previous := registryRetryMaxElapsedTime
	registryRetryMaxElapsedTime = 20 * time.Millisecond
	t.Cleanup(func() {
		registryRetryMaxElapsedTime = previous
	})

	started := time.Now()
	err := retry(func() error { return errors.New("backend unavailable") })
	if err == nil {
		t.Fatal("expected retry to return the backend error")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("expected bounded retry, took %s", elapsed)
	}
}

func TestServicePortPrefersNonIngressNetwork(t *testing.T) {
	container := &dockerapi.Container{
		Config:     &dockerapi.Config{},
		HostConfig: &dockerapi.HostConfig{},
		NetworkSettings: &dockerapi.NetworkSettings{
			IPAddress: "10.0.0.9",
			Networks: map[string]dockerapi.ContainerNetwork{
				"ingress":         {IPAddress: "10.0.0.9"},
				"dokploy-network": {IPAddress: "10.0.1.51"},
			},
		},
	}

	port := servicePort(container, dockerapi.Port("8080/tcp"), nil)
	assert.Equal(t, "10.0.1.51", port.ExposedIP)
	assert.Equal(t, []string{"dokploy-network"}, port.NetworkNames)
}

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
