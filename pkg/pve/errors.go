package pve

import (
	"errors"
	"net"
	"strings"

	proxmox "github.com/luthermonson/go-proxmox"
)

// PVE surfaces most failures as HTTP 500 with a status-line message, which
// go-proxmox turns into plain errors. Classification is therefore
// string-based by necessity; every substring below is a stable PVE message.

func IsVMIDConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "config file already exists")
}

func IsLockErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, s := range []string{
		"can't lock file",
		"got lock request timeout",
		"cfs-lock",
		"can't acquire lock",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func IsAgentNotRunning(err error) bool {
	return err != nil && strings.Contains(err.Error(), "QEMU guest agent is not running")
}

func IsNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	return proxmox.IsNotFound(err) ||
		strings.Contains(err.Error(), "does not exist") ||
		strings.Contains(err.Error(), "no such") // "no such logical volume", "no such VM"
}

// IsTransient reports network-level failures worth retrying. Deliberately
// narrow: PVE uses 500 for permanent errors, so 5xx is NOT transient.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{
		"connection refused",
		"connection reset",
		"unexpected EOF",
		"595", // PVE proxy: errors during connection establishment
		"i/o timeout",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
