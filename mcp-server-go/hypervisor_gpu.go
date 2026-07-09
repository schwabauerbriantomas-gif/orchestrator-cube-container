// Package main: hypervisor layer — GPU detection, monitoring, and passthrough.
//
// These tools expose GPU operations through the MCP interface. Three GPU
// ecosystems are supported:
//
// 1. NVIDIA discrete GPUs (nvidia-smi) — primary use case for ML workloads.
// 2. AMD GPUs (rocm-smi) — secondary.
// 3. Intel iGPUs (Intel media driver + /dev/dri) — for transcoding and light compute.
//
// GPU passthrough (VFIO) allows assigning a physical GPU to a VM. The LLM agent
// can detect available GPUs, assign them to VMs, and monitor utilization —
// all through MCP calls.
//
// RBAC: admin for assign/release, operator for exec (already covered by existing
// container tools), viewer for detect/list/stats.
package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Types ----

type GPUInfo struct {
	Index       int     `json:"index"`
	Name        string  `json:"name"`
	Vendor      string  `json:"vendor"` // nvidia, amd, intel
	PCIAddress  string  `json:"pci_address"`
	MemoryMB    int     `json:"memory_mb"`
	Driver      string  `json:"driver,omitempty"`
	CUDASupport bool    `json:"cuda_support,omitempty"`
	// Runtime stats (populated when available)
	GPUUtil   *float64 `json:"gpu_util,omitempty"`
	MemUtil   *float64 `json:"mem_util,omitempty"`
	TempC     *int     `json:"temp_c,omitempty"`
	PowerW    *float64 `json:"power_w,omitempty"`
	// VFIO passthrough state
	BoundToVFIO  bool   `json:"bound_to_vfio,omitempty"`
	AssignedToVM string `json:"assigned_to_vm,omitempty"`
}

type GPUAssignmentResult struct {
	PCIAddress string `json:"pci_address"`
	VMName     string `json:"vm_name"`
	Status     string `json:"status"`
}

// ---- GPU detection ----

// rePCI extracts PCI bus address from lspci output.
var rePCI = regexp.MustCompile(`^([0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}\.[0-9A-Fa-f])`)

func handleGPUList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	gpus := []GPUInfo{}

	// Try NVIDIA first
	if nvidiaGPUs, err := detectNVIDIAGPUs(); err == nil {
		gpus = append(gpus, nvidiaGPUs...)
	}

	// Try AMD
	if amdGPUs, err := detectAMDGPUs(); err == nil {
		gpus = append(gpus, amdGPUs...)
	}

	// Try Intel iGPU
	if intelGPUs, err := detectIntelGPUs(); err == nil {
		gpus = append(gpus, intelGPUs...)
	}

	if len(gpus) == 0 {
		return okResult(map[string]interface{}{
			"gpus":     []GPUInfo{},
			"total":    0,
			"message":  "No GPUs detected. For NVIDIA install nvidia-smi; for AMD install rocm-smi; Intel iGPUs are detected via lspci.",
		}), nil
	}

	return okResult(map[string]interface{}{
		"gpus":  gpus,
		"total": len(gpus),
	}), nil
}

func detectNVIDIAGPUs() ([]GPUInfo, error) {
	_, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi not found")
	}

	// Query: index, name, memory.total, pci.bus_id, driver_version, utilization.gpu, utilization.memory, temperature.gpu, power.draw
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=index,name,memory.total,pci.bus_id,driver_version,utilization.gpu,utilization.memory,temperature.gpu,power.draw",
		"--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		return nil, err
	}

	var gpus []GPUInfo
	for _, line := range splitNonEmpty(string(out)) {
		fields := strings.Split(line, ",")
		if len(fields) < 9 {
			continue
		}
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}

		memMB := parseGPUMem(fields[2])
		gpuUtil := parseOptionalFloat(fields[5])
		memUtil := parseOptionalFloat(fields[6])
		temp := parseOptionalInt(fields[7])
		power := parseOptionalFloat(fields[8])

		gpu := GPUInfo{
			Index:      parseIntSafe(fields[0]),
			Name:       fields[1],
			Vendor:     "nvidia",
			PCIAddress: fields[3],
			MemoryMB:   memMB,
			Driver:     fields[4],
			CUDASupport: true,
			GPUUtil:    gpuUtil,
			MemUtil:    memUtil,
			TempC:      temp,
			PowerW:     power,
		}

		// Check if bound to VFIO
		gpu.BoundToVFIO = checkVFIOBound(gpu.PCIAddress)

		gpus = append(gpus, gpu)
	}
	return gpus, nil
}

