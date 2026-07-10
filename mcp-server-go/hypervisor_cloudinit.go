// Package main: hypervisor layer — cloud-init and VM template management.
//
// Cloud-init enables headless VM provisioning: create a VM from a cloud image
// (e.g. Ubuntu cloud img, Alpine cloud) and inject SSH keys, hostname, user-data
// via a cloud-init NoCloud ISO. The VM boots and configures itself automatically.
//
// Workflow:
//   1. Download a cloud image (qcow2) — or provide your own
//   2. Generate a cloud-init ISO with ssh keys + user-data
//   3. Create a VM that boots from the cloud image with the ci ISO attached
//
// Tools provided:
//   - vm_cloudinit_create: generate a cloud-init NoCloud ISO from parameters
//   - vm_template_list: list available cloud images in the default pool
//   - vm_create_from_template: create a VM from a cloud image + cloud-init
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Types ----

type CloudInitSpec struct {
	Hostname  string   `json:"hostname"`
	SSHKeys   []string `json:"ssh_keys"`
	Username  string   `json:"username,omitempty"`
	Password  string   `json:"password,omitempty"`
	UserData  string   `json:"user_data,omitempty"` // raw cloud-init user-data
	Packages  []string `json:"packages,omitempty"`  // apt packages to install on first boot
}

type CloudInitResult struct {
	ISOPath    string `json:"iso_path"`
	SeedDir    string `json:"seed_dir"`
	Hostname   string `json:"hostname"`
	Username   string `json:"username,omitempty"`
	KeyCount   int    `json:"ssh_key_count"`
}

type VMTemplate struct {
	Name       string `json:"name"`
	Path       string `json:"path"`
	SizeMB     int64  `json:"size_mb"`
	ModifiedAt string `json:"modified_at"`
}

type VMFromTemplateResult struct {
	VMName    string `json:"vm_name"`
	Template  string `json:"template"`
	State     string `json:"state"`
	Message   string `json:"message"`
}

// ---- Constants ----

const (
	defaultImageDir = "/var/lib/libvirt/images"
	defaultSeedDir  = "/var/lib/libvirt/seeds"
)

// ---- Handlers ----

func handleVMCloudInitCreate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	spec := CloudInitSpec{
		Hostname: argString(args, "hostname"),
		Username: argString(args, "username"),
		Password: argString(args, "password"),
		UserData: argString(args, "user_data"),
		SSHKeys:  argStringSlice(args, "ssh_keys"),
		Packages: argStringSlice(args, "packages"),
	}

	if spec.Hostname == "" {
		return errResult("hostname is required"), nil
	}
	if err := validateHostname(spec.Hostname); err != nil {
		return errResult(err.Error()), nil
	}
	if spec.Username == "" {
		spec.Username = "ubuntu"
	}
	if err := validateCloudInitUsername(spec.Username); err != nil {
		return errResult(fmt.Sprintf("invalid username: %v", err)), nil
	}
	if len(spec.SSHKeys) == 0 && spec.Password == "" {
		return errResult("either ssh_keys or password is required for initial access"), nil
	}

	// Create seed directory
	seedDir := filepath.Join(defaultSeedDir, spec.Hostname)
	if err := os.MkdirAll(seedDir, 0700); err != nil { // R9-HYP-10: restrict perms, seed ISO may contain passwords
		return errResult(fmt.Sprintf("failed to create seed dir: %v", err)), nil
	}

	// Generate user-data
	userData := generateUserData(&spec)
	userDataPath := filepath.Join(seedDir, "user-data")
	if err := os.WriteFile(userDataPath, []byte(userData), 0600); err != nil {
		return errResult(fmt.Sprintf("failed to write user-data: %v", err)), nil
	}

	// Generate meta-data
	metaData := generateMetaData(&spec)
	metaDataPath := filepath.Join(seedDir, "meta-data")
	if err := os.WriteFile(metaDataPath, []byte(metaData), 0600); err != nil {
		return errResult(fmt.Sprintf("failed to write meta-data: %v", err)), nil
	}

	// Generate ISO using cloud-localds (or genisoimage as fallback)
	isoPath := filepath.Join(seedDir, spec.Hostname+"-seed.iso")
	if err := createCloudInitISO(seedDir, isoPath); err != nil {
		return errResult(fmt.Sprintf("failed to create cloud-init ISO: %v", err)), nil
	}

	return okResult(CloudInitResult{
		ISOPath:  isoPath,
		SeedDir:  seedDir,
		Hostname: spec.Hostname,
		Username: spec.Username,
		KeyCount: len(spec.SSHKeys),
	}), nil
}

