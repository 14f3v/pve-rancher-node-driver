package driver

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeOpts implements drivers.DriverOptions for tests.
type fakeOpts map[string]interface{}

func (f fakeOpts) String(key string) string {
	if v, ok := f[key].(string); ok {
		return v
	}
	return ""
}
func (f fakeOpts) StringSlice(key string) []string {
	if v, ok := f[key].([]string); ok {
		return v
	}
	return nil
}
func (f fakeOpts) Int(key string) int {
	if v, ok := f[key].(int); ok {
		return v
	}
	return 0
}
func (f fakeOpts) Bool(key string) bool {
	if v, ok := f[key].(bool); ok {
		return v
	}
	return false
}

func validOpts() fakeOpts {
	return fakeOpts{
		"pvenode-url":          "https://pve.example.com:8006",
		"pvenode-token-id":     "rancher@pve!machine",
		"pvenode-token-secret": "s3cret",
		"pvenode-template":     "ubuntu-2404-tmpl",
	}
}

func TestSetConfigFromFlagsDefaults(t *testing.T) {
	d := NewDriver("m", "/tmp/s")
	require.NoError(t, d.SetConfigFromFlags(validOpts()))

	assert.Equal(t, 2, d.Cores)
	assert.Equal(t, 4096, d.MemoryMB)
	assert.Equal(t, 300, d.AgentTimeout)
	assert.Equal(t, "10000-19999", d.VMIDRange)
	assert.Equal(t, "rancher", d.GetSSHUsername())
	assert.False(t, d.LinkedClone)
}

func TestSetConfigFromFlagsRequired(t *testing.T) {
	for _, missing := range []string{
		"pvenode-url", "pvenode-token-id", "pvenode-token-secret", "pvenode-template",
	} {
		opts := validOpts()
		delete(opts, missing)
		d := NewDriver("m", "/tmp/s")
		err := d.SetConfigFromFlags(opts)
		require.Error(t, err, "expected error when %s missing", missing)
		assert.Contains(t, err.Error(), missing)
	}
}

func TestSetConfigFromFlagsBadVMIDRange(t *testing.T) {
	for _, bad := range []string{"abc", "5-4", "10", "99-200", "100-1000000000"} {
		opts := validOpts()
		opts["pvenode-vmid-range"] = bad
		d := NewDriver("m", "/tmp/s")
		assert.Error(t, d.SetConfigFromFlags(opts), "range %q should be rejected", bad)
	}
}

func TestSetConfigFromFlagsBadVLAN(t *testing.T) {
	opts := validOpts()
	opts["pvenode-vlan"] = 5000
	d := NewDriver("m", "/tmp/s")
	assert.Error(t, d.SetConfigFromFlags(opts))
}

func TestParseVMIDRange(t *testing.T) {
	lo, hi, err := parseVMIDRange("10000-19999")
	require.NoError(t, err)
	assert.Equal(t, 10000, lo)
	assert.Equal(t, 19999, hi)
}

func TestCreateFlagsAllPrefixed(t *testing.T) {
	d := NewDriver("m", "/tmp/s")
	for _, f := range d.GetCreateFlags() {
		assert.Contains(t, f.String(), "pvenode-", "flag %q must carry the driver prefix", f.String())
	}
}
