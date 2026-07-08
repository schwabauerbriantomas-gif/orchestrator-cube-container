// Package main: service discovery registry.
//
// DiscoveryManager maintains a registry mapping logical service names to
// current IP:port endpoints. Entries can be registered explicitly via the
// service_register tool, or populated automatically by SyncFromContainers,
// which inspects running containers carrying a "discovery.name" label.
//
// The registry is persisted as a single JSON map at
// /var/lib/cube-container/discovery/entries.json so that service bindings
// survive server restarts. This file only defines the manager, its types,
// and the four MCP tool handlers; tool registration and RBAC wiring happen
// in server.go and auth.go respectively.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Constants ----

const (
	// defaultDiscoveryDir is the on-disk directory for the discovery store.
	defaultDiscoveryDir = "/var/lib/cube-container/discovery"
	// defaultDiscoveryFile is the JSON map persisted by DiscoveryManager.
	defaultDiscoveryFile = "entries.json"
	// discoveryLabelName is the container label that opts a container into
	// auto-discovery and provides the logical service name.
	discoveryLabelName = "discovery.name"
	// discoveryLabelHost optionally overrides the registered host.
	discoveryLabelHost = "discovery.host"
	// discoveryLabelPort optionally overrides the registered port.
	discoveryLabelPort = "discovery.port"
	// defaultSyncHost is the host used when a synced container omits
	// discovery.host (loopback on the Docker host network).
	defaultSyncHost = "127.0.0.1"
)

// ---- Types ----

