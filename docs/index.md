# Registrator

Registrator registers and deregisters Docker container and Swarm task services
in service discovery backends, and reconciles them again when drift appears
over time.

This fork focuses on:

- Swarm-aware runtime behavior
- config/env driven operation
- periodic reconcile for self-healing
- deterministic stale cleanup after restart
- built-in health, readiness, metrics, and peer status endpoints

Consul is the strongest production backend for this fork. The current stale
cleanup and ownership-based reconcile logic is specifically hardened around the
Consul agent service catalog.

## Getting Started

- [Quickstart](user/quickstart.md)
- [Run Reference](user/run.md)
- [Service Object](user/services.md)
- [Backend Reference](user/backends.md)

Typical standalone Docker example:

```bash
docker run -d \
  --name registrator \
  --net=host \
  --volume=/var/run/docker.sock:/var/run/docker.sock \
  -e REGISTRATOR_DISCOVERY_PROVIDER=consul \
  -e REGISTRATOR_DISCOVERY_MODE=local \
  -e REGISTRATOR_DISCOVERY_PORT=8500 \
  -e REGISTRATOR_STATUS_ADDR=:8080 \
  ghcr.io/xxavoraxx/registrator:latest
```

Typical Swarm global deployment:

```bash
docker service create \
  --name registrator \
  --mode global \
  --mount type=bind,src=/var/run/docker.sock,dst=/var/run/docker.sock \
  --network host \
  --env REGISTRATOR_DISCOVERY_PROVIDER=consul \
  --env REGISTRATOR_DISCOVERY_MODE=service \
  --env REGISTRATOR_DISCOVERY_SERVICE_NAME=consul \
  --env REGISTRATOR_STATUS_ADDR=:8080 \
  ghcr.io/xxavoraxx/registrator:latest
```

For security hardening, set `REGISTRATOR_STATUS_TOKEN` to protect the
`/metrics`, `/peerinfo`, and `/swarm/service/*` endpoints. `/healthz` and
`/readyz` intentionally remain open for orchestrator probes.
