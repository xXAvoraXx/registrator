package main

import (
	"errors"
	"log"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/pkg/usage"
	"github.com/gliderlabs/registrator/bridge"
	"github.com/gliderlabs/registrator/consul"
	"github.com/sirupsen/logrus"
)

var Version string

var versionChecker = usage.NewChecker("registrator", Version)

var metrics runtimeMetrics

const dockerOperationTimeout = 10 * time.Second

func assert(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		versionChecker.PrintVersion()
		os.Exit(0)
	}
	log.Printf("Starting registrator %s ...", Version)
	cfg, err := loadAppConfig()
	assert(err)
	assert(applyCLIOverrides(&cfg, os.Args[1:]))
	level, err := logrus.ParseLevel(cfg.Logging.Level)
	if err != nil {
		assert(err)
	}
	logrus.SetLevel(level)
	if cfg.Docker.Endpoint == "" {
		assert(errors.New("docker endpoint must be configured"))
	}
	docker, err := dockerapi.NewClient(cfg.Docker.Endpoint)
	assert(err)
	docker.SetTimeout(dockerOperationTimeout)
	eventDocker, err := dockerapi.NewClient(cfg.Docker.Endpoint)
	assert(err)

	swarmInfo := detectSwarmRuntime(docker)
	resolver := newSwarmPortResolver(docker, swarmInfo, cfg.Runtime.AdvertiseMode, cfg.Runtime.AdvertiseIPOverride, cfg.Runtime.ManagerAPIPort, statusPort(cfg.Runtime.StatusAddr), cfg.Runtime.StatusToken)
	if cfg.Discovery.Provider == "consul" {
		consul.ConfigureRuntime(docker, consul.RuntimeConfig{
			Mode:              cfg.Discovery.Mode,
			Address:           cfg.Discovery.Address,
			Port:              cfg.Discovery.Port,
			ServiceName:       cfg.Discovery.ServiceName,
			UseDockerResolve:  cfg.Discovery.UseDockerResolve,
			RequireLocalAgent: cfg.Discovery.RequireLocalAgent,
			LocalNodeAddress:  swarmInfo.NodeAddr,
		})
	}
	b, err := bridge.New(docker, buildRegistryURI(cfg), bridge.Config{
		HostIp:          cfg.Runtime.HostIP,
		Internal:        cfg.Runtime.Internal,
		Explicit:        cfg.Runtime.Explicit,
		UseIpFromLabel:  cfg.Runtime.UseIPFromLabel,
		ForceTags:       cfg.Runtime.ForceTags,
		RefreshTtl:      cfg.Runtime.RefreshTTL,
		RefreshInterval: cfg.Runtime.RefreshInterval,
		DeregisterCheck: cfg.Runtime.DeregisterCheck,
		Cleanup:         cfg.Runtime.Cleanup,
		LocalNodeID:     swarmInfo.NodeID,
		ResolveSwarm:    resolver.ResolveSwarmPorts,
		InspectServiceLabels: func(serviceID string) (map[string]string, error) {
			service, err := inspectSwarmService(docker, serviceID)
			if err != nil {
				return nil, err
			}
			return service.Spec.Labels, nil
		},
		NameSource:              cfg.Service.NameSource,
		NameLabelKey:            cfg.Service.LabelKey,
		IDFormat:                cfg.Service.IDFormat,
		AllowDiscoveryOverrides: cfg.Runtime.AllowDiscoveryOverrides,
		AllowCheckScripts:       cfg.Runtime.AllowCheckScripts,
		AllowTemplateHTTPGet:    cfg.Runtime.AllowTemplateHTTPGet,
	})
	assert(err)

	logrus.WithFields(logrus.Fields{
		"enabled":            swarmInfo.Enabled,
		"node_id":            swarmInfo.NodeID,
		"node_role":          swarmInfo.Role,
		"node_address":       swarmInfo.NodeAddr,
		"running_as_service": swarmInfo.RunningAsService,
		"swarm_service_id":   swarmInfo.SwarmServiceID,
	}).Info("runtime swarm status")

	if cfg.Runtime.StatusAddr != "" {
		go serveStatus(cfg.Runtime.StatusAddr, b, swarmInfo, docker, &metrics, reconcileStaleAfter(cfg.Runtime.ResyncInterval), cfg.Runtime.StatusToken)
	}

	attempt := 0
	retryAttempts := cfg.Runtime.RetryAttempts
	retryInterval := cfg.Runtime.RetryIntervalMs
	retryTotal := "infinite"
	if retryAttempts >= 0 {
		retryTotal = strconv.Itoa(retryAttempts + 1)
	}
	for retryAttempts == -1 || attempt <= retryAttempts {
		log.Printf("Connecting to backend (%v/%v)", attempt+1, retryTotal)

		err = b.Ping()
		if err == nil {
			metrics.setBackendReady(true)
			log.Printf("Connected to backend (%v/%v)", attempt+1, retryTotal)
			break
		}
		metrics.setBackendReady(false)
		log.Printf("Backend ping failed (%v/%v): %v", attempt+1, retryTotal, err)

		if err != nil && attempt == retryAttempts {
			assert(err)
		}

		time.Sleep(time.Duration(retryInterval) * time.Millisecond)
		attempt++
	}

	// Start event listener before listing containers to avoid missing anything
	events := make(chan *dockerapi.APIEvents, 256)
	assert(eventDocker.AddEventListener(events))
	log.Println("Listening for Docker events ...")

	metrics.reconcile(b, false)

	quit := make(chan struct{})

	// Start the TTL refresh timer
	refreshInterval := cfg.Runtime.RefreshInterval
	if refreshInterval > 0 {
		ticker := time.NewTicker(time.Duration(refreshInterval) * time.Second)
		go func() {
			for {
				select {
				case <-ticker.C:
					b.Refresh()
				case <-quit:
					ticker.Stop()
					return
				}
			}
		}()
	}

	// Start the resync timer if enabled
	resyncInterval := cfg.Runtime.ResyncInterval
	if resyncInterval > 0 {
		resyncTicker := time.NewTicker(time.Duration(resyncInterval) * time.Second)
		go func() {
			for {
				select {
				case <-resyncTicker.C:
					metrics.reconcile(b, true)
				case <-quit:
					resyncTicker.Stop()
					return
				}
			}
		}()
	}

	// Process Docker events
	for msg := range events {
		atomic.AddUint64(&metrics.eventsProcessed, 1)
		switch msg.Status {
		case "start":
			b.Add(msg.ID)
		case "die":
			b.RemoveOnExit(msg.ID)
		case "stop", "pause", "destroy":
			b.Remove(msg.ID)
		case "unpause", "health_status: healthy", "health_status:healthy":
			b.Add(msg.ID)
		case "health_status: unhealthy", "health_status:unhealthy":
			b.RemoveOnExit(msg.ID)
		}
	}

	close(quit)
	log.Fatal("Docker event loop closed") // todo: reconnect?
}

func statusPort(addr string) string {
	if addr == "" {
		return ""
	}
	_, port, err := net.SplitHostPort(addr)
	if err == nil {
		return port
	}
	return ""
}
