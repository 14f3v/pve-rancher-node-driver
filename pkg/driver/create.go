package driver

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/log"
	"github.com/rancher/machine/libmachine/ssh"

	"github.com/14f3v/pve-rancher-node-driver/pkg/pve"
)

// Create clones the template, configures it via PVE's built-in cloud-init
// fields, starts it, and waits for the MAC-matched agent IP. On failure it
// removes the half-created VM (unless --pvenode-keep-on-failure).
func (d *Driver) Create() error {
	ctx := context.Background()
	c, err := d.client()
	if err != nil {
		return err
	}

	if err := ssh.GenerateSSHKey(d.GetSSHKeyPath()); err != nil {
		return fmt.Errorf("generating SSH key: %w", err)
	}
	pub, err := os.ReadFile(d.GetSSHKeyPath() + ".pub")
	if err != nil {
		return fmt.Errorf("reading generated SSH public key: %w", err)
	}

	place, err := d.resolvePlacement(ctx, c)
	if err != nil {
		return err
	}

	lo, hi, err := parseVMIDRange(d.VMIDRange)
	if err != nil {
		return err
	}
	tags := []string{MachineTag}
	if d.ExtraTags != "" {
		for _, tag := range strings.Split(d.ExtraTags, ",") {
			if tag = strings.TrimSpace(tag); tag != "" {
				tags = append(tags, tag)
			}
		}
	}

	vm, err := c.CloneFromTemplate(ctx, place.Template, pve.CloneSpec{
		Name:       d.MachineName,
		TargetNode: d.NodeName,
		Storage:    d.Storage,
		Linked:     d.LinkedClone,
		Pool:       d.ResourcePool,
		VMIDLo:     lo,
		VMIDHi:     hi,
		Tags:       tags,
	})
	if err != nil {
		return err
	}
	d.VMID = int(vm.VMID)
	d.PVENode = place.TargetNode
	log.Infof("pvenode: created VM %d for machine %s", d.VMID, d.MachineName)

	if err := d.provision(ctx, c, vm, place, strings.TrimSpace(string(pub))); err != nil {
		if d.KeepOnFailure {
			log.Warnf("pvenode: create failed but VM %d is kept (--pvenode-keep-on-failure): %v", d.VMID, err)
			return err
		}
		log.Warnf("pvenode: create failed, removing half-created VM %d: %v", d.VMID, err)
		if cerr := d.Remove(); cerr != nil {
			return fmt.Errorf("%w (cleanup also failed: %v — delete VM %d manually in PVE)", err, cerr, d.VMID)
		}
		return err
	}
	return nil
}

func (d *Driver) provision(ctx context.Context, c *pve.Client, vm *proxmox.VirtualMachine, place *placement, pubKey string) error {
	opts := []proxmox.VirtualMachineOption{
		{Name: "cores", Value: d.Cores},
		{Name: "memory", Value: d.MemoryMB},
		{Name: "agent", Value: "1"},
		{Name: "onboot", Value: boolToInt(d.OnBoot)},
		{Name: "ciuser", Value: d.GetSSHUsername()},
		{Name: "sshkeys", Value: proxmox.EncodeSSHKeys(pubKey)},
		{Name: "ipconfig0", Value: "ip=dhcp"},
	}
	if d.CPUType != "" {
		opts = append(opts, proxmox.VirtualMachineOption{Name: "cpu", Value: d.CPUType})
	}
	if d.CIPassword != "" {
		opts = append(opts, proxmox.VirtualMachineOption{Name: "cipassword", Value: d.CIPassword})
	}
	if d.Nameserver != "" {
		opts = append(opts, proxmox.VirtualMachineOption{Name: "nameserver", Value: d.Nameserver})
	}
	if d.Searchdomain != "" {
		opts = append(opts, proxmox.VirtualMachineOption{Name: "searchdomain", Value: d.Searchdomain})
	}
	if d.Bridge != "" {
		net0 := "virtio,bridge=" + d.Bridge
		if d.VLANTag > 0 {
			net0 += ",tag=" + strconv.Itoa(d.VLANTag)
		}
		opts = append(opts, proxmox.VirtualMachineOption{Name: "net0", Value: net0})
	}

	log.Infof("pvenode: configuring VM %d (cores=%d memory=%dMB)", d.VMID, d.Cores, d.MemoryMB)
	if err := c.ApplyConfig(ctx, vm, opts); err != nil {
		return err
	}

	if d.DiskSizeGB > place.TemplateGB {
		log.Infof("pvenode: growing disk %s to %dG", place.BootDiskKey, d.DiskSizeGB)
		if err := c.ResizeVMDisk(ctx, vm, place.BootDiskKey, d.DiskSizeGB); err != nil {
			return err
		}
	}

	log.Infof("pvenode: starting VM %d", d.VMID)
	if err := c.StartVM(ctx, vm); err != nil {
		return err
	}

	// Refresh the config: net0 (and its MAC) may have been rewritten above.
	if err := vm.Ping(ctx); err != nil {
		return fmt.Errorf("refreshing VM %d config: %w", d.VMID, err)
	}

	timeout := time.Duration(d.AgentTimeout) * time.Second
	log.Infof("pvenode: waiting up to %s for the guest agent to report an IP", timeout)
	ip, err := c.WaitForIP(ctx, vm, d.SSHPort, timeout)
	if err != nil {
		return err
	}
	d.IPAddress = ip
	log.Infof("pvenode: VM %d is reachable at %s — handing off to SSH provisioning", d.VMID, ip)
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
