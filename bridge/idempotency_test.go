package bridge

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type countingRegistryAdapter struct {
	registerCalls int
}

func (c *countingRegistryAdapter) Ping() error { return nil }
func (c *countingRegistryAdapter) Register(service *Service) error {
	c.registerCalls++
	return nil
}
func (c *countingRegistryAdapter) Deregister(service *Service) error { return nil }
func (c *countingRegistryAdapter) Refresh(service *Service) error    { return nil }
func (c *countingRegistryAdapter) Services() ([]*Service, error)     { return nil, nil }

func TestServiceHashStableForTagOrder(t *testing.T) {
	svc1 := &Service{
		ID:    "id-1",
		Name:  "svc",
		IP:    "10.0.0.1",
		Port:  8080,
		Tags:  []string{"b", "a"},
		Attrs: map[string]string{"k1": "v1", "k2": "v2"},
		TTL:   10,
	}
	svc2 := &Service{
		ID:    "id-1",
		Name:  "svc",
		IP:    "10.0.0.1",
		Port:  8080,
		Tags:  []string{"a", "b"},
		Attrs: map[string]string{"k2": "v2", "k1": "v1"},
		TTL:   10,
	}

	assert.Equal(t, serviceHash(svc1), serviceHash(svc2))
}

func TestServiceHashChangesWhenServiceChanges(t *testing.T) {
	svc1 := &Service{ID: "id-1", Name: "svc", IP: "10.0.0.1", Port: 8080}
	svc2 := &Service{ID: "id-1", Name: "svc", IP: "10.0.0.1", Port: 9090}

	assert.NotEqual(t, serviceHash(svc1), serviceHash(svc2))
}

func TestDuplicateServiceIDsRemovesDuplicateBySignature(t *testing.T) {
	services := []*Service{
		{ID: "id-1", Name: "svc", IP: "10.0.0.1", Port: 8080, Tags: []string{"a"}},
		{ID: "id-2", Name: "svc", IP: "10.0.0.1", Port: 8080, Tags: []string{"a"}},
		{ID: "id-3", Name: "svc", IP: "10.0.0.1", Port: 9090, Tags: []string{"a"}},
	}

	duplicates := duplicateServiceIDs(services, map[string]struct{}{})
	assert.Equal(t, []string{"id-2"}, duplicates)
}

func TestDuplicateServiceIDsPrefersKnownLocalServiceID(t *testing.T) {
	services := []*Service{
		{ID: "old-id", Name: "svc", IP: "10.0.0.1", Port: 8080, Tags: []string{"a"}},
		{ID: "current-id", Name: "svc", IP: "10.0.0.1", Port: 8080, Tags: []string{"a"}},
	}

	duplicates := duplicateServiceIDs(services, map[string]struct{}{"current-id": {}})
	assert.Equal(t, []string{"old-id"}, duplicates)
}

func TestSeedServiceHashesPruneStaleHashEntries(t *testing.T) {
	adapter := &countingRegistryAdapter{}
	b := &Bridge{
		registry:      adapter,
		serviceHashes: map[string]string{},
	}
	service := &Service{ID: "svc-1", Name: "svc", IP: "10.0.0.1", Port: 8080}
	b.serviceHashes[service.ID] = serviceHash(service)
	b.serviceHashes["stale"] = "stale-hash"

	b.seedServiceHashes(nil)
	assert.Empty(t, b.serviceHashes)

	assert.NoError(t, b.registerService(service))
	assert.Equal(t, 1, adapter.registerCalls)
}
