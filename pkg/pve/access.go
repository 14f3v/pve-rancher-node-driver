package pve

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	proxmox "github.com/luthermonson/go-proxmox"
)

func (c *Client) PVEMajorVersion(ctx context.Context) (int, error) {
	v, err := c.px.Version(ctx)
	if err != nil {
		return 0, fmt.Errorf("querying PVE version (is the URL correct and the token valid?): %w", err)
	}
	majorStr, _, _ := strings.Cut(v.Release, ".")
	major, err := strconv.Atoi(majorStr)
	if err != nil {
		return 0, fmt.Errorf("unexpected PVE release string %q", v.Release)
	}
	return major, nil
}

// TokenPermissions returns the effective permissions of the calling token.
// A privsep token with no ACLs of its own returns an EMPTY map here — and
// silently empty lists from every other endpoint, which is why the caller
// checks this first.
func (c *Client) TokenPermissions(ctx context.Context) (proxmox.Permissions, error) {
	return c.px.Permissions(ctx, nil)
}
