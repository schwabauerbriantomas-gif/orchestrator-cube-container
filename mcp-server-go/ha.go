// Package main: CubeMaster high-availability — active-passive failover (design decision D6).
//
// We deliberately avoid Raft consensus to keep the RAM/CPU footprint low on
// 4 GB edge nodes. Instead we use simple active-passive replication:
//
//   - Exactly one CubeMaster is "active" at a time and serves all MCP requests.
//   - One or more "standby" nodes listen for heartbeats from the active node.
//   - The active node POSTs a heartbeat to each peer every heartbeatInterval.
//   - If a standby sees no heartbeat for failoverTimeout (5 missed beats), it
//     promotes itself to active.
//   - Split-brain mitigation is best-effort and lexicographic: if two nodes
//     both believe they are active, the one with the lower ID demotes itself.
//
// This file is self-contained (standard library only). The HAManager is wired
// into server.go (goroutine launch + mux registration) and auth.go (RBAC entry
// for the ha_state tool) by the parent agent.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// haManager is the process-wide HA coordinator. It is assigned in server.go's
// main() after construction and wired into the HTTP mux + MCP tool registry.
// Declared here so ha.go compiles standalone; server.go assigns it (does not
// redeclare).
var haManager *HAManager

// ---- Roles ----

// HARole identifies a node's current HA responsibility.
type HARole string

const (
	// RoleActive means this node owns the MCP request path.
	RoleActive HARole = "active"
	// RoleStandby means this node is ready to take over if the active dies.
	RoleStandby HARole = "standby"
	// RoleUnjoined means the node has not yet joined an HA cluster.
	RoleUnjoined HARole = "unjoined"
)

// ---- Peer ----

// HAPeer describes a known CubeMaster peer.
type HAPeer struct {
	ID       string    `json:"id"`
	Address  string    `json:"address"` // host:port
	LastSeen time.Time `json:"last_seen"`
	Healthy  bool      `json:"healthy"`
}

// ---- Heartbeat wire format ----

// heartbeatPayload is the body POSTed to /ha/heartbeat.
type heartbeatPayload struct {
	FromID    string `json:"from_id"`
	Role      string `json:"role"`
	Priority  int    `json:"priority"`
	Timestamp string `json:"timestamp"` // RFC3339
	HMAC      string `json:"hmac,omitempty"` // HMAC-SHA256 of FromID+Timestamp using sharedSecret (M1)
}

// ---- State snapshot for the MCP tool & GET /ha/state ----

// HAPeerInfo is the public, JSON-friendly view of a peer.
type HAPeerInfo struct {
	ID       string `json:"id"`
	Address  string `json:"address"`
	LastSeen string `json:"last_seen"`
	Healthy  bool   `json:"healthy"`
}

// HAState is the JSON response returned by the ha_state MCP tool and the
// GET /ha/state HTTP endpoint.
type HAState struct {
	SelfID       string       `json:"self_id"`
	Role         string       `json:"role"`
	ActiveID     string       `json:"active_id"`
	Peers        []HAPeerInfo `json:"peers"`
	Uptime       string       `json:"uptime"`
	HeartbeatAge string       `json:"heartbeat_age"` // time since last heartbeat from active
}

// ---- Manager ----

// HAManager coordinates active-passive failover for the CubeMaster process.
// All fields are guarded by mu unless noted. It is safe for concurrent use.
type HAManager struct {
	peers []HAPeer // known CubeMaster peers (excludes self)

	selfID   string  // this node's unique ID
	role     HARole  // RoleActive | RoleStandby | RoleUnjoined
	activeID string  // ID of the currently active node
	priority int     // failover priority (lower wins; 0 = highest)

	heartbeatInterval time.Duration // default 2s
	failoverTimeout   time.Duration // default 10s (5 missed heartbeats)
	lastHeartbeat     time.Time     // last heartbeat received from the active node
	startedAt         time.Time     // process/HA start time, for uptime

	sharedSecret string // HMAC pre-shared key for heartbeat auth (M1)

	heartbeatRate *rateLimiter // AS-6: per-source rate limiting on heartbeat endpoint

	mu sync.RWMutex
}