func detectAMDGPUs() ([]GPUInfo, error) {
	_, err := exec.LookPath("rocm-smi")
	if err != nil {
		return nil, fmt.Errorf("rocm-smi not found")
	}

	out, err := exec.Command("rocm-smi", "--showproductname", "--showmeminfo", "vram", "--showuse", "gpu", "--showtemp", "--showpower", "draw", "--json").Output()
	if err != nil {
		return nil, err
	}

	// rocm-smi JSON output is keyed by GPU index (e.g. "card0")
	// This is a simplified parser — real rocm-smi JSON structure varies by version
	var gpus []GPUInfo
	lines := splitNonEmpty(string(out))
	for _, line := range lines {
		if strings.Contains(line, "\"Card series\"") {
			// Extract card name
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				name := strings.Trim(strings.Trim(parts[1], "\""), " ,")
				gpu := GPUInfo{
					Name:    name,
					Vendor:  "amd",
				}
				gpus = append(gpus, gpu)
			}
		}
	}
	return gpus, nil
}

func detectIntelGPUs() ([]GPUInfo, error) {
	_, err := exec.LookPath("lspci")
	if err != nil {
		return nil, fmt.Errorf("lspci not found")
	}

	out, err := exec.Command("lspci", "-nn").Output()
	if err != nil {
		return nil, err
	}

	var gpus []GPUInfo
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(strings.ToLower(line), "vga compatible controller") &&
			!strings.Contains(strings.ToLower(line), "display controller") {
			continue
		}
		if !strings.Contains(strings.ToLower(line), "intel") {
			continue
		}

		match := rePCI.FindString(line)
		if match == "" {
			continue
		}

		// Extract name from the line
		name := strings.TrimSpace(line[len(match):])
		if idx := strings.Index(name, "Intel"); idx >= 0 {
			name = strings.TrimSpace(name[idx:])
		}

		gpu := GPUInfo{
			Name:       name,
			Vendor:     "intel",
			PCIAddress: normalizePCIAddress(match),
		}

		// Check for /dev/dri/renderD* (iGPU render node)
		renderNodes, _ := filepath.Glob("/dev/dri/renderD*")
		if len(renderNodes) > 0 {
			gpu.Driver = "i915"
		}

		gpu.BoundToVFIO = checkVFIOBound(gpu.PCIAddress)
		gpus = append(gpus, gpu)
	}
	return gpus, nil
}

// ---- GPU stats ----

