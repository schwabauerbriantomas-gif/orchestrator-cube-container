// Package main: backend abstraction for auto-detecting the container runtime.
//
// The MCP server talks to containers through a ContainerBackend interface.
// At startup, newBackend() probes for a Docker daemon socket. If found,
// DockerClient is used (lower latency, battle-tested). If not, it falls back
// to CubeClient → CubeAPI (lighter, for edge 4GB nodes without Docker).
//
// The active backend is exposed to the model via the backend_info MCP tool,
// so the model always knows which runtime it is operating on.
package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

// ContainerBackend abstracts the container runtime so the MCP server,
// deploy manager, and backup manager work with Docker or Cube without
// knowing which one is active.
type ContainerBackend interface {
	// BackendName returns "docker" or "cube".
	BackendName() string

	// Endpoint returns a human-readable connection string:
	//   docker → "/var/run/docker.sock"
	//   cube   → "http://localhost:3000"
	Endpoint() string

	// ---- Cluster ----
	Health() (interface{}, error)
	ClusterOverview() (interface{}, error)
	ClusterVersions() (interface{}, error)
	ListNodes() (interface{}, error)
	GetNode(nodeID string) (interface{}, error)

	// ---- Container lifecycle ----
	ListSandboxes(state string, limit int) (interface{}, error)
	GetSandbox(sandboxID string) (interface{}, error)
	CreateSandbox(templateID string, memoryMB int, cpuCount float64, envVars, metadata map[string]interface{}) (interface{}, error)
	KillSandbox(sandboxID string) (interface{}, error)
	RestartSandbox(sandboxID string) (interface{}, error)
	PauseSandbox(sandboxID string) (interface{}, error)
	ResumeSandbox(sandboxID string) (interface{}, error)
	GetSandboxLogs(sandboxID string, limit int) (interface{}, error)

	// ---- Templates ----
	ListTemplates() (interface{}, error)
	GetTemplate(templateID string) (interface{}, error)
	CreateTemplateFromImage(image string, exposePorts []int, writableLayerSizeGB int, mounts []map[string]interface{}, envVars map[string]interface{}, startCmd string) (interface{}, error)

	// ---- Exec ----
	ExecInSandbox(sandboxID, command string, timeout int) (interface{}, error)
}

// dockerSocketAvailable returns true if a Docker daemon is reachable at the
// configured socket path. It performs a fast connection probe (200ms timeout).
func dockerSocketAvailable() bool {
	socket := envOr("DOCKER_SOCKET", "/var/run/docker.sock")

	// Fast path: stat the socket file.
	info, err := os.Stat(socket)
	if err != nil || info.Mode()&os.ModeSocket == 0 {
		return false
	}

	// Verify a daemon is actually listening (not just a stale socket file).
	conn, err := net.DialTimeout("unix", socket, 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// newBackend auto-detects the best available container runtime:
//   1. If CUBE_BACKEND=docker is set, force Docker.
//   2. If CUBE_BACKEND=cube is set, force Cube.
//   3. If /var/run/docker.sock exists and a daemon responds, use Docker.
//   4. Otherwise, fall back to CubeClient → CubeAPI.
//
// This gives zero-config: Docker where Docker exists, Cube everywhere else.
func newBackend() ContainerBackend {
	forced := os.Getenv("CUBE_BACKEND")

	switch forced {
	case "docker":
		fmt.Fprintf(os.Stderr, "[cube-mcp] backend forced → docker\n")
		return newDockerClient()
	case "cube":
		fmt.Fprintf(os.Stderr, "[cube-mcp] backend forced → cube\n")
		return newCubeClient()
	}

	// Auto-detect: prefer Docker, fall back to Cube.
	if dockerSocketAvailable() {
		fmt.Fprintf(os.Stderr, "[cube-mcp] backend auto-detected → docker\n")
		return newDockerClient()
	}

	fmt.Fprintf(os.Stderr, "[cube-mcp] backend auto-detected → cube (no Docker socket found)\n")
	return newCubeClient()
}
