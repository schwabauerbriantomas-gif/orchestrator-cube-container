// Package main: hypervisor layer — ZFS storage management.
//
// ZFS provides enterprise-grade storage for VMs and containers: pooled storage,
// instant snapshots, deduplication, compression, and end-to-end checksumming.
// These tools expose ZFS operations through the MCP interface, allowing LLM
// agents to manage storage pools, datasets, and snapshots programmatically.
//
// Implementation shells out to the `zfs` and `zpool` CLIs — no CGO, no
// external Go library dependency. This mirrors the pattern used in gc.go (docker)
// and networking.go (iptables).
//
// RBAC: admin for mutating operations, viewer for read-only.
package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Types ----

type ZFSPool struct {
	Name      string `json:"name"`
	Size      string `json:"size"`
	Allocated string `json:"allocated"`
	Free      string `json:"free"`
	Health    string `json:"health"`
}

type ZFSDataset struct {
	Name           string `json:"name"`
	Used           string `json:"used"`
	Available      string `json:"available"`
	Referenced     string `json:"referenced"`
	Mountpoint     string `json:"mountpoint"`
	Compression    string `json:"compression,omitempty"`
	Deduplication  string `json:"dedup,omitempty"`
	RecordSize     string `json:"recordsize,omitempty"`
}

type ZFSSnapshot struct {
	Name    string `json:"name"`
	Used    string `json:"used"`
	Created string `json:"created"`
}

// ---- zfs/zpool execution helpers ----

func runZpool(args ...string) (string, error) {
	cmd := exec.Command("zpool", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("zpool %s: %w (output: %s)",
			strings.Join(args, " "), err, truncate(string(out), 500))
	}
	return strings.TrimSpace(string(out)), nil
}

func runZfs(args ...string) (string, error) {
	cmd := exec.Command("zfs", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("zfs %s: %w (output: %s)",
			strings.Join(args, " "), err, truncate(string(out), 500))
	}
	return strings.TrimSpace(string(out)), nil
}

func zfsAvailable() bool {
	_, err := exec.LookPath("zfs")
	return err == nil
}

// ---- Pool handlers ----

func handleZPoolList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !zfsAvailable() {
		return errResult("ZFS is not available on this host. Install zfsutils-linux."), nil
	}

	out, err := runZpool("list", "-H", "-p")
	if err != nil {
		return unwrapError(err), nil
	}

	pools := make([]ZFSPool, 0)
	for _, line := range splitNonEmpty(out) {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		pools = append(pools, ZFSPool{
			Name:      fields[0],
			Size:      humanizeBytes(fields[1]),
			Allocated: humanizeBytes(fields[2]),
			Free:      humanizeBytes(fields[3]),
			Health:    fields[len(fields)-1], // health is the last column
		})
	}
	return okResult(map[string]interface{}{
		"pools":  pools,
		"total":  len(pools),
	}), nil
}

func handleZPoolCreate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !zfsAvailable() {
		return errResult("ZFS is not available on this host."), nil
	}

	name := argString(req.GetArguments(), "name")
	devices := argString(req.GetArguments(), "devices")
	if name == "" || devices == "" {
		return errResult("name and devices are required"), nil
	}

	_, err := runZpool("create", name, devices)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{
		"status": "pool created",
		"name":   name,
	}), nil
}

func handleZPoolStatus(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !zfsAvailable() {
		return errResult("ZFS is not available on this host."), nil
	}

	name := argString(req.GetArguments(), "name")
	if name == "" {
		// Show all pool status
		out, err := runZpool("status")
		if err != nil {
			return unwrapError(err), nil
		}
		return okResult(map[string]string{"status_output": out}), nil
	}

	out, err := runZpool("status", name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{"pool": name, "status": out}), nil
}

func handleZPoolDestroy(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !zfsAvailable() {
		return errResult("ZFS is not available on this host."), nil
	}

	name := argString(req.GetArguments(), "name")
	if name == "" {
		return errResult("name is required"), nil
	}

	_, err := runZpool("destroy", name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{"status": "pool destroyed", "name": name}), nil
}

// ---- Dataset handlers ----

