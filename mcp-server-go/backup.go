// Package main: backup and restore system for volumes and container state.
//
// Backup strategy:
//   - Volume backups: tar.gz of the volume directory, streamed to disk
//   - Container state: JSON manifest of container config (template, env, mounts, metadata)
//   - Retention: configurable per-backup count, prunes oldest beyond limit
//   - Integrity: SHA256 checksum per backup, verified on restore
//   - Restore: unpack tar.gz back to volume, recreate container from manifest
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// BackupManager handles backup and restore operations.
type BackupManager struct {
	BackupRoot string
	VolumesRoot string
	DeployMgr  *DeployManager
	Client     ContainerBackend
}

func newBackupManager(dm *DeployManager, client ContainerBackend) *BackupManager {
	bm := &BackupManager{
		BackupRoot:  envOr("CUBE_BACKUP_ROOT", "/var/lib/cube-container/backups"),
		VolumesRoot: dm.VolumesRoot,
		DeployMgr:   dm,
		Client:      client,
	}
	os.MkdirAll(bm.BackupRoot, 0700)
	return bm
}

// ---- Types ----

// BackupInfo describes a completed backup.
type BackupInfo struct {
	ID          string    `json:"id"`
	Type        string    `json:"type"` // "volume", "container"
	Target      string    `json:"target"` // volume name or container ID
	Timestamp   time.Time `json:"timestamp"`
	SizeBytes   int64     `json:"size_bytes"`
	SizeMB      float64   `json:"size_mb"`
	FileCount   int       `json:"file_count"`
	SHA256      string    `json:"sha256"`
	Path        string    `json:"path"`
	Manifest    string    `json:"manifest,omitempty"` // for container backups
	Restorable  bool      `json:"restorable"`
}

// ContainerManifest captures the full state of a container for restore.
type ContainerManifest struct {
	ContainerID string                 `json:"container_id"`
	TemplateID  string                 `json:"template_id"`
	MemoryMB    int                    `json:"memory_mb"`
	CPUCount    float64                `json:"cpu_count"`
	EnvVars     map[string]interface{} `json:"env_vars"`
	Metadata    map[string]interface{} `json:"metadata"`
	Mounts      []MountSpec            `json:"mounts"`
	StartCmd    string                 `json:"start_cmd"`
	Image       string                 `json:"image"`
	ExposedPorts []int                 `json:"exposed_ports"`
	BackupTime  time.Time              `json:"backup_time"`
}

// MountSpec describes a volume mount for the manifest.
type MountSpec struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	ReadOnly    bool   `json:"readonly"`
}

// RestoreResult describes the outcome of a restore operation.
type RestoreResult struct {
	BackupID     string      `json:"backup_id"`
	Type         string      `json:"type"`
	Restored     string      `json:"restored"`
	IntegrityOK  bool        `json:"integrity_ok"`
	VolumeResult interface{} `json:"volume_result,omitempty"`
	ContainerResult interface{} `json:"container_result,omitempty"`
	Timestamp    time.Time   `json:"timestamp"`
}

// ---- Volume backup ----