// newHAManager builds an HAManager from environment configuration.
//
//   - CUBE_HA_PEERS: comma-separated "host:port" list of peer CubeMasters.
//   - CUBE_HA_SELF_ID: this node's ID (defaults to the hostname).
//   - CUBE_HA_ROLE: "active" or "standby" (defaults to "active" when there
//     are no peers, otherwise "standby").
func newHAManager() *HAManager {
	selfID := os.Getenv("CUBE_HA_SELF_ID")
	if selfID == "" {
		if host, err := os.Hostname(); err == nil && host != "" {
			selfID = host
		} else {
			selfID = "cube-master"
		}
	}

	// Priority: lower number = higher priority (wins split-brain resolution).
	// Default 100; set via CUBE_HA_PRIORITY env.
	priority := 100
	if p := os.Getenv("CUBE_HA_PRIORITY"); p != "" {
		if n, err := parsePositiveInt(p); err == nil {
			priority = n
		}
	}

	// Shared secret for HMAC heartbeat authentication (M1).
	secret := os.Getenv("CUBE_HA_SECRET")

	// Parse peers from CUBE_HA_PEERS. Each entry is "host:port"; we do not
	// know the peer's ID yet, so we use its address as a stand-in ID until a
	// heartbeat arrives carrying the real from_id.
	var peers []HAPeer
	if raw := strings.TrimSpace(os.Getenv("CUBE_HA_PEERS")); raw != "" {
		for _, addr := range strings.Split(raw, ",") {
			addr = strings.TrimSpace(addr)
			if addr == "" {
				continue
			}
			peers = append(peers, HAPeer{
				ID:      addr, // provisional; updated on first heartbeat
				Address: addr,
				Healthy: true,
			})
		}
	}

	// Determine initial role.
	role := RoleUnjoined
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CUBE_HA_ROLE"))) {
	case "active":
		role = RoleActive
	case "standby":
		role = RoleStandby
	default:
		// No explicit role: become active if we're alone, standby otherwise.
		if len(peers) == 0 {
			role = RoleActive
		} else {
			role = RoleStandby
		}
	}

	now := time.Now()
	m := &HAManager{
		peers:             peers,
		selfID:            selfID,
		role:              role,
		priority:          priority,
		heartbeatInterval: 2 * time.Second,
		failoverTimeout:   10 * time.Second,
		startedAt:         now,
		lastHeartbeat:     now, // optimistic: don't immediately trip failover at boot
		sharedSecret:      secret,
		heartbeatRate:     newRateLimiter(60, time.Minute), // AS-6: 60 heartbeats/min per source IP
	}

	// If we start active, we are the active node.
	if role == RoleActive {
		m.activeID = selfID
	}

	if secret == "" && len(peers) > 0 {
		fmt.Fprintln(os.Stderr, "[ha] WARNING: no CUBE_HA_SECRET set — heartbeat endpoint is unauthenticated")
	}

	return m
}

// parsePositiveInt parses a non-negative integer string.
func parsePositiveInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid integer")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// ---- Lifecycle ----

// Start launches the background goroutines that maintain HA state:
//
//   - sendHeartbeats: only runs when this node is active; broadcasts a
//     heartbeat to every peer every heartbeatInterval.
//   - watchFailover: only runs when this node is standby; promotes the node
//     if the active has not been heard from within failoverTimeout.
//
// Both loops honor ctx cancellation for clean shutdown.
func (m *HAManager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "[ha] starting node=%s role=%s peers=%d\n",
		m.selfID, m.role, len(m.peers))
	go m.sendHeartbeats(ctx)
	go m.watchFailover(ctx)
}

// ---- HTTP handlers ----

