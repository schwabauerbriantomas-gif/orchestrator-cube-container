// Package main: deployment versioning and rollback.
//
// Tracks each deployment as an append-only JSONL version history per app,
// allowing rollback to a previous version by re-deploying from its recorded
// git_url + branch. Each version manifest captures enough state to fully
// reconstruct the deployment (template, container, git provenance, start cmd).
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Types ----

// VersionManifest records a single deployment version for an app.
type VersionManifest struct {
	AppName       string    `json:"app_name"`
	VersionNumber int       `json:"version_number"`
	Timestamp     time.Time `json:"timestamp"`
	TemplateID    string    `json:"template_id"`
	ContainerID   string    `json:"container_id"`
	GitURL        string    `json:"git_url"`
	Branch        string    `json:"branch"`
	CommitHash    string    `json:"commit_hash"`
	StartCmd      string    `json:"start_cmd"`
}

// RollbackResult describes the outcome of a rollback operation.
type RollbackResult struct {
	AppName         string           `json:"app_name"`
	RolledBackFrom  *VersionManifest `json:"rolled_back_from"`
	RolledBackTo    *VersionManifest `json:"rolled_back_to"`
	OldContainerID  string           `json:"old_container_id"`
	NewContainerID  string           `json:"new_container_id"`
	Redeploy        interface{}      `json:"redeploy,omitempty"`
	Timestamp       time.Time        `json:"timestamp"`
}

// ---- VersionManager ----

// VersionManager tracks deployment versions per app in an append-only JSONL store.
type VersionManager struct {
	VersionsRoot string
	DeployMgr    *DeployManager
}

func newVersionManager(dm *DeployManager) *VersionManager {
	vm := &VersionManager{
		VersionsRoot: envOr("CUBE_VERSIONS_ROOT", "/var/lib/cube-container/deploy-versions"),
		DeployMgr:    dm,
	}
	os.MkdirAll(vm.VersionsRoot, 0755)
	return vm
}

// versionsPath returns the JSONL file path for the given app.
func (vm *VersionManager) versionsPath(appName string) string {
	return filepath.Join(vm.VersionsRoot, appName+".jsonl")
}

// RecordDeployment creates a new version entry (incrementing the per-app
// version number) and appends it to <VersionsRoot>/<app_name>.jsonl.
func (vm *VersionManager) RecordDeployment(appName, templateID, containerID, gitURL, branch, commitHash, startCmd string) (*VersionManifest, error) {
	if _, err := validateSafeName(appName); err != nil {
		return nil, err
	}

	versions, err := vm.ListVersions(appName)
	if err != nil {
		return nil, fmt.Errorf("read version history: %w", err)
	}
	nextVersion := 1
	if len(versions) > 0 {
		nextVersion = versions[len(versions)-1].VersionNumber + 1
	}

	manifest := &VersionManifest{
		AppName:       appName,
		VersionNumber: nextVersion,
		Timestamp:     time.Now(),
		TemplateID:    templateID,
		ContainerID:   containerID,
		GitURL:        gitURL,
		Branch:        branch,
		CommitHash:    commitHash,
		StartCmd:      startCmd,
	}

	if err := vm.appendVersion(appName, manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// appendVersion writes a single version manifest as a JSON line.
func (vm *VersionManager) appendVersion(appName string, manifest *VersionManifest) error {
	os.MkdirAll(vm.VersionsRoot, 0755)
	path := vm.versionsPath(appName)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open version history: %w", err)
	}
	defer f.Close()

	line, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("marshal version manifest: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("write version history: %w", err)
	}
	return nil
}

// ListVersions returns all recorded versions for an app, ordered by version number ascending.
func (vm *VersionManager) ListVersions(appName string) ([]*VersionManifest, error) {
	if _, err := validateSafeName(appName); err != nil {
		return nil, err
	}
	path := vm.versionsPath(appName)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []*VersionManifest{}, nil
		}
		return nil, fmt.Errorf("open version history: %w", err)
	}
	defer f.Close()

	var versions []*VersionManifest
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var v VersionManifest
		if err := json.Unmarshal(line, &v); err != nil {
			continue // skip malformed lines
		}
		versions = append(versions, &v)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read version history: %w", err)
	}
	return versions, nil
}

