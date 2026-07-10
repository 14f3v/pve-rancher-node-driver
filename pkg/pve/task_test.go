package pve

import (
	"context"
	"testing"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

func newTestClient(t *testing.T, s *pvetest.Server) *Client {
	t.Helper()
	c, err := New(Config{URL: s.URL(), TokenID: "u@pve!t", TokenSecret: "x"})
	require.NoError(t, err)
	return c
}

func TestWaitTaskOK(t *testing.T) {
	s := pvetest.New(t)
	upid := s.OKTask("pve1")
	c := newTestClient(t, s)

	task := proxmox.NewTask(proxmox.UPID(upid), c.px)
	assert.NoError(t, c.WaitTask(context.Background(), task, 5*time.Second))
}

func TestWaitTaskFailed(t *testing.T) {
	s := pvetest.New(t)
	upid := s.FailedTask("pve1", "clone failed: no space left")
	c := newTestClient(t, s)

	task := proxmox.NewTask(proxmox.UPID(upid), c.px)
	err := c.WaitTask(context.Background(), task, 5*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no space left")
}

func TestWaitTaskNil(t *testing.T) {
	s := pvetest.New(t)
	c := newTestClient(t, s)
	assert.NoError(t, c.WaitTask(context.Background(), nil, time.Second))
}
