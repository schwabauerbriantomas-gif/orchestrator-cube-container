# Untrusted Code Hosting — Isolation Options for Cube Container

> **Purpose**: This document evaluates options for safely executing untrusted
> third-party code (user-submitted scripts, CI/CD pipelines for external repos,
> multi-tenant workloads) within the Cube Container platform.

## Threat Model

When hosting code from third parties, the threats are:

1. **Container escape** — malicious code exploits a kernel bug to break out of the container and access the host
2. **Resource exhaustion** — code consumes all CPU/memory/disk, affecting other tenants
3. **Network attacks** — code scans internal networks, attacks other containers or the metadata service
4. **Side-channel attacks** — code observes timing/power to extract secrets from co-located workloads
5. **Supply chain** — malicious dependencies in the code being built/executed

Standard Docker containers share the host kernel — they rely on namespaces and cgroups for isolation, not hardware virtualization. A kernel vulnerability (like Dirty Pipe CVE-2022-0847) can bypass all container isolation.

## ⭐ Recommended: Cube KVM Backend (Already Integrated)

**CubeSandbox's KVM backend is the native solution — no additional software needed.**

The Cube backend (`CUBE_BACKEND=cube`) creates sandboxes using KVM + rust-vmm,
providing hardware-level isolation with sub-60ms boot times and <5MB memory
overhead per instance. Each sandbox runs its own guest kernel.

### Features Available Through MCP (129 tools)

| Feature | Tool | What It Does |
|---------|------|-------------|
| **KVM sandbox** | `secure_sandbox_create` | Creates isolated VM with own kernel |
| **Egress control** | `secure_sandbox_egress_add/list/remove` | Domain allowlist/blocklist per sandbox |
| **Credential vault** | `credential_vault` param | API keys injected by proxy, never in sandbox |
| **CubeCoW snapshots** | `secure_sandbox_snapshot/restore` | Instant rollback to known-good state |
| **Time limits** | `max_lifetime_seconds` param | Auto-pause after N seconds |
| **Network kill** | `network_disabled` param | Zero network access |

### Security Hardening Applied (Round 4 + Round 5 Audits)

#### Round 4

| ID | Fix |
|----|-----|
| C8 | Vault config rejects plaintext HTTP to remote hosts |
| H12 | Egress rule IDs validated (alphanumeric only, no path traversal) |
| M11 | Egress rules registered directly against CubeEgress, not just metadata |
| M12 | `secure_sandbox_list` filters to only secure sandboxes, not all containers |
| B8 | DNS rebinding documented as known limitation (CubeEgress must enforce at connection time) |

#### Round 5 (Attack Surface Audit)

| ID | Severity | Fix |
|----|----------|-----|
| AS-1 | High | **Security model documented**: `secure_sandbox_exec` intentionally does NOT filter commands. KVM isolation is the security boundary. Denylist expanded for defense-in-depth. |
| AS-2 | Medium | `exec_in_container` timeout hard-capped at 300s (floor 1s). Prevents resource exhaustion via long-running commands. |
| AS-3 | Medium | Denylist expanded with 15 additional patterns: pipe-to-network (`\| curl`, `\| wget`, `\| nc`), backtick substitution, reverse shell patterns (`/dev/tcp`), chaining operators. Note: defense-in-depth only — `sh -c` cannot be fully contained at the string level. |
| AS-4 | Medium | Inter-node Docker connections support real TLS (`CUBE_DOCKER_TLS=true`). Plaintext now prints a stderr warning. |
| AS-5 | Low | Webhook secrets accepted via `X-Git-Token` header only — query-param fallback removed to prevent log/Referer leakage. |
| AS-6 | Low | HA heartbeat endpoint rate-limited (60 req/min per-IP). |
| AS-7 | Info | Audit hash chain upgraded to HMAC-SHA256 keyed with `CUBE_SECRETS_KEY`. Tamper-evident against full-log-rewrite attacks. |

### Secure Sandbox Security Model (AS-1)

The secure sandbox is designed to execute **arbitrary untrusted code**. Applying the
`validateCommand()` allowlist would directly contradict this purpose — the sandbox
exists precisely because the code cannot be trusted.

**The security boundary is KVM hardware isolation:**

```
┌─────────────────────────────────────┐
│  Host kernel                        │
│  ┌────────────────────────────────┐ │
│  │  KVM                           │ │
│  │  ┌──────────────────────────┐  │ │
│  │  │  Guest kernel (isolated) │  │ │
│  │  │  ┌────────────────────┐  │  │ │
│  │  │  │  Untrusted code    │  │  │ │
│  │  │  │  (can do anything  │  │  │ │
│  │  │  │   WITHIN the VM)   │  │  │ │
│  │  │  └────────────────────┘  │  │ │
│  │  └──────────────────────────┘  │ │
│  └────────────────────────────────┘ │
└─────────────────────────────────────┘
```

