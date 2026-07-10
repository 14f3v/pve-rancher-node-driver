# Design: pve-rancher-node-driver

**Date:** 2026-07-10
**Status:** Approved (design reviewed in two parts + 24-finding adversarial review folded in)
**Repo:** `pve-rancher-node-driver`

## 1. Problem and goal

We run bare-metal Kubernetes on Proxmox VE (PVE), managed through Rancher so team members without Kubernetes CLI experience can operate clusters. Scaling is the remaining manual step: adding a node means hand-provisioning a PVE VM and joining it to the cluster.

The legacy community driver (`lnxbil/docker-machine-driver-proxmox-ve`) is dormant since 2020: password-only auth, unfixed clone timeouts on PVE ≥ 7.2, VMID/storage races. In practice it produced flaky VM creation and nodes that never joined. The modern community alternative (`Stellatarum/docker-machine-driver-pve`) is active but weakest exactly where we got burned: DHCP/guest-agent IP detection stalls and PVE 9.x clone edges. We use it as prior art, not as a base.

**Goal:** a production-quality Rancher **node driver** for Proxmox VE, so RKE2/K3s clusters scale from the Rancher UI with the machine-pool +/- counter alone.

### Success criteria

- Scale a worker pool 1→3→1 from the Rancher v2.11 UI with zero manual steps; nodes reach `Ready` in ~5–8 minutes.
- Reliable on a DHCP network: IP learned via qemu-guest-agent with MAC-matched interface selection and retry/backoff.
- Fail fast, fail clear: misconfiguration is caught before any VM is created, with an actionable message in the Rancher provisioning log.
- Zero orphans: failed creates self-clean; deleting a cluster leaves nothing on PVE — including the mid-create-crash case.
- Supports PVE 8.x and 9.x with a version-aware privilege model.
- Reliability bar: 20 consecutive scale operations without a flake.

### Scope decisions (confirmed with owner)

| Decision | Choice |
|---|---|
| Target clusters | New RKE2/K3s clusters via Rancher machine pools; the existing kubeadm cluster stays imported and is out of scope |
| Rancher version | v2.11 |
| PVE versions | 8.x and 9.x (9.x primary — 8.x security EOL is 2026-08-31) |
| VM strategy | Clone from a prepared cloud-init template |
| Node IPs | DHCP |
| Build vs adopt | Build our own driver; `Stellatarum/docker-machine-driver-pve` is prior art |
| UI | v1 uses Rancher's auto-generated form from driver flags; a UI extension is a v2 project |
| PVE topology | Single host now; cluster-ready flags (target node, storage) from day one |

Known horizon: node drivers remain the supported path for Rancher machine pools with no announced deprecation, but Rancher's long-term direction is native CAPI providers. Plan on a 2–3 year useful life; the CAPI route (Turtles + CAPMOX) is the eventual migration target.

## 2. Architecture

One Go binary, `docker-machine-driver-pvenode` (driver name `pvenode`, flag prefix `--pvenode-`), implementing the `rancher/machine` libmachine `drivers.Driver` interface. There is no server component: Rancher downloads the binary from GitHub Releases, enumerates its flags to auto-generate the cluster-creation form, and runs it inside provisioning jobs.

```
cmd/docker-machine-driver-pvenode/  → plugin entry (plugin.RegisterDriver)
pkg/driver/                         → Driver struct, flags, lifecycle methods
pkg/pve/                            → go-proxmox wrapper: clone, configure, cloud-init,
                                      task-wait, agent-IP wait, delete;
                                      retry/backoff + context timeouts on every call
pkg/validate/                       → PreCreateCheck fail-fast validation
```

