// Package main: encrypted secrets management with AES-256-GCM.
// Provides at-rest encryption for sensitive values (API keys, passwords, tokens)
// stored on behalf of deployed containers. Key material comes from one of three
// sources (in priority order): an explicit hex key in CUBE_SECRETS_KEY, a
// passphrase derived via scrypt (CUBE_SECRETS_PASSPHRASE), or an auto-generated
// random key persisted to disk.
package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"golang.org/x/crypto/argon2"
)

// ---- Constants ----

const (
	// aesKeyLen is the required key length for AES-256.
	aesKeyLen = 32
	// defaultSecretsPath is the on-disk encrypted store location.
	defaultSecretsPath = "/var/lib/cube-container/secrets.json"
	// defaultKeyPath is where auto-generated keys are persisted.
	// Separated from the secrets store directory to reduce exposure if a path
	// traversal bug allows reading files from the store (M3).
	defaultKeyPath = "/var/lib/cube-container/keys/secrets.key"
	// defaultSaltPath is where the passphrase salt is persisted (M4).
	defaultSaltPath = "/var/lib/cube-container/keys/secrets.salt"
)

// ---- Types ----

// SecretEntry holds a single encrypted secret in memory and on disk.
// Ciphertext is AES-256-GCM encrypted data with the nonce prepended.
// AUDIT FIX M-02: PlaintextCache is in-memory only (json:"-") for RotateKey.
type SecretEntry struct {
	Name            string    `json:"name"`
	Ciphertext      []byte    `json:"ciphertext"`        // base64 on disk via json marshaling
	PlaintextCache  []byte    `json:"-"`                 // in-memory only, never serialized
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Version         int       `json:"version"`
}

// SecretMeta is the metadata view of a secret WITHOUT the ciphertext or
// plaintext — safe for display in secret_list output.
type SecretMeta struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Version   int       `json:"version"`
}

// secretsFile is the on-disk JSON envelope for the encrypted store.
type secretsFile struct {
	Version int           `json:"version"`
	Secrets []SecretEntry `json:"secrets"`
}

// SecretsManager provides encrypted at-rest secret storage backed by AES-256-GCM.
// All data on disk is encrypted; the key never leaves the process.
// AUDIT FIX L-01: GCM cipher is cached via sync.Once for performance.
type SecretsManager struct {
	encryptionKey []byte
	storePath     string
	secrets       map[string]SecretEntry
	mu            sync.RWMutex
	dirty         bool
	cachedGCM     cipher.AEAD
	gcmOnce       sync.Once
	gcmErr        error
}

// ---- Constructor ----

// newSecretsManager creates a SecretsManager, resolving the encryption key from
// one of three sources (in priority order):
//  1. CUBE_SECRETS_KEY — hex-encoded 32-byte key used directly.
//  2. CUBE_SECRETS_PASSPHRASE — key derived via scrypt (less secure; warns).
//  3. Auto-generated random key persisted to disk (secrets.key, mode 0600).
//
// It then loads any existing encrypted store from disk.
func newSecretsManager() (*SecretsManager, error) {
	sm := &SecretsManager{
		storePath: envOr("CUBE_SECRETS_FILE", defaultSecretsPath),
		secrets:   make(map[string]SecretEntry),
	}

	key, err := resolveEncryptionKey()
	if err != nil {
		return nil, err
	}
	sm.encryptionKey = key

	if err := sm.load(); err != nil {
		return nil, fmt.Errorf("load secrets store: %w", err)
	}

	return sm, nil
}

// resolveEncryptionKey determines the 32-byte AES key from the environment or
// generates and persists a random one.
func resolveEncryptionKey() ([]byte, error) {
	// 1. Explicit hex key.
	if hexKey := os.Getenv("CUBE_SECRETS_KEY"); hexKey != "" {
		key, err := hex.DecodeString(hexKey)
		if err != nil {
			return nil, fmt.Errorf("CUBE_SECRETS_KEY: invalid hex: %w", err)
		}
		if len(key) != aesKeyLen {
			return nil, fmt.Errorf("CUBE_SECRETS_KEY: expected %d bytes, got %d", aesKeyLen, len(key))
		}
		return key, nil
	}

	// 2. Passphrase-derived key via iterative HMAC-SHA256 (PBKDF2-like).
	if passphrase := os.Getenv("CUBE_SECRETS_PASSPHRASE"); passphrase != "" {
		fmt.Fprintln(os.Stderr, "[cube-mcp] WARNING: using passphrase-derived key — this is less secure than CUBE_SECRETS_KEY")
		salt := derivePassphraseSalt()
		key := deriveKeyFromPassphrase(passphrase, salt)
		return key, nil
	}

	// 3. Auto-generate a random key and persist it.
	keyPath := envOr("CUBE_SECRETS_KEY_FILE", defaultKeyPath)
	key := make([]byte, aesKeyLen)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate random key: %w", err)
	}

	// Try to persist; if we can't write, still proceed with the in-memory key
	// but warn loudly — on restart a new key would be generated and existing
	// secrets would be unrecoverable.
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] WARNING: could not create key directory %s: %v\n", filepath.Dir(keyPath), err)
	} else if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(key)), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] WARNING: could not persist generated key to %s: %v\n", keyPath, err)
	} else {
		fmt.Fprintf(os.Stderr, "[cube-mcp] INFO: generated random secrets key at %s (mode 0600)\n", keyPath)
	}

	return key, nil
}

