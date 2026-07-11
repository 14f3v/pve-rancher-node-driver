package driver

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
	"github.com/14f3v/pve-rancher-node-driver/pkg/pve"
)

// createFixture wires the full Create flow on top of happyServer (from
// precreate_test.go). The clone handler registers all routes for the VM it
// creates, including its DELETE route: when deleted is non-nil it records
// deletions there; when nil, deletion still succeeds silently.
func createFixture(t *testing.T, storeDir string, deleted *atomic.Bool) (*pvetest.Server, *Driver, *atomic.Int32) {
	s := happyServer(t)
	upid := s.OKTask("pve1")

	var cloneCalls atomic.Int32
	s.HandleFunc("POST", "/nodes/pve1/qemu/9000/clone", func(w http.ResponseWriter, r *http.Request) {
		cloneCalls.Add(1)
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := int(body["newid"].(float64))
		path := "/nodes/pve1/qemu/" + strconv.Itoa(id)

		s.Handle("GET", path+"/status/current", 200, map[string]interface{}{
			"status": "stopped", "vmid": id, "name": "c1-pool1-abcde", "tags": "rancher-pvenode",
		})
		s.Handle("GET", path+"/config", 200, map[string]interface{}{
			"name": "c1-pool1-abcde", "tags": "rancher-pvenode",
			"net0":  "virtio=AA:BB:CC:DD:EE:99,bridge=vmbr0",
			"scsi0": "local-lvm:vm-disk-0,size=20G",
			"ide2":  "local-lvm:vm-cloudinit,media=cdrom",
		})
		s.Handle("POST", path+"/config", 200, upid)
		s.Handle("PUT", path+"/resize", 200, upid)
		s.Handle("POST", path+"/status/start", 200, upid)
		s.Handle("POST", path+"/status/shutdown", 200, upid)
		s.Handle("POST", path+"/status/stop", 200, upid)
		s.HandleFunc("DELETE", path, func(w http.ResponseWriter, r *http.Request) {
			if deleted != nil {
				deleted.Store(true)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":"` + upid + `"}`))
		})
		s.HandleFunc("GET", path+"/agent/network-get-interfaces", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"result":[
				{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:99","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"192.0.2.10","prefix":24}]}
			]}}`))
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": upid})
	})

	d := testDriver(s)
	d.StorePath = storeDir
	require.NoError(t, os.MkdirAll(filepath.Join(storeDir, "machines", d.MachineName), 0o755))
	return s, d, &cloneCalls
}

func TestCreateHappyPath(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = ln.Close() }()
	restore := pve.SetProbeDialer(func(network, address string, timeout time.Duration) (net.Conn, error) {
		return net.DialTimeout("tcp", ln.Addr().String(), timeout)
	})
	defer restore()

	_, d, cloneCalls := createFixture(t, t.TempDir(), nil)
	require.NoError(t, d.Create())

	assert.NotZero(t, d.VMID)
	assert.Equal(t, "pve1", d.PVENode)
	assert.Equal(t, "192.0.2.10", d.IPAddress)
	assert.Equal(t, int32(1), cloneCalls.Load())
	_, err = os.Stat(d.GetSSHKeyPath())
	assert.NoError(t, err, "private key must exist at GetSSHKeyPath")
	_, err = os.Stat(d.GetSSHKeyPath() + ".pub")
	assert.NoError(t, err)
}

func TestCreateCleansUpOnProvisionFailure(t *testing.T) {
	restore := pve.SetProbeDialer(func(network, address string, timeout time.Duration) (net.Conn, error) {
		return nil, errors.New("never reachable")
	})
	defer restore()

	var deleted atomic.Bool
	_, d, _ := createFixture(t, t.TempDir(), &deleted)
	d.AgentTimeout = 2 // fail the IP wait fast

	err := d.Create()
	require.Error(t, err)
	assert.True(t, deleted.Load(), "failed create must delete the half-created VM")
}

func TestCreateKeepOnFailureSkipsCleanup(t *testing.T) {
	restore := pve.SetProbeDialer(func(network, address string, timeout time.Duration) (net.Conn, error) {
		return nil, errors.New("never reachable")
	})
	defer restore()

	var deleted atomic.Bool
	_, d, _ := createFixture(t, t.TempDir(), &deleted)
	d.AgentTimeout = 2
	d.KeepOnFailure = true

	err := d.Create()
	require.Error(t, err)
	assert.False(t, deleted.Load(), "keep-on-failure must not delete the VM")
	assert.NotZero(t, d.VMID, "VMID must remain persisted for debugging")
}
