# Skill: Multi-Node Deployment

**Triggers:** When the user asks to add a node, scale across multiple machines/servers, deploy to a specific node or remote host, distribute/migrate workloads, set up a cluster, or move data/volumes between nodes.
**Prerequisites:** SSH access from the master to target nodes (for `volume_migrate`), nodes reachable at their registered address, and the node registered in the cluster before deploying to it.
**RBAC Required:** admin (for `node_add`, `node_remove`, `node_update`, `volume_migrate`); operator (for `deploy_to_node`); viewer (for `list_nodes`, `get_node`, `suggest_node`)

## Workflow

1. **Register the node** — `node_add` with `id` (unique), `address` (host:port), `backend` (docker or cube), `hostname`, `memory_mb`, `cpu_cores`, `disk_gb`, `state` (default active)
   - The node must be running a compatible backend (Docker Engine or Cube). Verify reachability before registering.
2. **Confirm registration** — `node_list` and `get_node` with the new node's ID
   - Verify the node shows as `active` with correct resource capacity.
3. **Pick the best node for placement** — `suggest_node` with `required_memory_mb`, `required_cpu_count`
   - Returns top-3 bin-packing candidates across ALL registered nodes. Use this to decide where new containers go.
4. **Deploy to the chosen node** — `deploy_to_node` with `node_id`, plus the deploy args (image/template/git config)
   - Creates a remote backend connection to the node and runs the container there. Pair with `suggest_node` output.
5. **Verify health on the new node** — `health_check_status` with the deployed container's ID
   - Health checks work across nodes; the master monitors all nodes.

## Volume Migration Between Nodes

To move an existing container's persistent data to a different node:

1. **Verify both nodes** — `get_node` on source and target. Both must be `active`.
2. **Migrate the volume** — `volume_migrate` with `volume_id`, `target_node_id`
   - Uses tar + scp over SSH. **Requires SSH access from the master to the target node.** Data is preserved; the source volume is not deleted automatically.
3. **Confirm** — `volume_info` on the target to verify size/file count match the source.
4. **Cutover** — stop/redeploy the container on the target node (`deploy_to_node`) and attach the migrated volume (`volume_attach`).
5. **Clean up the source** — once confirmed working on the target, `delete_volume` on the source (admin) if no longer needed.

## Health Checks Across Nodes

Health probes are cluster-wide — `health_check_list` shows status for containers on every node. There's no per-node filter; correlate via `get_container` (which includes the node ID) if you need to scope.

- A node going `offline` will show its containers' health checks as failing. Check `list_nodes` first when health failures spike cluster-wide.
- `ha_state` shows whether this master is active or standby — relevant if you have HA configured.

## Decision Points

- **Adding capacity vs. relocating?** New capacity → `node_add` then `deploy_to_node` for new workloads. Relocating → `volume_migrate` then redeploy on the target.
- **Which backend on the new node?** Match what's installed. Docker for full-featured nodes; Cube for edge/low-RAM (4GB) nodes. `backend_info` on the node (if reachable) tells you. Mismatched backend will fail at deploy time.
- **Node state** — set `state: "draining"` via `node_update` to stop new placements there while keeping existing containers (useful before maintenance). Set `state: "offline"` to fully remove from scheduling. `suggest_node` respects these states.
- **Bin-packing vs. spreading?** `suggest_node` optimizes for bin-packing (fewest, fullest nodes). If you want spreading for redundancy, deploy to the node with the most free capacity (`get_node` on candidates) instead.
- **SSH not available for migration?** `volume_migrate` requires it. Alternative: `backup_volume` on source, transfer out-of-band, `restore_backup` on target (manual). Slower but works without SSH.

## Error Recovery

- **`node_add` fails** → the node may already be registered (use `node_update` instead), unreachable at the given address, or running an incompatible backend. Verify connectivity and backend before retrying.
- **`deploy_to_node` fails** → remote backend connection issue. `get_node` to check state; verify the node's backend is actually running (SSH in and check the daemon). If the node is `offline`, set it `active` via `node_update` first.
- **`volume_migrate` fails** → SSH connectivity or permissions. The master must SSH to the target as a user with write access to the volume root. Check `CUBE_NODE_TLS_*` env vars if inter-node TLS is enabled. Do not retry blindly — a partial migration can leave a corrupt volume. `volume_info` on both sides to assess.
- **Node disappears (hardware failure)** → `list_nodes` will show it `offline`. Its containers are lost. Recover from backup: `list_backups`, `restore_backup` to a healthy node. If HA is configured, check `ha_state`.
- **Containers stuck after node drain** → `node_update` to draining doesn't migrate containers, it only stops new placements. You must explicitly `volume_migrate` + `deploy_to_node` to relocate them.
- **Plaintext inter-node traffic** → by default, remote Docker connections are plaintext. In production, set `CUBE_DOCKER_TLS=true` (AS-4). The server prints a stderr warning when plaintext is used. Volume migrations via SSH (`volume_migrate`) are already encrypted by SSH.

## Example Session

> **User:** Add a new node, node-b at 10.0.0.5:2375, Docker, 16GB RAM, 8 cores. Then move the API's volume there.
>
> **Agent:**
> 1. `node_add` `{"id": "node-b", "address": "10.0.0.5:2375", "backend": "docker", "hostname": "node-b", "memory_mb": 16384, "cpu_cores": 8, "disk_gb": 200, "state": "active"}`
> 2. `node_list` → confirms node-b active, capacity correct
> 3. `suggest_node` `{"required_memory_mb": 512, "required_cpu_count": 1}}` → node-b scores highest (empty node)
> 4. `get_node` `{"id": "node-b"}` → verify before migration
> 5. `volume_migrate` `{"volume_id": "api-data", "target_node_id": "node-b"}` → SSH transfer complete
> 6. `volume_info` `{"volume_id": "api-data"}` on node-b → size matches source
> 7. `deploy_to_node` `{"node_id": "node-b", "image": "...", "volumes": [{"id": "api-data", "path": "/data"}]}` → new container c-api-nodeb
> 8. `health_check_status` `{"container_id": "c-api-nodeb"}` → healthy
> 9. `kill_container` on the old node-a instance once traffic confirms on node-b
>
> API relocated to node-b with its data intact. Clean up the old volume on node-a when ready.
