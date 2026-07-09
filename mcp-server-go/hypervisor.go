// Package main: hypervisor layer — VM lifecycle management via libvirt.
//
// This file implements MCP-native hypervisor control for VMs. Unlike containers
// (which share the host kernel), VMs run their own guest kernel via KVM/QEMU,
// providing full hardware-level isolation.
//
// Design decisions:
//   - virsh subprocess, not libvirt-go RPC: simpler, auditable, no CGO,
//     consistent with existing patterns in networking.go (iptables) and gc.go (docker).
//   - Domain XML generated via Go text/template (no external XML library).
//   - All mutating operations require admin role; read-only requires viewer.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"text/template"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Types ----

type VMConfig struct {
	Name      string `json:"name"`
	VCPU      int    `json:"vcpu"`
	MemoryMB  int    `json:"memory_mb"`
	DiskGB    int    `json:"disk_gb"`
	DiskPath  string `json:"disk_path,omitempty"`
	ISOPath   string `json:"iso_path,omitempty"`
	OSVariant string `json:"os_variant,omitempty"`
	Network   string `json:"network,omitempty"`
}

type GPUAssignment struct {
	PCIAddress string `json:"pci_address"`
	Type       string `json:"type"`
}

type VMInfo struct {
	Name     string          `json:"name"`
	State    string          `json:"state"`
	MemoryKB int             `json:"memory_kb"`
	VCPU     int             `json:"vcpu"`
	CPUTime  string          `json:"cpu_time,omitempty"`
	Autostart bool           `json:"autostart"`
	ID       int             `json:"id"`
	GPUs     []GPUAssignment `json:"gpus,omitempty"`
	OS       string          `json:"os,omitempty"`
}

type VMSnapshot struct {
	Name string `json:"name"`
}

// ---- virsh execution helpers ----

func runVirsh(args ...string) (string, error) {
	return runVirshCtx(context.Background(), args...)
}

func runVirshCtx(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "virsh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("virsh %s: %w (output: %s)",
			strings.Join(args, " "), err, truncate(string(out), 500))
	}
	return strings.TrimSpace(string(out)), nil
}

func virshAvailable() bool {
	_, err := exec.LookPath("virsh")
	if err != nil {
		return false
	}
	_, err = runVirsh("uri")
	return err == nil
}

// truncate is already defined in deploy.go — reusing it.

// ---- VM info parsing ----

func getVMInfo(name string) (VMInfo, error) {
	out, err := runVirsh("dominfo", name)
	if err != nil {
		return VMInfo{}, err
	}
	info := VMInfo{Name: name}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch strings.ToLower(key) {
		case "state":
			info.State = val
		case "memory":
			fmt.Sscanf(val, "%d", &info.MemoryKB)
		case "cpu(s)":
			fmt.Sscanf(val, "%d", &info.VCPU)
		case "cpu time":
			info.CPUTime = val
		case "autostart":
			info.Autostart = val == "enable"
		case "id":
			fmt.Sscanf(val, "%d", &info.ID)
		case "os type":
			info.OS = val
		}
	}
	return info, nil
}

// ---- VM lifecycle handlers ----

func handleVMList(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !virshAvailable() {
		return errResult("libvirt (virsh) is not available on this host. Install libvirt-daemon-system and start libvirtd."), nil
	}

	stateFilter := argString(req.GetArguments(), "state")

	out, err := runVirsh("list", "--all", "--name")
	if err != nil {
		return unwrapError(err), nil
	}

	names := splitNonEmpty(out)
	vms := make([]VMInfo, 0, len(names))
	for _, name := range names {
		info, err := getVMInfo(name)
		if err != nil {
			continue
		}
		if stateFilter != "" && !strings.EqualFold(info.State, stateFilter) {
			continue
		}
		vms = append(vms, info)
	}
	return okResult(map[string]interface{}{
		"vms":   vms,
		"total": len(vms),
	}), nil
}

