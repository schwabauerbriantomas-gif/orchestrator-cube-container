# Dockerfile for Cube Container — Single Node (Container Mode)
#
# Builds CubeAPI (Rust), CubeMaster + Cubelet (Go), and MCP server (Python)
# into a single image for testing and deployment on low-resource nodes.
#
# Build:
#   docker build -f deploy/container-mode/Dockerfile -t cube-container:latest .
#
# Run:
#   docker run -d --name cube-node \
#     --privileged \
#     -v /var/lib/containerd:/var/lib/containerd \
#     -v /var/lib/cube-container:/var/lib/cube-container \
#     -p 3000:3000 \
#     -p 12088:12088 \
#     cube-container:latest

# ---- Stage 1: Build Go components ----
FROM golang:1.25 AS go-builder

WORKDIR /workspace

COPY Cubelet/ ./Cubelet/
COPY CubeMaster/ ./CubeMaster/
COPY cubelog/ ./cubelog/

# Build Cubelet (no CGO — container mode)
RUN cd Cubelet && CGO_ENABLED=0 go build -o /cubelet ./cmd/cubelet
RUN cd Cubelet && CGO_ENABLED=0 go build -o /cubecli ./cmd/cubecli

# Build CubeMaster
RUN cd CubeMaster && CGO_ENABLED=0 go build -o /cubemaster ./cmd/cubemaster
RUN cd CubeMaster && CGO_ENABLED=0 go build -o /cubemastercli ./cmd/cubemastercli

# ---- Stage 2: Build CubeAPI (Rust) ----
FROM rust:1.77-bookworm AS rust-builder

WORKDIR /workspace
COPY CubeAPI/ ./CubeAPI/
RUN cd CubeAPI && cargo build --release && cp target/release/cube-api /cubeapi

# ---- Stage 3: Build MCP server ----
FROM python:3.12-slim AS mcp-builder

WORKDIR /workspace
COPY mcp-server/ ./mcp-server/
RUN cd mcp-server && pip install --no-cache-dir mcp httpx && pip install -e .

# ---- Stage 4: Runtime ----
FROM ubuntu:22.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    containerd \
    runc \
    iptables \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Binaries from builders
COPY --from=go-builder /cubelet /usr/local/bin/cubelet
COPY --from=go-builder /cubecli /usr/local/bin/cubecli
COPY --from=go-builder /cubemaster /usr/local/bin/cubemaster
COPY --from=go-builder /cubemastercli /usr/local/bin/cubemastercli
COPY --from=rust-builder /cubeapi /usr/local/bin/cubeapi
COPY --from=mcp-builder /usr/local/lib/python3.12/site-packages/ /usr/local/lib/python3.12/site-packages/
COPY --from=mcp-builder /workspace/mcp-server/ /opt/cube-mcp/

# Config
COPY deploy/container-mode/config.toml /etc/cube-container/config.toml

# Entrypoint script
COPY deploy/container-mode/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

EXPOSE 3000 12088

ENTRYPOINT ["/entrypoint.sh"]
