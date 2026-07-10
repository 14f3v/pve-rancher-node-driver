# pve-rancher-node-driver Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A production-quality Rancher node driver for Proxmox VE (`pvenode`) so RKE2/K3s machine pools scale from the Rancher v2.11 UI, reliable on DHCP networks, on PVE 8.x and 9.x.

**Architecture:** One Go binary (`docker-machine-driver-pvenode`) implementing the `rancher/machine` libmachine `drivers.Driver` interface. It clones a cloud-init PVE template, configures it via PVE's built-in cloud-init fields, learns the IP by MAC-matching the qemu-guest-agent's interface report, and hands off to rancher-machine's SSH provisioning. No server component.

**Tech Stack:** Go 1.26; `github.com/luthermonson/go-proxmox v0.8.0` (PVE API client); `github.com/rancher/machine v0.15.0-rancher145` (driver SDK); `stretchr/testify` + `net/http/httptest` for tests; goreleaser + GitHub Actions for release.

**Spec:** `docs/superpowers/specs/2026-07-10-pve-rancher-node-driver-design.md` (approved 2026-07-10).

## Global Constraints

- Module path: `github.com/14f3v/pve-rancher-node-driver`. Go directive: `go 1.26.0` (rancher/machine v0.15.0-rancher145 requires it; go-proxmox requires ≥ 1.25).
- Dependency pins — exact, never float:
  - `github.com/luthermonson/go-proxmox v0.8.0` (pre-1.0; minors break APIs).
  - `github.com/rancher/machine v0.15.0-rancher145`. **NEVER `go get github.com/rancher/machine@latest`** — the repo carries stale upstream tags and `@latest` resolves to v0.16.2 (2019 pre-fork code).
  - go.mod must contain: `replace github.com/docker/docker => github.com/moby/moby v1.4.2-0.20170731201646-1009e6a40b29` (rancher/machine transitively needs it).
- Driver name: `pvenode`. Binary name: `docker-machine-driver-pvenode` (rancher-machine resolves plugins by this exact name). Flag prefix: `pvenode-`. Rancher form/credential field names are the flag name minus the `pvenode-` prefix, camelCased (`pvenode-token-id` → `tokenId`).
- Ownership tag stamped on every created VM: `rancher-pvenode`. Never delete a VM that lacks this tag.
- Every PVE mutation returns a task (UPID): always wait for it AND check `task.IsFailed`/`task.ExitStatus` — `task.Wait()` returns nil even when the task failed.
- Never blanket-retry HTTP 500s (PVE uses 500 for permanent errors too). Retry only classified-transient errors, lock errors, and VMID conflicts.
- The token secret must never appear in any log line.
- `go test ./...` must pass offline — unit tests use `httptest` fixtures only. Integration tests are env-gated shell scripts.
- Builds: `CGO_ENABLED=0`. Release assets are raw binaries (not archives) at versioned URLs — Rancher only re-downloads when URL/checksum change.
- All lifecycle state needed across driver calls (VMID, node, IP, SSH user/key path) lives in **exported** struct fields — each lifecycle call runs in a fresh process rehydrated from JSON.

## File Structure

```
go.mod, go.sum, .gitignore, LICENSE
cmd/docker-machine-driver-pvenode/main.go   — plugin entry point
pkg/driver/driver.go        — Driver struct, NewDriver, DriverName, client(), lookupVM()
pkg/driver/flags.go         — flag constants, GetCreateFlags, SetConfigFromFlags
pkg/driver/precreate.go     — PreCreateCheck
pkg/driver/create.go        — Create + cleanup-on-failure
pkg/driver/lifecycle.go     — GetState/Start/Stop/Kill/Restart/GetURL/GetIP/GetSSHHostname
pkg/driver/remove.go        — Remove
pkg/pve/client.go           — Config, New (token auth + TLS)
pkg/pve/errors.go           — error classifiers (lock / conflict / agent / notfound / transient)
pkg/pve/retry.go            — Retry with exponential backoff + jitter
pkg/pve/task.go             — WaitTask
pkg/pve/access.go           — PVEMajorVersion, TokenPermissions
pkg/pve/resources.go        — ListVMs, GetVM, ResolveTemplate, FindVMByNameAndTag, NodeBridges, NodeStorages
pkg/pve/clone.go            — CloneSpec, CloneFromTemplate (random VMID + conflict/lock retry)
pkg/pve/vmops.go            — ApplyConfig, ResizeDisk, StartVM, ShutdownVM, StopVM, DeleteVM
pkg/pve/agentip.go          — WaitForIP (MAC-matched + SSH probe), QueryAgentIP
pkg/validate/privileges.go  — PrivCheck, RequiredChecks (PVE 8 vs 9), Missing
pkg/validate/template.go    — EnsureTemplate, CloudInitDrive, NICMAC, BootDisk
pkg/validate/rules.go       — ValidateCloneMode, LinkedCloneOK, ValidateDiskSize, ValidateMachineName
internal/pvetest/server.go  — httptest fixture server shared by pve + driver tests
scripts/integration-test.sh          — lab integration (rancher-machine CLI)
scripts/integration-concurrent.sh    — 3 parallel creates (template-lock contention)
.goreleaser.yaml, .golangci.yml
.github/workflows/ci.yml, .github/workflows/release.yml
deploy/nodedriver.yaml      — NodeDriver CR with credential annotations + whitelist domains
README.md, docs/e2e-checklist.md
```

Unit tests live next to their package (`pkg/pve/*_test.go` in-package for access to unexported fields; `pkg/driver/*_test.go`, `pkg/validate/*_test.go`).

## Verified API cheat-sheet (from go-proxmox v0.8.0 + rancher/machine v0.15.0-rancher145 source)

Implementers: these signatures were verified against the tagged sources on 2026-07-10. Do not "correct" them from memory.

```go
// go-proxmox (import proxmox "github.com/luthermonson/go-proxmox")
proxmox.NewClient(baseURL string, opts ...proxmox.Option) *proxmox.Client // baseURL includes /api2/json; never errors
proxmox.WithAPIToken(tokenID, secret string) proxmox.Option              // tokenID = "user@realm!name"
proxmox.WithInsecureSkipVerify() proxmox.Option
proxmox.WithRootCAs(pool *x509.CertPool) proxmox.Option                  // NOT WithRootCAFile (swallows read errors)
proxmox.WithTimeout(d time.Duration) proxmox.Option                      // default client has NO timeout
(c *Client) Version(ctx) (*proxmox.Version, error)                       // .Release e.g. "9.2"
(c *Client) Node(ctx, name string) (*proxmox.Node, error)
(n *Node) VirtualMachine(ctx, vmid int) (*proxmox.VirtualMachine, error) // GETs status/current + config
(c *Client) Get(ctx, path string, v interface{}) error                   // raw; path gets baseURL prefix
(c *Client) Permissions(ctx, o *proxmox.PermissionsOptions) (proxmox.Permissions, error)
   // type Permissions map[string]Permission; type Permission map[string]IntOrBool
(v *VirtualMachine) Clone(ctx, params *proxmox.VirtualMachineCloneOptions) (newid int, task *proxmox.Task, err error)
   // options: NewID int, Name, Pool, Storage, Target string, Full proxmox.IntOrBool
(v *VirtualMachine) Config(ctx, opts ...proxmox.VirtualMachineOption) (*proxmox.Task, error)
   // VirtualMachineOption{Name string, Value interface{}}; names are raw PVE keys ("cores","sshkeys","ipconfig0",...)
proxmox.EncodeSSHKeys(keys ...string) string                              // REQUIRED encoding for "sshkeys" value
(v *VirtualMachine) ResizeDisk(ctx, disk, size string) (*proxmox.Task, error)  // size "40G"
(v *VirtualMachine) Start/Stop/Shutdown/Reboot(ctx) (*proxmox.Task, error)
(v *VirtualMachine) Delete(ctx, o *proxmox.VirtualMachineDeleteOptions) (*proxmox.Task, error)
   // options: Purge, DestroyUnreferencedDisks proxmox.IntOrBool
(v *VirtualMachine) AgentGetNetworkIFaces(ctx) ([]*proxmox.AgentNetworkIface, error)
   // AgentNetworkIface{Name, HardwareAddress string, IPAddresses []*AgentNetworkIPAddress}
   // AgentNetworkIPAddress{IPAddressType "ipv4"|"ipv6", IPAddress string, Prefix int}
(v *VirtualMachine) HasTag(value string) bool
(v *VirtualMachine) Ping(ctx) error                                       // refreshes status + config
(t *Task) Wait(ctx, interval, max time.Duration) error   // nil when stopped EVEN IF FAILED; check t.IsFailed/t.ExitStatus
proxmox.IsTimeout(err) / proxmox.IsNotAuthorized(err) / proxmox.IsNotFound(err)
// VirtualMachine fields: Status ("running"/"stopped"/"paused"), Lock string, VMID StringOrUint64,
//   Node string, Template IsTemplate(bool), VirtualMachineConfig *VirtualMachineConfig
// VirtualMachineConfig: Template IntOrBool, Agent string, Boot string ("order=scsi0;ide2"),
//   Nets/IDEs/SCSIs/SATAs/VirtIOs map[string]string (indexed devices; NO Net0-style fields in v0.8.0),
//   TagsSlice []string, Cores *int, Memory StringOrInt

// rancher/machine
drivers.BaseDriver{MachineName, StorePath, IPAddress, SSHUser string, SSHPort int, SSHKeyPath string}
   // provides GetMachineName, GetIP (returns .IPAddress), GetIPv6, GetSSHKeyPath (defaults to
   // <StorePath>/machines/<name>/id_rsa), GetSSHPort (22), GetSSHUsername ("root"), no-op PreCreateCheck
drivers.DriverOptions interface{ String(key) string; StringSlice(key) []string; Int(key) int; Bool(key) bool }
drivers.MustBeRunning(d Driver) error
mcnflag.StringFlag{Name, Usage, EnvVar, Value string}; IntFlag{...Value int}; BoolFlag{Name, Usage, EnvVar} // NO Value field
plugin.RegisterDriver(d drivers.Driver)              // github.com/rancher/machine/libmachine/drivers/plugin
ssh.GenerateSSHKey(path string) error                // github.com/rancher/machine/libmachine/ssh; writes path + path.pub
state.None/Running/Paused/Saved/Stopped/Stopping/Starting/Error/Timeout/NotFound  // libmachine/state
log.Infof/Debugf/Warnf/Errorf                        // github.com/rancher/machine/libmachine/log
```

---

### Task 1: Repo scaffold and plugin entry point

**Files:**
- Create: `go.mod`, `.gitignore`, `LICENSE`
- Create: `cmd/docker-machine-driver-pvenode/main.go`
- Create: `pkg/driver/driver.go`
- Test: `pkg/driver/driver_test.go`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: `driver.NewDriver(machineName, storePath string) *Driver`; `(*Driver).DriverName() string` returning `"pvenode"`; constant `driver.MachineTag = "rancher-pvenode"`; the `Driver` struct with all exported config/state fields later tasks populate.

- [ ] **Step 1: Create go.mod and .gitignore**

`go.mod`:

```go
module github.com/14f3v/pve-rancher-node-driver

go 1.26.0

replace github.com/docker/docker => github.com/moby/moby v1.4.2-0.20170731201646-1009e6a40b29

require (
	github.com/luthermonson/go-proxmox v0.8.0
	github.com/rancher/machine v0.15.0-rancher145
	github.com/stretchr/testify v1.10.0
)
```

`.gitignore`:

```
/bin/
/dist/
/.integration/
*.test
```

