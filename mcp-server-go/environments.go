// Package main: environment (namespace) management and promotion.
//
// Provides isolated environments (dev, staging, prod) within the same cluster.
// Each environment has its own containers, volumes, and config. The promote
// tool moves a deployment from one environment to the next in the pipeline.
//
// This is the missing piece for multi-env CI/CD: without environments, every
// deploy goes to the same flat namespace, making dev/prod separation impossible.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- Types ----

type Environment struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	CreatedAt   time.Time         `json:"created_at"`
	Containers  []string          `json:"containers"` // container IDs in this env
	ConfigMaps  map[string]string `json:"config_maps,omitempty"`
	Protected   bool              `json:"protected"` // protected envs can't be deleted
}

type EnvironmentListResult struct {
	Environments []EnvironmentSummary `json:"environments"`
	Total        int                  `json:"total"`
}

type EnvironmentSummary struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Containers  int       `json:"container_count"`
	CreatedAt   time.Time `json:"created_at"`
	Protected   bool      `json:"protected"`
}

type PromoteResult struct {
	Source      string    `json:"source_env"`
	Target      string    `json:"target_env"`
	ContainerID string    `json:"container_id"`
	NewContainerID string  `json:"new_container_id"`
	Image       string    `json:"image"`
	PromotedAt  time.Time `json:"promoted_at"`
	Status      string    `json:"status"`
}

// ---- Manager ----

var envMgr *EnvironmentManager

type EnvironmentManager struct {
	mu      sync.Mutex
	rootDir string
	envs    map[string]*Environment
}

func newEnvironmentManager() *EnvironmentManager {
	em := &EnvironmentManager{
		rootDir: envOr("CUBE_ENV_ROOT", "/var/lib/cube-container/environments"),
		envs:    make(map[string]*Environment),
	}
	em.loadFromDisk()

	// Ensure default environments exist
	em.ensureDefaults()
	return em
}

func (em *EnvironmentManager) ensureDefaults() {
	defaults := []struct {
		name string
		desc string
	}{
		{"dev", "Development environment — fast iteration, no SLA"},
		{"staging", "Staging — pre-production mirror for integration testing"},
		{"prod", "Production — protected, changes require explicit promotion"},
	}

	for _, d := range defaults {
		if _, exists := em.envs[d.name]; !exists {
			em.envs[d.name] = &Environment{
				Name:        d.name,
				Description: d.desc,
				CreatedAt:   time.Now().UTC(),
				Containers:  []string{},
				Protected:   d.name == "prod",
			}
		}
	}
	em.saveAll()
}

// ---- Disk persistence ----

func (em *EnvironmentManager) envFilePath(name string) string {
	return filepath.Join(em.rootDir, name+".json")
}

func (em *EnvironmentManager) loadFromDisk() {
	entries, err := os.ReadDir(em.rootDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(em.rootDir, entry.Name()))
		if err != nil {
			continue
		}
		var env Environment
		if err := json.Unmarshal(data, &env); err != nil {
			continue
		}
		em.envs[env.Name] = &env
	}
}

