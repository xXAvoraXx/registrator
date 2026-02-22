package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	dockerapi "github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/pkg/usage"
	"github.com/gliderlabs/registrator/bridge"
	"github.com/sirupsen/logrus"
)

var Version string

var versionChecker = usage.NewChecker("registrator", Version)

var hostIp = flag.String("ip", "", "IP for ports mapped to the host")
var internal = flag.Bool("internal", false, "Use internal ports instead of published ones")
var explicit = flag.Bool("explicit", false, "Only register containers which have SERVICE_NAME label set")
var useIpFromLabel = flag.String("useIpFromLabel", "", "Use IP which is stored in a label assigned to the container")
var refreshInterval = flag.Int("ttl-refresh", 0, "Frequency with which service TTLs are refreshed")
var refreshTtl = flag.Int("ttl", 0, "TTL for services (default is no expiry)")
var forceTags = flag.String("tags", "", "Append tags for all registered services (supports Go template)")
var resyncInterval = flag.Int("resync", 0, "Frequency with which services are resynchronized")
var deregister = flag.String("deregister", "always", "Deregister exited services \"always\" or \"on-success\"")
var retryAttempts = flag.Int("retry-attempts", 0, "Max retry attempts to establish a connection with the backend. Use -1 for infinite retries")
var retryInterval = flag.Int("retry-interval", 2000, "Interval (in millisecond) between retry-attempts.")
var cleanup = flag.Bool("cleanup", false, "Remove dangling services")
var statusAddr = flag.String("status-addr", "", "Address to bind health/readiness/metrics endpoints (disabled when empty)")
var logLevel = flag.String("log-level", "info", "Log level (debug, info, warn, error)")
var managerOnly = flag.Bool("swarm-manager-only", true, "When in Swarm mode, only managers perform registrations")
var advertiseMode = flag.String("advertise-mode", "node-ip", "Address mode for Swarm services: node-ip|service-vip|custom")
var advertiseIPOverride = flag.String("advertise-ip-override", "", "Custom advertise IP override used by advertise mode")
var redisAddr = flag.String("redis-addr", "", "Optional redis address for distributed locking/state (host:port)")
var clusterID = flag.String("cluster-id", "", "Cluster namespace for distributed lock/state keys")
var managerAPIPort = flag.Int("manager-api-port", 2375, "Docker API port used when querying manager nodes from workers")

var eventsProcessed uint64
var reconcileRuns uint64

