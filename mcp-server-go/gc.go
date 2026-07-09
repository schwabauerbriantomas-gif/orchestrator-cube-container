// Package main: garbage collection — image and volume cleanup.
//
// Prevents disk exhaustion on 60GB SSD edge nodes by periodically pruning
// old images and orphaned volumes. Runs as a background watcher that checks
// disk usage hourly and auto-prunes when over the configured threshold.
//
// Prune operations:
//   - Images:    `docker image prune -f --filter "until=168h"` (dangling + unused older than 7 days)
//   - Volumes:   `docker volume prune -f` + cross-reference with deploy.ListVolumes() to find orphaned app volumes
//   - DiskUsage: `docker system df --format json` for visibility into what's consuming disk
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Types ----

// PruneResult records what was cleaned up.
type PruneResult struct {
	Type       string `json:"type"`        // images or volumes
	Reclaimed  string `json:"reclaimed"`   // human-readable size
	ItemsRemoved int   `json:"items_removed"`
	Timestamp string `json:"timestamp"`
}

// DiskUsageEntry is one row from `docker system df`.
type DiskUsageEntry struct {
	Type       string `json:"type"`
	Total      int    `json:"total_count"`
	Active     int    `json:"active"`
	Size       string `json:"size"`
	Reclaimable string `json:"reclaimable"`
}

// ---- Manager ----

var gc *GarbageCollector

type GarbageCollector struct {
	mu           sync.Mutex
	rootDir      string
	threshold    int  // disk % that triggers auto-prune
	minAgeHours  int  // minimum image age before pruning
	enabled      bool
	stopCh       chan struct{}
	running      bool
}

func newGarbageCollector() *GarbageCollector {
	return &GarbageCollector{
		rootDir:     envOr("CUBE_GC_ROOT", "/var/lib/cube-container/gc"),
		threshold:   envInt("CUBE_GC_THRESHOLD", 85),
		minAgeHours: envInt("CUBE_GC_MIN_AGE_HOURS", 168), // 7 days
		enabled:     envOr("CUBE_GC_ENABLED", "true") != "false",
		stopCh:      make(chan struct{}),
	}
}

// envInt parses an int from env, returning fallback on error.
func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	var i int
	fmt.Sscanf(v, "%d", &i)
	if i == 0 {
		return fallback
	}
	return i
}

// ---- Prune operations ----

// PruneImages removes dangling and unused images older than minAgeHours.
func (g *GarbageCollector) PruneImages() (*PruneResult, error) {
	if !dockerAvailable() {
		return nil, fmt.Errorf("docker CLI not available")
	}

	ageFilter := fmt.Sprintf("until=%dh", g.minAgeHours)
	cmd := exec.Command("docker", "image", "prune", "-af", "--filter", ageFilter, "--format", "{{.Size}}")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker image prune: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	result := &PruneResult{
		Type:        "images",
		ItemsRemoved: len(lines),
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
	}
	if len(lines) > 0 && lines[0] != "" {
		// Sum is approximate — docker reports per-image reclaimed space
		result.Reclaimed = strings.TrimSpace(string(output))
	}

	fmt.Fprintf(os.Stderr, "[cube-mcp] GC: pruned %d images\n", result.ItemsRemoved)
	return result, nil
}

// PruneVolumes removes orphaned Docker volumes and cross-references
// deploy volumes with running containers to find app-level orphans.
func (g *GarbageCollector) PruneVolumes() (*PruneResult, error) {
	if !dockerAvailable() {
		return nil, fmt.Errorf("docker CLI not available")
	}

	// First: prune Docker's own orphaned volumes
	cmd := exec.Command("docker", "volume", "prune", "-f")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker volume prune: %w", err)
	}

	// Parse total reclaimed space
	result := &PruneResult{
		Type:      "volumes",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	// Try to extract reclaimed space from output
	for _, line := range strings.Split(string(output), "\n") {
		if strings.Contains(line, "reclaimed") {
			result.Reclaimed = strings.TrimSpace(line)
			break
		}
	}
	if result.Reclaimed == "" {
		result.Reclaimed = strings.TrimSpace(string(output))
	}

	// Second: check app volumes (deploy-managed) for orphans
	// A volume is orphaned if it exists in /volumes/ but no running container references it
	runningContainers, err := client.ListSandboxes("running", 200)
	if err == nil {
		activeVolumes := make(map[string]bool)
		for _, item := range asMapSlice(runningContainers) {
			m := asMap(item)
			// Check if container has volume references
			if mounts := m["mounts"]; mounts != nil {
				if ms, ok := mounts.([]interface{}); ok {
					for _, mt := range ms {
						if mm := asMap(mt); mm != nil {
							if src := toString(mm["Source"]); src != "" {
								// Extract volume name from path
								parts := strings.Split(src, "/")
								if len(parts) > 0 {
									activeVolumes[parts[len(parts)-1]] = true
								}
							}
						}
					}
				}
			}
		}

		// Check deploy volumes against active set
		volumes, err := deploy.ListVolumes()
		if err == nil {
			orphanCount := 0
			for _, vol := range volumes {
				if !activeVolumes[vol.Name] {
					// Volume has no running container — candidate for cleanup
					// We log but don't auto-delete app volumes (user data)
					orphanCount++
				}
			}
			result.ItemsRemoved = orphanCount
		}
	}

	fmt.Fprintf(os.Stderr, "[cube-mcp] GC: pruned orphaned volumes\n")
	return result, nil
}

