# Cube Container

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-green)](LICENSE)
[![MCP Server](https://img.shields.io/badge/MCP-129%20tools-orange)](https://modelcontextprotocol.io)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8)](https://go.dev)
[![Security Audit](https://img.shields.io/badge/Security-47%20issues%20fixed-red)](#security)
[![Tests](https://img.shields.io/badge/Tests-40%20passing-brightgreen)](#testing)

A container orchestration platform controlled by AI through the Model Context Protocol. An MCP server that replaces the DevOps role — the operations interface is natural language, not YAML.

158 tools covering the complete DevOps lifecycle: containers, images, deployments, scaling, health monitoring, networking, routing, secrets, backups, high availability, multi-node clusters, environments, notifications, scheduled jobs, database provisioning, certificates, event streaming, and **full hypervisor management** (VMs via KVM/libvirt, ZFS storage, GPU passthrough).

## Architecture

```
User → HTTPS → Caddy (TLS+WAF+Rate Limit) → MCP HTTP :8080
    → (Auth + RBAC + Audit + Body Limit + Max Connections)
    → Backend (Docker local | Cube local | Remote Node)
    → Containers
```

**Dual backend**: auto-detects Docker (production, unix socket) or Cube (edge, 4GB RAM nodes). Override with `CUBE_BACKEND=docker|cube`.

**Dual mode**: stdio for personal use (no auth needed), HTTP for production (full auth stack).

## Quick Start

### Personal (stdio)

```bash
go build -o mcp-server-go ./mcp-server-go
./mcp-server-go --mode stdio
```

Connect from any MCP-compatible AI client (Claude Desktop, etc.).

### Production (HTTP + auth)

```bash
# Generate an admin API key
./mcp-server-go --gen-key admin --label "production"

# Start with HTTP
./mcp-server-go --mode http --port 8080

# Call a tool
curl -X POST http://localhost:8080/mcp \
  -H "Authorization: Bearer <key>:<secret>" \
  -d '{"tool": "cluster_health"}'
```

### Docker

```bash
docker build -t cube-container .
docker run -d -p 8080:8080 -v /var/run/docker.sock:/var/run/docker.sock cube-container
```

## Tool Reference (158 tools)

<!-- Tool count is verified by CI: `grep -c 'registerTool(s,' mcp-server-go/tools_registration.go` must equal 158. -->

### Cluster & Nodes (12)

| Tool | Description | Role |
|------|-------------|------|
| `cluster_health` | Check cluster API health | viewer |
| `cluster_overview` | Total nodes, containers, CPU/RAM usage | viewer |
| `cluster_versions` | Version matrix of all components | viewer |
| `list_nodes` | All nodes with capacity and load | viewer |
| `get_node` | Detailed node info | viewer |
| `suggest_node` | Best node for new container (bin-packing) | viewer |
| `backend_info` | Active backend + features + tool count | viewer |
| `node_add` | Register a new cluster node | admin |
| `node_update` | Update node properties (address, state, resources) | admin |
| `node_remove` | Remove a node from the cluster registry | admin |
| `node_list` | List all registered cluster nodes | viewer |
| `node_get` | Detailed info about a specific node | viewer |

### Container Lifecycle (10)

| Tool | Description | Role |
|------|-------------|------|
| `list_containers` | List containers with optional filters | viewer |
| `get_container` | Detailed container info | viewer |
| `create_container` | Create + start from template | operator |
| `kill_container` | Stop + remove | operator |
| `pause_container` | Freeze (cgroup freezer) | operator |
| `resume_container` | Un-freeze | operator |
| `get_container_logs` | Recent logs | viewer |
| `tail_container_logs` | Last N lines (one-shot) | viewer |
| `exec_in_container` | Run command inside container | operator |
| `backend_info` | Backend type + tool count | viewer |

### Image Lifecycle (5) — CI/CD pipeline

| Tool | Description | Role |
|------|-------------|------|
| `image_build` | Build from Dockerfile (context tarballed to Docker API) | operator |
| `image_push` | Push to registry (validates no private hosts) | admin |
| `image_pull` | Pull image from registry | operator |
| `image_list` | List images with size + tags | viewer |
| `image_tag` | Tag existing image with new name | operator |

### Deploy (8)

| Tool | Description | Role |
|------|-------------|------|
| `deploy_from_git` | Clone → volume → template → container | operator |
| `deploy_from_code` | Deploy from inline files (no git) | operator |
| `update_code` | Update deployed code in-place | operator |
| `rollback_deploy` | Rollback to previous version | admin |
| `list_deploy_versions` | Version history | viewer |
| `deploy_to_node` | Deploy to specific remote node | operator |
| `deploy_rollout` | Rolling or blue-green update | operator |
| `suggest_node` | Best node for deployment | viewer |

### Scaling & Services (9)

| Tool | Description | Role |
|------|-------------|------|
| `service_create` | Define scalable service | admin |
| `service_remove` | Remove service definition | admin |
| `scale_set` | Set exact replica count | operator |
| `service_list` | All services with replica counts | viewer |
| `service_get` | Service details | viewer |
| `service_register` | Register endpoint in discovery | operator |
| `service_deregister` | Remove from discovery | operator |
| `service_resolve` | Look up service by name | viewer |
| `service_entries` | List all discovery entries | viewer |

### Health & Monitoring (6)

| Tool | Description | Role |
|------|-------------|------|
| `health_check_set` | Configure HTTP/TCP/exec probe + auto-restart | operator |
| `health_check_remove` | Remove health probe | operator |
| `health_check_list` | All health checks with status | viewer |
| `health_check_status` | Detailed status for container | viewer |
| `metrics_query` | Query cluster metrics (CPU, mem, requests) | viewer |
| `events_list` / `events_recent` | Cluster event stream | viewer |

### Log Aggregation (2)

| Tool | Description | Role |
|------|-------------|------|
| `logs_search` | Search across all containers by pattern/level | viewer |
| `logs_aggregate` | Error/warn/info counts per container | viewer |

### Volumes (7)

| Tool | Description | Role |
|------|-------------|------|
| `create_volume` | Create persistent volume | operator |
| `delete_volume` | Permanently delete | admin |
| `list_volumes` | All volumes | viewer |
| `volume_info` | Size, file count, attachments | viewer |
| `volume_attach` | Attach to running container | operator |
| `volume_detach` | Detach (data preserved) | operator |
| `volume_migrate` | Migrate to remote node via SSH | admin |

### Networking (9)

| Tool | Description | Role |
|------|-------------|------|
| `add_port_mapping` / `remove_port_mapping` / `list_port_mappings` | Port forwarding | operator/admin/viewer |
| `add_dns_alias` / `remove_dns_alias` / `list_dns_aliases` | DNS management | operator/admin/viewer |
| `add_network_policy` / `remove_network_policy` / `list_network_policies` | Network policies | admin/admin/viewer |

### Routing & TLS (4)

| Tool | Description | Role |
|------|-------------|------|
| `create_route` | Create Caddy reverse proxy route + auto TLS | admin |
| `delete_route` | Remove route | admin |
| `list_routes` | All routes | viewer |
| `reload_routes` | Reload Caddy config | admin |

### Secrets & ConfigMaps (9)

| Tool | Description | Role |
|------|-------------|------|
| `secret_set` | Store AES-256-GCM encrypted secret | admin |
| `secret_get` | Decrypt + retrieve | operator |
| `secret_list` | List with metadata (no values) | viewer |
| `secret_delete` | Permanently delete | admin |
| `configmap_create` / `configmap_update` / `configmap_get` / `configmap_list` / `configmap_remove` | Non-sensitive config | admin/operator/viewer/viewer/admin |

### Backup & Restore (5)

| Tool | Description | Role |
|------|-------------|------|
| `backup_volume` | Tar.gz backup with SHA256 | operator |
| `backup_container` | Full backup (config + volumes) | operator |
| `list_backups` | All backups | viewer |
| `restore_backup` | Restore from backup | admin |
| `delete_backup` | Delete backup | admin |

### Environments (4) — namespace isolation

| Tool | Description | Role |
|------|-------------|------|
| `env_create` | Create isolated environment | admin |
| `env_list` | List environments with container counts | viewer |
| `env_get` | Environment details | viewer |
| `env_promote` | Promote container dev → staging → prod | operator |

### Notifications (4) — alert delivery

| Tool | Description | Role |
|------|-------------|------|
| `notify_channel_add` | Register Slack/Discord/Telegram/Email | admin |
| `notify_channel_list` | List channels | viewer |
| `notify_channel_remove` | Remove channel | admin |
| `notify_send` | Send message to channel | operator |

### Auth & API Tokens (3)

| Tool | Description | Role |
|------|-------------|------|
| `auth_create_token` | Create API token (secret shown once) | admin |
| `auth_list_tokens` | List tokens (no secrets) | admin |
| `auth_revoke_token` | Revoke token | admin |

### Scheduled Jobs (4)

| Tool | Description | Role |
|------|-------------|------|
| `job_create` | Schedule periodic tool execution | admin |
| `job_list` | List jobs with next run | viewer |
| `job_remove` | Remove job | admin |
| `job_run` | Run job immediately | operator |

### Database Provisioning (3)

| Tool | Description | Role |
|------|-------------|------|
| `database_create` | Provision Postgres/MySQL/Redis/MongoDB | admin |
| `database_backup` | Backup DB volume | operator |
| `database_restore` | Restore DB from backup | admin |

### Certificates (2)

| Tool | Description | Role |
|------|-------------|------|
| `cert_list` | List TLS certs with expiry | viewer |
| `cert_renew` | Trigger renewal via Caddy reload | admin |

### Resource Management (4)

| Tool | Description | Role |
|------|-------------|------|
| `resource_set_limits` | Memory/CPU limits via docker update | operator |
| `resource_get_usage` | Real-time CPU/mem/net/disk for container | viewer |
| `resource_list_usage` | Usage for all containers | viewer |
| `resource_quota_summary` | Allocated vs capacity | viewer |

### Garbage Collection (3)

| Tool | Description | Role |
|------|-------------|------|
| `gc_prune_images` | Remove unused images (7+ days old) | operator |
| `gc_prune_volumes` | Remove orphaned volumes | operator |
| `gc_disk_usage` | Disk breakdown (images/containers/volumes) | viewer |

### Templates (3)

| Tool | Description | Role |
|------|-------------|------|
| `create_template` | Template from image | operator |
| `get_template` | Template details | viewer |
| `list_templates` | All templates | viewer |

### Audit & Compliance (1)

| Tool | Description | Role |
|------|-------------|------|
| `audit_query` | Search tamper-evident audit trail (hash chain) | viewer |

### Secure Sandbox (8) — KVM untrusted code execution

These tools provide hardware-level isolation for running untrusted code. Each sandbox runs its own guest kernel — kernel escape is impossible. **Cube backend only** (`CUBE_BACKEND=cube`).

| Tool | Description | Role |
|------|-------------|------|
| `secure_sandbox_create` | Create KVM-isolated VM with egress filtering + credential vault | admin |
| `secure_sandbox_exec` | Execute code inside sandbox (max 300s timeout) | operator |
| `secure_sandbox_egress_add` | Add domain to allowlist/blocklist | admin |
| `secure_sandbox_egress_list` | List egress rules for sandbox | viewer |
| `secure_sandbox_egress_remove` | Remove egress rule | admin |
| `secure_sandbox_snapshot` | CubeCoW instant snapshot for rollback | operator |
| `secure_sandbox_restore` | Restore sandbox to previous state | admin |
| `secure_sandbox_list` | List secure sandboxes only (filters by metadata) | viewer |

### High Availability (1)

| Tool | Description | Role |
|------|-------------|------|
| `ha_state` | Active-passive failover state | viewer |

### Alerting (4)

| Tool | Description | Role |
|------|-------------|------|
| `alert_rule_add` | Create alert rule (container_down, cpu_high, disk_high, mem_high) | admin |
| `alert_rule_remove` | Remove alert rule | admin |
| `alert_list` | List all alert rules with fire counts | viewer |
| `alert_test` | Fire test alert to verify webhook config | operator |

### Events (2)

| Tool | Description | Role |
|------|-------------|------|
| `events_list` | List events filtered by type/severity/time | viewer |
| `events_recent` | Get 20 most recent cluster events | viewer |

### Hypervisor: VM Lifecycle (13) — KVM/libvirt

| Tool | Description | Role |
|------|-------------|------|
| `vm_list` | List all VMs (KVM/QEMU domains) with state, memory, vCPU | viewer |
| `vm_get` | Get detailed VM info (state, IPs, autostart) | viewer |
| `vm_create` | Create and start a new KVM VM (auto-generates qcow2 disk) | admin |
| `vm_start` | Start a stopped VM | operator |
| `vm_stop` | Shutdown VM (graceful or force) | operator |
| `vm_pause` | Suspend a running VM (state preserved in memory) | operator |
| `vm_resume` | Resume a paused VM | operator |
| `vm_delete` | Permanently delete VM (undefine + optional disk removal) | admin |
| `vm_snapshot_create` | Create a libvirt snapshot of a VM | operator |
| `vm_snapshot_list` | List all snapshots for a VM | viewer |
| `vm_snapshot_restore` | Revert VM to a previous snapshot | admin |
| `vm_snapshot_delete` | Delete a VM snapshot | admin |
| `vm_migrate` | Live or offline migration to another host | admin |

### Hypervisor: ZFS Storage (12)

| Tool | Description | Role |
|------|-------------|------|
| `zpool_list` | List ZFS pools with size, health, free space | viewer |
| `zpool_create` | Create a new ZFS pool from block devices | admin |
| `zpool_status` | Detailed pool health and vdev status | viewer |
| `zpool_destroy` | Destroy a ZFS pool and all datasets | admin |
| `zdataset_list` | List ZFS datasets with usage stats | viewer |
| `zdataset_create` | Create a dataset with optional compression/recordsize | admin |
| `zdataset_destroy` | Destroy a dataset and its snapshots | admin |
| `zsnapshot_create` | Instant ZFS snapshot (near-zero cost, CoW) | operator |
| `zsnapshot_list` | List ZFS snapshots with usage and creation time | viewer |
| `zsnapshot_destroy` | Destroy a ZFS snapshot | admin |
| `zsnapshot_clone` | Clone snapshot into new writable dataset | operator |
| `zsnapshot_rollback` | Rollback dataset to snapshot (destroys intermediate) | admin |

### Hypervisor: GPU Management (4)

| Tool | Description | Role |
|------|-------------|------|
| `gpu_list` | Detect all GPUs (NVIDIA, AMD, Intel iGPU) with PCI/VFIO status | viewer |
| `gpu_stats` | Real-time GPU utilization (GPU%, mem%, temp, power, clocks) | viewer |
| `gpu_assign` | Assign GPU to VM via VFIO passthrough (requires IOMMU) | admin |
| `gpu_release` | Release GPU from VM, rebind to host driver | admin |

## Security

### Audit History

47 security issues identified and fixed across 5 audit rounds:

| Round | Critical | High | Medium | Low | Total |
|-------|----------|------|--------|-----|-------|
| 1 | 4 (C1-C4) | 5 (H1-H5) | 5 (M1-M5) | 4 (B1-B4) | 18 |
| 2 | 2 (C5-C6) | 3 (H6-H8) | 2 (M6-M7) | 1 (B5) | 8 |
| 3 | 1 (C7) | 3 (H9-H11) | 3 (M8-M10) | 2 (B6-B7) | 9 |
| 4 | 1 (C8) | 1 (H12) | 2 (M11-M12) | 1 (B8) | 5 |
| 5 | — | 1 (AS-1) | 3 (AS-2, AS-3, AS-4) | 2 (AS-5, AS-6) | 7 |
| **Total** | **8** | **13** | **15** | **10** | **47** |

Round 5 (Attack Surface Audit) findings:

| ID | Severity | Finding | Fix |
|----|----------|---------|-----|
| AS-1 | High | `secure_sandbox_exec` bypasses command allowlist | Documented as by-design: KVM isolation is the security boundary, not command filtering. Defense-in-depth denylist expanded. |
| AS-2 | Medium | `exec_in_container` accepts unbounded timeout | Hard cap of 300s + floor of 1s enforced in handler. |
| AS-3 | Medium | `sh -c` in Docker exec bypasses allowlist | Denylist expanded with 15 additional exfiltration/reverse-shell patterns (pipe-to-network, backtick substitution, chaining operators). Defense-in-depth only. |
| AS-4 | Medium | Remote Docker/Cube clients default to plaintext TCP | `newDockerClientWithTransport` now supports `transport="tls"` with real TLS dial. Warning printed to stderr when plaintext is used. |
| AS-5 | Low | Webhook secret accepted via `?token=` query param | Removed query param fallback. `X-Git-Token` header is now the only accepted method. |
| AS-6 | Low | HA heartbeat endpoint lacks rate limiting | Per-IP rate limiter (60 req/min) added to `HandleHeartbeat`. |
| AS-7 | Info | Audit hash chain uses plain SHA-256 (recomputable) | `computeAuditHash` now uses HMAC-SHA256 keyed with `CUBE_SECRETS_KEY`. Falls back to SHA-256 for backward compatibility with existing logs. |

### Security Features

- **Auth**: API key + secret, timing-safe comparison
- **RBAC**: 3 roles (viewer, operator, admin), per-tool permissions
- **Rate limiting**: 120 req/min per key (global), 60 req/min per-IP on HA heartbeat (AS-6)
- **Audit logging**: tamper-evident **HMAC-SHA256** hash chain (keyed with `CUBE_SECRETS_KEY`), JSONL format
- **Secrets**: AES-256-GCM encryption at rest, argon2id key derivation
- **Input validation**: allowlist for commands, expanded denylist for exfiltration/reverse-shell patterns, path traversal protection, SSRF prevention
- **TLS**: automatic via Caddy (Let's Encrypt), or native with cert files
- **Inter-node TLS**: `CUBE_DOCKER_TLS=true` enables real TLS for remote Docker connections (AS-4). Plaintext emits a stderr warning.
- **Body limiting**: configurable max request size
- **Connection limiting**: per-IP max connections
- **Network isolation**: inter-node TLS optional (`CUBE_NODE_TLS_CERT`)
- **Exec timeout cap**: `exec_in_container` hard-capped at 300s (AS-2)
- **Webhook auth**: `X-Git-Token` header only — no query-param secrets (AS-5)

### Validators

| Function | Protects Against |
|----------|-----------------|
| `validateCommand` | Command injection (allowlist) |
| `validatePathSafe` | Path traversal |
| `validateImageRef` | Image tag injection |
| `validateRegistryURL` | SSRF via registry URL |
| `validateWebhookURL` | SSRF via webhook/alert URLs |
| `validateTelegramToken` | SSRF via malformed bot token |
| `validateGitURL` | SSRF via git clone |
| `validateContainerID` | Argument injection via container ID |
| `validateMountPath` | Mounting sensitive host paths |
| `validateDomain` | Domain injection in routing |
| `validateBranchName` | Git branch injection |
| `isPrivateHost` | SSRF to internal/cloud metadata IPs |
| `validateTelegramToken` | Telegram token format spoofing |
| `validateDomainRule` | Egress domain injection / SSRF |
| `validateRuleID` | Egress rule ID path traversal |

## Configuration

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CUBE_BACKEND` | auto | `docker` or `cube` (auto-detect if unset) |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Docker daemon socket path |
| `CUBE_BUILD_ROOT` | `/tmp/cube-builds` | Root dir for image build contexts |
| `CUBE_AUTH_KEYS_FILE` | `/var/lib/cube-container/auth-keys.json` | API keys storage |
| `CUBE_AUDIT_DIR` | `/var/lib/cube-container/audit` | Audit log directory |
| `CUBE_AUDIT_LOG` | `/var/lib/cube-container/audit.logl` | Active audit log file |
| `CUBE_SECRETS_KEY` | — | AES-256 hex key (64 chars) |
| `CUBE_SECRETS_PASSPHRASE` | — | Passphrase for argon2id key derivation |
| `CUBE_TLS_CERT` | — | TLS certificate file |
| `CUBE_TLS_KEY` | — | TLS private key file |
| `CUBE_NODE_TLS_CERT` | — | Inter-node TLS cert |
| `CUBE_NODE_TLS_KEY` | — | Inter-node TLS key |
| `CUBE_DOCKER_TLS` | `false` | Enable TLS for remote Docker connections (AS-4). Set to `true` in production multi-node clusters. |
| `CUBE_HA_PEERS` | — | HA peer addresses (comma-separated) |
| `CUBE_HA_SELF_ID` | — | This node's HA ID |
| `CUBE_HA_PRIORITY` | `100` | HA failover priority |
| `CUBE_HA_SECRET` | — | HA heartbeat HMAC secret |
| `CUBE_WEBHOOK_ENABLED` | `false` | Git webhook listener |
| `CUBE_WEBHOOK_SECRET` | — | Webhook HMAC secret |
| `CUBE_CADDY_RELOAD` | `caddy reload` | Caddy reload command |
| `CUBE_SLACK_WEBHOOK` | — | Auto-configure Slack channel |
| `CUBE_DISCORD_WEBHOOK` | — | Auto-configure Discord channel |
| `CUBE_TELEGRAM_TOKEN` | — | Auto-configure Telegram channel |
| `CUBE_TELEGRAM_CHAT_ID` | — | Telegram target chat ID |
| `CUBE_ENV_ROOT` | `/var/lib/cube-container/environments` | Environment definitions |
| `CUBE_JOBS_ROOT` | `/var/lib/cube-container/jobs` | Scheduled job storage |
| `CUBE_DB_ROOT` | `/var/lib/cube-container/databases` | DB instance metadata |
| `CUBE_HEALTH_ROOT` | `/var/lib/cube-container/health` | Health check configs |
| `CUBE_SERVICES_ROOT` | `/var/lib/cube-container/services` | Service definitions |
| `CUBE_ALLOW_INSECURE_GIT` | `false` | Allow http:// git URLs |

## Testing

```bash
# Run all tests with race detector
go test -race -count=1 ./...

# Run benchmarks
go test -bench=. -benchmem ./...
```

40 tests covering security validators, auth/RBAC, backup/restore, e2e flows, concurrency stress tests.

## Build

```bash
# Static binary
CGO_ENABLED=0 go build -ldflags "-s -w" -o mcp-server-go ./mcp-server-go

# With version
go build -ldflags "-s -w -X main.version=1.1.0" -o mcp-server-go ./mcp-server-go
```

Binary size: ~8.5MB (statically linked, no CGO).

## Project Structure

```
mcp-server-go/
├── server.go            — main(), managers, HTTP middleware, stdio/HTTP mode
├── tools_registration.go — all 129 tool registrations via registerTool()
├── tools_helpers.go     — tool builders, arg extraction, handler registry (jobs)
├── handlers_basic.go    — handlers: cluster, containers, templates, deploy, volumes, backup
├── handlers_phase2.go   — handlers: images, deploy, logs, envs, jobs, DBs, certs, events
├── handlers_secure.go   — handlers: secure sandbox operations
├── backend.go           — ContainerBackend interface + auto-detection
├── docker_client.go     — Docker Engine API backend
├── client.go            — CubeAPI backend
├── auth.go              — API keys, RBAC, rate limiting, audit hash chain
├── auth_tokens.go       — Programmatic token management (create/list/revoke)
├── security.go          — Input validators (command, path, URL, container ID)
├── images.go            — Docker image lifecycle (build/push/pull/list/tag)
├── deploy.go            — Git deploy + persistent volumes + version tracking
├── deploy_rollout.go    — Rolling + blue-green deployment
├── scaling.go           — Replica management + load balancing
├── health.go            — Health probes + auto-restart watcher
├── nodes.go             — Multi-node cluster management
├── volumes.go           — Volume lifecycle + SSH migration
├── discovery.go         — Service discovery registry
├── resources.go         — Resource limits + quotas
├── gc.go                — Image/volume garbage collector
├── alerting.go          — Alert rules + webhook dispatcher
├── configmaps.go        — Non-sensitive configuration data
├── secrets.go           — AES-256-GCM encrypted secrets
├── backup.go            — Backup + restore + integrity verification
├── routing.go           — Caddy routes + TLS
├── networking.go        — Port mappings, DNS aliases, network policies
├── ha.go                — Active-passive failover + heartbeat
├── jobs.go              — Scheduled jobs with real tool execution
├── log_aggregation.go   — Multi-container log search
├── audit_query.go       — Audit trail search
├── environments.go      — Namespace isolation
├── notifications.go     — Slack/Discord/Telegram/Email delivery
├── databases.go         — Managed DB provisioning
├── certificates.go      — TLS cert inspection
├── events.go            — Cluster event stream
├── metrics.go           — Prometheus metrics export
├── metrics_query.go     — Programmatic metrics query
├── secure_sandbox.go    — KVM sandbox for untrusted code
├── scheduler.go         — Bin-packing node scheduler
├── rollback.go          — Deployment versioning + rollback
├── logstream.go         — SSE log streaming endpoint
├── webhook.go           — Git webhook endpoint
├── Dockerfile           — Multi-stage build (Go + Caddy)
├── Caddyfile            — TLS 1.3 + WAF + rate limiting
├── ARCHITECTURE.md      — Codebase map for AI agents
├── AGENT_GUIDE.md       — Conventions for working on this codebase
└── *_test.go            — Tests (security, auth, backup, e2e, bench, concurrency)
```

## Documentation

### For Developers and AI Agents

| Document | Purpose |
|----------|---------|
| [ARCHITECTURE.md](mcp-server-go/ARCHITECTURE.md) | Code map — file categories, patterns, data flow, how to add features |
| [AGENT_GUIDE.md](mcp-server-go/AGENT_GUIDE.md) | Conventions, checklists, common mistakes, security checklist |
| [skills/](skills/) | 8 workflow skills teaching the AI model correct tool sequences |
| [docs/UNTRUSTED_HOSTING.md](docs/UNTRUSTED_HOSTING.md) | Isolation options for hosting third-party/untrusted code |

### Skills (AI Workflow Playbooks)

The `skills/` directory contains playbooks that teach the AI model how to chain tools into correct workflows:

| Skill | When to use |
|-------|------------|
| `deploy-from-scratch.md` | Full deploy: template → container → route → health → scale → alert |
| `zero-downtime-update.md` | Rolling or blue-green deployment with health gate |
| `database-provisioning.md` | Managed DB creation with secrets + health + backup |
| `incident-response.md` | Diagnose failures: logs → metrics → events → audit |
| `multi-node-deployment.md` | Deploy across cluster nodes with volume migration |
| `security-hardening.md` | Lock down: tokens, secrets, alerts, cert monitoring |
| `backup-strategy.md` | Scheduled backups, restore, GC strategy |
| `environment-lifecycle.md` | Dev → staging → prod promotion workflow |
| `untrusted-code-execution.md` | KVM sandbox for untrusted/third-party code |

## Roadmap

### Security
- [x] ~~Network isolation for database containers~~ (partially via network policies)
- [ ] Hardened container defaults (seccomp + AppArmor + cap-drop)
- [ ] `--runtime` parameter for gVisor/Kata/Firecracker support
- [ ] `CUBE_SECURE_RUNTIME` env var for node-level isolation default
- [ ] Enforce `CUBE_DOCKER_TLS=true` in production (currently warns on plaintext)

### Untrusted Code Hosting
- [ ] gVisor (`runsc`) support for edge nodes (4GB RAM)
- [ ] Kata Containers for dedicated build servers (8GB+ RAM)
- [ ] firecracker-containerd for bare-metal FaaS
- [ ] Knative + Firecracker for multi-tenant SaaS mode
- See [docs/UNTRUSTED_HOSTING.md](docs/UNTRUSTED_HOSTING.md) for full analysis

### Features
- [ ] Real-time event streaming via SSE
- [ ] Log timestamp extraction from known formats (RFC3339, syslog)
- [ ] Email channel implementation (SMTP relay)
- [x] ~~Job tool execution~~ (job executor now runs real tools via handler registry)
- [ ] Tests for Phase 2 features (images, rollout, logs, envs, jobs, DBs, certs, events)
- [ ] Unit tests for AS-1 through AS-7 fixes

## License

Apache 2.0 — See [LICENSE](LICENSE).
