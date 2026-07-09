// Package main: volume lifecycle management — attach, detach, migrate, info.
//
// This file extends the DeployManager (which provides List/Create/Delete of
// volume directories) with runtime operations that the deploy layer does not
// cover:
//
//   - VolumeAttach: bind-mount a persistent volume into a RUNNING container.
//       Because neither Docker nor CubeAPI support hot-adding a bind mount to
//       a live container, attach is implemented as: read the current container
//       spec → create a new template that includes the extra mount → kill the
//       old container → create a fresh container from the new template with the
//       same resources/env/labels. The new container ID is returned.
//   - VolumeDetach: reverse of attach — recreate the container without the mount.
//   - VolumeMigrate: tar a volume directory and ship it to another cluster node
//       via scp, then register it there by creating the target directory.
//   - VolumeInfo: detailed view of a single volume, including the list of
//       containers that currently have it attached (from the attachment ledger).
//
// Attachments are persisted to /var/lib/cube-container/volumes/attachments.json
// so that the volume→container mapping survives restarts.
//
// The VolumeManager wraps the existing `deploy` global (*DeployManager) and
// borrows its ContainerBackend via deploy.client for container introspection.
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

// volumeMgr is the process-wide volume lifecycle manager.
// It is initialised in main() alongside the other managers.
var volumeMgr *VolumeManager

// ---- Types ----

// VolumeAttachment records that a volume is mounted into a container.
type VolumeAttachment struct {
	VolumeName  string    `json:"volume_name"`
	ContainerID string    `json:"container_id"`
	MountPath   string    `json:"mount_path"`
	AttachedAt  time.Time `json:"attached_at"`
}

// VolumeDetail is the enriched view returned by VolumeInfo. It embeds the
// basic VolumeInfo from deploy.go and adds the attachment ledger.
type VolumeDetail struct {
	Name        string             `json:"name"`
	Path        string             `json:"path"`
	SizeBytes   int64              `json:"size_bytes"`
	SizeMB      float64            `json:"size_mb"`
	FileCount   int                `json:"file_count"`
	Exists      bool               `json:"exists"`
	Attachments []VolumeAttachment `json:"attachments"`
}

// ---- Manager ----

// VolumeManager handles volume attach/detach/migrate/info.
// It wraps the DeployManager for directory-level volume operations and the
// ContainerBackend for container introspection/recreation.
type VolumeManager struct {
	mu              sync.Mutex
	deploy          *DeployManager
	client          ContainerBackend
	attachmentsFile string
	attachments     []VolumeAttachment
}

// newVolumeManager constructs a VolumeManager and loads persisted attachments.
func newVolumeManager(dm *DeployManager, c ContainerBackend) *VolumeManager {
	vm := &VolumeManager{
		deploy:          dm,
		client:          c,
		attachmentsFile: envOr("CUBE_VOLUME_ATTACHMENTS_FILE", "/var/lib/cube-container/volumes/attachments.json"),
	}
	vm.loadAttachments()
	return vm
}

// ---- Attachment persistence ----

// loadAttachments reads the attachment ledger from disk (best-effort).
func (vm *VolumeManager) loadAttachments() {
	data, err := os.ReadFile(vm.attachmentsFile)
	if err != nil {
		return // missing file is fine — start empty
	}
	var atts []VolumeAttachment
	if err := json.Unmarshal(data, &atts); err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] WARNING: failed to parse volume attachments: %v\n", err)
		return
	}
	vm.attachments = atts
}

// saveAttachments writes the attachment ledger atomically.
func (vm *VolumeManager) saveAttachments() error {
	if err := os.MkdirAll(filepath.Dir(vm.attachmentsFile), 0700); err != nil {
		return fmt.Errorf("create attachments dir: %w", err)
	}
	data, err := json.MarshalIndent(vm.attachments, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal attachments: %w", err)
	}
	tmp := vm.attachmentsFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write attachments tmp: %w", err)
	}
	return os.Rename(tmp, vm.attachmentsFile)
}

// ---- Attachment ledger helpers (callers must hold vm.mu) ----

// findAttachmentLocked returns the index of the attachment matching the given
// volume+container, or -1 if not found.
func (vm *VolumeManager) findAttachmentLocked(volumeName, containerID string) int {
	for i, a := range vm.attachments {
		if a.VolumeName == volumeName && a.ContainerID == containerID {
			return i
		}
	}
	return -1
}

