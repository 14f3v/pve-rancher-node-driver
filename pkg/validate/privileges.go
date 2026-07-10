// Package validate holds the pure validation logic behind PreCreateCheck:
// privilege sets, template requirements and clone-mode rules.
package validate

import (
	proxmox "github.com/luthermonson/go-proxmox"
)

type PrivCheck struct {
	Path string
	Priv string
}

// RequiredChecks returns the privileges the driver's token needs.
// PVE 9 removed VM.Monitor; guest-agent reads need VM.GuestAgent.Audit.
// SDN.Use on the bridge is required since PVE 8 for privsep tokens.
func RequiredChecks(pveMajor int, storage, bridge string, pool string) []PrivCheck {
	vmPrivs := []string{
		"VM.Clone", "VM.Allocate", "VM.Audit", "VM.PowerMgmt",
		"VM.Config.Disk", "VM.Config.CPU", "VM.Config.Memory",
		"VM.Config.Network", "VM.Config.Cloudinit", "VM.Config.Options",
	}
	if pveMajor >= 9 {
		vmPrivs = append(vmPrivs, "VM.GuestAgent.Audit")
	} else {
		vmPrivs = append(vmPrivs, "VM.Monitor")
	}

	var checks []PrivCheck
	for _, p := range vmPrivs {
		checks = append(checks, PrivCheck{Path: "/vms", Priv: p})
	}
	storagePath := "/storage"
	if storage != "" {
		storagePath = "/storage/" + storage
	}
	checks = append(checks,
		PrivCheck{Path: storagePath, Priv: "Datastore.AllocateSpace"},
		PrivCheck{Path: storagePath, Priv: "Datastore.Audit"},
	)
	if bridge != "" {
		checks = append(checks, PrivCheck{Path: "/sdn/zones/localnetwork/" + bridge, Priv: "SDN.Use"})
	}
	if pool != "" {
		checks = append(checks, PrivCheck{Path: "/pool/" + pool, Priv: "Pool.Allocate"})
	}
	return checks
}

// Missing returns the privileges not granted at ANY path. The scan is
// deliberately lenient (constraint: PVE ACL propagation and pool
// inheritance cannot be mirrored exactly from /access/permissions output);
// it exists to catch the common failure of a privsep token with no ACLs,
// which otherwise fails silently with empty API responses.
func Missing(perms proxmox.Permissions, checks []PrivCheck) []string {
	granted := map[string]bool{}
	for _, privs := range perms {
		for priv, ok := range privs {
			if bool(ok) {
				granted[priv] = true
			}
		}
	}
	var missing []string
	seen := map[string]bool{}
	for _, c := range checks {
		if !granted[c.Priv] && !seen[c.Priv] {
			missing = append(missing, c.Priv)
			seen[c.Priv] = true
		}
	}
	return missing
}
