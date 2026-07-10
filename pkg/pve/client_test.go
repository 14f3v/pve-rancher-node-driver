package pve

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

func TestNewAppendsAPIPath(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/version", 200, map[string]string{"release": "9.2", "version": "9.2.1"})

	c, err := New(Config{URL: s.URL(), TokenID: "u@pve!t", TokenSecret: "x"})
	require.NoError(t, err)

	v, err := c.px.Version(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "9.2", v.Release)
}

func TestNewRejectsBadCACert(t *testing.T) {
	_, err := New(Config{URL: "https://x", TokenID: "u@pve!t", TokenSecret: "x", CACertPEM: "not-pem"})
	assert.Error(t, err)
}
