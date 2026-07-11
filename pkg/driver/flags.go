package driver

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/rancher/machine/libmachine/drivers"
	"github.com/rancher/machine/libmachine/mcnflag"
)

const (
	flagURL           = "pvenode-url"
	flagTokenID       = "pvenode-token-id"
	flagTokenSecret   = "pvenode-token-secret"
	flagInsecureTLS   = "pvenode-insecure-tls"
	flagCACert        = "pvenode-ca-cert"
	flagNode          = "pvenode-node"
	flagTemplate      = "pvenode-template"
	flagStorage       = "pvenode-storage"
	flagLinkedClone   = "pvenode-linked-clone"
	flagCores         = "pvenode-cores"
	flagMemory        = "pvenode-memory"
	flagCPUType       = "pvenode-cpu-type"
	flagDiskSize      = "pvenode-disk-size"
	flagBridge        = "pvenode-bridge"
	flagVLAN          = "pvenode-vlan"
	flagPool          = "pvenode-pool"
	flagSSHUser       = "pvenode-ssh-user"
	flagAgentTimeout  = "pvenode-agent-timeout"
	flagVMIDRange     = "pvenode-vmid-range"
	flagCIPassword    = "pvenode-cipassword"
	flagNameserver    = "pvenode-nameserver"
	flagSearchdomain  = "pvenode-searchdomain"
	flagOnBoot        = "pvenode-onboot"
	flagTags          = "pvenode-tags"
	flagKeepOnFailure = "pvenode-keep-on-failure"

	defaultCores        = 2
	defaultMemoryMB     = 4096
	defaultSSHUser      = "rancher"
	defaultAgentTimeout = 300
	defaultVMIDRange    = "10000-19999"
)

func envVar(flag string) string {
	return strings.ToUpper(strings.ReplaceAll(flag, "-", "_"))
}

func (d *Driver) GetCreateFlags() []mcnflag.Flag {
	return []mcnflag.Flag{
		mcnflag.StringFlag{Name: flagURL, EnvVar: envVar(flagURL), Usage: "Proxmox VE API URL, e.g. https://pve.example.com:8006"},
		mcnflag.StringFlag{Name: flagTokenID, EnvVar: envVar(flagTokenID), Usage: "PVE API token ID (user@realm!tokenname)"},
		mcnflag.StringFlag{Name: flagTokenSecret, EnvVar: envVar(flagTokenSecret), Usage: "PVE API token secret"},
		mcnflag.BoolFlag{Name: flagInsecureTLS, EnvVar: envVar(flagInsecureTLS), Usage: "Skip TLS certificate verification"},
		mcnflag.StringFlag{Name: flagCACert, EnvVar: envVar(flagCACert), Usage: "PEM CA certificate to trust for the PVE API (content, not a path)"},
		mcnflag.StringFlag{Name: flagNode, EnvVar: envVar(flagNode), Usage: "Target PVE node name (default: the template's node)"},
		mcnflag.StringFlag{Name: flagTemplate, EnvVar: envVar(flagTemplate), Usage: "Template to clone: VM name or numeric VMID (required)"},
		mcnflag.StringFlag{Name: flagStorage, EnvVar: envVar(flagStorage), Usage: "Target storage for full clones (default: same as template)"},
		mcnflag.BoolFlag{Name: flagLinkedClone, EnvVar: envVar(flagLinkedClone), Usage: "Use a linked clone instead of a full clone"},
		mcnflag.IntFlag{Name: flagCores, EnvVar: envVar(flagCores), Usage: "Number of CPU cores", Value: defaultCores},
		mcnflag.IntFlag{Name: flagMemory, EnvVar: envVar(flagMemory), Usage: "Memory in MB", Value: defaultMemoryMB},
		mcnflag.StringFlag{Name: flagCPUType, EnvVar: envVar(flagCPUType), Usage: "CPU type, e.g. host (default: PVE default)"},
		mcnflag.IntFlag{Name: flagDiskSize, EnvVar: envVar(flagDiskSize), Usage: "Boot disk size in GB (grow-only; 0 = keep template size)"},
		mcnflag.StringFlag{Name: flagBridge, EnvVar: envVar(flagBridge), Usage: "Network bridge for net0, e.g. vmbr0 (default: keep template's)"},
		mcnflag.IntFlag{Name: flagVLAN, EnvVar: envVar(flagVLAN), Usage: "VLAN tag for net0 (0 = none)"},
		mcnflag.StringFlag{Name: flagPool, EnvVar: envVar(flagPool), Usage: "PVE resource pool for created VMs"},
		mcnflag.StringFlag{Name: flagSSHUser, EnvVar: envVar(flagSSHUser), Usage: "Cloud-init user for SSH provisioning", Value: defaultSSHUser},
		mcnflag.IntFlag{Name: flagAgentTimeout, EnvVar: envVar(flagAgentTimeout), Usage: "Seconds to wait for the guest agent to report an IP", Value: defaultAgentTimeout},
		mcnflag.StringFlag{Name: flagVMIDRange, EnvVar: envVar(flagVMIDRange), Usage: "VMID allocation range lo-hi", Value: defaultVMIDRange},
		mcnflag.StringFlag{Name: flagCIPassword, EnvVar: envVar(flagCIPassword), Usage: "Optional cloud-init password (console rescue)"},
		mcnflag.StringFlag{Name: flagNameserver, EnvVar: envVar(flagNameserver), Usage: "Optional DNS server override via cloud-init"},
		mcnflag.StringFlag{Name: flagSearchdomain, EnvVar: envVar(flagSearchdomain), Usage: "Optional DNS search domain via cloud-init"},
		mcnflag.BoolFlag{Name: flagOnBoot, EnvVar: envVar(flagOnBoot), Usage: "Start the VM automatically when the PVE host boots"},
		mcnflag.StringFlag{Name: flagTags, EnvVar: envVar(flagTags), Usage: "Extra PVE tags, comma-separated"},
		mcnflag.BoolFlag{Name: flagKeepOnFailure, EnvVar: envVar(flagKeepOnFailure), Usage: "Keep the VM when Create fails (standalone CLI debugging only)"},
	}
}

