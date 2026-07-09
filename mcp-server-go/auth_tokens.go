// Package main: programmatic API token management via MCP tools.
//
// The auth system in auth.go supports API keys with RBAC roles (viewer,
// operator, admin). Previously, creating/revoking tokens required CLI
// access (--gen-key flag). These tools let the LLM manage tokens directly,
// enabling automated key rotation and onboarding of new users/services.
//
// Security note: these tools require admin role. Created tokens are returned
// exactly once — the secret is not retrievable after creation (only the key ID).
package main

import (
	"fmt"
	"time"
)

// ---- Types ----

type CreateTokenResult struct {
	Key       string    `json:"key"`       // returned ONCE — the actual credential
	Secret    string    `json:"secret"`    // returned ONCE
	Role      string    `json:"role"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
}

type ListTokensResult struct {
	Tokens []TokenInfo `json:"tokens"`
	Total  int         `json:"total"`
}

type TokenInfo struct {
	Key       string    `json:"key"`
	Role      string    `json:"role"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used,omitempty"`
	Disabled  bool      `json:"disabled"`
}

type RevokeTokenResult struct {
	Key   string `json:"key"`
	Status string `json:"status"`
}

// ---- Handlers ----

// Note: these functions use the global keyStore from auth.go

func createToken(role, label string) (*CreateTokenResult, error) {
	if keyStore == nil {
		return nil, fmt.Errorf("auth key store is not initialized")
	}

	r := Role(role)
	if r != RoleViewer && r != RoleOperator && r != RoleAdmin {
		return nil, fmt.Errorf("invalid role: %s (must be viewer, operator, or admin)", role)
	}
	if label == "" {
		label = "mcp-created"
	}

	key, err := keyStore.GenerateKey(r, label)
	if err != nil {
		return nil, fmt.Errorf("failed to generate key: %w", err)
	}

	return &CreateTokenResult{
		Key:       key.Key,
		Secret:    key.Secret,
		Role:      string(key.Role),
		Label:     key.Label,
		CreatedAt: key.CreatedAt,
	}, nil
}

func listTokens() (*ListTokensResult, error) {
	if keyStore == nil {
		return nil, fmt.Errorf("auth key store is not initialized")
	}

	keyStore.mu.RLock()
	defer keyStore.mu.RUnlock()

	result := &ListTokensResult{
		Tokens: []TokenInfo{},
	}

	for _, k := range keyStore.keys {
		result.Tokens = append(result.Tokens, TokenInfo{
			Key:       k.Key,
			Role:      string(k.Role),
			Label:     k.Label,
			CreatedAt: k.CreatedAt,
			LastUsed:  k.LastUsed,
			Disabled:  k.Disabled,
		})
	}

	result.Total = len(result.Tokens)
	return result, nil
}

func revokeToken(keyID string) (*RevokeTokenResult, error) {
	if keyStore == nil {
		return nil, fmt.Errorf("auth key store is not initialized")
	}
	if keyID == "" {
		return nil, fmt.Errorf("key is required")
	}

	if err := keyStore.Revoke(keyID); err != nil {
		return nil, fmt.Errorf("failed to revoke key: %w", err)
	}

	return &RevokeTokenResult{
		Key:    keyID,
		Status: "revoked",
	}, nil
}
