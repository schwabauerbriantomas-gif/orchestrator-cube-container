#!/bin/bash
set -e

# Cube Container — Single Node Entrypoint
# Starts containerd, CubeMaster, Cubelet, CubeAPI, and MCP server

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
echo "[cube-container] WebUI at :12088"
echo "[cube-container] MCP server available (stdio)"

# Keep container alive
wait