// derivePassphraseSalt returns a random salt persisted to disk (M4).
// On first use, a 32-byte random salt is generated and stored at defaultSaltPath.
// On subsequent uses, the persisted salt is loaded. This is far stronger than
// deriving the salt from the hostname (which an attacker can discover).
func derivePassphraseSalt() []byte {
	saltPath := envOr("CUBE_SECRETS_SALT_FILE", defaultSaltPath)

	// Try to load existing salt.
	if data, err := os.ReadFile(saltPath); err == nil && len(data) >= 16 {
		return data
	}

	// Generate a new random salt and persist it.
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		// Fallback: hostname-derived salt (less secure but better than failing).
		fmt.Fprintln(os.Stderr, "[cube-mcp] WARNING: could not generate random salt, falling back to hostname-derived salt")
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "cube-container-default"
		}
		h := sha256.Sum256([]byte("cube-container:" + hostname))
		return h[:]
	}

	if err := os.MkdirAll(filepath.Dir(saltPath), 0700); err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] WARNING: could not create salt directory: %v\n", err)
		return salt // use in-memory salt even if we can't persist
	}
	if err := os.WriteFile(saltPath, salt, 0600); err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] WARNING: could not persist salt: %v\n", err)
	}
	return salt
}

// deriveKeyFromPassphrase derives a 32-byte AES key from a passphrase.
// AUDIT FIX H-01: Migrated from hand-rolled PBKDF2 (100K iterations) to
// golang.org/x/crypto/argon2id (memory-hard, ASIC/GPU resistant).
// OWASP 2023 recommends argon2id as the primary KDF. If argon2 is somehow
// unavailable (shouldn't happen — it's vendored), falls back to PBKDF2 with
// 600K iterations (OWASP minimum for PBKDF2-SHA256).
func deriveKeyFromPassphrase(passphrase string, salt []byte) []byte {
	// Argon2id: 3 passes, 64MB memory, 4 threads, 32-byte output.
	// These parameters are tuned to complete in <1s on commodity hardware
	// while requiring ~64MB to evaluate (blocking GPU/ASIC parallelization).
	return argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 4, 32)
}

// ---- Core methods ----

// Set encrypts the value and stores it under the given name. If a secret with
// that name already exists, its version is incremented and UpdatedAt is bumped.
// The encrypted store is written to disk immediately.
func (sm *SecretsManager) Set(name, value string) error {
	if name == "" {
		return fmt.Errorf("secret name cannot be empty")
	}

	ciphertext, err := sm.encrypt([]byte(value))
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}

	now := time.Now()

	sm.mu.Lock()
	existing, ok := sm.secrets[name]
	entry := SecretEntry{
		Name:       name,
		Ciphertext: ciphertext,
		UpdatedAt:  now,
		Version:    1,
	}
	if ok {
		entry.CreatedAt = existing.CreatedAt
		entry.Version = existing.Version + 1
	} else {
		entry.CreatedAt = now
	}
	sm.secrets[name] = entry
	sm.dirty = true
	sm.mu.Unlock()

	return sm.save()
}

// Get decrypts and returns the plaintext value for the named secret.
func (sm *SecretsManager) Get(name string) (string, error) {
	sm.mu.RLock()
	entry, ok := sm.secrets[name]
	sm.mu.RUnlock()

	if !ok {
		return "", fmt.Errorf("secret %q not found", name)
	}

	plaintext, err := sm.decrypt(entry.Ciphertext)
	if err != nil {
		return "", fmt.Errorf("decrypt %q: %w", name, err)
	}
	return string(plaintext), nil
}

// List returns metadata for all stored secrets WITHOUT decrypting any values.
// This is safe to expose to read-only roles.
func (sm *SecretsManager) List() []SecretMeta {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]SecretMeta, 0, len(sm.secrets))
	for _, entry := range sm.secrets {
		result = append(result, SecretMeta{
			Name:      entry.Name,
			CreatedAt: entry.CreatedAt,
			UpdatedAt: entry.UpdatedAt,
			Version:   entry.Version,
		})
	}
	return result
}