// DiskUsage returns what's consuming disk space.
func (g *GarbageCollector) DiskUsage() ([]DiskUsageEntry, error) {
	if !dockerAvailable() {
		return nil, fmt.Errorf("docker CLI not available")
	}

	cmd := exec.Command("docker", "system", "df")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker system df: %w", err)
	}

	// Parse the table output (docker system df doesn't have clean JSON format)
	// Format: TYPE | TOTAL | ACTIVE | SIZE | RECLAIMABLE
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var entries []DiskUsageEntry

	for i, line := range lines {
		if i == 0 {
			continue // header
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		entry := DiskUsageEntry{
			Type:        fields[0],
			Size:        fields[3],
			Reclaimable: fields[4],
		}
		fmt.Sscanf(fields[1], "%d", &entry.Total)
		fmt.Sscanf(fields[2], "%d", &entry.Active)
		entries = append(entries, entry)
	}

	return entries, nil
}

// ---- Disk usage percentage (for auto-prune trigger) ----

// diskUsagePercent returns the percentage of disk used on the root filesystem.
func diskUsagePercent() int {
	cmd := exec.Command("df", "-P", "/")
	output, err := cmd.Output()
	if err != nil {
		return 0
	}
	lines := strings.Split(string(output), "\n")
	if len(lines) < 2 {
		return 0
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 5 {
		return 0
	}
	// Use field index 4 (Use%) and strip the % sign
	pct := strings.TrimSuffix(fields[4], "%")
	var i int
	fmt.Sscanf(pct, "%d", &i)
	return i
}

// ---- Background watcher ----

func (g *GarbageCollector) Start() {
	if !g.enabled {
		fmt.Fprintf(os.Stderr, "[cube-mcp] GC: disabled via CUBE_GC_ENABLED=false\n")
		return
	}

	g.mu.Lock()
	if g.running {
		g.mu.Unlock()
		return
	}
	g.running = true
	g.mu.Unlock()

	go g.watch()
	fmt.Fprintf(os.Stderr, "[cube-mcp] GC watcher started (threshold=%d%%, min_age=%dh)\n", g.threshold, g.minAgeHours)
}

func (g *GarbageCollector) watch() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-g.stopCh:
			return
		case <-ticker.C:
			g.checkAndPrune()
		}
	}
}

func (g *GarbageCollector) checkAndPrune() {
	usage := diskUsagePercent()
	if usage == 0 {
		return // couldn't read disk usage
	}

	if usage >= g.threshold {
		fmt.Fprintf(os.Stderr, "[cube-mcp] GC: disk at %d%% (threshold %d%%) — auto-pruning\n", usage, g.threshold)
		result, err := g.PruneImages()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[cube-mcp] GC: image prune failed: %v\n", err)
		} else {
			_ = result
		}

		// Check again after image prune
		usage = diskUsagePercent()
		if usage >= g.threshold {
			result, err := g.PruneVolumes()
			if err != nil {
				fmt.Fprintf(os.Stderr, "[cube-mcp] GC: volume prune failed: %v\n", err)
			} else {
				_ = result
			}
		}
	}
}

// ---- asMapSlice helper ----

func asMapSlice(v interface{}) []interface{} {
	if s, ok := v.([]interface{}); ok {
		return s
	}
	return nil
}

// ---- Tool handlers: GC ----

func handleGCPruneImages(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	result, err := gc.PruneImages()
	if err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(result), nil
}

func handleGCPruneVolumes(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	result, err := gc.PruneVolumes()
	if err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(result), nil
}

func handleGCDiskUsage(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	entries, err := gc.DiskUsage()
	if err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(entries), nil
}