`LICENSE`: the standard Apache License 2.0 text (fetch verbatim from https://www.apache.org/licenses/LICENSE-2.0.txt), copyright line `Copyright 2026 Khemphet SOUVANNAPHASY`.

- [ ] **Step 2: Write the failing test**

`pkg/driver/driver_test.go`:

```go
package driver

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDriverName(t *testing.T) {
	d := NewDriver("test-machine", "/tmp/store")
	assert.Equal(t, "pvenode", d.DriverName())
	assert.Equal(t, "test-machine", d.GetMachineName())
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go mod tidy && go test ./pkg/driver/ -run TestDriverName -v`
Expected: FAIL (compile error: `NewDriver` undefined). Note: `go mod tidy` needs network on first run to fill go.sum.

- [ ] **Step 4: Write the Driver struct and constructor**

`pkg/driver/driver.go`:

```go
// Package driver implements the pvenode rancher-machine driver for Proxmox VE.
package driver

import (
	"github.com/rancher/machine/libmachine/drivers"

	"github.com/14f3v/pve-rancher-node-driver/pkg/pve"
)

const (
	// MachineTag marks every VM this driver creates. Remove refuses to
	// delete a VM without it (guards against VMID/name reuse).
	MachineTag = "rancher-pvenode"

	driverName = "pvenode"
)

type Driver struct {
	*drivers.BaseDriver

	// Cloud credential (Rancher fields: url, tokenId, tokenSecret, insecureTls, caCert)
	URL         string
	TokenID     string
	TokenSecret string
	InsecureTLS bool
	CACert      string // PEM content, optional

	// Machine config
	TemplateRef   string // template VM name or numeric VMID
	Storage       string // full clones only
	NodeName      string // target PVE node; empty = template's node
	LinkedClone   bool   // default false = full clone
	Cores         int
	MemoryMB      int
	CPUType       string
	DiskSizeGB    int // 0 = keep template size
	Bridge        string
	VLANTag       int
	ResourcePool  string
	AgentTimeout  int // seconds to wait for the guest agent to report an IP
	VMIDRange     string
	CIPassword    string
	Nameserver    string
	Searchdomain  string
	OnBoot        bool
	ExtraTags     string // comma-separated
	KeepOnFailure bool

	// Lifecycle state — exported so it survives the JSON round-trip
	// between driver processes (each call is a fresh process).
	VMID    int
	PVENode string
}

func NewDriver(machineName, storePath string) *Driver {
	return &Driver{
		BaseDriver: &drivers.BaseDriver{
			MachineName: machineName,
			StorePath:   storePath,
		},
	}
}

// DriverName is the plugin name; the binary must be named
// docker-machine-driver-pvenode to match.
func (d *Driver) DriverName() string {
	return driverName
}

// client builds a PVE API client from the stored credential fields.
// Never cached in a struct field: each lifecycle call runs in a fresh process.
func (d *Driver) client() (*pve.Client, error) {
	return pve.New(pve.Config{
		URL:         d.URL,
		TokenID:     d.TokenID,
		TokenSecret: d.TokenSecret,
		InsecureTLS: d.InsecureTLS,
		CACertPEM:   d.CACert,
	})
}

// Compile-time interface assertion: main.go hands *Driver to
// plugin.RegisterDriver(drivers.Driver), so the full interface must exist
// from day one. The stubs below are DELETED one by one as later tasks
// implement the real methods (a leftover stub = duplicate-method compile
// error, so forgetting is impossible).
var _ drivers.Driver = (*Driver)(nil)

var errNotImplemented = errors.New("pvenode: not implemented yet")

func (d *Driver) GetCreateFlags() []mcnflag.Flag                       { return nil }
func (d *Driver) SetConfigFromFlags(opts drivers.DriverOptions) error  { return nil }
func (d *Driver) Create() error                                        { return errNotImplemented }
func (d *Driver) Remove() error                                        { return errNotImplemented }
func (d *Driver) Start() error                                         { return errNotImplemented }
func (d *Driver) Stop() error                                          { return errNotImplemented }
func (d *Driver) Kill() error                                          { return errNotImplemented }
func (d *Driver) Restart() error                                       { return errNotImplemented }
func (d *Driver) GetState() (state.State, error)                       { return state.None, errNotImplemented }
func (d *Driver) GetURL() (string, error)                              { return "", errNotImplemented }
func (d *Driver) GetSSHHostname() (string, error)                      { return "", errNotImplemented }
```

Add the imports `errors`, `github.com/rancher/machine/libmachine/mcnflag`, and `github.com/rancher/machine/libmachine/state` to driver.go for the stubs.

This references `pkg/pve` which doesn't exist yet — create a minimal placeholder so Task 1 compiles, replaced for real in Task 3:

`pkg/pve/client.go`:

```go
// Package pve wraps the go-proxmox client with the retry, task-wait and
// lookup helpers the driver needs.
package pve

type Config struct {
	URL         string
	TokenID     string
	TokenSecret string
	InsecureTLS bool
	CACertPEM   string
}

type Client struct{}

func New(cfg Config) (*Client, error) {
	return &Client{}, nil
}
```

`cmd/docker-machine-driver-pvenode/main.go`:

```go
package main

import (
	"github.com/rancher/machine/libmachine/drivers/plugin"

	"github.com/14f3v/pve-rancher-node-driver/pkg/driver"
)

func main() {
	plugin.RegisterDriver(driver.NewDriver("", ""))
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go mod tidy && go test ./pkg/driver/ -run TestDriverName -v && go build ./...`
Expected: PASS, and the build succeeds.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum .gitignore LICENSE cmd/ pkg/
git commit -m "feat: scaffold pvenode driver plugin skeleton"
```

---

### Task 2: Driver flags and SetConfigFromFlags

**Files:**
- Create: `pkg/driver/flags.go`
- Test: `pkg/driver/flags_test.go`

**Interfaces:**
- Consumes: `Driver` struct fields from Task 1.
- Produces: `(*Driver).GetCreateFlags() []mcnflag.Flag`; `(*Driver).SetConfigFromFlags(opts drivers.DriverOptions) error`; `parseVMIDRange(s string) (lo, hi int, err error)`. Flag name constants `flagURL = "pvenode-url"` etc. used by tests and docs.

- [ ] **Step 1: Write the failing tests**

`pkg/driver/flags_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/driver/ -v`
Expected: FAIL — compile error on `parseVMIDRange` (undefined); once implemented, the default/required assertions fail against the Task 1 stubs.

- [ ] **Step 3: Implement flags.go — first DELETE the `GetCreateFlags` and `SetConfigFromFlags` stubs from driver.go**

`pkg/driver/flags.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/driver/ -v`
Expected: PASS (all tests).

- [ ] **Step 5: Commit**

```bash
git add pkg/driver/flags.go pkg/driver/flags_test.go
git commit -m "feat: driver flags and SetConfigFromFlags with validation"
```

---

### Task 3: PVE client constructor, error classification, retry helper, test fixture server

**Files:**
- Modify: `pkg/pve/client.go` (replace the Task 1 placeholder)
- Create: `pkg/pve/errors.go`, `pkg/pve/retry.go`
- Create: `internal/pvetest/server.go`
- Test: `pkg/pve/client_test.go`, `pkg/pve/errors_test.go`, `pkg/pve/retry_test.go`

**Interfaces:**
- Consumes: `pve.Config` shape from Task 1.
- Produces:
  - `pve.New(cfg Config) (*Client, error)` — real client; `Client` holds unexported `px *proxmox.Client`.
  - `pve.IsLockErr(err) bool`, `pve.IsVMIDConflict(err) bool`, `pve.IsAgentNotRunning(err) bool`, `pve.IsNotFoundErr(err) bool`, `pve.IsTransient(err) bool`.
  - `pve.Retry(ctx context.Context, attempts int, base time.Duration, retryable func(error) bool, fn func() error) error`.
  - `pvetest.New(t *testing.T) *pvetest.Server` with `Handle(method, path string, status int, data interface{})`, `HandleFunc(method, path string, h http.HandlerFunc)`, `URL() string` (returns the server base URL **without** `/api2/json`), and `OKTask(node string) string` (registers a completed-OK task status route, returns its UPID).

- [ ] **Step 1: Write the fixture server (test infrastructure, no TDD cycle of its own)**

`internal/pvetest/server.go`:

```go
// Package pvetest provides an httptest-backed fake of the PVE API
// (/api2/json/...) for driver and client unit tests.
package pvetest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Server routes by exact "METHOD /api2/json<path>" match. It deliberately
// does NOT use http.ServeMux: tests override routes (re-register the same
// path with different behavior), which ServeMux punishes with a panic.
type Server struct {
	t      *testing.T
	mu     sync.RWMutex
	routes map[string]http.HandlerFunc
	srv    *httptest.Server
}

func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{t: t, routes: map[string]http.HandlerFunc{}}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path // Path excludes the query string
		s.mu.RLock()
		h, ok := s.routes[key]
		s.mu.RUnlock()
		if !ok {
			t.Logf("pvetest: unhandled %s", key)
			http.Error(w, `{"errors":{"unhandled":"route"}}`, http.StatusNotFound)
			return
		}
		h(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// URL returns the base PVE URL (no /api2/json suffix) — what a user
// would put in --pvenode-url.
func (s *Server) URL() string { return s.srv.URL }

// HandleFunc registers (or overrides) a route. path is relative to
// /api2/json, e.g. "/version".
func (s *Server) HandleFunc(method, path string, h http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[method+" /api2/json"+path] = h
}

// Handle registers a JSON response wrapped in PVE's {"data": ...} envelope.
func (s *Server) Handle(method, path string, status int, data interface{}) {
	s.HandleFunc(method, path, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": data})
	})
}

// OKTask registers a task-status route reporting completed/OK and returns
// the UPID that clone/config/etc. fixtures should hand back.
func (s *Server) OKTask(node string) string {
	return s.taskRoute(node, "0000AB12", "OK")
}

// FailedTask is like OKTask but the task ends in an error exit status.
func (s *Server) FailedTask(node, exitStatus string) string {
	return s.taskRoute(node, "0000AB13", exitStatus)
}

func (s *Server) taskRoute(node, pid, exitStatus string) string {
	upid := fmt.Sprintf("UPID:%s:%s:00FF12AA:65F00001:qmtask:100:root@pam!rancher:", node, pid)
	s.Handle("GET", fmt.Sprintf("/nodes/%s/tasks/%s/status", node, upid), http.StatusOK, map[string]interface{}{
		"status": "stopped", "exitstatus": exitStatus, "node": node, "upid": upid,
		"id": "100", "type": "qmtask", "user": "root@pam!rancher",
		"pid": 1, "pstart": 1, "starttime": 1751900000,
	})
	return upid
}

// PVEError reproduces how PVE reports errors: the detail lives in the HTTP
// STATUS LINE REASON PHRASE (e.g. "HTTP/1.1 500 got lock request timeout"),
// which go-proxmox turns into the Go error string. net/http cannot set a
// custom reason phrase, so we hijack the connection and write it raw.
func PVEError(w http.ResponseWriter, code int, reason string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, reason, code)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		http.Error(w, reason, code)
		return
	}
	defer conn.Close()
	fmt.Fprintf(buf, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", code, reason)
	_ = buf.Flush()
}
```

Note the `PVEError` contract used throughout this plan: pass the reason WITHOUT the numeric prefix — `PVEError(w, 500, "got lock request timeout")` produces the error string `"500 got lock request timeout"` on the client side.

- [ ] **Step 2: Write the failing tests**

`pkg/pve/client_test.go`:

```go
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
```

`pkg/pve/errors_test.go`:

```go
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
	assert.True(t, IsAgentNotRunning(errors.New("500 QEMU guest agent is not running")))
	assert.True(t, IsNotFoundErr(errors.New(`500 Configuration file 'nodes/pve1/qemu-server/123.conf' does not exist`)))
	assert.True(t, IsTransient(errors.New("connection refused")))
	assert.True(t, IsTransient(errors.New("595 Errors during connection establishment")))
	assert.False(t, IsTransient(errors.New("500 storage 'nope' does not exist")))
	assert.False(t, IsLockErr(nil))
}
```

`pkg/pve/retry_test.go`:

```go
package pve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetrySucceedsAfterRetryable(t *testing.T) {
	calls := 0
	err := Retry(context.Background(), 5, time.Millisecond, IsLockErr, func() error {
		calls++
		if calls < 3 {
			return errors.New("got lock request timeout")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
}

func TestRetryStopsOnNonRetryable(t *testing.T) {
	calls := 0
	err := Retry(context.Background(), 5, time.Millisecond, IsLockErr, func() error {
		calls++
		return errors.New("permanent problem")
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls)
}

func TestRetryExhausts(t *testing.T) {
	err := Retry(context.Background(), 3, time.Millisecond, IsLockErr, func() error {
		return errors.New("got lock request timeout")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after 3 attempts")
}

func TestRetryHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Retry(ctx, 5, 10*time.Second, IsLockErr, func() error {
		return errors.New("got lock request timeout")
	})
	assert.ErrorIs(t, err, context.Canceled)
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./pkg/pve/ -v`
Expected: FAIL (compile errors: `IsLockErr`, `Retry` undefined; `c.px` undefined).

- [ ] **Step 4: Implement client.go, errors.go, retry.go**

`pkg/pve/client.go` (replaces placeholder):

```go
// Package pve wraps the go-proxmox client with the retry, task-wait and
// lookup helpers the driver needs.
package pve

import (
	"crypto/x509"
	"fmt"
	"strings"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
)

type Config struct {
	URL         string
	TokenID     string
	TokenSecret string
	InsecureTLS bool
	CACertPEM   string
}

type Client struct {
	px *proxmox.Client
}

func New(cfg Config) (*Client, error) {
	base := strings.TrimRight(cfg.URL, "/")
	if !strings.HasSuffix(base, "/api2/json") {
		base += "/api2/json"
	}
	opts := []proxmox.Option{
		proxmox.WithAPIToken(cfg.TokenID, cfg.TokenSecret),
		// The default go-proxmox HTTP client has no timeout at all.
		proxmox.WithTimeout(90 * time.Second),
	}
	if cfg.CACertPEM != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.CACertPEM)) {
			return nil, fmt.Errorf("pvenode: --pvenode-ca-cert does not contain a valid PEM certificate")
		}
		opts = append(opts, proxmox.WithRootCAs(pool))
	}
	if cfg.InsecureTLS {
		opts = append(opts, proxmox.WithInsecureSkipVerify())
	}
	return &Client{px: proxmox.NewClient(base, opts...)}, nil
}
```

`pkg/pve/errors.go`:

```go
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
```

`pkg/pve/retry.go`:

```go
package pve

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

const maxBackoff = 30 * time.Second

// Retry runs fn up to attempts times, backing off exponentially (base, 2x,
// 4x... capped at 30s) with ±50% jitter, as long as retryable(err) is true.
func Retry(ctx context.Context, attempts int, base time.Duration, retryable func(error) bool, fn func() error) error {
	var err error
	delay := base
	for i := 1; i <= attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if !retryable(err) {
			return err
		}
		if i == attempts {
			break
		}
		jittered := delay/2 + time.Duration(rand.Int63n(int64(delay)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(jittered):
		}
		if delay *= 2; delay > maxBackoff {
			delay = maxBackoff
		}
	}
	return fmt.Errorf("after %d attempts: %w", attempts, err)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./pkg/pve/ ./internal/... ./pkg/driver/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/pve/ internal/pvetest/
git commit -m "feat: PVE client with token auth, error classifiers, retry helper, test fixtures"
```

---

### Task 4: Task waiting

**Files:**
- Create: `pkg/pve/task.go`
- Test: `pkg/pve/task_test.go`

**Interfaces:**
- Consumes: `Client.px`, `pvetest` fixtures.
- Produces: `(*Client).WaitTask(ctx context.Context, task *proxmox.Task, timeout time.Duration) error` — nil-task tolerant; returns an error naming the UPID and `ExitStatus` when the task failed.

- [ ] **Step 1: Write the failing tests**

`pkg/pve/task_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/pve/ -run TestWaitTask -v`
Expected: FAIL (`WaitTask` undefined).

- [ ] **Step 3: Implement task.go**

`pkg/pve/task.go`:

```go
package pve

import (
	"context"
	"fmt"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
)

const taskPollInterval = 2 * time.Second

// WaitTask waits for a PVE task and converts a failed exit status into an
// error. go-proxmox's task.Wait returns nil once the task stops, EVEN IF
// the task failed — IsFailed/ExitStatus must be checked explicitly.
func (c *Client) WaitTask(ctx context.Context, task *proxmox.Task, timeout time.Duration) error {
	if task == nil {
		return nil
	}
	if err := task.Wait(ctx, taskPollInterval, timeout); err != nil {
		if proxmox.IsTimeout(err) {
			return fmt.Errorf("pve task %s did not finish within %s", task.UPID, timeout)
		}
		return fmt.Errorf("waiting for pve task %s: %w", task.UPID, err)
	}
	if task.IsFailed {
		return fmt.Errorf("pve task %s failed: %s", task.UPID, task.ExitStatus)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/pve/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/pve/task.go pkg/pve/task_test.go
git commit -m "feat: task waiting with explicit exit-status check"
```

---

### Task 5: PVE version detection, token permissions, privilege sets

**Files:**
- Create: `pkg/pve/access.go`
- Create: `pkg/validate/privileges.go`
- Test: `pkg/pve/access_test.go`, `pkg/validate/privileges_test.go`

**Interfaces:**
- Consumes: `Client.px`, `pvetest`.
- Produces:
  - `(*Client).PVEMajorVersion(ctx) (int, error)`.
  - `(*Client).TokenPermissions(ctx) (proxmox.Permissions, error)`.
  - `validate.PrivCheck{Path, Priv string}`; `validate.RequiredChecks(pveMajor int, storage, bridge string, pool string) []PrivCheck`; `validate.Missing(perms proxmox.Permissions, checks []PrivCheck) []string`.

- [ ] **Step 1: Write the failing tests**

`pkg/pve/access_test.go`:

```go
package pve

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

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
```

`pkg/validate/privileges_test.go`:

```go
package validate

import (
	"testing"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
)

func perms(m map[string]map[string]bool) proxmox.Permissions {
	out := proxmox.Permissions{}
	for path, privs := range m {
		p := proxmox.Permission{}
		for k, v := range privs {
			p[k] = proxmox.IntOrBool(v)
		}
		out[path] = p
	}
	return out
}

func TestRequiredChecksVersionAware(t *testing.T) {
	privsOf := func(checks []PrivCheck) []string {
		var out []string
		for _, c := range checks {
			out = append(out, c.Priv)
		}
		return out
	}
	v8 := RequiredChecks(8, "local", "vmbr0", "")
	v9 := RequiredChecks(9, "local", "vmbr0", "")
	assert.Contains(t, privsOf(v8), "VM.Monitor")           // PVE 8: agent reads
	assert.NotContains(t, privsOf(v8), "VM.GuestAgent.Audit")
	assert.Contains(t, privsOf(v9), "VM.GuestAgent.Audit")  // PVE 9 replacement
	assert.NotContains(t, privsOf(v9), "VM.Monitor")
	assert.Contains(t, privsOf(v9), "SDN.Use")
}

func TestRequiredChecksPool(t *testing.T) {
	checks := RequiredChecks(9, "local", "vmbr0", "rancher-pool")
	found := false
	for _, c := range checks {
		if c.Priv == "Pool.Allocate" && c.Path == "/pool/rancher-pool" {
			found = true
		}
	}
	assert.True(t, found)
}

func TestMissingLenientScan(t *testing.T) {
	// The check is advisory: a privilege granted at ANY path counts, because
	// PVE's ACL model (propagation, pools) is too flexible to mirror
	// exactly. The target bug class is the zero-ACL privsep token, which
	// yields an empty permissions map and silent empty API responses.
	p := perms(map[string]map[string]bool{
		"/pool/rancher-pool": {"VM.Clone": true, "VM.Allocate": true},
	})
	missing := Missing(p, []PrivCheck{
		{Path: "/vms", Priv: "VM.Clone"},        // granted elsewhere → OK
		{Path: "/vms", Priv: "VM.PowerMgmt"},    // granted nowhere → missing
	})
	assert.Equal(t, []string{"VM.PowerMgmt"}, missing)
}

func TestMissingEmptyPerms(t *testing.T) {
	missing := Missing(proxmox.Permissions{}, RequiredChecks(9, "local", "vmbr0", ""))
	assert.NotEmpty(t, missing)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/pve/ ./pkg/validate/ -v`
Expected: FAIL (compile errors: undefined symbols).

- [ ] **Step 3: Implement access.go and privileges.go**

`pkg/pve/access.go`:

```go
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
```

`pkg/validate/privileges.go`:

```go
// Package validate holds the pure validation logic behind PreCreateCheck:
// privilege sets, template requirements and clone-mode rules.
package validate

import (
	proxmox "github.com/luthermonson/go-proxmox"
)

type PrivCheck struct {
	Path string
	Priv string
}

// RequiredChecks returns the privileges the driver's token needs.
// PVE 9 removed VM.Monitor; guest-agent reads need VM.GuestAgent.Audit.
// SDN.Use on the bridge is required since PVE 8 for privsep tokens.
func RequiredChecks(pveMajor int, storage, bridge string, pool string) []PrivCheck {
	vmPrivs := []string{
		"VM.Clone", "VM.Allocate", "VM.Audit", "VM.PowerMgmt",
		"VM.Config.Disk", "VM.Config.CPU", "VM.Config.Memory",
		"VM.Config.Network", "VM.Config.Cloudinit", "VM.Config.Options",
	}
	if pveMajor >= 9 {
		vmPrivs = append(vmPrivs, "VM.GuestAgent.Audit")
	} else {
		vmPrivs = append(vmPrivs, "VM.Monitor")
	}

	var checks []PrivCheck
	for _, p := range vmPrivs {
		checks = append(checks, PrivCheck{Path: "/vms", Priv: p})
	}
	storagePath := "/storage"
	if storage != "" {
		storagePath = "/storage/" + storage
	}
	checks = append(checks,
		PrivCheck{Path: storagePath, Priv: "Datastore.AllocateSpace"},
		PrivCheck{Path: storagePath, Priv: "Datastore.Audit"},
	)
	if bridge != "" {
		checks = append(checks, PrivCheck{Path: "/sdn/zones/localnetwork/" + bridge, Priv: "SDN.Use"})
	}
	if pool != "" {
		checks = append(checks, PrivCheck{Path: "/pool/" + pool, Priv: "Pool.Allocate"})
	}
	return checks
}

// Missing returns the privileges not granted at ANY path. The scan is
// deliberately lenient (constraint: PVE ACL propagation and pool
// inheritance cannot be mirrored exactly from /access/permissions output);
// it exists to catch the common failure of a privsep token with no ACLs,
// which otherwise fails silently with empty API responses.
func Missing(perms proxmox.Permissions, checks []PrivCheck) []string {
	granted := map[string]bool{}
	for _, privs := range perms {
		for priv, ok := range privs {
			if bool(ok) {
				granted[priv] = true
			}
		}
	}
	var missing []string
	seen := map[string]bool{}
	for _, c := range checks {
		if !granted[c.Priv] && !seen[c.Priv] {
			missing = append(missing, c.Priv)
			seen[c.Priv] = true
		}
	}
	return missing
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/pve/ ./pkg/validate/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/pve/access.go pkg/pve/access_test.go pkg/validate/
git commit -m "feat: version-aware privilege checks (PVE 8 VM.Monitor vs PVE 9 VM.GuestAgent.Audit)"
```

---

### Task 6: Cluster-wide VM lookup, template resolution, node inventories

**Files:**
- Create: `pkg/pve/resources.go`
- Test: `pkg/pve/resources_test.go`

**Interfaces:**
- Consumes: `Client.px`, classifiers from Task 3.
- Produces:
  - `pve.VMResource{VMID uint64; Name, Node, Type, Status string; Template int}` (json tags matching `/cluster/resources`).
  - `(*Client).ListVMs(ctx) ([]VMResource, error)` — `GET /cluster/resources?type=vm`.
  - `(*Client).GetVM(ctx, node string, vmid int) (*proxmox.VirtualMachine, error)`.
  - `(*Client).ResolveTemplate(ctx, ref string) (*proxmox.VirtualMachine, error)` — ref is a numeric VMID or a unique template name; errors list available template names when not found.
  - `(*Client).FindVMByNameAndTag(ctx, name, tag string) (*proxmox.VirtualMachine, error)` — `(nil, nil)` when no match.
  - `pve.StorageInfo{Name, Type, Content string; Shared, Active bool}`; `(*Client).NodeStorages(ctx, node string) ([]StorageInfo, error)`; `(*Client).NodeBridges(ctx, node string) ([]string, error)`.

- [ ] **Step 1: Write the failing tests**

`pkg/pve/resources_test.go`:

```go
package pve

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

// registerVM registers the two routes node.VirtualMachine() fetches.
func registerVM(s *pvetest.Server, node string, vmid int, status map[string]interface{}, config map[string]interface{}) {
	s.Handle("GET", "/nodes/"+node+"/status", 200, map[string]interface{}{})
	s.Handle("GET", nodeQemuPath(node, vmid)+"/status/current", 200, status)
	s.Handle("GET", nodeQemuPath(node, vmid)+"/config", 200, config)
}

func templateFixture(s *pvetest.Server) {
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{
		{"vmid": 9000, "name": "ubuntu-2404-tmpl", "node": "pve1", "type": "qemu", "template": 1, "status": "stopped"},
		{"vmid": 105, "name": "some-vm", "node": "pve1", "type": "qemu", "template": 0, "status": "running"},
	})
	registerVM(s, "pve1", 9000,
		map[string]interface{}{"status": "stopped", "vmid": 9000, "name": "ubuntu-2404-tmpl", "template": 1},
		map[string]interface{}{
			"name": "ubuntu-2404-tmpl", "template": 1, "agent": "1",
			"boot": "order=scsi0;ide2;net0",
			"scsi0": "local-lvm:base-9000-disk-0,size=20G",
			"ide2":  "local-lvm:vm-9000-cloudinit,media=cdrom",
			"net0":  "virtio=DE:AD:BE:EF:12:34,bridge=vmbr0",
		})
}

func TestResolveTemplateByName(t *testing.T) {
	s := pvetest.New(t)
	templateFixture(s)
	c := newTestClient(t, s)

	vm, err := c.ResolveTemplate(context.Background(), "ubuntu-2404-tmpl")
	require.NoError(t, err)
	assert.Equal(t, 9000, int(vm.VMID))
	assert.Equal(t, "pve1", vm.Node)
}

func TestResolveTemplateByVMID(t *testing.T) {
	s := pvetest.New(t)
	templateFixture(s)
	c := newTestClient(t, s)

	vm, err := c.ResolveTemplate(context.Background(), "9000")
	require.NoError(t, err)
	assert.Equal(t, 9000, int(vm.VMID))
}

func TestResolveTemplateNotFoundListsAvailable(t *testing.T) {
	s := pvetest.New(t)
	templateFixture(s)
	c := newTestClient(t, s)

	_, err := c.ResolveTemplate(context.Background(), "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ubuntu-2404-tmpl") // actionable: names what exists
}

func TestResolveTemplateRejectsNonTemplate(t *testing.T) {
	s := pvetest.New(t)
	templateFixture(s)
	c := newTestClient(t, s)

	_, err := c.ResolveTemplate(context.Background(), "some-vm")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a template")
}

func TestFindVMByNameAndTag(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{
		{"vmid": 10005, "name": "c1-pool1-abcde", "node": "pve1", "type": "qemu", "template": 0, "status": "running"},
	})
	registerVM(s, "pve1", 10005,
		map[string]interface{}{"status": "running", "vmid": 10005, "name": "c1-pool1-abcde", "tags": "rancher-pvenode"},
		map[string]interface{}{"name": "c1-pool1-abcde", "tags": "rancher-pvenode"})
	c := newTestClient(t, s)

	vm, err := c.FindVMByNameAndTag(context.Background(), "c1-pool1-abcde", "rancher-pvenode")
	require.NoError(t, err)
	require.NotNil(t, vm)
	assert.Equal(t, 10005, int(vm.VMID))

	vm, err = c.FindVMByNameAndTag(context.Background(), "missing", "rancher-pvenode")
	require.NoError(t, err)
	assert.Nil(t, vm)
}

func TestNodeInventories(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/nodes/pve1/storage", 200, []map[string]interface{}{
		{"storage": "local-lvm", "type": "lvmthin", "content": "images,rootdir", "shared": 0, "active": 1},
		{"storage": "cephpool", "type": "rbd", "content": "images", "shared": 1, "active": 1},
	})
	s.Handle("GET", "/nodes/pve1/network", 200, []map[string]interface{}{
		{"iface": "vmbr0", "type": "bridge"},
		{"iface": "eno1", "type": "eth"},
	})
	c := newTestClient(t, s)

	storages, err := c.NodeStorages(context.Background(), "pve1")
	require.NoError(t, err)
	require.Len(t, storages, 2)
	assert.Equal(t, "lvmthin", storages[0].Type)
	assert.True(t, storages[1].Shared)

	bridges, err := c.NodeBridges(context.Background(), "pve1")
	require.NoError(t, err)
	assert.Equal(t, []string{"vmbr0"}, bridges)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/pve/ -v`
Expected: FAIL (undefined: `ResolveTemplate`, `nodeQemuPath`, ...).

- [ ] **Step 3: Implement resources.go**

`pkg/pve/resources.go`:

```go
package pve

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	proxmox "github.com/luthermonson/go-proxmox"
)

// VMResource is one row of GET /cluster/resources?type=vm.
type VMResource struct {
	VMID     uint64 `json:"vmid"`
	Name     string `json:"name"`
	Node     string `json:"node"`
	Type     string `json:"type"`
	Status   string `json:"status"`
	Template int    `json:"template"`
}

// StorageInfo is one row of GET /nodes/{node}/storage.
type StorageInfo struct {
	Name    string
	Type    string
	Content string
	Shared  bool
	Active  bool
}

type storageRow struct {
	Storage string `json:"storage"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Shared  int    `json:"shared"`
	Active  int    `json:"active"`
}

type networkRow struct {
	Iface string `json:"iface"`
	Type  string `json:"type"`
}

func nodeQemuPath(node string, vmid int) string {
	return fmt.Sprintf("/nodes/%s/qemu/%d", node, vmid)
}

func (c *Client) ListVMs(ctx context.Context) ([]VMResource, error) {
	var rows []VMResource
	if err := c.px.Get(ctx, "/cluster/resources?type=vm", &rows); err != nil {
		return nil, fmt.Errorf("listing cluster VMs: %w", err)
	}
	return rows, nil
}

func (c *Client) GetVM(ctx context.Context, node string, vmid int) (*proxmox.VirtualMachine, error) {
	n, err := c.px.Node(ctx, node)
	if err != nil {
		return nil, fmt.Errorf("getting PVE node %q: %w", node, err)
	}
	vm, err := n.VirtualMachine(ctx, vmid)
	if err != nil {
		return nil, fmt.Errorf("getting VM %d on node %q: %w", vmid, node, err)
	}
	return vm, nil
}

// ResolveTemplate finds the clone source. ref may be a numeric VMID or a
// template name (must be unique among templates).
func (c *Client) ResolveTemplate(ctx context.Context, ref string) (*proxmox.VirtualMachine, error) {
	rows, err := c.ListVMs(ctx)
	if err != nil {
		return nil, err
	}
	var match *VMResource
	var templateNames []string
	refVMID, refIsNumeric := 0, false
	if n, err := strconv.Atoi(ref); err == nil {
		refVMID, refIsNumeric = n, true
	}
	for i := range rows {
		row := &rows[i]
		if row.Type != "qemu" {
			continue
		}
		if row.Template == 1 {
			templateNames = append(templateNames, row.Name)
		}
		if (refIsNumeric && int(row.VMID) == refVMID) || (!refIsNumeric && row.Name == ref) {
			if match != nil {
				return nil, fmt.Errorf("template name %q is ambiguous (VMIDs %d and %d) — use the VMID instead", ref, match.VMID, row.VMID)
			}
			match = row
		}
	}
	if match == nil {
		sort.Strings(templateNames)
		return nil, fmt.Errorf("template %q not found; available templates: %s", ref, strings.Join(templateNames, ", "))
	}
	if match.Template != 1 {
		return nil, fmt.Errorf("VM %q (VMID %d) is not a template — convert it in PVE first (right-click > Convert to template)", match.Name, match.VMID)
	}
	return c.GetVM(ctx, match.Node, int(match.VMID))
}

// FindVMByNameAndTag is the orphan-recovery lookup used by Remove when the
// persisted VMID is missing (mid-create crash). Returns (nil, nil) when no
// VM matches. The tag match is mandatory: name alone must never be trusted.
func (c *Client) FindVMByNameAndTag(ctx context.Context, name, tag string) (*proxmox.VirtualMachine, error) {
	rows, err := c.ListVMs(ctx)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		row := &rows[i]
		if row.Type != "qemu" || row.Template == 1 || row.Name != name {
			continue
		}
		vm, err := c.GetVM(ctx, row.Node, int(row.VMID))
		if err != nil {
			if IsNotFoundErr(err) {
				continue
			}
			return nil, err
		}
		if vm.HasTag(tag) {
			return vm, nil
		}
	}
	return nil, nil
}

func (c *Client) NodeStorages(ctx context.Context, node string) ([]StorageInfo, error) {
	var rows []storageRow
	if err := c.px.Get(ctx, "/nodes/"+node+"/storage", &rows); err != nil {
		return nil, fmt.Errorf("listing storages on node %q: %w", node, err)
	}
	out := make([]StorageInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, StorageInfo{
			Name: r.Storage, Type: r.Type, Content: r.Content,
			Shared: r.Shared == 1, Active: r.Active == 1,
		})
	}
	return out, nil
}

func (c *Client) NodeBridges(ctx context.Context, node string) ([]string, error) {
	var rows []networkRow
	if err := c.px.Get(ctx, "/nodes/"+node+"/network", &rows); err != nil {
		return nil, fmt.Errorf("listing networks on node %q: %w", node, err)
	}
	var bridges []string
	for _, r := range rows {
		if r.Type == "bridge" {
			bridges = append(bridges, r.Iface)
		}
	}
	sort.Strings(bridges)
	return bridges, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/pve/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/pve/resources.go pkg/pve/resources_test.go
git commit -m "feat: cluster VM lookup, template resolution, node inventories"
```

---

### Task 7: Pure validation rules — template requirements, clone matrix, disk size, machine name

**Files:**
- Create: `pkg/validate/template.go`, `pkg/validate/rules.go`
- Test: `pkg/validate/template_test.go`, `pkg/validate/rules_test.go`

**Interfaces:**
- Consumes: `proxmox.VirtualMachine` / `VirtualMachineConfig` types.
- Produces:
  - `validate.EnsureTemplate(vm *proxmox.VirtualMachine) error` — is a template, agent enabled.
  - `validate.CloudInitDrive(cfg *proxmox.VirtualMachineConfig) (device string, ok bool)`.
  - `validate.NICMAC(cfg *proxmox.VirtualMachineConfig, key string) (string, error)`.
  - `validate.BootDisk(cfg *proxmox.VirtualMachineConfig) (key string, sizeGB int, value string, err error)`.
  - `validate.ValidateCloneMode(linked bool, storage, targetNode, templateNode string, templateStorageShared bool) error`.
  - `validate.LinkedCloneOK(bootDiskValue, storageType string) error`.
  - `validate.ValidateDiskSize(requestedGB, templateGB int) error`.
  - `validate.ValidateMachineName(name string) error`.

- [ ] **Step 1: Write the failing tests**

`pkg/validate/template_test.go`:

```go
package validate

import (
	"testing"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tmplConfig() *proxmox.VirtualMachineConfig {
	return &proxmox.VirtualMachineConfig{
		Template: proxmox.IntOrBool(true),
		Agent:    "1",
		Boot:     "order=scsi0;ide2;net0",
		SCSIs:    map[string]string{"scsi0": "local-lvm:base-9000-disk-0,size=20G"},
		IDEs:     map[string]string{"ide2": "local-lvm:vm-9000-cloudinit,media=cdrom"},
		Nets:     map[string]string{"net0": "virtio=DE:AD:BE:EF:12:34,bridge=vmbr0,firewall=1"},
	}
}

func tmplVM() *proxmox.VirtualMachine {
	return &proxmox.VirtualMachine{
		Template:             true,
		VirtualMachineConfig: tmplConfig(),
	}
}

func TestEnsureTemplateHappy(t *testing.T) {
	assert.NoError(t, EnsureTemplate(tmplVM()))
}

func TestEnsureTemplateRejectsAgentless(t *testing.T) {
	vm := tmplVM()
	vm.VirtualMachineConfig.Agent = ""
	err := EnsureTemplate(vm)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent")
}

func TestEnsureTemplateRejectsAgentDisabled(t *testing.T) {
	vm := tmplVM()
	vm.VirtualMachineConfig.Agent = "0"
	assert.Error(t, EnsureTemplate(vm))
}

func TestCloudInitDrive(t *testing.T) {
	dev, ok := CloudInitDrive(tmplConfig())
	assert.True(t, ok)
	assert.Equal(t, "ide2", dev)

	cfg := tmplConfig()
	cfg.IDEs = nil
	_, ok = CloudInitDrive(cfg)
	assert.False(t, ok)
}

func TestNICMAC(t *testing.T) {
	mac, err := NICMAC(tmplConfig(), "net0")
	require.NoError(t, err)
	assert.Equal(t, "DE:AD:BE:EF:12:34", mac)

	// all NIC models PVE supports for MAC extraction
	for _, model := range []string{"virtio", "e1000", "e1000e", "rtl8139", "vmxnet3"} {
		cfg := tmplConfig()
		cfg.Nets = map[string]string{"net0": model + "=AA:BB:CC:DD:EE:FF,bridge=vmbr1"}
		mac, err := NICMAC(cfg, "net0")
		require.NoError(t, err, model)
		assert.Equal(t, "AA:BB:CC:DD:EE:FF", mac)
	}

	_, err = NICMAC(&proxmox.VirtualMachineConfig{}, "net0")
	assert.Error(t, err)
}

func TestBootDisk(t *testing.T) {
	key, sizeGB, val, err := BootDisk(tmplConfig())
	require.NoError(t, err)
	assert.Equal(t, "scsi0", key)
	assert.Equal(t, 20, sizeGB)
	assert.Contains(t, val, "base-9000-disk-0")
}

func TestBootDiskSkipsCloudInitAndCDROM(t *testing.T) {
	cfg := tmplConfig()
	cfg.Boot = "order=ide2;scsi0"
	key, _, _, err := BootDisk(cfg)
	require.NoError(t, err)
	assert.Equal(t, "scsi0", key) // ide2 is the cloudinit cdrom, not a boot disk
}

func TestBootDiskFallbackWithoutBootOrder(t *testing.T) {
	cfg := tmplConfig()
	cfg.Boot = ""
	key, _, _, err := BootDisk(cfg)
	require.NoError(t, err)
	assert.Equal(t, "scsi0", key)
}

func TestBootDiskSizeUnits(t *testing.T) {
	for value, wantGB := range map[string]int{
		"local-lvm:d0,size=32G":    32,
		"local-lvm:d0,size=2048M":  2,
		"local-lvm:d0,size=1T":     1024,
	} {
		cfg := tmplConfig()
		cfg.SCSIs = map[string]string{"scsi0": value}
		_, sizeGB, _, err := BootDisk(cfg)
		require.NoError(t, err, value)
		assert.Equal(t, wantGB, sizeGB, value)
	}
}
```

`pkg/validate/rules_test.go`:

```go
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
	assert.Error(t, ValidateMachineName(strings.Repeat("a", 64)))    // >63 chars
	assert.Error(t, ValidateMachineName("Under_score"))              // invalid DNS char
	assert.Error(t, ValidateMachineName("-leading-dash"))
	assert.Error(t, ValidateMachineName(""))
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/validate/ -v`
Expected: FAIL (undefined symbols).

- [ ] **Step 3: Implement template.go and rules.go**

`pkg/validate/template.go`:

```go
package validate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	proxmox "github.com/luthermonson/go-proxmox"
)

// nicModels are the PVE NIC model keys whose value in a netN config string
// is the MAC address, e.g. "virtio=DE:AD:BE:EF:12:34,bridge=vmbr0".
var nicModels = map[string]bool{
	"virtio": true, "e1000": true, "e1000e": true, "rtl8139": true, "vmxnet3": true,
}

// EnsureTemplate checks the clone source is a template with the guest
// agent enabled. Without the agent the driver can never learn the VM's
// DHCP address.
func EnsureTemplate(vm *proxmox.VirtualMachine) error {
	if !bool(vm.Template) {
		return fmt.Errorf("VM %q is not a template", vm.Name)
	}
	cfg := vm.VirtualMachineConfig
	if cfg == nil {
		return fmt.Errorf("template %q has no readable config", vm.Name)
	}
	agent := cfg.Agent
	if agent == "" || strings.HasPrefix(agent, "0") || strings.Contains(agent, "enabled=0") {
		return fmt.Errorf(
			"template %q does not have the QEMU guest agent enabled (agent=%q); "+
				"set 'qm set <vmid> --agent 1' AND make sure qemu-guest-agent is installed in the image — "+
				"without it the driver cannot learn the VM's DHCP address", vm.Name, agent)
	}
	return nil
}

// CloudInitDrive finds the cloud-init drive (e.g. ide2 = "storage:vm-N-cloudinit,media=cdrom").
// Without one, ciuser/sshkeys/ipconfig0 are stored but never delivered to the guest.
func CloudInitDrive(cfg *proxmox.VirtualMachineConfig) (string, bool) {
	for _, devices := range []map[string]string{cfg.IDEs, cfg.SCSIs, cfg.SATAs} {
		for key, val := range devices {
			if strings.Contains(val, "cloudinit") {
				return key, true
			}
		}
	}
	return "", false
}

// NICMAC extracts the MAC address of a network device (e.g. "net0") from
// the VM config. The MAC is the value of the NIC-model key.
func NICMAC(cfg *proxmox.VirtualMachineConfig, key string) (string, error) {
	val, ok := cfg.Nets[key]
	if !ok || val == "" {
		return "", fmt.Errorf("VM config has no %s network device", key)
	}
	for _, part := range strings.Split(val, ",") {
		k, v, found := strings.Cut(strings.TrimSpace(part), "=")
		if found && nicModels[k] {
			return strings.ToUpper(v), nil
		}
	}
	return "", fmt.Errorf("could not extract MAC from %s=%q", key, val)
}

var sizeRe = regexp.MustCompile(`(?:^|,)size=(\d+)([MGT])`)

// BootDisk finds the template's boot disk key (e.g. "scsi0"), its size in
// GB, and the raw config value. It follows the boot order, skipping
// cdrom/cloudinit devices, then falls back to conventional first disks.
func BootDisk(cfg *proxmox.VirtualMachineConfig) (string, int, string, error) {
	lookup := func(key string) (string, bool) {
		for _, devices := range []map[string]string{cfg.SCSIs, cfg.VirtIOs, cfg.IDEs, cfg.SATAs} {
			if v, ok := devices[key]; ok {
				return v, true
			}
		}
		return "", false
	}
	isDisk := func(val string) bool {
		return !strings.Contains(val, "media=cdrom") && !strings.Contains(val, "cloudinit")
	}

	var candidates []string
	if order, ok := strings.CutPrefix(cfg.Boot, "order="); ok {
		candidates = strings.Split(order, ";")
	}
	candidates = append(candidates, "scsi0", "virtio0", "ide0", "sata0")

	for _, key := range candidates {
		key = strings.TrimSpace(key)
		val, ok := lookup(key)
		if !ok || !isDisk(val) {
			continue
		}
		m := sizeRe.FindStringSubmatch(val)
		if m == nil {
			return "", 0, "", fmt.Errorf("boot disk %s=%q has no parsable size", key, val)
		}
		n, _ := strconv.Atoi(m[1])
		sizeGB := 0
		switch m[2] {
		case "M":
			sizeGB = (n + 1023) / 1024
		case "G":
			sizeGB = n
		case "T":
			sizeGB = n * 1024
		}
		return key, sizeGB, val, nil
	}
	return "", 0, "", fmt.Errorf("could not find a boot disk in the template config (boot=%q)", cfg.Boot)
}
```

`pkg/validate/rules.go`:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/validate/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/validate/
git commit -m "feat: pure validation rules for template, clone mode, disk size, machine name"
```

---

### Task 8: PreCreateCheck

**Files:**
- Create: `pkg/driver/precreate.go`
- Test: `pkg/driver/precreate_test.go`

**Interfaces:**
- Consumes: everything from Tasks 3–7.
- Produces: `(*Driver).PreCreateCheck() error`, plus the internal helper `(*Driver).resolvePlacement(ctx, c *pve.Client) (*placement, error)` reused by Create in Task 12:

```go
type placement struct {
	Template     *proxmox.VirtualMachine // resolved clone source
	TargetNode   string                  // node the new VM will land on
	BootDiskKey  string                  // e.g. "scsi0"
	TemplateGB   int                     // template boot disk size
}
```

- [ ] **Step 1: Write the failing tests**

`pkg/driver/precreate_test.go`:

```go
package driver

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

// happyServer wires every route PreCreateCheck touches, PVE 9 flavor.
func happyServer(t *testing.T) *pvetest.Server {
	s := pvetest.New(t)
	s.Handle("GET", "/version", 200, map[string]string{"release": "9.2", "version": "9.2.1"})
	s.Handle("GET", "/access/permissions", 200, map[string]map[string]int{
		"/": {
			"VM.Clone": 1, "VM.Allocate": 1, "VM.Audit": 1, "VM.PowerMgmt": 1,
			"VM.Config.Disk": 1, "VM.Config.CPU": 1, "VM.Config.Memory": 1,
			"VM.Config.Network": 1, "VM.Config.Cloudinit": 1, "VM.Config.Options": 1,
			"VM.GuestAgent.Audit": 1,
			"Datastore.AllocateSpace": 1, "Datastore.Audit": 1, "SDN.Use": 1,
		},
	})
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{
		{"vmid": 9000, "name": "ubuntu-2404-tmpl", "node": "pve1", "type": "qemu", "template": 1, "status": "stopped"},
	})
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/9000/status/current", 200, map[string]interface{}{
		"status": "stopped", "vmid": 9000, "name": "ubuntu-2404-tmpl", "template": 1,
	})
	s.Handle("GET", "/nodes/pve1/qemu/9000/config", 200, map[string]interface{}{
		"name": "ubuntu-2404-tmpl", "template": 1, "agent": "1",
		"boot":  "order=scsi0;ide2;net0",
		"scsi0": "local-lvm:base-9000-disk-0,size=20G",
		"ide2":  "local-lvm:vm-9000-cloudinit,media=cdrom",
		"net0":  "virtio=DE:AD:BE:EF:12:34,bridge=vmbr0",
	})
	s.Handle("GET", "/nodes/pve1/storage", 200, []map[string]interface{}{
		{"storage": "local-lvm", "type": "lvmthin", "content": "images,rootdir", "shared": 0, "active": 1},
	})
	s.Handle("GET", "/nodes/pve1/network", 200, []map[string]interface{}{
		{"iface": "vmbr0", "type": "bridge"},
	})
	return s
}

func testDriver(s *pvetest.Server) *Driver {
	d := NewDriver("c1-pool1-abcde", "/tmp/store")
	d.URL = s.URL()
	d.TokenID = "rancher@pve!machine"
	d.TokenSecret = "s3cret"
	d.TemplateRef = "ubuntu-2404-tmpl"
	d.Storage = "local-lvm"
	d.Bridge = "vmbr0"
	d.Cores = 2
	d.MemoryMB = 4096
	d.DiskSizeGB = 40
	d.AgentTimeout = 300
	d.VMIDRange = "10000-19999"
	d.SSHUser = "rancher"
	d.SSHPort = 22
	return d
}

func TestPreCreateCheckHappy(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	assert.NoError(t, d.PreCreateCheck())
}

func TestPreCreateCheckMissingPrivilege(t *testing.T) {
	s := happyServer(t)
	// Override: token has zero ACLs (classic silent privsep failure).
	s.Handle("GET", "/access/permissions", 200, map[string]map[string]int{})
	d := testDriver(s)
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "privilege")
	assert.Contains(t, err.Error(), "VM.Clone")
}

func TestPreCreateCheckBadTemplate(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	d.TemplateRef = "missing-template"
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ubuntu-2404-tmpl") // lists what exists
}

func TestPreCreateCheckNoCloudInitDrive(t *testing.T) {
	s := happyServer(t)
	s.Handle("GET", "/nodes/pve1/qemu/9000/config", 200, map[string]interface{}{
		"name": "ubuntu-2404-tmpl", "template": 1, "agent": "1",
		"boot":  "order=scsi0",
		"scsi0": "local-lvm:base-9000-disk-0,size=20G",
		"net0":  "virtio=DE:AD:BE:EF:12:34,bridge=vmbr0",
	})
	d := testDriver(s)
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cloud-init")
}

func TestPreCreateCheckDiskShrink(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	d.DiskSizeGB = 10 // template is 20G
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shrink")
}

func TestPreCreateCheckUnknownBridge(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	d.Bridge = "vmbr9"
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "vmbr0") // lists available bridges
}

