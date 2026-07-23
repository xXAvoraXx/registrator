# Run Reference

Registrator is typically run once per host. In Swarm, the natural deployment
model is a `global` service. That lets each node register its own local
containers/tasks and clean up its own stale registrations.

## Configuration Model

Configuration precedence:

1. Config file (`REGISTRATOR_CONFIG`, default `/etc/registrator/config.yaml`)
2. Environment variable overrides
3. Runtime workload metadata overrides (`service.discovery.*`, `service.name`, `SERVICE_*`)

The main execution path is config/env driven. A positional `consul://...`
registry URI is no longer the primary model.

## Standalone Docker

Typical example:

```bash
docker run -d \
  --name registrator \
  --net=host \
  --volume=/var/run/docker.sock:/var/run/docker.sock \
  -e REGISTRATOR_DISCOVERY_PROVIDER=consul \
  -e REGISTRATOR_DISCOVERY_MODE=local \
  -e REGISTRATOR_DISCOVERY_PORT=8500 \
  -e REGISTRATOR_RUNTIME_CLEANUP=true \
  -e REGISTRATOR_RUNTIME_RESYNC_INTERVAL=30 \
  -e REGISTRATOR_STATUS_ADDR=:8080 \
  ghcr.io/xxavoraxx/registrator:latest
```

Required Docker options:

Option | Required | Description
------ | -------- | -----------
`--volume=/var/run/docker.sock:/var/run/docker.sock` | yes | Required for Docker API access
`--net=host` | recommended | Easiest way to preserve host-level IP/hostname behavior and local backend reachability

## Swarm Global Deployment

Recommended production deployment:

```bash
docker service create \
  --name registrator \
  --mode global \
  --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
  --network host \
  --env REGISTRATOR_DISCOVERY_PROVIDER=consul \
  --env REGISTRATOR_DISCOVERY_MODE=service \
  --env REGISTRATOR_DISCOVERY_SERVICE_NAME=consul \
  --env REGISTRATOR_RUNTIME_CLEANUP=true \
  --env REGISTRATOR_RUNTIME_RESYNC_INTERVAL=30 \
  --env REGISTRATOR_STATUS_ADDR=:8080 \
  ghcr.io/xxavoraxx/registrator:latest
```

Swarm worker nodes first query the manager Docker API for service port metadata.
If that path is unavailable, they fall back to the manager registrator peer
endpoint.

Operational notes:

- Keep `REGISTRATOR_RUNTIME_CLEANUP=true`.
- Do not set `REGISTRATOR_RUNTIME_RESYNC_INTERVAL=0`; that disables drift healing.
- Worker nodes must be able to reach manager node addresses on `REGISTRATOR_RUNTIME_MANAGER_API_PORT`.
- If peer fallback is required, manager tasks must expose a reachable `REGISTRATOR_STATUS_ADDR` endpoint.

## Running with a Config File

Example `/etc/registrator/config.yaml`:

```yaml
discovery:
  provider: consul
  mode: local
  port: 8500
  serviceName: consul
docker:
  endpoint: unix:///var/run/docker.sock
runtime:
  cleanup: true
  resyncInterval: 30
  statusAddr: ":8080"
logging:
  level: info
```

Run:

```bash
docker run -d \
  --name registrator \
  --net=host \
  --volume=/var/run/docker.sock:/var/run/docker.sock \
  --volume=/etc/registrator/config.yaml:/etc/registrator/config.yaml:ro \
  -e REGISTRATOR_CONFIG=/etc/registrator/config.yaml \
  ghcr.io/xxavoraxx/registrator:latest
```

## Security Options

Status endpoints:

- `/healthz`
- `/readyz`
- `/metrics`
- `/peerinfo`
- `/swarm/service/{id}`

If `REGISTRATOR_STATUS_TOKEN` is set, only these endpoints require a token:

- `/metrics`
- `/peerinfo`
- `/swarm/service/*`

`/healthz` and `/readyz` remain open for orchestrator probes.

Additional hardening options:

Variable | Default | Effect
-------- | ------- | ------
`REGISTRATOR_RUNTIME_ALLOW_DISCOVERY_OVERRIDES` | `true` | Enables/disables `service.discovery.*` label overrides
`REGISTRATOR_RUNTIME_ALLOW_CHECK_SCRIPTS` | `true` | Enables/disables Consul `SERVICE_CHECK_SCRIPT` and `SERVICE_CHECK_CMD`
`REGISTRATOR_RUNTIME_ALLOW_TEMPLATE_HTTP_GET` | `true` | Enables/disables `httpGet` use inside `runtime.forceTags` templates

Registrator logs a warning when status endpoints are exposed on a non-loopback
address without a token.

## Common Environment Variables

Variable | Default | Description
-------- | ------- | -----------
`REGISTRATOR_DISCOVERY_PROVIDER` | `consul` | Backend provider
`REGISTRATOR_DISCOVERY_MODE` | `local` | Backend addressing mode
`REGISTRATOR_DISCOVERY_ADDRESS` | _(empty)_ | Backend address override
`REGISTRATOR_DISCOVERY_PORT` | `8500` | Backend port
`REGISTRATOR_DISCOVERY_SERVICE_NAME` | `consul` | Backend service name in `service` mode
`REGISTRATOR_DISCOVERY_REQUIRE_LOCAL_AGENT` | `false` | Require a healthy local Consul client on the current Swarm node
`REGISTRATOR_DOCKER_ENDPOINT` | `unix:///var/run/docker.sock` | Docker API endpoint
`REGISTRATOR_STATUS_ADDR` | _(empty)_ | Status endpoint listen address
`REGISTRATOR_STATUS_TOKEN` | _(empty)_ | Status endpoint token
`REGISTRATOR_RUNTIME_CLEANUP` | `true` | Stale registration cleanup
`REGISTRATOR_RUNTIME_RESYNC_INTERVAL` | `30` | Periodic reconcile interval
`REGISTRATOR_RUNTIME_MANAGER_API_PORT` | `2375` | Worker -> manager Docker API port
`CONSUL_HTTP_TOKEN` | _(empty)_ | Consul ACL token

## Consul ACL token

If Consul ACLs are enabled, provide the token through the environment:

```bash
docker run -d \
  --name registrator \
  --net=host \
  --volume=/var/run/docker.sock:/var/run/docker.sock \
  -e REGISTRATOR_DISCOVERY_PROVIDER=consul \
  -e REGISTRATOR_DISCOVERY_MODE=local \
  -e CONSUL_HTTP_TOKEN=<acl-token> \
  ghcr.io/xxavoraxx/registrator:latest
```

## Backend URI Note

Runtime configuration builds the registry URI from `provider`, `address`, and
`port`. That model is sufficient for Consul.

Legacy backends that require a path, prefix, or domain (`consulkv`, `etcd`,
`skydns2`, `zookeeper`) still depend on the URI semantics described in the
backend reference. Their config-first path is not as direct as Consul.
