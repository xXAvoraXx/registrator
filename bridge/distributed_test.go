package bridge

import (
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/stretchr/testify/assert"
)

type countingRegistry struct {
	registerCalls   int
	deregisterCalls int
}

func (c *countingRegistry) Ping() error                   { return nil }
func (c *countingRegistry) Refresh(_ *Service) error      { return nil }
func (c *countingRegistry) Services() ([]*Service, error) { return nil, nil }
func (c *countingRegistry) Register(_ *Service) error     { c.registerCalls++; return nil }
func (c *countingRegistry) Deregister(_ *Service) error   { c.deregisterCalls++; return nil }

type coordinatorStub struct {
	allow bool
}

func (c *coordinatorStub) OwnsContainer(_ *dockerapi.Container) bool { return true }
func (c *coordinatorStub) BeforeRegister(_ *Service, _ string) (bool, error) {
	return c.allow, nil
}
func (c *coordinatorStub) AfterRegister(_ *Service, _ string) error { return nil }
func (c *coordinatorStub) BeforeDeregister(_ *Service) (bool, error) {
	return c.allow, nil
}
func (c *coordinatorStub) AfterDeregister(_ *Service) error { return nil }

func TestRegisterServiceSkipsDuplicates(t *testing.T) {
	reg := &countingRegistry{}
	b := &Bridge{
		registry:      reg,
		serviceHashes: map[string]string{},
	}
	svc := &Service{ID: "svc-a", Name: "svc", IP: "10.0.0.1", Port: 8080}
	assert.NoError(t, b.registerService(svc))
	assert.NoError(t, b.registerService(svc))
	assert.Equal(t, 1, reg.registerCalls)
}

func TestSeedServiceHashesPreventsDuplicateOnRestart(t *testing.T) {
	reg := &countingRegistry{}
	b := &Bridge{
		registry:      reg,
		serviceHashes: map[string]string{},
	}
	svc := &Service{ID: "svc-a", Name: "svc", IP: "10.0.0.1", Port: 8080}
	b.seedServiceHashes([]*Service{svc})
	assert.NoError(t, b.registerService(svc))
	assert.Equal(t, 0, reg.registerCalls)
}

func TestCoordinatorCanPreventRegistration(t *testing.T) {
	reg := &countingRegistry{}
	b := &Bridge{
		registry:      reg,
		serviceHashes: map[string]string{},
		config: Config{
			Coordinator: &coordinatorStub{allow: false},
		},
	}
	svc := &Service{ID: "svc-a", Name: "svc", IP: "10.0.0.1", Port: 8080}
	assert.NoError(t, b.registerService(svc))
	assert.Equal(t, 0, reg.registerCalls)
}