func TestPreCreateCheckUnknownStorage(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	d.Storage = "nope"
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local-lvm")
}

func TestPreCreateCheckLinkedCloneWithStorage(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	d.LinkedClone = true // storage already set → invalid combination
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "linked")
}

func TestPreCreateCheckBadMachineName(t *testing.T) {
	s := happyServer(t)
	d := testDriver(s)
	d.MachineName = "Invalid_Name_With_Underscores_That_Is_Also_Way_Too_Long_For_A_Hostname_Really"
	err := d.PreCreateCheck()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hostname")
}

func TestPreCreateCheckPermissionEndpointUnavailable(t *testing.T) {
	// If /access/permissions itself errors, warn and continue (some
	// realms restrict it) — do not block creation.
	s := happyServer(t)
	s.HandleFunc("GET", "/access/permissions", func(w http.ResponseWriter, r *http.Request) {
		pvetest.PVEError(w, 501, "not implemented")
	})
	d := testDriver(s)
	assert.NoError(t, d.PreCreateCheck())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/driver/ -run TestPreCreate -v`
Expected: FAIL (`PreCreateCheck` from BaseDriver is a no-op nil → assertions on errors fail; `resolvePlacement` undefined).

- [ ] **Step 3: Implement precreate.go**

`pkg/driver/precreate.go`:

```go
package driver

import (
	"context"
	"fmt"
	"strings"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/log"

	"github.com/14f3v/pve-rancher-node-driver/pkg/pve"
	"github.com/14f3v/pve-rancher-node-driver/pkg/validate"
)

type placement struct {
	Template    *proxmox.VirtualMachine
	TargetNode  string
	BootDiskKey string
	TemplateGB  int
}

// PreCreateCheck validates everything BEFORE any VM exists, so
// misconfiguration surfaces as one actionable error in the Rancher
// provisioning log instead of a half-created VM.
func (d *Driver) PreCreateCheck() error {
	ctx := context.Background()
	c, err := d.client()
	if err != nil {
		return err
	}

	if err := validate.ValidateMachineName(d.MachineName); err != nil {
		return err
	}

	major, err := c.PVEMajorVersion(ctx)
	if err != nil {
		return err
	}
	log.Infof("pvenode: connected to PVE %d.x at %s", major, d.URL)

	// Token privilege check first: privsep tokens without ACLs fail
	// SILENTLY (empty lists everywhere), so every later error would lie.
	if perms, err := c.TokenPermissions(ctx); err != nil {
		log.Warnf("pvenode: could not query token permissions (%v) — skipping privilege pre-check", err)
	} else {
		checks := validate.RequiredChecks(major, d.Storage, d.Bridge, d.ResourcePool)
		if missing := validate.Missing(perms, checks); len(missing) > 0 {
			return fmt.Errorf(
				"the API token is missing privileges: %s. Grant them via a role on the token itself "+
					"(privsep tokens do NOT inherit the user's permissions) or recreate the token with privsep=0. "+
					"See the driver README for ready-made PVE %d role definitions",
				strings.Join(missing, ", "), major)
		}
	}

	if _, err := d.resolvePlacement(ctx, c); err != nil {
		return err
	}
	return nil
}

// resolvePlacement resolves and validates the template, target node,
// storage, bridge and disk sizing. Reused by Create.
func (d *Driver) resolvePlacement(ctx context.Context, c *pve.Client) (*placement, error) {
	tmpl, err := c.ResolveTemplate(ctx, d.TemplateRef)
	if err != nil {
		return nil, err
	}
	if err := validate.EnsureTemplate(tmpl); err != nil {
		return nil, err
	}
	cfg := tmpl.VirtualMachineConfig
	if _, ok := validate.CloudInitDrive(cfg); !ok {
		return nil, fmt.Errorf(
			"template %q has no cloud-init drive — without one, user/SSH-key/network settings are never "+
				"delivered to the guest. Add one in PVE: qm set %d --ide2 <storage>:cloudinit",
			tmpl.Name, int(tmpl.VMID))
	}
	if _, err := validate.NICMAC(cfg, "net0"); err != nil {
		return nil, fmt.Errorf("template %q: %w", tmpl.Name, err)
	}

	bootKey, templateGB, bootVal, err := validate.BootDisk(cfg)
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", tmpl.Name, err)
	}
	if err := validate.ValidateDiskSize(d.DiskSizeGB, templateGB); err != nil {
		return nil, err
	}

	targetNode := d.NodeName
	if targetNode == "" {
		targetNode = tmpl.Node
	}

	storages, err := c.NodeStorages(ctx, targetNode)
	if err != nil {
		return nil, err
	}
	templateStorageID, _, _ := strings.Cut(bootVal, ":")
	var templateStorage, requestedStorage *pve.StorageInfo
	var names []string
	for i := range storages {
		st := &storages[i]
		names = append(names, st.Name)
		if st.Name == templateStorageID {
			templateStorage = st
		}
		if d.Storage != "" && st.Name == d.Storage {
			requestedStorage = st
		}
	}
	if d.Storage != "" {
		if requestedStorage == nil {
			return nil, fmt.Errorf("storage %q not found on node %q; available: %s",
				d.Storage, targetNode, strings.Join(names, ", "))
		}
		if !strings.Contains(requestedStorage.Content, "images") {
			return nil, fmt.Errorf("storage %q on node %q cannot hold VM disks (content=%q)",
				d.Storage, targetNode, requestedStorage.Content)
		}
	}

	templateShared := templateStorage != nil && templateStorage.Shared
	if err := validate.ValidateCloneMode(d.LinkedClone, d.Storage, d.NodeName, tmpl.Node, templateShared); err != nil {
		return nil, err
	}
	if d.LinkedClone && templateStorage != nil {
		if err := validate.LinkedCloneOK(bootVal, templateStorage.Type); err != nil {
			return nil, err
		}
	}

	if d.Bridge != "" {
		bridges, err := c.NodeBridges(ctx, targetNode)
		if err != nil {
			return nil, err
		}
		found := false
		for _, b := range bridges {
			if b == d.Bridge {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("bridge %q not found on node %q; available bridges: %s",
				d.Bridge, targetNode, strings.Join(bridges, ", "))
		}
	}

	return &placement{
		Template:    tmpl,
		TargetNode:  targetNode,
		BootDiskKey: bootKey,
		TemplateGB:  templateGB,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/driver/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/driver/precreate.go pkg/driver/precreate_test.go
git commit -m "feat: fail-fast PreCreateCheck with actionable errors"
```

---

### Task 9: VMID allocation and clone with conflict/lock retries + ownership tag

**Files:**
- Create: `pkg/pve/clone.go`
- Test: `pkg/pve/clone_test.go`

**Interfaces:**
- Consumes: `ListVMs`, `GetVM`, `WaitTask`, `Retry`, classifiers.
- Produces:

```go
type CloneSpec struct {
	Name       string   // new VM name = Rancher machine name
	TargetNode string   // "" = template's node
	Storage    string   // full clones only
	Linked     bool
	Pool       string
	VMIDLo     int
	VMIDHi     int
	Tags       []string // ownership tag + extra tags, applied right after clone
}
// CloneFromTemplate clones, waits for the clone task, applies tags, and
// returns the new VM (fetched from its node). On clone-task failure it
// best-effort-deletes the partial VM before returning the error.
func (c *Client) CloneFromTemplate(ctx context.Context, tmpl *proxmox.VirtualMachine, spec CloneSpec) (*proxmox.VirtualMachine, error)
```

- [ ] **Step 1: Write the failing tests**

`pkg/pve/clone_test.go`:

```go
package pve

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

// cloneFixture returns a server with a template on pve1 and a task-status
// route; the clone handler is pluggable.
func cloneFixture(t *testing.T, cloneHandler http.HandlerFunc) (*pvetest.Server, *Client) {
	s := pvetest.New(t)
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{
		{"vmid": 9000, "name": "tmpl", "node": "pve1", "type": "qemu", "template": 1, "status": "stopped"},
	})
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/9000/status/current", 200, map[string]interface{}{
		"status": "stopped", "vmid": 9000, "name": "tmpl", "template": 1,
	})
	s.Handle("GET", "/nodes/pve1/qemu/9000/config", 200, map[string]interface{}{
		"name": "tmpl", "template": 1, "agent": "1",
		"scsi0": "local-lvm:base-9000-disk-0,size=20G",
		"ide2":  "local-lvm:vm-9000-cloudinit,media=cdrom",
		"net0":  "virtio=DE:AD:BE:EF:12:34,bridge=vmbr0",
	})
	s.HandleFunc("POST", "/nodes/pve1/qemu/9000/clone", cloneHandler)
	c := newTestClient(t, s)
	return s, c
}

