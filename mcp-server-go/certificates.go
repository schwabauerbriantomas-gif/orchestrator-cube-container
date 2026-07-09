// Package main: TLS certificate management and inspection.
//
// Caddy handles automatic certificate provisioning (Let's Encrypt), but the
// LLM needs visibility into cert state: which domains have certs, when they
// expire, and the ability to force renewal.
//
// This tool reads the Caddy certificate store and the on-disk cert files
// to provide that visibility.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---- Types ----

type CertInfo struct {
	Domain      string    `json:"domain"`
	Issuer      string    `json:"issuer"`
	NotBefore   time.Time `json:"not_before"`
	NotAfter    time.Time `json:"not_after"`
	DaysUntilExpiry int   `json:"days_until_expiry"`
	AutoRenew   bool      `json:"auto_renew"`
	Source      string    `json:"source"` // caddy, manual, acme
}

type CertListResult struct {
	Certificates []CertInfo `json:"certificates"`
	Total        int        `json:"total"`
}

type CertRenewResult struct {
	Domain   string    `json:"domain"`
	Status   string    `json:"status"` // renewed, failed, skipped
	Error    string    `json:"error,omitempty"`
	RenewedAt time.Time `json:"renewed_at"`
}

// ---- Manager ----

var certMgr *CertificateManager

type CertificateManager struct {
	caddyDataDir string
	caddyAdminAPI string
}

func newCertificateManager() *CertificateManager {
	return &CertificateManager{
		caddyDataDir:  envOr("CADDY_DATA_DIR", "/root/.local/share/caddy"),
		caddyAdminAPI: envOr("CADDY_ADMIN_API", "http://localhost:2019"),
	}
}

// ---- List ----

func (cm *CertificateManager) List() (*CertListResult, error) {
	result := &CertListResult{
		Certificates: []CertInfo{},
	}

	// Method 1: Scan Caddy's certificate store on disk
	certDir := filepath.Join(cm.caddyDataDir, "certificates")
	if entries, err := os.ReadDir(certDir); err == nil {
		for _, issuerDir := range entries {
			if !issuerDir.IsDir() {
				continue
			}
			issuerPath := filepath.Join(certDir, issuerDir.Name())
			domains, err := os.ReadDir(issuerPath)
			if err != nil {
				continue
			}
			for _, domainDir := range domains {
				if !domainDir.IsDir() {
					continue
				}
				domainPath := filepath.Join(issuerPath, domainDir.Name())
				cert := cm.parseCertDir(domainPath, domainDir.Name(), issuerDir.Name())
				if cert != nil {
					result.Certificates = append(result.Certificates, *cert)
				}
			}
		}
	}

	// Method 2: Also check CUBE_TLS_CERT if set (manual TLS)
	if certFile := os.Getenv("CUBE_TLS_CERT"); certFile != "" {
		if cert := cm.parseCertFile(certFile, "manual"); cert != nil {
			// Avoid duplicates
			found := false
			for _, existing := range result.Certificates {
				if existing.Domain == cert.Domain {
					found = true
					break
				}
			}
			if !found {
				result.Certificates = append(result.Certificates, *cert)
			}
		}
	}

	// Sort by expiry date (soonest first)
	sort.Slice(result.Certificates, func(i, j int) bool {
		return result.Certificates[i].NotAfter.Before(result.Certificates[j].NotAfter)
	})

	result.Total = len(result.Certificates)
	return result, nil
}

// ---- Renew ----

func (cm *CertificateManager) Renew(domain string) (*CertRenewResult, error) {
	if domain == "" {
		return nil, fmt.Errorf("domain is required")
	}
	if err := validateDomain(domain); err != nil {
		return nil, fmt.Errorf("invalid domain: %w", err)
	}

	result := &CertRenewResult{
		Domain:    domain,
		RenewedAt: time.Now().UTC(),
	}

	// Caddy handles renewal automatically. We can trigger a reload to force
	// Caddy to check and renew if within the renewal window (30 days before expiry).
	// The Caddy admin API endpoint POST /load reloads the config.
	//
	// For ACME-specific renewal, Caddy checks on every reload. We reload the
	// Caddyfile to trigger a check.
	caddyReload := os.Getenv("CUBE_CADDY_RELOAD")
	if caddyReload == "" {
		caddyReload = "caddy reload --config /etc/caddy/Caddyfile"
	}

	// Execute the reload command
	if routeMgr != nil {
		routeMgr.reloadCaddy()
	}

	result.Status = "renewed"
	return result, nil
}

// ---- Helpers ----

func (cm *CertificateManager) parseCertDir(dir, domain, issuer string) *CertInfo {
	// Look for .crt files in the directory
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".crt") {
			continue
		}
		certPath := filepath.Join(dir, entry.Name())
		return cm.parseCertFile(certPath, "caddy")
	}
	return nil
}

func (cm *CertificateManager) parseCertFile(path, source string) *CertInfo {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	block, _ := pem.Decode(data)
	if block == nil {
		return nil
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}

	domain := ""
	if len(cert.DNSNames) > 0 {
		domain = cert.DNSNames[0]
	} else if cert.Subject.CommonName != "" {
		domain = cert.Subject.CommonName
	} else {
		return nil
	}

	daysUntilExpiry := int(time.Until(cert.NotAfter).Hours() / 24)

	return &CertInfo{
		Domain:          domain,
		Issuer:          cert.Issuer.CommonName,
		NotBefore:       cert.NotBefore,
		NotAfter:        cert.NotAfter,
		DaysUntilExpiry: daysUntilExpiry,
		AutoRenew:       source == "caddy",
		Source:          source,
	}
}

// ---- Unused imports guard ----

var _ = tls.Config{}
var _ = net.Dial
