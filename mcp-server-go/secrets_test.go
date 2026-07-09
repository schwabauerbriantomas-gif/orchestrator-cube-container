package main

import (
	"encoding/hex"
	"os"
	"testing"
)

// AUDIT FIX L-06: Tests for the crypto layer (secrets.go).
// Covers encrypt/decrypt round-trip, key derivation, rotation, and nonce uniqueness.

func TestSecretsEncryptDecryptRoundTrip(t *testing.T) {
	// Use an explicit hex key so both the key and store are fully controlled.
	dir := t.TempDir()
	storeFile := dir + "/store.json"
	// 32-byte key hex-encoded: 00 01 02 ... 1f
	keyBytes := make([]byte, 32)
	for i := range keyBytes {
		keyBytes[i] = byte(i)
	}
	hexKey := hex.EncodeToString(keyBytes)
	os.Setenv("CUBE_SECRETS_KEY", hexKey)
	os.Setenv("CUBE_SECRETS_FILE", storeFile)

	sm, err := newSecretsManager()
	if err != nil {
		t.Fatalf("newSecretsManager: %v", err)
	}

	plaintexts := [][]byte{
		[]byte("hello world"),
		[]byte(""),
		[]byte("API_KEY=sk-1234567890abcdef"),
		make([]byte, 4096), // large payload
	}

	for i, pt := range plaintexts {
		ct, err := sm.encrypt(pt)
		if err != nil {
			t.Fatalf("encrypt[%d]: %v", i, err)
		}

		// Verify ciphertext is different from plaintext
		if len(pt) > 0 && string(ct) == string(pt) {
			t.Errorf("ciphertext [%d] equals plaintext — not encrypted!", i)
		}

		// Decrypt and verify round-trip
		decrypted, err := sm.decrypt(ct)
		if err != nil {
			t.Fatalf("decrypt[%d]: %v", i, err)
		}
		if string(decrypted) != string(pt) {
			t.Errorf("round-trip [%d] mismatch: got %q, want %q", i, string(decrypted), string(pt))
		}
	}
}

func TestSecretsNonceUniqueness(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("CUBE_SECRETS_KEY", "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	os.Setenv("CUBE_SECRETS_FILE", dir+"/store.json")

	sm, err := newSecretsManager()
	if err != nil {
		t.Fatalf("newSecretsManager: %v", err)
	}

	// Encrypt the same plaintext 100 times — each ciphertext MUST be different
	// because AES-GCM uses a random nonce.
	plaintext := []byte("same input")
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		ct, err := sm.encrypt(plaintext)
		if err != nil {
			t.Fatalf("encrypt[%d]: %v", i, err)
		}
		key := string(ct)
		if seen[key] {
			t.Errorf("duplicate ciphertext at iteration %d — nonce not random!", i)
		}
		seen[key] = true
	}
}

func TestSecretsDecryptWithWrongKey(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("CUBE_SECRETS_KEY", "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	os.Setenv("CUBE_SECRETS_FILE", dir+"/store.json")

	sm1, err := newSecretsManager()
	if err != nil {
		t.Fatalf("newSecretsManager 1: %v", err)
	}

	// Encrypt with sm1
	ct, err := sm1.encrypt([]byte("secret data"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Try to decrypt with a different key
	os.Setenv("CUBE_SECRETS_KEY", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	os.Setenv("CUBE_SECRETS_FILE", dir+"/store2.json")
	sm2, err := newSecretsManager()
	if err != nil {
		t.Fatalf("newSecretsManager 2: %v", err)
	}

	_, err = sm2.decrypt(ct)
	if err == nil {
		t.Error("expected decryption failure with wrong key, but it succeeded")
	}
}

func TestSecretsSetGetDelete(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("CUBE_SECRETS_KEY", "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	os.Setenv("CUBE_SECRETS_FILE", dir+"/store.json")

	sm, err := newSecretsManager()
	if err != nil {
		t.Fatalf("newSecretsManager: %v", err)
	}

	// Set
	if err := sm.Set("API_KEY", "sk-test-12345"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Get
	val, err := sm.Get("API_KEY")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "sk-test-12345" {
		t.Errorf("Get returned wrong value: got %q, want %q", val, "sk-test-12345")
	}

	// Delete
	if err := sm.Delete("API_KEY"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Verify deleted
	_, err = sm.Get("API_KEY")
	if err == nil {
		t.Error("expected API_KEY to be deleted")
	}
}

func TestSecretsRotateKey(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("CUBE_SECRETS_KEY", "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	os.Setenv("CUBE_SECRETS_FILE", dir+"/store.json")

	sm, err := newSecretsManager()
	if err != nil {
		t.Fatalf("newSecretsManager: %v", err)
	}

	// Store several secrets
	secrets := map[string]string{
		"DB_PASS": "supersecret123",
		"API_KEY": "sk-abcdef",
		"TOKEN":   "tok-xyz789",
	}
	for name, val := range secrets {
		if err := sm.Set(name, val); err != nil {
			t.Fatalf("Set(%s): %v", name, err)
		}
	}

	// Rotate to a new key
	newKey := make([]byte, 32)
	for i := range newKey {
		newKey[i] = byte(0xFF - i)
	}
	if err := sm.RotateKey(newKey); err != nil {
		t.Fatalf("RotateKey: %v", err)
	}

	// Verify all secrets are still readable after rotation
	for name, expectedVal := range secrets {
		val, err := sm.Get(name)
		if err != nil {
			t.Errorf("secret %s not readable after rotation: %v", name, err)
			continue
		}
		if val != expectedVal {
			t.Errorf("secret %s changed after rotation: got %q, want %q", name, val, expectedVal)
		}
	}
}

func TestSecretsRotateKeyInvalidKey(t *testing.T) {
	dir := t.TempDir()
	os.Setenv("CUBE_SECRETS_KEY", "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	os.Setenv("CUBE_SECRETS_FILE", dir+"/store.json")

	sm, err := newSecretsManager()
	if err != nil {
		t.Fatalf("newSecretsManager: %v", err)
	}

	// Key too short
	shortKey := make([]byte, 16)
	if err := sm.RotateKey(shortKey); err == nil {
		t.Error("expected error for short key, got nil")
	}

	// Key too long
	longKey := make([]byte, 64)
	if err := sm.RotateKey(longKey); err == nil {
		t.Error("expected error for long key, got nil")
	}
}

func TestSecretsPersistenceAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	storeFile := dir + "/store.json"
	fixedKey := "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

	os.Setenv("CUBE_SECRETS_KEY", fixedKey)
	os.Setenv("CUBE_SECRETS_FILE", storeFile)

	// First instance stores a secret
	sm1, err := newSecretsManager()
	if err != nil {
		t.Fatalf("newSecretsManager 1: %v", err)
	}
	if err := sm1.Set("PERSISTED_KEY", "persisted-value"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Second instance loads from the same disk paths
	sm2, err := newSecretsManager()
	if err != nil {
		t.Fatalf("newSecretsManager 2: %v", err)
	}

	val, err := sm2.Get("PERSISTED_KEY")
	if err != nil {
		t.Fatalf("expected PERSISTED_KEY to survive restart: %v", err)
	}
	if val != "persisted-value" {
		t.Errorf("got %q, want %q", val, "persisted-value")
	}
}