// registerNewVM makes the freshly cloned VM fetchable and lets Config
// (tags) succeed.
func registerNewVM(s *pvetest.Server, vmid int, upid string) {
	path := nodeQemuPath("pve1", vmid)
	s.Handle("GET", path+"/status/current", 200, map[string]interface{}{
		"status": "stopped", "vmid": vmid, "name": "new-vm",
	})
	s.Handle("GET", path+"/config", 200, map[string]interface{}{
		"name": "new-vm",
		"net0": "virtio=AA:BB:CC:DD:EE:01,bridge=vmbr0",
	})
	s.Handle("POST", path+"/config", 200, upid)
}

func TestCloneHappyPath(t *testing.T) {
	var s *pvetest.Server
	var upid string
	var clonedID atomic.Int64
	s, c := cloneFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		clonedID.Store(int64(body["newid"].(float64)))
		registerNewVM(s, int(clonedID.Load()), upid)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": upid})
	})
	upid = s.OKTask("pve1")

	tmpl, err := c.GetVM(context.Background(), "pve1", 9000)
	require.NoError(t, err)

	vm, err := c.CloneFromTemplate(context.Background(), tmpl, CloneSpec{
		Name: "new-vm", VMIDLo: 10000, VMIDHi: 19999,
		Tags: []string{"rancher-pvenode"},
	})
	require.NoError(t, err)
	got := int(clonedID.Load())
	assert.Equal(t, got, int(vm.VMID))
	assert.GreaterOrEqual(t, got, 10000)
	assert.LessOrEqual(t, got, 19999)
}

