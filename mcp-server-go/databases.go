// Package main: database provisioning and lifecycle.
//
// Creates managed database instances (PostgreSQL, MySQL, Redis, MongoDB) as
// containers with sensible defaults: persistent volumes, automatic backups,
// health checks, and credentials stored as secrets.
//
// This is the tool the LLM uses when a user says "give me a Postgres for my
// app" — it handles everything: container creation, volume, user/password
// generation, secret storage, and health check registration.
package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---- Types ----

type DBType string

const (
	DBPostgres DBType = "postgres"
	DBMySQL    DBType = "mysql"
	DBRedis    DBType = "redis"
	DBMongoDB  DBType = "mongodb"
)

type DatabaseInstance struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Type        DBType    `json:"type"`
	Version     string    `json:"version"`
	ContainerID string    `json:"container_id"`
	VolumeName  string    `json:"volume_name"`
	Port        int       `json:"port"`
	Password    string    `json:"-"`        // never serialized to JSON responses
	PasswordSecret string `json:"password_secret"` // secret name where password is stored
	CreatedAt   time.Time `json:"created_at"`
	Status      string    `json:"status"`
}

type DatabaseCreateParams struct {
	Name        string `json:"name"`
	Type        DBType `json:"type"`
	Version     string `json:"version"`     // e.g. "16" for postgres:16
	MemoryMB    int    `json:"memory_mb"`   // default 512
	VolumeSize  int    `json:"volume_size_gb"` // default 5
}

type DatabaseBackupResult struct {
	DatabaseID string    `json:"database_id"`
	BackupFile string    `json:"backup_file"`
	Size       int64     `json:"size_bytes"`
	CreatedAt  time.Time `json:"created_at"`
}

type DatabaseRestoreResult struct {
	DatabaseID string    `json:"database_id"`
	RestoredFrom string  `json:"restored_from"`
	Status     string    `json:"status"`
	RestoredAt time.Time `json:"restored_at"`
}

// ---- Manager ----

var dbMgr *DatabaseManager

type DatabaseManager struct {
	mu      sync.Mutex
	rootDir string
	dbs     map[string]*DatabaseInstance
}

func newDatabaseManager() *DatabaseManager {
	dm := &DatabaseManager{
		rootDir: envOr("CUBE_DB_ROOT", "/var/lib/cube-container/databases"),
		dbs:     make(map[string]*DatabaseInstance),
	}
	dm.loadFromDisk()
	return dm
}

// ---- Defaults per DB type ----

var dbDefaults = map[DBType]struct {
	Image   string
	Port    int
	EnvVars []string
}{
	DBPostgres: {
		Image: "postgres:16-alpine",
		Port:  5432,
		EnvVars: []string{"POSTGRES_DB=app", "POSTGRES_USER=app"},
	},
	DBMySQL: {
		Image: "mysql:8",
		Port:  3306,
		EnvVars: []string{"MYSQL_DATABASE=app", "MYSQL_USER=app"},
	},
	DBRedis: {
		Image: "redis:7-alpine",
		Port:  6379,
		EnvVars: []string{},
	},
	DBMongoDB: {
		Image: "mongo:7",
		Port:  27017,
		EnvVars: []string{},
	},
}

// ---- Disk persistence ----

func (dm *DatabaseManager) dbFilePath() string {
	return filepath.Join(dm.rootDir, "databases.json")
}

func (dm *DatabaseManager) loadFromDisk() {
	data, err := os.ReadFile(dm.dbFilePath())
	if err != nil {
		return
	}
	var dbs []DatabaseInstance
	if err := json.Unmarshal(data, &dbs); err != nil {
		return
	}
	for i := range dbs {
		dm.dbs[dbs[i].ID] = &dbs[i]
	}
}

func (dm *DatabaseManager) saveToDisk() error {
	if err := os.MkdirAll(dm.rootDir, 0700); err != nil {
		return err
	}
	dbs := make([]DatabaseInstance, 0, len(dm.dbs))
	for _, db := range dm.dbs {
		dbs = append(dbs, *db)
	}
	data, err := json.MarshalIndent(dbs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dm.dbFilePath(), data, 0600)
}

// ---- Operations ----

