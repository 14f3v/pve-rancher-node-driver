package pve

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

// cloneFixture returns a server with a template on pve1 and a task-status
// route; the clone handler is pluggable.
func cloneFixture(t *testing.T, cloneHandler http.HandlerFunc) (*pvetest.Server, *Client) {
	s := pvetest.New(t)
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{
		{"vmid": 9000, "name": "tmpl", "node": "pve1", "type": "qemu", "template": 1, "status": "stopped"},
	})
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/9000/status/current", 200, map[string]interface{}{
		"status": "stopped", "vmid": 9000, "name": "tmpl", "template": 1,
	})
	s.Handle("GET", "/nodes/pve1/qemu/9000/config", 200, map[string]interface{}{
		"name": "tmpl", "template": 1, "agent": "1",
		"scsi0": "local-lvm:base-9000-disk-0,size=20G",
		"ide2":  "local-lvm:vm-9000-cloudinit,media=cdrom",
		"net0":  "virtio=DE:AD:BE:EF:12:34,bridge=vmbr0",
	})
	s.HandleFunc("POST", "/nodes/pve1/qemu/9000/clone", cloneHandler)
	c := newTestClient(t, s)
	return s, c
}

// registerNewVM makes the freshly cloned VM fetchable and lets Config
// (tags) succeed.
func registerNewVM(s *pvetest.Server, vmid int, upid string) {
	path := nodeQemuPath("pve1", vmid)
	s.Handle("GET", path+"/status/current", 200, map[string]interface{}{
		"status": "stopped", "vmid": vmid, "name": "new-vm",
	})
	s.Handle("GET", path+"/config", 200, map[string]interface{}{
		"name": "new-vm",
		"net0": "virtio=AA:BB:CC:DD:EE:01,bridge=vmbr0",
	})
	s.Handle("POST", path+"/config", 200, upid)
}

func TestCloneHappyPath(t *testing.T) {
	var s *pvetest.Server
	var upid string
	var clonedID atomic.Int64
	s, c := cloneFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		clonedID.Store(int64(body["newid"].(float64)))
		registerNewVM(s, int(clonedID.Load()), upid)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": upid})
	})
	upid = s.OKTask("pve1")

	tmpl, err := c.GetVM(context.Background(), "pve1", 9000)
	require.NoError(t, err)

	vm, err := c.CloneFromTemplate(context.Background(), tmpl, CloneSpec{
		Name: "new-vm", VMIDLo: 10000, VMIDHi: 19999,
		Tags: []string{"rancher-pvenode"},
	})
	require.NoError(t, err)
	got := int(clonedID.Load())
	assert.Equal(t, got, int(vm.VMID))
	assert.GreaterOrEqual(t, got, 10000)
	assert.LessOrEqual(t, got, 19999)
}

func TestCloneRetriesOnVMIDConflict(t *testing.T) {
	var s *pvetest.Server
	var upid string
	var calls atomic.Int32
	s, c := cloneFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			pvetest.PVEError(w, 500, "unable to create VM - config file already exists")
			return
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		registerNewVM(s, int(body["newid"].(float64)), upid)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": upid})
	})
	upid = s.OKTask("pve1")

	tmpl, err := c.GetVM(context.Background(), "pve1", 9000)
	require.NoError(t, err)

	_, err = c.CloneFromTemplate(context.Background(), tmpl, CloneSpec{
		Name: "new-vm", VMIDLo: 10000, VMIDHi: 19999, Tags: []string{"rancher-pvenode"},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load())
}

func TestCloneRetriesOnTemplateLock(t *testing.T) {
	var s *pvetest.Server
	var upid string
	var calls atomic.Int32
	s, c := cloneFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			pvetest.PVEError(w, 500, "got lock request timeout")
			return
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		registerNewVM(s, int(body["newid"].(float64)), upid)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": upid})
	})
	upid = s.OKTask("pve1")

	tmpl, err := c.GetVM(context.Background(), "pve1", 9000)
	require.NoError(t, err)

	_, err = c.CloneFromTemplate(context.Background(), tmpl, CloneSpec{
		Name: "new-vm", VMIDLo: 10000, VMIDHi: 19999, Tags: []string{"rancher-pvenode"},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load())
}

func TestCloneAvoidsUsedVMIDs(t *testing.T) {
	// Range with exactly one free ID: existing VMs occupy the rest.
	existing := []map[string]interface{}{
		{"vmid": 9000, "name": "tmpl", "node": "pve1", "type": "qemu", "template": 1, "status": "stopped"},
		{"vmid": 100, "name": "used-a", "node": "pve1", "type": "qemu", "template": 0, "status": "running"},
		{"vmid": 101, "name": "used-b", "node": "pve1", "type": "qemu", "template": 0, "status": "running"},
	}
	var s *pvetest.Server
	var upid string
	s, c := cloneFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, 102.0, body["newid"], "must pick the only free VMID in range")
		registerNewVM(s, 102, upid)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": upid})
	})
	s.Handle("GET", "/cluster/resources", 200, existing)
	upid = s.OKTask("pve1")

	tmpl, err := c.GetVM(context.Background(), "pve1", 9000)
	require.NoError(t, err)

	_, err = c.CloneFromTemplate(context.Background(), tmpl, CloneSpec{
		Name: "new-vm", VMIDLo: 100, VMIDHi: 102, Tags: []string{"rancher-pvenode"},
	})
	require.NoError(t, err)
}

// TestCloneCleansUpAfterFailedTask drives the safety-critical orphan-cleanup
// path: the clone POST succeeds but the clone TASK fails, so CloneFromTemplate
// must best-effort-delete the partial VM before surfacing the error. Without
// this, a failed clone would silently leak a VM.
func TestCloneCleansUpAfterFailedTask(t *testing.T) {
	var s *pvetest.Server
	var failUpid, okUpid string
	var deleted atomic.Bool
	s, c := cloneFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := int(body["newid"].(float64))
		// Make the partial VM fetchable so bestEffortDelete can find it,
		// and record its deletion.
		registerNewVM(s, id, okUpid)
		s.HandleFunc("DELETE", nodeQemuPath("pve1", id), func(w http.ResponseWriter, r *http.Request) {
			deleted.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": okUpid})
		})
		// The clone task itself fails.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": failUpid})
	})
	failUpid = s.FailedTask("pve1", "clone failed: no space left on device")
	okUpid = s.OKTask("pve1")

	tmpl, err := c.GetVM(context.Background(), "pve1", 9000)
	require.NoError(t, err)

	vm, err := c.CloneFromTemplate(context.Background(), tmpl, CloneSpec{
		Name: "new-vm", VMIDLo: 10000, VMIDHi: 19999, Tags: []string{"rancher-pvenode"},
	})
	require.Error(t, err)
	assert.Nil(t, vm)
	assert.Contains(t, err.Error(), "no space left", "the clone-task failure must surface, not the cleanup outcome")
	assert.True(t, deleted.Load(), "a failed clone task must best-effort-delete the partial VM")
}
