# Dockerfile for Cube Container — Single Node (Container Mode)
#
# Builds CubeAPI (Rust), CubeMaster + Cubelet (Go), and MCP server (Go)
# into a single image for testing and deployment on low-resource nodes.
#
# Build:
#   docker build -f deploy/container-mode/Dockerfile -t cube-container:latest .
#
# Run (principle of least privilege — use specific capabilities, NOT --privileged):
#   docker run -d --name cube-node \
#     --cap-add SYS_ADMIN \
#     --cap-add NET_ADMIN \
#     --cap-add NET_BIND_SERVICE \
#     --device /dev/fuse \
#     --device /dev/net/tun \
#     --tmpfs /tmp \
#     --tmpfs /run \
#     -v /var/lib/containerd:/var/lib/containerd \
#     -v /var/lib/cube-container:/var/lib/cube-container \
#     -p 3000:3000 \
#     -p 80:80 \
#     -p 443:443 \
#     cube-container:latest
#
# SECURITY NOTE: --privileged grants ALL capabilities and exposes ALL host
# devices. Do NOT use it. The capabilities listed above provide the minimum
# needed for containerd + networking. If you need Docker-in-Docker, connect
# to the host daemon via a read-only socket instead of mounting it RW.

# ---- Stage 1: Build Go components ----
FROM golang:1.25.12@sha256:d7912cedddfa15b2900a8dfb7187df0af5ec2cb424a371139b5b352fd3e6b740 AS go-builder

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
FROM rust:1.77-bookworm@sha256:83101f6985c93e1e6501b3375de188ee3d2cbb89968bcc91611591f9f447bd42 AS rust-builder

WORKDIR /workspace
COPY CubeAPI/ ./CubeAPI/
RUN cd CubeAPI && cargo build --release && cp target/release/cube-api /cubeapi

# ---- Stage 3: Build MCP server (Go) ----
FROM golang:1.25.12@sha256:d7912cedddfa15b2900a8dfb7187df0af5ec2cb424a371139b5b352fd3e6b740 AS mcp-builder

WORKDIR /workspace
COPY mcp-server-go/ ./mcp-server-go/
RUN cd mcp-server-go && CGO_ENABLED=0 go build -o /cube-mcp .

# ---- Stage 4: Runtime ----
FROM ubuntu:22.04@sha256:0e0a0fc6d18feda9db1590da249ac93e8d5abfea8f4c3c0c849ce512b5ef8982

# Install tini for proper PID 1 signal handling + zombie reaping.
# Without tini, containerd child processes become zombies and signal
# forwarding is unreliable, causing graceful shutdown to fail.
RUN apt-get update && apt-get install --no-install-recommends -y \
    tini \
    containerd \
    runc \
    iptables \
    ca-certificates \
    curl \
    && rm -rf /var/lib/apt/lists/*

# Install Caddy binary with checksum verification (supply-chain safety)
RUN curl -sfL "https://github.com/caddyserver/caddy/releases/download/v2.8.4/caddy_2.8.4_linux_amd64.tar.gz" -o /tmp/caddy.tar.gz \
    && echo "a7e8306c54138cf88e371c5ec0caf7baf142ecc1d60a30897dfb67d65d3748c8 /tmp/caddy.tar.gz" | sha256sum -c - \
    && tar xz -C /usr/local/bin caddy --warning=no-unknown-key -f /tmp/caddy.tar.gz \
    && rm /tmp/caddy.tar.gz \
    && caddy version

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
RUN mkdir -p /etc/caddy /var/log/cube-container
RUN echo 'import /etc/caddy/cube-routes.caddy' > /etc/caddy/Caddyfile

# Copy production Caddyfile
COPY deploy/container-mode/Caddyfile /etc/caddy/Caddyfile

# Entrypoint script
COPY deploy/container-mode/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# Do NOT EXPOSE 8080 — MCP HTTP is internal-only, proxied by Caddy
EXPOSE 3000 80 443

# Health check via Caddy ONLY (the public entry point).
# If Caddy is down, the container must be reported unhealthy so
# orchestrators restart it — do NOT fallback to the raw 8080 port.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -sf http://localhost:80/health || exit 1

# tini as PID 1: proper signal forwarding + zombie reaping
ENTRYPOINT ["/usr/bin/tini", "--", "/entrypoint.sh"]
