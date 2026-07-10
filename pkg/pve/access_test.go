package pve

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

// newTestClient builds a Client wired to the given pvetest fake server.
func newTestClient(t *testing.T, s *pvetest.Server) *Client {
	t.Helper()
	c, err := New(Config{URL: s.URL(), TokenID: "u@pve!t", TokenSecret: "x"})
	require.NoError(t, err)
	return c
}

func TestPVEMajorVersion(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/version", 200, map[string]string{"release": "9.2", "version": "9.2.1"})
	c := newTestClient(t, s)

	major, err := c.PVEMajorVersion(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 9, major)
}

func TestTokenPermissions(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/access/permissions", 200, map[string]map[string]int{
		"/vms":           {"VM.Clone": 1, "VM.Allocate": 1},
		"/storage/local": {"Datastore.AllocateSpace": 1},
	})
	c := newTestClient(t, s)

	perms, err := c.TokenPermissions(context.Background())
	require.NoError(t, err)
	assert.True(t, bool(perms["/vms"]["VM.Clone"]))
}
