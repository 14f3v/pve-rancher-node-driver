package pve

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/log"

	"github.com/14f3v/pve-rancher-node-driver/pkg/validate"
)

// probeDialer is a seam for tests; production uses net.DialTimeout.
var probeDialer = net.DialTimeout

func setProbeDialerForTest(d func(network, address string, timeout time.Duration) (net.Conn, error)) (restore func()) {
	old := probeDialer
	probeDialer = d
	return func() { probeDialer = old }
}

const (
	ipPollStart = 2 * time.Second
	ipPollMax   = 15 * time.Second
	probeWait   = 3 * time.Second
)

// WaitForIP waits until the guest agent reports an IPv4 address on the
// VM's net0 interface (matched by MAC — interface names are unreliable and
// reused templates report docker0/cni0/etc.), then verifies TCP
// reachability of the SSH port. "Agent responded" is NOT "has address":
// the agent often comes up before DHCP finishes.
func (c *Client) WaitForIP(ctx context.Context, vm *proxmox.VirtualMachine, sshPort int, timeout time.Duration) (string, error) {
	mac, err := validate.NICMAC(vm.VirtualMachineConfig, "net0")
	if err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	interval := ipPollStart
	lastState := "waiting for qemu-guest-agent"
	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf(
				"timed out after %s waiting for VM %d's IP (last state: %s) — "+
					"check qemu-guest-agent is installed and enabled in the template, and that DHCP works on the VM's bridge",
				timeout, int(vm.VMID), lastState)
		}

		ifaces, err := vm.AgentGetNetworkIFaces(ctx)
		switch {
		case err == nil:
			if ip := pickIPv4(ifaces, mac); ip != "" {
				addr := net.JoinHostPort(ip, strconv.Itoa(sshPort))
				conn, perr := probeDialer("tcp", addr, probeWait)
				if perr == nil {
					_ = conn.Close()
					return ip, nil
				}
				lastState = fmt.Sprintf("got IP %s but %s not reachable yet", ip, addr)
			} else {
				lastState = fmt.Sprintf("agent up, no IPv4 on interface %s yet", mac)
			}
		case IsAgentNotRunning(err):
			lastState = "qemu-guest-agent not running yet"
		case proxmox.IsNotAuthorized(err):
			return "", fmt.Errorf(
				"token may not query the guest agent (needs VM.Monitor on PVE 8 / VM.GuestAgent.Audit on PVE 9): %w", err)
		default:
			lastState = fmt.Sprintf("agent query error: %v", err)
		}
		log.Debugf("pvenode: VM %d: %s", int(vm.VMID), lastState)

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
		if interval = interval * 3 / 2; interval > ipPollMax {
			interval = ipPollMax
		}
	}
}

// QueryAgentIP is the single-shot variant for GetIP calls.
func (c *Client) QueryAgentIP(ctx context.Context, vm *proxmox.VirtualMachine) (string, error) {
	mac, err := validate.NICMAC(vm.VirtualMachineConfig, "net0")
	if err != nil {
		return "", err
	}
	ifaces, err := vm.AgentGetNetworkIFaces(ctx)
	if err != nil {
		return "", err
	}
	ip := pickIPv4(ifaces, mac)
	if ip == "" {
		return "", fmt.Errorf("guest agent reports no usable IPv4 on interface %s", mac)
	}
	return ip, nil
}

// pickIPv4 returns the first global IPv4 on the interface whose MAC
// matches (case-insensitive). Loopback, link-local and unspecified
// addresses never count.
func pickIPv4(ifaces []*proxmox.AgentNetworkIface, mac string) string {
	for _, ifc := range ifaces {
		if !strings.EqualFold(ifc.HardwareAddress, mac) {
			continue
		}
		for _, a := range ifc.IPAddresses {
			if a.IPAddressType != "ipv4" {
				continue
			}
			ip := net.ParseIP(a.IPAddress)
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			return a.IPAddress
		}
	}
	return ""
}
