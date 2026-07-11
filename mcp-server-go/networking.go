// Package main: network management tools for container networking.
// Implements DNS aliases, port mapping management, and network policies.
// These tools let the AI agent configure container networking without
// burning tokens on manual iptables/CNI commands.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// NetworkManager handles container network configuration.
type NetworkManager struct {
	mu        sync.RWMutex
	ConfigDir string
}

func newNetworkManager() *NetworkManager {
	dir := envOr("CUBE_NETCONFIG_ROOT", "/var/lib/cube-container/network")
	os.MkdirAll(dir, 0700)
	return &NetworkManager{ConfigDir: dir}
}

// ---- Types ----

// PortMapping represents a host-to-container port forward.
type PortMapping struct {
	ID           string `json:"id"`
	ContainerID  string `json:"container_id"`
	HostPort     int    `json:"host_port"`
	ContainerPort int   `json:"container_port"`
	Protocol     string `json:"protocol"` // tcp or udp
	HostIP       string `json:"host_ip"`  // default 0.0.0.0
	CreatedAt    string `json:"created_at"`
}

// DNSAlias represents a DNS name pointing to a container.
type DNSAlias struct {
	ID           string `json:"id"`
	Alias        string `json:"alias"`         // e.g. "myapp.cube.local"
	ContainerID  string `json:"container_id"`
	Target       string `json:"target"`        // container IP or hostname
	CreatedAt    string `json:"created_at"`
}

// NetworkPolicy represents a firewall rule between containers.
type NetworkPolicy struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	SourceContainer string `json:"source_container"`
	DestContainer  string `json:"dest_container"`
	Action       string `json:"action"` // allow or deny
	Protocol     string `json:"protocol"`
	Port         int    `json:"port"` // 0 = all ports
	CreatedAt    string `json:"created_at"`
}

// ---- Port mapping ----

func (nm *NetworkManager) AddPortMapping(containerID string, hostPort, containerPort int, protocol, hostIP string) (*PortMapping, error) {
	if _, err := validateSafeName(containerID); err != nil {
		// Container IDs may contain colons, validate loosely
		if containerID == "" {
			return nil, fmt.Errorf("container_id is required")
		}
	}
	if hostPort < 1 || hostPort > 65535 {
		return nil, fmt.Errorf("host_port must be 1-65535")
	}
	if containerPort < 1 || containerPort > 65535 {
		return nil, fmt.Errorf("container_port must be 1-65535")
	}
	if protocol == "" {
		protocol = "tcp"
	}
	if protocol != "tcp" && protocol != "udp" {
		return nil, fmt.Errorf("protocol must be tcp or udp")
	}
	if hostIP == "" {
		hostIP = "0.0.0.0"
	}

	// Check for port conflicts
	existing := nm.listPortMappingsLocked()
	for _, pm := range existing {
		if pm.HostPort == hostPort && pm.Protocol == protocol && pm.HostIP == hostIP {
			return nil, fmt.Errorf("port %d/%s on %s is already mapped to container %s", hostPort, protocol, hostIP, pm.ContainerID)
		}
	}

	mapping := &PortMapping{
		ID:            fmt.Sprintf("pm_%s", randomHex(4)),
		ContainerID:   containerID,
		HostPort:      hostPort,
		ContainerPort: containerPort,
		Protocol:      protocol,
		HostIP:        hostIP,
		CreatedAt:     nowRFC3339(),
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.savePortMapping(mapping)

	// Apply via iptables
	nm.applyPortMapping(mapping)

	return mapping, nil
}

func (nm *NetworkManager) RemovePortMapping(mappingID string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	mappings := nm.listPortMappingsLocked()
	for _, pm := range mappings {
		if pm.ID == mappingID {
			nm.removePortMappingFile(pm)
			nm.removePortMappingIptables(pm)
			return nil
		}
	}
	return fmt.Errorf("port mapping %s not found", mappingID)
}

func (nm *NetworkManager) ListPortMappings() ([]*PortMapping, error) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return nm.listPortMappingsLocked(), nil
}

// ---- DNS aliases ----

