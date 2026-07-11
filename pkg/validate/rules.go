package validate

import (
	"fmt"
	"regexp"
	"strings"
)

// ValidateCloneMode rejects flag combinations PVE would silently ignore
// or reject at clone time.
func ValidateCloneMode(linked bool, storage, targetNode, templateNode string, templateStorageShared bool) error {
	if linked && storage != "" {
		return fmt.Errorf(
			"--pvenode-storage has no effect on linked clones (PVE ignores it; a linked clone always " +
				"lives on the template's storage) — drop the storage option or use a full clone")
	}
	if targetNode != "" && targetNode != templateNode && !templateStorageShared {
		return fmt.Errorf(
			"target node %q differs from the template's node %q but the template's storage is not shared — "+
				"cloning across nodes requires shared storage", targetNode, templateNode)
	}
	return nil
}

// linkedCloneStorageTypes can hold base images for linked clones.
var linkedCloneStorageTypes = map[string]bool{
	"lvmthin": true, "zfspool": true, "rbd": true, "btrfs": true,
}

// LinkedCloneOK checks the template's boot-disk storage can back a linked
// clone: a base-image-capable storage type, or a qcow2 file on file storage.
func LinkedCloneOK(bootDiskValue, storageType string) error {
	if linkedCloneStorageTypes[storageType] || strings.Contains(bootDiskValue, ".qcow2") {
		return nil
	}
	return fmt.Errorf(
		"the template's boot disk (%s on %q storage) cannot back a linked clone — "+
			"use a full clone, or move the template to lvmthin/zfs/rbd/btrfs storage or qcow2 format",
		bootDiskValue, storageType)
}

// ValidateDiskSize enforces grow-only resizing. requestedGB == 0 means
// keep the template's size.
func ValidateDiskSize(requestedGB, templateGB int) error {
	if requestedGB != 0 && requestedGB < templateGB {
		return fmt.Errorf(
			"requested disk size %dG is smaller than the template's %dG — PVE cannot shrink disks; "+
				"set --pvenode-disk-size to at least %d (or 0 to keep the template size)",
			requestedGB, templateGB, templateGB)
	}
	return nil
}

var dnsNameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?$`)

// ValidateMachineName ensures the Rancher machine name works as a PVE VM
// name and a guest hostname (cloud-init sets the hostname from it).
func ValidateMachineName(name string) error {
	if len(name) == 0 || len(name) > 63 || !dnsNameRe.MatchString(name) {
		return fmt.Errorf(
			"machine name %q cannot be used as a hostname (must be 1-63 chars, alphanumeric and dashes, "+
				"no leading/trailing dash) — use shorter, DNS-safe cluster and pool names in Rancher", name)
	}
	return nil
}
