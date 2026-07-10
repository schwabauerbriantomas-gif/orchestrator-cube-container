#!/bin/bash
set -euo pipefail

# Cube Container — Single Node Entrypoint
# Starts containerd, CubeMaster, Cubelet, CubeAPI, MCP server, and Caddy.
#
# All services run as background jobs. We use `wait -n` (bash 4.3+) to
# detect if ANY child exits prematurely — if one dies, the container
# exits so the orchestrator can restart it. This prevents silent
# degradation (e.g., Caddy dies but MCP keeps running unproxied).

echo "[cube-container] Starting containerd..."
containerd &

# Wait for containerd socket
for i in $(seq 1 10); do
    [ -S /run/containerd/containerd.sock ] && break
    sleep 0.5
done

echo "[cube-container] Starting CubeMaster..."
cubemaster --config /etc/cube-container/config.toml &

echo "[cube-container] Starting Cubelet..."
cubelet --config /etc/cube-container/config.toml &

echo "[cube-container] Starting CubeAPI..."
cubeapi &

# Wait for CubeAPI to be ready
for i in $(seq 1 15); do
    curl -sf http://localhost:3000/cubeapi/v1/health >/dev/null 2>&1 && break
    sleep 1
done

echo "[cube-container] CubeAPI ready at :3000"

# Start MCP HTTP server (auth middleware, RBAC, rate limiting, audit chain)
echo "[cube-container] Starting MCP HTTP server..."
cube-mcp --mode http --port 8080 &

# Wait for MCP server to be ready
for i in $(seq 1 10); do
    curl -sf http://localhost:8080/health >/dev/null 2>&1 && break
    sleep 0.5
done

echo "[cube-container] MCP server ready at :8080 (behind Caddy proxy)"

# Start Caddy (TLS, WAF, security headers, rate limiting)
echo "[cube-container] Starting Caddy reverse proxy..."
caddy run --config /etc/caddy/Caddyfile --adapter caddyfile &

echo "[cube-container] All services started."
echo "[cube-container]   CubeAPI:  http://localhost:3000"
echo "[cube-container]   MCP:      http://localhost:8080 (internal)"
echo "[cube-container]   Caddy:    :80/:443 (public TLS)"
echo "[cube-container]   WebUI:    served via Caddy"

# Wait for ANY child to exit. If one dies, log it and exit the container
# so the orchestrator (Docker/k8s) can restart all services cleanly.
# This prevents silent degradation where one critical service is down
# but the container appears healthy.
wait -n 2>/dev/null || wait
EXIT_CODE=$?
echo "[cube-container] WARNING: a child process exited (code=$EXIT_CODE). Shutting down."
exit "$EXIT_CODE"