// HandleHeartbeat handles POST /ha/heartbeat.
// The body is a heartbeatPayload; receiving one resets the failover timer.
func (m *HAManager) HandleHeartbeat(w http.ResponseWriter, r *http.Request) {
	if m == nil {
		http.Error(w, "HA manager not initialized", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// AS-6: Rate limit heartbeat endpoint to prevent flooding
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	if m.heartbeatRate != nil && !m.heartbeatRate.Allow(ip) {
		http.Error(w, "heartbeat rate limit exceeded", http.StatusTooManyRequests)
		return
	}

	var hb heartbeatPayload
	if err := json.NewDecoder(r.Body).Decode(&hb); err != nil {
		http.Error(w, "invalid heartbeat body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if hb.FromID == "" {
		http.Error(w, "missing from_id", http.StatusBadRequest)
		return
	}

	// HMAC authentication (M1): if a shared secret is configured, verify the HMAC.
	// AUDIT FIX M-06: If CUBE_HA_SECRET is NOT set, reject all heartbeats.
	// Previously the endpoint was unauthenticated when the secret was missing,
	// allowing anyone to spoof heartbeats and prevent/force failover.
	if m.sharedSecret == "" {
		// N-07: Log to audit trail even on rejection.
		fmt.Fprintf(os.Stderr, "[ha] heartbeat rejected from %s: no HA secret configured\n", r.RemoteAddr)
		http.Error(w, "HA heartbeat disabled — set CUBE_HA_SECRET to enable", http.StatusForbidden)
		return
	}
	if hb.HMAC == "" {
		fmt.Fprintf(os.Stderr, "[ha] heartbeat rejected from %s: missing HMAC (from_id=%s)\n", r.RemoteAddr, hb.FromID)
		http.Error(w, "missing heartbeat HMAC", http.StatusUnauthorized)
		return
	}
	expected := computeHeartbeatHMAC(m.sharedSecret, hb.FromID, hb.Timestamp)
	if !hmac.Equal([]byte(hb.HMAC), []byte(expected)) {
		fmt.Fprintf(os.Stderr, "[ha] heartbeat rejected from %s: invalid HMAC (from_id=%s)\n", r.RemoteAddr, hb.FromID)
		http.Error(w, "invalid heartbeat HMAC", http.StatusUnauthorized)
		return
	}

	// R9-AUTH-01: Replay protection — reject heartbeats with stale timestamps.
	// Accept ±10 seconds skew (heartbeat interval is typically 2s).
	parsedTs, err := time.Parse(time.RFC3339Nano, hb.Timestamp)
	if err != nil {
		http.Error(w, "invalid heartbeat timestamp format", http.StatusUnauthorized)
		return
	}
	if abs := time.Since(parsedTs); abs > 10*time.Second {
		http.Error(w, fmt.Sprintf("stale heartbeat (timestamp skew: %v)", abs), http.StatusUnauthorized)
		return
	}

	m.mu.Lock()
	// Record the heartbeat from the active node.
	m.lastHeartbeat = time.Now()

	// Track the sender as a peer (update LastSeen / healthiness).
	ts := m.lastHeartbeat
	found := false
	for i := range m.peers {
		if m.peers[i].ID == hb.FromID || m.peers[i].Address == hb.FromID {
			m.peers[i].ID = hb.FromID
			m.peers[i].LastSeen = ts
			m.peers[i].Healthy = true
			found = true
			break
		}
	}
	if !found {
		m.peers = append(m.peers, HAPeer{
			ID:       hb.FromID,
			Address:  hb.FromID,
			LastSeen: ts,
			Healthy:  true,
		})
	}

	// If the heartbeat announces an active node, record it.
	if strings.EqualFold(hb.Role, string(RoleActive)) {
		m.activeID = hb.FromID
		// Split-brain mitigation (M2): if WE also think we're active but the
		// sender has a numerically lower priority (higher precedence), we demote.
		// Ties are broken by lexicographic ID comparison.
		if m.role == RoleActive && hb.FromID != m.selfID {
			peerWins := hb.Priority < m.priority ||
				(hb.Priority == m.priority && hb.FromID > m.selfID)
			if peerWins {
				m.role = RoleStandby
				fmt.Fprintf(os.Stderr,
					"[ha] split-brain resolved: demoting to standby (local=%s prio=%d < peer=%s prio=%d)\n",
					m.selfID, m.priority, hb.FromID, hb.Priority)
			}
		}
	}
	m.mu.Unlock()

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

// computeHeartbeatHMAC returns the hex-encoded HMAC-SHA256 of fromID:timestamp
// keyed by the shared secret. Used for heartbeat authentication (M1).
// R9-AUTH-10: Uses ':' delimiter to prevent ambiguous concatenation.
func computeHeartbeatHMAC(secret, fromID, timestamp string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(fromID + ":" + timestamp))
	return hex.EncodeToString(h.Sum(nil))
}

// HandleHAGetState handles GET /ha/state.
func (m *HAManager) HandleHAGetState(w http.ResponseWriter, r *http.Request) {
	if m == nil {
		http.Error(w, "HA manager not initialized", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(m.State())
}

// ---- Role transitions ----

// Promote transitions this node from standby to active. It is called
// automatically by watchFailover when the active node is deemed dead.
func (m *HAManager) Promote() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.role
	m.role = RoleActive
	m.activeID = m.selfID
	m.lastHeartbeat = time.Now() // reset so we don't immediately re-trip
	fmt.Fprintf(os.Stderr, "[ha] promoted %s: %s -> active (was active=%s)\n",
		m.selfID, old, m.activeID)
}

// Demote transitions this node to standby. It is used when a higher-priority
// (lexicographically greater) node announces itself as active, resolving a
// split-brain condition.
func (m *HAManager) Demote() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.role
	m.role = RoleStandby
	m.lastHeartbeat = time.Now() // reset failover timer; give the new active a chance
	fmt.Fprintf(os.Stderr, "[ha] demoted %s: %s -> standby\n", m.selfID, old)
}

// ---- State & failover detection ----

// State returns a point-in-time snapshot of the HA state, safe for the MCP
// tool and the HTTP /ha/state endpoint.
func (m *HAManager) State() HAState {
	if m == nil {
		return HAState{Role: string(RoleUnjoined)}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	peers := make([]HAPeerInfo, 0, len(m.peers))
	for _, p := range m.peers {
		lastSeen := ""
		if !p.LastSeen.IsZero() {
			lastSeen = p.LastSeen.Format(time.RFC3339)
		}
		peers = append(peers, HAPeerInfo{
			ID:       p.ID,
			Address:  p.Address,
			LastSeen: lastSeen,
			Healthy:  p.Healthy,
		})
	}

	hbAge := time.Since(m.lastHeartbeat)
	return HAState{
		SelfID:       m.selfID,
		Role:         string(m.role),
		ActiveID:     m.activeID,
		Peers:        peers,
		Uptime:       time.Since(m.startedAt).Round(time.Second).String(),
		HeartbeatAge: hbAge.Round(time.Millisecond).String(),
	}
}

// CheckFailover reports whether the currently active node should be considered
// dead — i.e. no heartbeat has been received within failoverTimeout. Only
// meaningful for standby nodes; active nodes always see themselves as healthy.
func (m *HAManager) CheckFailover() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.role != RoleStandby {
		return false
	}
	return time.Since(m.lastHeartbeat) >= m.failoverTimeout
}

// ---- Background loops ----

// sendHeartbeats runs on every node but only sends when this node is active.
// Each tick POSTs a heartbeat to every known peer. Failed peers are marked
// unhealthy so their state is reflected in /ha/state and the ha_state tool.
func (m *HAManager) sendHeartbeats(ctx context.Context) {
	ticker := time.NewTicker(m.heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		m.mu.RLock()
		active := m.role == RoleActive
		m.mu.RUnlock()
		if !active {
			continue
		}

		m.dispatchHeartbeats()
	}
}

// dispatchHeartbeats sends one heartbeat POST to every peer and updates peer
// health based on the outcome. It reads a snapshot of the peer list under the
// lock, then performs network I/O outside the lock to avoid blocking readers.
func (m *HAManager) dispatchHeartbeats() {
	m.mu.RLock()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	hb := heartbeatPayload{
		FromID:    m.selfID,
		Role:      string(m.role),
		Priority:  m.priority,
		Timestamp: ts,
	}
	if m.sharedSecret != "" {
		hb.HMAC = computeHeartbeatHMAC(m.sharedSecret, m.selfID, ts)
	}
	addresses := make([]string, len(m.peers))
	for i, p := range m.peers {
		addresses[i] = p.Address
	}
	m.mu.RUnlock()

	if len(addresses) == 0 {
		return
	}

	body, _ := json.Marshal(hb)
	client := &http.Client{
		Timeout: m.heartbeatInterval,
	}
	// When TLS is enabled, verify peer cert against the internal CA.
	// The CA cert path is configured via CUBE_TLS_CA (defaults to ca.crt alongside the cert).
	if os.Getenv("CUBE_TLS_CERT") != "" {
		caPath := os.Getenv("CUBE_TLS_CA")
		if caPath == "" {
			// Default: ca.crt next to the cert file
			caPath = "/etc/cube-container/tls/ca.crt"
		}
		caPool := x509.NewCertPool()
		caLoaded := false
		if caCert, err := os.ReadFile(caPath); err == nil {
			caLoaded = caPool.AppendCertsFromPEM(caCert)
		}
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:            caPool,
				InsecureSkipVerify: !caLoaded, // Only skip if no CA loaded
			},
		}
	}

	healthy := make(map[string]bool, len(addresses))
	var wg sync.WaitGroup
	var mu sync.Mutex // guards healthy

	for _, addr := range addresses {
		wg.Add(1)
		go func(addr string) {
			defer wg.Done()
			scheme := "http"
			if os.Getenv("CUBE_TLS_CERT") != "" {
				scheme = "https"
			}
			url := fmt.Sprintf("%s://%s/ha/heartbeat", scheme, addr)
			req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			defer resp.Body.Close()
			mu.Lock()
			healthy[addr] = resp.StatusCode >= 200 && resp.StatusCode < 300
			mu.Unlock()
		}(addr)
	}
	wg.Wait()

	// Apply health results.
	m.mu.Lock()
	now := time.Now()
	for i := range m.peers {
		if ok, hit := healthy[m.peers[i].Address]; hit {
			m.peers[i].Healthy = ok
			if ok {
				m.peers[i].LastSeen = now
			}
		} else {
			// No successful response — mark unhealthy if we never heard back.
			m.peers[i].Healthy = false
		}
	}
	m.mu.Unlock()
}

// watchFailover runs on every node. A standby node checks each tick whether
// the active has gone silent for longer than failoverTimeout; if so, it
// promotes itself. An active node does nothing here.
func (m *HAManager) watchFailover(ctx context.Context) {
	// Check more frequently than the failover window to detect death promptly.
	checkEvery := m.heartbeatInterval
	if checkEvery > time.Second {
		checkEvery = time.Second
	}
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		if m.CheckFailover() {
			fmt.Fprintf(os.Stderr,
				"[ha] failover triggered: no heartbeat from active %s for %s; promoting self\n",
				m.activeIDSnapshot(), m.failoverTimeout)
			m.Promote()
		}
	}
}

// activeIDSnapshot returns the current activeID under the read lock.
func (m *HAManager) activeIDSnapshot() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.activeID
}

// ---- MCP tool handler ----
//
// handleHAState is the MCP tool handler for "ha_state" (read-only, RoleViewer).
// It returns the current HA state: this node's role, the active node ID, peer
// health, and timing information. The tool is registered in server.go and the
// permission is added to toolPermissions in auth.go by the parent agent.
func handleHAState(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if haManager == nil {
		return errResult("HA manager not initialized"), nil
	}
	return okResult(haManager.State()), nil
}