func (d *Driver) SetConfigFromFlags(opts drivers.DriverOptions) error {
	d.URL = opts.String(flagURL)
	d.TokenID = opts.String(flagTokenID)
	d.TokenSecret = opts.String(flagTokenSecret)
	d.InsecureTLS = opts.Bool(flagInsecureTLS)
	d.CACert = opts.String(flagCACert)
	d.NodeName = opts.String(flagNode)
	d.TemplateRef = opts.String(flagTemplate)
	d.Storage = opts.String(flagStorage)
	d.LinkedClone = opts.Bool(flagLinkedClone)
	d.Cores = opts.Int(flagCores)
	d.MemoryMB = opts.Int(flagMemory)
	d.CPUType = opts.String(flagCPUType)
	d.DiskSizeGB = opts.Int(flagDiskSize)
	d.Bridge = opts.String(flagBridge)
	d.VLANTag = opts.Int(flagVLAN)
	d.ResourcePool = opts.String(flagPool)
	d.SSHUser = opts.String(flagSSHUser)
	d.SSHPort = 22
	d.AgentTimeout = opts.Int(flagAgentTimeout)
	d.VMIDRange = opts.String(flagVMIDRange)
	d.CIPassword = opts.String(flagCIPassword)
	d.Nameserver = opts.String(flagNameserver)
	d.Searchdomain = opts.String(flagSearchdomain)
	d.OnBoot = opts.Bool(flagOnBoot)
	d.ExtraTags = opts.String(flagTags)
	d.KeepOnFailure = opts.Bool(flagKeepOnFailure)

	// Defaults for zero values (Rancher may send explicit zeros).
	if d.Cores == 0 {
		d.Cores = defaultCores
	}
	if d.MemoryMB == 0 {
		d.MemoryMB = defaultMemoryMB
	}
	if d.SSHUser == "" {
		d.SSHUser = defaultSSHUser
	}
	if d.AgentTimeout == 0 {
		d.AgentTimeout = defaultAgentTimeout
	}
	if d.VMIDRange == "" {
		d.VMIDRange = defaultVMIDRange
	}

	for flag, val := range map[string]string{
		flagURL:         d.URL,
		flagTokenID:     d.TokenID,
		flagTokenSecret: d.TokenSecret,
		flagTemplate:    d.TemplateRef,
	} {
		if val == "" {
			return fmt.Errorf("required option --%s is not set", flag)
		}
	}
	if _, _, err := parseVMIDRange(d.VMIDRange); err != nil {
		return fmt.Errorf("--%s: %w", flagVMIDRange, err)
	}
	if d.VLANTag < 0 || d.VLANTag > 4094 {
		return fmt.Errorf("--%s must be between 0 and 4094, got %d", flagVLAN, d.VLANTag)
	}
	// A VLAN tag can only be applied by rewriting net0, which needs the
	// bridge name. Without --pvenode-bridge the tag would be silently
	// dropped and nodes would land on the wrong (untagged) network.
	if d.VLANTag > 0 && d.Bridge == "" {
		return fmt.Errorf("--%s requires --%s (the VLAN tag is set on net0, which must name a bridge)", flagVLAN, flagBridge)
	}
	if d.DiskSizeGB < 0 {
		return fmt.Errorf("--%s must be >= 0, got %d", flagDiskSize, d.DiskSizeGB)
	}
	return nil
}

// parseVMIDRange parses "lo-hi". PVE VMIDs must be within [100, 999999999].
func parseVMIDRange(s string) (int, int, error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid VMID range %q, expected lo-hi (e.g. 10000-19999)", s)
	}
	lo, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid VMID range %q: %w", s, err)
	}
	hi, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid VMID range %q: %w", s, err)
	}
	if lo < 100 || hi > 999999999 || lo >= hi {
		return 0, 0, fmt.Errorf("invalid VMID range %d-%d: need 100 <= lo < hi <= 999999999", lo, hi)
	}
	return lo, hi, nil
}