func handleVMTemplateList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dir := defaultImageDir
	templates := []VMTemplate{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return errResult(fmt.Sprintf("failed to read image dir %s: %v", dir, err)), nil
	}

	for _, entry := range entries {
		name := entry.Name()
		// Only show qcow2 and img files
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".qcow2" && ext != ".img" && ext != ".iso" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		templates = append(templates, VMTemplate{
			Name:       name,
			Path:       filepath.Join(dir, name),
			SizeMB:     info.Size() / (1024 * 1024),
			ModifiedAt: info.ModTime().Format(time.RFC3339),
		})
	}

	return okResult(map[string]interface{}{
		"templates":   templates,
		"total":       len(templates),
		"image_dir":   dir,
	}), nil
}

func handleVMCreateFromTemplate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !virshAvailable() {
		return errResult("libvirt (virsh) is not available on this host."), nil
	}

	args := req.GetArguments()
	vmName := argString(args, "name")
	templatePath := argString(args, "template_path")
	seedISO := argString(args, "seed_iso")
	vcpu := argInt(args, "vcpu", 2)
	memoryMB := argInt(args, "memory_mb", 2048)
	diskGB := argInt(args, "disk_gb", 20)
	network := argString(args, "network")
	if network == "" {
		network = "default"
	}

	if vmName == "" || templatePath == "" {
		return errResult("name and template_path are required"), nil
	}
	if err := validateVMName(vmName); err != nil {
		return errResult(err.Error()), nil
	}
	// Validate template path is within allowed directories
	if err := validateFilePath(templatePath, defaultImageDir); err != nil {
		return errResult(fmt.Sprintf("invalid template_path: %v", err)), nil
	}
	// R8-M04: validate seed_iso path
	if err := validateFilePathOrEmpty(seedISO, defaultImageDir, defaultSeedDir); err != nil {
		return errResult(fmt.Sprintf("invalid seed_iso: %v", err)), nil
	}
	// R8-H03: validate network name
	if err := validateNetworkName(network); err != nil {
		return errResult(fmt.Sprintf("invalid network: %v", err)), nil
	}

	// Verify template exists
	if _, err := os.Stat(templatePath); err != nil {
		return errResult(fmt.Sprintf("template not found: %s", templatePath)), nil
	}

	// Create overlay disk from template (copy-on-write)
	diskPath := filepath.Join(defaultImageDir, vmName+".qcow2")
	cmd := exec.Command("qemu-img", "create", "-f", "qcow2",
		"-b", templatePath, "-F", "qcow2",
		diskPath, fmt.Sprintf("%dG", diskGB))
	if out, err := cmd.CombinedOutput(); err != nil {
		return errResult(fmt.Sprintf("failed to create overlay disk: %v (%s)", err, string(out))), nil
	}

	// Generate domain XML with optional cloud-init ISO
	isoPath := seedISO
	cfg := &VMConfig{
		Name:      vmName,
		VCPU:      vcpu,
		MemoryMB:  memoryMB,
		DiskGB:    diskGB,
		DiskPath:  diskPath,
		ISOPath:   isoPath,
		Network:   network,
	}

	domXML, err := generateDomainXML(cfg)
	if err != nil {
		return errResult(fmt.Sprintf("failed to generate domain XML: %v", err)), nil
	}

	tmpPath := "/tmp/cube-vm-" + vmName + ".xml"
	if err := os.WriteFile(tmpPath, []byte(domXML), 0600); err != nil {
		return errResult(fmt.Sprintf("failed to write XML: %v", err)), nil
	}

	// Define + start
	if _, err := runVirsh("define", tmpPath); err != nil {
		return unwrapError(err), nil
	}

	if _, err := runVirsh("start", vmName); err != nil {
		info, _ := getVMInfo(vmName)
		return okResult(map[string]interface{}{
			"message": fmt.Sprintf("VM '%s' defined but start failed: %v", vmName, err),
			"vm":      info,
			"warning": "VM is defined but not running.",
		}), nil
	}

	info, _ := getVMInfo(vmName)
	return okResult(VMFromTemplateResult{
		VMName:   vmName,
		Template: templatePath,
		State:    info.State,
		Message:  fmt.Sprintf("VM '%s' created from template and started", vmName),
	}), nil
}

