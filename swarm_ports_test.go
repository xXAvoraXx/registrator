package main

import (
	"strings"
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
)

func TestInspectServiceNoManagerAddress(t *testing.T) {
	docker, err := dockerapi.NewClient("unix:///tmp/registrator-missing-docker.sock")
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	resolver := newSwarmPortResolver(docker, swarmRuntime{Role: "worker"}, "", "", 2375)

	_, err = resolver.inspectService("service-id")
	if err == nil {
		t.Fatalf("expected inspectService error")
	}
	if !strings.Contains(err.Error(), "no manager node address discovered") {
		t.Fatalf("expected actionable manager discovery error, got: %v", err)
	}
}
