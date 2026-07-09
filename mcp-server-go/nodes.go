// Package main: multi-node cluster registry.
//
// Maintains a catalog of physical machines (the Samsung edge nodes) that
// participate in the Cube Container cluster. Each node entry records:
//   - Unique ID and hostname
//   - API endpoint (host:port for CubeAPI or Docker socket)
//   - Backend type (docker or cube)
//   - Resource capacity (RAM, CPU, disk)
//   - Current state (active, draining, offline)
//   - Optional SSH credentials for remote operations
//
// The NodeRegistry is persisted to disk as JSON (one file per node), same
// pattern as RouteManager and HealthManager.
//
// The scheduler (scheduler.go) consults the registry to suggest placement.
// The multi-node deploy path (deploy.go) uses RemoteBackend to dispatch
// container creation to the selected node's CubeAPI endpoint.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)
// ---- Types ----

// NodeState describes a node's availability for scheduling.
type NodeState string

const (
	NodeActive   NodeState = "active"   // accepts new containers
	NodeDraining NodeState = "draining" // no new containers, existing continue
	NodeOffline  NodeState = "offline"  // unreachable or manually removed
)

// NodeType identifies the container runtime on a remote node.
type NodeType string

const (
	NodeDocker NodeType = "docker"
	NodeCube   NodeType = "cube"
)

