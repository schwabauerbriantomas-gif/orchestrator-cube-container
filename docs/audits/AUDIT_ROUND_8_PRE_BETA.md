# Round 8 — Pre-Beta Security Audit

**Date:** July 9, 2026
**Scope:** Full pre-release review — authentication, hypervisor, web UI, deploy, CI/CD, dependencies
**Method:** 4 parallel subagent audits (auth/session, injection/hypervisor, web/deploy, CI/deps)
**Total findings:** 27 new (1 CRITICAL, 8 HIGH, 12 MEDIUM, 6 LOW)

---

## Findings Summary

### CI/CD & Deployment Audit

| ID | Severity | Component | Issue | Status |
|---|---|---|---|---|
| C-CI-01 | ~~CRITICAL~~ | go.mod | Go 1.25.0 — 23 reachable stdlib CVEs | ✅ Fixed: bump to 1.25.12 |
| NEW-01 | ~~HIGH~~ | Caddyfile | No Content-Security-Policy header | ✅ Fixed: CSP added |
| NEW-02 | ~~HIGH~~ | entrypoint.sh | MCP HTTP server + Caddy never started | ✅ Fixed: both now start |
| H-CI-02 | ~~HIGH~~ | ci.yml | gosec@master — floating tag | ✅ Fixed: SHA-pinned |
| H-CI-03 | ~~HIGH~~ | ci.yml | All actions use mutable tags | ✅ Fixed: all SHA-pinned |
| H-CI-04 | ~~HIGH~~ | .github | No Dependabot config | ✅ Fixed: dependabot.yml added |
| NEW-03 | MEDIUM | Dockerfile | No USER directive (runs as root) | ⏳ Deferred: containerd needs root |
| NEW-04 | ~~MEDIUM~~ | Dockerfile | Caddy download without checksum | ✅ Fixed: SHA256 verification |
| NEW-05 | ~~MEDIUM~~ | auth.go | ipFromAddr breaks IPv6 | ✅ Fixed: net.SplitHostPort |
| NEW-06 | MEDIUM | auth.go/api.ts | No CSRF infrastructure | ⏳ Deferred: blocks cookie migration only |
| M-CI-05 | MEDIUM | ci.yml | No SBOM generation | ⏳ Post-beta |
| M-CI-06 | ~~MEDIUM~~ | ci.yml | No govulncheck SARIF upload | ✅ Fixed: SARIF upload added |
| M-CI-07 | ~~MEDIUM~~ | Dockerfile | golang:1.25 floating tag | ✅ Fixed: pinned to 1.25.12 |
| M-CI-08 | LOW | ci.yml | Single Go version in matrix | ⏳ Acceptable for beta |
| NEW-07 | ~~LOW~~ | Dockerfile | curl without --fail | ✅ Fixed: -sfL flag added |
| NEW-08 | ~~LOW~~ | Dockerfile | No HEALTHCHECK | ✅ Fixed: healthcheck added |
| NEW-09 | ~~LOW~~ | Dockerfile | EXPOSE 8080 bypasses Caddy | ✅ Fixed: removed from EXPOSE |
| L-CI-09 | ~~LOW~~ | ci.yml | No timeout-minutes | ✅ Fixed: 15min on all jobs |
| L-CI-10 | LOW | ci.yml | Secrets exposure | ✅ Clean — no secrets used |

### Hypervisor Injection Audit

| ID | Severity | Component | Issue | Status |
|---|---|---|---|---|
| H-01 | ~~HIGH~~ | hypervisor_zfs.go | Unvalidated devices in zpool create (arg injection) | ✅ Fixed: validateDevicePath |
| H-02 | ~~HIGH~~ | hypervisor_zfs.go | Unvalidated compression/recordsize (property injection) | ✅ Fixed: allowlist + numeric validation |
| H-03 | ~~HIGH~~ | hypervisor.go | Unvalidated disk_path/iso_path/network → XML injection | ✅ Fixed: validateFilePathOrEmpty + validateNetworkName + xmlEscape |
| M-01 | ~~MEDIUM~~ | hypervisor_cloudinit.go | YAML injection in packages (cloud-init RCE) | ✅ Fixed: validatePackageName |
| M-02 | ~~MEDIUM~~ | hypervisor_cloudinit.go | YAML injection in password field | ✅ Fixed: %q quoting |
| M-03 | MEDIUM | hypervisor.go | Systemic text/template for XML (no contextual escaping) | ✅ Mitigated: xmlEscape on all interpolations |
| M-04 | ~~MEDIUM~~ | hypervisor_cloudinit.go | Unvalidated seed_iso path (XML injection) | ✅ Fixed: validateFilePathOrEmpty |