// attachmentsForVolumeLocked returns all attachments for a given volume name.
func (vm *VolumeManager) attachmentsForVolumeLocked(volumeName string) []VolumeAttachment {
	var out []VolumeAttachment
	for _, a := range vm.attachments {
		if a.VolumeName == volumeName {
			out = append(out, a)
		}
	}
	return out
}

// pruneContainerLocked drops stale attachments whose container no longer matches
// the provided set of live container IDs.
func (vm *VolumeManager) pruneContainerLocked(liveIDs map[string]bool) {
	kept := vm.attachments[:0]
	for _, a := range vm.attachments {
		if liveIDs[a.ContainerID] {
			kept = append(kept, a)
		}
	}
	vm.attachments = kept
}

// ---- Container introspection ----

// containerSummary is the subset of container fields needed for attach/detach.
type containerSummary struct {
	ID        string
	Image     string
	MemoryMB  int
	CPUCount  float64
	Env       []string
	Labels    map[string]interface{}
	Mounts    []map[string]interface{}
}

// inspectContainer fetches a normalised summary of a container via the backend.
// Returns an error if the container does not exist or the backend fails.
func (vm *VolumeManager) inspectContainer(containerID string) (*containerSummary, error) {
	raw, err := vm.client.GetSandbox(containerID)
	if err != nil {
		return nil, err
	}
	m := asMap(raw)
	if len(m) == 0 {
		return nil, fmt.Errorf("container %s not found", containerID)
	}

	cs := &containerSummary{
		ID:       toString(m["sandboxID"]),
		Image:    toString(m["image"]),
		MemoryMB: int(toFloat(m["memoryMB"])),
	}

	// CPU count may be missing on some backends; default to 1.0.
	if cpu := toFloat(m["cpuCount"]); cpu > 0 {
		cs.CPUCount = cpu
	} else {
		cs.CPUCount = 1.0
	}

	// Env is a []interface{} of "KEY=VALUE" strings.
	if envRaw, ok := m["env"].([]interface{}); ok {
		cs.Env = make([]string, 0, len(envRaw))
		for _, e := range envRaw {
			cs.Env = append(cs.Env, toString(e))
		}
	}

	// Labels are already a map[string]interface{} from the backend.
	if labels, ok := m["labels"].(map[string]interface{}); ok {
		cs.Labels = labels
	}

	// Mounts may be a []interface{} of maps.
	if mountsRaw, ok := m["mounts"].([]interface{}); ok {
		for _, mr := range mountsRaw {
			if mm, ok := mr.(map[string]interface{}); ok {
				cs.Mounts = append(cs.Mounts, mm)
			}
		}
	}

	return cs, nil
}

// envSliceToMap converts a []string of "KEY=VALUE" into a map.
func envSliceToMap(env []string) map[string]interface{} {
	out := make(map[string]interface{}, len(env))
	for _, e := range env {
		if idx := strings.IndexByte(e, '='); idx >= 0 {
			out[e[:idx]] = e[idx+1:]
		}
	}
	return out
}

// mountSpec builds the deploy-style mount map for a host path → destination.
func mountSpec(source, destination string, readonly bool) map[string]interface{} {
	return map[string]interface{}{
		"source":      source,
		"destination": destination,
		"readonly":    readonly,
	}
}

// normaliseMounts converts backend mount entries (Docker shape) into the
// deploy-style {source, destination, readonly} form, preserving existing mounts.
func normaliseMounts(raw []map[string]interface{}) []map[string]interface{} {
	var out []map[string]interface{}
	for _, m := range raw {
		src := ""
		if v, ok := m["Source"]; ok {
			src = toString(v)
		} else if v, ok := m["source"]; ok {
			src = toString(v)
		}
		dst := ""
		if v, ok := m["Destination"]; ok {
			dst = toString(v)
		} else if v, ok := m["destination"]; ok {
			dst = toString(v)
		}
		if src == "" || dst == "" {
			continue
		}
		ro := false
		if v, ok := m["RW"]; ok {
			// Docker uses "RW" (bool) — readonly is !RW.
			if b, ok := v.(bool); ok {
				ro = !b
			}
		} else if v, ok := m["readonly"]; ok {
			if b, ok := v.(bool); ok {
				ro = b
			}
		}
		out = append(out, mountSpec(src, dst, ro))
	}
	return out
}

