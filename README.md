# Cube Container

[![Fork of TencentCloud/CubeSandbox](https://img.shields.io/badge/fork%20of-CubeSandbox-blue)](https://github.com/TencentCloud/CubeSandbox)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-green)](LICENSE)
[![MCP Server](https://img.shields.io/badge/MCP-22%20tools-orange)](https://modelcontextprotocol.io)
[![Min RAM: 4GB](https://img.shields.io/badge/Min%20RAM-4GB-success)]()

**Container-mode fork of [CubeSandbox](https://github.com/TencentCloud/CubeSandbox)** — the same control plane, E2B-compatible API, and MCP orchestration, but running on **native Linux containers (containerd + runc + overlayfs)** instead of KVM MicroVMs.

Designed for **low-resource edge nodes** (4 GB RAM, 4 cores, no KVM) where hardware virtualization isn't available or is overkill.

---

## Why This Fork?

CubeSandbox is an excellent AI-agent sandbox runtime (8.7K+ stars on GitHub), but it requires:

- **KVM support** (`/dev/kvm`)
- **8+ GB RAM** per node
- **XFS with reflink** for Copy-on-Write storage
- A full hypervisor stack (rustvmm / Cloud Hypervisor)

**Cube Container** strips out the entire VM layer — `hypervisor/`, `cubecow/`, `CubeNet/`, `CubeShim/` — and runs everything as native containers. Same control plane, same API surface, same lifecycle management, but **10× lighter and 6× faster cold starts**.

### Target hardware

| Spec | CubeSandbox (upstream) | **Cube Container** |
|------|----------------------|---------------------|
| Min RAM per node | 8 GB | **4 GB** |
| Virtualization required | KVM (`/dev/kvm`) | **None** |
| CPU overhead per workload | ~50 MB (VM kernel + VMM) | **~2–5 MB** (process) |
| Cold start | ~60 ms (boot VM kernel) | **~5–10 ms** (clone + namespaces) |
| Auto-resume (pause/thaw) | ~100 ms (VM restore) | **~15 ms** (cgroup freezer) |

Runs on COTS mini-PCs, ARM SBCs, recycled office machines, Proxmox VMs, or any Linux host with containerd.

---

## What Changed

| Component | CubeSandbox (original) | Cube Container (this fork) |
|-----------|----------------------|---------------------------|
| **Isolation** | KVM MicroVM (hardware-level) | runc containers (namespace-level) |
| **Hypervisor** | `hypervisor/` (rustvmm, 30+ modules) | ❌ Removed — not needed |
| **Storage** | `cubecow/` (XFS reflink CoW) | `overlayfs` (containerd native) |
| **Networking** | `CubeNet/` (eBPF + vsock) | CNI bridge (standard) |
| **Runtime Shim** | `CubeShim/` (containerd → VM bridge) | ❌ Removed — runc is direct |
| **Storage plugin** | CubeCow + XFS only | Accepts `overlayfs` backend |

### What Stayed the Same

- ✅ **CubeAPI** (Rust) — E2B-compatible REST API on `:3000`
- ✅ **CubeMaster** (Go) — multi-node scheduler
- ✅ **Cubelet** (Go) — per-node lifecycle management
- ✅ **cube-lifecycle-manager** — auto-pause/resume logic
- ✅ **CubeProxy** (nginx) — reverse proxy + TLS
- ✅ **CubeEgress** (OpenResty) — egress control
- ✅ **network-agent** (Go) — network management
- ✅ **Web Console** (`:12088`)

### What Was Added

- ✅ **MCP Server** (Python) — 22 tools for AI-agent-driven orchestration
- ✅ **Auth Gateway** (FastAPI) — API-key + RBAC + rate limiting + audit for multiuser production
- ✅ **Caddy Proxy** — TLS 1.3 + WAF + rate limiting for external exposure
- ✅ **Persistent Deploy** — deploy from git or inline code with volume support
- ✅ **Input Validation** — path traversal prevention, git URL sanitization, command injection blocking

---

## Architecture

### Dual-mode operation

Cube Container runs in two modes simultaneously:

```
 ┌─────────────────────────────────────────────────────────────┐
 │                      LOCAL (trusted)                         │
 │                                                              │
 │  AI Agent ──stdio──▶ MCP Server ──HTTP──▶ CubeAPI :3000     │
 │  (Hermes,           (Python,        │      (Rust, E2B API)   │
 │   Claude,           22 tools)       │                        │
 │   Cursor)                           ▼                        │
 │                              CubeMaster (Go)                  │
 │                              ├── Node 1 (Cubelet + runc)     │
 │                              ├── Node 2 (Cubelet + runc)     │
 │                              └── Node N (Cubelet + runc)     │
 └─────────────────────────────────────────────────────────────┘

 ┌─────────────────────────────────────────────────────────────┐
 │                   REMOTE (untrusted / production)            │
 │                                                              │
 │  External                  Caddy :443                        │
 │  Client ────HTTPS──▶       ├── TLS 1.3                       │
 │  (API key)                 ├── WAF (15 OWASP rules)          │
 │                            └── Rate limiting                  │
 │                                  │                           │
 │                                  ▼                           │
 │                          Auth Gateway :8090                   │
 │                          ├── API-key + secret auth            │
 │                          ├── RBAC (viewer/operator/admin)    │
 │                          ├── Rate limiting (120 req/min/key) │
 │                          └── Audit trail (JSONL append-only) │
 │                                  │                           │
 │                                  ▼                           │
 │                          MCP HTTP :8080 / CubeAPI :3000       │
 └─────────────────────────────────────────────────────────────┘
```

### Security layer separation

| Layer | Where | Applies to |
|-------|-------|------------|
| Input validation (path traversal, git sanitization, cmd injection) | `security.py` — MCP server | **Both** modes |
| TLS 1.3 + WAF + rate limiting | `Caddyfile` — Caddy proxy | HTTP mode only |
| API-key auth + RBAC + audit | `auth_gateway.py` — FastAPI | HTTP mode only |

This means: local stdio mode has zero auth overhead (it's a pipe), while HTTP mode gets full production-grade security at the proxy + gateway layers.

---

## MCP Tools (22 total)

Any MCP-compatible AI agent (Claude, Cursor, Hermes, OpenAI agents, local LLMs) can manage the entire cluster through natural language.

### Cluster Management (5)

| Tool | Description |
|------|-------------|
| `cluster_health` | Check CubeAPI reachability |
| `cluster_overview` | Node count, running containers, resource capacity |
| `cluster_versions` | Component version matrix (CubeAPI, CubeMaster, Cubelet) |
| `list_nodes` | All nodes with CPU/RAM/disk info |
| `get_node` | Detailed node info by ID |

### Container Lifecycle (7)

| Tool | Description |
|------|-------------|
| `list_containers` | Running / paused / stopped containers |
| `get_container` | Container details by ID |
| `create_container` | Deploy from template with CPU/RAM/env config |
| `kill_container` | Stop and remove |
| `pause_container` | Freeze (cgroup freezer, ~0 CPU, ~15ms to resume) |
| `resume_container` | Thaw a paused container |
| `get_container_logs` | Fetch stdout/stderr logs |

### Templates (3)

| Tool | Description |
|------|-------------|
| `list_templates` | Available container templates |
| `create_template` | Create from any OCI image with port mappings |
| `get_template` | Template details |

### Persistent Deployment (4)

| Tool | Description |
|------|-------------|
| `deploy_from_git` | Clone repo, build image, deploy with env vars + volumes |
| `deploy_from_code` | Deploy from inline files (no git needed) |
| `update_code` | Pull latest from git and redeploy |
| `exec_in_container` | Run command inside a running container |

### Volumes (3)

| Tool | Description |
|------|-------------|
| `list_volumes` | Persistent volumes across the cluster |
| `create_volume` | Create a named volume |
| `delete_volume` | Remove a volume |

---

## Quick Start

### Build the image

```bash
docker build -f deploy/container-mode/Dockerfile -t cube-container:latest .
```

### Run a single node

```bash
docker run -d --name cube-node \
  --privileged \
  -v /var/lib/containerd:/var/lib/containerd \
  -v /var/lib/cube-container:/var/lib/cube-container \
  -p 3000:3000 \
  -p 12088:12088 \
  cube-container:latest
```

### Configure your MCP client

```json
{
  "mcpServers": {
    "cube-container": {
      "command": "cube-mcp",
      "env": {
        "CUBE_API_URL": "http://localhost:3000",
        "CUBE_API_KEY": "e2b_000000"
      }
    }
  }
}
```

### Deploy a service from git (via MCP)

```
User: "Deploy the app at github.com/me/my-api on port 8000"

Agent → deploy_from_git(
    git_url="https://github.com/me/my-api",
    expose_ports=[8000],
    memory_mb=256
)
→ Container running at node-2:8000
```

### Expose securely for external users

```bash
# Start the auth gateway
python -m cube_mcp.auth_gateway --port 8090

# Start Caddy with TLS + WAF
caddy run --config deploy/container-mode/Caddyfile
```

Generate API keys:
```bash
python -m cube_mcp.auth_gateway --gen-key --role operator
# → key: cc_live_a1b2c3d4...  secret: sec_e5f6g7h8...
```

---

## Comparison with Alternatives

### Sandbox / Isolation Runtimes

| Feature | **Cube Container** | CubeSandbox (upstream) | E2B | Daytona | fly.io Machines |
|---------|-------------------|----------------------|-----|---------|----------------|
| **Isolation model** | runc containers | KVM MicroVM | gVisor Firecracker | Containers / MicroVM | Firecracker |
| **Min RAM per node** | **4 GB** | 8 GB | 8 GB (managed) | 8 GB | 4 GB (managed) |
| **Cold start** | **~5 ms** | ~60 ms | ~150 ms (cold) | ~5 s | ~300 ms |
| **Self-hosted** | ✅ | ✅ | ❌ (SaaS only) | ✅ | ❌ (managed) |
| **KVM required** | ❌ | ✅ | ✅ | Optional | N/A |
| **MCP support** | ✅ 22 tools | ❌ | ❌ | ❌ | ❌ |
| **AI-agent native** | ✅ (MCP, stdio+HTTP) | ❌ | SDK only | ❌ | ❌ |
| **Cost** | Your hardware | Your hardware (needs KVM) | $0.05/hr per sandbox | $0.02–0.10/hr | $0.001/min+ |
| **Best for** | Edge nodes, self-hosted services, AI agent ops | Untrusted LLM code execution | Managed code sandboxes | Dev environments | Managed global infra |

**When to use Cube Container over the others:**

- ✅ You have **low-resource hardware** without KVM (ARM boards, mini-PCs, VPS)
- ✅ You want **AI agents to manage your infra** (MCP-native, not just an SDK)
- ✅ You need **self-hosted** — no per-hour costs, no vendor lock-in
- ✅ You're running **trusted workloads** (your own services, not untrusted user code)
- ❌ You need **hardware isolation** for untrusted code → use upstream CubeSandbox or E2B

### Container Orchestration (K8s / Nomad / Docker Swarm)

| Feature | **Cube Container** | Kubernetes | Nomad | Docker Swarm |
|---------|-------------------|-----------|-------|-------------|
| **Complexity** | Low (single binary + MCP) | High (etcd, kubelet, CNI, CSI) | Medium | Low |
| **Min nodes** | 1 | 1 (but heavy) | 1 | 1 |
| **Min RAM (control plane)** | **~50 MB** | ~1 GB | ~200 MB | ~100 MB |
| **MCP orchestration** | ✅ (native) | Via 3rd-party tools | ❌ | ❌ |
| **Auto-pause idle** | ✅ (~15 ms resume) | ❌ | ❌ | ❌ |
| **Git-driven deploy** | ✅ (`deploy_from_git`) | ArgoCD / Flux (separate) | Templates | ❌ |
| **Learning curve** | Low | Steep | Medium | Low |
| **Best for** | Edge, AI-agent ops, small clusters | Enterprise, large-scale | Hybrid workloads | Simple stacks |

Cube Container is **not trying to replace Kubernetes**. It targets a different niche: small clusters (1–10 nodes) on resource-constrained hardware where K8s is overkill, but you still need multi-node scheduling, lifecycle management, and MCP-native AI-agent control.

---

## Security Model

### Security Trade-off (important)

| | CubeSandbox (KVM) | Cube Container (runc) |
|---|---|---|
| Isolation strength | Hardware (dedicated kernel) | Namespace (shared kernel) |
| Container escape risk | Near zero | Low but nonzero |
| Best for | Untrusted LLM-generated code | **Trusted workloads and services** |

**Cube Container is designed for hosting your own services** (APIs, static sites, workers, bots) where you control what runs inside the containers. It is **not suitable for running untrusted user-submitted code**.

For untrusted workloads, use upstream [CubeSandbox](https://github.com/TencentCloud/CubeSandbox) with KVM.

### Production Security (HTTP mode)

For multiuser / external access, Cube Container provides layered security:

```
HTTPS client → Caddy (TLS 1.3 + WAF + rate limit)
             → Auth Gateway (API key + RBAC + audit)
             → MCP Server / CubeAPI (input validation)
```

| Control | Implementation |
|---------|----------------|
| Transport encryption | TLS 1.3 via Caddy |
| WAF | 15 OWASP Top-10 rules (SQLi, XSS, path traversal, SSRF, etc.) |
| Authentication | API key + secret pair, HMAC-signed requests |
| Authorization | RBAC: viewer (read-only), operator (deploy/manage), admin (full) |
| Rate limiting | 120 req/min per API key (configurable) |
| Input validation | Path traversal prevention, git URL sanitization, command injection blocking |
| Audit logging | JSONL append-only with tamper-evident hashing |

---

## Files Modified from Upstream

| File | Change |
|------|--------|
| `Cubelet/pkg/cubecow/cubecow_stub.go` | Replaced error stubs with success no-ops (container mode doesn't need CubeCow) |
| `Cubelet/pkg/nsenter/nsenter_nocgo.go` | Added `!cgo` fallback (original requires CGO for namespace operations) |
| `Cubelet/pkg/cubemnt/nsenter_nocgo.go` | Same `!cgo` fallback for mount namespace operations |
| `Cubelet/storage/plugin.go` | Added `StorageBackendOverlayFS` constant and overlayfs path in `init()` |

## Directories Removed

| Directory | Lines removed | Reason |
|-----------|--------------|--------|
| `hypervisor/` | ~62,000 | rustvmm/KVM VMM — not needed without VMs |
| `cubecow/` | ~2,000 | XFS reflink CoW engine — replaced by overlayfs |
| `CubeNet/` | ~3,500 | eBPF networking for VMs — replaced by CNI bridge |
| `CubeShim/` | ~3,000 | containerd→VM bridge — runc is direct |

**~70,000 lines removed.** The entire VM, networking, and storage-virtualization stack — gone.

## Files Added

| Path | Description |
|------|-------------|
| `mcp-server/src/cube_mcp/server.py` | MCP server (22 tools, dual stdio+HTTP mode) |
| `mcp-server/src/cube_mcp/client.py` | HTTP client for CubeAPI |
| `mcp-server/src/cube/mcp/deploy.py` | Persistent deploy from git/code with volumes |
| `mcp-server/src/cube_mcp/security.py` | Input validation (path traversal, git URL, cmd injection) |
| `mcp-server/src/cube_mcp/auth_gateway.py` | FastAPI auth gateway — API keys, RBAC, rate limiting, audit |
| `deploy/container-mode/Dockerfile` | Container-mode build (CubeAPI + Cubelet + MCP) |
| `deploy/container-mode/Caddyfile` | Caddy reverse proxy with TLS 1.3 + WAF + rate limiting |
| `deploy/container-mode/entrypoint.sh` | Node startup script |
| `deploy/container-mode/config.toml` | Runtime configuration |

---

## Project Structure

```
cube-container/
├── CubeAPI/                   # Rust — E2B-compatible REST API (:3000)
├── CubeMaster/                # Go — multi-node scheduler
├── Cubelet/                   # Go — per-node lifecycle (modified for overlayfs)
├── cube-lifecycle-manager/    # Auto-pause/resume controller
├── CubeProxy/                 # nginx reverse proxy + TLS
├── web/                       # React web console (:12088)
├── mcp-server/                # Python — MCP server (22 tools) + auth gateway
│   ├── src/cube_mcp/
│   │   ├── server.py          # MCP server — dual stdio + HTTP mode
│   │   ├── client.py          # CubeAPI HTTP client
│   │   ├── deploy.py          # Persistent deploy from git/code
│   │   ├── security.py        # Input validation
│   │   └── auth_gateway.py    # Auth + RBAC + audit (FastAPI)
│   └── pyproject.toml
├── deploy/
│   └── container-mode/        # Dockerfile, Caddyfile, config
├── examples/                  # Integration examples
└── sdk/                       # Python + Go SDKs
```

---

## Development

### Run the MCP server locally (stdio mode)

```bash
cd mcp-server
pip install -e .
CUBE_API_URL=http://localhost:3000 cube-mcp
```

### Run the auth gateway

```bash
python -m cube_mcp.auth_gateway --port 8090
```

### Run tests

```bash
cd mcp-server && python -m pytest tests/ -v
```

---

## Roadmap

- [ ] Multi-node auto-discovery (mDNS / etcd)
- [ ] WebSocket-based log streaming via MCP
- [ ] Container resource metrics (CPU/RAM/I/O) via MCP
- [ ] Preemptive scheduling based on node load
- [ ] Snapshot/rollback for container state
- [ ] OIDC / OAuth2 authentication for auth gateway
- [ ] Webhook notifications on container events

---

## License

Apache 2.0 (inherited from upstream [CubeSandbox](https://github.com/TencentCloud/CubeSandbox) / Tencent Cloud).

---

## Credits

- **[TencentCloud/CubeSandbox](https://github.com/TencentCloud/CubeSandbox)** — original project, 8.7K+ stars. This fork would not exist without their work.
- Built on [containerd](https://containerd.io/), [runc](https://github.com/opencontainers/runc), [Caddy](https://caddyserver.com/), [FastAPI](https://fastapi.tiangolo.com/), and the [MCP protocol](https://modelcontextprotocol.io/).