// RollbackDeploy rolls an app back to its previous deployment version (N-1).
// It re-deploys from the previous version's git_url + branch and records the
// new deployment as a new version entry. Returns the old and new container IDs.
func (vm *VersionManager) RollbackDeploy(appName string) (*RollbackResult, error) {
	if _, err := validateSafeName(appName); err != nil {
		return nil, err
	}

	versions, err := vm.ListVersions(appName)
	if err != nil {
		return nil, err
	}
	if len(versions) < 2 {
		return nil, fmt.Errorf("cannot rollback app '%s': only %d version(s) recorded (need at least 2)", appName, len(versions))
	}

	current := versions[len(versions)-1]
	previous := versions[len(versions)-2]

	if previous.GitURL == "" {
		return nil, fmt.Errorf("previous version %d has no git_url; cannot rollback", previous.VersionNumber)
	}

	branch := previous.Branch
	if branch == "" {
		branch = "main"
	}

	// Re-deploy from the previous version's git provenance.
	image := "python:3.12-slim"
	redeploy, err := vm.DeployMgr.DeployFromGit(
		previous.GitURL, branch, image,
		[]int{8000}, nil, previous.StartCmd, appName, 256, 1.0,
	)
	if err != nil {
		return nil, fmt.Errorf("rollback redeploy failed: %w", err)
	}

	newContainerID := extractContainerID(redeploy)

	// Record the rollback as a new version entry.
	newManifest, _ := vm.RecordDeployment(
		appName,
		previous.TemplateID,
		newContainerID,
		previous.GitURL,
		branch,
		previous.CommitHash,
		previous.StartCmd,
	)

	result := &RollbackResult{
		AppName:        appName,
		RolledBackFrom: current,
		RolledBackTo:   previous,
		OldContainerID: current.ContainerID,
		NewContainerID: newContainerID,
		Redeploy:       redeploy,
		Timestamp:      time.Now(),
	}
	if newManifest != nil {
		result.NewContainerID = newManifest.ContainerID
	}
	return result, nil
}

// extractContainerID pulls the container ID out of a DeployFromGit result map.
func extractContainerID(data interface{}) string {
	m, ok := data.(map[string]interface{})
	if !ok {
		return ""
	}
	if container, ok := m["container"].(map[string]interface{}); ok {
		for _, key := range []string{"sandboxID", "sandboxId", "id", "containerID"} {
			if v, exists := container[key]; exists {
				return fmt.Sprintf("%v", v)
			}
		}
	}
	return ""
}

// ---- Package-level wrappers (used by MCP handlers) ----
//
// These delegate to a package-level VersionManager instance initialized in
// main(), mirroring the pattern used by deploy/backupMgr/client.

var versionMgr *VersionManager

func recordDeployment(appName, templateID, containerID, gitURL, branch, commitHash, startCmd string) (*VersionManifest, error) {
	return versionMgr.RecordDeployment(appName, templateID, containerID, gitURL, branch, commitHash, startCmd)
}

func rollbackDeploy(appName string) (*RollbackResult, error) {
	return versionMgr.RollbackDeploy(appName)
}

func listVersions(appName string) ([]*VersionManifest, error) {
	return versionMgr.ListVersions(appName)
}

// ---- MCP handlers ----

func handleRollbackDeploy(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	appName := argString(args, "app_name")
	if appName == "" {
		return errResult("app_name is required"), nil
	}
	data, err := rollbackDeploy(appName)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleListVersions(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	appName := argString(args, "app_name")
	if appName == "" {
		return errResult("app_name is required"), nil
	}
	data, err := listVersions(appName)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}