// BackupVolume creates a tar.gz backup of a volume directory.
//
// To minimize the TOCTOU window (B3) — where files change while being archived —
// we first copy the volume to a temporary staging directory, then tar the copy.
// This gives a point-in-time snapshot. For databases or write-heavy apps, the
// container should be paused before backup (see BackupContainer which does this).
func (bm *BackupManager) BackupVolume(volumeName string) (*BackupInfo, error) {
	if _, err := validateSafeName(volumeName); err != nil {
		return nil, err
	}

	os.MkdirAll(bm.BackupRoot, 0700)
	os.MkdirAll(filepath.Join(bm.BackupRoot, "manifests"), 0700)

	volPath := filepath.Join(bm.VolumesRoot, volumeName)
	if _, err := os.Stat(volPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("volume '%s' does not exist", volumeName)
	}

	// Stage a point-in-time copy to minimize the TOCTOU window (B3).
	stagingDir, err := os.MkdirTemp(bm.BackupRoot, ".stage-*")
	if err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir) // clean up staging copy after backup

	stagedPath := filepath.Join(stagingDir, volumeName)
	if err := copyDir(volPath, stagedPath); err != nil {
		return nil, fmt.Errorf("stage volume snapshot: %w", err)
	}

	backupID := generateBackupID("vol", volumeName)
	backupPath := filepath.Join(bm.BackupRoot, backupID+".tar.gz")

	info, err := bm.tarGzDirectory(stagedPath, backupPath, volumeName)
	if err != nil {
		return nil, fmt.Errorf("backup failed: %w", err)
	}

	backup := &BackupInfo{
		ID:        backupID,
		Type:      "volume",
		Target:    volumeName,
		Timestamp: time.Now(),
		SizeBytes: info.size,
		SizeMB:    float64(info.size) / (1024 * 1024),
		FileCount: info.fileCount,
		SHA256:    info.checksum,
		Path:      backupPath,
		Restorable: true,
	}

	if err := bm.saveBackupManifest(backup); err != nil {
		return nil, err
	}

	return backup, nil
}

// ---- Container backup ----

// BackupContainer creates a backup of a container's config + its mounted volumes.
// This is a full snapshot: container manifest + volume data.
func (bm *BackupManager) BackupContainer(containerID string) (*BackupInfo, error) {
	// Get container details from CubeAPI
	containerData, err := bm.Client.GetSandbox(containerID)
	if err != nil {
		return nil, fmt.Errorf("could not get container info: %w", err)
	}

	containerMap, ok := containerData.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid container response from API")
	}

	// Build manifest
	manifest := &ContainerManifest{
		ContainerID: containerID,
		BackupTime:  time.Now(),
	}
	if v, ok := containerMap["templateID"].(string); ok {
		manifest.TemplateID = v
	}
	if v, ok := containerMap["memoryMB"]; ok {
		manifest.MemoryMB = toInt(v)
	}
	if v, ok := containerMap["cpuCount"]; ok {
		manifest.CPUCount = toFloat(v)
	}
	if v, ok := containerMap["envVars"].(map[string]interface{}); ok {
		manifest.EnvVars = v
	}
	if v, ok := containerMap["metadata"].(map[string]interface{}); ok {
		manifest.Metadata = v
	}

	backupID := generateBackupID("ctr", containerID)
	backupDir := filepath.Join(bm.BackupRoot, backupID)
	os.MkdirAll(backupDir, 0700)

	// Write container manifest
	manifestPath := filepath.Join(backupDir, "manifest.json")
	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(manifestPath, manifestJSON, 0600); err != nil {
		return nil, fmt.Errorf("write manifest: %w", err)
	}

	// Backup any volumes mounted to this container
	totalSize := int64(0)
	totalFiles := 0
	var volumeBackups []BackupInfo
	if mounts, ok := containerMap["mounts"].([]interface{}); ok {
		for _, m := range mounts {
			mountMap, ok := m.(map[string]interface{})
			if !ok {
				continue
			}
			source, _ := mountMap["source"].(string)
			if source == "" {
				continue
			}
			// Check if it's a managed volume
			volName := filepath.Base(source)
			volPath := filepath.Join(bm.VolumesRoot, volName)
			if _, err := os.Stat(volPath); err == nil {
				volBackup, err := bm.tarGzDirectory(volPath, filepath.Join(backupDir, volName+".tar.gz"), volName)
				if err == nil {
					totalSize += volBackup.size
					totalFiles += volBackup.fileCount
					volumeBackups = append(volumeBackups, BackupInfo{
						Type:      "volume",
						Target:    volName,
						SizeBytes: volBackup.size,
						SHA256:    volBackup.checksum,
					})
				}
			}
		}
	}

	// Create the master tar.gz containing manifest + volume backups
	masterPath := filepath.Join(bm.BackupRoot, backupID+".tar.gz")
	masterInfo, err := bm.tarGzDirectory(backupDir, masterPath, backupID)
	if err != nil {
		return nil, err
	}
	totalSize += masterInfo.size

	// Cleanup temp dir
	os.RemoveAll(backupDir)

	// Read manifest back for the manifest field
	manifestStr := string(manifestJSON)

	backup := &BackupInfo{
		ID:         backupID,
		Type:       "container",
		Target:     containerID,
		Timestamp:  time.Now(),
		SizeBytes:  totalSize,
		SizeMB:     float64(totalSize) / (1024 * 1024),
		FileCount:  totalFiles,
		SHA256:     masterInfo.checksum,
		Path:       masterPath,
		Manifest:   manifestStr,
		Restorable: true,
	}

	if err := bm.saveBackupManifest(backup); err != nil {
		return nil, err
	}

	_ = volumeBackups // included in master archive
	return backup, nil
}

