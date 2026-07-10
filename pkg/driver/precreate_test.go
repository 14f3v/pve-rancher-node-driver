package driver

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

// happyServer wires every route PreCreateCheck touches, PVE 9 flavor.
func happyServer(t *testing.T) *pvetest.Server {
	s := pvetest.New(t)
	s.Handle("GET", "/version", 200, map[string]string{"release": "9.2", "version": "9.2.1"})
	s.Handle("GET", "/access/permissions", 200, map[string]map[string]int{
		"/": {
			"VM.Clone": 1, "VM.Allocate": 1, "VM.Audit": 1, "VM.PowerMgmt": 1,
			"VM.Config.Disk": 1, "VM.Config.CPU": 1, "VM.Config.Memory": 1,
			"VM.Config.Network": 1, "VM.Config.Cloudinit": 1, "VM.Config.Options": 1,
			"VM.GuestAgent.Audit":     1,
			"Datastore.AllocateSpace": 1, "Datastore.Audit": 1, "SDN.Use": 1,
		},
	})
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{
		{"vmid": 9000, "name": "ubuntu-2404-tmpl", "node": "pve1", "type": "qemu", "template": 1, "status": "stopped"},
	})
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/9000/status/current", 200, map[string]interface{}{
		"status": "stopped", "vmid": 9000, "name": "ubuntu-2404-tmpl", "template": 1,
	})
	s.Handle("GET", "/nodes/pve1/qemu/9000/config", 200, map[string]interface{}{
		"name": "ubuntu-2404-tmpl", "template": 1, "agent": "1",
		"boot":  "order=scsi0;ide2;net0",
		"scsi0": "local-lvm:base-9000-disk-0,size=20G",
		"ide2":  "local-lvm:vm-9000-cloudinit,media=cdrom",
		"net0":  "virtio=DE:AD:BE:EF:12:34,bridge=vmbr0",
	})
	s.Handle("GET", "/nodes/pve1/storage", 200, []map[string]interface{}{
		{"storage": "local-lvm", "type": "lvmthin", "content": "images,rootdir", "shared": 0, "active": 1},
	})
	s.Handle("GET", "/nodes/pve1/network", 200, []map[string]interface{}{
		{"iface": "vmbr0", "type": "bridge"},
	})
	return s
}

func testDriver(s *pvetest.Server) *Driver {
	d := NewDriver("c1-pool1-abcde", "/tmp/store")
	d.URL = s.URL()
	d.TokenID = "rancher@pve!machine"
	d.TokenSecret = "s3cret"
	d.TemplateRef = "ubuntu-2404-tmpl"
	d.Storage = "local-lvm"
	d.Bridge = "vmbr0"
	d.Cores = 2
	d.MemoryMB = 4096
	d.DiskSizeGB = 40
	d.AgentTimeout = 300
	d.VMIDRange = "10000-19999"
	d.SSHUser = "rancher"
	d.SSHPort = 22
	return d
}

func TestPreCreateCheckHappy(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	assert.NoError(t, d.PreCreateCheck())
}

func TestPreCreateCheckMissingPrivilege(t *testing.T) {
	s := happyServer(t)
	// Override: token has zero ACLs (classic silent privsep failure).
	s.Handle("GET", "/access/permissions", 200, map[string]map[string]int{})
	d := testDriver(s)
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "privilege")
	assert.Contains(t, err.Error(), "VM.Clone")
}

func TestPreCreateCheckBadTemplate(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	d.TemplateRef = "missing-template"
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ubuntu-2404-tmpl") // lists what exists
}

func TestPreCreateCheckNoCloudInitDrive(t *testing.T) {
	s := happyServer(t)
	s.Handle("GET", "/nodes/pve1/qemu/9000/config", 200, map[string]interface{}{
		"name": "ubuntu-2404-tmpl", "template": 1, "agent": "1",
		"boot":  "order=scsi0",
		"scsi0": "local-lvm:base-9000-disk-0,size=20G",
		"net0":  "virtio=DE:AD:BE:EF:12:34,bridge=vmbr0",
	})
	d := testDriver(s)
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cloud-init")
}

func TestPreCreateCheckDiskShrink(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	d.DiskSizeGB = 10 // template is 20G
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shrink")
}

func TestPreCreateCheckUnknownBridge(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	d.Bridge = "vmbr9"
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vmbr0") // lists available bridges
}

func TestPreCreateCheckUnknownStorage(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	d.Storage = "nope"
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local-lvm")
}

func TestPreCreateCheckLinkedCloneWithStorage(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	d.LinkedClone = true // storage already set → invalid combination
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "linked")
}

func TestPreCreateCheckBadMachineName(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	d.MachineName = "Invalid_Name_With_Underscores_That_Is_Also_Way_Too_Long_For_A_Hostname_Really"
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hostname")
}

func TestPreCreateCheckPermissionEndpointUnavailable(t *testing.T) {
	// If /access/permissions itself errors, warn and continue (some
	// realms restrict it) — do not block creation.
	s := happyServer(t)
	s.HandleFunc("GET", "/access/permissions", func(w http.ResponseWriter, r *http.Request) {
		pvetest.PVEError(w, 501, "not implemented")
	})
	d := testDriver(s)
	assert.NoError(t, d.PreCreateCheck())
}
