package validate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	proxmox "github.com/luthermonson/go-proxmox"
)

// nicModels are the PVE NIC model keys whose value in a netN config string
// is the MAC address, e.g. "virtio=DE:AD:BE:EF:12:34,bridge=vmbr0".
var nicModels = map[string]bool{
	"virtio": true, "e1000": true, "e1000e": true, "rtl8139": true, "vmxnet3": true,
}

// EnsureTemplate checks the clone source is a template with the guest
// agent enabled. Without the agent the driver can never learn the VM's
// DHCP address.
func EnsureTemplate(vm *proxmox.VirtualMachine) error {
	if !bool(vm.Template) {
		return fmt.Errorf("VM %q is not a template", vm.Name)
	}
	cfg := vm.VirtualMachineConfig
	if cfg == nil {
		return fmt.Errorf("template %q has no readable config", vm.Name)
	}
	agent := cfg.Agent
	if agent == "" || strings.HasPrefix(agent, "0") || strings.Contains(agent, "enabled=0") {
		return fmt.Errorf(
			"template %q does not have the QEMU guest agent enabled (agent=%q); "+
				"set 'qm set <vmid> --agent 1' AND make sure qemu-guest-agent is installed in the image — "+
				"without it the driver cannot learn the VM's DHCP address", vm.Name, agent)
	}
	return nil
}

// CloudInitDrive finds the cloud-init drive (e.g. ide2 = "storage:vm-N-cloudinit,media=cdrom").
// Without one, ciuser/sshkeys/ipconfig0 are stored but never delivered to the guest.
func CloudInitDrive(cfg *proxmox.VirtualMachineConfig) (string, bool) {
	for _, devices := range []map[string]string{cfg.IDEs, cfg.SCSIs, cfg.SATAs} {
		for key, val := range devices {
			if strings.Contains(val, "cloudinit") {
				return key, true
			}
		}
	}
	return "", false
}

// NICMAC extracts the MAC address of a network device (e.g. "net0") from
// the VM config. The MAC is the value of the NIC-model key.
func NICMAC(cfg *proxmox.VirtualMachineConfig, key string) (string, error) {
	val, ok := cfg.Nets[key]
	if !ok || val == "" {
		return "", fmt.Errorf("VM config has no %s network device", key)
	}
	for _, part := range strings.Split(val, ",") {
		k, v, found := strings.Cut(strings.TrimSpace(part), "=")
		if found && nicModels[k] {
			return strings.ToUpper(v), nil
		}
	}
	return "", fmt.Errorf("could not extract MAC from %s=%q", key, val)
}

var sizeRe = regexp.MustCompile(`(?:^|,)size=(\d+)([MGT])`)

// BootDisk finds the template's boot disk key (e.g. "scsi0"), its size in
// GB, and the raw config value. It follows the boot order, skipping
// cdrom/cloudinit devices, then falls back to conventional first disks.
func BootDisk(cfg *proxmox.VirtualMachineConfig) (string, int, string, error) {
	lookup := func(key string) (string, bool) {
		for _, devices := range []map[string]string{cfg.SCSIs, cfg.VirtIOs, cfg.IDEs, cfg.SATAs} {
			if v, ok := devices[key]; ok {
				return v, true
			}
		}
		return "", false
	}
	isDisk := func(val string) bool {
		return !strings.Contains(val, "media=cdrom") && !strings.Contains(val, "cloudinit")
	}

	var candidates []string
	if order, ok := strings.CutPrefix(cfg.Boot, "order="); ok {
		candidates = strings.Split(order, ";")
	}
	candidates = append(candidates, "scsi0", "virtio0", "ide0", "sata0")

	for _, key := range candidates {
		key = strings.TrimSpace(key)
		val, ok := lookup(key)
		if !ok || !isDisk(val) {
			continue
		}
		m := sizeRe.FindStringSubmatch(val)
		if m == nil {
			return "", 0, "", fmt.Errorf("boot disk %s=%q has no parsable size", key, val)
		}
		n, _ := strconv.Atoi(m[1])
		sizeGB := 0
		switch m[2] {
		case "M":
			sizeGB = (n + 1023) / 1024
		case "G":
			sizeGB = n
		case "T":
			sizeGB = n * 1024
		}
		return key, sizeGB, val, nil
	}
	return "", 0, "", fmt.Errorf("could not find a boot disk in the template config (boot=%q)", cfg.Boot)
}