// ---- Cloud-init generators ----

func generateUserData(spec *CloudInitSpec) string {
	var buf strings.Builder

	buf.WriteString("#cloud-config\n")

	// Hostname
	buf.WriteString(fmt.Sprintf("hostname: %s\n", spec.Hostname))
	buf.WriteString(fmt.Sprintf("fqdn: %s.localdomain\n", spec.Hostname))
	buf.WriteString("manage_etc_hosts: true\n")

	// User
	buf.WriteString(fmt.Sprintf("users:\n"))
	buf.WriteString(fmt.Sprintf("  - name: %s\n", spec.Username))
	buf.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
	buf.WriteString("    shell: /bin/bash\n")
	if len(spec.SSHKeys) > 0 {
		buf.WriteString("    ssh_authorized_keys:\n")
		for _, key := range spec.SSHKeys {
			if err := validateSSHKey(key); err != nil {
				// Skip invalid keys rather than failing entire gen — caller validates
				continue
			}
			buf.WriteString(fmt.Sprintf("      - %s\n", strings.TrimSpace(key)))
		}
	}
	if spec.Password != "" {
		buf.WriteString(fmt.Sprintf("    lock_passwd: false\n"))
		buf.WriteString(fmt.Sprintf("    plain_text_passwd: %q\n", spec.Password))
		buf.WriteString("    chpasswd: { expire: false }\n")
	}

	// SSH server config
	buf.WriteString("ssh_pwauth: true\n")

	// Packages
	if len(spec.Packages) > 0 {
		buf.WriteString("packages:\n")
		for _, pkg := range spec.Packages {
			if err := validatePackageName(pkg); err != nil {
				continue // skip invalid package names
			}
			buf.WriteString(fmt.Sprintf("  - %s\n", pkg))
		}
	}

	// Raw user-data override (appended after structured config)
	if spec.UserData != "" {
		buf.WriteString("\n# Custom user-data\n")
		buf.WriteString(spec.UserData)
		buf.WriteString("\n")
	}

	return buf.String()
}

func generateMetaData(spec *CloudInitSpec) string {
	return fmt.Sprintf("instance-id: %s\nlocal-hostname: %s\n",
		spec.Hostname, spec.Hostname)
}

func createCloudInitISO(seedDir, isoPath string) error {
	// Try cloud-localds first (simplest)
	if _, err := exec.LookPath("cloud-localds"); err == nil {
		cmd := exec.Command("cloud-localds", isoPath,
			filepath.Join(seedDir, "user-data"),
			filepath.Join(seedDir, "meta-data"))
		return cmd.Run()
	}

	// Fallback: genisoimage
	if _, err := exec.LookPath("genisoimage"); err == nil {
		cmd := exec.Command("genisoimage", "-output", isoPath,
			"-volid", "cidata",
			"-joliet", "-rock",
			filepath.Join(seedDir, "user-data"),
			filepath.Join(seedDir, "meta-data"))
		return cmd.Run()
	}

	// Fallback: xorriso
	if _, err := exec.LookPath("xorriso"); err == nil {
		cmd := exec.Command("xorriso", "-as", "genisoimage", "-output", isoPath,
			"-volid", "cidata",
			"-joliet", "-rock",
			filepath.Join(seedDir, "user-data"),
			filepath.Join(seedDir, "meta-data"))
		return cmd.Run()
	}

	return fmt.Errorf("no ISO creation tool found (install cloud-image-utils or genisoimage)")
}
