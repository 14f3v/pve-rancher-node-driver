package pve

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

// registerVM registers the two routes node.VirtualMachine() fetches.
func registerVM(s *pvetest.Server, node string, vmid int, status map[string]interface{}, config map[string]interface{}) {
	s.Handle("GET", "/nodes/"+node+"/status", 200, map[string]interface{}{})
	s.Handle("GET", nodeQemuPath(node, vmid)+"/status/current", 200, status)
	s.Handle("GET", nodeQemuPath(node, vmid)+"/config", 200, config)
}

func templateFixture(s *pvetest.Server) {
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{
		{"vmid": 9000, "name": "ubuntu-2404-tmpl", "node": "pve1", "type": "qemu", "template": 1, "status": "stopped"},
		{"vmid": 105, "name": "some-vm", "node": "pve1", "type": "qemu", "template": 0, "status": "running"},
	})
	registerVM(s, "pve1", 9000,
		map[string]interface{}{"status": "stopped", "vmid": 9000, "name": "ubuntu-2404-tmpl", "template": 1},
		map[string]interface{}{
			"name": "ubuntu-2404-tmpl", "template": 1, "agent": "1",
			"boot":  "order=scsi0;ide2;net0",
			"scsi0": "local-lvm:base-9000-disk-0,size=20G",
			"ide2":  "local-lvm:vm-9000-cloudinit,media=cdrom",
			"net0":  "virtio=DE:AD:BE:EF:12:34,bridge=vmbr0",
		})
}

func TestResolveTemplateByName(t *testing.T) {
	s := pvetest.New(t)
	templateFixture(s)
	c := newTestClient(t, s)

	vm, err := c.ResolveTemplate(context.Background(), "ubuntu-2404-tmpl")
	require.NoError(t, err)
	assert.Equal(t, 9000, int(vm.VMID))
	assert.Equal(t, "pve1", vm.Node)
}

func TestResolveTemplateByVMID(t *testing.T) {
	s := pvetest.New(t)
	templateFixture(s)
	c := newTestClient(t, s)

	vm, err := c.ResolveTemplate(context.Background(), "9000")
	require.NoError(t, err)
	assert.Equal(t, 9000, int(vm.VMID))
}

func TestResolveTemplateNotFoundListsAvailable(t *testing.T) {
	s := pvetest.New(t)
	templateFixture(s)
	c := newTestClient(t, s)

	_, err := c.ResolveTemplate(context.Background(), "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ubuntu-2404-tmpl") // actionable: names what exists
}

func TestResolveTemplateRejectsNonTemplate(t *testing.T) {
	s := pvetest.New(t)
	templateFixture(s)
	c := newTestClient(t, s)

	_, err := c.ResolveTemplate(context.Background(), "some-vm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a template")
}

func TestFindVMByNameAndTag(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{
		{"vmid": 10005, "name": "c1-pool1-abcde", "node": "pve1", "type": "qemu", "template": 0, "status": "running"},
	})
	registerVM(s, "pve1", 10005,
		map[string]interface{}{"status": "running", "vmid": 10005, "name": "c1-pool1-abcde", "tags": "rancher-pvenode"},
		map[string]interface{}{"name": "c1-pool1-abcde", "tags": "rancher-pvenode"})
	c := newTestClient(t, s)

	vm, err := c.FindVMByNameAndTag(context.Background(), "c1-pool1-abcde", "rancher-pvenode")
	require.NoError(t, err)
	require.NotNil(t, vm)
	assert.Equal(t, 10005, int(vm.VMID))

	vm, err = c.FindVMByNameAndTag(context.Background(), "missing", "rancher-pvenode")
	require.NoError(t, err)
	assert.Nil(t, vm)
}

func TestNodeInventories(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/nodes/pve1/storage", 200, []map[string]interface{}{
		{"storage": "local-lvm", "type": "lvmthin", "content": "images,rootdir", "shared": 0, "active": 1},
		{"storage": "cephpool", "type": "rbd", "content": "images", "shared": 1, "active": 1},
	})
	s.Handle("GET", "/nodes/pve1/network", 200, []map[string]interface{}{
		{"iface": "vmbr0", "type": "bridge"},
		{"iface": "eno1", "type": "eth"},
	})
	c := newTestClient(t, s)

	storages, err := c.NodeStorages(context.Background(), "pve1")
	require.NoError(t, err)
	require.Len(t, storages, 2)
	assert.Equal(t, "lvmthin", storages[0].Type)
	assert.True(t, storages[1].Shared)

	bridges, err := c.NodeBridges(context.Background(), "pve1")
	require.NoError(t, err)
	assert.Equal(t, []string{"vmbr0"}, bridges)
}
