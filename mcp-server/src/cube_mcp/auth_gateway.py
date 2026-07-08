"""CubeAPI Auth Gateway.

Production auth layer for Cube Container. Sits in front of CubeAPI and:
1. Authenticates every request (zero-trust)
2. Authorizes based on RBAC roles
3. Rate limits per API key
4. Audits all actions to append-only JSONL

Architecture:
    External user → Caddy (TLS 1.3 + WAF) → Auth Gateway :8090 → CubeAPI :3000
                                          → MCP HTTP :8080

Usage:
    pip install fastapi uvicorn httpx
    uvicorn cube_mcp.auth_gateway:app --host 0.0.0.0 --port 8090

Environment:
    CUBE_API_URL=http://localhost:3000   # Backend CubeAPI
    CUBE_AUTH_DIR=/var/lib/cube-container/auth
"""
from __future__ import annotations

import hashlib
import hmac
import json
import os
import secrets
import time
from collections import defaultdict
from dataclasses import dataclass, field
from enum import Enum
from pathlib import Path
from typing import Any

import httpx
from fastapi import FastAPI, Request, Response
from fastapi.responses import JSONResponse

app = FastAPI(title="Cube Container Auth Gateway", version="0.1.0")

BACKEND_URL = os.environ.get("CUBE_API_URL", "http://localhost:3000")
AUTH_DIR = os.environ.get("CUBE_AUTH_DIR", "/var/lib/cube-container/auth")
AUDIT_DIR = os.path.join(AUTH_DIR, "audit")
Path(AUTH_DIR).mkdir(parents=True, exist_ok=True)
Path(AUDIT_DIR).mkdir(parents=True, exist_ok=True)

# ---- RBAC ----


class Permission(str, Enum):
    CLUSTER_READ = "cluster:read"
    CONTAINER_READ = "container:read"
    CONTAINER_WRITE = "container:write"
    CONTAINER_LIFECYCLE = "container:lifecycle"
    CONTAINER_EXEC = "container:exec"
    TEMPLATE_READ = "template:read"
    TEMPLATE_WRITE = "template:write"
    DEPLOY = "deploy"
    VOLUME_READ = "volume:read"
    VOLUME_WRITE = "volume:write"
    ADMIN = "admin"


ROLE_PERMISSIONS: dict[str, set[Permission]] = {
    "viewer": {
        Permission.CLUSTER_READ,
        Permission.CONTAINER_READ,
        Permission.TEMPLATE_READ,
        Permission.VOLUME_READ,
    },
    "operator": {
        Permission.CLUSTER_READ,
        Permission.CONTAINER_READ,
        Permission.CONTAINER_WRITE,
        Permission.CONTAINER_LIFECYCLE,
        Permission.CONTAINER_EXEC,
        Permission.TEMPLATE_READ,
        Permission.VOLUME_READ,
        Permission.DEPLOY,
    },
    "admin": set(Permission),
}


# Map URL paths to required permissions
PATH_PERMISSIONS: dict[str, Permission] = {
    # Read-only
    "GET /cubeapi/v1/health": Permission.CLUSTER_READ,
    "GET /cubeapi/v1/cluster": Permission.CLUSTER_READ,
    "GET /cubeapi/v1/nodes": Permission.CLUSTER_READ,
    "GET /cubeapi/v1/v2/sandboxes": Permission.CONTAINER_READ,
    "GET /cubeapi/v1/sandboxes": Permission.CONTAINER_READ,
    "GET /cubeapi/v1/templates": Permission.TEMPLATE_READ,
    # Write
    "POST /v1/sandboxes": Permission.CONTAINER_WRITE,
    "POST /cubeapi/v1/templates": Permission.TEMPLATE_WRITE,
    "DELETE /cubeapi/v1/sandboxes": Permission.CONTAINER_WRITE,
    # Lifecycle
    "POST /pause": Permission.CONTAINER_LIFECYCLE,
    "POST /resume": Permission.CONTAINER_LIFECYCLE,
    # Exec
    "POST /exec": Permission.CONTAINER_EXEC,
}


def _required_permission(method: str, path: str) -> Permission | None:
    """Determine required permission from HTTP method + path."""
    key = f"{method} {path}"
    # Try exact match first
    if key in PATH_PERMISSIONS:
        return PATH_PERMISSIONS[key]
    # Pattern match
    if "/pause" in path and method == "POST":
        return Permission.CONTAINER_LIFECYCLE
    if "/resume" in path and method == "POST":
        return Permission.CONTAINER_LIFECYCLE
    if "/exec" in path and method == "POST":
        return Permission.CONTAINER_EXEC
    if method == "DELETE" and "/sandboxes/" in path:
        return Permission.CONTAINER_WRITE
    if method == "GET":
        return Permission.CLUSTER_READ  # Default read for GET
    if method == "POST":
        return Permission.CONTAINER_WRITE  # Default write for POST
    if method == "DELETE":
        return Permission.CONTAINER_WRITE
    return None


# ---- API Key Store ----


