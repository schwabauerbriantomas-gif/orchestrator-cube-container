# Skill: Incident Response

**Triggers:** When the user reports something is broken, down, slow, erroring, throwing 500s, "logs look bad", "users are complaining", or asks to investigate/diagnose/triage an issue.
**Prerequisites:** A running cluster with the affected containers. Logs and metrics are being collected.
**RBAC Required:** viewer (all investigation tools are read-only); operator/admin to take remediation action

## Workflow — Investigate

1. **Find errors in logs** — `logs_search` with `pattern: "ERROR"` (or the specific error string), optionally `containers` (list of IDs), `level: "error"`, `since_minutes`, `max_results`
   - Cast a wide net first (all containers, last 30 min), then narrow to the offender.
2. **Aggregate to find the worst offender** — `logs_aggregate` with `containers` (or empty for all), `since_lines`
   - Returns per-container error/warn/info counts, sorted by error count. **This is how you identify which container is failing** when the user doesn't know.
3. **Check metrics for resource pressure** — `metrics_query` with `metric` (e.g. CPU/memory pattern), `container` filter
   - High CPU/mem often explains slow responses or OOM kills. Cross-reference spikes with the error timeline.
4. **Review recent events** — `events_list` with `severity: "critical"`, `since_minutes`, or `events_recent` for a quick recent window
   - Shows container restarts, health failures, deploys, scaling events, and alert firings. Correlate the incident start with a deploy or restart.
5. **Check who did what** — `audit_query` with `since_hours`, optional `tool_name`, `success: false`
   - Tamper-evident trail of every tool call. If a human or agent made a change that broke things, it shows here. Filter `success: false` for failed operations.

## Workflow — Remediate

Once you've identified the failing container and probable cause:

6. **Get full context on the offender** — `get_container` with the ID, `get_container_logs` for its recent output, `health_check_status` to see probe state and restart count.
7. **Restart it** — if it's wedged but the image is fine: `kill_container` then redeploy, OR if it's part of a service, the health check's auto-restart may already be handling it (check `health_check_status` restart count). For a hard reset without losing the definition, there's no "restart" tool — kill + recreate via the original template/deploy.
8. **Roll back a bad deploy** — if events/audit show a recent deploy preceded the breakage: `rollback_deploy` (admin).
9. **Scale out under load** — if metrics show saturation: `scale_set` to add replicas.
10. **Communicate** — `notify_send` to the on-call channel with a summary.

## Identifying Which Container Is Failing

When the user says "the API is down" but there are many containers:

1. `logs_aggregate` `{"since_lines": 200}` — the container with the highest error count is your primary suspect.
2. `health_check_list` — any container showing `failing` or high `restart_count` is down or flapping.
3. `events_list` `{"severity": "critical", "since_minutes": 30}` — recent critical events point at the problem.
4. `resource_list_usage` — a container pegged at 100% CPU or near memory limit is likely thrashing.

Triangulate: the container that appears in 2+ of these is your culprit.

## Finding Root Cause

- **Sudden errors after a deploy** → check `audit_query` for `deploy_*` / `update_code` calls just before the incident → likely a bad image/code change → `rollback_deploy`.
- **OOM / memory climb** → `metrics_query` for memory on the container → if it's a steady climb, it's a leak; if it's a spike, it's load → `scale_set` or `resource_set_limits` to raise the limit.
- **Disk full** → `gc_disk_usage` → if reclaimable space exists, `gc_prune_images` + `gc_prune_volumes` → also check if a DB volume is growing unboundedly.
- **Can't reach a dependency** → the failing container's logs will show connection refused / timeout to another service → `service_resolve` to check discovery → the dependency may be down (recurse this workflow on it).
- **Cert expired** → `cert_list` → if a route's cert is expired, traffic fails with TLS errors → `cert_renew`.
- **Node failure** → `list_nodes` for any node in `offline`/`draining` state → containers on it are gone → `ha_state` to check failover.

## Decision Points

- **User names a specific container?** Skip straight to `get_container` + `get_container_logs` + `health_check_status` on that ID. Don't waste time on `logs_aggregate`.
- **Widespread outage?** Start with `cluster_health` and `list_nodes` — a node or the cluster itself may be down, not an individual container.
- **Intermittent?** Use longer `since_minutes` windows in `logs_search` and `events_list` to catch the pattern. Check `metrics_query` over time for periodic spikes.
- **Security incident?** `audit_query` is your primary tool — it's tamper-evident (hash-chained). Look for unexpected `success: false` calls, tokens created, secrets accessed, or containers killed by an unexpected source.

## Error Recovery

- **`logs_search` returns nothing** → the containers may not be emitting logs at the searched level, or `since_minutes` is too short. Try a broader pattern and longer window.
- **No metrics available** → `metrics_query` may return empty if Prometheus scraping isn't running. Fall back to `resource_list_usage` for a real-time snapshot.
- **Container keeps restarting (CrashLoopBackoff equivalent)** → `health_check_status` shows high restart count → `get_container_logs` for the crash reason → if it's a config/code issue, `rollback_deploy`; if resource, `resource_set_limits` to raise limits.
- **Can't fix it from logs alone** → `exec_in_container` to poke around inside (`ls`, `cat config`, `curl localhost:port`). This is operator role and the command must pass the allowlist validator. Timeout is hard-capped at 300s (AS-2). For truly untrusted diagnostic scripts, use `secure_sandbox_exec` instead.

## Example Session

> **User:** The API is throwing 500s. Users are complaining. Fix it.
>
> **Agent:**
> 1. `logs_aggregate` `{"since_lines": 200}` → `c-api3` has 47 errors (others have 0–2)
> 2. `logs_search` `{"pattern": "ERROR", "containers": ["c-api3"], "since_minutes": 15, "max_results": 20}`
>    → repeated `FATAL: password authentication failed for user "api"` — DB connection issue
> 3. `events_list` `{"severity": "critical", "since_minutes": 60}}` → `secret_updated: api-db-password` 22 min ago
> 4. `audit_query` `{"since_hours": 1, "tool_name": "secret_set"}}` → confirms a human rotated the DB password but the app's ConfigMap wasn't updated
> 5. `secret_get` `{"name": "api-db-password"}` → new password
> 6. `configmap_update` `{"name": "api-db-conn", "data": {"DATABASE_URL": "postgresql://api:<new-pass>@api-db:5432/api-db"}}`
> 7. `get_container` `{"container_id": "c-api3"}` → confirm restart picked up new config; or redeploy
>
> Root cause: DB password was rotated but the app config wasn't updated. Fixed. Confirm 500s have stopped?
