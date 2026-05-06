# Quickstart

This document gets Registrator running quickly with Consul. For production
behavior, use [Run Reference](run.md), and for backend details use
[Backend Reference](backends.md).

## Goal

This walkthrough shows:

1. Consul is reachable
2. Registrator is listening for Docker events
3. A new container is registered
4. The registration is removed when the container is removed

## Before Starting

You need:

- Docker Engine
- access to `/var/run/docker.sock`
- a Consul agent running on `127.0.0.1:8500` or another reachable address

The examples here are for standalone Docker. For Swarm, use the global
deployment example in [Run Reference](run.md).

## Running Registrator

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
  ghcr.io/xxavoraxx/registrator:latest
```

Check the logs:

```bash
docker logs registrator
```

Expected behavior:

- the Docker event listener is running
- the backend connection is established
- the initial `Sync()` has completed

## Test with Redis

```bash
docker run -d -P --name redis redis
```

Consul service list:

```bash
curl http://127.0.0.1:8500/v1/catalog/services
```

`redis` should appear. For the detailed registration:

```bash
curl http://127.0.0.1:8500/v1/catalog/service/redis
```

The service registration typically includes:

- `ServiceID`
- `ServiceName`
- `ServicePort`
- `ServiceTags`
- Consul `ServiceMeta`

Registrator adds the `registrator` tag and internal ownership metadata to the
Consul services it manages.

## Test Removal

```bash
docker rm -f redis
curl http://127.0.0.1:8500/v1/catalog/service/redis
```

The registration should disappear.

## Restart and Stale Cleanup Note

In this fork, the reconcile flow also reads backend state and removes stale
local registrations. After a registrator or host restart, old Consul service
entries for the same node should not accumulate indefinitely.

For that behavior to work, keep:

- `REGISTRATOR_RUNTIME_CLEANUP=true`
- `REGISTRATOR_RUNTIME_RESYNC_INTERVAL` non-zero

enabled.

## Next Steps

- [Run Reference](run.md)
- [Service Object](services.md)
- [Backend Reference](backends.md)