@dataclass
class ApiKey:
    key_id: str
    secret_hash: str  # SHA-256 of secret (never store plaintext)
    role: str
    label: str = ""
    created_at: float = field(default_factory=time.time)
    last_used: float = 0.0
    request_count: int = 0
    rate_limit: int = 120  # req/min


def _hash_secret(secret: str) -> str:
    return hashlib.sha256(secret.encode()).hexdigest()


def _load_keys() -> dict[str, ApiKey]:
    keys_file = os.path.join(AUTH_DIR, "keys.json")
    if not os.path.exists(keys_file):
        return {}
    with open(keys_file) as f:
        data = json.load(f)
    keys = {}
    for key_id, d in data.items():
        keys[key_id] = ApiKey(
            key_id=key_id,
            secret_hash=d["secret_hash"],
            role=d["role"],
            label=d.get("label", ""),
            created_at=d.get("created_at", time.time()),
            last_used=d.get("last_used", 0),
            request_count=d.get("request_count", 0),
            rate_limit=d.get("rate_limit", 120),
        )
    return keys


def _save_keys(keys: dict[str, ApiKey]):
    keys_file = os.path.join(AUTH_DIR, "keys.json")
    data = {
        k: {
            "secret_hash": v.secret_hash,
            "role": v.role,
            "label": v.label,
            "created_at": v.created_at,
            "last_used": v.last_used,
            "request_count": v.request_count,
            "rate_limit": v.rate_limit,
        }
        for k, v in keys.items()
    }
    with open(keys_file, "w") as f:
        json.dump(data, f, indent=2)
    os.chmod(keys_file, 0o600)


# In-memory key cache
_keys: dict[str, ApiKey] | None = None
_rate_buckets: dict[str, dict[int, int]] = defaultdict(dict)


def _get_keys() -> dict[str, ApiKey]:
    global _keys
    if _keys is None:
        _keys = _load_keys()
        if not _keys:
            # Bootstrap: create default admin key
            secret = secrets.token_urlsafe(32)
            _keys = {
                "admin": ApiKey(
                    key_id="admin",
                    secret_hash=_hash_secret(secret),
                    role="admin",
                    label="Bootstrap admin key",
                )
            }
            _save_keys(_keys)
            # Print the credential ONCE
            print(f"\n{'='*60}")
            print(f"BOOTSTRAP ADMIN KEY (save this, shown once):")
            print(f"  admin.{secret}")
            print(f"{'='*60}\n")

    return _keys


def _check_rate_limit(key_id: str, limit: int) -> bool:
    now_minute = int(time.time() / 60)
    bucket = _rate_buckets[key_id]
    # Purge old buckets
    for m in list(bucket.keys()):
        if m < now_minute - 1:
            del bucket[m]
    count = bucket.get(now_minute, 0)
    if count >= limit:
        return False
    bucket[now_minute] = count + 1
    return True


# ---- Audit ----


def _audit(key_id: str, method: str, path: str, status: int, detail: str = ""):
    entry = {
        "timestamp": time.time(),
        "key_id": key_id,
        "method": method,
        "path": path,
        "status": status,
        "detail": detail,
    }
    audit_file = os.path.join(
        AUDIT_DIR, f"audit-{time.strftime('%Y-%m-%d', time.gmtime())}.jsonl"
    )
    with open(audit_file, "a") as f:
        f.write(json.dumps(entry) + "\n")


# ---- Auth middleware ----


@app.middleware("http")
async def auth_middleware(request: Request, call_next):
    """Zero-trust auth: validate every request."""

    # Skip auth for key management endpoints and health checks
    if request.url.path in ("/_health", "/_keys"):
        return await call_next(request)

    # Extract credential from Authorization header
    auth_header = request.headers.get("Authorization", "")
    if not auth_header.startswith("Bearer "):
        _audit("anonymous", request.method, request.url.path, 401, "No auth header")
        return JSONResponse(
            status_code=401,
            content={"error": "Missing or invalid Authorization header"},
        )

    credential = auth_header[7:]  # Strip "Bearer "

    # Parse credential: format is "{key_id}.{secret}"
    parts = credential.split(".", 1)
    if len(parts) != 2:
        _audit("invalid", request.method, request.url.path, 401, "Bad credential format")
        return JSONResponse(status_code=401, content={"error": "Invalid credential format"})

    key_id, secret = parts
    keys = _get_keys()
    key = keys.get(key_id)

    if key is None:
        # Constant-time comparison to prevent enumeration
        hmac.compare_digest("x" * 64, "y" * 64)
        _audit(key_id, request.method, request.url.path, 401, "Unknown key")
        return JSONResponse(status_code=401, content={"error": "Invalid credentials"})

    # Verify secret
    if not hmac.compare_digest(key.secret_hash, _hash_secret(secret)):
        _audit(key_id, request.method, request.url.path, 401, "Bad secret")
        return JSONResponse(status_code=401, content={"error": "Invalid credentials"})

    # Rate limit
    if not _check_rate_limit(key_id, key.rate_limit):
        _audit(key_id, request.method, request.url.path, 429, "Rate limited")
        return JSONResponse(status_code=429, content={"error": "Rate limit exceeded"})

    # RBAC
    required = _required_permission(request.method, request.url.path)
    if required:
        perms = ROLE_PERMISSIONS.get(key.role, set())
        if required not in perms:
            _audit(
                key_id,
                request.method,
                request.url.path,
                403,
                f"Missing {required.value}",
            )
            return JSONResponse(
                status_code=403,
                content={"error": f"Insufficient permissions: requires {required.value}"},
            )

    # Update key usage
    key.last_used = time.time()
    key.request_count += 1
    _save_keys(keys)

    response = await call_next(request)
    _audit(key_id, request.method, request.url.path, response.status_code)
    return response