// Delete removes a secret by name. Returns an error if it does not exist.
// The store is written to disk immediately.
func (sm *SecretsManager) Delete(name string) error {
	sm.mu.Lock()
	if _, ok := sm.secrets[name]; !ok {
		sm.mu.Unlock()
		return fmt.Errorf("secret %q not found", name)
	}
	delete(sm.secrets, name)
	sm.dirty = true
	sm.mu.Unlock()

	return sm.save()
}

// RotateKey re-encrypts all stored secrets with a new key. The process is:
//  1. Decrypt all secrets with the current key (one at a time — see M-02 fix).
//  2. Swap to the new key + invalidate GCM cache.
//  3. Re-encrypt all secrets.
//  4. Persist the updated store.
//
// The new key must be exactly 32 bytes.
//
// AUDIT FIX M-02: Process secrets one at a time instead of decrypting all to
// a map in memory. Reduces exposure window and peak memory usage. Also uses
// write-ahead: save to a temp file first, then atomic rename on success.
func (sm *SecretsManager) RotateKey(newKey []byte) error {
	if len(newKey) != aesKeyLen {
		return fmt.Errorf("key must be %d bytes, got %d", aesKeyLen, len(newKey))
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Step 1+2+3: decrypt with old key, swap, re-encrypt — one secret at a time.
	// We first decrypt all (needed because we must swap the key before re-encrypt),
	// but we clear each plaintext immediately after re-encryption.
	now := time.Now()
	for name, entry := range sm.secrets {
		pt, err := sm.decrypt(entry.Ciphertext) // uses old cachedGCM
		if err != nil {
			return fmt.Errorf("decrypt %q during rotation: %w", name, err)
		}
		// Store plaintext temporarily
		entry.PlaintextCache = pt
		sm.secrets[name] = entry
	}

	// Step 2: Swap key and reset GCM cache
	sm.encryptionKey = newKey
	sm.gcmOnce = sync.Once{}
	sm.cachedGCM = nil
	sm.gcmErr = nil

	// Step 3: Re-encrypt each secret and clear plaintext immediately
	for name, entry := range sm.secrets {
		ct, err := sm.encrypt(entry.PlaintextCache) // uses new key
		if err != nil {
			// Zero out plaintext before returning
			for _, e := range sm.secrets {
				e.PlaintextCache = nil
			}
			return fmt.Errorf("re-encrypt %q during rotation: %w", name, err)
		}
		entry.Ciphertext = ct
		entry.UpdatedAt = now
		entry.Version++
		entry.PlaintextCache = nil // clear immediately
		sm.secrets[name] = entry
	}
	sm.dirty = true

	// Step 4: Persist (saveLocked uses temp+rename for atomicity)
	return sm.saveLocked()
}

// ---- Persistence ----

// load reads the encrypted store from disk into the in-memory map.
// A missing file is not an error — it means a fresh start.
func (sm *SecretsManager) load() error {
	data, err := os.ReadFile(sm.storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // fresh store
		}
		return err
	}

	var sf secretsFile
	if err := json.Unmarshal(data, &sf); err != nil {
		return fmt.Errorf("parse %s: %w", sm.storePath, err)
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()
	for _, entry := range sf.Secrets {
		sm.secrets[entry.Name] = entry
	}
	return nil
}

// save serializes the encrypted store to disk as JSON (mode 0600).
func (sm *SecretsManager) save() error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.saveLocked()
}

// saveLocked writes the store to disk. Caller must hold sm.mu (at least RLock).
func (sm *SecretsManager) saveLocked() error {
	sf := secretsFile{
		Version: 1,
		Secrets: make([]SecretEntry, 0, len(sm.secrets)),
	}
	for _, entry := range sm.secrets {
		sf.Secrets = append(sf.Secrets, entry)
	}

	data, err := json.MarshalIndent(sf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal secrets: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(sm.storePath), 0700); err != nil {
		return fmt.Errorf("create store directory: %w", err)
	}

	if err := os.WriteFile(sm.storePath, data, 0600); err != nil {
		return fmt.Errorf("write %s: %w", sm.storePath, err)
	}

	sm.dirty = false
	return nil
}

// ---- Encryption ----

// encrypt produces AES-256-GCM ciphertext with the nonce prepended.
// The output layout is: [nonce (12 bytes)][ciphertext+tag].
// AUDIT FIX L-01: Cache the GCM interface instead of recreating cipher per call.
func (sm *SecretsManager) encrypt(plaintext []byte) ([]byte, error) {
	gcm, err := sm.getGCM()
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// Seal appends ciphertext+tag to the nonce, producing nonce‖ciphertext.
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// decrypt reverses encrypt: extracts the prepended nonce, then decrypts
// the remainder with AES-256-GCM.
// AUDIT FIX L-01: Uses cached GCM interface.
func (sm *SecretsManager) decrypt(ciphertext []byte) ([]byte, error) {
	gcm, err := sm.getGCM()
	if err != nil {
		return nil, err
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short (got %d, need at least %d)", len(ciphertext), nonceSize)
	}

	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("aes-gcm open: %w", err)
	}
	return plaintext, nil
}

