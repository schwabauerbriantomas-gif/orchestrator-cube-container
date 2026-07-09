// Package main: resource limits enforcement for containers.
//
// Applies hard cgroup limits (memory, CPU) to running containers via the
// Docker CLI (`docker update`). Also reads real-time usage via `docker stats`
// and maintains a persistent policy store so limits can be re-applied on
// container restart.
//
// For Cube backend (edge nodes without Docker), limits are applied at
// creation time only (CreateSandbox accepts memory/cpu parameters).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Types ----

// ResourcePolicy records the intended limits for a container.
type ResourcePolicy struct {
	ContainerID string    `json:"container_id"`
	MemoryMB    int       `json:"memory_mb"`
	CPUCores    float64   `json:"cpu_cores"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ContainerUsage is a real-time resource snapshot from `docker stats`.
type ContainerUsage struct {
	ContainerID string  `json:"container_id"`
	Name        string  `json:"name"`
	CPUPercent  float64 `json:"cpu_percent"`
	MemUsage    string  `json:"mem_usage"`
	MemPercent  float64 `json:"mem_percent"`
	NetIO       string  `json:"net_io"`
	BlockIO     string  `json:"block_io"`
	PIDs        int     `json:"pids"`
}

// QuotaSummary aggregates node-wide resource allocation.
type QuotaSummary struct {
	TotalContainers int     `json:"total_containers"`
	AllocatedMB     int     `json:"allocated_memory_mb"`
	AllocatedCPU    float64 `json:"allocated_cpu_cores"`
	NodeMemoryMB    int     `json:"node_memory_mb"`
	NodeCPUCores    float64 `json:"node_cpu_cores"`
	MemUtilization  float64 `json:"memory_utilization_percent"`
	CPUUtilization  float64 `json:"cpu_utilization_percent"`
}

// ---- Manager ----

var resourceMgr *ResourceManager

type ResourceManager struct {
	mu       sync.Mutex
	policies map[string]*ResourcePolicy
	rootDir  string
}

func newResourceManager() *ResourceManager {
	rm := &ResourceManager{
		policies: make(map[string]*ResourcePolicy),
		rootDir:  envOr("CUBE_RESOURCES_ROOT", "/var/lib/cube-container/resources"),
	}
	rm.loadFromDisk()
	return rm
}

// ---- Disk persistence ----

func (rm *ResourceManager) policyFilePath() string {
	return filepath.Join(rm.rootDir, "policies.json")
}

func (rm *ResourceManager) loadFromDisk() {
	data, err := os.ReadFile(rm.policyFilePath())
	if err != nil {
		return
	}
	var policies map[string]*ResourcePolicy
	if err := json.Unmarshal(data, &policies); err != nil {
		return
	}
	rm.policies = policies
}

func (rm *ResourceManager) saveToDisk() error {
	if err := os.MkdirAll(rm.rootDir, 0700); err != nil {
		return fmt.Errorf("create resources root: %w", err)
	}
	data, err := json.MarshalIndent(rm.policies, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal policies: %w", err)
	}
	return os.WriteFile(rm.policyFilePath(), data, 0600)
}

// ---- Docker CLI helpers ----

// dockerAvailable checks if the docker binary exists on the host.
func dockerAvailable() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// SetLimits applies memory and CPU limits to a running container via docker update.
func (rm *ResourceManager) SetLimits(containerID string, memoryMB int, cpuCores float64) error {
	if containerID == "" {
		return fmt.Errorf("container_id is required")
	}
	// Argument injection prevention (C6): validate container_id is safe
	if err := validateContainerID(containerID); err != nil {
		return err
	}

	if !dockerAvailable() {
		// For Cube backend, limits were applied at creation time.
		// Store the policy anyway for future reference.
		return rm.storePolicy(containerID, memoryMB, cpuCores)
	}

	args := []string{"update"}
	if memoryMB > 0 {
		args = append(args, fmt.Sprintf("--memory=%dm", memoryMB))
	}
	if cpuCores > 0 {
		args = append(args, fmt.Sprintf("--cpus=%.2f", cpuCores))
	}
	args = append(args, containerID)

	cmd := exec.Command("docker", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker update failed: %w: %s", err, strings.TrimSpace(string(output)))
	}

	return rm.storePolicy(containerID, memoryMB, cpuCores)
}

func (rm *ResourceManager) storePolicy(containerID string, memoryMB int, cpuCores float64) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	now := time.Now().UTC()
	if existing, ok := rm.policies[containerID]; ok {
		existing.MemoryMB = memoryMB
		existing.CPUCores = cpuCores
		existing.UpdatedAt = now
	} else {
		rm.policies[containerID] = &ResourcePolicy{
			ContainerID: containerID,
			MemoryMB:    memoryMB,
			CPUCores:    cpuCores,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
	}
	return rm.saveToDisk()
}

// GetUsage returns real-time resource usage for a single container.
func (rm *ResourceManager) GetUsage(containerID string) (*ContainerUsage, error) {
	if containerID == "" {
		return nil, fmt.Errorf("container_id is required")
	}
	// Argument injection prevention (C6)
	if err := validateContainerID(containerID); err != nil {
		return nil, err
	}
	if !dockerAvailable() {
		return nil, fmt.Errorf("docker CLI not available (Cube backend limits are set at creation time)")
	}

	cmd := exec.Command("docker", "stats", "--no-stream", "--format",
		`{"container_id":"{{.ID}}","name":"{{.Name}}","cpu_percent":"{{.CPUPerc}}","mem_usage":"{{.MemUsage}}","mem_percent":"{{.MemPerc}}","net_io":"{{.NetIO}}","block_io":"{{.BlockIO}}","pids":"{{.PIDs}}"}`,
		containerID)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker stats failed: %w", err)
	}

	usage, err := parseStatsOutput(output)
	if err != nil {
		return nil, err
	}
	if len(usage) == 0 {
		return nil, fmt.Errorf("no stats for container %s", containerID)
	}
	return &usage[0], nil
}

// ListUsage returns usage for all running containers.
func (rm *ResourceManager) ListUsage() ([]ContainerUsage, error) {
	if !dockerAvailable() {
		return nil, fmt.Errorf("docker CLI not available")
	}

	cmd := exec.Command("docker", "stats", "--no-stream", "--format",
		`{"container_id":"{{.ID}}","name":"{{.Name}}","cpu_percent":"{{.CPUPerc}}","mem_usage":"{{.MemUsage}}","mem_percent":"{{.MemPerc}}","net_io":"{{.NetIO}}","block_io":"{{.BlockIO}}","pids":"{{.PIDs}}"}`)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker stats failed: %w", err)
	}

	return parseStatsOutput(output)
}

// QuotaSummary aggregates allocated limits vs node capacity.
func (rm *ResourceManager) QuotaSummary() (*QuotaSummary, error) {
	rm.mu.Lock()
	allocatedMB := 0
	allocatedCPU := 0.0
	count := len(rm.policies)
	for _, p := range rm.policies {
		allocatedMB += p.MemoryMB
		allocatedCPU += p.CPUCores
	}
	rm.mu.Unlock()

	summary := &QuotaSummary{
		TotalContainers: count,
		AllocatedMB:     allocatedMB,
		AllocatedCPU:    allocatedCPU,
	}

	// Get node capacity from Docker if available
	if dockerAvailable() {
		info, err := exec.Command("docker", "info", "--format", `{"mem":"{{.MemTotal}}","cpu":"{{.NCPU}}"}`).Output()
		if err == nil {
			var nodeInfo struct {
				Mem string `json:"mem"`
				CPU string `json:"cpu"`
			}
			if json.Unmarshal(info, &nodeInfo) == nil {
				// MemTotal is in bytes — convert to MB
				var memBytes int64
				fmt.Sscanf(nodeInfo.Mem, "%d", &memBytes)
				summary.NodeMemoryMB = int(memBytes / (1024 * 1024))
				fmt.Sscanf(nodeInfo.CPU, "%f", &summary.NodeCPUCores)
			}
		}
	}

	if summary.NodeMemoryMB > 0 {
		summary.MemUtilization = float64(allocatedMB) / float64(summary.NodeMemoryMB) * 100
	}
	if summary.NodeCPUCores > 0 {
		summary.CPUUtilization = allocatedCPU / summary.NodeCPUCores * 100
	}

	return summary, nil
}

// parseStatsOutput parses the JSON-lines output from docker stats --format.
func parseStatsOutput(output []byte) ([]ContainerUsage, error) {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var results []ContainerUsage

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]string
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		usage := ContainerUsage{
			ContainerID: raw["container_id"],
			Name:        strings.TrimPrefix(raw["name"], "/"),
			CPUPercent:  parsePercent(raw["cpu_percent"]),
			MemUsage:    raw["mem_usage"],
			MemPercent:  parsePercent(raw["mem_percent"]),
			NetIO:       raw["net_io"],
			BlockIO:     raw["block_io"],
		}
		fmt.Sscanf(raw["pids"], "%d", &usage.PIDs)
		results = append(results, usage)
	}
	return results, nil
}

// parsePercent converts "12.34%" to 12.34.
func parsePercent(s string) float64 {
	s = strings.TrimSuffix(strings.TrimSpace(s), "%")
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

// ---- Tool handlers: Resources ----

func handleResourceSetLimits(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	memoryMB := argInt(args, "memory_mb", 0)
	cpuCores := argFloat(args, "cpu_count", 0)

	if memoryMB == 0 && cpuCores == 0 {
		return errResult("at least one of memory_mb or cpu_count must be set"), nil
	}

	if err := resourceMgr.SetLimits(containerID, memoryMB, cpuCores); err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(map[string]interface{}{
		"container_id": containerID,
		"memory_mb":    memoryMB,
		"cpu_count":    cpuCores,
		"status":       "applied",
	}), nil
}

func handleResourceGetUsage(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	usage, err := resourceMgr.GetUsage(containerID)
	if err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(usage), nil
}

func handleResourceListUsage(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	usage, err := resourceMgr.ListUsage()
	if err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(usage), nil
}

func handleResourceQuotaSummary(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	summary, err := resourceMgr.QuotaSummary()
	if err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(summary), nil
}