// recreateContainerWithMounts tears down containerID and creates a new container
// from the same image/resources with the supplied mount list. Returns the new
// container's raw backend result.
//
// This is the core mechanism for attach/detach: neither Docker nor CubeAPI can
// hot-add a bind mount to a running container, so we recreate it.
func (vm *VolumeManager) recreateContainerWithMounts(cs *containerSummary, mounts []map[string]interface{}) (interface{}, error) {
	// Build a fresh template that bakes in the desired mounts.
	envMap := envSliceToMap(cs.Env)
	tmpl, err := vm.client.CreateTemplateFromImage(
		cs.Image,
		nil, // ports are encoded in the image; pass nil to inherit
		1,   // writable layer size GB (matches deploy.go default)
		mounts,
		envMap,
		"", // start command inherited from image
	)
	if err != nil {
		return nil, fmt.Errorf("create template with mounts: %w", err)
	}
	newTemplateID := extractID(tmpl)
	if newTemplateID == "" {
		return nil, fmt.Errorf("created template returned no ID")
	}

	// Kill the old container (best-effort — ignore not-found).
	if _, err := vm.client.KillSandbox(cs.ID); err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] attach: kill old container %s failed (continuing): %v\n", cs.ID, err)
	}

	// Create the replacement container with identical resources.
	result, err := vm.client.CreateSandbox(newTemplateID, cs.MemoryMB, cs.CPUCount, envMap, cs.Labels)
	if err != nil {
		return nil, fmt.Errorf("recreate container: %w", err)
	}
	return result, nil
}

// ---- Volume operations ----

// VolumeAttach attaches a persistent volume to a container at mountPath.
// The container is recreated with the additional bind mount; the new container
// ID is returned in the result map under "new_container_id".
func (vm *VolumeManager) VolumeAttach(containerID, volumeName, mountPath string) (map[string]interface{}, error) {
	if containerID == "" {
		return nil, fmt.Errorf("container_id is required")
	}
	if _, err := validateSafeName(volumeName); err != nil {
		return nil, err
	}
	if mountPath == "" {
		return nil, fmt.Errorf("mount_path is required")
	}
	if !strings.HasPrefix(mountPath, "/") {
		return nil, fmt.Errorf("mount_path must be an absolute path (got %q)", mountPath)
	}

	// Verify the volume directory exists.
	volPath := filepath.Join(vm.deploy.VolumesRoot, volumeName)
	if _, err := os.Stat(volPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("volume %q does not exist at %s", volumeName, volPath)
	}

	// Inspect the container.
	cs, err := vm.inspectContainer(containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect container: %w", err)
	}

	// Build the new mount list: existing mounts + the new volume mount.
	mounts := normaliseMounts(cs.Mounts)
	// Avoid duplicate destinations.
	for _, m := range mounts {
		if toString(m["destination"]) == mountPath {
			return nil, fmt.Errorf("container %s already has a mount at %s", containerID, mountPath)
		}
	}
	mounts = append(mounts, mountSpec(volPath, mountPath, false))

	// Recreate the container with the combined mounts.
	result, err := vm.recreateContainerWithMounts(cs, mounts)
	if err != nil {
		return nil, err
	}

	// Extract the new container ID for ledger + return value.
	newID := extractID(result)
	if newID == "" {
		newID = containerID // fallback if backend didn't echo an ID
	}

	// Update the attachment ledger.
	vm.mu.Lock()
	// Remove any stale attachment for the OLD container id.
	if idx := vm.findAttachmentLocked(volumeName, containerID); idx >= 0 {
		vm.attachments = append(vm.attachments[:idx], vm.attachments[idx+1:]...)
	}
	vm.attachments = append(vm.attachments, VolumeAttachment{
		VolumeName:  volumeName,
		ContainerID: newID,
		MountPath:   mountPath,
		AttachedAt:  time.Now().UTC(),
	})
	if err := vm.saveAttachments(); err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] attach: failed to persist attachments: %v\n", err)
	}
	vm.mu.Unlock()

	return map[string]interface{}{
		"volume_name":      volumeName,
		"container_id":     containerID,
		"new_container_id": newID,
		"mount_path":       mountPath,
		"source":           volPath,
		"status":           "attached",
	}, nil
}