func handleGPUStats(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pciAddr := argString(req.GetArguments(), "pci_address")

	if _, err := exec.LookPath("nvidia-smi"); err == nil {
		args := []string{
			"--query-gpu=index,name,pci.bus_id,utilization.gpu,utilization.memory,memory.used,memory.total,temperature.gpu,power.draw,power.limit,clocks.current.graphics,clocks.current.memory",
			"--format=csv,noheader,nounits",
		}
		out, err := exec.Command("nvidia-smi", args...).Output()
		if err != nil {
			return unwrapError(err), nil
		}

		stats := []map[string]interface{}{}
		for _, line := range splitNonEmpty(string(out)) {
			fields := strings.Split(line, ",")
			if len(fields) < 12 {
				continue
			}
			for i := range fields {
				fields[i] = strings.TrimSpace(fields[i])
			}
			busID := fields[2]
			if pciAddr != "" && !strings.EqualFold(busID, pciAddr) {
				continue
			}
			stats = append(stats, map[string]interface{}{
				"index":           parseIntSafe(fields[0]),
				"name":            fields[1],
				"pci_address":     busID,
				"gpu_util_pct":    parseOptionalFloat(fields[3]),
				"mem_util_pct":    parseOptionalFloat(fields[4]),
				"mem_used_mb":     parseIntSafe(fields[5]),
				"mem_total_mb":    parseIntSafe(fields[6]),
				"temp_c":          parseOptionalInt(fields[7]),
				"power_draw_w":    parseOptionalFloat(fields[8]),
				"power_limit_w":   parseOptionalFloat(fields[9]),
				"clock_graphics":  parseOptionalInt(fields[10]),
				"clock_memory":    parseOptionalInt(fields[11]),
			})
		}
		return okResult(map[string]interface{}{
			"gpus":  stats,
			"total": len(stats),
		}), nil
	}

	return errResult("No GPU monitoring tools available (nvidia-smi not found)."), nil
}

// ---- GPU passthrough (VFIO) ----

func handleGPUAssign(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pciAddr := argString(req.GetArguments(), "pci_address")
	vmName := argString(req.GetArguments(), "vm_name")
	if pciAddr == "" || vmName == "" {
		return errResult("pci_address and vm_name are required"), nil
	}

	// Detach from host driver and bind to vfio-pci
	normalized := normalizePCIAddress(pciAddr)

	// 1. Unbind from current driver
	unbindPath := "/sys/bus/pci/devices/" + normalized + "/driver/unbind"
	if err := writeFileAsRoot(unbindPath, normalized); err != nil {
		// Might already be unbound — continue
	}

	// 2. Get vendor:device ID
	vendorID, err := readFileAsString("/sys/bus/pci/devices/" + normalized + "/vendor")
	deviceID, err2 := readFileAsString("/sys/bus/pci/devices/" + normalized + "/device")
	if err != nil || err2 != nil {
		return errResult("failed to read GPU vendor/device IDs from sysfs"), nil
	}
	vendorDevice := strings.TrimSpace(vendorID) + " " + strings.TrimSpace(deviceID)

	// 3. Bind to vfio-pci
	vfioNewIDPath := "/sys/bus/pci/drivers/vfio-pci/new_id"
	if err := writeFileAsRoot(vfioNewIDPath, vendorDevice); err != nil {
		return errResult(fmt.Sprintf("failed to bind GPU %s to vfio-pci: %v. Ensure VFIO is enabled in kernel (intel_iommu=on / amd_iommu=on)", normalized, err)), nil
	}

	// 4. Attach to VM via virsh attach-device
	// Generate device XML and attach
	bus, slot, fn := extractPCIComponents(normalized)
	deviceXML := fmt.Sprintf(`<hostdev mode='subsystem' type='pci' managed='yes'>
  <source>
    <address domain='0x0000' bus='%s' slot='%s' function='%s'/>
  </source>
</hostdev>`, bus, slot, fn)

	tmpPath := "/tmp/cube-gpu-" + vmName + ".xml"
	if err := writeFilePublic(tmpPath, deviceXML); err != nil {
		return errResult(fmt.Sprintf("failed to write device XML: %v", err)), nil
	}

	_, err = runVirsh("attach-device", vmName, tmpPath, "--persistent")
	if err != nil {
		return unwrapError(err), nil
	}

	return okResult(GPUAssignmentResult{
		PCIAddress: normalized,
		VMName:     vmName,
		Status:     "assigned",
	}), nil
}

