# Superisletmem Consul Agent Deployment

These manifests move Consul client agents from standalone Docker Compose
containers to Swarm-managed services. Consul server containers remain under
Dokploy and are not part of this stack.

## Safety invariants

- Pin both Consul and Registrator images by digest.
- Preserve the existing `consul-agent-data` local volume.
- Remove the standalone agent before enabling the matching Swarm task.
- Migrate and validate one production node at a time.
- Never deploy production without explicit approval.

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
