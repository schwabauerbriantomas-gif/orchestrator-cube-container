// Package main: Proxmox VE REST API client.
//
// Connects to a Proxmox VE cluster via its REST API (/api2/json/).
// This is a pluggable backend — the MCP server auto-detects whether
// a Proxmox endpoint is configured and routes VM/storage/snapshot
// operations to this client instead of the local libvirt backend.
//
// Authentication: Proxmox uses token-based auth (API token with
// `PVEAPIToken=USER!TOKENID=SECRET` header). We never store the
// Proxmox root password — tokens can be scoped and revoked.
//
// Security:
//   - TLS verification is ON by default (can be disabled for homelab)
//   - API token is stored in the encrypted secrets store
//   - All operations are audited via the audit log
//   - RBAC is enforced at the MCP layer (same as local tools)
package main

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// ---- Proxmox client ----

// ProxmoxClient wraps a Proxmox VE REST API connection.
type ProxmoxClient struct {
	baseURL    string
	apiToken   string // PVEAPIToken=USER!TOKENID=SECRET
	httpClient *http.Client
	node       string // default node for single-node setups
}

// NewProxmoxClient creates a client from connection parameters.
// token must be in format "USER!TOKENID=SECRET" (e.g. "root@pam!mcp=tkabc123")
func NewProxmoxClient(host string, token string, insecureTLS bool, defaultNode string) (*ProxmoxClient, error) {
	if host == "" {
		return nil, fmt.Errorf("proxmox host is required")
	}
	if token == "" {
		return nil, fmt.Errorf("proxmox API token is required")
	}

	// Normalize URL
	if !strings.HasPrefix(host, "https://") && !strings.HasPrefix(host, "http://") {
		host = "https://" + host
	}
	host = strings.TrimSuffix(host, "/")

	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: insecureTLS, // #nosec G402 -- homelab option, documented
		},
	}

	return &ProxmoxClient{
		baseURL:  host,
		apiToken: "PVEAPIToken=" + token,
		httpClient: &http.Client{
			Transport: tr,
			Timeout:   30 * time.Second,
		},
		node: defaultNode,
	}, nil
}

// ---- Low-level API call helpers ----

func (c *ProxmoxClient) do(method, path string, params url.Values) ([]byte, int, error) {
	var body io.Reader
	if params != nil {
		body = strings.NewReader(params.Encode())
	}

	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", c.apiToken)
	if params != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("proxmox API request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := limitedReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response: %w", err)
	}

	return data, resp.StatusCode, nil
}

// apiGet performs a GET request to the Proxmox API.
func (c *ProxmoxClient) apiGet(path string) (json.RawMessage, error) {
	data, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("proxmox API error (HTTP %d): %s", status, truncateStr(string(data), 200))
	}

	// Proxmox wraps responses in {"data": ...}
	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		// Might be a raw response
		return json.RawMessage(data), nil
	}
	return wrapper.Data, nil
}

// apiPost performs a POST request to the Proxmox API.
func (c *ProxmoxClient) apiPost(path string, params url.Values) (json.RawMessage, error) {
	if params == nil {
		params = url.Values{}
	}
	data, status, err := c.do("POST", path, params)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("proxmox API error (HTTP %d): %s", status, truncateStr(string(data), 200))
	}

	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return json.RawMessage(data), nil
	}
	return wrapper.Data, nil
}

// apiPut performs a PUT request to the Proxmox API.
func (c *ProxmoxClient) apiPut(path string, params url.Values) (json.RawMessage, error) {
	if params == nil {
		params = url.Values{}
	}
	data, status, err := c.do("PUT", path, params)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("proxmox API error (HTTP %d): %s", status, truncateStr(string(data), 200))
	}

	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return json.RawMessage(data), nil
	}
	return wrapper.Data, nil
}

// apiDelete performs a DELETE request to the Proxmox API.
func (c *ProxmoxClient) apiDelete(path string) (json.RawMessage, error) {
	data, status, err := c.do("DELETE", path, nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("proxmox API error (HTTP %d): %s", status, truncateStr(string(data), 200))
	}

	var wrapper struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return json.RawMessage(data), nil
	}
	return wrapper.Data, nil
}

// ---- High-level VM operations ----