**Stack:** Go; `luthermonson/go-proxmox` pinned at v0.8.0 and vendored (pre-1.0 with breaking changes between minors — v0.8.0 changed `Delete`'s signature). Verify actual method names/signatures (`GetAgentNetworkIFaces`, `WaitForAgent`, `Task.WaitForCompleteStatus`) before coding against them.

**Driver state contract:** every value a later lifecycle call needs (VMID, node, SSHUser, IPAddress, SSHKeyPath) is an exported struct field — each lifecycle call runs as a fresh RPC subprocess that deserializes `config.json`. The SSH key is generated with `ssh.GenerateSSHKey(d.GetSSHKeyPath())` into the machine dir; rancher-machine reads the private key from exactly that path.

**Full interface:** `Create`, `Remove`, `Start`, `Stop` (graceful with timeout, then force), `Kill` (hard stop), `Restart`, `GetState` (maps PVE status including lock states; a gone VM returns cleanly so `rm` stays idempotent), `GetURL` (`tcp://<ip>:2376`; errors while IP unknown), `GetIP`/`GetSSHHostname` (re-query the agent rather than trusting cached IP — DHCP leases change), `GetSSHPort`, `GetSSHUsername`, `GetSSHKeyPath`, `GetMachineName`, `GetCreateFlags`, `SetConfigFromFlags`, `PreCreateCheck`. No `Upgrade` (provisioner concern, not part of the driver interface).

## 3. Configuration surface

**Cloud credential** (stored once in Rancher, reused per cluster): PVE URL, API token ID (`user@realm!tokenid`), token secret, CA certificate or insecure-TLS flag.

Credential separation is expressed through **NodeDriver annotations**, not the binary: `privateCredentialFields: "tokenSecret"`, `publicCredentialFields` for URL/token ID (normalized camelCase field names). Without these annotations Rancher generates no cloud-credential schema and the secret is stored in plaintext machine config. The registration manifest ships the exact annotation set.

**Machine config** (per machine pool): template (name or VMID), storage, target PVE node (optional; defaults to the template's node), full/linked clone, cores, memory, disk size in GB (grow-only, absolute), network bridge, VLAN tag, resource pool, SSH username, agent-IP timeout (default 300 s), VMID range (default 10000–19999), cipassword (console rescue when SSH breaks), nameserver/searchdomain, CPU type, onboot, extra VM tags, keep-on-failure debug flag (standalone CLI use only; see §7).

**Cloud-init:** v1 uses only PVE's built-in fields (`ciuser`, `cipassword`, `sshkeys`, `ipconfig0=dhcp`, `nameserver`, `searchdomain`) — no custom user-data snippets, which would require snippets-storage permissions and are a common driver failure source. SSH keys pass through go-proxmox's `EncodeSSHKeys` helper exactly once (PVE requires rawurlencode semantics; hand-escaping causes 500 "SSH public key validation error").

**Secret hygiene:** the token secret is redacted from all logging, including the RPC flag echo.

## 4. PreCreateCheck — fail fast, before any VM exists

1. API reachable; query `/version` and select the PVE-8 or PVE-9 privilege set. PVE 9 removed `VM.Monitor`; guest-agent reads need `VM.GuestAgent.Audit`. The README ships two role definitions.
2. Check `/access/permissions` for the **token** first. Privsep tokens without their own ACLs get silently filtered empty lists — not 403s — so every downstream error becomes misleading ("template not found" when the truth is "token has no ACL"). Required set (PVE 9 flavor): `VM.Clone`, `VM.Allocate`, `VM.Config.*`, `VM.PowerMgmt`, `VM.Audit`, `VM.GuestAgent.Audit`, `Datastore.AllocateSpace`, `Datastore.Audit`; `SDN.Use` on the bridge (ACL path `/sdn/zones/localnetwork/<bridge>`, propagated for VLAN tags); `Pool.Allocate` if a pool is set.
3. Template exists, is a template, has `agent: 1`, and has a cloud-init drive (without one, `ciuser`/`sshkeys` are stored but never delivered — the create times out later with a misleading error).
4. Clone-mode matrix: linked clone + explicit storage → hard error (PVE silently ignores storage on linked clones); linked clone requires base-image-capable storage (qcow2/LVM-thin/ZFS/RBD); target node ≠ template node requires shared storage.
5. Storage exists on the target node with `images` content type; bridge exists.
6. Requested disk size ≥ template boot-disk size (PVE cannot shrink; catching this here avoids a half-created VM).
7. MachineName fits guest hostname limits (Rancher owns naming; docs recommend short cluster/pool names).

## 5. Create flow

1. PreCreateCheck (§4).
2. **VMID: random within the configured range, retry on conflict.** `/cluster/nextid` is non-atomic and races when Rancher creates pool nodes in parallel; random+retry is the accepted fix and also keeps Rancher VMs visually segregated from hand-allocated IDs.
3. **Clone** (linked or full per flag), waiting on the PVE task. VM name = MachineName; a PVE tag `rancher-pvenode` is stamped at clone time (the orphan-GC anchor, §7). Retry with jitter on `VM is locked (clone)`, cfs-lock, and lock-timeout errors — parallel clones contend on the template's source lock.
4. **Configure:** cores, memory, CPU type, bridge/VLAN, onboot, guest agent on, cloud-init fields, tags; then resize the disk, waiting on the resize task (it returns a UPID on modern PVE).
5. **Start**, task-wait.
6. **Wait for IP — MAC-matched:** read `net0`'s MAC from the VM config, poll the agent's network interfaces for the one whose `hardware-address` matches, and wait until *that interface* has an IPv4. Rationale: "agent responded" ≠ "has an address" (the agent often answers before DHCP completes); interface-name selection breaks across distro renames (eth0/ens18); first-IPv4 heuristics pick `docker0`/`cni0`/tailscale on reused templates. Exponential-backoff polling with a configurable overall timeout (default 300 s); "agent not up yet" keeps waiting while hard errors abort; on timeout the error says: "agent never reported in Xs — check qemu-guest-agent is installed and enabled in the template." Probe TCP:22 before declaring success.
7. Hand off — rancher-machine SSHes in with the generated key and Rancher installs RKE2 via its machine job. The driver's job is done.

## 6. Remove and lifecycle

- `Remove`: graceful stop with timeout → force stop → delete with disk purge. Verify go-proxmox v0.8.0's `Delete` exposes `purge=1` and `destroy-unreferenced-disks=1`; if not, use a raw request — otherwise cloud-init disks and unreferenced volumes leak.
- Idempotent: VM already gone counts as success; `VM is locked` is retried.

## 7. Zero-orphan guarantee

- **The VMID-persistence gap:** rancher-machine persists driver config before `Create()` and after it returns. A mid-create crash (or killed provisioning pod) leaves a config with no VMID, and Rancher's subsequent `rm` job would orphan the VM. Fix: `Remove()` looks up by persisted VMID first, then falls back to lookup by **name AND the `rancher-pvenode` tag** — never name alone, since name/VMID reuse across clusters could delete the wrong VM.
- Cleanup inside a failed `Create` first waits for or aborts the in-flight clone task (deleting fails on the clone lock otherwise), then deletes the half-created VM.
- A keep-for-debug flag disables self-cleanup for standalone CLI use only; in Rancher the rm job always calls `Remove`.

## 8. Error handling

Every PVE call gets a context timeout and exponential backoff on transient errors (5xx, timeouts). Every PVE task's UPID exit status is checked, never assumed. Error messages name the problem and the fix. Each step logs clearly — these logs surface in Rancher's provisioning UI, so operators see "waiting for guest agent (2m10s/5m)…" instead of a silent spinner. Secrets are redacted.

## 9. Testing

- **Unit** (mocked PVE API via httptest): table-driven IP-selection tests with adversarial fixtures (docker0/cni0/tailscale present, IPv6-first, agent-up-before-DHCP, MAC matching); VMID-conflict retry; clone-lock retry; flag validation; the clone-mode matrix.
- **Integration** (the fast loop): drive the built driver binary with the `rancher-machine` CLI directly against the lab PVE — no Rancher needed, seconds to iterate. Run the matrix: PVE 8.4 **and** 9.x (privilege model and task shapes differ). Include 3 concurrent creates to exercise template-lock contention.
- **E2E checklist** (documented in the repo): register the driver in Rancher v2.11 → create an RKE2 cluster (1 control-plane + 1 worker) → scale 1→3→1 → delete the cluster → verify zero orphaned VMs, including after one deliberately failed create.
- **CI:** GitHub Actions — golangci-lint, unit tests, goreleaser cross-compile. Ship linux/amd64 and linux/arm64 (the binary runs inside Rancher's pods); darwin builds are for local development only.

## 10. Distribution and documentation

- GitHub Releases with SHA256 checksums. **Versioned release URLs only** — Rancher re-stages the binary only when URL or checksum changes; never republish a changed binary at the same URL.
- NodeDriver registration whitelist domains: `github.com`, `objects.githubusercontent.com`, **and** `release-assets.githubusercontent.com` (GitHub's 2025 asset-host migration; missing it leaves the driver stuck "downloading"). The registration manifest includes the credential-field annotations (§3).
- README covers:
  - PVE API-token setup with two role definitions (PVE 8 and PVE 9) and the privsep/ACL explanation.
  - Template preparation: Ubuntu 24.04 cloud image with qemu-guest-agent **baked in** via `virt-customize` (recommended — the stock cloud image lacks the agent, the #1 cause of "stuck waiting for IP"), or cloud-init package install as fallback.
  - The provisioning-v2 SSH contract: the cloud-init user needs **passwordless sudo, curl, and bash**. Without them the driver reports success and Rancher's system-agent install fails afterward — the worst kind of failure to debug.

## 11. Out of scope for v1 (v1.x candidates)

UI extension (planned v2, once the driver is proven), LXC containers, static IP/IPAM, multi-NIC, HA placement across PVE cluster nodes, Windows nodes, `scsihw` override, `citype` escape hatch, SSH-probe fallback for agent-less images.

## 12. Key references

- Prior art: [Stellatarum/docker-machine-driver-pve](https://github.com/Stellatarum/docker-machine-driver-pve) (Apache-2.0); [lnxbil/docker-machine-driver-proxmox-ve](https://github.com/lnxbil/docker-machine-driver-proxmox-ve) (dormant).
- Driver SDK: [rancher/machine](https://github.com/rancher/machine) — libmachine `drivers.Driver`, `mcnflag`, localbinary plugin protocol.
- PVE client: [luthermonson/go-proxmox](https://github.com/luthermonson/go-proxmox) v0.8.0.
- Rancher node-driver mechanics: [machine-drivers internals](https://extensions.rancher.io/internal/code-base-works/machine-drivers), [manage node drivers](https://ranchermanager.docs.rancher.com/how-to-guides/new-user-guides/authentication-permissions-and-global-configuration/about-provisioning-drivers/manage-node-drivers).
- PVE specifics: [VM Templates and Clones](https://pve.proxmox.com/wiki/VM_Templates_and_Clones), [Resize disks](https://pve.proxmox.com/wiki/Resize_disks), PVE 9 `VM.GuestAgent.*` privilege split (pve-devel), `SDN.Use` bridge ACLs (PVE forum 130337), non-atomic `/cluster/nextid` (PVE forum 123984).
