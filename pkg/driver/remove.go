package driver

import (
	"context"
	"fmt"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/log"
)

// Remove deletes the machine's VM with disk purge. Idempotent: an
// already-gone VM is success (Rancher's rm job may run after a failed
// create already cleaned up). The ownership-tag guard makes the
// name-fallback lookup safe against VMID/name reuse.
func (d *Driver) Remove() error {
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
		log.Infof("pvenode: machine %q (VMID %d) is already gone, nothing to remove", d.MachineName, d.VMID)
		return nil
	}
	if !vm.HasTag(MachineTag) {
		return fmt.Errorf(
			"refusing to delete VM %d (%s): it does not carry the %q ownership tag — "+
				"it may have been replaced or modified outside the driver",
			int(vm.VMID), vm.Name, MachineTag)
	}

	if vm.Status == proxmox.StatusVirtualMachineRunning {
		if err := c.ShutdownVM(ctx, vm, gracefulShutdownTimeout); err != nil {
			log.Warnf("pvenode: graceful shutdown of VM %d failed (%v), forcing stop", int(vm.VMID), err)
			if err := c.StopVM(ctx, vm); err != nil {
				return fmt.Errorf("could not stop VM %d before removal: %w", int(vm.VMID), err)
			}
		}
	}

	log.Infof("pvenode: deleting VM %d (%s) with disk purge", int(vm.VMID), vm.Name)
	return c.DeleteVM(ctx, vm)
}