// VolumeDetach removes a volume mount from a container by recreating the
// container without that mount.
func (vm *VolumeManager) VolumeDetach(containerID, volumeName string) (map[string]interface{}, error) {
	if containerID == "" {
		return nil, fmt.Errorf("container_id is required")
	}
	if _, err := validateSafeName(volumeName); err != nil {
		return nil, err
	}

	// Inspect the container.
	cs, err := vm.inspectContainer(containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect container: %w", err)
	}

	volPath := filepath.Join(vm.deploy.VolumesRoot, volumeName)

	// Build the mount list excluding the volume being detached.
	existing := normaliseMounts(cs.Mounts)
	var kept []map[string]interface{}
	removed := false
	for _, m := range existing {
		if toString(m["source"]) == volPath {
			removed = true
			continue
		}
		kept = append(kept, m)
	}
	if !removed {
		return map[string]interface{}{
			"volume_name":  volumeName,
			"container_id": containerID,
			"status":       "not_attached",
		}, nil
	}

	// Recreate the container without the volume mount.
	result, err := vm.recreateContainerWithMounts(cs, kept)
	if err != nil {
		return nil, err
	}
	newID := extractID(result)
	if newID == "" {
		newID = containerID
	}

	// Update the ledger: remove entries for both old and new container id.
	vm.mu.Lock()
	for _, id := range []string{containerID, newID} {
		for {
			idx := vm.findAttachmentLocked(volumeName, id)
			if idx < 0 {
				break
			}
			vm.attachments = append(vm.attachments[:idx], vm.attachments[idx+1:]...)
		}
	}
	if err := vm.saveAttachments(); err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] detach: failed to persist attachments: %v\n", err)
	}
	vm.mu.Unlock()

	return map[string]interface{}{
		"volume_name":      volumeName,
		"container_id":     containerID,
		"new_container_id": newID,
		"status":           "detached",
	}, nil
}

// VolumeMigrate copies a volume to a remote cluster node via tar+scp, then
// registers it there by ensuring the target directory exists.
//
// The fromNode/toNode arguments are node IDs registered in nodeRegistry.
// fromNode is informational (the volume must already be local on this host);
// toNode must be reachable via SSH for scp to succeed.
func (vm *VolumeManager) VolumeMigrate(volumeName, fromNode, toNode string) (map[string]interface{}, error) {
	if _, err := validateSafeName(volumeName); err != nil {
		return nil, err
	}
	if toNode == "" {
		return nil, fmt.Errorf("to_node is required")
	}

	// Verify source volume exists locally.
	srcPath := filepath.Join(vm.deploy.VolumesRoot, volumeName)
	if _, err := os.Stat(srcPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("volume %q does not exist locally at %s", volumeName, srcPath)
	}

	// Resolve target node.
	targetNode, err := nodeRegistry.getNode(toNode)
	if err != nil {
		return nil, fmt.Errorf("resolve target node: %w", err)
	}
	targetHost := targetNode.Hostname
	if targetHost == "" {
		// Fall back to the address (host:port) stripped of the port.
		targetHost = strings.SplitN(targetNode.Address, ":", 2)[0]
	}
	if targetHost == "" {
		return nil, fmt.Errorf("could not determine SSH host for node %s", toNode)
	}

	// The remote path mirrors the local layout.
	remoteVolRoot := vm.deploy.VolumesRoot
	remotePath := filepath.Join(remoteVolRoot, volumeName)

	// Step 1: create a tarball of the volume in a temp file.
	tmpTar, err := os.CreateTemp("", "cube-vol-migrate-*.tar")
	if err != nil {
		return nil, fmt.Errorf("create temp tar file: %w", err)
	}
	tmpTarPath := tmpTar.Name()
	tmpTar.Close()
	defer os.Remove(tmpTarPath)

	tarCmd := exec.Command("tar", "-cf", tmpTarPath, "-C", vm.deploy.VolumesRoot, volumeName)
	if out, err := tarCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("tar volume: %w: %s", err, truncate(string(out), 500))
	}

	tarInfo, _ := os.Stat(tmpTarPath)
	tarSize := int64(0)
	if tarInfo != nil {
		tarSize = tarInfo.Size()
	}

	// Step 2: ensure the remote volume root exists, then scp the tarball.
	// Use ssh to mkdir -p the remote root.
	mkdirCmd := exec.Command("ssh",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		targetHost,
		"mkdir -p "+shellQuote(remoteVolRoot),
	)
	if out, err := mkdirCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ssh mkdir on %s: %w: %s", targetHost, err, truncate(string(out), 500))
	}

	// Step 3: copy the tarball to the remote root.
	remoteTar := filepath.Join(remoteVolRoot, ".__migrate_"+volumeName+".tar")
	scpCmd := exec.Command("scp",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		tmpTarPath, targetHost+":"+remoteTar,
	)
	if out, err := scpCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("scp tarball to %s: %w: %s", targetHost, err, truncate(string(out), 500))
	}

	// Step 4: extract on the remote node and clean up the tarball.
	extractCmd := exec.Command("ssh",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		targetHost,
		fmt.Sprintf("tar -xf %s -C %s && rm -f %s",
			shellQuote(remoteTar), shellQuote(remoteVolRoot), shellQuote(remoteTar)),
	)
	if out, err := extractCmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("remote extract on %s: %w: %s", targetHost, err, truncate(string(out), 500))
	}

	// Mark the target node as seen.
	nodeRegistry.markSeen(toNode)

	return map[string]interface{}{
		"volume_name":  volumeName,
		"from_node":    fromNode,
		"to_node":      toNode,
		"to_host":      targetHost,
		"remote_path":  remotePath,
		"tar_size":     tarSize,
		"status":       "migrated",
	}, nil
}

