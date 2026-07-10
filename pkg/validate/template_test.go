package validate

import (
	"testing"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tmplConfig() *proxmox.VirtualMachineConfig {
	return &proxmox.VirtualMachineConfig{
		Template: proxmox.IntOrBool(true),
		Agent:    "1",
		Boot:     "order=scsi0;ide2;net0",
		SCSIs:    map[string]string{"scsi0": "local-lvm:base-9000-disk-0,size=20G"},
		IDEs:     map[string]string{"ide2": "local-lvm:vm-9000-cloudinit,media=cdrom"},
		Nets:     map[string]string{"net0": "virtio=DE:AD:BE:EF:12:34,bridge=vmbr0,firewall=1"},
	}
}

func tmplVM() *proxmox.VirtualMachine {
	return &proxmox.VirtualMachine{
		Template:             true,
		VirtualMachineConfig: tmplConfig(),
	}
}

func TestEnsureTemplateHappy(t *testing.T) {
	assert.NoError(t, EnsureTemplate(tmplVM()))
}

func TestEnsureTemplateRejectsAgentless(t *testing.T) {
	vm := tmplVM()
	vm.VirtualMachineConfig.Agent = ""
	err := EnsureTemplate(vm)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent")
}

func TestEnsureTemplateRejectsAgentDisabled(t *testing.T) {
	vm := tmplVM()
	vm.VirtualMachineConfig.Agent = "0"
	assert.Error(t, EnsureTemplate(vm))
}

func TestCloudInitDrive(t *testing.T) {
	dev, ok := CloudInitDrive(tmplConfig())
	assert.True(t, ok)
	assert.Equal(t, "ide2", dev)

	cfg := tmplConfig()
	cfg.IDEs = nil
	_, ok = CloudInitDrive(cfg)
	assert.False(t, ok)
}

func TestNICMAC(t *testing.T) {
	mac, err := NICMAC(tmplConfig(), "net0")
	require.NoError(t, err)
	assert.Equal(t, "DE:AD:BE:EF:12:34", mac)

	// all NIC models PVE supports for MAC extraction
	for _, model := range []string{"virtio", "e1000", "e1000e", "rtl8139", "vmxnet3"} {
		cfg := tmplConfig()
		cfg.Nets = map[string]string{"net0": model + "=AA:BB:CC:DD:EE:FF,bridge=vmbr1"}
		mac, err := NICMAC(cfg, "net0")
		require.NoError(t, err, model)
		assert.Equal(t, "AA:BB:CC:DD:EE:FF", mac)
	}

	_, err = NICMAC(&proxmox.VirtualMachineConfig{}, "net0")
	assert.Error(t, err)
}

func TestBootDisk(t *testing.T) {
	key, sizeGB, val, err := BootDisk(tmplConfig())
	require.NoError(t, err)
	assert.Equal(t, "scsi0", key)
	assert.Equal(t, 20, sizeGB)
	assert.Contains(t, val, "base-9000-disk-0")
}

func TestBootDiskSkipsCloudInitAndCDROM(t *testing.T) {
	cfg := tmplConfig()
	cfg.Boot = "order=ide2;scsi0"
	key, _, _, err := BootDisk(cfg)
	require.NoError(t, err)
	assert.Equal(t, "scsi0", key) // ide2 is the cloudinit cdrom, not a boot disk
}

func TestBootDiskFallbackWithoutBootOrder(t *testing.T) {
	cfg := tmplConfig()
	cfg.Boot = ""
	key, _, _, err := BootDisk(cfg)
	require.NoError(t, err)
	assert.Equal(t, "scsi0", key)
}

func TestBootDiskSizeUnits(t *testing.T) {
	for value, wantGB := range map[string]int{
		"local-lvm:d0,size=32G":   32,
		"local-lvm:d0,size=2048M": 2,
		"local-lvm:d0,size=1T":    1024,
	} {
		cfg := tmplConfig()
		cfg.SCSIs = map[string]string{"scsi0": value}
		_, sizeGB, _, err := BootDisk(cfg)
		require.NoError(t, err, value)
		assert.Equal(t, wantGB, sizeGB, value)
	}
}
