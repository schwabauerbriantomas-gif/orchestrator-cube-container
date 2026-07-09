# Skill: Security Hardening

**Triggers:** When the user asks to lock down / harden / secure the cluster or a service, set up service accounts / API tokens, store credentials safely, add alerts for downtime, monitor certificate expiry, configure alert notifications, or "make this production-ready".
**Prerequisites:** A running cluster. For alerts and notifications, decide on the delivery channel (Slack/Discord/Telegram/Email webhook) beforehand.
**RBAC Required:** admin (for token creation, secrets, routes, network policies, notification channels); operator (for health checks, alert rules, notify_send)

## Workflow

1. **Create a service account token** — `auth_create_token` with `role` (viewer|operator|admin — pick the **minimum** necessary), `label`
   - The secret is returned **only once**. Store it immediately in a password manager or vault. For service accounts (apps/agents talking to the API), prefer `operator` over `admin` unless the service needs admin-only tools.
2. **Store credentials encrypted** — `secret_set` with `name`, `value`
   - For any credential the app or cluster needs (DB passwords, third-party API keys, registry tokens). Stored AES-256-GCM encrypted at rest. **Never** put secrets in env_vars of `create_container` directly — use secrets + ConfigMap references.
3. **Configure health checks** — `health_check_set` on every production container
   - A health probe enables auto-restart and is a prerequisite for meaningful `container_down` alerts (the alert fires when the container is down AND can't be auto-restarted).
4. **Add downtime alerts** — `alert_rule_add` with `type: "container_down"`, `container_id`, `severity: "critical"`
   - Also consider `cpu_high`, `mem_high`, `disk_high` (with `threshold` %, `node_id`) for capacity alerts.
5. **Monitor certificate expiry** — `cert_list` to see all TLS certs and their expiry/renewal status
   - Caddy auto-renews within 30 days of expiry, but verify the list periodically. If a cert shows as expired or renewal failing, `cert_renew` (reloads Caddy to trigger renewal).
6. **Set up alert delivery** — `notify_channel_add` with `name`, `type` (slack|discord|telegram|email), and the type-specific args (`webhook_url` for Slack/Discord, `bot_token`+`chat_id` for Telegram, `email_to`+`smtp_host` for email)
   - Alerts fire to webhook URLs. Without a channel configured, `container_down` alerts have nowhere to go. Test with `alert_test`.
7. **Lock down network access (optional, admin)** — `add_network_policy` with `action: "deny"` between containers that shouldn't talk; `add_port_mapping` only where truly needed (avoid exposing DBs publicly).
8. **Enable inter-node TLS (multi-node clusters)** — set `CUBE_DOCKER_TLS=true` for encrypted remote Docker connections (AS-4). Plaintext emits a stderr warning and should never be used in production.
9. **Verify webhook auth (if webhooks are enabled)** — webhook secrets are accepted ONLY via the `X-Git-Token` header (AS-5). Ensure CI/CD pipelines send the token in the header, not as a query param.

## Decision Points

- **Token role?** Apply least privilege. Read-only dashboards/agents → `viewer`. Deploy/scaling actions → `operator`. Cluster administration → `admin`. You can always `auth_revoke_token` and re-create at a higher role if needed.
- **Secret vs. ConfigMap?** If the value is sensitive (password, key, token) → `secret_set`. If it's non-sensitive config (feature flags, connection host/port, log level) → `configmap_create`. Mixing them up is a common mistake.
- **Which alert types?** Production minimum: `container_down` (critical) for every user-facing container. Add `cpu_high`/`mem_high`/`disk_high` (warning, threshold ~80%) per node once you have steady-state traffic.
- **Alert severity?** `container_down` on a prod user-facing service → `critical`. Resource pressure → `warning`. Informational → `info`. Over-escalating causes alert fatigue; under-escalating means you miss outages.
- **Network policy needed?** Only if containers from different services/environments shouldn't reach each other. Default is open within the cluster. Add deny rules for DBs that should only be reachable by their app.
- **Inter-node TLS?** Set `CUBE_DOCKER_TLS=true` in any multi-node production deployment (AS-4). Without it, remote Docker API calls are plaintext. The server prints a warning, but it won't refuse to start.
- **`exec_in_container` timeout?** Hard-capped at 300s (AS-2). If a command needs more time, split it or run it as a job. For truly untrusted code, use `secure_sandbox_exec` instead — its security boundary is KVM isolation, not command filtering (AS-1).
- **Audit trail integrity?** The audit hash chain uses HMAC-SHA256 keyed with `CUBE_SECRETS_KEY` (AS-7). Always set this env var in production — without it, the chain falls back to plain SHA-256, which an attacker could recompute after tampering.

## Error Recovery

- **`auth_create_token` secret lost** → it was shown once. You cannot retrieve it again (`auth_list_tokens` shows metadata only). `auth_revoke_token` the compromised/lost token and create a new one.
- **`secret_set` fails** → check `CUBE_SECRETS_KEY` or `CUBE_SECRETS_PASSPHRASE` is configured. Without an encryption key, secrets can't be stored.
- **`alert_rule_add` fires but no notification arrives** → no channel configured, or the webhook URL is wrong. `notify_channel_list` to verify, then `alert_test` to fire a test alert and confirm delivery.
- **`cert_list` shows an expired cert** → `cert_renew` immediately. If renewal still fails, the domain's DNS may have changed or the server IP rotated (Caddy needs the domain to resolve here for HTTP-01 challenge). Check DNS.
- **`notify_channel_add` for Telegram fails** → the bot token must be a valid format; the validator checks for SSRF/spoofing. Re-create the bot token with BotFather if malformed.
- **Token compromised** → `auth_revoke_token` immediately (disables it), then audit with `audit_query` `{"since_hours": 24, "role": "<compromised-role>"}` to see what the token did.

## Example Session

> **User:** Make the cluster production-ready. I want alerts if anything goes down, and a Slack channel for notifications.
>
> **Agent:**
> 1. `auth_create_token` `{"role": "operator", "label": "deploy-bot"}` → token secret (store now)
> 2. `secret_set` `{"name": "registry-token", "value": "<registry-pull-token>"}` → stored encrypted
> 3. For each prod container:
>    - `health_check_set` `{"container_id": "c-api1", "type": "http", "http_port": 8000, "http_path": "/health", "failure_threshold": 3}`
>    - `alert_rule_add` `{"id": "api1-down", "name": "API down", "type": "container_down", "container_id": "c-api1", "severity": "critical"}`
> 4. `alert_rule_add` `{"id": "node-cpu", "name": "High CPU", "type": "cpu_high", "node_id": "node-a", "severity": "warning", "threshold": 80}`
> 5. `cert_list` → all certs valid, earliest expiry in 45 days (auto-renewal will handle it)
> 6. `notify_channel_add` `{"name": "oncall-slack", "type": "slack", "webhook_url": "https://hooks.slack.com/services/..."}}` → channel registered
> 7. `alert_test` `{"rule_id": "api1-down"}` → test alert delivered to Slack ✓
>
> Cluster is hardened: least-privilege deploy token, encrypted credentials, health probes + auto-restart, downtime + capacity alerts, cert monitoring, and Slack delivery verified.
