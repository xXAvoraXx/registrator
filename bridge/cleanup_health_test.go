package bridge

import (
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
)

func TestCleanupUnhealthyReason(t *testing.T) {
	if got := cleanupUnhealthyReason(nil); got == "" {
		t.Fatalf("expected reason for nil container")
	}
	if got := cleanupUnhealthyReason(&dockerapi.Container{
		State: dockerapi.State{Running: false},
	}); got != "container not running" {
		t.Fatalf("expected not running reason, got %q", got)
	}
	if got := cleanupUnhealthyReason(&dockerapi.Container{
		State: dockerapi.State{Running: true, Health: dockerapi.Health{Status: "unhealthy"}},
	}); got != "container health is unhealthy" {
		t.Fatalf("expected unhealthy reason, got %q", got)
	}
	if got := cleanupUnhealthyReason(&dockerapi.Container{
		State: dockerapi.State{Running: true, Health: dockerapi.Health{Status: "healthy"}},
	}); got != "" {
		t.Fatalf("expected healthy container to have no cleanup reason, got %q", got)
	}
}