func getopt(name, def string) string {
	if env := os.Getenv(name); env != "" {
		return env
	}
	return def
}

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

	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s [options] <registry URI>\n\n", os.Args[0])
		flag.PrintDefaults()
	}

	flag.Parse()
	level, err := logrus.ParseLevel(*logLevel)
	if err != nil {
		assert(err)
	}
	logrus.SetLevel(level)

	if flag.NArg() != 1 {
		if flag.NArg() == 0 {
			fmt.Fprint(os.Stderr, "Missing required argument for registry URI.\n\n")
		} else {
			fmt.Fprintln(os.Stderr, "Extra unparsed arguments:")
			fmt.Fprintln(os.Stderr, " ", strings.Join(flag.Args()[1:], " "))
			fmt.Fprint(os.Stderr, "Options should come before the registry URI argument.\n\n")
		}
		flag.Usage()
		os.Exit(2)
	}

	if *hostIp != "" {
		log.Println("Forcing host IP to", *hostIp)
	}

	if (*refreshTtl == 0 && *refreshInterval > 0) || (*refreshTtl > 0 && *refreshInterval == 0) {
		assert(errors.New("-ttl and -ttl-refresh must be specified together or not at all"))
	} else if *refreshTtl > 0 && *refreshTtl <= *refreshInterval {
		assert(errors.New("-ttl must be greater than -ttl-refresh"))
	}

	if *retryInterval <= 0 {
		assert(errors.New("-retry-interval must be greater than 0"))
	}

	dockerHost := os.Getenv("DOCKER_HOST")
	if dockerHost == "" {
		if runtime.GOOS != "windows" {
			os.Setenv("DOCKER_HOST", "unix:///tmp/docker.sock")
		} else {
			os.Setenv("DOCKER_HOST", "npipe:////./pipe/docker_engine")
		}
	}

	docker, err := dockerapi.NewClientFromEnv()
	assert(err)

	if *deregister != "always" && *deregister != "on-success" {
		assert(errors.New("-deregister must be \"always\" or \"on-success\""))
	}

	swarmInfo := detectSwarmRuntime(docker)
	coordinator := newDistributedCoordinator(docker, swarmInfo, *managerOnly, *advertiseMode, *advertiseIPOverride, *redisAddr, *clusterID, *managerAPIPort)
	b, err := bridge.New(docker, flag.Arg(0), bridge.Config{
		HostIp:          *hostIp,
		Internal:        *internal,
		Explicit:        *explicit,
		UseIpFromLabel:  *useIpFromLabel,
		ForceTags:       *forceTags,
		RefreshTtl:      *refreshTtl,
		RefreshInterval: *refreshInterval,
		DeregisterCheck: *deregister,
		Cleanup:         *cleanup,
		Coordinator:     coordinator,
		ResolveSwarm:    coordinator.ResolveSwarmPorts,
	})
	assert(err)

	logrus.WithFields(logrus.Fields{
		"enabled":            swarmInfo.Enabled,
		"node_id":            swarmInfo.NodeID,
		"node_role":          swarmInfo.Role,
		"node_address":       swarmInfo.NodeAddr,
		"running_as_service": swarmInfo.RunningAsService,
		"swarm_service_id":   swarmInfo.SwarmServiceID,
		"cluster_id":         *clusterID,
		"redis_enabled":      *redisAddr != "",
	}).Info("runtime swarm status")

	passiveNode := swarmInfo.Enabled && swarmInfo.Role == "worker" && *managerOnly
	if passiveNode {
		logrus.Info("swarm-manager-only enabled: running in passive worker mode")
	}

	if *statusAddr != "" {
		go serveStatus(*statusAddr, b, &eventsProcessed, &reconcileRuns)
	}

	attempt := 0
	for *retryAttempts == -1 || attempt <= *retryAttempts {
		log.Printf("Connecting to backend (%v/%v)", attempt, *retryAttempts)

		err = b.Ping()
		if err == nil {
			break
		}

		if err != nil && attempt == *retryAttempts {
			assert(err)
		}

		time.Sleep(time.Duration(*retryInterval) * time.Millisecond)
		attempt++
	}

	// Start event listener before listing containers to avoid missing anything
	events := make(chan *dockerapi.APIEvents)
	assert(docker.AddEventListener(events))
	log.Println("Listening for Docker events ...")

	if !passiveNode {
		b.Sync(false)
		atomic.AddUint64(&reconcileRuns, 1)
	}

	quit := make(chan struct{})

	// Start the TTL refresh timer
	if *refreshInterval > 0 {
		ticker := time.NewTicker(time.Duration(*refreshInterval) * time.Second)
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
	if *resyncInterval > 0 {
		resyncTicker := time.NewTicker(time.Duration(*resyncInterval) * time.Second)
		go func() {
			for {
				select {
				case <-resyncTicker.C:
					if !passiveNode {
						b.Sync(true)
						atomic.AddUint64(&reconcileRuns, 1)
					}
				case <-quit:
					resyncTicker.Stop()
					return
				}
			}
		}()
	}

	// Process Docker events
	for msg := range events {
		atomic.AddUint64(&eventsProcessed, 1)
		switch msg.Status {
		case "start":
			if !passiveNode {
				go b.Add(msg.ID)
			}
		case "die":
			if !passiveNode {
				go b.RemoveOnExit(msg.ID)
			}
		case "stop", "pause", "destroy":
			if !passiveNode {
				go b.Remove(msg.ID)
			}
		case "unpause", "health_status: healthy", "health_status:healthy":
			if !passiveNode {
				go b.Add(msg.ID)
			}
		case "health_status: unhealthy", "health_status:unhealthy":
			if !passiveNode {
				go b.RemoveOnExit(msg.ID)
			}
		}
	}

	close(quit)
	log.Fatal("Docker event loop closed") // todo: reconnect?
}