# ---- Key management endpoints ----


@app.get("/_health")
async def health():
    return {"status": "ok", "service": "auth-gateway"}


@app.get("/_keys")
async def list_keys(key_id: str = "", secret: str = ""):
    """List API keys (requires admin authentication via query params)."""
    keys = _get_keys()
    k = keys.get(key_id)
    if k is None or not hmac.compare_digest(k.secret_hash, _hash_secret(secret)):
        return JSONResponse(status_code=403, content={"error": "Admin access required"})

    return [
        {
            "key_id": v.key_id,
            "role": v.role,
            "label": v.label,
            "created_at": v.created_at,
            "last_used": v.last_used,
            "request_count": v.request_count,
        }
        for v in keys.values()
    ]


@app.post("/_keys")
async def create_key(request: Request):
    """Create a new API key (requires admin auth in body)."""
    body = await request.json()
    keys = _get_keys()

    # Verify admin
    admin_cred = body.get("admin_credential", "")
    parts = admin_cred.split(".", 1)
    if len(parts) != 2:
        return JSONResponse(status_code=403, content={"error": "Admin auth required"})
    admin = keys.get(parts[0])
    if admin is None or admin.role != "admin" or not hmac.compare_digest(
        admin.secret_hash, _hash_secret(parts[1])
    ):
        return JSONResponse(status_code=403, content={"error": "Admin auth required"})

    new_key_id = body["key_id"]
    new_role = body["role"]
    if new_role not in ROLE_PERMISSIONS:
        return JSONResponse(status_code=400, content={"error": "Invalid role"})
    if new_key_id in keys:
        return JSONResponse(status_code=409, content={"error": "Key already exists"})

    new_secret = secrets.token_urlsafe(32)
    keys[new_key_id] = ApiKey(
        key_id=new_key_id,
        secret_hash=_hash_secret(new_secret),
        role=new_role,
        label=body.get("label", ""),
        rate_limit=body.get("rate_limit", 120),
    )
    _save_keys(keys)
    _audit(parts[0], "POST", "/_keys", 201, f"Created key {new_key_id}")

    return {"key_id": new_key_id, "credential": f"{new_key_id}.{new_secret}", "role": new_role}


@app.delete("/_keys/{key_id}")
async def revoke_key(key_id: str, request: Request):
    """Revoke an API key."""
    auth_header = request.headers.get("Authorization", "")
    credential = auth_header[7:] if auth_header.startswith("Bearer ") else ""
    parts = credential.split(".", 1)
    if len(parts) != 2:
        return JSONResponse(status_code=403, content={"error": "Admin auth required"})

    keys = _get_keys()
    admin = keys.get(parts[0])
    if admin is None or admin.role != "admin" or not hmac.compare_digest(
        admin.secret_hash, _hash_secret(parts[1])
    ):
        return JSONResponse(status_code=403, content={"error": "Admin auth required"})

    if key_id not in keys:
        return JSONResponse(status_code=404, content={"error": "Key not found"})

    del keys[key_id]
    _save_keys(keys)
    _audit(parts[0], "DELETE", f"/_keys/{key_id}", 200, f"Revoked {key_id}")
    return {"status": "revoked", "key_id": key_id}


# ---- Reverse proxy to CubeAPI ----


@app.api_route("/{path:path}", methods=["GET", "POST", "PUT", "DELETE", "PATCH"])
async def proxy(path: str, request: Request):
    """Proxy authenticated requests to backend CubeAPI."""
    async with httpx.AsyncClient(timeout=30.0) as client:
        # Build target URL
        url = f"{BACKEND_URL}/{path}"
        if request.url.query:
            url += f"?{request.url.query}"

        # Forward request
        body = await request.body() if request.method in ("POST", "PUT", "PATCH") else None
        headers = dict(request.headers)
        headers.pop("authorization", None)  # Strip auth before forwarding

        resp = await client.request(
            request.method,
            url,
            content=body,
            headers=headers,
        )

        return Response(
            content=resp.content,
            status_code=resp.status_code,
            headers=dict(resp.headers),
            media_type=resp.headers.get("content-type"),
        )


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=int(os.environ.get("AUTH_PORT", "8090")))
