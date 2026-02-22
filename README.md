# Registrator (Swarm-Aware Fork)

Registrator automatically registers and deregisters Docker workloads in service discovery backends (for example, Consul), while continuously reconciling runtime state.

This fork modernizes the original project with:

- Go modules and current Go toolchain support
- Swarm runtime awareness (manager/worker detection)
- idempotent registration behavior to reduce duplicate writes
- broader Docker event handling and periodic reconciliation
- built-in health, readiness, and metrics endpoints
- configuration-driven runtime (config file + env + runtime labels)

## Why this fork

The original `gliderlabs/registrator` is stable but largely unmaintained for modern Swarm-centric production deployments. This fork focuses on reliability and deterministic behavior in clustered environments while preserving the core bridge model and adapter compatibility.

## Key differences from original project

- **Swarm-aware startup introspection**:
  - detects Swarm state
  - logs node ID, node address, node role
  - detects whether registrator runs as a Swarm service task
- **Deterministic/idempotent registration path**:
  - hashes service metadata before writing
  - skips writes when backend state is already equivalent
  - retries register/deregister operations with exponential backoff
- **Node-local ownership enforcement**:
  - services are only processed for containers whose `container.Node.ID` matches the local Swarm node ID
- **Config-first architecture**:
  - no required runtime CLI flags
  - base config is loaded from file
  - environment variables override file values
  - runtime container/service labels override both for that specific workload
- **Expanded event coverage**:
  - `start`, `die`, `stop`, `pause`, `unpause`, `destroy`
  - health transitions (`health_status: healthy`, `health_status: unhealthy`)
- **Operational endpoints**:
  - `/healthz`
  - `/readyz`
  - `/metrics` (Prometheus text format)

## Architecture overview

Current implementation is intentionally incremental and keeps existing adapter contracts:

- `registrator.go` – process bootstrap, docker event loop, retry setup, timers
- `bridge/` – container inspection, service model derivation, idempotent register/remove flow
- backend adapters – Consul, Consul KV, etcd, SkyDNS2, ZooKeeper

Core runtime loops:

1. connect to Docker and backend
2. detect runtime/Swarm node metadata
3. subscribe to Docker events
4. perform initial sync
5. process events + optional TTL refresh + optional periodic resync

## Swarm awareness behavior

At startup, registrator logs:

- whether Swarm is enabled
- node ID
- node role (`manager` or `worker`)
- node address
- whether process is running as a Swarm service task

Ownership is node-local: if an inspected container belongs to a different Swarm node, it is skipped and never registered by this instance.

## Configuration model

Configuration priority:

1. Config file (`REGISTRATOR_CONFIG`, default `/etc/registrator/config.yaml`)
2. Environment variable overrides
3. Runtime container/service label overrides (`service.discovery.*`, `service.name`)

### Config file usage example

Full example `/etc/registrator/config.yaml` (all available config keys):

```yaml
discovery:
  provider: consul                # default: consul
  mode: local                     # default: local
  address: ""                     # default: empty (auto-resolved from mode)
  port: 8500                      # default: 8500
  serviceName: consul             # default: consul
  useDockerResolve: true          # default: true
service:
  nameSource: service.name        # default: service.name
  labelKey: service.name          # default: service.name
  idFormat: "{hostname}:{name}:{port}" # default: {hostname}:{name}:{port}
docker:
  endpoint: unix:///tmp/docker.sock # default: unix:///tmp/docker.sock
  swarmMode: true                 # default: true
runtime:
  hostIP: ""                      # default: empty
  internal: false                 # default: false
  explicit: false                 # default: false
  useIPFromLabel: ""              # default: empty
  forceTags: ""                   # default: empty
  refreshTTL: 0                   # default: 0
  refreshInterval: 0              # default: 0
  deregisterCheck: always         # default: always
  cleanup: true                   # default: true
  retryAttempts: 10               # default: 10 (-1 = infinite)
  retryIntervalMs: 2000           # default: 2000
  resyncInterval: 30              # default: 30
  statusAddr: ":8080"             # optional (health/readiness/metrics listener)
  advertiseMode: node-ip          # default: node-ip
  advertiseIPOverride: ""         # default: empty
  managerAPIPort: 2375            # default: 2375
logging:
  level: info                     # default: info
```