func handleGPURelease(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	pciAddr := argString(req.GetArguments(), "pci_address")
	vmName := argString(req.GetArguments(), "vm_name")
	if pciAddr == "" || vmName == "" {
		return errResult("pci_address and vm_name are required"), nil
	}

	normalized := normalizePCIAddress(pciAddr)

	// Detach from VM
	bus, slot, fn := extractPCIComponents(normalized)
	deviceXML := fmt.Sprintf(`<hostdev mode='subsystem' type='pci' managed='yes'>
  <source>
    <address domain='0x0000' bus='%s' slot='%s' function='%s'/>
  </source>
</hostdev>`, bus, slot, fn)

	tmpPath := "/tmp/cube-gpu-release-" + vmName + ".xml"
	if err := writeFilePublic(tmpPath, deviceXML); err != nil {
		return errResult(fmt.Sprintf("failed to write device XML: %v", err)), nil
	}

	_, err := runVirsh("detach-device", vmName, tmpPath, "--persistent")
	if err != nil {
		return unwrapError(err), nil
	}

	// Rebind to original driver (nvidia, amdgpu, i915)
	// Remove from vfio-pci and let kernel rebind
	unbindPath := "/sys/bus/pci/devices/" + normalized + "/driver/unbind"
	writeFileAsRoot(unbindPath, normalized)

	// Try to rebind to nvidia driver
	nvidiaBindPath := "/sys/bus/pci/drivers/nvidia/bind"
	writeFileAsRoot(nvidiaBindPath, normalized)

	return okResult(GPUAssignmentResult{
		PCIAddress: normalized,
		VMName:     vmName,
		Status:     "released",
	}), nil
}

// ---- VFIO helpers ----

// checkVFIOBound checks if a PCI device is currently bound to vfio-pci driver.
func checkVFIOBound(pciAddr string) bool {
	if pciAddr == "" {
		return false
	}
	normalized := normalizePCIAddress(pciAddr)
	link, err := filepath.EvalSymlinks("/sys/bus/pci/devices/" + normalized + "/driver")
	if err != nil {
		return false
	}
	return strings.Contains(filepath.Base(link), "vfio")
}

// normalizePCIAddress converts "01:00.0" or "0000:01:00.0" to "0000:01:00.0".
func normalizePCIAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	if !strings.Contains(addr, ":") {
		return addr
	}
	// Add domain prefix if missing
	if !strings.HasPrefix(addr, "0000:") {
		addr = "0000:" + addr
	}
	return addr
}

// extractPCIComponents splits "0000:01:00.0" into bus, slot, function for libvirt XML.
// Returns: bus="0x01", slot="0x00", function="0x0"
func extractPCIComponents(addr string) (bus, slot, function string) {
	normalized := normalizePCIAddress(addr)
	// Format: 0000:01:00.0
	parts := strings.SplitN(normalized, ":", 3)
	if len(parts) != 3 {
		return "0x00", "0x00", "0x0"
	}
	bus = "0x" + parts[1]
	slotFunc := strings.SplitN(parts[2], ".", 2)
	if len(slotFunc) != 2 {
		return bus, "0x00", "0x0"
	}
	slot = "0x" + slotFunc[0]
	function = "0x" + slotFunc[1]
	return
}

// ---- Parse helpers ----

func parseGPUMem(s string) int {
	s = strings.TrimSpace(s)
	n := parseIntSafe(s)
	return n
}

func parseIntSafe(s string) int {
	s = strings.TrimSpace(strings.TrimRight(s, " MiB"))
	n := 0
	fmt.Sscanf(s, "%d", &n)
	return n
}

func parseOptionalFloat(s string) *float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "[N/A]" || s == "N/A" {
		return nil
	}
	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err == nil {
		return &f
	}
	return nil
}

func parseOptionalInt(s string) *int {
	s = strings.TrimSpace(s)
	if s == "" || s == "[N/A]" || s == "N/A" {
		return nil
	}
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
		return &n
	}
	return nil
}

// ---- File helpers ----

func writeFileAsRoot(path, content string) error {
	return exec.Command("sh", "-c", fmt.Sprintf("echo '%s' > %s", content, path)).Run()
}

func writeFilePublic(path, content string) error {
	cmd := exec.Command("sh", "-c", fmt.Sprintf("cat > '%s'", path))
	cmd.Stdin = strings.NewReader(content)
	return cmd.Run()
}

func readFileAsString(path string) (string, error) {
	out, err := exec.Command("cat", path).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