### Authentication Audit

| ID | Severity | Component | Issue | Status |
|---|---|---|---|---|
| A-M01 | ~~MEDIUM~~ | auth.go | rand.Read error ignored — predictable keys on CSPRNG failure | ✅ Fixed: log.Fatal on failure |
| A-M02 | ~~MEDIUM~~ | auth.go | RBAC fail-open when tool extraction returns "" | ✅ Fixed: fail-closed for tools/call |
| A-M03 | MEDIUM | auth.go | RequireRole endpoints have no rate limiting | ⏳ Post-beta: per-key limiter still applies |
| A-M04 | MEDIUM | auth.go | Audit logging silently disabled on file open failure | ⏳ Post-beta: stderr warning exists |
| A-M05 | MEDIUM | auth.go | Audit chain degrades to unkeyed SHA-256 without secrets manager | ⏳ Post-beta: requires startup fail-fast refactor |
| A-L01 | ~~LOW~~ | auth.go | handleCreateKey leaks SecretHash in response | ✅ Fixed: response struct excludes hash |
| A-L02 | LOW | auth.go | Per-IP rate limiter ineffective behind reverse proxy | ⏳ Post-beta: documented limitation |

---

## Resolved vs Deferred

**Resolved: 19 / 27** (all CRITICAL + all HIGH)

**Deferred to post-beta (8 items):** All MEDIUM/LOW with documented justification — none represent active exploitable vulnerabilities in the current architecture.

---

## Deferred Items (justified)

### NEW-03: Dockerfile runs as root
**Why deferred:** `containerd` and `cubelet` require root for DinD pattern.
Running `cube-mcp` and `caddy` as non-root would require `gosu`/`su-exec` privilege
separation — planned for v0.10.

### NEW-06: No CSRF infrastructure
**Why deferred:** Currently safe — auth uses custom headers that cross-origin forms
cannot set. CSRF tokens needed only for cookie-based auth (planned for TOTP/2FA in v0.10).

### M-CI-05: No SBOM
**Why deferred:** Compliance feature, not a vulnerability. Planned for v1.0 (EU CRA).

### A-M03: RequireRole endpoints no rate limiting
**Why deferred:** Per-key rate limiter still applies via Wrap(). The RequireRole endpoints
(/auth/keys, /metrics) are admin-only and not exposed to untrusted users in the current
deployment model.

### A-M04: Audit logging silently disabled on file open failure
**Why deferred:** Server emits stderr warning on startup. Refactoring to fail-fast requires
changes to server initialization order. Planned for v0.10.

### A-M05: Audit chain HMAC degradation
**Why deferred:** Only triggers when secrets manager fails to initialize (env var missing).
Server logs WARNING but continues. Requires startup hardening refactor. Planned for v0.10.

### A-L02: Per-IP rate limiter behind reverse proxy
**Why deferred:** Documented limitation. Per-key limiter is the primary defense. Caddy
handles X-Forwarded-For at the proxy layer.

---

## Cumulative Audit History

| Round | Scope | Findings | Resolved |
|---|---|---|---|
| 1-2 | Core MCP server | 15 | 15 |
| 3-4 | Attack surfaces | 14 | 14 |
| 5 | Sentinel consensus | 11 | 11 |
| 6 | Apex forensics | 7 | 7 |
| 7 | Hypervisor layer | 9 | 9 |
| 8 | Pre-beta (this round) | 27 (19 fixed, 8 deferred) | 19 |
| **Total** | | **83** (75 fixed, 8 deferred) | **75 resolved** |

---

## Methodology

4 independent audits run in parallel, each by a separate subagent with isolated context:

1. **Auth/Session audit** — reviewed auth.go, auth_tokens.go, auth_test.go for timing
   attacks, RBAC bypass, key entropy, audit chain integrity
2. **Injection/Hypervisor audit** — reviewed all hypervisor_*.go for shell injection,
   path traversal, XML/YAML injection, PCI address injection, resource exhaustion
3. **Web/Deploy audit** — reviewed Caddyfile, Dockerfile, entrypoint.sh, web/ for
   CORS, CSRF, CSP, Docker security, TLS config
4. **CI/CD audit** — reviewed ci.yml, go.mod, go.sum for dependency CVEs (live
   govulncheck), supply chain risks, action pinning, Dependabot gaps
