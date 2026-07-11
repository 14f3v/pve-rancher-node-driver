package pve

import (
	"context"
	"fmt"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
)

const (
	configTaskTimeout = 2 * time.Minute
	resizeTaskTimeout = 5 * time.Minute
	startTaskTimeout  = 2 * time.Minute
	stopTaskTimeout   = 2 * time.Minute
	deleteTaskTimeout = 10 * time.Minute
	deleteAttempts    = 5
)

func (c *Client) ApplyConfig(ctx context.Context, vm *proxmox.VirtualMachine, opts []proxmox.VirtualMachineOption) error {
	task, err := vm.Config(ctx, opts...)
	if err != nil {
		return fmt.Errorf("configuring VM %d: %w", int(vm.VMID), err)
	}
	if err := c.WaitTask(ctx, task, configTaskTimeout); err != nil {
		return fmt.Errorf("configuring VM %d: %w", int(vm.VMID), err)
	}
	return nil
}

func (c *Client) ResizeVMDisk(ctx context.Context, vm *proxmox.VirtualMachine, disk string, sizeGB int) error {
	task, err := vm.ResizeDisk(ctx, disk, fmt.Sprintf("%dG", sizeGB))
	if err != nil {
		return fmt.Errorf("resizing disk %s of VM %d to %dG: %w", disk, int(vm.VMID), sizeGB, err)
	}
	if err := c.WaitTask(ctx, task, resizeTaskTimeout); err != nil {
		return fmt.Errorf("resizing disk %s of VM %d: %w", disk, int(vm.VMID), err)
	}
	return nil
}

func (c *Client) StartVM(ctx context.Context, vm *proxmox.VirtualMachine) error {
	task, err := vm.Start(ctx)
	if err != nil {
		return fmt.Errorf("starting VM %d: %w", int(vm.VMID), err)
	}
	return c.WaitTask(ctx, task, startTaskTimeout)
}

// ShutdownVM asks the guest to shut down cleanly and waits up to timeout.
func (c *Client) ShutdownVM(ctx context.Context, vm *proxmox.VirtualMachine, timeout time.Duration) error {
	task, err := vm.Shutdown(ctx)
	if err != nil {
		return fmt.Errorf("shutting down VM %d: %w", int(vm.VMID), err)
	}
	return c.WaitTask(ctx, task, timeout)
}

// StopVM is a hard power-off.
func (c *Client) StopVM(ctx context.Context, vm *proxmox.VirtualMachine) error {
	task, err := vm.Stop(ctx)
	if err != nil {
		return fmt.Errorf("stopping VM %d: %w", int(vm.VMID), err)
	}
	return c.WaitTask(ctx, task, stopTaskTimeout)
}

// DeleteVM destroys the VM with disk purge, retrying lock contention.
func (c *Client) DeleteVM(ctx context.Context, vm *proxmox.VirtualMachine) error {
	return Retry(ctx, deleteAttempts, 2*time.Second,
		func(err error) bool { return IsLockErr(err) || IsTransient(err) },
		func() error {
			task, err := vm.Delete(ctx, &proxmox.VirtualMachineDeleteOptions{
				Purge:                    proxmox.IntOrBool(true),
				DestroyUnreferencedDisks: proxmox.IntOrBool(true),
			})
			if err != nil {
				return err
			}
			return c.WaitTask(ctx, task, deleteTaskTimeout)
		})
}
