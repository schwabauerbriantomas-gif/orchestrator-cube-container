// Package main: configuration maps — non-sensitive config management.
//
// Separates configuration from secrets. ConfigMaps store env vars, feature
// flags, and arbitrary config data that is NOT sensitive (use secrets.go for
// passwords, API keys, tokens). ConfigMaps can be applied to containers at
// deploy time as environment variables or mounted as files.
//
// Stored as JSON on disk in /var/lib/cube-container/configmaps/.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Types ----

// ConfigMap is a named set of key-value configuration entries.
type ConfigMap struct {
	Name      string            `json:"name"`
	Data      map[string]string `json:"data"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// ConfigMapSummary is the list view.
type ConfigMapSummary struct {
	Name      string `json:"name"`
	Keys      int    `json:"keys"`
	UpdatedAt string `json:"updated_at"`
}

// ---- Manager ----

var configMgr *ConfigMapManager

type ConfigMapManager struct {
	mu      sync.Mutex
	maps    map[string]*ConfigMap
	rootDir string
}

func newConfigMapManager() *ConfigMapManager {
	cm := &ConfigMapManager{
		maps:    make(map[string]*ConfigMap),
		rootDir: envOr("CUBE_CONFIGMAP_ROOT", "/var/lib/cube-container/configmaps"),
	}
	cm.loadFromDisk()
	return cm
}

// ---- Disk persistence ----

func (cm *ConfigMapManager) filePath(name string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_").Replace(name)
	return filepath.Join(cm.rootDir, safe+".json")
}

func (cm *ConfigMapManager) loadFromDisk() {
	entries, err := os.ReadDir(cm.rootDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(cm.rootDir, entry.Name()))
		if err != nil {
			continue
		}
		var cm2 ConfigMap
		if err := jsonDecode(data, &cm2); err != nil {
			continue
		}
		cm.maps[cm2.Name] = &cm2
	}
}

func (cm *ConfigMapManager) save(c *ConfigMap) error {
	if err := os.MkdirAll(cm.rootDir, 0700); err != nil {
		return fmt.Errorf("create configmap root: %w", err)
	}
	data, err := jsonEncode(c)
	if err != nil {
		return fmt.Errorf("marshal configmap: %w", err)
	}
	return os.WriteFile(cm.filePath(c.Name), data, 0600)
}

func (cm *ConfigMapManager) deleteFile(name string) {
	os.Remove(cm.filePath(name))
}

// ---- CRUD ----

func (cm *ConfigMapManager) create(name string, data map[string]string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}

	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, exists := cm.maps[name]; exists {
		return fmt.Errorf("configmap %s already exists (use configmap_update)", name)
	}

	c := &ConfigMap{
		Name:      name,
		Data:      data,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	cm.maps[name] = c
	return cm.save(c)
}

func (cm *ConfigMapManager) update(name string, data map[string]string, merge bool) (*ConfigMap, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	c, ok := cm.maps[name]
	if !ok {
		// Create if not exists
		c = &ConfigMap{
			Name:      name,
			Data:      make(map[string]string),
			CreatedAt: time.Now().UTC(),
		}
		cm.maps[name] = c
	}

	if merge {
		for k, v := range data {
			c.Data[k] = v
		}
	} else {
		c.Data = data
	}
	c.UpdatedAt = time.Now().UTC()

	if err := cm.save(c); err != nil {
		return nil, err
	}
	return c, nil
}

func (cm *ConfigMapManager) remove(name string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if _, ok := cm.maps[name]; !ok {
		return fmt.Errorf("configmap %s not found", name)
	}
	delete(cm.maps, name)
	cm.deleteFile(name)
	return nil
}

func (cm *ConfigMapManager) get(name string) (*ConfigMap, error) {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	c, ok := cm.maps[name]
	if !ok {
		return nil, fmt.Errorf("configmap %s not found", name)
	}
	return c, nil
}

func (cm *ConfigMapManager) list() []ConfigMapSummary {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	out := make([]ConfigMapSummary, 0, len(cm.maps))
	for _, c := range cm.maps {
		out = append(out, ConfigMapSummary{
			Name:      c.Name,
			Keys:      len(c.Data),
			UpdatedAt: c.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

// asEnvVars returns the configmap data as a map suitable for container env vars.
func (cm *ConfigMapManager) asEnvVars(name string) (map[string]interface{}, error) {
	c, err := cm.get(name)
	if err != nil {
		return nil, err
	}
	env := make(map[string]interface{}, len(c.Data))
	for k, v := range c.Data {
		env[k] = v
	}
	return env, nil
}

// ---- Tool handlers: ConfigMaps ----

func handleConfigMapCreate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	name := argString(args, "name")
	if name == "" {
		return errResult("name is required"), nil
	}

	data := make(map[string]string)
	if d := argMap(args, "data"); d != nil {
		for k, v := range d {
			data[k] = fmt.Sprintf("%v", v)
		}
	}

	if err := configMgr.create(name, data); err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(map[string]interface{}{
		"name":   name,
		"keys":   len(data),
		"status": "created",
	}), nil
}

func handleConfigMapUpdate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	name := argString(args, "name")
	if name == "" {
		return errResult("name is required"), nil
	}

	data := make(map[string]string)
	if d := argMap(args, "data"); d != nil {
		for k, v := range d {
			data[k] = fmt.Sprintf("%v", v)
		}
	}
	merge := argString(args, "mode") != "replace"

	c, err := configMgr.update(name, data, merge)
	if err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(c), nil
}

func handleConfigMapRemove(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	name := argString(args, "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	if err := configMgr.remove(name); err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(map[string]interface{}{"name": name, "status": "removed"}), nil
}

func handleConfigMapGet(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	name := argString(args, "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	c, err := configMgr.get(name)
	if err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(c), nil
}

func handleConfigMapList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return okResult(configMgr.list()), nil
}