// VolumeInfo returns detailed information about a single volume, including the
// list of containers that currently have it attached (per the ledger).
func (vm *VolumeManager) VolumeInfo(volumeName string) (*VolumeDetail, error) {
	if _, err := validateSafeName(volumeName); err != nil {
		return nil, err
	}

	detail := &VolumeDetail{
		Name:        volumeName,
		Path:        filepath.Join(vm.deploy.VolumesRoot, volumeName),
		Attachments: []VolumeAttachment{},
	}

	// Walk the directory to compute size + file count (mirrors deploy.ListVolumes).
	volPath := detail.Path
	if info, err := os.Stat(volPath); err == nil && info.IsDir() {
		detail.Exists = true
		filepath.Walk(volPath, func(_ string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if !info.IsDir() {
				detail.SizeBytes += info.Size()
				detail.FileCount++
			}
			return nil
		})
		detail.SizeMB = float64(detail.SizeBytes) / (1024.0 * 1024.0)
	}

	// Attach the ledger entries for this volume.
	vm.mu.Lock()
	detail.Attachments = vm.attachmentsForVolumeLocked(volumeName)
	vm.mu.Unlock()

	return detail, nil
}

// PruneStaleAttachments removes ledger entries for containers that no longer
// exist. Callers pass the set of currently-live container IDs.
func (vm *VolumeManager) PruneStaleAttachments(liveIDs map[string]bool) {
	vm.mu.Lock()
	defer vm.mu.Unlock()
	before := len(vm.attachments)
	vm.pruneContainerLocked(liveIDs)
	if len(vm.attachments) != before {
		if err := vm.saveAttachments(); err != nil {
			fmt.Fprintf(os.Stderr, "[cube-mcp] prune attachments: %v\n", err)
		}
	}
}

// shellQuote single-quotes a string for safe interpolation into a shell command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

// ---- Tool handlers: Volume lifecycle ----

// handleVolumeAttach attaches a persistent volume to a running container.
// The container is recreated with the additional mount; the new container ID
// is returned.
func handleVolumeAttach(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	volumeName := argString(args, "volume_name")
	if volumeName == "" {
		return errResult("volume_name is required"), nil
	}
	mountPath := argString(args, "mount_path")
	if mountPath == "" {
		return errResult("mount_path is required"), nil
	}

	data, err := volumeMgr.VolumeAttach(containerID, volumeName, mountPath)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// handleVolumeDetach detaches a volume from a container by recreating the
// container without the mount.
func handleVolumeDetach(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	volumeName := argString(args, "volume_name")
	if volumeName == "" {
		return errResult("volume_name is required"), nil
	}

	data, err := volumeMgr.VolumeDetach(containerID, volumeName)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// handleVolumeMigrate copies a volume to a remote cluster node via tar+scp.
func handleVolumeMigrate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	volumeName := argString(args, "volume_name")
	if volumeName == "" {
		return errResult("volume_name is required"), nil
	}
	fromNode := argString(args, "from_node")
	toNode := argString(args, "to_node")
	if toNode == "" {
		return errResult("to_node is required"), nil
	}

	data, err := volumeMgr.VolumeMigrate(volumeName, fromNode, toNode)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// handleVolumeInfo returns detailed information about a single volume,
// including which containers have it attached.
func handleVolumeInfo(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	volumeName := argString(args, "volume_name")
	if volumeName == "" {
		return errResult("volume_name is required"), nil
	}

	data, err := volumeMgr.VolumeInfo(volumeName)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}
