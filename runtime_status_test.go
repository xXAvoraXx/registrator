package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dockerapi "github.com/fsouza/go-dockerclient"
)

func TestDetectSwarmRuntimeReadsSwarmTaskLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/info":
			_, _ = w.Write([]byte(`{"Swarm":{"LocalNodeState":"active","NodeID":"info-node","NodeAddr":"10.0.0.2","ControlAvailable":true}}`))
		case strings.HasPrefix(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = w.Write([]byte(`{"Config":{"Labels":{"com.docker.swarm.service.id":"svc-1","com.docker.swarm.service.name":"registrator","com.docker.swarm.task.id":"task-1","com.docker.swarm.node.id":"label-node"}},"NetworkSettings":{"Networks":{"ingress":{"IPAddress":"10.0.1.172"}}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	docker, err := dockerapi.NewClient(server.URL)
	if err != nil {
		t.Fatalf("failed to create docker client: %v", err)
	}
	runtime := detectSwarmRuntime(docker)
	if !runtime.Enabled || !runtime.RunningAsService {
		t.Fatalf("expected swarm service runtime, got %+v", runtime)
	}
	if runtime.SwarmServiceID != "svc-1" || runtime.SwarmServiceName != "registrator" || runtime.SwarmTaskID != "task-1" {
		t.Fatalf("unexpected swarm labels: %+v", runtime)
	}
	if runtime.NodeID != "info-node" {
		t.Fatalf("expected node id from swarm info, got %q", runtime.NodeID)
	}
	if runtime.OverlayIP != "10.0.1.172" {
		t.Fatalf("expected overlay ip from network settings, got %q", runtime.OverlayIP)
	}
	if runtime.Role != "manager" {
		t.Fatalf("expected manager role, got %q", runtime.Role)
	}
}

func TestFetchPeerInfoParsesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(peerInfo{
			ServiceID:   "svc-1",
			ServiceName: "registrator",
			TaskID:      "task-1",
			NodeID:      "node-1",
			Hostname:    "host-1",
			OverlayIP:   "10.0.1.172",
			Role:        "worker",
		})
	}))
	defer server.Close()

	info, err := fetchPeerInfo(server.Client(), server.URL+"/peerinfo")
	if err != nil {
		t.Fatalf("fetchPeerInfo returned error: %v", err)
	}
	if info.ServiceName != "registrator" || info.TaskID != "task-1" || info.Role != "worker" {
		t.Fatalf("unexpected peer info payload: %+v", info)
	}
}
