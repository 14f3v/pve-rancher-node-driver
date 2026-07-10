package pve

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

// vmFixture returns a running VM at pve1/10005 plus its client.
func vmFixture(t *testing.T) (*pvetest.Server, *Client, *proxmox.VirtualMachine) {
	s := pvetest.New(t)
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/10005/status/current", 200, map[string]interface{}{
		"status": "running", "vmid": 10005, "name": "m1", "tags": "rancher-pvenode",
	})
	s.Handle("GET", "/nodes/pve1/qemu/10005/config", 200, map[string]interface{}{
		"name": "m1", "net0": "virtio=AA:BB:CC:DD:EE:01,bridge=vmbr0",
	})
	c := newTestClient(t, s)
	vm, err := c.GetVM(context.Background(), "pve1", 10005)
	require.NoError(t, err)
	return s, c, vm
}

func TestApplyConfig(t *testing.T) {
	s, c, vm := vmFixture(t)
	upid := s.OKTask("pve1")
	s.Handle("POST", "/nodes/pve1/qemu/10005/config", 200, upid)

	err := c.ApplyConfig(context.Background(), vm, []proxmox.VirtualMachineOption{
		{Name: "cores", Value: 2},
	})
	assert.NoError(t, err)
}

func TestApplyConfigTaskFailure(t *testing.T) {
	s, c, vm := vmFixture(t)
	upid := s.FailedTask("pve1", "hotplug problem")
	s.Handle("POST", "/nodes/pve1/qemu/10005/config", 200, upid)

	err := c.ApplyConfig(context.Background(), vm, []proxmox.VirtualMachineOption{
		{Name: "cores", Value: 2},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hotplug problem")
}

func TestResizeVMDisk(t *testing.T) {
	s, c, vm := vmFixture(t)
	upid := s.OKTask("pve1")
	s.Handle("PUT", "/nodes/pve1/qemu/10005/resize", 200, upid)

	assert.NoError(t, c.ResizeVMDisk(context.Background(), vm, "scsi0", 40))
}

func TestStartStopShutdown(t *testing.T) {
	s, c, vm := vmFixture(t)
	upid := s.OKTask("pve1")
	s.Handle("POST", "/nodes/pve1/qemu/10005/status/start", 200, upid)
	s.Handle("POST", "/nodes/pve1/qemu/10005/status/shutdown", 200, upid)
	s.Handle("POST", "/nodes/pve1/qemu/10005/status/stop", 200, upid)

	ctx := context.Background()
	assert.NoError(t, c.StartVM(ctx, vm))
	assert.NoError(t, c.ShutdownVM(ctx, vm, 30*time.Second))
	assert.NoError(t, c.StopVM(ctx, vm))
}

func TestDeleteVMRetriesLock(t *testing.T) {
	s, c, vm := vmFixture(t)
	upid := s.OKTask("pve1")
	var calls atomic.Int32
	s.HandleFunc("DELETE", "/nodes/pve1/qemu/10005", func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			pvetest.PVEError(w, 500, "can't lock file '/var/lock/qemu-server/lock-10005.conf' - got timeout")
			return
		}
		// purge params must be present
		q := r.URL.Query()
		assert.Equal(t, "1", q.Get("purge"))
		assert.Equal(t, "1", q.Get("destroy-unreferenced-disks"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"` + upid + `"}`))
	})

	assert.NoError(t, c.DeleteVM(context.Background(), vm))
	assert.Equal(t, int32(2), calls.Load())
}
