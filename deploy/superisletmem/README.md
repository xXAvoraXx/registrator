# Superisletmem Consul Agent Deployment

These manifests move Consul client agents from standalone Docker Compose
containers to Swarm-managed services. Consul server containers remain under
Dokploy and are not part of this stack.

## Safety invariants

- Pin both Consul and Registrator images by digest.
- Preserve the existing `consul-agent-data` local volume.
- Remove the standalone agent before enabling the matching Swarm task.
- Use Registrator as the only writer for application service registrations.
- Merge the matching file under `applications/` into the existing Dokploy
  environment; never replace the complete environment or secret values.
- Merge `consul-kv-registrator-owner.patch.json` structurally into each
  application's existing Consul KV JSON; never replace the complete value.
- Require exactly one passing, `registrator`-tagged catalog record for each
  migrated application service before continuing.
- Migrate and validate one production node at a time.
- Never deploy production without explicit approval.

## Application registration ownership

`consul-kv-registrator-owner.patch.json` disables Steeltoe writes while keeping
Consul reads enabled with passing-only queries. The application environment
files opt the intended HTTP port back into Registrator despite the existing
generic `SERVICE_IGNORE` setting. They also replace the old standalone-agent
hostname: application-node services use `consul-agent-application`, while
management services use `consul-agent-management`. The gRPC service uses its
own `business-service-grpc.env` snippet for the same reason.

Back up and structurally merge the patch into these keys:

1. `sync-service/appsettings.json`
2. `business-service/appsettings.json`
3. `business-service/appsettings-admin.json`
4. `admin-service/appsettings.json`
5. `Gateway/appsettings.json`

Redeploy the affected application after changing its KV value. Environment
variables alone are insufficient because the current application configuration
order allows the Consul KV value to win.

Apply one application at a time in this order:

1. `sync-service`
2. `business-service`
3. `business-service-grpc` using its existing Swarm labels
4. `admin-service`
5. `gateway`

After each deployment, verify:

- the old Steeltoe-generated service ID is absent;
- exactly one Registrator-owned service ID is present;
- the check is `passing` and includes a one-minute critical deregistration
  timeout where the application metadata declares it;
- `/readyz` is `200`;
- the application smoke test succeeds.

Rollback an application by restoring both its encrypted Consul KV backup and
Dokploy environment snapshot, then redeploying its previous immutable image.
Remove a legacy registration only after its Registrator-owned replacement is
passing and application smoke tests succeed.

## Test rollout

1. Save the existing Dokploy Consul compose and Registrator service specs.
2. Remove only `consul-agent` from the test Dokploy compose and deploy it.
3. Add `consul_agent_swarm=true` to the test Swarm node.
4. Deploy `consul-agents.test.yml` as stack `consul-agents`.
5. Verify `agent-node-1`, catalog parity, critical checks, Registrator readiness,
   and the test Keycloak route.

The test host uses `/opt/config-share/consul-agent-config.hcl`; production
hosts use `/srv/config-share/consul-agent-config.hcl`.

## Production rollout

Deploy `consul-agents.prod.yml` before setting the migration gate label. The
three services remain at `0/1` until their matching node receives
`consul_agent_swarm=true`.

For each node in management, persistence, application order:

1. Remove only the standalone `consul-agent` from that node's Dokploy compose.
2. Add the migration gate label to that Swarm node.
3. Wait for the constrained agent task to become healthy.
4. Verify Consul membership, catalog parity, critical checks, local Registrator
   `/readyz`, and the node's application smoke tests.

Rollback one node by removing its migration gate label, restoring the saved
Dokploy compose, and redeploying the standalone agent against the same volume.

## Monitoring

Mount the matching file from `monitoring/` into Prometheus and add node-local
blackbox jobs for `/readyz` plus direct `/metrics` scrapes. Production probes
must use `100.101.0.1`, `100.101.0.2`, and `100.101.0.3`; do not route these
checks through Swarm ingress.