func TestCloneRetriesOnVMIDConflict(t *testing.T) {
	var s *pvetest.Server
	var upid string
	var calls atomic.Int32
	s, c := cloneFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			pvetest.PVEError(w, 500, "unable to create VM - config file already exists")
			return
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		registerNewVM(s, int(body["newid"].(float64)), upid)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": upid})
	})
	upid = s.OKTask("pve1")

	tmpl, err := c.GetVM(context.Background(), "pve1", 9000)
	require.NoError(t, err)

	_, err = c.CloneFromTemplate(context.Background(), tmpl, CloneSpec{
		Name: "new-vm", VMIDLo: 10000, VMIDHi: 19999, Tags: []string{"rancher-pvenode"},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load())
}

func TestCloneRetriesOnTemplateLock(t *testing.T) {
	var s *pvetest.Server
	var upid string
	var calls atomic.Int32
	s, c := cloneFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			pvetest.PVEError(w, 500, "got lock request timeout")
			return
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		registerNewVM(s, int(body["newid"].(float64)), upid)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": upid})
	})
	upid = s.OKTask("pve1")

	tmpl, err := c.GetVM(context.Background(), "pve1", 9000)
	require.NoError(t, err)

	_, err = c.CloneFromTemplate(context.Background(), tmpl, CloneSpec{
		Name: "new-vm", VMIDLo: 10000, VMIDHi: 19999, Tags: []string{"rancher-pvenode"},
	})
	require.NoError(t, err)
	assert.Equal(t, int32(2), calls.Load())
}

func TestCloneAvoidsUsedVMIDs(t *testing.T) {
	// Range with exactly one free ID: existing VMs occupy the rest.
	existing := []map[string]interface{}{
		{"vmid": 9000, "name": "tmpl", "node": "pve1", "type": "qemu", "template": 1, "status": "stopped"},
		{"vmid": 100, "name": "used-a", "node": "pve1", "type": "qemu", "template": 0, "status": "running"},
		{"vmid": 101, "name": "used-b", "node": "pve1", "type": "qemu", "template": 0, "status": "running"},
	}
	var s *pvetest.Server
	var upid string
	s, c := cloneFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		assert.Equal(t, 102.0, body["newid"], "must pick the only free VMID in range")
		registerNewVM(s, 102, upid)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": upid})
	})
	s.Handle("GET", "/cluster/resources", 200, existing)
	upid = s.OKTask("pve1")

	tmpl, err := c.GetVM(context.Background(), "pve1", 9000)
	require.NoError(t, err)

	_, err = c.CloneFromTemplate(context.Background(), tmpl, CloneSpec{
		Name: "new-vm", VMIDLo: 100, VMIDHi: 102, Tags: []string{"rancher-pvenode"},
	})
	require.NoError(t, err)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/pve/ -run TestClone -v`
Expected: FAIL (`CloneSpec`, `CloneFromTemplate` undefined).

- [ ] **Step 3: Implement clone.go**

`pkg/pve/clone.go`:

```go
package pve

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/log"
)

type CloneSpec struct {
	Name       string
	TargetNode string
	Storage    string
	Linked     bool
	Pool       string
	VMIDLo     int
	VMIDHi     int
	Tags       []string
}

const (
	cloneAttempts    = 5
	cloneTaskTimeout = 15 * time.Minute // full clones of big templates are slow
	tagTaskTimeout   = 2 * time.Minute
)

// CloneFromTemplate clones tmpl into a new VM and stamps the ownership
// tags. VMIDs are picked RANDOMLY in [VMIDLo, VMIDHi]: /cluster/nextid is
// not atomic, and Rancher creates pool nodes in parallel — random+retry is
// the accepted fix. Retried errors: VMID conflicts (new random ID) and
// template/cfs lock contention (same ID, backoff with jitter).
func (c *Client) CloneFromTemplate(ctx context.Context, tmpl *proxmox.VirtualMachine, spec CloneSpec) (*proxmox.VirtualMachine, error) {
	targetNode := spec.TargetNode
	if targetNode == "" {
		targetNode = tmpl.Node
	}

	var vmid int
	err := Retry(ctx, cloneAttempts, 2*time.Second,
		func(err error) bool { return IsVMIDConflict(err) || IsLockErr(err) || IsTransient(err) },
		func() error {
			id, err := c.randomFreeVMID(ctx, spec.VMIDLo, spec.VMIDHi)
			if err != nil {
				return err
			}
			log.Infof("pvenode: cloning template %d -> VM %d (%s) on node %s", int(tmpl.VMID), id, spec.Name, targetNode)
			opts := &proxmox.VirtualMachineCloneOptions{
				NewID: id,
				Name:  spec.Name,
				Full:  proxmox.IntOrBool(!spec.Linked),
				Pool:  spec.Pool,
			}
			if spec.Storage != "" {
				opts.Storage = spec.Storage
			}
			if spec.TargetNode != "" && spec.TargetNode != tmpl.Node {
				opts.Target = spec.TargetNode
			}
			newid, task, err := tmpl.Clone(ctx, opts)
			if err != nil {
				return err
			}
			if err := c.WaitTask(ctx, task, cloneTaskTimeout); err != nil {
				// Partial VM possible after a failed clone task: best-effort cleanup.
				c.bestEffortDelete(ctx, targetNode, newid)
				return err
			}
			vmid = newid
			return nil
		})
	if err != nil {
		return nil, fmt.Errorf("cloning template %q: %w", tmpl.Name, err)
	}

	vm, err := c.GetVM(ctx, targetNode, vmid)
	if err != nil {
		return nil, fmt.Errorf("clone succeeded but VM %d is not readable: %w", vmid, err)
	}

	if len(spec.Tags) > 0 {
		task, err := vm.Config(ctx, proxmox.VirtualMachineOption{
			Name: "tags", Value: strings.Join(spec.Tags, ";"),
		})
		if err == nil {
			err = c.WaitTask(ctx, task, tagTaskTimeout)
		}
		if err != nil {
			c.bestEffortDelete(ctx, targetNode, vmid)
			return nil, fmt.Errorf("tagging VM %d: %w", vmid, err)
		}
	}
	return vm, nil
}

// randomFreeVMID picks a random VMID in [lo, hi] not currently used by any
// VM or template. A racing allocation elsewhere still surfaces as a clone
// "config file already exists" error, which the caller retries.
func (c *Client) randomFreeVMID(ctx context.Context, lo, hi int) (int, error) {
	rows, err := c.ListVMs(ctx)
	if err != nil {
		return 0, err
	}
	used := make(map[int]bool, len(rows))
	for _, r := range rows {
		used[int(r.VMID)] = true
	}
	span := hi - lo + 1
	free := span - countUsedIn(used, lo, hi)
	if free <= 0 {
		return 0, fmt.Errorf("no free VMID in range %d-%d (%d in use) — widen --pvenode-vmid-range", lo, hi, span)
	}
	for {
		id := lo + rand.Intn(span)
		if !used[id] {
			return id, nil
		}
	}
}

func countUsedIn(used map[int]bool, lo, hi int) int {
	n := 0
	for id := range used {
		if id >= lo && id <= hi {
			n++
		}
	}
	return n
}

// bestEffortDelete removes a partial VM after a failed create step; errors
// are logged, not returned (the original failure matters more).
func (c *Client) bestEffortDelete(ctx context.Context, node string, vmid int) {
	if vmid == 0 {
		return
	}
	vm, err := c.GetVM(ctx, node, vmid)
	if err != nil {
		if !IsNotFoundErr(err) {
			log.Warnf("pvenode: cleanup: could not read partial VM %d: %v", vmid, err)
		}
		return
	}
	task, err := vm.Delete(ctx, &proxmox.VirtualMachineDeleteOptions{
		Purge:                    proxmox.IntOrBool(true),
		DestroyUnreferencedDisks: proxmox.IntOrBool(true),
	})
	if err == nil {
		err = c.WaitTask(ctx, task, 5*time.Minute)
	}
	if err != nil {
		log.Warnf("pvenode: cleanup of partial VM %d failed (delete it manually in PVE): %v", vmid, err)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/pve/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/pve/clone.go pkg/pve/clone_test.go
git commit -m "feat: clone with random VMID allocation, conflict/lock retries, ownership tag"
```

---

### Task 10: VM operations — configure, resize, start, shutdown, stop, delete

**Files:**
- Create: `pkg/pve/vmops.go`
- Test: `pkg/pve/vmops_test.go`

**Interfaces:**
- Consumes: `WaitTask`, `Retry`, classifiers.
- Produces:
  - `(*Client).ApplyConfig(ctx, vm *proxmox.VirtualMachine, opts []proxmox.VirtualMachineOption) error`
  - `(*Client).ResizeVMDisk(ctx, vm *proxmox.VirtualMachine, disk string, sizeGB int) error`
  - `(*Client).StartVM(ctx, vm) error`
  - `(*Client).ShutdownVM(ctx, vm, timeout time.Duration) error` (graceful)
  - `(*Client).StopVM(ctx, vm) error` (hard)
  - `(*Client).DeleteVM(ctx, vm) error` — purge + destroy-unreferenced-disks, lock-retried.

- [ ] **Step 1: Write the failing tests**

`pkg/pve/vmops_test.go`:

```go
package pve

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

// vmFixture returns a running VM at pve1/10005 plus its client.
func vmFixture(t *testing.T) (*pvetest.Server, *Client, *proxmox.VirtualMachine) {
	s := pvetest.New(t)
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/10005/status/current", 200, map[string]interface{}{
		"status": "running", "vmid": 10005, "name": "m1", "tags": "rancher-pvenode",
	})
	s.Handle("GET", "/nodes/pve1/qemu/10005/config", 200, map[string]interface{}{
		"name": "m1", "net0": "virtio=AA:BB:CC:DD:EE:01,bridge=vmbr0",
	})
	c := newTestClient(t, s)
	vm, err := c.GetVM(context.Background(), "pve1", 10005)
	require.NoError(t, err)
	return s, c, vm
}

func TestApplyConfig(t *testing.T) {
	s, c, vm := vmFixture(t)
	upid := s.OKTask("pve1")
	s.Handle("POST", "/nodes/pve1/qemu/10005/config", 200, upid)

	err := c.ApplyConfig(context.Background(), vm, []proxmox.VirtualMachineOption{
		{Name: "cores", Value: 2},
	})
	assert.NoError(t, err)
}

func TestApplyConfigTaskFailure(t *testing.T) {
	s, c, vm := vmFixture(t)
	upid := s.FailedTask("pve1", "hotplug problem")
	s.Handle("POST", "/nodes/pve1/qemu/10005/config", 200, upid)

	err := c.ApplyConfig(context.Background(), vm, []proxmox.VirtualMachineOption{
		{Name: "cores", Value: 2},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hotplug problem")
}

func TestResizeVMDisk(t *testing.T) {
	s, c, vm := vmFixture(t)
	upid := s.OKTask("pve1")
	s.Handle("PUT", "/nodes/pve1/qemu/10005/resize", 200, upid)

	assert.NoError(t, c.ResizeVMDisk(context.Background(), vm, "scsi0", 40))
}

func TestStartStopShutdown(t *testing.T) {
	s, c, vm := vmFixture(t)
	upid := s.OKTask("pve1")
	s.Handle("POST", "/nodes/pve1/qemu/10005/status/start", 200, upid)
	s.Handle("POST", "/nodes/pve1/qemu/10005/status/shutdown", 200, upid)
	s.Handle("POST", "/nodes/pve1/qemu/10005/status/stop", 200, upid)

	ctx := context.Background()
	assert.NoError(t, c.StartVM(ctx, vm))
	assert.NoError(t, c.ShutdownVM(ctx, vm, 30*time.Second))
	assert.NoError(t, c.StopVM(ctx, vm))
}

func TestDeleteVMRetriesLock(t *testing.T) {
	s, c, vm := vmFixture(t)
	upid := s.OKTask("pve1")
	var calls atomic.Int32
	s.HandleFunc("DELETE", "/nodes/pve1/qemu/10005", func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			pvetest.PVEError(w, 500, "can't lock file '/var/lock/qemu-server/lock-10005.conf' - got timeout")
			return
		}
		// purge params must be present
		q := r.URL.Query()
		assert.Equal(t, "1", q.Get("purge"))
		assert.Equal(t, "1", q.Get("destroy-unreferenced-disks"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"` + upid + `"}`))
	})

	assert.NoError(t, c.DeleteVM(context.Background(), vm))
	assert.Equal(t, int32(2), calls.Load())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/pve/ -run 'TestApplyConfig|TestResize|TestStartStop|TestDeleteVM' -v`
Expected: FAIL (undefined symbols).

- [ ] **Step 3: Implement vmops.go**

`pkg/pve/vmops.go`:

```go
package pve

import (
	"context"
	"fmt"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
)

const (
	configTaskTimeout = 2 * time.Minute
	resizeTaskTimeout = 5 * time.Minute
	startTaskTimeout  = 2 * time.Minute
	stopTaskTimeout   = 2 * time.Minute
	deleteTaskTimeout = 10 * time.Minute
	deleteAttempts    = 5
)

func (c *Client) ApplyConfig(ctx context.Context, vm *proxmox.VirtualMachine, opts []proxmox.VirtualMachineOption) error {
	task, err := vm.Config(ctx, opts...)
	if err != nil {
		return fmt.Errorf("configuring VM %d: %w", int(vm.VMID), err)
	}
	if err := c.WaitTask(ctx, task, configTaskTimeout); err != nil {
		return fmt.Errorf("configuring VM %d: %w", int(vm.VMID), err)
	}
	return nil
}

func (c *Client) ResizeVMDisk(ctx context.Context, vm *proxmox.VirtualMachine, disk string, sizeGB int) error {
	task, err := vm.ResizeDisk(ctx, disk, fmt.Sprintf("%dG", sizeGB))
	if err != nil {
		return fmt.Errorf("resizing disk %s of VM %d to %dG: %w", disk, int(vm.VMID), sizeGB, err)
	}
	if err := c.WaitTask(ctx, task, resizeTaskTimeout); err != nil {
		return fmt.Errorf("resizing disk %s of VM %d: %w", disk, int(vm.VMID), err)
	}
	return nil
}

func (c *Client) StartVM(ctx context.Context, vm *proxmox.VirtualMachine) error {
	task, err := vm.Start(ctx)
	if err != nil {
		return fmt.Errorf("starting VM %d: %w", int(vm.VMID), err)
	}
	return c.WaitTask(ctx, task, startTaskTimeout)
}

// ShutdownVM asks the guest to shut down cleanly and waits up to timeout.
func (c *Client) ShutdownVM(ctx context.Context, vm *proxmox.VirtualMachine, timeout time.Duration) error {
	task, err := vm.Shutdown(ctx)
	if err != nil {
		return fmt.Errorf("shutting down VM %d: %w", int(vm.VMID), err)
	}
	return c.WaitTask(ctx, task, timeout)
}

// StopVM is a hard power-off.
func (c *Client) StopVM(ctx context.Context, vm *proxmox.VirtualMachine) error {
	task, err := vm.Stop(ctx)
	if err != nil {
		return fmt.Errorf("stopping VM %d: %w", int(vm.VMID), err)
	}
	return c.WaitTask(ctx, task, stopTaskTimeout)
}

// DeleteVM destroys the VM with disk purge, retrying lock contention.
func (c *Client) DeleteVM(ctx context.Context, vm *proxmox.VirtualMachine) error {
	return Retry(ctx, deleteAttempts, 2*time.Second,
		func(err error) bool { return IsLockErr(err) || IsTransient(err) },
		func() error {
			task, err := vm.Delete(ctx, &proxmox.VirtualMachineDeleteOptions{
				Purge:                    proxmox.IntOrBool(true),
				DestroyUnreferencedDisks: proxmox.IntOrBool(true),
			})
			if err != nil {
				return err
			}
			return c.WaitTask(ctx, task, deleteTaskTimeout)
		})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/pve/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/pve/vmops.go pkg/pve/vmops_test.go
git commit -m "feat: VM operations with task waits and lock-retried delete"
```

---

### Task 11: MAC-matched agent IP wait with SSH reachability probe

**Files:**
- Create: `pkg/pve/agentip.go`
- Test: `pkg/pve/agentip_test.go`

**Interfaces:**
- Consumes: `validate.NICMAC`, classifiers.
- Produces:
  - `(*Client).WaitForIP(ctx context.Context, vm *proxmox.VirtualMachine, sshPort int, timeout time.Duration) (string, error)` — the create-time wait: MAC-matched, IPv4-only, TCP-probed.
  - `(*Client).QueryAgentIP(ctx context.Context, vm *proxmox.VirtualMachine) (string, error)` — single-shot, used by GetIP.
  - `pickIPv4(ifaces []*proxmox.AgentNetworkIface, mac string) string` (unexported; table-tested).

- [ ] **Step 1: Write the failing tests**

`pkg/pve/agentip_test.go`:

```go
package pve

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

func iface(name, mac string, addrs ...map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"name": name, "hardware-address": mac, "ip-addresses": addrs,
	}
}

func addr(kind, ip string) map[string]interface{} {
	return map[string]interface{}{"ip-address-type": kind, "ip-address": ip, "prefix": 24}
}

// The adversarial fixture set from the spec: docker/cni bridges, IPv6
// first, agent answering before DHCP finished.
func TestPickIPv4Adversarial(t *testing.T) {
	mac := "AA:BB:CC:DD:EE:01"
	toIfaces := func(raw []map[string]interface{}) []*proxmox.AgentNetworkIface {
		// build typed structs matching the JSON the agent returns
		var out []*proxmox.AgentNetworkIface
		for _, r := range raw {
			ifc := &proxmox.AgentNetworkIface{
				Name:            r["name"].(string),
				HardwareAddress: r["hardware-address"].(string),
			}
			for _, a := range r["ip-addresses"].([]map[string]interface{}) {
				ifc.IPAddresses = append(ifc.IPAddresses, &proxmox.AgentNetworkIPAddress{
					IPAddressType: a["ip-address-type"].(string),
					IPAddress:     a["ip-address"].(string),
				})
			}
			out = append(out, ifc)
		}
		return out
	}

	tests := []struct {
		name   string
		ifaces []map[string]interface{}
		want   string
	}{
		{
			name: "docker and cni bridges present — must pick the MAC-matched NIC",
			ifaces: []map[string]interface{}{
				iface("docker0", "02:42:00:00:00:01", addr("ipv4", "172.17.0.1")),
				iface("cni0", "02:42:00:00:00:02", addr("ipv4", "10.42.0.1")),
				iface("ens18", mac, addr("ipv4", "192.168.1.50")),
			},
			want: "192.168.1.50",
		},
		{
			name: "IPv6 listed before IPv4 on the right interface",
			ifaces: []map[string]interface{}{
				iface("ens18", mac, addr("ipv6", "fe80::1"), addr("ipv6", "2001:db8::5"), addr("ipv4", "192.168.1.51")),
			},
			want: "192.168.1.51",
		},
		{
			name: "agent up before DHCP: right NIC has no IPv4 yet",
			ifaces: []map[string]interface{}{
				iface("ens18", mac, addr("ipv6", "fe80::1")),
			},
			want: "",
		},
		{
			name: "link-local IPv4 must not count",
			ifaces: []map[string]interface{}{
				iface("ens18", mac, addr("ipv4", "169.254.10.10")),
			},
			want: "",
		},
		{
			name: "interface renamed (eth0 vs ens18) is irrelevant — match is by MAC",
			ifaces: []map[string]interface{}{
				iface("eth0", mac, addr("ipv4", "192.168.1.52")),
			},
			want: "192.168.1.52",
		},
		{
			name: "MAC case-insensitive",
			ifaces: []map[string]interface{}{
				iface("ens18", "aa:bb:cc:dd:ee:01", addr("ipv4", "192.168.1.53")),
			},
			want: "192.168.1.53",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, pickIPv4(toIfaces(tc.ifaces), mac))
		})
	}
}

func TestWaitForIPHappyAfterAgentDelay(t *testing.T) {
	s, c, vm := vmFixture(t) // net0 MAC AA:BB:CC:DD:EE:01 (Task 10 fixture)

	// A local listener plays the VM's sshd; the probe dialer is redirected
	// to it because the fixture's reported IP (192.0.2.10, TEST-NET) is not
	// actually routable in the test environment.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	restore := setProbeDialerForTest(func(network, address string, d time.Duration) (net.Conn, error) {
		assert.Equal(t, "192.0.2.10:22", address, "probe must target the reported IP and SSH port")
		return net.DialTimeout("tcp", ln.Addr().String(), d)
	})
	defer restore()

	// The three phases of a real DHCP boot: agent down → agent up but no
	// address yet → address present (alongside a docker0 decoy).
	var calls atomic.Int32
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/agent/network-get-interfaces", func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			pvetest.PVEError(w, 500, "QEMU guest agent is not running")
		case 2:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"result":[
				{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:01","ip-addresses":[]}
			]}}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"result":[
				{"name":"docker0","hardware-address":"02:42:00:00:00:01","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"172.17.0.1","prefix":16}]},
				{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:01","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"192.0.2.10","prefix":24}]}
			]}}`))
		}
	})

	ip, err := c.WaitForIP(context.Background(), vm, 22, 30*time.Second)
	require.NoError(t, err)
	assert.Equal(t, "192.0.2.10", ip)
	assert.GreaterOrEqual(t, calls.Load(), int32(3))
}

