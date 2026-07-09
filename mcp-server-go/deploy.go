// Package main: persistent deploy operations (git-based deployment + volumes).
// Port of deploy.py.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// DeployManager manages git-based persistent deploys and volumes.
type DeployManager struct {
	client         ContainerBackend
	VolumesRoot    string
	WorkspacesRoot string
}

func newDeployManager(client ContainerBackend) *DeployManager {
	dm := &DeployManager{
		client:         client,
		VolumesRoot:    envOr("CUBE_VOLUMES_ROOT", "/volumes"),
		WorkspacesRoot: envOr("CUBE_WORKSPACES_ROOT", "/deploy-workspaces"),
	}
	os.MkdirAll(dm.VolumesRoot, 0755)
	os.MkdirAll(dm.WorkspacesRoot, 0755)
	return dm
}

// VolumeInfo describes a persistent volume.
type VolumeInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SizeMB    float64 `json:"size_mb"`
	FileCount int    `json:"file_count"`
}

// ListVolumes lists all persistent volumes with size and usage.
func (dm *DeployManager) ListVolumes() ([]VolumeInfo, error) {
	entries, err := os.ReadDir(dm.VolumesRoot)
	if err != nil {
		return nil, fmt.Errorf("read volumes root: %w", err)
	}
	var volumes []VolumeInfo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		volPath := filepath.Join(dm.VolumesRoot, entry.Name())
		var size int64
		var count int
		filepath.Walk(volPath, func(_ string, info os.FileInfo, err error) error {
			if err != nil || info == nil {
				return nil
			}
			if !info.IsDir() {
				size += info.Size()
				count++
			}
			return nil
		})
		volumes = append(volumes, VolumeInfo{
			Name:      entry.Name(),
			Path:      volPath,
			SizeBytes: size,
			SizeMB:    float64(size) / (1024.0 * 1024.0),
			FileCount: count,
		})
	}
	return volumes, nil
}

// CreateVolume creates a new persistent volume directory.
func (dm *DeployManager) CreateVolume(name string) (map[string]interface{}, error) {
	if _, err := validateSafeName(name); err != nil {
		return nil, err
	}
	path := filepath.Join(dm.VolumesRoot, name)
	if _, err := validatePathSafe(path, dm.VolumesRoot); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		return map[string]interface{}{
			"name":   name,
			"path":   path,
			"status": "already_exists",
		}, nil
	}
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("create volume: %w", err)
	}
	return map[string]interface{}{
		"name":   name,
		"path":   path,
		"status": "created",
	}, nil
}

// DeleteVolume deletes a persistent volume. WARNING: destroys all data.
func (dm *DeployManager) DeleteVolume(name string) (map[string]interface{}, error) {
	if _, err := validateSafeName(name); err != nil {
		return nil, err
	}
	path := filepath.Join(dm.VolumesRoot, name)
	if _, err := validatePathSafe(path, dm.VolumesRoot); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return map[string]interface{}{
			"name":   name,
			"status": "not_found",
		}, nil
	}
	if err := os.RemoveAll(path); err != nil {
		return nil, fmt.Errorf("delete volume: %w", err)
	}
	return map[string]interface{}{
		"name":   name,
		"status": "deleted",
	}, nil
}

