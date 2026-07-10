package pve

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

func iface(name, mac string, addrs ...map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"name": name, "hardware-address": mac, "ip-addresses": addrs,
	}
}

func addr(kind, ip string) map[string]interface{} {
	return map[string]interface{}{"ip-address-type": kind, "ip-address": ip, "prefix": 24}
}

// The adversarial fixture set from the spec: docker/cni bridges, IPv6
// first, agent answering before DHCP finished.
func TestPickIPv4Adversarial(t *testing.T) {
	mac := "AA:BB:CC:DD:EE:01"
	toIfaces := func(raw []map[string]interface{}) []*proxmox.AgentNetworkIface {
		// build typed structs matching the JSON the agent returns
		var out []*proxmox.AgentNetworkIface
		for _, r := range raw {
			ifc := &proxmox.AgentNetworkIface{
				Name:            r["name"].(string),
				HardwareAddress: r["hardware-address"].(string),
			}
			for _, a := range r["ip-addresses"].([]map[string]interface{}) {
				ifc.IPAddresses = append(ifc.IPAddresses, &proxmox.AgentNetworkIPAddress{
					IPAddressType: a["ip-address-type"].(string),
					IPAddress:     a["ip-address"].(string),
				})
			}
			out = append(out, ifc)
		}
		return out
	}

	tests := []struct {
		name   string
		ifaces []map[string]interface{}
		want   string
	}{
		{
			name: "docker and cni bridges present — must pick the MAC-matched NIC",
			ifaces: []map[string]interface{}{
				iface("docker0", "02:42:00:00:00:01", addr("ipv4", "172.17.0.1")),
				iface("cni0", "02:42:00:00:00:02", addr("ipv4", "10.42.0.1")),
				iface("ens18", mac, addr("ipv4", "192.168.1.50")),
			},
			want: "192.168.1.50",
		},
		{
			name: "IPv6 listed before IPv4 on the right interface",
			ifaces: []map[string]interface{}{
				iface("ens18", mac, addr("ipv6", "fe80::1"), addr("ipv6", "2001:db8::5"), addr("ipv4", "192.168.1.51")),
			},
			want: "192.168.1.51",
		},
		{
			name: "agent up before DHCP: right NIC has no IPv4 yet",
			ifaces: []map[string]interface{}{
				iface("ens18", mac, addr("ipv6", "fe80::1")),
			},
			want: "",
		},
		{
			name: "link-local IPv4 must not count",
			ifaces: []map[string]interface{}{
				iface("ens18", mac, addr("ipv4", "169.254.10.10")),
			},
			want: "",
		},
		{
			name: "interface renamed (eth0 vs ens18) is irrelevant — match is by MAC",
			ifaces: []map[string]interface{}{
				iface("eth0", mac, addr("ipv4", "192.168.1.52")),
			},
			want: "192.168.1.52",
		},
		{
			name: "MAC case-insensitive",
			ifaces: []map[string]interface{}{
				iface("ens18", "aa:bb:cc:dd:ee:01", addr("ipv4", "192.168.1.53")),
			},
			want: "192.168.1.53",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, pickIPv4(toIfaces(tc.ifaces), mac))
		})
	}
}

func TestWaitForIPHappyAfterAgentDelay(t *testing.T) {
	s, c, vm := vmFixture(t) // net0 MAC AA:BB:CC:DD:EE:01 (Task 10 fixture)

	// A local listener plays the VM's sshd; the probe dialer is redirected
	// to it because the fixture's reported IP (192.0.2.10, TEST-NET) is not
	// actually routable in the test environment.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	restore := setProbeDialerForTest(func(network, address string, d time.Duration) (net.Conn, error) {
		assert.Equal(t, "192.0.2.10:22", address, "probe must target the reported IP and SSH port")
		return net.DialTimeout("tcp", ln.Addr().String(), d)
	})
	defer restore()

	// The three phases of a real DHCP boot: agent down → agent up but no
	// address yet → address present (alongside a docker0 decoy).
	var calls atomic.Int32
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/agent/network-get-interfaces", func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			pvetest.PVEError(w, 500, "QEMU guest agent is not running")
		case 2:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"result":[
				{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:01","ip-addresses":[]}
			]}}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"result":[
				{"name":"docker0","hardware-address":"02:42:00:00:00:01","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"172.17.0.1","prefix":16}]},
				{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:01","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"192.0.2.10","prefix":24}]}
			]}}`))
		}
	})

	ip, err := c.WaitForIP(context.Background(), vm, 22, 30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "192.0.2.10", ip)
	assert.GreaterOrEqual(t, calls.Load(), int32(3))
}

func TestWaitForIPTimeoutMessage(t *testing.T) {
	s, c, vm := vmFixture(t)
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/agent/network-get-interfaces", func(w http.ResponseWriter, r *http.Request) {
		pvetest.PVEError(w, 500, "QEMU guest agent is not running")
	})

	_, err := c.WaitForIP(context.Background(), vm, 22, 3*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "qemu-guest-agent is installed and enabled")
}

func TestQueryAgentIP(t *testing.T) {
	s, c, vm := vmFixture(t)
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/agent/network-get-interfaces", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":[
			{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:01","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"192.168.1.60","prefix":24}]}
		]}}`))
	})

	ip, err := c.QueryAgentIP(context.Background(), vm)
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.60", ip)
}