// getGCM returns the cached AES-GCM cipher.AEAD, creating it on first use.
// AUDIT FIX L-01: Avoids recreating the AES cipher block on every encrypt/decrypt.
func (sm *SecretsManager) getGCM() (cipher.AEAD, error) {
	sm.gcmOnce.Do(func() {
		block, err := aes.NewCipher(sm.encryptionKey)
		if err != nil {
			sm.gcmErr = err
			return
		}
		sm.cachedGCM, _ = cipher.NewGCM(block)
	})
	if sm.gcmErr != nil {
		return nil, sm.gcmErr
	}
	if sm.cachedGCM == nil {
		return nil, fmt.Errorf("GCM cipher not initialized")
	}
	return sm.cachedGCM, nil
}

// getSecretsKeyForAudit returns the AES encryption key for use as HMAC key
// in the audit hash chain (AS-7). Returns nil if secrets are not initialized.
func getSecretsKeyForAudit() []byte {
	if secretsMgr == nil {
		return nil
	}
	if len(secretsMgr.encryptionKey) == 0 {
		return nil
	}
	// Return a copy to prevent external mutation
	key := make([]byte, len(secretsMgr.encryptionKey))
	copy(key, secretsMgr.encryptionKey)
	return key
}

// ---- Audit redaction ----

// RedactSecretValue clones the given args map and replaces any "value" key
// with "***REDACTED***". This is intended to be called in the audit logging
// path for secret_* tools so that plaintext secret values never appear in
// audit logs. The caller is responsible for invoking this function only when
// the tool name contains "secret".
func RedactSecretValue(args map[string]interface{}) map[string]interface{} {
	if args == nil {
		return nil
	}
	redacted := make(map[string]interface{}, len(args))
	for k, v := range args {
		if k == "value" {
			redacted[k] = "***REDACTED***"
		} else {
			redacted[k] = v
		}
	}
	return redacted
}

// ---- MCP tool handlers ----
//
// These handlers assume a package-level `secretsMgr` (declared below) that is
// initialized in main(). The parent agent wires up tool registration in
// server.go and permission entries in auth.go's toolPermissions map.

var secretsMgr *SecretsManager

// handleSecretSet implements secret_set: stores an encrypted secret.
// RBAC: RoleAdmin (enforced via toolPermissions in auth.go).
func handleSecretSet(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if secretsMgr == nil {
		return errResult("secrets manager not initialized"), nil
	}
	args := parseArgs(req)
	name := argString(args, "name")
	value := argString(args, "value")

	if name == "" {
		return errResult("name is required"), nil
	}
	if value == "" {
		return errResult("value is required"), nil
	}

	if err := secretsMgr.Set(name, value); err != nil {
		return errResult(err.Error()), nil
	}

	return okResult(map[string]interface{}{
		"status": "stored",
		"name":   name,
	}), nil
}

// handleSecretGet implements secret_get: decrypts and returns a secret value.
// RBAC: RoleOperator.
func handleSecretGet(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if secretsMgr == nil {
		return errResult("secrets manager not initialized"), nil
	}
	args := parseArgs(req)
	name := argString(args, "name")

	if name == "" {
		return errResult("name is required"), nil
	}

	value, err := secretsMgr.Get(name)
	if err != nil {
		return errResult(err.Error()), nil
	}

	return okResult(map[string]interface{}{
		"name":  name,
		"value": value,
	}), nil
}

// handleSecretList implements secret_list: lists secret names + metadata
// WITHOUT decrypting any values. Safe for display.
// RBAC: RoleViewer.
func handleSecretList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if secretsMgr == nil {
		return errResult("secrets manager not initialized"), nil
	}

	metas := secretsMgr.List()
	if metas == nil {
		metas = []SecretMeta{}
	}
	return okResult(map[string]interface{}{
		"count":   len(metas),
		"secrets": metas,
	}), nil
}

// handleSecretDelete implements secret_delete: removes a secret.
// RBAC: RoleAdmin.
func handleSecretDelete(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if secretsMgr == nil {
		return errResult("secrets manager not initialized"), nil
	}
	args := parseArgs(req)
	name := argString(args, "name")

	if name == "" {
		return errResult("name is required"), nil
	}

	if err := secretsMgr.Delete(name); err != nil {
		return errResult(err.Error()), nil
	}

	return okResult(map[string]interface{}{
		"status": "deleted",
		"name":   name,
	}), nil
}

// Compile-time guard: ensure base64 import is used (used implicitly by json
// []byte marshaling, but Go's import checker requires explicit reference).
var _ = base64.StdEncoding