// DeployFromGit deploys a service from a git repository with persistent storage.
func (dm *DeployManager) DeployFromGit(gitURL, branch, image string, exposePorts []int, envVars map[string]interface{}, startCmd, volumeName string, memoryMB int, cpuCount float64) (map[string]interface{}, error) {
	// Validate git URL
	validURL, err := validateGitURL(gitURL)
	if err != nil {
		return nil, err
	}
	gitURL = validURL

	appName := sanitizeGitURLForName(gitURL)
	if volumeName == "" {
		volumeName = appName
	} else {
		if _, err := validateSafeName(volumeName); err != nil {
			return nil, err
		}
	}

	// Clone/pull repo
	workspace := filepath.Join(dm.WorkspacesRoot, appName)
	if _, err := validatePathSafe(workspace, dm.WorkspacesRoot); err != nil {
		return nil, err
	}
	cloneResult, err := dm.gitCloneOrPull(gitURL, branch, workspace)
	if err != nil {
		return nil, err
	}

	// Create volume
	volume, err := dm.CreateVolume(volumeName)
	if err != nil {
		return nil, err
	}

	// Sync code to volume
	syncResult := dm.syncCode(workspace, filepath.Join(dm.VolumesRoot, volumeName))

	// Detect start command
	if startCmd == "" {
		startCmd = detectStartCmd(workspace)
	}

	// Create template with volume mount
	template, err := dm.client.CreateTemplateFromImage(
		image,
		exposePorts,
		1,
		[]map[string]interface{}{{
			"source":      fmt.Sprintf("%s/%s", dm.VolumesRoot, volumeName),
			"destination": "/app",
			"readonly":    false,
		}},
		mergeEnvVars(map[string]interface{}{
			"DEPLOY_SOURCE": "git",
			"GIT_URL":       gitURL,
			"GIT_BRANCH":    branch,
		}, envVars),
		startCmd,
	)
	if err != nil {
		return nil, err
	}

	templateID := extractID(template)

	// Create container
	container, err := dm.client.CreateSandbox(templateID, memoryMB, cpuCount, nil, map[string]interface{}{
		"app":     appName,
		"source":  "git",
		"git_url": gitURL,
		"branch":  branch,
		"volume":  volumeName,
	})
	if err != nil {
		return nil, err
	}

	// Extract container ID and commit hash for version tracking
	containerID := extractID(container)
	commitHash, _ := cloneResult["commit_hash"].(string)

	// Record deployment version for rollback support
	var versionInfo map[string]interface{}
	if versionMgr != nil {
		manifest, vErr := recordDeployment(appName, templateID, containerID, gitURL, branch, commitHash, startCmd)
		if vErr == nil && manifest != nil {
			versionInfo = map[string]interface{}{
				"version_number": manifest.VersionNumber,
			}
		}
	}

	return map[string]interface{}{
		"app_name":    appName,
		"volume":      volume,
		"clone":       cloneResult,
		"sync":        syncResult,
		"template_id": templateID,
		"container":   container,
		"start_cmd":   startCmd,
		"version":     versionInfo,
	}, nil
}

// DeployFromCode deploys from inline code files (no git needed).
func (dm *DeployManager) DeployFromCode(appName string, files map[string]string, image string, exposePorts []int, envVars map[string]interface{}, startCmd string, memoryMB int) (map[string]interface{}, error) {
	if _, err := validateSafeName(appName); err != nil {
		return nil, err
	}

	volume, err := dm.CreateVolume(appName)
	if err != nil {
		return nil, err
	}
	volumePath := filepath.Join(dm.VolumesRoot, appName)

	// Write files
	for filename, content := range files {
		filepath_, err := dm.safeWriteFile(volumePath, filename, content)
		if err != nil {
			return nil, fmt.Errorf("write %s: %w", filename, err)
		}
		_ = filepath_
	}

	// Detect start command
	if startCmd == "" {
		startCmd = detectStartCmd(volumePath)
	}

	// Create template
	template, err := dm.client.CreateTemplateFromImage(
		image,
		exposePorts,
		1,
		[]map[string]interface{}{{
			"source":      volumePath,
			"destination": "/app",
			"readonly":    false,
		}},
		envVars,
		startCmd,
	)
	if err != nil {
		return nil, err
	}

	templateID := extractID(template)

	// Create container
	var fileList []string
	for f := range files {
		fileList = append(fileList, f)
	}
	container, err := dm.client.CreateSandbox(templateID, memoryMB, 1.0, nil, map[string]interface{}{
		"app":    appName,
		"source": "inline_code",
		"volume": appName,
		"files":  fileList,
	})
	if err != nil {
		return nil, err
	}

	// Record deployment version for rollback support
	containerID := extractID(container)
	var versionInfo map[string]interface{}
	if versionMgr != nil {
		manifest, vErr := recordDeployment(appName, templateID, containerID, "", "", "", startCmd)
		if vErr == nil && manifest != nil {
			versionInfo = map[string]interface{}{
				"version_number": manifest.VersionNumber,
			}
		}
	}

	return map[string]interface{}{
		"app_name":      appName,
		"volume":        volume,
		"files_written": fileList,
		"template_id":   templateID,
		"container":     container,
		"start_cmd":     startCmd,
		"version":       versionInfo,
	}, nil
}

