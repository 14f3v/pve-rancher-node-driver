package driver

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

func TestRemoveIdempotentWhenGone(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/status/current", func(w http.ResponseWriter, r *http.Request) {
		pvetest.PVEError(w, 500, "Configuration file 'nodes/pve1/qemu-server/10005.conf' does not exist")
	})
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{})
	d := testDriver(s)
	d.VMID = 10005
	d.PVENode = "pve1"

	assert.NoError(t, d.Remove(), "removing an already-gone VM must succeed")
}

func TestRemoveRefusesUntaggedVM(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/10005/status/current", 200, map[string]interface{}{
		"status": "running", "vmid": 10005, "name": "c1-pool1-abcde", "tags": "somebody-elses-vm",
	})
	s.Handle("GET", "/nodes/pve1/qemu/10005/config", 200, map[string]interface{}{
		"name": "c1-pool1-abcde", "tags": "somebody-elses-vm",
	})
	d := testDriver(s)
	d.VMID = 10005
	d.PVENode = "pve1"

	err := d.Remove()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rancher-pvenode")
}

func TestRemoveStopsThenDeletes(t *testing.T) {
	s, d := machineFixture(t, "running")
	upid := s.OKTask("pve1")
	s.Handle("POST", "/nodes/pve1/qemu/10005/status/shutdown", 200, upid)
	var deleted atomic.Bool
	s.HandleFunc("DELETE", "/nodes/pve1/qemu/10005", func(w http.ResponseWriter, r *http.Request) {
		deleted.Store(true)
		assert.Equal(t, "1", r.URL.Query().Get("purge"))
		assert.Equal(t, "1", r.URL.Query().Get("destroy-unreferenced-disks"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"` + upid + `"}`))
	})

	require.NoError(t, d.Remove())
	assert.True(t, deleted.Load())
}

func TestRemoveFallbackByNameAndTag(t *testing.T) {
	// Mid-create crash: no VMID persisted, but the tagged VM exists.
	s := pvetest.New(t)
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{
		{"vmid": 10007, "name": "c1-pool1-abcde", "node": "pve1", "type": "qemu", "template": 0, "status": "stopped"},
	})
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/10007/status/current", 200, map[string]interface{}{
		"status": "stopped", "vmid": 10007, "name": "c1-pool1-abcde", "tags": "rancher-pvenode",
	})
	s.Handle("GET", "/nodes/pve1/qemu/10007/config", 200, map[string]interface{}{
		"name": "c1-pool1-abcde", "tags": "rancher-pvenode",
	})
	upid := s.OKTask("pve1")
	var deleted atomic.Bool
	s.HandleFunc("DELETE", "/nodes/pve1/qemu/10007", func(w http.ResponseWriter, r *http.Request) {
		deleted.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"` + upid + `"}`))
	})
	d := testDriver(s) // VMID == 0

	require.NoError(t, d.Remove())
	assert.True(t, deleted.Load())
}
