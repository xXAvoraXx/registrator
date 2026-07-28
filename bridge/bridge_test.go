package bridge

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewError(t *testing.T) {
	bridge, err := New(nil, "", Config{})
	assert.Nil(t, bridge)
	assert.Error(t, err)
}

func TestNewValid(t *testing.T) {
	Register(new(fakeFactory), "fake")
	// Note: the following is valid for New() since it does not
	// actually connect to docker.
	bridge, err := New(nil, "fake://", Config{})

	assert.NotNil(t, bridge)
	assert.NoError(t, err)
}

func TestServiceCountDoesNotWaitForBridgeLock(t *testing.T) {
	b := &Bridge{serviceCount: 3}
	b.Lock()
	defer b.Unlock()

	done := make(chan int, 1)
	go func() {
		done <- b.ServiceCount()
	}()

	select {
	case count := <-done:
		assert.Equal(t, 3, count)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("ServiceCount blocked on the bridge lock")
	}
}