The code inside the sandbox can run `rm -rf /` — and it only destroys the disposable
guest filesystem. It can run reverse shells — but they're contained by the egress
allowlist and network kill switch. **Command filtering is unnecessary when the entire
kernel is disposable.**

The denylist in `security.go` (applied to `exec_in_container`, not `secure_sandbox_exec`)
serves as defense-in-depth for the Docker backend, where no hardware isolation exists.

### Why This Beats External Solutions

- **No KVM setup needed** — CubeSandbox already manages it
- **<5MB overhead** vs 32-128MB for Firecracker/Kata
- **<60ms boot** — same class as Firecracker
- **CubeEgress proxy** — credential vault is unique to CubeSandbox
- **CubeCoW** — snapshot/restore is near-instant

### When to Use External Solutions Instead

| Scenario | Use |
|----------|-----|
| **Cube backend available** | ✅ Cube KVM (native, best option) |
| Docker-only, need mild isolation | gVisor (Level 2) |
| Need OCI compatibility + VM isolation | Kata Containers (Level 3) |
| Building FaaS/serverless platform | Firecracker-containerd (Level 4) |

---

## Alternative Options (If Cube Backend Is Not Available)

## Isolation Levels

| Level | Technology | Isolation Strength | Performance Overhead | Complexity |
|-------|-----------|-------------------|---------------------|------------|
| 0 | Standard container (Docker/runc) | Low — shared kernel | ~0% | None (current) |
| 1 | Seccomp + AppArmor + dropped capabilities | Medium — syscall filter | ~0-2% | Low |
| 2 | gVisor (runsc) | High — user-space kernel | 5-20% (syscall-heavy) | Medium |
| 3 | Kata Containers | Highest — lightweight VM | 2-10% | Medium |
| 4 | Firecracker microVM | Highest — minimal VM | 2-5% | High |

## Option Analysis

### Level 1: Hardened Container (Immediate, Zero Cost)

**What**: Tighten the existing Docker backend with seccomp profiles, AppArmor, and dropped capabilities.

**How**: Add a `--security-opt` flag to container creation in `docker_client.go`:
```go
// Drop all capabilities, add back only what's needed
SecurityOpts: []string{
    "no-new-privileges",
    "apparmor=docker-strict",
},
CapDrop: []string{"ALL"},
CapAdd:  []string{"NET_BIND_SERVICE"}, // only if needed
```

**Pros**: Zero new dependencies, works today, minimal overhead.
**Cons**: Still shares kernel. Not sufficient for truly untrusted code.

**Verdict**: ✅ **Implement as baseline for all containers**. This should be the default, not optional.

---

### Level 2: gVisor (Best Balance for Edge Nodes)