Run with local binary:

```bash
REGISTRATOR_CONFIG=/etc/registrator/config.yaml ./registrator
```

Run with Docker (bind-mount config file):

```bash
docker run -d \
  --name registrator \
  -v /var/run/docker.sock:/tmp/docker.sock \
  -v /etc/registrator/config.yaml:/etc/registrator/config.yaml:ro \
  -e REGISTRATOR_CONFIG=/etc/registrator/config.yaml \
  ghcr.io/xxavoraxx/registrator:latest
```

Supported environment variables:

| Environment Variable | Default | Description |
|---|---|---|
| `REGISTRATOR_CONFIG` | `/etc/registrator/config.yaml` | Path to configuration file. If the file exists, env values override file values. |
| `REGISTRATOR_DISCOVERY_PROVIDER` | `consul` | Discovery backend provider (for example, `consul`). |
| `REGISTRATOR_DISCOVERY_MODE` | `local` | Discovery address resolution mode (`local`, `service`, etc.). |
| `REGISTRATOR_DISCOVERY_ADDRESS` | _(empty)_ | Direct address override for discovery backend. |
| `REGISTRATOR_DISCOVERY_PORT` | `8500` | Discovery backend port. |
| `REGISTRATOR_DISCOVERY_SERVICE_NAME` | `consul` | Discovery service name used in `service` mode. |
| `REGISTRATOR_DISCOVERY_USE_DOCKER_RESOLVE` | `true` | Enables/disables Docker-based address resolution in `local` mode. |
| `REGISTRATOR_SERVICE_NAME_SOURCE` | `service.name` | Service name source (label/metadata key). |
| `REGISTRATOR_SERVICE_LABEL_KEY` | `service.name` | Default label key used to read service name. |
| `REGISTRATOR_SERVICE_ID_FORMAT` | `{hostname}:{name}:{port}` | Format string for generated service IDs. |
| `REGISTRATOR_DOCKER_ENDPOINT` | `unix:///tmp/docker.sock` | Docker API endpoint. |
| `REGISTRATOR_DOCKER_SWARM_MODE` | `true` | Enables/disables Swarm-aware behavior. |
| `REGISTRATOR_STATUS_ADDR` | `:8080` | Listen address for health/readiness/metrics endpoints. |
| `REGISTRATOR_RUNTIME_HOST_IP` | _(empty)_ | Runtime host IP override. |
| `REGISTRATOR_RUNTIME_INTERNAL` | `false` | Controls whether internal ports are processed. |
| `REGISTRATOR_RUNTIME_EXPLICIT` | `false` | Controls processing of only explicitly declared services. |
| `REGISTRATOR_RUNTIME_FORCE_TAGS` | _(empty)_ | Comma-separated tags appended to all service registrations. |
| `REGISTRATOR_RUNTIME_REFRESH_TTL` | `0` | TTL refresh period in seconds (`0` disables). |
| `REGISTRATOR_RUNTIME_REFRESH_INTERVAL` | `0` | Refresh loop interval in seconds (`0` disables). |
| `REGISTRATOR_RUNTIME_DEREGISTER_CHECK` | `always` | Policy for deregister/check handling. |
| `REGISTRATOR_RUNTIME_CLEANUP` | `true` | Enables/disables cleanup behavior during runtime flows. |
| `REGISTRATOR_RUNTIME_RETRY_ATTEMPTS` | `10` | Retry attempts for register/deregister (`-1` means infinite). |
| `REGISTRATOR_RUNTIME_RETRY_INTERVAL_MS` | `2000` | Delay between retries in milliseconds. |
| `REGISTRATOR_RUNTIME_RESYNC_INTERVAL` | `30` | Periodic resync interval in seconds. |
| `CONSUL_HTTP_TOKEN` | _(empty)_ | Consul ACL token (consumed by Consul client from env). |
| `CONSUL_CACERT` | _(empty)_ | CA certificate file path used in `consul-tls` mode. |
| `CONSUL_CLIENT_CERT` | _(empty)_ | Client certificate file path used in `consul-tls` mode. |
| `CONSUL_CLIENT_KEY` | _(empty)_ | Client private key file path used in `consul-tls` mode. |