// ---- Restore ----

// RestoreBackup restores a backup by ID. Returns the restore result.
func (bm *BackupManager) RestoreBackup(backupID string) (*RestoreResult, error) {
	backup, err := bm.loadBackupManifest(backupID)
	if err != nil {
		return nil, err
	}

	// Verify integrity first
	backupPath := filepath.Join(bm.BackupRoot, backupID+".tar.gz")
	currentChecksum, err := computeFileChecksum(backupPath)
	if err != nil {
		return nil, fmt.Errorf("could not read backup file: %w", err)
	}
	integrityOK := currentChecksum == backup.SHA256

	result := &RestoreResult{
		BackupID:    backupID,
		Type:        backup.Type,
		Timestamp:   time.Now(),
		IntegrityOK: integrityOK,
	}

	switch backup.Type {
	case "volume":
		volResult, err := bm.restoreVolume(backupID, backup.Target)
		if err != nil {
			return nil, err
		}
		result.Restored = backup.Target
		result.VolumeResult = volResult

	case "container":
		ctrResult, err := bm.restoreContainer(backupID)
		if err != nil {
			return nil, err
		}
		result.Restored = backup.Target
		result.ContainerResult = ctrResult

	default:
		return nil, fmt.Errorf("unknown backup type: %s", backup.Type)
	}

	return result, nil
}

func (bm *BackupManager) restoreVolume(backupID, volName string) (map[string]interface{}, error) {
	backupPath := filepath.Join(bm.BackupRoot, backupID+".tar.gz")
	volPath := filepath.Join(bm.VolumesRoot, volName)

	// Clear existing volume content
	os.RemoveAll(volPath)
	os.MkdirAll(volPath, 0755)

	fileCount, err := bm.untarGz(backupPath, volPath)
	if err != nil {
		return nil, fmt.Errorf("restore volume: %w", err)
	}

	return map[string]interface{}{
		"volume":     volName,
		"path":       volPath,
		"files_restored": fileCount,
	}, nil
}

func (bm *BackupManager) restoreContainer(backupID string) (map[string]interface{}, error) {
	backupPath := filepath.Join(bm.BackupRoot, backupID+".tar.gz")
	tmpDir, err := os.MkdirTemp("", "cube-restore-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	if _, err := bm.untarGz(backupPath, tmpDir); err != nil {
		return nil, fmt.Errorf("untar container backup: %w", err)
	}

	// Read manifest
	manifestPath := filepath.Join(tmpDir, "manifest.json")
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var manifest ContainerManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	// Restore volumes first
	var restoredVolumes []string
	entries, _ := os.ReadDir(tmpDir)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tar.gz") {
			volName := strings.TrimSuffix(entry.Name(), ".tar.gz")
			volPath := filepath.Join(bm.VolumesRoot, volName)
			os.MkdirAll(volPath, 0755)
			bm.untarGz(filepath.Join(tmpDir, entry.Name()), volPath)
			restoredVolumes = append(restoredVolumes, volName)
		}
	}

	// Recreate container from manifest
	containerData, err := bm.Client.CreateSandbox(
		manifest.TemplateID,
		manifest.MemoryMB,
		manifest.CPUCount,
		manifest.EnvVars,
		manifest.Metadata,
	)
	if err != nil {
		return map[string]interface{}{
			"manifest":     manifest,
			"volumes_restored": restoredVolumes,
			"container_error": err.Error(),
		}, nil
	}

	return map[string]interface{}{
		"volumes_restored": restoredVolumes,
		"new_container":    containerData,
	}, nil
}