func (em *EnvironmentManager) saveEnv(env *Environment) error {
	if err := os.MkdirAll(em.rootDir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(em.envFilePath(env.Name), data, 0600)
}

func (em *EnvironmentManager) saveAll() {
	for _, env := range em.envs {
		_ = em.saveEnv(env)
	}
}

// ---- Operations ----

func (em *EnvironmentManager) Create(name, description string, protected bool) (*Environment, error) {
	em.mu.Lock()
	defer em.mu.Unlock()

	if name == "" {
		return nil, fmt.Errorf("environment name is required")
	}
	if err := validateName(name); err != nil {
		return nil, fmt.Errorf("invalid environment name: %w", err)
	}
	if _, exists := em.envs[name]; exists {
		return nil, fmt.Errorf("environment '%s' already exists", name)
	}

	env := &Environment{
		Name:        name,
		Description: description,
		CreatedAt:   time.Now().UTC(),
		Containers:  []string{},
		Protected:   protected,
	}
	em.envs[name] = env
	if err := em.saveEnv(env); err != nil {
		delete(em.envs, name)
		return nil, fmt.Errorf("failed to persist environment: %w", err)
	}
	return env, nil
}

func (em *EnvironmentManager) List() (*EnvironmentListResult, error) {
	em.mu.Lock()
	defer em.mu.Unlock()

	result := &EnvironmentListResult{
		Environments: []EnvironmentSummary{},
	}
	for _, env := range em.envs {
		// Count live containers (filter out dead ones)
		live := em.liveContainers(env)
		result.Environments = append(result.Environments, EnvironmentSummary{
			Name:        env.Name,
			Description: env.Description,
			Containers:  len(live),
			CreatedAt:   env.CreatedAt,
			Protected:   env.Protected,
		})
	}
	sort.Slice(result.Environments, func(i, j int) bool {
		return result.Environments[i].Name < result.Environments[j].Name
	})
	result.Total = len(result.Environments)
	return result, nil
}

func (em *EnvironmentManager) Get(name string) (*Environment, error) {
	em.mu.Lock()
	defer em.mu.Unlock()

	env, ok := em.envs[name]
	if !ok {
		return nil, fmt.Errorf("environment '%s' not found", name)
	}
	// Update container list to live only
	live := em.liveContainers(env)
	env.Containers = live
	return env, nil
}

func (em *EnvironmentManager) Remove(name string) error {
	em.mu.Lock()
	defer em.mu.Unlock()

	env, ok := em.envs[name]
	if !ok {
		return fmt.Errorf("environment '%s' not found", name)
	}
	if env.Protected {
		return fmt.Errorf("environment '%s' is protected and cannot be removed", name)
	}
	delete(em.envs, name)
	_ = os.Remove(em.envFilePath(name))
	return nil
}

// Promote moves a container from one environment to the next.
// It creates a new container in the target environment with the same image
// and config, then removes the source container.
func (em *EnvironmentManager) Promote(sourceEnv, targetEnv, containerID string) (*PromoteResult, error) {
	em.mu.Lock()
	defer em.mu.Unlock()

	source, ok := em.envs[sourceEnv]
	if !ok {
		return nil, fmt.Errorf("source environment '%s' not found", sourceEnv)
	}
	target, ok := em.envs[targetEnv]
	if !ok {
		return nil, fmt.Errorf("target environment '%s' not found", targetEnv)
	}

	// Verify container is in source env
	found := false
	for _, cid := range source.Containers {
		if cid == containerID {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("container %s is not in environment '%s'", containerID, sourceEnv)
	}

	// Get the source container details
	containerRaw, err := client.GetSandbox(containerID)
	if err != nil {
		return nil, fmt.Errorf("failed to get source container: %w", err)
	}
	container, ok := containerRaw.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected container response format")
	}

	// Extract image
	image, _ := container["image"].(string)
	if image == "" {
		// Docker format
		if img, ok := container["Config"].(map[string]interface{}); ok {
			image, _ = img["Image"].(string)
		}
	}
	if image == "" {
		return nil, fmt.Errorf("could not determine image from source container")
	}

	// Create a new container in the target env with the same image
	templateResp, err := client.CreateTemplateFromImage(image, nil, 0, nil, nil, "")
	if err != nil {
		return nil, fmt.Errorf("failed to create template for promotion: %w", err)
	}
	templateMap, _ := templateResp.(map[string]interface{})
	templateID, _ := templateMap["id"].(string)

	// Create container with environment label
	newResp, err := client.CreateSandbox(templateID, 512, 1.0, nil, map[string]interface{}{
		"environment": targetEnv,
		"promoted_from": sourceEnv,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create container in target env: %w", err)
	}
	newContainer, _ := newResp.(map[string]interface{})
	newContainerID, _ := newContainer["id"].(string)

	// Add to target, remove from source
	target.Containers = append(target.Containers, newContainerID)
	source.Containers = em.removeFromStringSlice(source.Containers, containerID)

	em.saveEnv(source)
	em.saveEnv(target)

	// Kill the old container in the source env
	_, _ = client.KillSandbox(containerID)

	return &PromoteResult{
		Source:         sourceEnv,
		Target:         targetEnv,
		ContainerID:    containerID,
		NewContainerID: newContainerID,
		Image:          image,
		PromotedAt:     time.Now().UTC(),
		Status:         "promoted",
	}, nil
}

// ---- Helpers ----

func (em *EnvironmentManager) liveContainers(env *Environment) []string {
	live := []string{}
	for _, cid := range env.Containers {
		_, err := client.GetSandbox(cid)
		if err == nil {
			live = append(live, cid)
		}
	}
	return live
}

func (em *EnvironmentManager) removeFromStringSlice(s []string, val string) []string {
	result := []string{}
	for _, v := range s {
		if v != val {
			result = append(result, v)
		}
	}
	return result
}

// validateName ensures environment names are safe.
func validateName(name string) error {
	if len(name) > 32 {
		return fmt.Errorf("name too long (max 32 chars)")
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return fmt.Errorf("name contains invalid characters (only lowercase alphanumeric, '-', '_' allowed)")
		}
	}
	return nil
}