// ServiceEntry maps a logical service name to a live endpoint backed by a
// container. LastUpdated records when the entry was last touched so callers
// can detect stale registrations.
type ServiceEntry struct {
	Name        string    `json:"name"`
	ContainerID string    `json:"container_id"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	LastUpdated time.Time `json:"last_updated"`
}

// discoveryFile is the on-disk JSON envelope for the registry.
type discoveryFile struct {
	Version int           `json:"version"`
	Entries []ServiceEntry `json:"entries"`
}

// DiscoveryManager is a thread-safe registry of ServiceEntry records keyed
// by service name. It persists to a single JSON file on every mutation.
type DiscoveryManager struct {
	storePath string
	entries   map[string]ServiceEntry
	mu        sync.RWMutex
}

// ---- Constructor ----

// newDiscoveryManager creates a DiscoveryManager backed by the JSON file at
// dir/entries.json (defaults overridable via CUBE_DISCOVERY_DIR). A missing
// file is not an error — it means a fresh registry.
func newDiscoveryManager() *DiscoveryManager {
	dir := envOr("CUBE_DISCOVERY_DIR", defaultDiscoveryDir)
	dm := &DiscoveryManager{
		storePath: filepath.Join(dir, defaultDiscoveryFile),
		entries:   make(map[string]ServiceEntry),
	}
	if err := dm.load(); err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] WARNING: discovery load failed: %v\n", err)
	}
	return dm
}

// ---- Core operations ----

// Register adds or updates the entry for name. ContainerID records which
// container backs the service; host and port form the resolved endpoint.
// The registry is persisted immediately.
func (dm *DiscoveryManager) Register(name, containerID, host string, port int) error {
	if name == "" {
		return fmt.Errorf("service name cannot be empty")
	}
	if host == "" {
		return fmt.Errorf("host cannot be empty")
	}
	if port < 0 || port > 65535 {
		return fmt.Errorf("port must be in 0..65535, got %d", port)
	}

	now := time.Now()
	dm.mu.Lock()
	dm.entries[name] = ServiceEntry{
		Name:        name,
		ContainerID: containerID,
		Host:        host,
		Port:        port,
		LastUpdated: now,
	}
	dm.mu.Unlock()

	return dm.save()
}

// Deregister removes the entry for name. It is not an error if the name is
// unknown. The registry is persisted immediately.
func (dm *DiscoveryManager) Deregister(name string) error {
	dm.mu.Lock()
	_, existed := dm.entries[name]
	if existed {
		delete(dm.entries, name)
	}
	dm.mu.Unlock()

	if !existed {
		return nil // idempotent
	}
	return dm.save()
}

// Resolve looks up name and returns its host:port string, or an error if the
// name is not registered. Callers should treat a non-nil error as "unknown
// service" and surface it to the user.
func (dm *DiscoveryManager) Resolve(name string) (string, *ServiceEntry, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	entry, ok := dm.entries[name]
	if !ok {
		return "", nil, fmt.Errorf("service %q not registered", name)
	}
	return fmt.Sprintf("%s:%d", entry.Host, entry.Port), &entry, nil
}

// ListServices returns all registered entries sorted by name for stable
// output. Returns a non-nil empty slice when the registry is empty.
func (dm *DiscoveryManager) ListServices() []ServiceEntry {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	result := make([]ServiceEntry, 0, len(dm.entries))
	for _, e := range dm.entries {
		result = append(result, e)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

// SyncFromContainers iterates running containers via the global ContainerBackend
// and auto-registers any that carry a "discovery.name" label. The host and port
// may be overridden per container via "discovery.host" and "discovery.port"
// labels; otherwise host defaults to 127.0.0.1 and port to 0.
//
// Returns the number of entries registered/refreshed. Containers without the
// label are skipped silently. Backend errors are returned to the caller.
func (dm *DiscoveryManager) SyncFromContainers() (int, error) {
	if client == nil {
		return 0, fmt.Errorf("container backend not initialized")
	}

	raw, err := client.ListSandboxes("running", 100)
	if err != nil {
		return 0, fmt.Errorf("list running sandboxes: %w", err)
	}

	arr, ok := raw.([]interface{})
	if !ok {
		return 0, nil // nothing to sync
	}

	registered := 0
	now := time.Now()

	// Stage updates under the lock, then persist once at the end.
	updates := make(map[string]ServiceEntry)
	for _, item := range arr {
		cm := asMap(item)
		labels := asMap(cm["labels"])
		name := toString(labels[discoveryLabelName])
		if name == "" {
			continue // not a discoverable service
		}

		host := toString(labels[discoveryLabelHost])
		if host == "" {
			host = defaultSyncHost
		}
		port := toInt(labels[discoveryLabelPort])

		updates[name] = ServiceEntry{
			Name:        name,
			ContainerID: toString(cm["sandboxID"]),
			Host:        host,
			Port:        port,
			LastUpdated: now,
		}
	}

	if len(updates) == 0 {
		return 0, nil
	}

	dm.mu.Lock()
	for name, entry := range updates {
		dm.entries[name] = entry
		registered++
	}
	dm.mu.Unlock()

	if err := dm.save(); err != nil {
		return registered, fmt.Errorf("persist after sync: %w", err)
	}
	return registered, nil
}

// ---- Persistence ----

// load reads the registry from disk into the in-memory map.
// A missing file is not an error — it means a fresh start.
func (dm *DiscoveryManager) load() error {
	data, err := os.ReadFile(dm.storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // fresh registry
		}
		return err
	}

	var df discoveryFile
	if err := json.Unmarshal(data, &df); err != nil {
		return fmt.Errorf("parse %s: %w", dm.storePath, err)
	}

	dm.mu.Lock()
	defer dm.mu.Unlock()
	for _, entry := range df.Entries {
		dm.entries[entry.Name] = entry
	}
	return nil
}

// save serializes the registry to disk as JSON (mode 0600, dir 0700).
func (dm *DiscoveryManager) save() error {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	df := discoveryFile{
		Version: 1,
		Entries: make([]ServiceEntry, 0, len(dm.entries)),
	}
	for _, entry := range dm.entries {
		df.Entries = append(df.Entries, entry)
	}
	// Stable order on disk for readable diffs.
	sort.Slice(df.Entries, func(i, j int) bool {
		return df.Entries[i].Name < df.Entries[j].Name
	})

	data, err := json.MarshalIndent(df, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal discovery entries: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dm.storePath), 0700); err != nil {
		return fmt.Errorf("create discovery directory: %w", err)
	}

	if err := os.WriteFile(dm.storePath, data, 0600); err != nil {
		return fmt.Errorf("write %s: %w", dm.storePath, err)
	}
	return nil
}

// ---- MCP tool handlers ----
//
// These handlers assume a package-level `discoveryMgr` (declared below) that
// is initialized in main(). The parent agent wires up tool registration in
// server.go and permission entries in auth.go's toolPermissions map.

var discoveryMgr *DiscoveryManager

// handleDiscoveryRegister implements service_register: add or update a service
// entry mapping a logical name to a container endpoint.
func handleDiscoveryRegister(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if discoveryMgr == nil {
		return errResult("discovery manager not initialized"), nil
	}
	args := parseArgs(req)
	name := argString(args, "name")
	containerID := argString(args, "container_id")
	host := argString(args, "host")
	port := argInt(args, "port", 0)

	if name == "" {
		return errResult("name is required"), nil
	}
	if host == "" {
		return errResult("host is required"), nil
	}

	if err := discoveryMgr.Register(name, containerID, host, port); err != nil {
		return errResult(err.Error()), nil
	}

	return okResult(map[string]interface{}{
		"status": "registered",
		"name":   name,
		"endpoint": fmt.Sprintf("%s:%d", host, port),
	}), nil
}

// handleDiscoveryDeregister implements service_deregister: remove a service
// entry by name. Idempotent — a missing name is still reported as success.
func handleDiscoveryDeregister(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if discoveryMgr == nil {
		return errResult("discovery manager not initialized"), nil
	}
	args := parseArgs(req)
	name := argString(args, "name")

	if name == "" {
		return errResult("name is required"), nil
	}

	if err := discoveryMgr.Deregister(name); err != nil {
		return errResult(err.Error()), nil
	}

	return okResult(map[string]interface{}{
		"status": "deregistered",
		"name":   name,
	}), nil
}

// handleDiscoveryResolve implements service_resolve: look up a service name and
// return its current host:port endpoint plus full entry metadata.
func handleDiscoveryResolve(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if discoveryMgr == nil {
		return errResult("discovery manager not initialized"), nil
	}
	args := parseArgs(req)
	name := argString(args, "name")

	if name == "" {
		return errResult("name is required"), nil
	}

	endpoint, entry, err := discoveryMgr.Resolve(name)
	if err != nil {
		return errResult(err.Error()), nil
	}

	return okResult(map[string]interface{}{
		"name":     name,
		"endpoint": endpoint,
		"entry":    entry,
	}), nil
}

// handleServiceListEntries implements service_list: return all registered
// service entries. Pass sync=true to first refresh from running container
// labels.
//
// NOTE: named handleServiceListEntries (not handleServiceList) because
// scaling.go already defines handleServiceList for scaling replica groups.
func handleServiceListEntries(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if discoveryMgr == nil {
		return errResult("discovery manager not initialized"), nil
	}
	args := parseArgs(req)

	if argString(args, "sync") == "true" {
		if _, err := discoveryMgr.SyncFromContainers(); err != nil {
			return errResult(fmt.Sprintf("sync failed: %v", err)), nil
		}
	}

	entries := discoveryMgr.ListServices()
	if entries == nil {
		entries = []ServiceEntry{}
	}
	return okResult(map[string]interface{}{
		"count":   len(entries),
		"entries": entries,
	}), nil
}