func TestWaitForIPTimeoutMessage(t *testing.T) {
	s, c, vm := vmFixture(t)
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/agent/network-get-interfaces", func(w http.ResponseWriter, r *http.Request) {
		pvetest.PVEError(w, 500, "QEMU guest agent is not running")
	})

	_, err := c.WaitForIP(context.Background(), vm, 22, 3*time.Second)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "qemu-guest-agent is installed and enabled")
}

func TestQueryAgentIP(t *testing.T) {
	s, c, vm := vmFixture(t)
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/agent/network-get-interfaces", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":[
			{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:01","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"192.168.1.60","prefix":24}]}
		]}}`))
	})

	ip, err := c.QueryAgentIP(context.Background(), vm)
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.60", ip)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/pve/ -run 'TestPickIPv4|TestWaitForIP|TestQueryAgentIP' -v`
Expected: FAIL (undefined symbols).

- [ ] **Step 3: Implement agentip.go**

`pkg/pve/agentip.go`:

```go
package pve

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/log"

	"github.com/14f3v/pve-rancher-node-driver/pkg/validate"
)

// probeDialer is a seam for tests; production uses net.DialTimeout.
var probeDialer = net.DialTimeout

func setProbeDialerForTest(d func(network, address string, timeout time.Duration) (net.Conn, error)) (restore func()) {
	old := probeDialer
	probeDialer = d
	return func() { probeDialer = old }
}

const (
	ipPollStart = 2 * time.Second
	ipPollMax   = 15 * time.Second
	probeWait   = 3 * time.Second
)

// WaitForIP waits until the guest agent reports an IPv4 address on the
// VM's net0 interface (matched by MAC — interface names are unreliable and
// reused templates report docker0/cni0/etc.), then verifies TCP
// reachability of the SSH port. "Agent responded" is NOT "has address":
// the agent often comes up before DHCP finishes.
func (c *Client) WaitForIP(ctx context.Context, vm *proxmox.VirtualMachine, sshPort int, timeout time.Duration) (string, error) {
	mac, err := validate.NICMAC(vm.VirtualMachineConfig, "net0")
	if err != nil {
		return "", err
	}

	deadline := time.Now().Add(timeout)
	interval := ipPollStart
	lastState := "waiting for qemu-guest-agent"
	for {
		if time.Now().After(deadline) {
			return "", fmt.Errorf(
				"timed out after %s waiting for VM %d's IP (last state: %s) — "+
					"check qemu-guest-agent is installed and enabled in the template, and that DHCP works on the VM's bridge",
				timeout, int(vm.VMID), lastState)
		}

		ifaces, err := vm.AgentGetNetworkIFaces(ctx)
		switch {
		case err == nil:
			if ip := pickIPv4(ifaces, mac); ip != "" {
				addr := net.JoinHostPort(ip, strconv.Itoa(sshPort))
				conn, perr := probeDialer("tcp", addr, probeWait)
				if perr == nil {
					_ = conn.Close()
					return ip, nil
				}
				lastState = fmt.Sprintf("got IP %s but %s not reachable yet", ip, addr)
			} else {
				lastState = fmt.Sprintf("agent up, no IPv4 on interface %s yet", mac)
			}
		case IsAgentNotRunning(err):
			lastState = "qemu-guest-agent not running yet"
		case proxmox.IsNotAuthorized(err):
			return "", fmt.Errorf(
				"token may not query the guest agent (needs VM.Monitor on PVE 8 / VM.GuestAgent.Audit on PVE 9): %w", err)
		default:
			lastState = fmt.Sprintf("agent query error: %v", err)
		}
		log.Debugf("pvenode: VM %d: %s", int(vm.VMID), lastState)

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
		if interval = interval * 3 / 2; interval > ipPollMax {
			interval = ipPollMax
		}
	}
}

// QueryAgentIP is the single-shot variant for GetIP calls.
func (c *Client) QueryAgentIP(ctx context.Context, vm *proxmox.VirtualMachine) (string, error) {
	mac, err := validate.NICMAC(vm.VirtualMachineConfig, "net0")
	if err != nil {
		return "", err
	}
	ifaces, err := vm.AgentGetNetworkIFaces(ctx)
	if err != nil {
		return "", err
	}
	ip := pickIPv4(ifaces, mac)
	if ip == "" {
		return "", fmt.Errorf("guest agent reports no usable IPv4 on interface %s", mac)
	}
	return ip, nil
}

// pickIPv4 returns the first global IPv4 on the interface whose MAC
// matches (case-insensitive). Loopback, link-local and unspecified
// addresses never count.
func pickIPv4(ifaces []*proxmox.AgentNetworkIface, mac string) string {
	for _, ifc := range ifaces {
		if !strings.EqualFold(ifc.HardwareAddress, mac) {
			continue
		}
		for _, a := range ifc.IPAddresses {
			if a.IPAddressType != "ipv4" {
				continue
			}
			ip := net.ParseIP(a.IPAddress)
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
				continue
			}
			return a.IPAddress
		}
	}
	return ""
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/pve/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/pve/agentip.go pkg/pve/agentip_test.go
git commit -m "feat: MAC-matched agent IP wait with SSH probe and adversarial fixtures"
```

---

### Task 12: Driver lifecycle — GetState, Start/Stop/Kill/Restart, GetURL, GetIP, GetSSHHostname, lookupVM

**Files:**
- Create: `pkg/driver/lifecycle.go`
- Modify: `pkg/driver/driver.go` (add `lookupVM`)
- Test: `pkg/driver/lifecycle_test.go`

**Interfaces:**
- Consumes: `pve.Client` methods from Tasks 6, 10, 11.
- Produces:
  - `(*Driver).lookupVM(ctx context.Context, c *pve.Client) (*proxmox.VirtualMachine, error)` — persisted VMID+node first, then cluster-wide by VMID, then name+tag fallback; `(nil, nil)` when gone. Used by lifecycle, Remove, Create-cleanup.
  - `GetState() (state.State, error)`, `Start()`, `Stop()` (graceful→force), `Kill()`, `Restart()`, `GetURL()`, `GetIP()`, `GetSSHHostname()`.

- [ ] **Step 1: Write the failing tests**

`pkg/driver/lifecycle_test.go`:

```go
package driver

import (
	"net/http"
	"testing"

	"github.com/rancher/machine/libmachine/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

// machineFixture: a created machine (VMID 10005 on pve1) with our tag.
func machineFixture(t *testing.T, status string) (*pvetest.Server, *Driver) {
	s := pvetest.New(t)
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/10005/status/current", 200, map[string]interface{}{
		"status": status, "vmid": 10005, "name": "c1-pool1-abcde", "tags": "rancher-pvenode",
	})
	s.Handle("GET", "/nodes/pve1/qemu/10005/config", 200, map[string]interface{}{
		"name": "c1-pool1-abcde", "tags": "rancher-pvenode",
		"net0": "virtio=AA:BB:CC:DD:EE:01,bridge=vmbr0",
	})
	d := testDriver(s)
	d.VMID = 10005
	d.PVENode = "pve1"
	return s, d
}

func TestGetStateRunning(t *testing.T) {
	_, d := machineFixture(t, "running")
	st, err := d.GetState()
	require.NoError(t, err)
	assert.Equal(t, state.Running, st)
}

func TestGetStateStopped(t *testing.T) {
	_, d := machineFixture(t, "stopped")
	st, err := d.GetState()
	require.NoError(t, err)
	assert.Equal(t, state.Stopped, st)
}

func TestGetStateGoneVM(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/status/current", func(w http.ResponseWriter, r *http.Request) {
		pvetest.PVEError(w, 500, "Configuration file 'nodes/pve1/qemu-server/10005.conf' does not exist")
	})
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{})
	d := testDriver(s)
	d.VMID = 10005
	d.PVENode = "pve1"

	st, err := d.GetState()
	require.NoError(t, err)
	assert.Equal(t, state.NotFound, st)
}

func TestLifecycleOperations(t *testing.T) {
	s, d := machineFixture(t, "running")
	upid := s.OKTask("pve1")
	for _, op := range []string{"start", "shutdown", "stop", "reboot"} {
		s.Handle("POST", "/nodes/pve1/qemu/10005/status/"+op, 200, upid)
	}

	assert.NoError(t, d.Start())
	assert.NoError(t, d.Stop())
	assert.NoError(t, d.Kill())
	assert.NoError(t, d.Restart())
}

func TestGetURLRequiresRunning(t *testing.T) {
	_, d := machineFixture(t, "stopped")
	_, err := d.GetURL()
	assert.Error(t, err)
}

func TestGetURLAndSSHHostname(t *testing.T) {
	s, d := machineFixture(t, "running")
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/agent/network-get-interfaces", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"result":[
			{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:01","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"192.168.1.70","prefix":24}]}
		]}}`))
	})

	url, err := d.GetURL()
	require.NoError(t, err)
	assert.Equal(t, "tcp://192.168.1.70:2376", url)

	host, err := d.GetSSHHostname()
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.70", host)
	assert.Equal(t, "192.168.1.70", d.IPAddress) // cache refreshed
}

func TestGetIPFallsBackToCache(t *testing.T) {
	s, d := machineFixture(t, "running")
	d.IPAddress = "192.168.1.99"
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/agent/network-get-interfaces", func(w http.ResponseWriter, r *http.Request) {
		pvetest.PVEError(w, 500, "QEMU guest agent is not running")
	})

	ip, err := d.GetIP()
	require.NoError(t, err)
	assert.Equal(t, "192.168.1.99", ip)
}

func TestLookupVMFallbackByNameAndTag(t *testing.T) {
	// Simulates the mid-create-crash orphan: persisted config has no VMID.
	s := pvetest.New(t)
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{
		{"vmid": 10007, "name": "c1-pool1-abcde", "node": "pve1", "type": "qemu", "template": 0, "status": "running"},
	})
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/10007/status/current", 200, map[string]interface{}{
		"status": "running", "vmid": 10007, "name": "c1-pool1-abcde", "tags": "rancher-pvenode",
	})
	s.Handle("GET", "/nodes/pve1/qemu/10007/config", 200, map[string]interface{}{
		"name": "c1-pool1-abcde", "tags": "rancher-pvenode",
	})
	d := testDriver(s) // MachineName c1-pool1-abcde, VMID 0

	st, err := d.GetState()
	require.NoError(t, err)
	assert.Equal(t, state.Running, st)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/driver/ -run 'TestGetState|TestLifecycle|TestGetURL|TestGetIP|TestLookup' -v`
Expected: FAIL (the Task 1 stubs return "not implemented yet").

- [ ] **Step 3: Implement lookupVM (append to driver.go) and lifecycle.go — first DELETE the `Start/Stop/Kill/Restart/GetState/GetURL/GetSSHHostname` stubs from driver.go**

Append to `pkg/driver/driver.go`:

```go
// lookupVM finds this machine's VM: persisted node+VMID first, then a
// cluster-wide VMID search (VM may have been migrated), then — for the
// mid-create-crash case where the VMID never got persisted — by machine
// name guarded by the ownership tag. Returns (nil, nil) when the VM is
// gone: callers treat that as "already removed".
func (d *Driver) lookupVM(ctx context.Context, c *pve.Client) (*proxmox.VirtualMachine, error) {
	if d.VMID != 0 {
		if d.PVENode != "" {
			vm, err := c.GetVM(ctx, d.PVENode, d.VMID)
			if err == nil {
				return vm, nil
			}
			if !pve.IsNotFoundErr(err) {
				return nil, err
			}
		}
		rows, err := c.ListVMs(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if int(row.VMID) == d.VMID && row.Type == "qemu" {
				return c.GetVM(ctx, row.Node, d.VMID)
			}
		}
		return nil, nil
	}
	return c.FindVMByNameAndTag(ctx, d.MachineName, MachineTag)
}
```

Add the imports `context` and `proxmox "github.com/luthermonson/go-proxmox"` to driver.go.

`pkg/driver/lifecycle.go`:

```go
package driver

import (
	"context"
	"fmt"
	"net"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/drivers"
	"github.com/rancher/machine/libmachine/log"
	"github.com/rancher/machine/libmachine/state"

	"github.com/14f3v/pve-rancher-node-driver/pkg/pve"
)

const gracefulShutdownTimeout = 90 * time.Second

// withVM runs fn against this machine's VM; error if the VM is gone.
func (d *Driver) withVM(fn func(ctx context.Context, c *pve.Client, vm *proxmox.VirtualMachine) error) error {
	ctx := context.Background()
	c, err := d.client()
	if err != nil {
		return err
	}
	vm, err := d.lookupVM(ctx, c)
	if err != nil {
		return err
	}
	if vm == nil {
		return fmt.Errorf("machine %q (VMID %d) no longer exists on PVE", d.MachineName, d.VMID)
	}
	return fn(ctx, c, vm)
}

func (d *Driver) GetState() (state.State, error) {
	ctx := context.Background()
	c, err := d.client()
	if err != nil {
		return state.None, err
	}
	vm, err := d.lookupVM(ctx, c)
	if err != nil {
		return state.Error, err
	}
	if vm == nil {
		return state.NotFound, nil
	}
	switch vm.Status {
	case proxmox.StatusVirtualMachineRunning:
		if vm.Lock == "clone" {
			return state.Starting, nil
		}
		return state.Running, nil
	case proxmox.StatusVirtualMachineStopped:
		return state.Stopped, nil
	case proxmox.StatusVirtualMachinePaused:
		return state.Paused, nil
	default:
		return state.Error, fmt.Errorf("unknown PVE VM status %q", vm.Status)
	}
}

func (d *Driver) Start() error {
	return d.withVM(func(ctx context.Context, c *pve.Client, vm *proxmox.VirtualMachine) error {
		return c.StartVM(ctx, vm)
	})
}

// Stop shuts the guest down cleanly, falling back to a hard stop.
func (d *Driver) Stop() error {
	return d.withVM(func(ctx context.Context, c *pve.Client, vm *proxmox.VirtualMachine) error {
		if err := c.ShutdownVM(ctx, vm, gracefulShutdownTimeout); err != nil {
			log.Warnf("pvenode: graceful shutdown of VM %d failed (%v), forcing stop", d.VMID, err)
			return c.StopVM(ctx, vm)
		}
		return nil
	})
}

func (d *Driver) Kill() error {
	return d.withVM(func(ctx context.Context, c *pve.Client, vm *proxmox.VirtualMachine) error {
		return c.StopVM(ctx, vm)
	})
}