// UpdateCode pulls latest from git and syncs to the container's volume.
func (dm *DeployManager) UpdateCode(containerID, gitURL, branch string) (map[string]interface{}, error) {
	validURL, err := validateGitURL(gitURL)
	if err != nil {
		return nil, err
	}
	gitURL = validURL
	appName := sanitizeGitURLForName(gitURL)
	workspace := filepath.Join(dm.WorkspacesRoot, appName)
	volumePath := filepath.Join(dm.VolumesRoot, appName)

	cloneResult, err := dm.gitCloneOrPull(gitURL, branch, workspace)
	if err != nil {
		return nil, err
	}

	syncResult := dm.syncCode(workspace, volumePath)

	// Send restart signal
	var restartResult interface{}
	r, err := dm.client.ExecInSandbox(containerID, "kill -HUP 1 2>/dev/null || true", 10)
	if err == nil {
		restartResult = r
	}

	return map[string]interface{}{
		"container_id":   containerID,
		"git_pull":       cloneResult,
		"sync":           syncResult,
		"synced_to":      volumePath,
		"restart_signal": restartResult,
	}, nil
}

// ---- Internal helpers ----

func (dm *DeployManager) gitCloneOrPull(gitURL, branch, workspace string) (map[string]interface{}, error) {
	// Check git is available before attempting clone/pull
	if !isGitInstalled() {
		return nil, fmt.Errorf("git is not installed or not in PATH — required for deploy operations")
	}

	// Validate branch name to prevent option injection (H3).
	if err := validateBranchName(branch); err != nil {
		return nil, err
	}

	gitDir := filepath.Join(workspace, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		// Pull: fetch + reset
		runGit(workspace, "fetch", "origin", branch)
		output := runGit(workspace, "reset", "--hard", "origin/"+branch)
		commitHash := strings.TrimSpace(runGit(workspace, "rev-parse", "HEAD"))
		return map[string]interface{}{
			"action":      "pulled",
			"branch":      branch,
			"commit_hash": commitHash,
			"output":      truncate(output, 500),
		}, nil
	}
	// Clone — note the -- separator before positional args to prevent option injection.
	os.MkdirAll(workspace, 0755)
	output := runGit(workspace, "clone", "--depth", "1", "-b", branch, "--", gitURL, workspace)
	commitHash := strings.TrimSpace(runGit(workspace, "rev-parse", "HEAD"))
	return map[string]interface{}{
		"action":      "cloned",
		"branch":      branch,
		"commit_hash": commitHash,
		"output":      truncate(output, 500),
	}, nil
}

// validateBranchName ensures a git branch name is safe to pass as a CLI arg.
// Rejects names starting with - (option injection) and names with shell metacharacters.
func validateBranchName(branch string) error {
	if branch == "" {
		return fmt.Errorf("branch name cannot be empty")
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("branch name cannot start with '-' (would be interpreted as a git option)")
	}
	// Git branch names can contain: alphanumeric, -, _, ., /
	// But no spaces, no shell metacharacters, no backticks
	for _, c := range branch {
		if c == ' ' || c == '`' || c == '$' || c == '\\' || c == '"' || c == '\'' || c == ';' || c == '|' || c == '&' || c == '<' || c == '>' || c == '\n' {
			return fmt.Errorf("branch name contains invalid character: %q", c)
		}
	}
	if len(branch) > 200 {
		return fmt.Errorf("branch name too long (max 200 chars)")
	}
	return nil
}