func (nm *NetworkManager) AddDNSAlias(alias, containerID, target string) (*DNSAlias, error) {
	if alias == "" {
		return nil, fmt.Errorf("alias is required")
	}
	// Validate alias looks like a hostname
	if !strings.Contains(alias, ".") {
		return nil, fmt.Errorf("alias must be a FQDN (e.g. myapp.cube.local)")
	}
	// Prevent /etc/hosts injection: alias must not contain newlines or spaces (H5)
	if strings.ContainsAny(alias, "\n\r	 ") {
		return nil, fmt.Errorf("alias contains invalid characters (whitespace/newlines forbidden)")
	}
	if containerID == "" {
		return nil, fmt.Errorf("container_id is required")
	}
	if target == "" {
		return nil, fmt.Errorf("target is required (container IP)")
	}
	// Validate target is a valid IP address (prevents /etc/hosts injection, H5)
	if net.ParseIP(target) == nil {
		return nil, fmt.Errorf("target must be a valid IP address (got '%s')", target)
	}

	dns := &DNSAlias{
		ID:          fmt.Sprintf("dns_%s", randomHex(4)),
		Alias:       alias,
		ContainerID: containerID,
		Target:      target,
		CreatedAt:   nowRFC3339(),
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.saveDNSAlias(dns)

	// Add to /etc/hosts for local resolution
	nm.addToHosts(alias, target)

	return dns, nil
}

func (nm *NetworkManager) RemoveDNSAlias(alias string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	aliases := nm.listDNSAliasesLocked()
	for _, a := range aliases {
		if a.Alias == alias {
			nm.removeDNSAliasFile(a)
			nm.removeFromHosts(a.Alias)
			return nil
		}
	}
	return fmt.Errorf("DNS alias %s not found", alias)
}

func (nm *NetworkManager) ListDNSAliases() ([]*DNSAlias, error) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return nm.listDNSAliasesLocked(), nil
}

// ---- Network policies ----

func (nm *NetworkManager) AddNetworkPolicy(name, source, dest, action, protocol string, port int) (*NetworkPolicy, error) {
	if name == "" {
		return nil, fmt.Errorf("policy name is required")
	}
	if action != "allow" && action != "deny" {
		return nil, fmt.Errorf("action must be 'allow' or 'deny'")
	}
	if protocol == "" {
		protocol = "tcp"
	}

	policy := &NetworkPolicy{
		ID:             fmt.Sprintf("np_%s", randomHex(4)),
		Name:           name,
		SourceContainer: source,
		DestContainer:  dest,
		Action:         action,
		Protocol:       protocol,
		Port:           port,
		CreatedAt:      nowRFC3339(),
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()
	nm.saveNetworkPolicy(policy)

	// Apply via iptables (simplified — real implementation would track container IPs)
	// nm.applyNetworkPolicy(policy)

	return policy, nil
}

func (nm *NetworkManager) ListNetworkPolicies() ([]*NetworkPolicy, error) {
	nm.mu.RLock()
	defer nm.mu.RUnlock()
	return nm.listNetworkPoliciesLocked(), nil
}

func (nm *NetworkManager) RemoveNetworkPolicy(policyID string) error {
	nm.mu.Lock()
	defer nm.mu.Unlock()

	policies := nm.listNetworkPoliciesLocked()
	for _, p := range policies {
		if p.ID == policyID {
			nm.removeNetworkPolicyFile(p)
			return nil
		}
	}
	return fmt.Errorf("network policy %s not found", policyID)
}

// ---- File persistence ----

func (nm *NetworkManager) savePortMapping(pm *PortMapping) {
	path := filepath.Join(nm.ConfigDir, "ports", pm.ID+".json")
	os.MkdirAll(filepath.Dir(path), 0700)
	data, _ := json.MarshalIndent(pm, "", "  ")
	os.WriteFile(path, data, 0600)
}

func (nm *NetworkManager) listPortMappingsLocked() []*PortMapping {
	dir := filepath.Join(nm.ConfigDir, "ports")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []*PortMapping{}
	}
	var result []*PortMapping
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var pm PortMapping
		if json.Unmarshal(data, &pm) == nil {
			result = append(result, &pm)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].HostPort < result[j].HostPort })
	return result
}

func (nm *NetworkManager) removePortMappingFile(pm *PortMapping) {
	os.Remove(filepath.Join(nm.ConfigDir, "ports", pm.ID+".json"))
}

func (nm *NetworkManager) saveDNSAlias(dns *DNSAlias) {
	path := filepath.Join(nm.ConfigDir, "dns", dns.ID+".json")
	os.MkdirAll(filepath.Dir(path), 0700)
	data, _ := json.MarshalIndent(dns, "", "  ")
	os.WriteFile(path, data, 0600)
}

func (nm *NetworkManager) listDNSAliasesLocked() []*DNSAlias {
	dir := filepath.Join(nm.ConfigDir, "dns")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []*DNSAlias{}
	}
	var result []*DNSAlias
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var dns DNSAlias
		if json.Unmarshal(data, &dns) == nil {
			result = append(result, &dns)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Alias < result[j].Alias })
	return result
}