**What**: [gVisor](https://gvisor.dev/) is a user-space kernel written in Go. It intercepts syscalls from the container and handles them in a sandboxed environment, preventing direct kernel access.

**How**: Install gVisor on the host, configure containerd/Docker to use the `runsc` runtime:
```bash
# Install gVisor
wget https://storage.googleapis.com/gvisor/releases/release/latest/x86_64/runsc
chmod +x runsc && mv runsc /usr/local/bin/

# Configure Docker
{
  "runtimes": {
    "runsc": { "path": "/usr/local/bin/runsc" }
  }
}

# Use in Cube Container
create_container --runtime=runsc
```

**Pros**:
- Strong isolation without VM overhead
- Negligible startup time (process, not VM)
- Works on 4GB RAM edge nodes (no per-container VM memory tax)
- Transparent — no app changes needed

**Cons**:
- 5-20% performance overhead on syscall-heavy workloads
- Some syscalls not implemented (rare, but can break exotic apps)
- Adds ~50MB to the host

**Verdict**: ✅ **Best option for Samsung edge nodes (4GB RAM)**. Strong isolation without the memory cost of VMs.

---

### Level 3: Kata Containers (Maximum Compatibility + Strong Isolation)

**What**: [Kata Containers](https://katacontainers.io/) runs each container inside a lightweight VM (using QEMU, Cloud Hypervisor, or Firecracker as the hypervisor). Hardware-level isolation with full syscall compatibility.

**How**: Install Kata runtime, configure containerd:
```bash
# Install Kata
apt install kata-runtime kata-proxy kata-shim

# Configure Docker
{
  "runtimes": {
    "kata-runtime": { "path": "/usr/bin/kata-runtime" }
  }
}
```

**Pros**:
- Hardware-level isolation (VM boundary)
- Full syscall compatibility — runs any Linux app
- Good Kubernetes/containerd integration
- Supports multiple hypervisors (QEMU, Cloud Hypervisor, Firecracker)

**Cons**:
- Each container needs its own VM kernel → ~128MB memory overhead per container
- Slower startup (200ms-1s vs ~10ms for plain containers)
- Not practical on 4GB RAM nodes for more than 2-3 containers

**Verdict**: ⚠️ **Good for dedicated build nodes with 8GB+ RAM**. Too heavy for 4GB Samsung nodes.

---

### Level 4: Firecracker microVM (Maximum Security, FaaS-grade)

**What**: [Firecracker](https://firecracker-microvm.github.io/) is AWS's purpose-built VMM for serverless (powers Lambda). Creates minimal VMs in <125ms with a tiny attack surface (~50K lines of Rust).

**Integration**: [firecracker-containerd](https://github.com/firecracker-microvm/firecracker-containerd) provides a containerd runtime shim that runs each container in its own Firecracker microVM.

**How**: Install firecracker-containerd, configure as runtime class:
```bash
# Requires KVM support (bare metal or nested virtualization)
# Install firecracker + firecracker-containerd
git clone https://github.com/firecracker-microvm/firecracker-containerd
cd firecracker-containerd && make && make install

# Configure containerd with firecracker runtime shim
# Each container gets: own kernel, own VM, own network namespace
```

**Pros**:
- Sub-125ms boot time (fastest VM technology)
- Minimal attack surface (no device emulation, no PCI bus)
- Used in production by AWS Lambda and Fargate
- Per-container VM = strongest possible isolation

**Cons**:
- Requires KVM (bare metal or nested virt) — NOT available in most VPS
- Requires a Linux kernel image per VM (~5MB)
- Networking setup is complex (TAP devices per VM)
- firecracker-containerd is not as mature as Kata

**Verdict**: ✅ **Best for dedicated bare-metal build servers**. Overkill for edge nodes but ideal for a central "build farm" that processes untrusted code.

---

### Bonus: Knative + Firecracker (Full Serverless Platform)

For a complete untrusted code execution platform (like AWS Lambda self-hosted):

- **Knative** provides: scale-to-zero, event-driven invocation, traffic routing
- **Firecracker-containerd** provides: per-function microVM isolation
- **Cube Container MCP** provides: natural language operations interface

This is the architecture used by AWS Lambda / Google Cloud Run, but self-hosted.

**Complexity**: High. Requires Kubernetes + Knative + Firecracker-containerd.
**Best for**: Multi-tenant SaaS where customers upload arbitrary code.

---

### Bonus: Nuclio (Simpler Serverless)

[Nuclio](https://github.com/nuclio/nuclio) is a simpler alternative to Knative:

- Built-in function management UI
- Supports Go, Python, Node.js, shell
- Can be configured to use Firecracker for isolation
- Easier to deploy than Knative (no Kubernetes required)

**Best for**: Internal code execution platform with moderate isolation needs.

## Recommendation for Cube Container

### Tier 1: Edge Nodes (Samsung 4GB RAM)
**Use gVisor (`runsc`)**. Add a `--runtime` parameter to `create_container` and `deploy_from_git` that defaults to standard Docker but can be set to `runsc` for untrusted workloads. gVisor runs on 4GB RAM without per-container memory tax.

### Tier 2: Central Build Server (8GB+ RAM)
**Use Kata Containers** or **Firecracker** for maximum isolation. This is where CI/CD pipelines for external repos run. Add a `CUBE_SECURE_RUNTIME` environment variable that sets the default runtime for a node.

### Tier 3: Multi-Tenant SaaS
**Use Knative + Firecracker** for the full serverless platform. This is a future direction if Cube Container evolves into a multi-tenant product.

### Implementation Path

```
Phase 1 (immediate):
  - Add seccomp + AppArmor + cap-drop to Docker backend (hardened default)
  - Add --runtime parameter to create_container

Phase 2 (near-term):
  - Add gVisor support (document setup, test with runsc)
  - Add CUBE_SECURE_RUNTIME env var for node-level default

Phase 3 (future):
  - Kata Containers integration for build nodes
  - firecracker-containerd for bare-metal servers
  - Knative for multi-tenant mode
```

## Quick Comparison Table

| Feature | Standard Docker | gVisor | Kata | Firecracker |
|---------|----------------|--------|------|-------------|
| Isolation | Namespace + cgroups | User-space kernel | VM (hardware) | microVM (hardware) |
| Kernel shared | Yes | No (intercepted) | No (own kernel) | No (own kernel) |
| RAM overhead | 0 | ~0 | ~128MB/container | ~32MB/container |
| Startup time | ~10ms | ~10ms | 200ms-1s | <125ms |
| Syscall compat | Full | Partial | Full | Full |
| KVM required | No | No | Yes | Yes |
| Edge (4GB) viable | ✅ | ✅ | ❌ (too heavy) | ❌ (needs KVM) |
| Best for | Trusted code | Untrusted code on edge | Untrusted on beefy nodes | Serverless / FaaS |