## Installation / Kurulum

### Prerequisites

- Docker Engine (Swarm optional)
- Access to Docker socket from registrator container (`/var/run/docker.sock` -> `/tmp/docker.sock`)
- A discovery backend (default examples use Consul)

### Option 1: Build and run binary locally

```bash
git clone https://github.com/xXAvoraXx/registrator.git
cd registrator
go build -o registrator .

# optional config file
mkdir -p /etc/registrator
cat >/etc/registrator/config.yaml <<'YAML'
discovery:
  provider: consul
  mode: local
  port: 8500
  serviceName: consul
docker:
  endpoint: unix:///tmp/docker.sock
logging:
  level: info
YAML

REGISTRATOR_CONFIG=/etc/registrator/config.yaml ./registrator
```

### Option 2: Run with Docker image

```bash
docker run -d \
  --name registrator \
  -v /var/run/docker.sock:/tmp/docker.sock \
  -e REGISTRATOR_DISCOVERY_PROVIDER=consul \
  -e REGISTRATOR_DISCOVERY_MODE=local \
  -e REGISTRATOR_DISCOVERY_PORT=8500 \
  -e REGISTRATOR_STATUS_ADDR=:8080 \
  ghcr.io/xxavoraxx/registrator:latest
```

### Option 3: Swarm global deployment

Use the **Docker Swarm (global mode)** example below; it is the recommended production install pattern.

## Deployment examples

### Standalone Docker

```bash
docker run -d \
  --name registrator \
  --net=host \
  -v /var/run/docker.sock:/tmp/docker.sock \
  -e REGISTRATOR_STATUS_ADDR=:8080 \
  -e REGISTRATOR_DISCOVERY_MODE=local \
  ghcr.io/xxavoraxx/registrator:latest
```

### Docker Swarm (global mode)

```bash
docker service create \
  --name registrator \
  --mode global \
  --mount type=bind,src=/var/run/docker.sock,dst=/tmp/docker.sock \
  --network host \
  --env REGISTRATOR_STATUS_ADDR=:8080 \
  --env REGISTRATOR_DISCOVERY_MODE=service \
  --env REGISTRATOR_DISCOVERY_SERVICE_NAME=consul \
  ghcr.io/xxavoraxx/registrator:latest
```

## Consul integration

Discovery provider and addressing are configured through config/env/labels (not required CLI URI flags).  
In `Discovery.Mode=local`, Registrator resolves the local Consul agent via Docker API for fresh registration attempts (no IP cache).

## Swarm manager metadata resolution

For Swarm service containers, Registrator resolves service ports from Swarm service endpoint metadata.  
On worker nodes, it can query manager nodes in sorted order (with backoff retries) for authoritative service `EndpointSpec.Ports`, reducing dependence on local worker-only container networking details.

## Failure handling model

- backend connection retries before startup completion (`-retry-attempts`, `-retry-interval`)
- register/deregister operations retried with exponential backoff
- optional periodic resync (`-resync`) to self-heal drift
- readiness endpoint validates backend liveness through adapter `Ping()`
- startup reconciliation seeds authoritative backend fingerprints before processing events, preventing duplicate writes after simultaneous restarts

## Upgrade notes from original registrator

- Runtime configuration is now config-driven (file + env + runtime labels)
- CLI flag dependency has been removed from the main execution path

## Configuration reference

See the **Configuration model** section for currently supported `REGISTRATOR_*` environment variables and precedence rules.

## Production best practices

1. Run in Swarm global mode and mount `/var/run/docker.sock` to `/tmp/docker.sock`.
2. Enable periodic reconciliation (`-resync`) to heal eventual drift.
3. Scrape `/metrics` and alert on event/reconcile anomalies.
4. Gate traffic rollouts on `/readyz`.
5. Keep backend ACL/TLS hardening enabled (for Consul and other adapters where supported).

## Development

```bash
go test ./...
```

## License

MIT
