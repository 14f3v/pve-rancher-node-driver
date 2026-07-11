package pve

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClassifiers(t *testing.T) {
	assert.True(t, IsVMIDConflict(errors.New("500 unable to create VM 10005 - config file already exists")))
	assert.True(t, IsLockErr(errors.New("500 can't lock file '/var/lock/qemu-server/lock-9000.conf' - got timeout")))
	assert.True(t, IsLockErr(errors.New("got lock request timeout")))
	assert.True(t, IsLockErr(errors.New("cfs-lock 'file-user_cfg' error")))
	assert.True(t, IsLockErr(errors.New("clone failed: can't lock file '/var/lock/pve-manager/pve-storage-local-lvm' - got timeout")))
	assert.True(t, IsLockErr(errors.New("clone failed: Maximum number of retries (60) exceeded")))
	assert.True(t, IsAgentNotRunning(errors.New("500 QEMU guest agent is not running")))
	assert.True(t, IsNotFoundErr(errors.New(`500 Configuration file 'nodes/pve1/qemu-server/123.conf' does not exist`)))
	assert.True(t, IsTransient(errors.New("connection refused")))
	assert.True(t, IsTransient(errors.New("595 Errors during connection establishment")))
	assert.False(t, IsTransient(errors.New("500 storage 'nope' does not exist")))
	assert.False(t, IsLockErr(nil))
}
