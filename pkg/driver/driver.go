// Package driver implements the pvenode rancher-machine driver for Proxmox VE.
package driver

import (
	"context"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/drivers"

	"github.com/14f3v/pve-rancher-node-driver/pkg/pve"
)

const (
	// MachineTag marks every VM this driver creates. Remove refuses to
	// delete a VM without it (guards against VMID/name reuse).
	MachineTag = "rancher-pvenode"

	driverName = "pvenode"
)

type Driver struct {
	*drivers.BaseDriver

	// Cloud credential (Rancher fields: url, tokenId, tokenSecret, insecureTls, caCert)
	URL         string
	TokenID     string
	TokenSecret string
	InsecureTLS bool
	CACert      string // PEM content, optional

	// Machine config
	TemplateRef   string // template VM name or numeric VMID
	Storage       string // full clones only
	NodeName      string // target PVE node; empty = template's node
	LinkedClone   bool   // default false = full clone
	Cores         int
	MemoryMB      int
	CPUType       string
	DiskSizeGB    int // 0 = keep template size
	Bridge        string
	VLANTag       int
	ResourcePool  string
	AgentTimeout  int // seconds to wait for the guest agent to report an IP
	VMIDRange     string
	CIPassword    string
	Nameserver    string
	Searchdomain  string
	OnBoot        bool
	ExtraTags     string // comma-separated
	KeepOnFailure bool

	// Lifecycle state — exported so it survives the JSON round-trip
	// between driver processes (each call is a fresh process).
	VMID    int
	PVENode string
}

func NewDriver(machineName, storePath string) *Driver {
	return &Driver{
		BaseDriver: &drivers.BaseDriver{
			MachineName: machineName,
			StorePath:   storePath,
		},
	}
}

// DriverName is the plugin name; the binary must be named
// docker-machine-driver-pvenode to match.
func (d *Driver) DriverName() string {
	return driverName
}

// client builds a PVE API client from the stored credential fields.
// Never cached in a struct field: each lifecycle call runs in a fresh process.
func (d *Driver) client() (*pve.Client, error) {
	return pve.New(pve.Config{
		URL:         d.URL,
		TokenID:     d.TokenID,
		TokenSecret: d.TokenSecret,
		InsecureTLS: d.InsecureTLS,
		CACertPEM:   d.CACert,
	})
}

// Compile-time interface assertion: main.go hands *Driver to
// plugin.RegisterDriver(drivers.Driver), so the full interface must exist.
// Every drivers.Driver method (Create, Remove, GetIP, Start, Stop, ...) is
// now implemented for real — no stubs remain.
var _ drivers.Driver = (*Driver)(nil)

// lookupVM finds this machine's VM: persisted node+VMID first, then a
// cluster-wide VMID search (VM may have been migrated), then — for the
// mid-create-crash case where the VMID never got persisted — by machine
// name guarded by the ownership tag. Returns (nil, nil) when the VM is
// gone: callers treat that as "already removed".
func (d *Driver) lookupVM(ctx context.Context, c *pve.Client) (*proxmox.VirtualMachine, error) {
	if d.VMID != 0 {
		if d.PVENode != "" {
			vm, err := c.GetVM(ctx, d.PVENode, d.VMID)
			if err == nil {
				return vm, nil
			}
			if !pve.IsNotFoundErr(err) {
				return nil, err
			}
		}
		rows, err := c.ListVMs(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if int(row.VMID) == d.VMID && row.Type == "qemu" {
				vm, err := c.GetVM(ctx, row.Node, d.VMID)
				if err != nil {
					// Deleted in the window between the list and this fetch:
					// treat as gone so GetState/Remove stay idempotent.
					if pve.IsNotFoundErr(err) {
						return nil, nil
					}
					return nil, err
				}
				return vm, nil
			}
		}
		return nil, nil
	}
	return c.FindVMByNameAndTag(ctx, d.MachineName, MachineTag)
}