func (d *Driver) Restart() error {
	return d.withVM(func(ctx context.Context, c *pve.Client, vm *proxmox.VirtualMachine) error {
		task, err := vm.Reboot(ctx)
		if err != nil {
			return fmt.Errorf("rebooting VM %d: %w", d.VMID, err)
		}
		return c.WaitTask(ctx, task, gracefulShutdownTimeout)
	})
}

// GetIP re-queries the guest agent (DHCP leases change across reboots),
// falling back to the last known address if the agent is unreachable.
func (d *Driver) GetIP() (string, error) {
	ctx := context.Background()
	c, err := d.client()
	if err != nil {
		return "", err
	}
	vm, err := d.lookupVM(ctx, c)
	if err == nil && vm != nil {
		if ip, qerr := c.QueryAgentIP(ctx, vm); qerr == nil {
			d.IPAddress = ip
			return ip, nil
		}
	}
	if d.IPAddress != "" {
		return d.IPAddress, nil
	}
	return "", fmt.Errorf("no IP address known for machine %q yet", d.MachineName)
}

func (d *Driver) GetSSHHostname() (string, error) {
	return d.GetIP()
}

func (d *Driver) GetURL() (string, error) {
	if err := drivers.MustBeRunning(d); err != nil {
		return "", err
	}
	ip, err := d.GetIP()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("tcp://%s", net.JoinHostPort(ip, "2376")), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/driver/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/driver/driver.go pkg/driver/lifecycle.go pkg/driver/lifecycle_test.go
git commit -m "feat: driver lifecycle with agent-fresh GetIP and resilient VM lookup"
```

---

### Task 13: Remove — idempotent, tag-guarded, disk-purging

**Files:**
- Create: `pkg/driver/remove.go`
- Test: `pkg/driver/remove_test.go`

**Interfaces:**
- Consumes: `lookupVM`, `pve.ShutdownVM/StopVM/DeleteVM`.
- Produces: `(*Driver).Remove() error`.

- [ ] **Step 1: Write the failing tests**

`pkg/driver/remove_test.go`:

```go
package driver

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
)

func TestRemoveIdempotentWhenGone(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.HandleFunc("GET", "/nodes/pve1/qemu/10005/status/current", func(w http.ResponseWriter, r *http.Request) {
		pvetest.PVEError(w, 500, "Configuration file 'nodes/pve1/qemu-server/10005.conf' does not exist")
	})
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{})
	d := testDriver(s)
	d.VMID = 10005
	d.PVENode = "pve1"

	assert.NoError(t, d.Remove(), "removing an already-gone VM must succeed")
}

func TestRemoveRefusesUntaggedVM(t *testing.T) {
	s := pvetest.New(t)
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/10005/status/current", 200, map[string]interface{}{
		"status": "running", "vmid": 10005, "name": "c1-pool1-abcde", "tags": "somebody-elses-vm",
	})
	s.Handle("GET", "/nodes/pve1/qemu/10005/config", 200, map[string]interface{}{
		"name": "c1-pool1-abcde", "tags": "somebody-elses-vm",
	})
	d := testDriver(s)
	d.VMID = 10005
	d.PVENode = "pve1"

	err := d.Remove()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rancher-pvenode")
}

func TestRemoveStopsThenDeletes(t *testing.T) {
	s, d := machineFixture(t, "running")
	upid := s.OKTask("pve1")
	s.Handle("POST", "/nodes/pve1/qemu/10005/status/shutdown", 200, upid)
	var deleted atomic.Bool
	s.HandleFunc("DELETE", "/nodes/pve1/qemu/10005", func(w http.ResponseWriter, r *http.Request) {
		deleted.Store(true)
		assert.Equal(t, "1", r.URL.Query().Get("purge"))
		assert.Equal(t, "1", r.URL.Query().Get("destroy-unreferenced-disks"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"` + upid + `"}`))
	})

	require.NoError(t, d.Remove())
	assert.True(t, deleted.Load())
}

func TestRemoveFallbackByNameAndTag(t *testing.T) {
	// Mid-create crash: no VMID persisted, but the tagged VM exists.
	s := pvetest.New(t)
	s.Handle("GET", "/cluster/resources", 200, []map[string]interface{}{
		{"vmid": 10007, "name": "c1-pool1-abcde", "node": "pve1", "type": "qemu", "template": 0, "status": "stopped"},
	})
	s.Handle("GET", "/nodes/pve1/status", 200, map[string]interface{}{})
	s.Handle("GET", "/nodes/pve1/qemu/10007/status/current", 200, map[string]interface{}{
		"status": "stopped", "vmid": 10007, "name": "c1-pool1-abcde", "tags": "rancher-pvenode",
	})
	s.Handle("GET", "/nodes/pve1/qemu/10007/config", 200, map[string]interface{}{
		"name": "c1-pool1-abcde", "tags": "rancher-pvenode",
	})
	upid := s.OKTask("pve1")
	var deleted atomic.Bool
	s.HandleFunc("DELETE", "/nodes/pve1/qemu/10007", func(w http.ResponseWriter, r *http.Request) {
		deleted.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"` + upid + `"}`))
	})
	d := testDriver(s) // VMID == 0

	require.NoError(t, d.Remove())
	assert.True(t, deleted.Load())
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/driver/ -run TestRemove -v`
Expected: FAIL (the Task 1 `Remove` stub returns "not implemented yet").

- [ ] **Step 3: Implement remove.go — first DELETE the `Remove` stub from driver.go**

`pkg/driver/remove.go`:

```go
package driver

import (
	"context"
	"fmt"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/log"
)

// Remove deletes the machine's VM with disk purge. Idempotent: an
// already-gone VM is success (Rancher's rm job may run after a failed
// create already cleaned up). The ownership-tag guard makes the
// name-fallback lookup safe against VMID/name reuse.
func (d *Driver) Remove() error {
	ctx := context.Background()
	c, err := d.client()
	if err != nil {
		return err
	}
	vm, err := d.lookupVM(ctx, c)
	if err != nil {
		return err
	}
	if vm == nil {
		log.Infof("pvenode: machine %q (VMID %d) is already gone, nothing to remove", d.MachineName, d.VMID)
		return nil
	}
	if !vm.HasTag(MachineTag) {
		return fmt.Errorf(
			"refusing to delete VM %d (%s): it does not carry the %q ownership tag — "+
				"it may have been replaced or modified outside the driver",
			int(vm.VMID), vm.Name, MachineTag)
	}

	if vm.Status == proxmox.StatusVirtualMachineRunning {
		if err := c.ShutdownVM(ctx, vm, gracefulShutdownTimeout); err != nil {
			log.Warnf("pvenode: graceful shutdown of VM %d failed (%v), forcing stop", int(vm.VMID), err)
			if err := c.StopVM(ctx, vm); err != nil {
				return fmt.Errorf("could not stop VM %d before removal: %w", int(vm.VMID), err)
			}
		}
	}

	log.Infof("pvenode: deleting VM %d (%s) with disk purge", int(vm.VMID), vm.Name)
	return c.DeleteVM(ctx, vm)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./pkg/driver/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/driver/remove.go pkg/driver/remove_test.go
git commit -m "feat: idempotent tag-guarded Remove with disk purge"
```

---

### Task 14: Create — assembly with cleanup-on-failure

**Files:**
- Create: `pkg/driver/create.go`
- Test: `pkg/driver/create_test.go`

**Interfaces:**
- Consumes: everything prior — `resolvePlacement`, `CloneFromTemplate`, `ApplyConfig`, `ResizeVMDisk`, `StartVM`, `WaitForIP`, `Remove`, `ssh.GenerateSSHKey`, `proxmox.EncodeSSHKeys`.
- Produces: `(*Driver).Create() error`. After success: `d.VMID`, `d.PVENode`, `d.IPAddress` set; SSH keypair at `d.GetSSHKeyPath()`(+`.pub`).

- [ ] **Step 1: Write the failing tests**

`pkg/driver/create_test.go`:

First, a small seam file so driver tests can bypass the real TCP probe:

`pkg/pve/probe_seam.go`:

```go
package pve

import (
	"net"
	"time"
)

// SetProbeDialer replaces the SSH-probe dialer and returns a restore
// function. ONLY for tests of dependent packages (pkg/driver); production
// code must never call it.
func SetProbeDialer(d func(network, address string, timeout time.Duration) (net.Conn, error)) (restore func()) {
	return setProbeDialerForTest(d)
}
```

`pkg/driver/create_test.go`:

```go
package driver

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/14f3v/pve-rancher-node-driver/internal/pvetest"
	"github.com/14f3v/pve-rancher-node-driver/pkg/pve"
)