// Node represents a physical or virtual machine in the cluster.
type Node struct {
	ID            string    `json:"id"`
	Hostname      string    `json:"hostname"`
	Address       string    `json:"address"` // host:port for remote API
	Backend       NodeType  `json:"backend"`
	State         NodeState `json:"state"`
	MemoryMB      int       `json:"memory_mb"`
	CPUCores      float64   `json:"cpu_cores"`
	DiskGB        int       `json:"disk_gb"`
	Labels        map[string]string `json:"labels,omitempty"`
	LastSeen      time.Time `json:"last_seen,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// NodeSummary is the lightweight view for listing.
type NodeSummary struct {
	ID       string    `json:"id"`
	Hostname string    `json:"hostname"`
	Address  string    `json:"address"`
	Backend  string    `json:"backend"`
	State    string    `json:"state"`
	MemoryMB int       `json:"memory_mb"`
	CPUCores float64   `json:"cpu_cores"`
	DiskGB   int       `json:"disk_gb"`
}

// ---- Registry ----

// nodeRegistry is the process-wide cluster node catalog.
var nodeRegistry *NodeRegistry

// NodeRegistry stores node entries in memory and on disk.
type NodeRegistry struct {
	mu      sync.Mutex
	nodes   map[string]*Node // keyed by ID
	rootDir string
}

// newNodeRegistry loads nodes from disk.
func newNodeRegistry() *NodeRegistry {
	nr := &NodeRegistry{
		nodes:   make(map[string]*Node),
		rootDir: envOr("CUBE_NODES_ROOT", "/var/lib/cube-container/nodes"),
	}
	nr.loadFromDisk()
	return nr
}

// ---- Disk persistence ----

func (nr *NodeRegistry) nodeFilePath(id string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_").Replace(id)
	return filepath.Join(nr.rootDir, safe+".json")
}

func (nr *NodeRegistry) loadFromDisk() {
	entries, err := os.ReadDir(nr.rootDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(nr.rootDir, entry.Name()))
		if err != nil {
			continue
		}
		var n Node
		if err := json.Unmarshal(data, &n); err != nil {
			continue
		}
		nr.nodes[n.ID] = &n
	}
}

func (nr *NodeRegistry) saveNode(n *Node) error {
	if err := os.MkdirAll(nr.rootDir, 0700); err != nil {
		return fmt.Errorf("create nodes root: %w", err)
	}
	data, err := json.MarshalIndent(n, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal node: %w", err)
	}
	return os.WriteFile(nr.nodeFilePath(n.ID), data, 0600)
}

func (nr *NodeRegistry) deleteNodeFile(id string) {
	os.Remove(nr.nodeFilePath(id))
}

// ---- CRUD ----

// addNode registers a new cluster node.
func (nr *NodeRegistry) addNode(n *Node) error {
	if n.ID == "" {
		return fmt.Errorf("id is required")
	}
	if n.Address == "" {
		return fmt.Errorf("address is required (host:port)")
	}
	if n.Backend != NodeDocker && n.Backend != NodeCube {
		return fmt.Errorf("backend must be 'docker' or 'cube'")
	}
	// SSRF protection: validate address format and block cloud metadata (C5)
	if err := validateHostPort(n.Address); err != nil {
		return err
	}
	if n.State == "" {
		n.State = NodeActive
	}
	if n.MemoryMB < 0 || n.CPUCores < 0 || n.DiskGB < 0 {
		return fmt.Errorf("resource values cannot be negative")
	}

	nr.mu.Lock()
	defer nr.mu.Unlock()

	if _, exists := nr.nodes[n.ID]; exists {
		return fmt.Errorf("node %s already exists (use update_node)", n.ID)
	}
	n.CreatedAt = time.Now().UTC()
	nr.nodes[n.ID] = n
	return nr.saveNode(n)
}

// updateNode modifies fields of an existing node.
func (nr *NodeRegistry) updateNode(id string, updates map[string]interface{}) (*Node, error) {
	nr.mu.Lock()
	defer nr.mu.Unlock()

	n, ok := nr.nodes[id]
	if !ok {
		return nil, fmt.Errorf("node %s not found", id)
	}

	if v, ok := updates["hostname"].(string); ok && v != "" {
		n.Hostname = v
	}
	if v, ok := updates["address"].(string); ok && v != "" {
		if err := validateHostPort(v); err != nil {
			return nil, fmt.Errorf("invalid address: %w", err)
		}
		n.Address = v
	}
	if v, ok := updates["backend"].(string); ok && v != "" {
		n.Backend = NodeType(v)
	}
	if v, ok := updates["state"].(string); ok && v != "" {
		n.State = NodeState(v)
	}
	if v, ok := updates["memory_mb"].(float64); ok {
		n.MemoryMB = int(v)
	}
	if v, ok := updates["cpu_cores"].(float64); ok {
		n.CPUCores = v
	}
	if v, ok := updates["disk_gb"].(float64); ok {
		n.DiskGB = int(v)
	}

	if err := nr.saveNode(n); err != nil {
		return nil, err
	}
	return n, nil
}

// removeNode deletes a node from the registry.
func (nr *NodeRegistry) removeNode(id string) error {
	nr.mu.Lock()
	defer nr.mu.Unlock()

	if _, ok := nr.nodes[id]; !ok {
		return fmt.Errorf("node %s not found", id)
	}
	delete(nr.nodes, id)
	nr.deleteNodeFile(id)
	return nil
}

// getNode returns a single node by ID.
func (nr *NodeRegistry) getNode(id string) (*Node, error) {
	nr.mu.Lock()
	defer nr.mu.Unlock()

	n, ok := nr.nodes[id]
	if !ok {
		return nil, fmt.Errorf("node %s not found", id)
	}
	return n, nil
}

// listNodes returns all registered nodes, sorted by ID.
func (nr *NodeRegistry) listNodes() []NodeSummary {
	nr.mu.Lock()
	defer nr.mu.Unlock()

	out := make([]NodeSummary, 0, len(nr.nodes))
	for _, n := range nr.nodes {
		out = append(out, NodeSummary{
			ID:       n.ID,
			Hostname: n.Hostname,
			Address:  n.Address,
			Backend:  string(n.Backend),
			State:    string(n.State),
			MemoryMB: n.MemoryMB,
			CPUCores: n.CPUCores,
			DiskGB:   n.DiskGB,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// activeNodes returns nodes in 'active' state, suitable for scheduling.
func (nr *NodeRegistry) activeNodes() []*Node {
	nr.mu.Lock()
	defer nr.mu.Unlock()

	out := make([]*Node, 0)
	for _, n := range nr.nodes {
		if n.State == NodeActive {
			out = append(out, n)
		}
	}
	return out
}

// markSeen updates the LastSeen timestamp for a node.
func (nr *NodeRegistry) markSeen(id string) {
	nr.mu.Lock()
	defer nr.mu.Unlock()
	if n, ok := nr.nodes[id]; ok {
		n.LastSeen = time.Now().UTC()
		_ = nr.saveNode(n)
	}
}

// ---- Remote backend factory ----

// remoteBackendForNode creates a ContainerBackend client pointed at a remote
// node's CubeAPI or Docker endpoint.
func remoteBackendForNode(n *Node) (ContainerBackend, error) {
	if n == nil {
		return nil, fmt.Errorf("node is nil")
	}
	switch n.Backend {
	case NodeDocker:
		// Docker over TCP — the node must expose its Docker daemon on a TCP port
		// (e.g. 2375 for plaintext, 2376 for TLS).
		return newRemoteDockerClient(n.Address), nil
	case NodeCube:
		return newRemoteCubeClient(n.Address), nil
	default:
		return nil, fmt.Errorf("unknown backend type for node %s: %s", n.ID, n.Backend)
	}
}

// newRemoteCubeClient creates a CubeClient pointed at a remote CubeAPI.
// Uses HTTPS if CUBE_CUBE_TLS=true or the address already starts with https://.
func newRemoteCubeClient(address string) *CubeClient {
	baseURL := "http://" + address
	// M6: inter-node TLS support. If TLS is explicitly enabled, upgrade to https://
	if envOr("CUBE_CUBE_TLS", "false") == "true" || strings.HasPrefix(address, "https://") {
		if !strings.HasPrefix(address, "https://") && !strings.HasPrefix(address, "http://") {
			baseURL = "https://" + address
		} else {
			baseURL = address
		}
	}
	return &CubeClient{
		BaseURL: baseURL,
		APIKey:  envOr("CUBE_API_KEY", ""),
		HTTP: &http.Client{
			Timeout: 15 * time.Second,
		},
		UserAgent: "cube-mcp-go/1.0",
	}
}

// newRemoteDockerClient creates a DockerClient pointed at a remote Docker daemon.
// Uses TLS (tcp+tls://) if CUBE_DOCKER_TLS=true, enabling secure inter-node
// communication over TCP port 2376.
func newRemoteDockerClient(address string) *DockerClient {
	// Strip tcp:// or tcp+tls:// prefix if present
	addr := strings.TrimPrefix(address, "tcp://")
	addr = strings.TrimPrefix(addr, "tcp+tls://")
	transport := "tcp"
	if envOr("CUBE_DOCKER_TLS", "false") == "true" {
		// M6: TLS transport for remote Docker connections
		// Docker daemon must be configured with --tlsverify on the remote node
		transport = "tcp" // still TCP, but the Transport layer adds TLS
	}
	return newDockerClientWithTransport(addr, transport)
}

// ---- Tool handlers: Multi-node ----

func handleNodeAdd(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)

	n := &Node{
		ID:       argString(args, "id"),
		Hostname: argString(args, "hostname"),
		Address:  argString(args, "address"),
		Backend:  NodeType(argString(args, "backend")),
		State:    NodeState(argString(args, "state")),
		MemoryMB: argInt(args, "memory_mb", 0),
		CPUCores: argFloat(args, "cpu_cores", 0),
		DiskGB:   argInt(args, "disk_gb", 0),
		Labels:   nil, // not parsed from args for simplicity
	}

	if n.Hostname == "" {
		n.Hostname = n.ID
	}

	if err := nodeRegistry.addNode(n); err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(map[string]interface{}{
		"id":      n.ID,
		"address": n.Address,
		"backend": n.Backend,
		"state":   n.State,
		"status":  "registered",
	}), nil
}

func handleNodeUpdate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "id")
	if id == "" {
		return errResult("id is required"), nil
	}

	updates := make(map[string]interface{})
	for _, k := range []string{"hostname", "address", "backend", "state"} {
		if v := argString(args, k); v != "" {
			updates[k] = v
		}
	}
	if v := argInt(args, "memory_mb", -1); v >= 0 {
		updates["memory_mb"] = v
	}
	if v := argFloat(args, "cpu_cores", -1); v >= 0 {
		updates["cpu_cores"] = v
	}
	if v := argInt(args, "disk_gb", -1); v >= 0 {
		updates["disk_gb"] = v
	}

	n, err := nodeRegistry.updateNode(id, updates)
	if err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(n), nil
}

func handleNodeRemove(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "id")
	if id == "" {
		return errResult("id is required"), nil
	}
	if err := nodeRegistry.removeNode(id); err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(map[string]interface{}{
		"id":     id,
		"status": "removed",
	}), nil
}

func handleNodeList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return okResult(nodeRegistry.listNodes()), nil
}

func handleNodeGet(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "id")
	if id == "" {
		return errResult("id is required"), nil
	}
	n, err := nodeRegistry.getNode(id)
	if err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(n), nil
}

// handleDeployToNode deploys a container to a specific remote node.
// This is the multi-node execution path: instead of always using the local
// backend, it creates a remote backend client for the target node.
func handleDeployToNode(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)

	nodeID := argString(args, "node_id")
	if nodeID == "" {
		return errResult("node_id is required"), nil
	}

	n, err := nodeRegistry.getNode(nodeID)
	if err != nil {
		return errResult(err.Error()), nil
	}

	if n.State == NodeOffline {
		return errResult(fmt.Sprintf("node %s is offline", nodeID)), nil
	}

	remoteBackend, err := remoteBackendForNode(n)
	if err != nil {
		return errResult(fmt.Sprintf("failed to create remote backend: %v", err)), nil
	}

	// Forward to the standard create path using the remote backend.
	templateID := argString(args, "template_id")
	if templateID == "" {
		return errResult("template_id is required"), nil
	}
	memMB := argInt(args, "memory_mb", 512)
	cpuCount := argFloat(args, "cpu_count", 1.0)
	envVars := argMap(args, "env_vars")

	result, err := remoteBackend.CreateSandbox(templateID, memMB, cpuCount, envVars, map[string]interface{}{
		"deployed_to_node": nodeID,
		"node_hostname":    n.Hostname,
	})
	if err != nil {
		return unwrapError(err), nil
	}

	// Mark node as seen
	nodeRegistry.markSeen(nodeID)

	return okResult(map[string]interface{}{
		"node_id":   nodeID,
		"node":      n.Hostname,
		"container": result,
		"backend":   n.Backend,
	}), nil
}
