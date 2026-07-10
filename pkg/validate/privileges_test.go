package validate

import (
	"testing"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
)

func perms(m map[string]map[string]bool) proxmox.Permissions {
	out := proxmox.Permissions{}
	for path, privs := range m {
		p := proxmox.Permission{}
		for k, v := range privs {
			p[k] = proxmox.IntOrBool(v)
		}
		out[path] = p
	}
	return out
}

func TestRequiredChecksVersionAware(t *testing.T) {
	privsOf := func(checks []PrivCheck) []string {
		var out []string
		for _, c := range checks {
			out = append(out, c.Priv)
		}
		return out
	}
	v8 := RequiredChecks(8, "local", "vmbr0", "")
	v9 := RequiredChecks(9, "local", "vmbr0", "")
	assert.Contains(t, privsOf(v8), "VM.Monitor") // PVE 8: agent reads
	assert.NotContains(t, privsOf(v8), "VM.GuestAgent.Audit")
	assert.Contains(t, privsOf(v9), "VM.GuestAgent.Audit") // PVE 9 replacement
	assert.NotContains(t, privsOf(v9), "VM.Monitor")
	assert.Contains(t, privsOf(v9), "SDN.Use")
}

func TestRequiredChecksPool(t *testing.T) {
	checks := RequiredChecks(9, "local", "vmbr0", "rancher-pool")
	found := false
	for _, c := range checks {
		if c.Priv == "Pool.Allocate" && c.Path == "/pool/rancher-pool" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestMissingLenientScan(t *testing.T) {
	// The check is advisory: a privilege granted at ANY path counts, because
	// PVE's ACL model (propagation, pools) is too flexible to mirror
	// exactly. The target bug class is the zero-ACL privsep token, which
	// yields an empty permissions map and silent empty API responses.
	p := perms(map[string]map[string]bool{
		"/pool/rancher-pool": {"VM.Clone": true, "VM.Allocate": true},
	})
	missing := Missing(p, []PrivCheck{
		{Path: "/vms", Priv: "VM.Clone"},     // granted elsewhere → OK
		{Path: "/vms", Priv: "VM.PowerMgmt"}, // granted nowhere → missing
	})
	assert.Equal(t, []string{"VM.PowerMgmt"}, missing)
}

func TestMissingEmptyPerms(t *testing.T) {
	missing := Missing(proxmox.Permissions{}, RequiredChecks(9, "local", "vmbr0", ""))
	assert.NotEmpty(t, missing)
}
