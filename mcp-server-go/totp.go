// Package main: TOTP (RFC 6238) second-factor authentication.
//
// Implements Time-based One-Time Passwords compatible with
// Microsoft Authenticator, Google Authenticator, and Authy.
//
// Design:
//   - TOTP is OPTIONAL per API key. Admins can require it for their own keys.
//   - Automated agents (CI/CD, scripts) use API keys WITHOUT TOTP — they
//     can't provide a time-based code. Their security comes from scoped
//     roles (viewer/operator), rate limiting, and audit logging.
//   - Human admins enroll via QR code, then must provide a 6-digit code
//     on every API call when TOTP is enabled for their key.
//   - Destructive operations (delete_volume, vm_delete, restore_backup)
//     can require a fresh TOTP code even when TOTP is optional, similar
//     to Steam Guard's trade confirmation flow.
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---- TOTP constants (RFC 6238) ----

const (
	totpPeriod       = 30      // seconds per code
	totpDigits       = 6       // 6-digit code
	totpSecretLength = 20      // 160 bits (RFC 4226 recommended)
	totpSkew         = 1       // allow ±1 period (±30s) for clock drift
	totpIssuer       = "CubeContainer"
)

// totpAlgorithm is HMAC-SHA1 (RFC 6238 default, compatible with all
// authenticator apps including Google/Microsoft Authenticator).
var totpAlgorithm = sha1.New

// ---- TOTP secret store ----

// TOTPStore manages TOTP secrets per API key.
// Secrets are stored in-memory only (never persisted to disk in plaintext).
// The enrollment state (enabled/disabled) IS persisted via the KeyStore.
type TOTPStore struct {
	mu      sync.RWMutex
	secrets map[string][]byte // keyID → TOTP secret
}

func newTOTPStore() *TOTPStore {
	return &TOTPStore{
		secrets: make(map[string][]byte),
	}
}

// Enroll generates a new TOTP secret for the given API key.
// Returns the secret as a base32 string and an otpauth:// URL for QR code
// generation. The caller must verify a code before the enrollment is
// considered active.
func (ts *TOTPStore) Enroll(keyID, accountName string) (secret string, otpauthURL string, err error) {
	raw := make([]byte, totpSecretLength)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("failed to generate TOTP secret: %w", err)
	}

	// Base32 encode (no padding — authenticator apps don't expect it)
	encoder := base32.StdEncoding.WithPadding(base32.NoPadding)
	secretB32 := encoder.EncodeToString(raw)

	// Store secret
	ts.mu.Lock()
	ts.secrets[keyID] = raw
	ts.mu.Unlock()

	// Build otpauth:// URL (compatible with Google/Microsoft Authenticator)
	// Format: otpauth://totp/Issuer:account?secret=BASE32&issuer=Issuer&algorithm=SHA1&digits=6&period=30
	label := fmt.Sprintf("%s:%s", totpIssuer, accountName)
	otpauthURL = fmt.Sprintf("otpauth://totp/%s?secret=%s&issuer=%s&algorithm=SHA1&digits=%d&period=%d",
		label, secretB32, totpIssuer, totpDigits, totpPeriod)

	return secretB32, otpauthURL, nil
}

// Verify checks a TOTP code against the stored secret.
// Allows ±totpSkew periods for clock drift.
func (ts *TOTPStore) Verify(keyID, code string) bool {
	ts.mu.RLock()
	secret, exists := ts.secrets[keyID]
	ts.mu.RUnlock()

	if !exists {
		return false
	}

	// Parse the code as an integer
	var codeInt int
	if _, err := fmt.Sscanf(code, "%06d", &codeInt); err != nil {
		return false
	}
	if codeInt < 0 || codeInt >= 1000000 {
		return false
	}

	now := time.Now().Unix()
	for skew := -int64(totpSkew); skew <= int64(totpSkew); skew++ {
		counter := (now / int64(totpPeriod)) + skew
		expected := generateTOTP(secret, counter)
		if hmac.Equal([]byte(fmt.Sprintf("%06d", expected)), []byte(fmt.Sprintf("%06d", codeInt))) {
			return true
		}
	}
	return false
}

// Remove deletes the TOTP secret for a key (used when disabling TOTP).
func (ts *TOTPStore) Remove(keyID string) {
	ts.mu.Lock()
	delete(ts.secrets, keyID)
	ts.mu.Unlock()
}

// HasTOTP checks if a key has a TOTP secret enrolled.
func (ts *TOTPStore) HasTOTP(keyID string) bool {
	ts.mu.RLock()
	_, exists := ts.secrets[keyID]
	ts.mu.RUnlock()
	return exists
}

// ---- TOTP code generation (RFC 4226 HOTP / RFC 6238 TOTP) ----

// generateTOTP computes a TOTP code for the given counter.
// Implements the HOTP algorithm with HMAC-SHA1.
func generateTOTP(secret []byte, counter int64) int {
	// Convert counter to 8-byte big-endian
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(counter)) //nosec G115 -- counter is always positive in TOTP (time-based)

	// HMAC-SHA1
	mac := hmac.New(totpAlgorithm, secret)
	mac.Write(buf[:])
	hash := mac.Sum(nil)

	// Dynamic truncation (RFC 4226 §5.4)
	offset := int(hash[len(hash)-1] & 0x0f)
	binaryCode := ((int(hash[offset]) & 0x7f) << 24) |
		((int(hash[offset+1]) & 0xff) << 16) |
		((int(hash[offset+2]) & 0xff) << 8) |
		(int(hash[offset+3]) & 0xff)

	// Extract 6 digits
	return binaryCode % 1000000
}

// CurrentCode generates the current TOTP code for a secret.
// This is used only for testing and enrollment verification — never
// exposed via the API in production.
func currentTOTPCode(secret []byte) string {
	counter := time.Now().Unix() / int64(totpPeriod)
	return fmt.Sprintf("%06d", generateTOTP(secret, counter))
}

// ---- TOTP-protected tool set ----

// totpRequiredTools are tools that require a fresh TOTP code even when
// TOTP is not globally enabled for the key. This is the "Steam Guard"
// pattern: destructive operations need explicit confirmation.
var totpRequiredTools = map[string]bool{
	"delete_volume":              true,
	"vm_delete":                  true,
	"restore_backup":             true,
	"delete_backup":              true,
	"zpool_destroy":              true,
	"zdataset_destroy":           true,
	"zsnapshot_rollback":         true,
	"rollback_deploy":            true,
	"secure_sandbox_restore":     true,
	"remove_network_policy":      true,
	"node_remove":                true,
	"service_remove":             true,
}

// IsTOTPRequired returns true if the tool requires a TOTP confirmation.
func IsTOTPRequired(toolName string) bool {
	return totpRequiredTools[toolName]
}

// ---- Utility: sanitize account name for otpauth URL ----

func sanitizeAccountName(name string) string {
	// Remove characters that would break the otpauth URL
	name = strings.ReplaceAll(name, ":", "")
	name = strings.ReplaceAll(name, "?", "")
	name = strings.ReplaceAll(name, "&", "")
	name = strings.ReplaceAll(name, "=", "")
	name = strings.TrimSpace(name)
	if name == "" {
		return "admin"
	}
	return name
}