// createFixture wires the full Create flow on top of happyServer (from
// precreate_test.go). The clone handler registers all routes for the VM it
// creates, including its DELETE route: when deleted is non-nil it records
// deletions there; when nil, deletion still succeeds silently.
func createFixture(t *testing.T, storeDir string, deleted *atomic.Bool) (*pvetest.Server, *Driver, *atomic.Int32) {
	s := happyServer(t)
	upid := s.OKTask("pve1")

	var cloneCalls atomic.Int32
	s.HandleFunc("POST", "/nodes/pve1/qemu/9000/clone", func(w http.ResponseWriter, r *http.Request) {
		cloneCalls.Add(1)
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		id := int(body["newid"].(float64))
		path := "/nodes/pve1/qemu/" + strconv.Itoa(id)

		s.Handle("GET", path+"/status/current", 200, map[string]interface{}{
			"status": "stopped", "vmid": id, "name": "c1-pool1-abcde", "tags": "rancher-pvenode",
		})
		s.Handle("GET", path+"/config", 200, map[string]interface{}{
			"name": "c1-pool1-abcde", "tags": "rancher-pvenode",
			"net0":  "virtio=AA:BB:CC:DD:EE:99,bridge=vmbr0",
			"scsi0": "local-lvm:vm-disk-0,size=20G",
			"ide2":  "local-lvm:vm-cloudinit,media=cdrom",
		})
		s.Handle("POST", path+"/config", 200, upid)
		s.Handle("PUT", path+"/resize", 200, upid)
		s.Handle("POST", path+"/status/start", 200, upid)
		s.Handle("POST", path+"/status/shutdown", 200, upid)
		s.Handle("POST", path+"/status/stop", 200, upid)
		s.HandleFunc("DELETE", path, func(w http.ResponseWriter, r *http.Request) {
			if deleted != nil {
				deleted.Store(true)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":"` + upid + `"}`))
		})
		s.HandleFunc("GET", path+"/agent/network-get-interfaces", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"result":[
				{"name":"ens18","hardware-address":"aa:bb:cc:dd:ee:99","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"192.0.2.10","prefix":24}]}
			]}}`))
		})

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": upid})
	})

	d := testDriver(s)
	d.StorePath = storeDir
	require.NoError(t, os.MkdirAll(filepath.Join(storeDir, "machines", d.MachineName), 0o755))
	return s, d, &cloneCalls
}

func TestCreateHappyPath(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	restore := pve.SetProbeDialer(func(network, address string, timeout time.Duration) (net.Conn, error) {
		return net.DialTimeout("tcp", ln.Addr().String(), timeout)
	})
	defer restore()

	_, d, cloneCalls := createFixture(t, t.TempDir(), nil)
	require.NoError(t, d.Create())

	assert.NotZero(t, d.VMID)
	assert.Equal(t, "pve1", d.PVENode)
	assert.Equal(t, "192.0.2.10", d.IPAddress)
	assert.Equal(t, int32(1), cloneCalls.Load())
	_, err = os.Stat(d.GetSSHKeyPath())
	assert.NoError(t, err, "private key must exist at GetSSHKeyPath")
	_, err = os.Stat(d.GetSSHKeyPath() + ".pub")
	assert.NoError(t, err)
}

func TestCreateCleansUpOnProvisionFailure(t *testing.T) {
	restore := pve.SetProbeDialer(func(network, address string, timeout time.Duration) (net.Conn, error) {
		return nil, errors.New("never reachable")
	})
	defer restore()

	var deleted atomic.Bool
	_, d, _ := createFixture(t, t.TempDir(), &deleted)
	d.AgentTimeout = 2 // fail the IP wait fast

	err := d.Create()
	require.Error(t, err)
	assert.True(t, deleted.Load(), "failed create must delete the half-created VM")
}

func TestCreateKeepOnFailureSkipsCleanup(t *testing.T) {
	restore := pve.SetProbeDialer(func(network, address string, timeout time.Duration) (net.Conn, error) {
		return nil, errors.New("never reachable")
	})
	defer restore()

	var deleted atomic.Bool
	_, d, _ := createFixture(t, t.TempDir(), &deleted)
	d.AgentTimeout = 2
	d.KeepOnFailure = true

	err := d.Create()
	require.Error(t, err)
	assert.False(t, deleted.Load(), "keep-on-failure must not delete the VM")
	assert.NotZero(t, d.VMID, "VMID must remain persisted for debugging")
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./pkg/driver/ -run TestCreate -v`
Expected: FAIL (the Task 1 `Create` stub returns "not implemented yet").

- [ ] **Step 3: Implement create.go — first DELETE the `Create` stub and the now-unused `errNotImplemented` from driver.go**

`pkg/driver/create.go`:

```go
package driver

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	proxmox "github.com/luthermonson/go-proxmox"
	"github.com/rancher/machine/libmachine/log"
	"github.com/rancher/machine/libmachine/ssh"

	"github.com/14f3v/pve-rancher-node-driver/pkg/pve"
)

// Create clones the template, configures it via PVE's built-in cloud-init
// fields, starts it, and waits for the MAC-matched agent IP. On failure it
// removes the half-created VM (unless --pvenode-keep-on-failure).
func (d *Driver) Create() error {
	ctx := context.Background()
	c, err := d.client()
	if err != nil {
		return err
	}

	if err := ssh.GenerateSSHKey(d.GetSSHKeyPath()); err != nil {
		return fmt.Errorf("generating SSH key: %w", err)
	}
	pub, err := os.ReadFile(d.GetSSHKeyPath() + ".pub")
	if err != nil {
		return fmt.Errorf("reading generated SSH public key: %w", err)
	}

	place, err := d.resolvePlacement(ctx, c)
	if err != nil {
		return err
	}

	lo, hi, err := parseVMIDRange(d.VMIDRange)
	if err != nil {
		return err
	}
	tags := []string{MachineTag}
	if d.ExtraTags != "" {
		for _, tag := range strings.Split(d.ExtraTags, ",") {
			if tag = strings.TrimSpace(tag); tag != "" {
				tags = append(tags, tag)
			}
		}
	}

	vm, err := c.CloneFromTemplate(ctx, place.Template, pve.CloneSpec{
		Name:       d.MachineName,
		TargetNode: d.NodeName,
		Storage:    d.Storage,
		Linked:     d.LinkedClone,
		Pool:       d.ResourcePool,
		VMIDLo:     lo,
		VMIDHi:     hi,
		Tags:       tags,
	})
	if err != nil {
		return err
	}
	d.VMID = int(vm.VMID)
	d.PVENode = place.TargetNode
	log.Infof("pvenode: created VM %d for machine %s", d.VMID, d.MachineName)

	if err := d.provision(ctx, c, vm, place, strings.TrimSpace(string(pub))); err != nil {
		if d.KeepOnFailure {
			log.Warnf("pvenode: create failed but VM %d is kept (--pvenode-keep-on-failure): %v", d.VMID, err)
			return err
		}
		log.Warnf("pvenode: create failed, removing half-created VM %d: %v", d.VMID, err)
		if cerr := d.Remove(); cerr != nil {
			return fmt.Errorf("%w (cleanup also failed: %v — delete VM %d manually in PVE)", err, cerr, d.VMID)
		}
		return err
	}
	return nil
}

func (d *Driver) provision(ctx context.Context, c *pve.Client, vm *proxmox.VirtualMachine, place *placement, pubKey string) error {
	opts := []proxmox.VirtualMachineOption{
		{Name: "cores", Value: d.Cores},
		{Name: "memory", Value: d.MemoryMB},
		{Name: "agent", Value: "1"},
		{Name: "onboot", Value: boolToInt(d.OnBoot)},
		{Name: "ciuser", Value: d.GetSSHUsername()},
		{Name: "sshkeys", Value: proxmox.EncodeSSHKeys(pubKey)},
		{Name: "ipconfig0", Value: "ip=dhcp"},
	}
	if d.CPUType != "" {
		opts = append(opts, proxmox.VirtualMachineOption{Name: "cpu", Value: d.CPUType})
	}
	if d.CIPassword != "" {
		opts = append(opts, proxmox.VirtualMachineOption{Name: "cipassword", Value: d.CIPassword})
	}
	if d.Nameserver != "" {
		opts = append(opts, proxmox.VirtualMachineOption{Name: "nameserver", Value: d.Nameserver})
	}
	if d.Searchdomain != "" {
		opts = append(opts, proxmox.VirtualMachineOption{Name: "searchdomain", Value: d.Searchdomain})
	}
	if d.Bridge != "" {
		net0 := "virtio,bridge=" + d.Bridge
		if d.VLANTag > 0 {
			net0 += ",tag=" + strconv.Itoa(d.VLANTag)
		}
		opts = append(opts, proxmox.VirtualMachineOption{Name: "net0", Value: net0})
	}

	log.Infof("pvenode: configuring VM %d (cores=%d memory=%dMB)", d.VMID, d.Cores, d.MemoryMB)
	if err := c.ApplyConfig(ctx, vm, opts); err != nil {
		return err
	}

	if d.DiskSizeGB > place.TemplateGB {
		log.Infof("pvenode: growing disk %s to %dG", place.BootDiskKey, d.DiskSizeGB)
		if err := c.ResizeVMDisk(ctx, vm, place.BootDiskKey, d.DiskSizeGB); err != nil {
			return err
		}
	}

	log.Infof("pvenode: starting VM %d", d.VMID)
	if err := c.StartVM(ctx, vm); err != nil {
		return err
	}

	// Refresh the config: net0 (and its MAC) may have been rewritten above.
	if err := vm.Ping(ctx); err != nil {
		return fmt.Errorf("refreshing VM %d config: %w", d.VMID, err)
	}

	timeout := time.Duration(d.AgentTimeout) * time.Second
	log.Infof("pvenode: waiting up to %s for the guest agent to report an IP", timeout)
	ip, err := c.WaitForIP(ctx, vm, d.SSHPort, timeout)
	if err != nil {
		return err
	}
	d.IPAddress = ip
	log.Infof("pvenode: VM %d is reachable at %s — handing off to SSH provisioning", d.VMID, ip)
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./... -v`
Expected: PASS (whole repo).

- [ ] **Step 5: Commit**

```bash
git add pkg/driver/driver.go pkg/driver/create.go pkg/driver/create_test.go pkg/pve/probe_seam.go
git commit -m "feat: Create with cloud-init provisioning and cleanup-on-failure"
```

---

### Task 15: Lab integration harness (rancher-machine CLI)

**Files:**
- Create: `scripts/integration-test.sh`, `scripts/integration-concurrent.sh`

**Interfaces:**
- Consumes: the built driver binary; a real PVE lab (env-gated).
- Produces: repeatable create→verify→remove cycles without Rancher. Not run in CI.

No TDD cycle — these are operational scripts; the "test" is running them against the lab.

- [ ] **Step 1: Write scripts/integration-test.sh**

```bash
#!/usr/bin/env bash
# Lab integration test: drives the built driver through the rancher-machine
# CLI against a real PVE host — no Rancher needed. Seconds to iterate.
#
# Requires: Linux amd64 (rancher-machine release binary), or set
# RANCHER_MACHINE_BIN to a locally built rancher-machine.
#
# Note: after the driver reports the IP, rancher-machine's own provisioning
# runs over SSH (it will attempt a Docker engine install on the VM — this
# needs internet access from the node network and is itself a useful test
# of the SSH/sudo/curl template contract).
set -euo pipefail

: "${PVE_URL:?set PVE_URL, e.g. https://pve.example.com:8006}"
: "${PVE_TOKEN_ID:?set PVE_TOKEN_ID, e.g. rancher@pve!machine}"
: "${PVE_TOKEN_SECRET:?set PVE_TOKEN_SECRET}"
: "${PVE_TEMPLATE:?set PVE_TEMPLATE (template name or VMID)}"
PVE_STORAGE="${PVE_STORAGE:-}"
PVE_BRIDGE="${PVE_BRIDGE:-}"
PVE_INSECURE_TLS="${PVE_INSECURE_TLS:-true}"

RM_VERSION=v0.15.0-rancher145
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$ROOT/.integration"
STORE="$WORK/store"
mkdir -p "$WORK/bin" "$STORE"

echo "==> building driver"
CGO_ENABLED=0 go build -o "$WORK/bin/docker-machine-driver-pvenode" "$ROOT/cmd/docker-machine-driver-pvenode"

if [[ -n "${RANCHER_MACHINE_BIN:-}" ]]; then
  RM="$RANCHER_MACHINE_BIN"
else
  RM="$WORK/bin/rancher-machine"
  if [[ ! -x "$RM" ]]; then
    echo "==> downloading rancher-machine $RM_VERSION (linux-amd64)"
    curl -fsSL "https://github.com/rancher/machine/releases/download/${RM_VERSION}/rancher-machine-amd64.tar.gz" \
      | tar -xz -C "$WORK/bin"
  fi
fi
export PATH="$WORK/bin:$PATH"

NAME="${1:-pvenode-it-$RANDOM}"
args=(
  --driver pvenode
  --pvenode-url "$PVE_URL"
  --pvenode-token-id "$PVE_TOKEN_ID"
  --pvenode-token-secret "$PVE_TOKEN_SECRET"
  --pvenode-template "$PVE_TEMPLATE"
)
[[ "$PVE_INSECURE_TLS" == "true" ]] && args+=(--pvenode-insecure-tls)
[[ -n "$PVE_STORAGE" ]] && args+=(--pvenode-storage "$PVE_STORAGE")
[[ -n "$PVE_BRIDGE" ]] && args+=(--pvenode-bridge "$PVE_BRIDGE")

cleanup() {
  echo "==> cleanup: removing $NAME"
  "$RM" -s "$STORE" rm -y "$NAME" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "==> creating machine $NAME"
"$RM" -s "$STORE" create "${args[@]}" "$NAME"

echo "==> verifying"
"$RM" -s "$STORE" ls
IP="$("$RM" -s "$STORE" ip "$NAME")"
echo "    machine IP: $IP"
"$RM" -s "$STORE" ssh "$NAME" -- sudo true                       # passwordless-sudo contract
"$RM" -s "$STORE" ssh "$NAME" -- 'command -v curl >/dev/null'    # curl contract

echo "==> removing machine"
"$RM" -s "$STORE" rm -y "$NAME"
trap - EXIT
echo "PASS"
```

- [ ] **Step 2: Write scripts/integration-concurrent.sh**

```bash
#!/usr/bin/env bash
# Exercises the VMID race and the template clone lock: three concurrent
# creates from separate rancher-machine processes (exactly what Rancher
# does when a pool scales 1 -> 3), then removes them all.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/integration-test.sh"

pids=()
names=()
for i in 1 2 3; do
  name="pvenode-race-$i-$RANDOM"
  names+=("$name")
  "$SCRIPT" "$name" &
  pids+=($!)
done

fail=0
for pid in "${pids[@]}"; do
  wait "$pid" || fail=1
done

if [[ "$fail" == "1" ]]; then
  echo "FAIL: at least one concurrent create failed"
  exit 1
fi
echo "PASS: 3 concurrent create/remove cycles succeeded (${names[*]})"
```

- [ ] **Step 3: Make executable and sanity-check**

Run: `chmod +x scripts/*.sh && bash -n scripts/integration-test.sh && bash -n scripts/integration-concurrent.sh`
Expected: no output (syntax OK).

- [ ] **Step 4: Run against the lab (manual gate — requires PVE access)**

Run:
```bash
export PVE_URL=https://<your-pve>:8006 PVE_TOKEN_ID='rancher@pve!machine' \
       PVE_TOKEN_SECRET=<secret> PVE_TEMPLATE=ubuntu-2404-tmpl
./scripts/integration-test.sh
```
Expected: `PASS`. If no lab is reachable from the dev machine, defer this step to the E2E phase and note it in the commit message.

- [ ] **Step 5: Commit**

```bash
git add scripts/
git commit -m "feat: lab integration harness via rancher-machine CLI"
```

---

### Task 16: CI, lint, and release pipeline

**Files:**
- Create: `.github/workflows/ci.yml`, `.github/workflows/release.yml`
- Create: `.goreleaser.yaml`, `.golangci.yml`

**Interfaces:**
- Consumes: the full module.
- Produces: release assets named `docker-machine-driver-pvenode-linux-amd64` (+`-linux-arm64`, `-darwin-arm64`) as **raw binaries** plus `checksums.txt` — the names `deploy/nodedriver.yaml` and the README reference.

- [ ] **Step 1: Write .goreleaser.yaml**

```yaml
version: 2
project_name: docker-machine-driver-pvenode
builds:
  - main: ./cmd/docker-machine-driver-pvenode
    binary: docker-machine-driver-pvenode
    env:
      - CGO_ENABLED=0
    goos:
      - linux
      - darwin
    goarch:
      - amd64
      - arm64
    ignore:
      - goos: darwin
        goarch: amd64
archives:
  # Raw binaries, not tarballs: Rancher's NodeDriver URL must point at a
  # directly executable file.
  - formats: [binary]
    name_template: "{{ .Binary }}-{{ .Os }}-{{ .Arch }}"
checksum:
  name_template: checksums.txt
changelog:
  use: github
```

- [ ] **Step 2: Write .golangci.yml**

```yaml
version: "2"
linters:
  default: standard
```

- [ ] **Step 3: Write .github/workflows/ci.yml**

```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: go vet ./...
      - run: go test ./...
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: golangci/golangci-lint-action@v8
        with:
          version: latest
```

- [ ] **Step 4: Write .github/workflows/release.yml**

```yaml
name: release
on:
  push:
    tags: ["v*"]
permissions:
  contents: write
jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: goreleaser/goreleaser-action@v6
        with:
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

- [ ] **Step 5: Validate locally**

Run: `go vet ./... && go test ./...`, then `goreleaser check` if goreleaser is installed locally (`brew install goreleaser` — optional; CI is the authority).
Expected: vet/tests pass; `goreleaser check` reports the config is valid.

- [ ] **Step 6: Commit**

```bash
git add .github/ .goreleaser.yaml .golangci.yml
git commit -m "ci: lint + test workflow and goreleaser release pipeline"
```

---

### Task 17: README, NodeDriver registration manifest, E2E checklist

**Files:**
- Create: `deploy/nodedriver.yaml`, `README.md`, `docs/e2e-checklist.md`

**Interfaces:**
- Consumes: flag names (Task 2), release asset names (Task 16), privilege sets (Task 5).
- Produces: everything an operator needs to register and use the driver.

- [ ] **Step 1: Write deploy/nodedriver.yaml**

```yaml
# Register the pvenode driver in Rancher:
#   kubectl apply -f deploy/nodedriver.yaml   (against the Rancher local cluster)
# or paste the url/checksum into Cluster Management > Drivers > Node Drivers > Add.
#
# Per release: replace <VERSION> with the tag (e.g. v0.1.0) and <SHA256>
# with the binary's line from that release's checksums.txt. NEVER reuse a
# URL for changed binaries — Rancher only re-downloads when url/checksum change.
apiVersion: management.cattle.io/v3
kind: NodeDriver
metadata:
  name: pvenode
  annotations:
    # Field names = flag minus the pvenode- prefix, camelCased.
    publicCredentialFields: "url,tokenId"
    privateCredentialFields: "tokenSecret"
    optionalCredentialFields: "insecureTls,caCert"
    passwordFields: "tokenSecret"
spec:
  displayName: "Proxmox VE (pvenode)"
  description: "Provision RKE2/K3s nodes as Proxmox VE virtual machines"
  active: true
  addCloudCredential: true
  builtin: false
  url: "https://github.com/14f3v/pve-rancher-node-driver/releases/download/<VERSION>/docker-machine-driver-pvenode-linux-amd64"
  checksum: "<SHA256>"
  uiUrl: ""
  externalId: ""
  whitelistDomains:
    - github.com
    - objects.githubusercontent.com
    - release-assets.githubusercontent.com
```

- [ ] **Step 2: Write README.md**

Sections and their load-bearing content (write full prose around these; every command block below goes in verbatim):

1. **What/why** — native Rancher node driver for Proxmox VE; RKE2/K3s machine pools; DHCP-reliable (MAC-matched guest-agent IP detection); PVE 8.x + 9.x; zero-orphan lifecycle. Link the design doc.
2. **Compatibility** — Rancher ≥ v2.11; PVE 8.x and 9.x (9.x primary — 8.x security EOL 2026-08-31).
3. **PVE API token setup** — explain privsep: a token does NOT inherit the user's permissions; grant the role to the token itself (or use `--privsep 0`). Two role definitions:

```bash
# PVE 9.x
pveum role add RancherPVENode -privs "VM.Clone,VM.Allocate,VM.Audit,VM.PowerMgmt,VM.Config.Disk,VM.Config.CPU,VM.Config.Memory,VM.Config.Network,VM.Config.Cloudinit,VM.Config.Options,VM.GuestAgent.Audit,Datastore.AllocateSpace,Datastore.Audit,SDN.Use,Pool.Allocate"

# PVE 8.x (VM.Monitor instead of VM.GuestAgent.Audit)
pveum role add RancherPVENode -privs "VM.Clone,VM.Allocate,VM.Audit,VM.PowerMgmt,VM.Config.Disk,VM.Config.CPU,VM.Config.Memory,VM.Config.Network,VM.Config.Cloudinit,VM.Config.Options,VM.Monitor,Datastore.AllocateSpace,Datastore.Audit,SDN.Use,Pool.Allocate"

# user + token + ACLs (BOTH the user and the token need the ACL)
pveum user add rancher@pve
pveum user token add rancher@pve machine
pveum acl modify / -user rancher@pve -role RancherPVENode
pveum acl modify / -token 'rancher@pve!machine' -role RancherPVENode
```

4. **Template preparation** (Ubuntu 24.04 example) — the three hard requirements called out loudly: (a) qemu-guest-agent **baked into the image** (the driver cannot learn the DHCP IP without it), (b) a cloud-init drive, (c) the cloud-init user gets passwordless sudo + curl + bash (Ubuntu cloud images do by default; Rancher's system-agent install fails without them AFTER the driver reports success):

```bash
# on the PVE host
wget https://cloud-images.ubuntu.com/releases/noble/release/ubuntu-24.04-server-cloudimg-amd64.img
apt install -y libguestfs-tools
virt-customize -a ubuntu-24.04-server-cloudimg-amd64.img --install qemu-guest-agent

qm create 9000 --name ubuntu-2404-tmpl --memory 2048 --cores 2 \
  --net0 virtio,bridge=vmbr0 --scsihw virtio-scsi-single \
  --agent 1 --serial0 socket --vga serial0
qm importdisk 9000 ubuntu-24.04-server-cloudimg-amd64.img local-lvm
qm set 9000 --scsi0 local-lvm:vm-9000-disk-0 --boot order=scsi0 \
  --ide2 local-lvm:cloudinit
qm template 9000
```

5. **Registering the driver in Rancher** — apply `deploy/nodedriver.yaml` or the UI flow; the whitelist-domains list and WHY all three domains are needed (GitHub release assets redirect to `release-assets.githubusercontent.com` since 2025 — missing it leaves the driver stuck "downloading").
6. **Creating a cluster** — cloud credential (url/tokenId/tokenSecret), machine pool form fields = flag table (name, default, description for every flag from Task 2), scaling via the +/- counter.
7. **Troubleshooting** — table mapping symptoms to causes: "stuck waiting for IP" → agent not in image / agent disabled / DHCP broken; "silently empty template list" → privsep token without ACLs; "driver stuck downloading" → whitelist domains; "nodes provision but never Ready" → sudo/curl contract; "refusing to delete VM" → ownership-tag guard explanation.
8. **Development** — build (`go build ./...`), test (`go test ./...`), lab integration (`scripts/integration-test.sh` env vars), release (tag `v*` → goreleaser).

- [ ] **Step 3: Write docs/e2e-checklist.md**

```markdown
# E2E acceptance checklist (Rancher v2.11)

Run per release candidate against the lab PVE. All boxes must pass.

## Setup
- [ ] Release published; NodeDriver registered with the release URL + checksum
      and the three whitelist domains; driver shows "Active" in
      Cluster Management > Drivers > Node Drivers.
- [ ] Cloud credential created (URL, token ID, token secret) and accepted.

## Create
- [ ] Create an RKE2 cluster: 1 machine pool "control" (1 node,
      etcd+controlplane), 1 pool "worker" (1 node, worker), both pvenode.
- [ ] Both VMs appear in PVE named after their machine names, tagged
      `rancher-pvenode`, in the configured VMID range.
- [ ] Cluster reaches Active; both nodes Ready in ~5-8 min each.

## Scale (the reliability bar: 20 consecutive ops without a flake)
- [ ] Scale worker pool 1 → 3 with the +/- counter. Both new nodes Ready.
      No VMID conflicts, no clone-lock failures (this is the parallel path).
- [ ] Scale 3 → 1. Removed VMs are deleted from PVE with their disks
      (check PVE storage for orphaned volumes: none).
- [ ] Repeat scale up/down until 20 total operations have run clean.

## Failure injection
- [ ] Create a pool with a deliberately broken template (agent removed):
      provisioning fails with the actionable agent error in the Rancher
      provisioning log, and the failed VM is cleaned up from PVE.
- [ ] Delete a machine from Rancher while it is mid-provisioning: no
      orphaned VM remains on PVE afterwards.

## Teardown
- [ ] Delete the cluster. PVE shows zero remaining VMs, zero orphaned
      disks, zero orphaned cloud-init volumes from this cluster.

## Recorded results
| Date | Driver version | Rancher | PVE | Scale ops | Orphans | Notes |
|------|----------------|---------|-----|-----------|---------|-------|
```

- [ ] **Step 4: Verify docs render and commit**

Run: `git add deploy/ README.md docs/e2e-checklist.md && git commit -m "docs: README, NodeDriver manifest, E2E checklist"`
Expected: committed. Skim README in a Markdown preview for broken formatting.

---

## Verification (whole-plan)

1. **Unit**: `go test ./...` — green, offline. Confirm the adversarial IP fixtures (docker0/cni0, IPv6-first, agent-before-DHCP, MAC case), VMID-conflict retry, clone-lock retry, tag-guard refusal, and idempotent-remove tests all exist and pass.
2. **Static**: `go vet ./...` and `golangci-lint run` clean.
3. **Integration (lab)**: `scripts/integration-test.sh` PASS against PVE 8.4 **and** a PVE 9.x host (set `PVE_URL` per host); then `scripts/integration-concurrent.sh` PASS (template-lock contention).
4. **E2E**: work through `docs/e2e-checklist.md` on Rancher v2.11 — including the 20-op scale soak and both failure injections.
5. **Release dry-run**: tag `v0.1.0-rc1`, confirm the GitHub release carries raw binaries + `checksums.txt`, register in Rancher via `deploy/nodedriver.yaml`, confirm the driver activates (no "downloading" hang).

## Out of scope (deliberate, from the spec)

UI extension (v2), LXC, static-IP/IPAM, multi-NIC, HA placement, Windows, `scsihw` override, `citype` escape hatch, SSH-probe fallback for agent-less images.