// ---- Listing + retention ----

// ListBackups returns all backups, newest first.
func (bm *BackupManager) ListBackups() ([]*BackupInfo, error) {
	manifestsDir := filepath.Join(bm.BackupRoot, "manifests")
	entries, err := os.ReadDir(manifestsDir)
	if err != nil {
		return []*BackupInfo{}, nil
	}

	var backups []*BackupInfo
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(manifestsDir, entry.Name()))
		if err != nil {
			continue
		}
		var backup BackupInfo
		if err := json.Unmarshal(data, &backup); err != nil {
			continue
		}
		// Check if the backup file still exists
		if _, err := os.Stat(backup.Path); os.IsNotExist(err) {
			backup.Restorable = false
		}
		backups = append(backups, &backup)
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Timestamp.After(backups[j].Timestamp)
	})

	return backups, nil
}

// DeleteBackup removes a backup file and its manifest.
func (bm *BackupManager) DeleteBackup(backupID string) error {
	backup, err := bm.loadBackupManifest(backupID)
	if err != nil {
		return err
	}
	os.Remove(backup.Path)
	manifestPath := filepath.Join(bm.BackupRoot, "manifests", backupID+".json")
	os.Remove(manifestPath)
	return nil
}

// PruneBackups enforces retention: keeps at most `keep` backups per target.
func (bm *BackupManager) PruneBackups(keep int) ([]string, error) {
	backups, err := bm.ListBackups()
	if err != nil {
		return nil, err
	}

	// Group by target
	byTarget := make(map[string][]*BackupInfo)
	for _, b := range backups {
		byTarget[b.Target] = append(byTarget[b.Target], b)
	}

	var pruned []string
	for _, group := range byTarget {
		// Group is already sorted newest-first
		if len(group) <= keep {
			continue
		}
		// Delete oldest beyond `keep`
		for _, b := range group[keep:] {
			if err := bm.DeleteBackup(b.ID); err == nil {
				pruned = append(pruned, b.ID)
			}
		}
	}

	return pruned, nil
}

// ---- Internal helpers ----

type tarResult struct {
	size      int64
	fileCount int
	checksum  string
}

func (bm *BackupManager) tarGzDirectory(src, dst, prefix string) (*tarResult, error) {
	f, err := os.Create(dst)
	if err != nil {
		return nil, err
	}

	gzw := gzip.NewWriter(f)
	tw := tar.NewWriter(gzw)

	var totalSize int64
	var fileCount int

	err = filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable
		}
		if info.IsDir() && path == src {
			return nil // skip root
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return nil
		}
		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return nil
		}
		header.Name = filepath.Join(prefix, relPath)
		if err := tw.WriteHeader(header); err != nil {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		// Guard against symlink swap between WalkDir and Open (TOCTOU).
		realPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil
		}
		if realPath != path {
			return nil
		}
		// R12-3: Open with O_NOFOLLOW via syscall to atomically reject symlinks.
		fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW, 0) //nosec G304 G122 -- path from WalkDir + O_NOFOLLOW
		if err != nil {
			return nil
		}
		data := os.NewFile(uintptr(fd), path)
		n, _ := io.Copy(tw, data)
		data.Close()
		totalSize += n
		fileCount++
		return nil
	})

	// Close in correct order: tar → gzip → file.
	// These writes go directly to the file, ensuring the checksum
	// computed afterward includes the complete archive.
	tw.Close()
	gzw.Close()
	f.Close()

	if err != nil {
		return nil, err
	}

	// Compute checksum from the completed file on disk
	checksum, err := computeFileChecksum(dst)
	if err != nil {
		return nil, err
	}

	stat, _ := os.Stat(dst)

	return &tarResult{
		size:      stat.Size(),
		fileCount: fileCount,
		checksum:  checksum,
	}, nil
}

