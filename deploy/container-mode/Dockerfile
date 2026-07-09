# Dockerfile for Cube Container — Single Node (Container Mode)
#
# Builds CubeAPI (Rust), CubeMaster + Cubelet (Go), and MCP server (Go)
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
#     -v /var/run/docker.sock:/var/run/docker.sock \
#     -p 3000:3000 \
#     -p 8080:8080 \
#     -p 80:80 \
#     -p 443:443 \
#     cube-container:latest

# ---- Stage 1: Build Go components ----
FROM golang:1.25.12 AS go-builder

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

# ---- Stage 3: Build MCP server (Go) ----
FROM golang:1.25.12 AS mcp-builder

WORKDIR /workspace
COPY mcp-server-go/ ./mcp-server-go/
RUN cd mcp-server-go && CGO_ENABLED=0 go build -o /cube-mcp .

# ---- Stage 4: Runtime ----
FROM ubuntu:26.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    containerd \
    runc \
    iptables \
    ca-certificates \
    git \
    && rm -rf /var/lib/apt/lists/*

# Install Caddy binary with checksum verification (supply-chain safety)
RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates \
    && curl -sfL "https://github.com/caddyserver/caddy/releases/download/v2.8.4/caddy_2.8.4_linux_amd64.tar.gz" -o /tmp/caddy.tar.gz \
    && echo "2bc233c323352d1aaa95f6b6d8b13c300b6e6d28c6d84b4f5dcc5bf1e86c4d4a /tmp/caddy.tar.gz" | sha256sum -c - \
    && tar xz -C /usr/local/bin caddy --warning=no-unknown-key -f /tmp/caddy.tar.gz \
    && rm /tmp/caddy.tar.gz \
    && caddy version \
    && rm -rf /var/lib/apt/lists/*

# Binaries from builders
COPY --from=go-builder /cubelet /usr/local/bin/cubelet
COPY --from=go-builder /cubecli /usr/local/bin/cubecli
COPY --from=go-builder /cubemaster /usr/local/bin/cubemaster
COPY --from=go-builder /cubemastercli /usr/local/bin/cubemastercli
COPY --from=rust-builder /cubeapi /usr/local/bin/cubeapi
COPY --from=mcp-builder /cube-mcp /usr/local/bin/cube-mcp

# Config
COPY deploy/container-mode/config.toml /etc/cube-container/config.toml

# Caddy base config — imports dynamically generated route fragments
RUN mkdir -p /etc/caddy
RUN echo 'import /etc/caddy/cube-routes.caddy' > /etc/caddy/Caddyfile

# Copy production Caddyfile
COPY deploy/container-mode/Caddyfile /etc/caddy/Caddyfile

# Entrypoint script
COPY deploy/container-mode/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Do NOT EXPOSE 8080 — MCP HTTP is internal-only, proxied by Caddy
EXPOSE 3000 80 443

# Health check via Caddy (public health endpoint)
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -sf http://localhost:80/health || curl -sf http://localhost:8080/health || exit 1

ENTRYPOINT ["/entrypoint.sh"]