func (dm *DeployManager) syncCode(source, dest string) map[string]interface{} {
	os.MkdirAll(dest, 0755)
	copied := 0
	entries, err := os.ReadDir(source)
	if err != nil {
		return map[string]interface{}{"error": err.Error(), "dest": dest}
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		srcPath := filepath.Join(source, entry.Name())
		dstPath := filepath.Join(dest, entry.Name())
		// Remove existing
		os.RemoveAll(dstPath)
		if entry.IsDir() {
			copyDir(srcPath, dstPath)
		} else {
			copyFile(srcPath, dstPath)
		}
		copied++
	}
	return map[string]interface{}{
		"files_synced": copied,
		"dest":         dest,
	}
}

func (dm *DeployManager) safeWriteFile(volumePath, filename, content string) (string, error) {
	// Validate filename: no path traversal
	if strings.Contains(filename, "..") {
		return "", fmt.Errorf("invalid filename: path traversal")
	}
	parts := strings.Split(filename, "/")
	for _, p := range parts {
		if p != "" {
			if _, err := validateSafeName(p); err != nil {
				return "", fmt.Errorf("invalid filename component '%s': %w", p, err)
			}
		}
	}
	fullPath := filepath.Join(volumePath, filename)
	if _, err := validatePathSafe(fullPath, volumePath); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return "", err
	}
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return "", err
	}
	return fullPath, nil
}

func runGit(dir string, args ...string) string {
	env := append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = env
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &out
	cmd.Run()
	return out.String()
}

func detectStartCmd(path string) string {
	// Python: uvicorn (FastAPI/Starlette)
	reqPath := filepath.Join(path, "requirements.txt")
	if data, err := os.ReadFile(reqPath); err == nil {
		reqs := strings.ToLower(string(data))
		if strings.Contains(reqs, "fastapi") || strings.Contains(reqs, "starlette") {
			return "pip install -r requirements.txt && cd /app && uvicorn main:app --host 0.0.0.0 --port 8000"
		}
		if strings.Contains(reqs, "flask") {
			return "pip install -r requirements.txt && cd /app && python main.py"
		}
	}
	// Python: pyproject.toml
	if _, err := os.Stat(filepath.Join(path, "pyproject.toml")); err == nil {
		return "cd /app && pip install -e . && uvicorn main:app --host 0.0.0.0 --port 8000"
	}
	// Node.js
	if _, err := os.Stat(filepath.Join(path, "package.json")); err == nil {
		return "cd /app && npm install && npm start"
	}
	// Go binary
	if _, err := os.Stat(filepath.Join(path, "app")); err == nil {
		return "cd /app && ./app"
	}
	if _, err := os.Stat(filepath.Join(path, "server")); err == nil {
		return "cd /app && ./server"
	}
	// Static
	if _, err := os.Stat(filepath.Join(path, "index.html")); err == nil {
		return "cd /app && python3 -m http.server 8000"
	}
	return "cd /app && python main.py 2>/dev/null || python3 -m http.server 8000"
}

// ---- Utility ----

func extractID(data interface{}) string {
	m, ok := data.(map[string]interface{})
	if !ok {
		return ""
	}
	for _, key := range []string{"templateID", "id", "sandboxID", "sandboxId"} {
		if v, exists := m[key]; exists {
			return fmt.Sprintf("%v", v)
		}
	}
	return ""
}

func mergeEnvVars(base, extra map[string]interface{}) map[string]interface{} {
	merged := make(map[string]interface{})
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(dstPath, info.Mode())
		}
		return copyFile(path, dstPath)
	})
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != recursiveIgnore(err, src) {
		return err
	}
	os.MkdirAll(filepath.Dir(dst), 0755)
	return os.WriteFile(dst, data, 0644)
}

// recursiveIgnore ignores errors for symlinks/unreadable files.
func recursiveIgnore(err error, _ string) error {
	if err == nil {
		return nil
	}
	// Skip symlinks and socket files
	if re, ok := err.(*os.PathError); ok {
		_ = re
		return nil
	}
	// Use regex to ignore common skip patterns
	if regexp.MustCompile(`permission denied|no such file`).MatchString(strings.ToLower(err.Error())) {
		return nil
	}
	return err
}

// toJSON is a helper for pretty-printing tool results.
func toJSON(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