func handleVMGet(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !virshAvailable() {
		return errResult("libvirt (virsh) is not available on this host."), nil
	}

	name := argString(req.GetArguments(), "name")
	if name == "" {
		return errResult("name is required"), nil
	}

	info, err := getVMInfo(name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(info), nil
}

func handleVMCreate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !virshAvailable() {
		return errResult("libvirt (virsh) is not available on this host. Install libvirt-daemon-system and start libvirtd."), nil
	}

	args := req.GetArguments()
	cfg := VMConfig{
		Name:      argString(args, "name"),
		VCPU:      argInt(args, "vcpu", 2),
		MemoryMB:  argInt(args, "memory_mb", 2048),
		DiskGB:    argInt(args, "disk_gb", 20),
		DiskPath:  argString(args, "disk_path"),
		ISOPath:   argString(args, "iso_path"),
		OSVariant: argString(args, "os_variant"),
		Network:   argString(args, "network"),
	}
	if cfg.Name == "" {
		return errResult("name is required"), nil
	}
	if cfg.Network == "" {
		cfg.Network = "default"
	}

	// Generate domain XML
	domXML, err := generateDomainXML(&cfg)
	if err != nil {
		return errResult(fmt.Sprintf("failed to generate domain XML: %v", err)), nil
	}

	// Write XML to temp file, define from it
	tmpPath := "/tmp/cube-vm-" + cfg.Name + ".xml"
	if err := os.WriteFile(tmpPath, []byte(domXML), 0600); err != nil {
		return errResult(fmt.Sprintf("failed to write XML: %v", err)), nil
	}

	// Define (persistent) + start
	_, err = runVirsh("define", tmpPath)
	if err != nil {
		return unwrapError(err), nil
	}

	_, err = runVirsh("start", cfg.Name)
	if err != nil {
		info, _ := getVMInfo(cfg.Name)
		return okResult(map[string]interface{}{
			"message": fmt.Sprintf("VM '%s' defined but start failed: %v", cfg.Name, err),
			"vm":      info,
			"warning": "VM is defined but not running. Check libvirtd logs.",
		}), nil
	}

	info, _ := getVMInfo(cfg.Name)
	return okResult(map[string]interface{}{
		"message": fmt.Sprintf("VM '%s' created and started", cfg.Name),
		"vm":      info,
	}), nil
}

func handleVMStart(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req.GetArguments(), "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	_, err := runVirsh("start", name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{"status": "started", "name": name}), nil
}

func handleVMStop(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req.GetArguments(), "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	force := argString(req.GetArguments(), "force")
	if force == "true" {
		_, err := runVirsh("destroy", name)
		if err != nil {
			return unwrapError(err), nil
		}
	} else {
		_, err := runVirsh("shutdown", name)
		if err != nil {
			return unwrapError(err), nil
		}
	}
	return okResult(map[string]string{"status": "stopped", "name": name}), nil
}

func handleVMPause(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req.GetArguments(), "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	_, err := runVirsh("suspend", name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{"status": "paused", "name": name}), nil
}

func handleVMResume(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req.GetArguments(), "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	_, err := runVirsh("resume", name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{"status": "running", "name": name}), nil
}

func handleVMDelete(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req.GetArguments(), "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	removeDisk := argString(req.GetArguments(), "remove_disk")

	// Stop first if running (best effort)
	runVirsh("destroy", name)

	// Undefine — try with --nvram first, fallback for older libvirt
	_, err := runVirsh("undefine", name, "--nvram")
	if err != nil {
		_, err = runVirsh("undefine", name)
		if err != nil {
			return unwrapError(err), nil
		}
	}

	if removeDisk == "true" {
		runVirsh("vol-delete", "--pool", "default", name+".qcow2")
	}

	return okResult(map[string]string{"status": "deleted", "name": name}), nil
}

// ---- VM snapshots ----

func handleVMSnapshot(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req.GetArguments(), "name")
	snapName := argString(req.GetArguments(), "snapshot_name")
	if name == "" || snapName == "" {
		return errResult("name and snapshot_name are required"), nil
	}
	_, err := runVirsh("snapshot-create-as", name, snapName)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{
		"status":        "snapshot created",
		"vm":            name,
		"snapshot_name": snapName,
	}), nil
}

func handleVMSnapshotList(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req.GetArguments(), "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	out, err := runVirsh("snapshot-list", "--domain", name, "--name")
	if err != nil {
		return unwrapError(err), nil
	}
	names := splitNonEmpty(out)
	snaps := make([]VMSnapshot, 0, len(names))
	for _, sn := range names {
		snaps = append(snaps, VMSnapshot{Name: sn})
	}
	return okResult(map[string]interface{}{
		"vm":        name,
		"snapshots": snaps,
		"total":     len(snaps),
	}), nil
}

func handleVMSnapshotRestore(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req.GetArguments(), "name")
	snapName := argString(req.GetArguments(), "snapshot_name")
	if name == "" || snapName == "" {
		return errResult("name and snapshot_name are required"), nil
	}
	_, err := runVirsh("snapshot-revert", "--domain", name, snapName)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{
		"status":        "reverted",
		"vm":            name,
		"snapshot_name": snapName,
	}), nil
}

