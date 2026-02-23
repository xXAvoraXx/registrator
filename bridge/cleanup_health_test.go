package bridge

import (
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
)

func TestCleanupUnhealthyReason(t *testing.T) {
	if got := cleanupUnhealthyReason(nil); got != "container state unavailable" {
		t.Fatalf("expected unavailable state reason, got %q", got)
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

func TestCollectContainerIPsAndKnownCheck(t *testing.T) {
	ips := make(map[string]struct{})
	collectContainerIPs(&dockerapi.Container{
		NetworkSettings: &dockerapi.NetworkSettings{
			IPAddress: "172.18.0.10",
			Networks: map[string]dockerapi.ContainerNetwork{
				"app":     {IPAddress: "10.0.1.20"},
				"ingress": {IPAddress: "10.0.9.30"},
			},
		},
	}, ips)

	if !isIPKnownInDockerNetworks("172.18.0.10", ips) {
		t.Fatalf("expected primary network IP to be known")
	}
	if !isIPKnownInDockerNetworks("10.0.1.20", ips) {
		t.Fatalf("expected overlay network IP to be known")
	}
	if !isIPKnownInDockerNetworks("10.0.9.30", ips) {
		t.Fatalf("expected ingress network IP to be known")
	}
	if isIPKnownInDockerNetworks("10.0.8.40", ips) {
		t.Fatalf("did not expect unknown IP to be marked as known")
	}
}

func TestRunningContainerIPsUsesListingNetworkIPs(t *testing.T) {
	b := &Bridge{}
	ips := b.runningContainerIPs([]dockerapi.APIContainers{
		{
			ID: "container-1",
			Networks: dockerapi.NetworkList{
				Networks: map[string]dockerapi.ContainerNetwork{
					"app": {IPAddress: "10.10.0.5"},
				},
			},
		},
	})
	if !isIPKnownInDockerNetworks("10.10.0.5", ips) {
		t.Fatalf("expected listing network IP to be known")
	}
}
