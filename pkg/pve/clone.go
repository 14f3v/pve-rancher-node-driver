package pve

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/log"
)

type CloneSpec struct {
	Name       string
	TargetNode string
	Storage    string
	Linked     bool
	Pool       string
	VMIDLo     int
	VMIDHi     int
	Tags       []string
}

const (
	cloneAttempts    = 5
	cloneTaskTimeout = 15 * time.Minute // full clones of big templates are slow
	tagTaskTimeout   = 2 * time.Minute
)

// CloneFromTemplate clones tmpl into a new VM and stamps the ownership
// tags. VMIDs are picked RANDOMLY in [VMIDLo, VMIDHi]: /cluster/nextid is
// not atomic, and Rancher creates pool nodes in parallel — random+retry is
// the accepted fix. Retried errors: VMID conflicts (new random ID) and
// template/cfs lock contention (same ID, backoff with jitter).
func (c *Client) CloneFromTemplate(ctx context.Context, tmpl *proxmox.VirtualMachine, spec CloneSpec) (*proxmox.VirtualMachine, error) {
	targetNode := spec.TargetNode
	if targetNode == "" {
		targetNode = tmpl.Node
	}

	var vmid int
	err := Retry(ctx, cloneAttempts, 2*time.Second,
		func(err error) bool { return IsVMIDConflict(err) || IsLockErr(err) || IsTransient(err) },
		func() error {
			id, err := c.randomFreeVMID(ctx, spec.VMIDLo, spec.VMIDHi)
			if err != nil {
				return err
			}
			log.Infof("pvenode: cloning template %d -> VM %d (%s) on node %s", int(tmpl.VMID), id, spec.Name, targetNode)
			opts := &proxmox.VirtualMachineCloneOptions{
				NewID: id,
				Name:  spec.Name,
				Full:  proxmox.IntOrBool(!spec.Linked),
				Pool:  spec.Pool,
			}
			if spec.Storage != "" {
				opts.Storage = spec.Storage
			}
			if spec.TargetNode != "" && spec.TargetNode != tmpl.Node {
				opts.Target = spec.TargetNode
			}
			newid, task, err := tmpl.Clone(ctx, opts)
			if err != nil {
				return err
			}
			if err := c.WaitTask(ctx, task, cloneTaskTimeout); err != nil {
				// Partial VM possible after a failed clone task: best-effort cleanup.
				c.bestEffortDelete(ctx, targetNode, newid)
				return err
			}
			vmid = newid
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("cloning template %q: %w", tmpl.Name, err)
	}

	vm, err := c.GetVM(ctx, targetNode, vmid)
	if err != nil {
		return nil, fmt.Errorf("clone succeeded but VM %d is not readable: %w", vmid, err)
	}

	if len(spec.Tags) > 0 {
		task, err := vm.Config(ctx, proxmox.VirtualMachineOption{
			Name: "tags", Value: strings.Join(spec.Tags, ";"),
		})
		if err == nil {
			err = c.WaitTask(ctx, task, tagTaskTimeout)
		}
		if err != nil {
			c.bestEffortDelete(ctx, targetNode, vmid)
			return nil, fmt.Errorf("tagging VM %d: %w", vmid, err)
		}
	}
	return vm, nil
}

// randomFreeVMID picks a random VMID in [lo, hi] not currently used by any
// VM or template. A racing allocation elsewhere still surfaces as a clone
// "config file already exists" error, which the caller retries.
func (c *Client) randomFreeVMID(ctx context.Context, lo, hi int) (int, error) {
	rows, err := c.ListVMs(ctx)
	if err != nil {
		return 0, err
	}
	used := make(map[int]bool, len(rows))
	for _, r := range rows {
		used[int(r.VMID)] = true
	}
	span := hi - lo + 1
	free := span - countUsedIn(used, lo, hi)
	if free <= 0 {
		return 0, fmt.Errorf("no free VMID in range %d-%d (%d in use) — widen --pvenode-vmid-range", lo, hi, span)
	}
	for {
		id := lo + rand.Intn(span)
		if !used[id] {
			return id, nil
		}
	}
}

func countUsedIn(used map[int]bool, lo, hi int) int {
	n := 0
	for id := range used {
		if id >= lo && id <= hi {
			n++
		}
	}
	return n
}

// bestEffortDelete removes a partial VM after a failed create step; errors
// are logged, not returned (the original failure matters more).
func (c *Client) bestEffortDelete(ctx context.Context, node string, vmid int) {
	if vmid == 0 {
		return
	}
	vm, err := c.GetVM(ctx, node, vmid)
	if err != nil {
		if !IsNotFoundErr(err) {
			log.Warnf("pvenode: cleanup: could not read partial VM %d: %v", vmid, err)
		}
		return
	}
	task, err := vm.Delete(ctx, &proxmox.VirtualMachineDeleteOptions{
		Purge:                    proxmox.IntOrBool(true),
		DestroyUnreferencedDisks: proxmox.IntOrBool(true),
	})
	if err == nil {
		err = c.WaitTask(ctx, task, 5*time.Minute)
	}
	if err != nil {
		log.Warnf("pvenode: cleanup of partial VM %d failed (delete it manually in PVE): %v", vmid, err)
	}
}
