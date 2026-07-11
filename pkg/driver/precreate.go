package driver

import (
	"context"
	"fmt"
	"strings"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/log"

	"github.com/14f3v/pve-rancher-node-driver/pkg/pve"
	"github.com/14f3v/pve-rancher-node-driver/pkg/validate"
)

type placement struct {
	Template    *proxmox.VirtualMachine
	TargetNode  string
	BootDiskKey string
	TemplateGB  int
}

// PreCreateCheck validates everything BEFORE any VM exists, so
// misconfiguration surfaces as one actionable error in the Rancher
// provisioning log instead of a half-created VM.
func (d *Driver) PreCreateCheck() error {
	ctx := context.Background()
	c, err := d.client()
	if err != nil {
		return err
	}

	if err := validate.ValidateMachineName(d.MachineName); err != nil {
		return err
	}

	major, err := c.PVEMajorVersion(ctx)
	if err != nil {
		return err
	}
	log.Infof("pvenode: connected to PVE %d.x at %s", major, d.URL)

	// Token privilege check first: privsep tokens without ACLs fail
	// SILENTLY (empty lists everywhere), so every later error would lie.
	if perms, err := c.TokenPermissions(ctx); err != nil {
		log.Warnf("pvenode: could not query token permissions (%v) — skipping privilege pre-check", err)
	} else {
		checks := validate.RequiredChecks(major, d.Storage, d.Bridge, d.ResourcePool)
		if missing := validate.Missing(perms, checks); len(missing) > 0 {
			return fmt.Errorf(
				"the API token is missing privileges: %s. Grant them via a role on the token itself "+
					"(privsep tokens do NOT inherit the user's permissions) or recreate the token with privsep=0. "+
					"See the driver README for ready-made PVE %d role definitions",
				strings.Join(missing, ", "), major)
		}
	}

	if _, err := d.resolvePlacement(ctx, c); err != nil {
		return err
	}
	return nil
}

// resolvePlacement resolves and validates the template, target node,
// storage, bridge and disk sizing. Reused by Create.
func (d *Driver) resolvePlacement(ctx context.Context, c *pve.Client) (*placement, error) {
	tmpl, err := c.ResolveTemplate(ctx, d.TemplateRef)
	if err != nil {
		return nil, err
	}
	if err := validate.EnsureTemplate(tmpl); err != nil {
		return nil, err
	}
	cfg := tmpl.VirtualMachineConfig
	if _, ok := validate.CloudInitDrive(cfg); !ok {
		return nil, fmt.Errorf(
			"template %q has no cloud-init drive — without one, user/SSH-key/network settings are never "+
				"delivered to the guest. Add one in PVE: qm set %d --ide2 <storage>:cloudinit",
			tmpl.Name, int(tmpl.VMID))
	}
	if _, err := validate.NICMAC(cfg, "net0"); err != nil {
		return nil, fmt.Errorf("template %q: %w", tmpl.Name, err)
	}

	bootKey, templateGB, bootVal, err := validate.BootDisk(cfg)
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", tmpl.Name, err)
	}
	if err := validate.ValidateDiskSize(d.DiskSizeGB, templateGB); err != nil {
		return nil, err
	}

	targetNode := d.NodeName
	if targetNode == "" {
		targetNode = tmpl.Node
	}

	storages, err := c.NodeStorages(ctx, targetNode)
	if err != nil {
		return nil, err
	}
	templateStorageID, _, _ := strings.Cut(bootVal, ":")
	var templateStorage, requestedStorage *pve.StorageInfo
	var names []string
	for i := range storages {
		st := &storages[i]
		names = append(names, st.Name)
		if st.Name == templateStorageID {
			templateStorage = st
		}
		if d.Storage != "" && st.Name == d.Storage {
			requestedStorage = st
		}
	}
	if d.Storage != "" {
		if requestedStorage == nil {
			return nil, fmt.Errorf("storage %q not found on node %q; available: %s",
				d.Storage, targetNode, strings.Join(names, ", "))
		}
		if !strings.Contains(requestedStorage.Content, "images") {
			return nil, fmt.Errorf("storage %q on node %q cannot hold VM disks (content=%q)",
				d.Storage, targetNode, requestedStorage.Content)
		}
	}

	templateShared := templateStorage != nil && templateStorage.Shared
	if err := validate.ValidateCloneMode(d.LinkedClone, d.Storage, d.NodeName, tmpl.Node, templateShared); err != nil {
		return nil, err
	}
	if d.LinkedClone && templateStorage != nil {
		if err := validate.LinkedCloneOK(bootVal, templateStorage.Type); err != nil {
			return nil, err
		}
	}

	if d.Bridge != "" {
		bridges, err := c.NodeBridges(ctx, targetNode)
		if err != nil {
			return nil, err
		}
		found := false
		for _, b := range bridges {
			if b == d.Bridge {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("bridge %q not found on node %q; available bridges: %s",
				d.Bridge, targetNode, strings.Join(bridges, ", "))
		}
	}

	return &placement{
		Template:    tmpl,
		TargetNode:  targetNode,
		BootDiskKey: bootKey,
		TemplateGB:  templateGB,
	}, nil
}