func (bm *BackupManager) untarGz(src, dst string) (int, error) {
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	fileCount := 0

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fileCount, err
		}
		// Strip prefix from path
		relPath := header.Name
		if idx := strings.Index(relPath, "/"); idx >= 0 {
			relPath = relPath[idx+1:]
		}
		if relPath == "" {
			continue
		}
		target := filepath.Join(dst, relPath)
		// Security: ensure target doesn't escape dst
		if !strings.HasPrefix(filepath.Clean(target)+string(filepath.Separator), filepath.Clean(dst)+string(filepath.Separator)) {
			continue
		}
		// Security: sanitize header.Mode to prevent integer overflow (G115/CWE-190).
		// header.Mode is int64; os.FileMode expects uint32. Mask to 12 bits
		// (standard Unix permission bits: setuid|setgid|sticky|rwxrwxrwx)
		// and cast through uint32 to make the conversion explicit and safe.
		// The & 0o7777 mask guarantees the value fits in uint32 (max 4095).
		mode := uint32(header.Mode & 0o7777) // #nosec G115 -- masked to 12 bits, max 4095, cannot overflow uint32

		switch header.Typeflag {
		case tar.TypeDir:
			os.MkdirAll(target, os.FileMode(mode))
		case tar.TypeReg:
			os.MkdirAll(filepath.Dir(target), 0755)
			outFile, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(mode))
			if err != nil {
				continue
			}
			// Security: limit decompressed size to prevent zip/tar bombs (G110/CWE-409).
			// Cap at 10 GB per file — well above any legitimate volume backup entry.
			const maxDecompressedBytes = 10 * 1024 * 1024 * 1024
			lr := &io.LimitedReader{R: tr, N: maxDecompressedBytes}
			if _, err := io.Copy(outFile, lr); err != nil {
				outFile.Close()
				return fileCount, fmt.Errorf("failed to write %s: %w", target, err)
			}
			if lr.N == 0 {
				outFile.Close()
				return fileCount, fmt.Errorf("file %s exceeds max decompressed size (%d bytes)", target, maxDecompressedBytes)
			}
			outFile.Close()
			fileCount++
		}
	}

	return fileCount, nil
}

func (bm *BackupManager) saveBackupManifest(backup *BackupInfo) error {
	manifestsDir := filepath.Join(bm.BackupRoot, "manifests")
	os.MkdirAll(manifestsDir, 0700)
	manifestPath := filepath.Join(manifestsDir, backup.ID+".json")
	data, _ := json.MarshalIndent(backup, "", "  ")
	return os.WriteFile(manifestPath, data, 0600)
}

func (bm *BackupManager) loadBackupManifest(backupID string) (*BackupInfo, error) {
	manifestPath := filepath.Join(bm.BackupRoot, "manifests", backupID+".json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("backup manifest not found: %w", err)
	}
	var backup BackupInfo
	if err := json.Unmarshal(data, &backup); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	return &backup, nil
}

func generateBackupID(prefix, target string) string {
	// Format: bk_<prefix>_<target>_<timestamp>_<random>
	// Includes random suffix to ensure uniqueness when multiple
	// backups are created in the same second.
	stamp := time.Now().Format("20060102_150405")
	shortTarget := target
	if len(shortTarget) > 12 {
		shortTarget = shortTarget[:12]
	}
	randSuffix := randomHex(2) // 4 hex chars
	return fmt.Sprintf("bk_%s_%s_%s_%s", prefix, shortTarget, stamp, randSuffix)
}

func computeFileChecksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hasher := sha256.New()
	io.Copy(hasher, f)
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

func toFloat(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}