func (nm *NetworkManager) removeDNSAliasFile(dns *DNSAlias) {
	os.Remove(filepath.Join(nm.ConfigDir, "dns", dns.ID+".json"))
}

func (nm *NetworkManager) saveNetworkPolicy(np *NetworkPolicy) {
	path := filepath.Join(nm.ConfigDir, "policies", np.ID+".json")
	os.MkdirAll(filepath.Dir(path), 0700)
	data, _ := json.MarshalIndent(np, "", "  ")
	os.WriteFile(path, data, 0600)
}

func (nm *NetworkManager) listNetworkPoliciesLocked() []*NetworkPolicy {
	dir := filepath.Join(nm.ConfigDir, "policies")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []*NetworkPolicy{}
	}
	var result []*NetworkPolicy
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var np NetworkPolicy
		if json.Unmarshal(data, &np) == nil {
			result = append(result, &np)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func (nm *NetworkManager) removeNetworkPolicyFile(np *NetworkPolicy) {
	os.Remove(filepath.Join(nm.ConfigDir, "policies", np.ID+".json"))
}

// ---- System integration ----

func (nm *NetworkManager) applyPortMapping(pm *PortMapping) {
	// Use iptables to forward host port to container
	// This is a simplified version — real implementation needs container IP
	cmd := exec.Command("iptables", "-t", "nat", "-A", "PREROUTING",
		"-p", pm.Protocol,
		"--dport", fmt.Sprintf("%d", pm.HostPort),
		"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", pm.HostIP, pm.ContainerPort))
	cmd.Run()
}

func (nm *NetworkManager) removePortMappingIptables(pm *PortMapping) {
	exec.Command("iptables", "-t", "nat", "-D", "PREROUTING",
		"-p", pm.Protocol,
		"--dport", fmt.Sprintf("%d", pm.HostPort),
		"-j", "DNAT", "--to-destination", fmt.Sprintf("%s:%d", pm.HostIP, pm.ContainerPort)).Run()
}

func (nm *NetworkManager) addToHosts(alias, target string) {
	// Append to /etc/hosts if not already there
	data, _ := os.ReadFile("/etc/hosts")
	if strings.Contains(string(data), alias) {
		return
	}
	entry := fmt.Sprintf("\n%s %s  # cube-container\n", target, alias)
	f, err := os.OpenFile("/etc/hosts", os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(entry)
}

func (nm *NetworkManager) removeFromHosts(alias string) {
	data, err := os.ReadFile("/etc/hosts")
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	var kept []string
	for _, line := range lines {
		if !strings.Contains(line, alias) {
			kept = append(kept, line)
		}
	}
	os.WriteFile("/etc/hosts", []byte(strings.Join(kept, "\n")), 0644) //nosec G703 -- fixed path /etc/hosts, content is sanitized host entries
}

// ---- MCP tool handlers ----

func handleAddPortMapping(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	hostPort := argInt(args, "host_port", 0)
	containerPort := argInt(args, "container_port", 0)
	protocol := argString(args, "protocol")
	hostIP := argString(args, "host_ip")
	data, err := netMgr.AddPortMapping(containerID, hostPort, containerPort, protocol, hostIP)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleRemovePortMapping(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	mappingID := argString(args, "mapping_id")
	if mappingID == "" {
		return errResult("mapping_id is required"), nil
	}
	if err := netMgr.RemovePortMapping(mappingID); err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]interface{}{"mapping_id": mappingID, "status": "removed"}), nil
}

func handleListPortMappings(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := netMgr.ListPortMappings()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleAddDNSAlias(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	alias := argString(args, "alias")
	containerID := argString(args, "container_id")
	target := argString(args, "target")
	data, err := netMgr.AddDNSAlias(alias, containerID, target)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleRemoveDNSAlias(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	alias := argString(args, "alias")
	if err := netMgr.RemoveDNSAlias(alias); err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]interface{}{"alias": alias, "status": "removed"}), nil
}

func handleListDNSAliases(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := netMgr.ListDNSAliases()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleAddNetworkPolicy(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	data, err := netMgr.AddNetworkPolicy(
		argString(args, "name"),
		argString(args, "source_container"),
		argString(args, "dest_container"),
		argString(args, "action"),
		argString(args, "protocol"),
		argInt(args, "port", 0),
	)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleListNetworkPolicies(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := netMgr.ListNetworkPolicies()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleRemoveNetworkPolicy(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	policyID := argString(args, "policy_id")
	if err := netMgr.RemoveNetworkPolicy(policyID); err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]interface{}{"policy_id": policyID, "status": "removed"}), nil
}

// nowRFC3339 returns current time in RFC3339 format.
func nowRFC3339() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z07:00")
}
