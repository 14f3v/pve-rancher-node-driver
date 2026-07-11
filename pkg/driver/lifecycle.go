package driver

import (
	"context"
	"fmt"
	"net"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/drivers"
	"github.com/rancher/machine/libmachine/log"
	"github.com/rancher/machine/libmachine/state"

	"github.com/14f3v/pve-rancher-node-driver/pkg/pve"
)

const gracefulShutdownTimeout = 90 * time.Second

// withVM runs fn against this machine's VM; error if the VM is gone.
func (d *Driver) withVM(fn func(ctx context.Context, c *pve.Client, vm *proxmox.VirtualMachine) error) error {
	ctx := context.Background()
	c, err := d.client()
	if err != nil {
		return err
	}
	vm, err := d.lookupVM(ctx, c)
	if err != nil {
		return err
	}
	if vm == nil {
		return fmt.Errorf("machine %q (VMID %d) no longer exists on PVE", d.MachineName, d.VMID)
	}
	return fn(ctx, c, vm)
}

func (d *Driver) GetState() (state.State, error) {
	ctx := context.Background()
	c, err := d.client()
	if err != nil {
		return state.None, err
	}
	vm, err := d.lookupVM(ctx, c)
	if err != nil {
		return state.Error, err
	}
	if vm == nil {
		return state.NotFound, nil
	}
	switch vm.Status {
	case proxmox.StatusVirtualMachineRunning:
		if vm.Lock == "clone" {
			return state.Starting, nil
		}
		return state.Running, nil
	case proxmox.StatusVirtualMachineStopped:
		return state.Stopped, nil
	case proxmox.StatusVirtualMachinePaused:
		return state.Paused, nil
	default:
		return state.Error, fmt.Errorf("unknown PVE VM status %q", vm.Status)
	}
}

func (d *Driver) Start() error {
	return d.withVM(func(ctx context.Context, c *pve.Client, vm *proxmox.VirtualMachine) error {
		return c.StartVM(ctx, vm)
	})
}

// Stop shuts the guest down cleanly, falling back to a hard stop.
func (d *Driver) Stop() error {
	return d.withVM(func(ctx context.Context, c *pve.Client, vm *proxmox.VirtualMachine) error {
		if err := c.ShutdownVM(ctx, vm, gracefulShutdownTimeout); err != nil {
			log.Warnf("pvenode: graceful shutdown of VM %d failed (%v), forcing stop", d.VMID, err)
			return c.StopVM(ctx, vm)
		}
		return nil
	})
}

func (d *Driver) Kill() error {
	return d.withVM(func(ctx context.Context, c *pve.Client, vm *proxmox.VirtualMachine) error {
		return c.StopVM(ctx, vm)
	})
}

func (d *Driver) Restart() error {
	return d.withVM(func(ctx context.Context, c *pve.Client, vm *proxmox.VirtualMachine) error {
		task, err := vm.Reboot(ctx)
		if err != nil {
			return fmt.Errorf("rebooting VM %d: %w", d.VMID, err)
		}
		return c.WaitTask(ctx, task, gracefulShutdownTimeout)
	})
}

// GetIP re-queries the guest agent (DHCP leases change across reboots),
// falling back to the last known address if the agent is unreachable.
func (d *Driver) GetIP() (string, error) {
	ctx := context.Background()
	c, err := d.client()
	if err != nil {
		return "", err
	}
	vm, err := d.lookupVM(ctx, c)
	if err == nil && vm != nil {
		if ip, qerr := c.QueryAgentIP(ctx, vm); qerr == nil {
			d.IPAddress = ip
			return ip, nil
		}
	}
	if d.IPAddress != "" {
		return d.IPAddress, nil
	}
	return "", fmt.Errorf("no IP address known for machine %q yet", d.MachineName)
}

func (d *Driver) GetSSHHostname() (string, error) {
	return d.GetIP()
}

func (d *Driver) GetURL() (string, error) {
	if err := drivers.MustBeRunning(d); err != nil {
		return "", err
	}
	ip, err := d.GetIP()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("tcp://%s", net.JoinHostPort(ip, "2376")), nil
}
