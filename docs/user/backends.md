# Backend Reference

Registrator supports multiple backends, but current reconciliation and stale
cleanup behavior is strongest with Consul. This fork treats Docker as the source
of truth and uses backend reads during startup and resync; the Consul agent
catalog gives the cleanest implementation for that model.

See also [Contributing Backends](../dev/backends.md).

## Consul

	consul://<address>:<port>
	consul-unix://<filepath>
	consul-tls://<address>:<port>

Consul is the recommended backend for production.

If no address and port is specified, it will default to `127.0.0.1:8500`.

Consul support in this fork includes:

- service tags
- service metadata (`Meta`) mapped from `Service.Attrs`
- registrator ownership metadata for safer stale cleanup after restart
- health check attributes

When using the `consul-tls` scheme, registrator communicates with Consul
through TLS. You must set the following environment variables:

- `CONSUL_CACERT`: CA file location
- `CONSUL_CLIENT_CERT`: certificate file location
- `CONSUL_CLIENT_KEY`: key file location

For more information on the Consul check parameters below, see the
[API documentation](https://www.consul.io/api/agent/check.html#register-check).

### Consul HTTP Check

This feature is available when using a Consul version that supports HTTP checks.
Containers specifying these extra metadata fields in labels or environment will
be used to register an HTTP health check with the service.

```bash
SERVICE_80_CHECK_HTTP=/health/endpoint/path
SERVICE_80_CHECK_HTTP_PORT=8080
SERVICE_80_CHECK_INTERVAL=15s
SERVICE_80_CHECK_TIMEOUT=1s
SERVICE_80_CHECK_HTTP_METHOD=HEAD
```

It works for services on any port, not just 80. If it is the only service, you
can also use `SERVICE_CHECK_HTTP`. For multi-port containers,
`SERVICE_CHECK_HTTP` defaults to the lowest exposed TCP port; override
explicitly with `SERVICE_CHECK_HTTP_PORT`.

### Consul HTTPS Check

```bash
SERVICE_443_CHECK_HTTPS=/health/endpoint/path
SERVICE_443_CHECK_INTERVAL=15s
SERVICE_443_CHECK_TIMEOUT=1s
SERVICE_443_CHECK_HTTPS_METHOD=HEAD
```

### Consul TCP Check

```bash
SERVICE_443_CHECK_TCP=true
SERVICE_443_CHECK_INTERVAL=15s
SERVICE_443_CHECK_TIMEOUT=3s
```

### Consul Script Check

Script checks are powerful but high risk. If running Consul in a container,
you are limited to what is available inside that container.

```bash
SERVICE_CHECK_SCRIPT=curl --silent --fail example.com
```

The default interval for any non-TTL check is `10s`, but you can set it with
`_CHECK_INTERVAL`. The check command will be interpolated with the
`$SERVICE_IP` and `$SERVICE_PORT` placeholders:

```bash
SERVICE_CHECK_SCRIPT=nc $SERVICE_IP $SERVICE_PORT | grep OK
```

If `REGISTRATOR_RUNTIME_ALLOW_CHECK_SCRIPTS=false`, both
`SERVICE_CHECK_SCRIPT` and `SERVICE_CHECK_CMD` metadata are ignored.

### Consul TTL Check

```bash
SERVICE_CHECK_TTL=30s
```

This causes Consul to expect regular heartbeats to keep the service healthy.

### Consul gRPC Check

```bash
SERVICE_CHECK_GRPC=true
SERVICE_CHECK_INTERVAL=5s
SERVICE_CHECK_TIMEOUT=3s
SERVICE_CHECK_GRPC_USE_TLS=true
SERVICE_CHECK_TLS_SKIP_VERIFY=true
```

### Consul Initial Health Check Status

```bash
SERVICE_CHECK_INITIAL_STATUS=passing
```

### Consul Critical Service Deregistration

```bash
SERVICE_CHECK_DEREGISTER_AFTER=10m
```

### Consul ownership and cleanup

Registrator-managed Consul services always include the `registrator` tag.
Services registered by this fork also include internal ownership metadata used
to identify which local registrations can be safely removed during reconcile.

That matters most for:

- node restarts
- registrator restarts
- stale legacy IDs left behind after hostname/runtime drift
- custom `SERVICE_ID` values on registrator-managed services

## Consul KV

	consulkv://<address>:<port>/<prefix>
	consulkv-unix://<filepath>:/<prefix>

This backend uses the Consul key-value store instead of the native service
catalog. It behaves more like etcd and currently does not support TTLs.

If no address and port is specified, it will default to `127.0.0.1:8500`.

Using the prefix from the Registry URI, service definitions are stored as:

	<prefix>/<service-name>/<service-id> = <ip>:<port>

## Etcd

	etcd://<address>:<port>/<prefix>

Etcd works similarly to Consul KV, except it supports service TTLs. It does not
use the richer Consul metadata and ownership model.

If no address and port is specified, it will default to `127.0.0.1:2379`.

Using the prefix from the Registry URI, service definitions are stored as:

	<prefix>/<service-name>/<service-id> = <ip>:<port>

## SkyDNS 2

	skydns2://<address>:<port>/<domain>

SkyDNS 2 uses etcd, so this backend writes service definitions in a format
compatible with SkyDNS 2. The path may not be omitted and must be a valid DNS
domain for SkyDNS.

If no address and port is specified, it will default to `127.0.0.1:2379`.

Using a Registry URI with the domain `cluster.local`, service definitions are
stored as:

	/skydns/local/cluster/<service-name>/<service-id> = {"host":"<ip>","port":<port>}

SkyDNS requires the service ID to be a valid DNS hostname, so this backend
requires containers to override the service ID to a valid DNS name. Example:

	$ docker run -d --name redis-1 -e SERVICE_ID=redis-1 -p 6379:6379 redis

## Zookeeper Store

The Zookeeper backend lets you publish ephemeral znodes into zookeeper. This
mode is enabled by specifying a zookeeper path. The backend supports publishing
a JSON znode body complete with defined service attributes/tags as well as the
service name and container id.

Example URIs:

	$ registrator zookeeper://zookeeper.host/basepath
	$ registrator zookeeper://192.168.1.100:9999/basepath

Within the base path specified in the zookeeper URI, registrator will create
the following path tree containing a JSON entry for the service:

	<service-name>/<service-port> = <JSON>

The JSON will contain all information about the published container service. As
an example, the following container start:

	docker run -i -p 80 -e 'SERVICE_80_NAME=www' -t ubuntu:14.04 /bin/bash

Will result in the zookeeper path and JSON znode body:

	/basepath/www/80 = {"Name":"www","IP":"192.168.1.123","PublicPort":49153,"PrivatePort":80,"ContainerID":"9124853ff0d1","Tags":[],"Attrs":{}}