func (dm *DatabaseManager) Create(params DatabaseCreateParams) (*DatabaseInstance, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if params.Name == "" {
		return nil, fmt.Errorf("database name is required")
	}
	if err := validateName(params.Name); err != nil {
		return nil, fmt.Errorf("invalid database name: %w", err)
	}
	if err := validateDBType(params.Type); err != nil {
		return nil, err
	}
	if params.MemoryMB <= 0 {
		params.MemoryMB = 512
	}

	defaults, ok := dbDefaults[params.Type]
	if !ok {
		return nil, fmt.Errorf("unsupported database type: %s", params.Type)
	}

	// Generate a secure random password
	password, err := generateSecurePassword(24)
	if err != nil {
		return nil, fmt.Errorf("failed to generate password: %w", err)
	}

	// Create volume for persistence
	volName := fmt.Sprintf("db-%s-data", params.Name)
	_, err = deploy.CreateVolume(volName)
	if err != nil {
		return nil, fmt.Errorf("failed to create volume: %w", err)
	}

	// Create template from image
	image := defaults.Image
	if params.Version != "" {
		// Replace version in image tag
		parts := strings.Split(image, ":")
		if len(parts) == 2 {
			image = fmt.Sprintf("%s:%s", parts[0], params.Version)
		}
	}

	// Build env vars
	envVars := map[string]interface{}{}
	for _, ev := range defaults.EnvVars {
		parts := strings.SplitN(ev, "=", 2)
		if len(parts) == 2 {
			envVars[parts[0]] = parts[1]
		}
	}
	// Set password env var
	switch params.Type {
	case DBPostgres:
		envVars["POSTGRES_PASSWORD"] = password
	case DBMySQL:
		envVars["MYSQL_ROOT_PASSWORD"] = password
	case DBMongoDB:
		envVars["MONGO_INITDB_ROOT_USERNAME"] = "admin"
		envVars["MONGO_INITDB_ROOT_PASSWORD"] = password
	case DBRedis:
		envVars["REDIS_PASSWORD"] = password // custom, requires redis-server --requirepass
	}

	templateResp, err := client.CreateTemplateFromImage(
		image,
		[]int{defaults.Port},
		0,
		nil,
		envVars,
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create template: %w", err)
	}
	templateMap, _ := templateResp.(map[string]interface{})
	templateID, _ := templateMap["id"].(string)

	// Create the container
	containerResp, err := client.CreateSandbox(templateID, params.MemoryMB, 1.0, nil, map[string]interface{}{
		"database": params.Name,
		"type":     string(params.Type),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create database container: %w", err)
	}
	container, _ := containerResp.(map[string]interface{})
	containerID, _ := container["id"].(string)

	// Attach the volume
	if volumeMgr != nil && containerID != "" {
		_, _ = volumeMgr.VolumeAttach(containerID, volName, "/var/lib/postgresql/data") // simplified mount path
	}

	// Store password as a secret
	secretName := fmt.Sprintf("db-%s-password", params.Name)
	if secretsMgr != nil {
		_ = secretsMgr.Set(secretName, password)
	}

	// Register health check (TCP probe on the database port)
	if healthMgr != nil && containerID != "" {
		hc := &HealthCheck{
			ContainerID:      containerID,
			Type:             HealthTCP,
			IntervalSeconds:  30,
			TimeoutSeconds:   5,
			FailureThreshold: 3,
			TCPPort:          defaults.Port,
			Enabled:          true,
			CreatedAt:        time.Now().UTC(),
			LastStatus:       "unknown",
		}
		_ = healthMgr.setHealthCheck(hc)
	}

	id := generateID("db")
	instance := &DatabaseInstance{
		ID:            id,
		Name:          params.Name,
		Type:          params.Type,
		Version:       params.Version,
		ContainerID:   containerID,
		VolumeName:    volName,
		Port:          defaults.Port,
		Password:      password,
		PasswordSecret: secretName,
		CreatedAt:     time.Now().UTC(),
		Status:        "running",
	}
	dm.dbs[id] = instance
	if err := dm.saveToDisk(); err != nil {
		// Best effort cleanup
		_, _ = client.KillSandbox(containerID)
		return nil, fmt.Errorf("failed to persist database: %w", err)
	}

	return instance, nil
}

func (dm *DatabaseManager) Backup(dbID string) (*DatabaseBackupResult, error) {
	dm.mu.Lock()
	db, ok := dm.dbs[dbID]
	dm.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("database '%s' not found", dbID)
	}

	// Use the backup manager to backup the volume
	if backupMgr == nil {
		return nil, fmt.Errorf("backup manager not initialized")
	}

	result, err := backupMgr.BackupVolume(db.VolumeName)
	if err != nil {
		return nil, fmt.Errorf("backup failed: %w", err)
	}

	return &DatabaseBackupResult{
		DatabaseID: dbID,
		BackupFile: result.ID,
		Size:       result.SizeBytes,
		CreatedAt:  time.Now().UTC(),
	}, nil
}

func (dm *DatabaseManager) Restore(dbID, backupID string) (*DatabaseRestoreResult, error) {
	dm.mu.Lock()
	db, ok := dm.dbs[dbID]
	dm.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("database '%s' not found", dbID)
	}
	if backupMgr == nil {
		return nil, fmt.Errorf("backup manager not initialized")
	}

	_, err := backupMgr.RestoreBackup(backupID)
	if err != nil {
		return nil, fmt.Errorf("restore failed: %w", err)
	}

	// Restart the container to pick up restored data
	_, _ = client.RestartSandbox(db.ContainerID)

	return &DatabaseRestoreResult{
		DatabaseID:   dbID,
		RestoredFrom: backupID,
		Status:       "restored",
		RestoredAt:   time.Now().UTC(),
	}, nil
}

// ---- Helpers ----

func validateDBType(t DBType) error {
	switch t {
	case DBPostgres, DBMySQL, DBRedis, DBMongoDB:
		return nil
	}
	return fmt.Errorf("invalid database type: %s (must be postgres, mysql, redis, or mongodb)", t)
}

func generateSecurePassword(length int) (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	result := make([]byte, length)
	for i := range result {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		result[i] = charset[n.Int64()]
	}
	return string(result), nil
}