func handleVMSnapshotDelete(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req.GetArguments(), "name")
	snapName := argString(req.GetArguments(), "snapshot_name")
	if name == "" || snapName == "" {
		return errResult("name and snapshot_name are required"), nil
	}
	_, err := runVirsh("snapshot-delete", "--domain", name, snapName)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{
		"status":        "snapshot deleted",
		"vm":            name,
		"snapshot_name": snapName,
	}), nil
}

// ---- VM migration ----

func handleVMMigrate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := argString(req.GetArguments(), "name")
	destHost := argString(req.GetArguments(), "dest_host")
	if name == "" || destHost == "" {
		return errResult("name and dest_host are required"), nil
	}
	live := argString(req.GetArguments(), "live")
	migrateURI := fmt.Sprintf("qemu+ssh://%s/system", destHost)

	args := []string{"migrate", "--undefinesource", "--persistent"}
	if live == "true" {
		args = append(args, "--live")
	} else {
		args = append(args, "--offline")
	}
	args = append(args, name, migrateURI)

	_, err := runVirsh(args...)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]string{
		"status":    "migrated",
		"vm":        name,
		"dest_host": destHost,
	}), nil
}

// ---- VM network info ----

func getVMIPs(name string) []string {
	out, err := runVirsh("domifaddr", name, "--source", "agent", "--full", "--format", "json")
	if err != nil {
		return nil
	}
	var result struct {
		Ifaddrs []struct {
			Addr string `json:"addr"`
		} `json:"ifaddrs"`
	}
	if json.Unmarshal([]byte(out), &result) != nil {
		return nil
	}
	ips := make([]string, 0, len(result.Ifaddrs))
	for _, a := range result.Ifaddrs {
		if idx := strings.Index(a.Addr, "/"); idx > 0 {
			ips = append(ips, a.Addr[:idx])
		} else {
			ips = append(ips, a.Addr)
		}
	}
	return ips
}

// ---- Domain XML generation ----

const domainXMLTemplate = `<domain type='kvm'>
  <name>{{.Name}}</name>
  <memory unit='KiB'>{{.MemKiB}}</memory>
  <currentMemory unit='KiB'>{{.MemKiB}}</currentMemory>
  <vcpu>{{.VCPU}}</vcpu>
  <os>
    <type arch='x86_64' machine='q35'>hvm</type>
    {{if .ISOPath}}<boot dev='cdrom'/>{{end}}
    <boot dev='hd'/>
  </os>
  <features>
    <acpi/><apic/>
  </features>
  <cpu mode='host-passthrough'/>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>destroy</on_crash>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='{{.DiskPath}}'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    {{if .ISOPath}}
    <disk type='file' device='cdrom'>
      <driver name='qemu' type='raw'/>
      <source file='{{.ISOPath}}'/>
      <target dev='sda' bus='sata'/>
      <readonly/>
    </disk>
    {{end}}
    <interface type='network'>
      <source network='{{.Network}}'/>
      <model type='virtio'/>
    </interface>
    <serial type='pty'>
      <target port='0'/>
    </serial>
    <console type='pty'>
      <target type='serial' port='0'/>
    </console>
    <graphics type='vnc' port='-1' autoport='yes' listen='0.0.0.0'/>
    <video>
      <model type='cirrus' vram='16384' heads='1'/>
    </video>
  </devices>
</domain>`

func generateDomainXML(cfg *VMConfig) (string, error) {
	diskPath := cfg.DiskPath
	if diskPath == "" {
		diskPath = "/var/lib/libvirt/images/" + cfg.Name + ".qcow2"
		createQcow2Disk(diskPath, cfg.DiskGB)
	}

	data := struct {
		Name     string
		MemKiB   int
		VCPU     int
		DiskPath string
		ISOPath  string
		Network  string
	}{
		Name:     cfg.Name,
		MemKiB:   cfg.MemoryMB * 1024,
		VCPU:     cfg.VCPU,
		DiskPath: diskPath,
		ISOPath:  cfg.ISOPath,
		Network:  cfg.Network,
	}

	tmpl, err := template.New("domain").Parse(domainXMLTemplate)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func createQcow2Disk(path string, sizeGB int) error {
	return exec.Command("qemu-img", "create", "-f", "qcow2", path,
		fmt.Sprintf("%dG", sizeGB)).Run()
}

// ---- Utility ----

// splitNonEmpty is defined in logstream.go — splits by \n, trims whitespace, drops empties.

// Keep time import referenced (used in future heartbeat handlers).
var _ = time.Now
