# Service Object

Registrator is primarily concerned with services that are added to a service
discovery registry. In this model, a service is anything listening on a port.
If a container listens on multiple ports, it has multiple services.

Services are created from container metadata and then passed to a registry
backend.

	type Service struct {
		ID    string               // unique service instance ID
		Name  string               // service name
		IP    string               // IP address service is located at
		Port  int                  // port service is listening on
		Tags  []string             // extra tags to classify service
		Attrs map[string]string    // extra attribute metadata
	}

## Container Overrides

The fields `Name`, `Tags`, `Attrs`, and `ID` can be overridden by user-defined
container metadata. You can use environment variables or labels prefixed with
`SERVICE_` or `SERVICE_x_` to set values, where `x` is the internal exposed
port. For example `SERVICE_NAME=customerdb` and `SERVICE_80_NAME=api`.

You use a port in the key name to refer to a particular service on that port.
Metadata variables without a port in the name are used as defaults for all
services or can be used to refer to the single exposed service.

The `Attrs` field is populated by metadata using any other field names in the
key name. For example, `SERVICE_REGION=us-east`.

Since metadata is stored as environment variables or labels, the container
author can include defaults in the Dockerfile and the operator can still
override them.

## Detecting Services

By default, Registrator picks up services from containers that have explicitly
published ports, for example `-p` or `-P`. This is true for containers running
in host network mode as well:

	$ docker run --net=host -p 8080:8080 -p 8443:8443 ...

If running with `runtime.internal=true` or
`REGISTRATOR_RUNTIME_INTERNAL=true`, Registrator will instead look for exposed
ports. These can be set from the Dockerfile or explicitly with:

	$ docker run --expose=8080 ...

You can tell Registrator to ignore a container by setting `SERVICE_IGNORE`.
You can ignore a single service with `SERVICE_<port>_IGNORE=true`.

## Service Name

Service names are what you use in service discovery lookups. By default, the
service name is determined by this pattern:

	<base(container-image)>[-<exposed-port> if >1 ports]

Using the base of the container image, if the image is `gliderlabs/foobar`, the
service name is `foobar`. If the image is `redis` the service name is `redis`.

If a container has multiple exposed ports, Registrator appends the internal
exposed port to differentiate them. For example, an image `nginx` with ports 80
and 443 will produce two services named `nginx-80` and `nginx-443`.

You can override this default name with `SERVICE_NAME` or `SERVICE_x_NAME`,
where `x` is the internal exposed port. If a container has multiple exposed
ports then setting `SERVICE_NAME` still results in multiple services named
`SERVICE_NAME-<exposed port>`.

## IP and Port

IP and port make up the address that the service name resolves to. By default,
the port is the public published port and the IP attempts to resolve to the
host IP.

If automatic IP detection is not right for your environment, set
`runtime.hostIP` or `REGISTRATOR_RUNTIME_HOST_IP` explicitly.

If you use `runtime.internal=true`, Registrator uses the exposed port and the
Docker-assigned internal IP of the container.

## Tags and Attributes

Tags and attributes are extra metadata fields for services. Backend support is
not uniform:

- Consul supports tags and service metadata
- older key-value backends may ignore some or all attributes

Attributes can also drive backend-specific features. For example, Consul uses
them for health checks documented in [Backend Reference](./backends.md#consul).

Registrator-managed Consul services also receive internal ownership metadata so
restart and reconcile flows can identify which stale registrations belong to
the current node. Treat those keys as implementation metadata, not application
metadata.

## Unique ID

The ID is a cluster-wide unique identifier for the service instance. Users
usually query by service name rather than ID, but IDs matter for reconcile and
cleanup behavior.

By default, Registrator uses the configured `service.idFormat` template. The
default template is:

	<hostname>:<container-name>:<exposed-port>[:udp if udp]

The hostname portion is resolved from the local runtime identity used by
Registrator for both registration and stale cleanup. In Swarm-aware mode this
prefers Docker node or engine identity before falling back to the OS hostname.

The name of the container is included because it is more human-friendly than a
raw container ID.

To identify the specific service, Registrator uses the internal exposed port.
That is usually more meaningful than the published port, which may be an
arbitrary host-side value.

If the service is UDP, that is appended to differentiate it from TCP on the
same port.

You can override the ID with `SERVICE_ID` or `SERVICE_x_ID`, but do it
carefully. For Consul, this fork adds ownership metadata to its own
registrations so custom IDs can still be cleaned up safely after restart.

## Examples

### Single service with defaults

	$ docker run -d --name redis.0 -p 10000:6379 progrium/redis

Results in `Service`:

	{
		"ID": "hostname:redis.0:6379",
		"Name": "redis",
		"Port": 10000,
		"IP": "192.168.1.102",
		"Tags": [],
		"Attrs": {}
	}

### Single service with metadata

	$ docker run -d --name redis.0 -p 10000:6379 \
		-e "SERVICE_NAME=db" \
		-e "SERVICE_TAGS=master,backups" \
		-e "SERVICE_REGION=us2" progrium/redis

Results in `Service`:

	{
		"ID": "hostname:redis.0:6379",
		"Name": "db",
		"Port": 10000,
		"IP": "192.168.1.102",
		"Tags": ["master", "backups"],
		"Attrs": {"region": "us2"}
	}

Keep in mind not every backend uses the full `Service` object. Consul uses tags
and metadata attributes; older backends may only use the core name, IP, and
port fields.

The comma can be escaped by adding a backslash:

	$ docker run -d --name redis.0 -p 10000:6379 \
		-e "SERVICE_NAME=db" \
		-e "SERVICE_TAGS=/(;\\,:-_)/" \
		-e "SERVICE_REGION=us2" progrium/redis

### Multiple services with defaults

	$ docker run -d --name nginx.0 -p 4443:443 -p 8000:80 progrium/nginx

Results in two `Service` objects:

	[
		{
			"ID": "hostname:nginx.0:443",
			"Name": "nginx-443",
			"Port": 4443,
			"IP": "192.168.1.102",
			"Tags": [],
			"Attrs": {}
		},
		{
			"ID": "hostname:nginx.0:80",
			"Name": "nginx-80",
			"Port": 8000,
			"IP": "192.168.1.102",
			"Tags": [],
			"Attrs": {}
		}
	]

### Multiple services with metadata

	$ docker run -d --name nginx.0 -p 4443:443 -p 8000:80 \
		-e "SERVICE_443_NAME=https" \
		-e "SERVICE_443_ID=https.12345" \
		-e "SERVICE_443_SNI=enabled" \
		-e "SERVICE_80_NAME=http" \
		-e "SERVICE_TAGS=www" progrium/nginx

Results in two `Service` objects:

	[
		{
			"ID": "https.12345",
			"Name": "https",
			"Port": 4443,
			"IP": "192.168.1.102",
			"Tags": ["www"],
			"Attrs": {"sni": "enabled"}
		},
		{
			"ID": "hostname:nginx.0:80",
			"Name": "http",
			"Port": 8000,
			"IP": "192.168.1.102",
			"Tags": ["www"],
			"Attrs": {}
		}
	]

### Using labels to define metadata

	$ docker run -d --name redis.0 -p 10000:6379 \
		-l "SERVICE_NAME=db" \
		-l "SERVICE_TAGS=master,backups" \
		-l "SERVICE_REGION=us2" dockerfile/redis

Results in `Service`:

	{
		"ID": "hostname:redis.0:6379",
		"Name": "db",
		"Port": 10000,
		"IP": "192.168.1.102",
		"Tags": ["master", "backups"],
		"Attrs": {"region": "us2"}
	}
