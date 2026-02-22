# Registrator (Swarm-Aware Fork)

Registrator automatically registers and deregisters Docker workloads in service discovery backends (for example, Consul), while continuously reconciling runtime state.

This fork modernizes the original project with:

- Go modules and current Go toolchain support
- Swarm runtime awareness (manager/worker detection)
- idempotent registration behavior to reduce duplicate writes
- broader Docker event handling and periodic reconciliation
- built-in health, readiness, and metrics endpoints

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
- **Deterministic distributed ownership**:
  - Swarm workers are passive when manager-only mode is enabled
  - manager ownership is selected deterministically per service with `hash(serviceID) % managers`
  - fallback ownership without Redis uses discovered registrator Swarm task nodes
- **Optional Redis distributed coordination**:
  - lock key: `registrator:{cluster_id}:lock:{service_id}`
  - state key: `registrator:{cluster_id}:state:{service_id}`
  - `SET NX EX` lock semantics with auto-expiry, plus fallback to in-memory coordination if Redis is unavailable
- **Expanded event coverage**:
  - `start`, `die`, `stop`, `pause`, `unpause`, `destroy`
  - health transitions (`health_status: healthy`, `health_status: unhealthy`)
- **Optional manager-only mode in Swarm**:
  - workers can run passive to avoid duplicate writes
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

When `-swarm-manager-only=true` (default), worker nodes stay passive in Swarm mode and do not mutate registry state.  
If multiple managers are present, each Swarm service maps deterministically to exactly one manager, preventing multi-manager duplicate registration.

## Deployment examples

### Standalone Docker

```bash
docker run -d \
  --name registrator \
  --net=host \
  -v /var/run/docker.sock:/tmp/docker.sock \
  ghcr.io/xxavoraxx/registrator:latest \
  -status-addr :8080 \
  consul://127.0.0.1:8500
```

### Docker Swarm (global mode)

```bash
docker service create \
  --name registrator \
  --mode global \
  --mount type=bind,src=/var/run/docker.sock,dst=/tmp/docker.sock \
  --network host \
  ghcr.io/xxavoraxx/registrator:latest \
  -swarm-manager-only=true \
  -resync=30 \
  -status-addr :8080 \
  consul://consul.service.consul:8500
```

## Consul integration

Use the backend URI as final positional argument:

```bash
registrator consul://consul.service.consul:8500
```

All existing service metadata conventions (`SERVICE_*`) remain supported.

## Redis coordination

When `-redis-addr` is configured, Registrator enables distributed lock/state coordination:

- Lock key: `registrator:{cluster_id}:lock:{service_id}`
- State key: `registrator:{cluster_id}:state:{service_id}`
- Lock acquisition uses Redis `SET key value NX EX ttl`
- Locks auto-expire to avoid deadlocks after crashes
- Service fingerprints are stored to avoid duplicate writes across process restarts

If Redis is unavailable, Registrator gracefully falls back to local in-memory coordination and continues processing (with warning logs).

## Inter-instance discovery (without Redis)

When Redis is not configured, Registrator discovers peer instances by listing Swarm tasks for the current Registrator service (`com.docker.swarm.service.id`) and building a sorted active node list. That list is used as deterministic ownership input when manager discovery is not available.

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

- Keep existing backend URI argument model and adapter semantics
- New flags were added (`-status-addr`, `-log-level`, `-swarm-manager-only`, `-redis-addr`, `-cluster-id`, `-advertise-mode`, `-advertise-ip-override`, `-manager-api-port`)
- In Swarm, manager-only mode is now default for safer operation

## Configuration reference

### Core flags

- `-ip` host IP override for published ports
- `-internal` register internal container networking instead of host published ports
- `-explicit` only register workloads with explicit service naming metadata
- `-useIpFromLabel` load address from a container label
- `-tags` append tags to all services (template-enabled)
- `-cleanup` remove dangling services discovered during cleanup pass
- `-deregister` `always|on-success`
- `-advertise-mode` `node-ip|service-vip|custom`
- `-advertise-ip-override` explicit address used by advertise mode

### Reliability and sync

- `-resync` periodic full synchronization interval (seconds)
- `-retry-attempts` backend connection retry attempts (`-1` = infinite)
- `-retry-interval` delay between backend connection retries (milliseconds)
- `-ttl` service TTL value
- `-ttl-refresh` TTL refresh interval

### Runtime/observability

- `-swarm-manager-only` run workers in passive mode when Swarm is active
- `-status-addr` bind address for `/healthz`, `/readyz`, `/metrics`
- `-log-level` logging verbosity (`debug|info|warn|error`)
- `-redis-addr` Redis endpoint (`host:port`) for distributed locking and state
- `-cluster-id` namespace used in distributed coordination keys
- `-manager-api-port` manager Docker API port for worker-side manager metadata resolution

### Environment variables

- `DOCKER_HOST` Docker API endpoint (defaults to `/tmp/docker.sock` on Unix)

## Production best practices

1. Run in Swarm global mode with `-swarm-manager-only=true`.
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