// ProxmoxVM represents a VM in Proxmox.
type ProxmoxVM struct {
	VMID    int    `json:"vmid"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	CPUs    int    `json:"cpus"`
	Memory  int    `json:"maxmem"` // bytes
	Disk    int64  `json:"maxdisk"` // bytes
	Node    string `json:"node"`
	Uptime  int64  `json:"uptime"`
}

// ListVMs returns all VMs across all nodes (or a specific node).
func (c *ProxmoxClient) ListVMs() ([]ProxmoxVM, error) {
	// GET /api2/json/cluster/resources?type=vm
	data, err := c.apiGet("/api2/json/cluster/resources?type=vm")
	if err != nil {
		return nil, err
	}

	var vms []ProxmoxVM
	if err := json.Unmarshal(data, &vms); err != nil {
		return nil, fmt.Errorf("failed to parse VM list: %w", err)
	}
	return vms, nil
}

// GetVM returns details for a specific VM.
func (c *ProxmoxClient) GetVM(node string, vmid int) (*ProxmoxVM, error) {
	data, err := c.apiGet(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/current", node, vmid))
	if err != nil {
		return nil, err
	}

	var vm ProxmoxVM
	if err := json.Unmarshal(data, &vm); err != nil {
		return nil, fmt.Errorf("failed to parse VM: %w", err)
	}
	vm.VMID = vmid
	vm.Node = node
	return &vm, nil
}

// CreateVM creates a new VM on the specified node.
func (c *ProxmoxClient) CreateVM(node string, params url.Values) (int, error) {
	data, err := c.apiPost(fmt.Sprintf("/api2/json/nodes/%s/qemu", node), params)
	if err != nil {
		return 0, err
	}

	// Proxmox returns the new VMID
	var result struct {
		VMID int `json:"vmid"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return 0, fmt.Errorf("failed to parse VMID from response: %w", err)
	}
	return result.VMID, nil
}

