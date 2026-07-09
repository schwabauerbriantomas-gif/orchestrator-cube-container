# Skill: Untrusted Code Execution

**Triggers:** "run this code safely", "execute untrusted script", "isolate this code", "test this in a sandbox"
**Prerequisites:** Cube backend running (CUBE_BACKEND=cube), KVM available on host
**RBAC Required:** admin (to create), operator (to exec), viewer (to list)

## When to Use Secure Sandbox vs Regular Container

| Situation | Use |
|-----------|-----|
| Your own code, trusted repo | Regular container (`create_container`) |
| User-submitted code, CI/CD for external repos | **Secure sandbox** (`secure_sandbox_create`) |
| Code from untrusted sources, malware analysis | **Secure sandbox** with `network_disabled=true` |
| Need to run code that might be malicious | **Secure sandbox** with `egress_allowlist` + `max_lifetime_seconds` |

## Workflow

### Step 1 — Create a template for the untrusted code

```
create_template(
  image="python:3.12-slim",
  expose_ports=[],
  start_cmd="sleep infinity"
)
```

### Step 2 — Create the secure sandbox

**Most restrictive (malware analysis):**
```
secure_sandbox_create(
  template_id="<id>",
  memory_mb=512,
  cpu_count=1.0,
  network_disabled=true,           // no network at all
  max_lifetime_seconds=300          // auto-pause after 5 minutes
)
```

**Controlled access (CI/CD for external repo):**
```
secure_sandbox_create(
  template_id="<id>",
  memory_mb=1024,
  cpu_count=2.0,
  egress_allowlist=["github.com", "pypi.org", "registry.npmjs.org"],
  max_lifetime_seconds=600
)
```

**With credential vault (code needs API access but mustn't see the key):**
```
secure_sandbox_create(
  template_id="<id>",
  memory_mb=512,
  egress_allowlist=["api.openai.com"],
  credential_vault={
    "api.openai.com": "sk-xxx"    // injected on egress, never in sandbox
  }
)
```

### Step 3 — Snapshot the clean state (before running untrusted code)

```
secure_sandbox_snapshot(sandbox_id="<id>")
// Save the snapshot_id — you can restore to this clean state later
```

### Step 4 — Execute the untrusted code

```
secure_sandbox_exec(
  sandbox_id="<id>",
  command="python /app/user_script.py",
  timeout_seconds=30    // max 300 for untrusted code
)
```

### Step 5 — Analyze results or roll back

If the code modified the sandbox state and you want a clean slate:
```
secure_sandbox_restore(sandbox_id="<id>", snapshot_id="<from step 3>")
```

### Step 6 — Clean up

The sandbox auto-pauses after `max_lifetime_seconds`. To stop immediately:
```
kill_container(container_id="<sandbox_id>")
```

## Egress Control Management

After creating a sandbox, you can dynamically update its network access:

```
// Allow a specific domain
secure_sandbox_egress_add(sandbox_id="<id>", domain="api.github.com", action="allow")

// Block a domain (overrides allowlist)
secure_sandbox_egress_add(sandbox_id="<id>", domain="evil.com", action="block")

// List all rules
secure_sandbox_egress_list(sandbox_id="<id>")

// Remove a rule
secure_sandbox_egress_remove(rule_id="<rule-id>")
```

## Credential Vault — How It Works

```
┌─────────────────┐         ┌──────────────┐         ┌─────────────────┐
│  Secure Sandbox │ ──GET──►│  CubeEgress  │ ──GET──►│  api.openai.com │
│  (untrusted)    │         │  (proxy)     │         │                 │
│                 │ ◄──resp─│  injects key │ ◄──resp─│                 │
│  never sees key │         │  in header   │         │                 │
└─────────────────┘         └──────────────┘         └─────────────────┘
```

The untrusted code calls `https://api.openai.com/v1/chat` normally. CubeEgress
intercepts the request, adds `Authorization: Bearer sk-xxx`, and forwards it.
The key NEVER appears in:
- Sandbox environment variables
- Sandbox filesystem
- Process memory visible to sandbox
- Logs

## Decision Points

- **Does the code need network access?**
  - No → `network_disabled=true` (most secure)
  - Yes, known domains → `egress_allowlist=["domain1", "domain2"]`
  - Yes, unknown domains → Don't use a secure sandbox, block everything

- **Does the code need API keys?**
  - Use `credential_vault` — keys are injected by proxy, invisible to code

- **Worried about resource exhaustion?**
  - Set `memory_mb` and `cpu_count` conservatively
  - Set `max_lifetime_seconds` to auto-pause

- **Need to test something destructive?**
  - `secure_sandbox_snapshot` first, then `secure_sandbox_restore` after

## Error Recovery

- `secure_sandbox_create` fails with "requires Cube backend" → set `CUBE_BACKEND=cube`
- `secure_sandbox_exec` times out → increase `timeout_seconds` (max 300)
- `secure_sandbox_exec` fails with exit code ≠ 0 → check `stderr` in response
- CubeEgress vault config fails → sandbox runs without vault, code sees no keys

## Example Session

```
User: I need to test this Python script from a user, I'm not sure if it's safe
Agent: I'll create an isolated KVM sandbox for it.
       [create_template → python:3.12-slim]
       [secure_sandbox_create → network disabled, 5min lifetime]
       Taking a snapshot of the clean state.
       [secure_sandbox_snapshot → saved]
       Running the script in isolation.
       [secure_sandbox_exec → "python /app/script.py"]
       Result:
       - stdout: "Hello, World!"
       - exit code: 0
       - duration: 1.2s
       The script ran safely. It printed "Hello, World!" and exited cleanly.
       No network access was available. The sandbox will auto-pause in 5 minutes.
       Want me to restore the clean snapshot or let it expire?
```
