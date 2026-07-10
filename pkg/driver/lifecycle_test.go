package driver

import (
	"net/http"
	"testing"

	"github.com/rancher/machine/libmachine/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

// machineFixture: a created machine (VMID 10005 on pve1) with our tag.
func machineFixture(t *testing.T, status string) (*pvetest.Server, *Driver) {
	s := pvetest.New(t)
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/10005/status/current", 200, map[string]interface{}{
		"status": status, "vmid": 10005, "name": "c1-pool1-abcde", "tags": "rancher-pvenode",
	})
	s.Handle("GET", "/nodes/pve1/qemu/10005/config", 200, map[string]interface{}{
		"name": "c1-pool1-abcde", "tags": "rancher-pvenode",
		"net0": "virtio=AA:BB:CC:DD:EE:01,bridge=vmbr0",
	})
	d := testDriver(s)
	d.VMID = 10005
	d.PVENode = "pve1"
	return s, d
}

func TestGetStateRunning(t *testing.T) {
	_, d := machineFixture(t, "running")
	st, err := d.GetState()
	require.NoError(t, err)
	assert.Equal(t, state.Running, st)
}

func TestGetStateStopped(t *testing.T) {
	_, d := machineFixture(t, "stopped")
	st, err := d.GetState()
	require.NoError(t, err)
	assert.Equal(t, state.Stopped, st)
}

func TestGetStateGoneVM(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/status/current", func(w http.ResponseWriter, r *http.Request) {
		pvetest.PVEError(w, 500, "Configuration file 'nodes/pve1/qemu-server/10005.conf' does not exist")
	})
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{})
	d := testDriver(s)
	d.VMID = 10005
	d.PVENode = "pve1"

	st, err := d.GetState()
	require.NoError(t, err)
	assert.Equal(t, state.NotFound, st)
}

func TestLifecycleOperations(t *testing.T) {
	s, d := machineFixture(t, "running")
	upid := s.OKTask("pve1")
	for _, op := range []string{"start", "shutdown", "stop", "reboot"} {
		s.Handle("POST", "/nodes/pve1/qemu/10005/status/"+op, 200, upid)
	}

	assert.NoError(t, d.Start())
	assert.NoError(t, d.Stop())
	assert.NoError(t, d.Kill())
	assert.NoError(t, d.Restart())
}

func TestGetURLRequiresRunning(t *testing.T) {
	_, d := machineFixture(t, "stopped")
	_, err := d.GetURL()
	assert.Error(t, err)
}

func TestGetURLAndSSHHostname(t *testing.T) {
	s, d := machineFixture(t, "running")
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/agent/network-get-interfaces", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":[
			{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:01","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"192.168.1.70","prefix":24}]}
		]}}`))
	})

	url, err := d.GetURL()
	require.NoError(t, err)
	assert.Equal(t, "tcp://192.168.1.70:2376", url)

	host, err := d.GetSSHHostname()
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.70", host)
	assert.Equal(t, "192.168.1.70", d.IPAddress) // cache refreshed
}

func TestGetIPFallsBackToCache(t *testing.T) {
	s, d := machineFixture(t, "running")
	d.IPAddress = "192.168.1.99"
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/agent/network-get-interfaces", func(w http.ResponseWriter, r *http.Request) {
		pvetest.PVEError(w, 500, "QEMU guest agent is not running")
	})

	ip, err := d.GetIP()
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.99", ip)
}

func TestLookupVMFallbackByNameAndTag(t *testing.T) {
	// Simulates the mid-create-crash orphan: persisted config has no VMID.
	s := pvetest.New(t)
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{
		{"vmid": 10007, "name": "c1-pool1-abcde", "node": "pve1", "type": "qemu", "template": 0, "status": "running"},
	})
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/10007/status/current", 200, map[string]interface{}{
		"status": "running", "vmid": 10007, "name": "c1-pool1-abcde", "tags": "rancher-pvenode",
	})
	s.Handle("GET", "/nodes/pve1/qemu/10007/config", 200, map[string]interface{}{
		"name": "c1-pool1-abcde", "tags": "rancher-pvenode",
	})
	d := testDriver(s) // MachineName c1-pool1-abcde, VMID 0

	st, err := d.GetState()
	require.NoError(t, err)
	assert.Equal(t, state.Running, st)
}