func handleZDatasetList(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !zfsAvailable() {
		return errResult("ZFS is not available on this host."), nil
	}

	pool := argString(req.GetArguments(), "pool")

	args := []string{"list", "-t", "filesystem", "-o", "name,used,avail,refer,mountpoint", "-r"}
	if pool != "" {
		args = append(args, pool)
	}

	out, err := runZfs(args...)
	if err != nil {
		return unwrapError(err), nil
	}

	datasets := make([]ZFSDataset, 0)
	lines := splitNonEmpty(out)
	for _, line := range lines[1:] { // skip header
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		datasets = append(datasets, ZFSDataset{
			Name:       fields[0],
			Used:       fields[1],
			Available:  fields[2],
			Referenced: fields[3],
			Mountpoint: fields[4],
		})
	}
	return okResult(map[string]interface{}{
		"datasets": datasets,
		"total":    len(datasets),
	}), nil
}

func handleZDatasetCreate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !zfsAvailable() {
		return errResult("ZFS is not available on this host."), nil
	}

	name := argString(req.GetArguments(), "name")
	if name == "" {
		return errResult("name is required"), nil
	}

	args := []string{"create"}
	compression := argString(req.GetArguments(), "compression")
	if compression != "" {
		args = append(args, "-o", "compression="+compression)
	}
	recordSize := argString(req.GetArguments(), "recordsize")
	if recordSize != "" {
		args = append(args, "-o", "recordsize="+recordSize)
	}
	args = append(args, name)

	_, err := runZfs(args...)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{"status": "dataset created", "name": name}), nil
}

func handleZDatasetDestroy(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !zfsAvailable() {
		return errResult("ZFS is not available on this host."), nil
	}

	name := argString(req.GetArguments(), "name")
	if name == "" {
		return errResult("name is required"), nil
	}

	_, err := runZfs("destroy", "-r", name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{"status": "dataset destroyed", "name": name}), nil
}

// ---- Snapshot handlers ----

func handleZSnapshotCreate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !zfsAvailable() {
		return errResult("ZFS is not available on this host."), nil
	}

	dataset := argString(req.GetArguments(), "dataset")
	name := argString(req.GetArguments(), "name")
	if dataset == "" || name == "" {
		return errResult("dataset and name are required"), nil
	}

	fullName := dataset + "@" + name
	_, err := runZfs("snapshot", fullName)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{
		"status": "snapshot created",
		"name":   fullName,
	}), nil
}

func handleZSnapshotList(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !zfsAvailable() {
		return errResult("ZFS is not available on this host."), nil
	}

	dataset := argString(req.GetArguments(), "dataset")

	args := []string{"list", "-t", "snapshot", "-o", "name,used,creation", "-r"}
	if dataset != "" {
		args = append(args, dataset)
	}

	out, err := runZfs(args...)
	if err != nil {
		return unwrapError(err), nil
	}

	snapshots := make([]ZFSSnapshot, 0)
	lines := splitNonEmpty(out)
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		snapshots = append(snapshots, ZFSSnapshot{
			Name:    fields[0],
			Used:    fields[1],
			Created: strings.Join(fields[2:], " "),
		})
	}
	return okResult(map[string]interface{}{
		"snapshots": snapshots,
		"total":     len(snapshots),
	}), nil
}

func handleZSnapshotDestroy(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !zfsAvailable() {
		return errResult("ZFS is not available on this host."), nil
	}

	name := argString(req.GetArguments(), "name")
	if name == "" {
		return errResult("name is required (format: pool/dataset@snap)"), nil
	}

	_, err := runZfs("destroy", name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{"status": "snapshot destroyed", "name": name}), nil
}

func handleZSnapshotClone(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !zfsAvailable() {
		return errResult("ZFS is not available on this host."), nil
	}

	snapshot := argString(req.GetArguments(), "snapshot")
	cloneName := argString(req.GetArguments(), "clone_name")
	if snapshot == "" || cloneName == "" {
		return errResult("snapshot and clone_name are required"), nil
	}

	_, err := runZfs("clone", snapshot, cloneName)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{
		"status":     "clone created",
		"snapshot":   snapshot,
		"clone_name": cloneName,
	}), nil
}

func handleZSnapshotRollback(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !zfsAvailable() {
		return errResult("ZFS is not available on this host."), nil
	}

	name := argString(req.GetArguments(), "name")
	if name == "" {
		return errResult("name is required (format: pool/dataset@snap)"), nil
	}

	_, err := runZfs("rollback", "-r", name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{"status": "rolled back", "name": name}), nil
}

// ---- Utility ----

// humanizeBytes converts a byte count string to human-readable format.
func humanizeBytes(bytes string) string {
	n, err := strconv.ParseInt(bytes, 10, 64)
	if err != nil {
		return bytes
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%c", float64(n)/float64(div), "KMGTPE"[exp])
}
