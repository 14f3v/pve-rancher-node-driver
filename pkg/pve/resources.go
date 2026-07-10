package pve

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	proxmox "github.com/luthermonson/go-proxmox"
)

// VMResource is one row of GET /cluster/resources?type=vm.
type VMResource struct {
	VMID     uint64 `json:"vmid"`
	Name     string `json:"name"`
	Node     string `json:"node"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Template int    `json:"template"`
}

// StorageInfo is one row of GET /nodes/{node}/storage.
type StorageInfo struct {
	Name    string
	Type    string
	Content string
	Shared  bool
	Active  bool
}

type storageRow struct {
	Storage string `json:"storage"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Shared  int    `json:"shared"`
	Active  int    `json:"active"`
}

type networkRow struct {
	Iface string `json:"iface"`
	Type  string `json:"type"`
}

func nodeQemuPath(node string, vmid int) string {
	return fmt.Sprintf("/nodes/%s/qemu/%d", node, vmid)
}

func (c *Client) ListVMs(ctx context.Context) ([]VMResource, error) {
	var rows []VMResource
	if err := c.px.Get(ctx, "/cluster/resources?type=vm", &rows); err != nil {
		return nil, fmt.Errorf("listing cluster VMs: %w", err)
	}
	return rows, nil
}

func (c *Client) GetVM(ctx context.Context, node string, vmid int) (*proxmox.VirtualMachine, error) {
	n, err := c.px.Node(ctx, node)
	if err != nil {
		return nil, fmt.Errorf("getting PVE node %q: %w", node, err)
	}
	vm, err := n.VirtualMachine(ctx, vmid)
	if err != nil {
		return nil, fmt.Errorf("getting VM %d on node %q: %w", vmid, node, err)
	}
	return vm, nil
}

// ResolveTemplate finds the clone source. ref may be a numeric VMID or a
// template name (must be unique among templates).
func (c *Client) ResolveTemplate(ctx context.Context, ref string) (*proxmox.VirtualMachine, error) {
	rows, err := c.ListVMs(ctx)
	if err != nil {
		return nil, err
	}
	var match *VMResource
	var templateNames []string
	refVMID, refIsNumeric := 0, false
	if n, err := strconv.Atoi(ref); err == nil {
		refVMID, refIsNumeric = n, true
	}
	for i := range rows {
		row := &rows[i]
		if row.Type != "qemu" {
			continue
		}
		if row.Template == 1 {
			templateNames = append(templateNames, row.Name)
		}
		if (refIsNumeric && int(row.VMID) == refVMID) || (!refIsNumeric && row.Name == ref) {
			if match != nil {
				return nil, fmt.Errorf("template name %q is ambiguous (VMIDs %d and %d) — use the VMID instead", ref, match.VMID, row.VMID)
			}
			match = row
		}
	}
	if match == nil {
		sort.Strings(templateNames)
		return nil, fmt.Errorf("template %q not found; available templates: %s", ref, strings.Join(templateNames, ", "))
	}
	if match.Template != 1 {
		return nil, fmt.Errorf("VM %q (VMID %d) is not a template — convert it in PVE first (right-click > Convert to template)", match.Name, match.VMID)
	}
	return c.GetVM(ctx, match.Node, int(match.VMID))
}

// FindVMByNameAndTag is the orphan-recovery lookup used by Remove when the
// persisted VMID is missing (mid-create crash). Returns (nil, nil) when no
// VM matches. The tag match is mandatory: name alone must never be trusted.
func (c *Client) FindVMByNameAndTag(ctx context.Context, name, tag string) (*proxmox.VirtualMachine, error) {
	rows, err := c.ListVMs(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		row := &rows[i]
		if row.Type != "qemu" || row.Template == 1 || row.Name != name {
			continue
		}
		vm, err := c.GetVM(ctx, row.Node, int(row.VMID))
		if err != nil {
			if IsNotFoundErr(err) {
				continue
			}
			return nil, err
		}
		if vm.HasTag(tag) {
			return vm, nil
		}
	}
	return nil, nil
}

func (c *Client) NodeStorages(ctx context.Context, node string) ([]StorageInfo, error) {
	var rows []storageRow
	if err := c.px.Get(ctx, "/nodes/"+node+"/storage", &rows); err != nil {
		return nil, fmt.Errorf("listing storages on node %q: %w", node, err)
	}
	out := make([]StorageInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, StorageInfo{
			Name: r.Storage, Type: r.Type, Content: r.Content,
			Shared: r.Shared == 1, Active: r.Active == 1,
		})
	}
	return out, nil
}

func (c *Client) NodeBridges(ctx context.Context, node string) ([]string, error) {
	var rows []networkRow
	if err := c.px.Get(ctx, "/nodes/"+node+"/network", &rows); err != nil {
		return nil, fmt.Errorf("listing networks on node %q: %w", node, err)
	}
	var bridges []string
	for _, r := range rows {
		if r.Type == "bridge" {
			bridges = append(bridges, r.Iface)
		}
	}
	sort.Strings(bridges)
	return bridges, nil
}