// StartVM starts a VM.
func (c *ProxmoxClient) StartVM(node string, vmid int) error {
	_, err := c.apiPost(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/start", node, vmid), nil)
	return err
}

// StopVM stops a VM (force power-off).
func (c *ProxmoxClient) StopVM(node string, vmid int) error {
	_, err := c.apiPost(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/stop", node, vmid), nil)
	return err
}

// ShutdownVM gracefully shuts down a VM.
func (c *ProxmoxClient) ShutdownVM(node string, vmid int) error {
	_, err := c.apiPost(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/shutdown", node, vmid), nil)
	return err
}

// RebootVM reboots a VM.
func (c *ProxmoxClient) RebootVM(node string, vmid int) error {
	_, err := c.apiPost(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/reboot", node, vmid), nil)
	return err
}

// DeleteVM deletes a VM (must be stopped first).
func (c *ProxmoxClient) DeleteVM(node string, vmid int) error {
	_, err := c.apiDelete(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d", node, vmid))
	return err
}

// MigrateVM migrates a VM to another node.
func (c *ProxmoxClient) MigrateVM(node string, vmid int, targetNode string, online bool) error {
	params := url.Values{}
	params.Set("target", targetNode)
	if online {
		params.Set("online", "1")
	}
	_, err := c.apiPost(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/migrate", node, vmid), params)
	return err
}

// ---- Snapshots ----

// ProxmoxSnapshot represents a VM snapshot.
type ProxmoxSnapshot struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	SnapTime    int64  `json:"snaptime"`
	VMState     int    `json:"vmstate"` // 1 if snapshot includes RAM
}

// ListSnapshots returns all snapshots for a VM.
func (c *ProxmoxClient) ListSnapshots(node string, vmid int) ([]ProxmoxSnapshot, error) {
	data, err := c.apiGet(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/snapshot", node, vmid))
	if err != nil {
		return nil, err
	}

	var snaps []ProxmoxSnapshot
	if err := json.Unmarshal(data, &snaps); err != nil {
		return nil, fmt.Errorf("failed to parse snapshots: %w", err)
	}
	return snaps, nil
}

// CreateSnapshot creates a VM snapshot.
func (c *ProxmoxClient) CreateSnapshot(node string, vmid int, name string, description string, includeRAM bool) error {
	params := url.Values{}
	params.Set("snapname", name)
	if description != "" {
		params.Set("description", description)
	}
	if includeRAM {
		params.Set("vmstate", "1")
	}
	_, err := c.apiPost(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/snapshot", node, vmid), params)
	return err
}

// RestoreSnapshot restores a VM from a snapshot.
func (c *ProxmoxClient) RestoreSnapshot(node string, vmid int, snapName string) error {
	_, err := c.apiPost(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/snapshot/%s/rollback", node, vmid, snapName), nil)
	return err
}

// DeleteSnapshot deletes a VM snapshot.
func (c *ProxmoxClient) DeleteSnapshot(node string, vmid int, snapName string) error {
	_, err := c.apiDelete(fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/snapshot/%s", node, vmid, snapName))
	return err
}

// ---- Storage ----

// ProxmoxStorage represents a storage backend in Proxmox.
type ProxmoxStorage struct {
	Storage   string `json:"storage"`
	Type      string `json:"type"`
	Total     int64  `json:"total"`     // bytes
	Used      int64  `json:"used"`      // bytes
	Available int64  `json:"avail"`     // bytes
	Enabled   int    `json:"enabled"`
	Active    int    `json:"active"`
	Content   string `json:"content"`   // "images,iso,backup,vztmpl"
}

// ListStorage returns all storage backends.
func (c *ProxmoxClient) ListStorage() ([]ProxmoxStorage, error) {
	data, err := c.apiGet("/api2/json/storage")
	if err != nil {
		return nil, err
	}

	var storage []ProxmoxStorage
	if err := json.Unmarshal(data, &storage); err != nil {
		return nil, fmt.Errorf("failed to parse storage list: %w", err)
	}
	return storage, nil
}

// ---- Nodes ----

// ProxmoxNode represents a node in the Proxmox cluster.
type ProxmoxNode struct {
	Node   string  `json:"node"`
	Status string  `json:"status"`
	CPU    float64 `json:"cpu"`     // 0.0-1.0
	MaxCPU int     `json:"maxcpu"`
	Memory int64   `json:"maxmem"`  // bytes
	Disk   int64   `json:"maxdisk"` // bytes
	Uptime int64   `json:"uptime"`
}

// ListNodes returns all nodes in the Proxmox cluster.
func (c *ProxmoxClient) ListNodes() ([]ProxmoxNode, error) {
	data, err := c.apiGet("/api2/json/nodes")
	if err != nil {
		return nil, err
	}

	var nodes []ProxmoxNode
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nil, fmt.Errorf("failed to parse node list: %w", err)
	}
	return nodes, nil
}

// ---- LXC containers ----

// ProxmoxContainer represents an LXC container in Proxmox.
type ProxmoxContainer struct {
	CTID   int    `json:"vmid"`
	Name   string `json:"name"`
	Status string `json:"status"`
	CPUs   int    `json:"cpus"`
	Memory int64  `json:"maxmem"`
	Disk   int64  `json:"maxdisk"`
	Node   string `json:"node"`
}

// ListContainers returns all LXC containers.
func (c *ProxmoxClient) ListContainers() ([]ProxmoxContainer, error) {
	data, err := c.apiGet("/api2/json/cluster/resources?type=lxc")
	if err != nil {
		return nil, err
	}

	var containers []ProxmoxContainer
	if err := json.Unmarshal(data, &containers); err != nil {
		return nil, fmt.Errorf("failed to parse container list: %w", err)
	}
	return containers, nil
}

// ---- Utility ----

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// ---- Proxmox backend manager ----

// proxmoxBackend is the global Proxmox client (nil if not configured).
var proxmoxBackend *ProxmoxClient

// initProxmoxBackend initializes the Proxmox backend from environment variables.
// If CUBE_PROXMOX_HOST is not set, the backend remains nil (disabled).
func initProxmoxBackend() {
	host := envOr("CUBE_PROXMOX_HOST", "")
	if host == "" {
		return
	}

	token := envOr("CUBE_PROXMOX_TOKEN", "")
	if token == "" {
		fmt.Fprintf(os.Stderr, "[cube-mcp] warning: CUBE_PROXMOX_HOST set but CUBE_PROXMOX_TOKEN missing — Proxmox backend disabled\n")
		return
	}

	insecureTLS := envOr("CUBE_PROXMOX_INSECURE_TLS", "false") == "true"
	defaultNode := envOr("CUBE_PROXMOX_NODE", "")

	client, err := NewProxmoxClient(host, token, insecureTLS, defaultNode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] warning: failed to init Proxmox backend: %v\n", err)
		return
	}

	proxmoxBackend = client
	fmt.Printf("[cube-mcp] Proxmox backend initialized: %s (node: %s)\n", host, defaultNode)
}
