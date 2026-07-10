package validate

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateCloneMode(t *testing.T) {
	// linked + explicit storage: PVE silently ignores storage on linked clones — hard error.
	assert.Error(t, ValidateCloneMode(true, "local-lvm", "", "pve1", false))
	// linked without storage: fine.
	assert.NoError(t, ValidateCloneMode(true, "", "", "pve1", false))
	// cross-node clone needs shared template storage.
	assert.Error(t, ValidateCloneMode(false, "", "pve2", "pve1", false))
	assert.NoError(t, ValidateCloneMode(false, "", "pve2", "pve1", true))
	// same node explicit: fine.
	assert.NoError(t, ValidateCloneMode(false, "big-storage", "pve1", "pve1", false))
}

func TestLinkedCloneOK(t *testing.T) {
	assert.NoError(t, LinkedCloneOK("local-lvm:base-9000-disk-0,size=20G", "lvmthin"))
	assert.NoError(t, LinkedCloneOK("tank:base-9000-disk-0,size=20G", "zfspool"))
	assert.NoError(t, LinkedCloneOK("local:9000/base-9000-disk-0.qcow2,size=20G", "dir"))
	// raw file on dir storage cannot back a linked clone
	assert.Error(t, LinkedCloneOK("local:9000/base-9000-disk-0.raw,size=20G", "dir"))
	assert.Error(t, LinkedCloneOK("santest:base-9000-disk-0,size=20G", "lvm"))
}

func TestValidateDiskSize(t *testing.T) {
	assert.NoError(t, ValidateDiskSize(0, 20))  // 0 = keep template size
	assert.NoError(t, ValidateDiskSize(40, 20))
	assert.NoError(t, ValidateDiskSize(20, 20)) // equal → no resize, valid
	err := ValidateDiskSize(10, 20)             // PVE cannot shrink
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "shrink")
}

func TestValidateMachineName(t *testing.T) {
	assert.NoError(t, ValidateMachineName("c1-pool1-abcde-xyz12"))
	assert.Error(t, ValidateMachineName(strings.Repeat("a", 64))) // >63 chars
	assert.Error(t, ValidateMachineName("Under_score"))           // invalid DNS char
	assert.Error(t, ValidateMachineName("-leading-dash"))
	assert.Error(t, ValidateMachineName(""))
}
