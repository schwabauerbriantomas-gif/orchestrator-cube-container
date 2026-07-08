# cube-agent

The in-VM guest agent for Cube Sandbox. It runs as PID 1 (init) inside each MicroVM and manages the full container lifecycle within the sandbox.

## Upstream

cube-agent is derived from the **[kata-containers/kata-containers](https://github.com/kata-containers/kata-containers)** agent (`src/agent/`), originally authored by the Kata Containers community and licensed under [Apache-2.0](../LICENSE).

Cube-specific modifications include:
- Replaced the Kata runtime protocol with the Cube ttrpc API (`libs/protocols/`)
- Added `cube/` sub-crate for Cube-specific sandbox extensions (custom device management, configuration handling)
- Adapted vsock port assignments and boot flow to match the Cube hypervisor and shim

The original Kata Containers copyright notices and license headers are preserved in all modified source files.

## Role in the System

```
Host
┌────────────────────────────────┐
│  containerd-shim-cube-rs       │
│       │  ttrpc over vsock      │
└───────┼────────────────────────┘
        │  (vsock channel)
   MicroVM boundary
        │
┌───────▼────────────────────────┐
│  cube-agent  (PID 1 / init)    │
│       │                        │
│  ┌────▼──────────────────────┐ │
│  │  container workload       │ │
│  └───────────────────────────┘ │
└────────────────────────────────┘
```

cube-agent is packaged into the guest image at build time (as `/sbin/init`). When the MicroVM boots, the agent starts immediately and:

1. **Initialises the guest environment** — mounts filesystems, sets up namespaces, configures networking via vsock/netlink.
2. **Listens for ttrpc commands** — exposes the Cube agent API over a vsock channel; the shim (`containerd-shim-cube-rs`) connects to this channel to drive the agent.
3. **Manages container lifecycle** — handles `CreateContainer / StartContainer / ExecProcess / SignalProcess / RemoveContainer` requests, delegating to `rustjail` for OCI-compliant container execution.
4. **Forwards I/O** — proxies container stdio streams back to the shim over vsock.
5. **Exposes metrics** — exports Prometheus-compatible metrics for guest CPU, memory, and container health.

## Repository Layout

```
agent/
├── src/             # Agent binary (main entry point, ttrpc server, sandbox/container state)
│   ├── main.rs      # Startup, vsock listener, ttrpc server init
│   ├── rpc.rs       # ttrpc service handlers (implements the Cube agent API)
│   ├── sandbox.rs   # Sandbox state management
│   ├── mount.rs     # Filesystem mount handling
│   ├── network.rs   # Guest network configuration
│   └── ...
├── rustjail/        # OCI container runtime primitives (namespaces, cgroups, seccomp)
├── cube/            # Cube-specific extensions (device model, config)
├── libs/
│   ├── protocols/   # ttrpc API definitions (protobuf) and generated Rust bindings
│   ├── oci/         # OCI spec types
│   ├── logging/     # slog-based logging setup
│   └── safe-path/   # Safe filesystem path utilities
├── vsock-exporter/  # Prometheus metrics exporter over vsock
├── bootstrap.sh     # One-time musl toolchain setup
└── build.sh         # Release build script (musl static binary)
```

## Build

cube-agent is built as a **statically linked musl binary** so it can run as `/sbin/init` without any host library dependencies.

### Prerequisites

```bash
# Install the musl target and bootstrap system dependencies (run once)
arch=$(uname -m)
rustup target add "${arch}-unknown-linux-musl"
sudo ln -s /usr/bin/g++ /bin/musl-g++
sudo bash bootstrap.sh
```

### Compile

```bash
bash build.sh
```

The output binary is placed at `target/<arch>-unknown-linux-musl/release/cube-agent`.

### Build via Docker (recommended for CI)

```bash
make all-docker
```

## API

cube-agent exposes a [ttrpc](https://github.com/containerd/ttrpc) API over vsock. The protocol is defined in protobuf files under `libs/protocols/protos/` and is shared with the Cube runtime (`CubeShim`).

To regenerate protocol bindings after changing `.proto` files:

```bash
# Rust bindings (auto-generated at build time by build.rs)
cargo build

# Go bindings (used by the Cube runtime side)
make generate-protocols   # requires protoc in $PATH
```

To install `protoc`:

```bash
# Debian/Ubuntu
sudo apt-get install -y protobuf-compiler

# Fedora/CentOS/RHEL
sudo dnf install -y protobuf-compiler
```

## License

Apache-2.0 — see [LICENSE](../LICENSE) for details.

This component incorporates code from the [Kata Containers](https://github.com/kata-containers/kata-containers) project, © The Kata Containers Authors, licensed under Apache-2.0.
