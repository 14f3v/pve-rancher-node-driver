# pve-rancher-node-driver

A native [Rancher node driver](https://ranchermanager.docs.rancher.com/pages-for-subheaders/node-drivers-and-node-templates) for [Proxmox VE](https://www.proxmox.com/en/proxmox-virtual-environment/overview), driver name **`pvenode`**. It lets an RKE2 or K3s cluster in Rancher provision, scale, and remove Proxmox VE virtual machines as machine-pool nodes straight from the Rancher UI — no external IPAM, no hand-run `qm` commands, no orphaned VMs when things go wrong.

The design goal is a scale-the-worker-pool-and-walk-away experience: click the `+` on a machine pool, a VM clones and boots on Proxmox, and the node joins the cluster and reaches `Ready` in roughly 5–8 minutes, repeatably, on an ordinary DHCP network. That last part is the reason this driver exists rather than reusing one of the older community drivers: it determines a new VM's IP address by matching the freshly-created NIC's MAC address against the interfaces reported by the QEMU guest agent, so it does not get fooled by Docker/CNI bridges, IPv6 link-local addresses, or a guest agent that answers before DHCP has actually finished. VM lifecycle is designed for zero orphans: a failed `Create` cleans up after itself, `Remove` is idempotent, and deletes are guarded by an ownership tag so the driver never touches a VM it didn't create.

Supported Proxmox VE versions are 8.x and 9.x — see [Compatibility](#compatibility) below for the differences that matter operationally (mainly which privilege the driver's API token needs).

For the full design rationale — why this driver exists instead of adopting an existing community one, the IP-detection algorithm, the clone/VMID-allocation concurrency model, and the failure-injection test matrix — see [`docs/superpowers/specs/2026-07-10-pve-rancher-node-driver-design.md`](docs/superpowers/specs/2026-07-10-pve-rancher-node-driver-design.md).

## Compatibility

- **Rancher**: v2.11 or later.
- **Proxmox VE**: 8.x and 9.x are both supported. **9.x is the primary target** — 8.x is in its security-maintenance tail and reaches end of life on **2026-08-31**, so new deployments should prefer 9.x where possible.
- The one place PVE 8 vs 9 matters to an operator is the API token's privilege set (below): PVE 9 replaced the `VM.Monitor` privilege with the more narrowly scoped `VM.GuestAgent.Audit` for reading guest-agent data. The driver detects the running PVE major version at `Create` time and validates against the right set automatically; you still need to have granted the right role.

## PVE API token setup

The driver authenticates to the Proxmox VE API with an **API token**, not a username/password. Create a dedicated `rancher@pve` user and a token under it — never point this driver at a token derived from an administrative account.

The detail that trips people up here is **privilege separation (privsep)**. By default, a PVE API token does **not** inherit the permissions of the user it belongs to — the token has its own, separate ACL entries, and starts with zero privileges even if the owning user is a full administrator. You have two options:

1. **(Recommended)** Grant the driver's role to *both* the user and the token explicitly, as shown below. This keeps the token's blast radius auditable and equal to exactly what the driver needs.
2. Create the token with `--privsep 0`, which makes it inherit the user's permissions. This is simpler but means the token is as powerful as the user account it comes from — avoid this unless you understand the tradeoff.

If you skip granting the ACL to the token itself (the second `pveum acl modify ... -token` line below), the symptom is not an authentication error — it's *silent, empty* API responses: the template dropdown in the Rancher UI comes back blank, clones fail with vague permission errors, and nothing points you at "the token has no ACLs." The driver's pre-create validation checks the token's effective permissions up front specifically to catch this before it wastes your time.

Run one of the two role definitions below depending on your PVE major version, then create the user, token, and ACLs:

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

The token ID you give Rancher is `rancher@pve!machine`; the token secret is printed once by `pveum user token add` — save it, PVE will not show it again.

## Template preparation

VMs are created by cloning a Proxmox VE template, so the template has to be prepared correctly before you point the driver at it. This example builds an Ubuntu 24.04 cloud-image template, but the same three requirements apply to any distro image you use:

1. **`qemu-guest-agent` must be baked into the image itself**, not installed by a post-boot script. The driver has no other way to learn a freshly-cloned VM's DHCP-assigned IP address than asking the guest agent — if the agent isn't present and running the moment the VM comes up, `Create` will time out waiting for an IP that never gets reported.
2. **The VM needs a cloud-init drive.** The driver configures the VM's network, hostname, and SSH key material through cloud-init on every clone; without the `ide2`/cloud-init disk there is nothing for the driver to write that configuration to.
3. **The cloud-init user needs passwordless `sudo`, plus `curl` and `bash` on the `PATH`.** Rancher's own provisioning (the system-agent bootstrap that turns a booted VM into a cluster node) runs entirely over SSH as this user *after* the driver has already reported the VM as created — the driver's own job is done by the time this step runs, so if the cloud-init user can't sudo without a password prompt, or the image is missing `curl`/`bash`, the VM comes up, the driver says it succeeded, and the node then just sits there and never goes `Ready`. Stock Ubuntu cloud images already configure the default cloud-init user this way, so as long as you don't override `sudo`/`ssh_pwauth` in your own cloud-init user-data, this requirement is satisfied automatically.

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

`virt-customize --install qemu-guest-agent` bakes the agent into the disk image itself before any VM boots from it, which is what satisfies requirement (1) above — installing the agent via a first-boot cloud-init script is not equivalent, because the driver starts polling for the agent immediately after clone. `--agent 1` on `qm create` tells PVE to expect and talk to the guest agent for this VM; the `--ide2 local-lvm:cloudinit` line on `qm set` attaches the cloud-init drive from requirement (2). Once `qm template 9000` runs, VM 9000 becomes an immutable template — `pvenode-template` in the driver's config can reference it either as `9000` (numeric VMID) or `ubuntu-2404-tmpl` (name).

## Registering the driver in Rancher

Apply the manifest against Rancher's local cluster:

```bash
kubectl apply -f deploy/nodedriver.yaml
```

Before applying, edit the two placeholders in `deploy/nodedriver.yaml`: `<VERSION>` (the release tag, e.g. `v0.1.0`) and `<SHA256>` (the matching line for `docker-machine-driver-pvenode-linux-amd64` out of that release's `checksums.txt`). If you'd rather not edit YAML, the same fields are available through the UI at **Cluster Management > Drivers > Node Drivers > Add Node Driver** — paste the release's binary URL and checksum there instead.

One detail in the manifest is easy to get wrong and will leave the driver stuck: `whitelistDomains` must list **all three** of `github.com`, `objects.githubusercontent.com`, and `release-assets.githubusercontent.com`. Rancher fetches the driver binary through a redirect chain — the `github.com/.../releases/download/...` URL redirects to `objects.githubusercontent.com`, and since 2025 GitHub further redirects large release-asset downloads through `release-assets.githubusercontent.com`. Rancher's node-driver downloader only follows redirects to domains present in `whitelistDomains`; if any one of the three is missing, the download silently stalls and the driver sits in "Downloading" in the UI indefinitely instead of becoming "Active." `deploy/nodedriver.yaml` ships with all three already listed — if you hand-author your own manifest, or use the UI form (which also has a whitelist-domains field), make sure not to drop any of them.

Rancher only re-downloads the driver binary when the `url` or `checksum` field actually changes, so on every release bump both fields together — reusing a URL across releases with a changed binary will leave stale nodes running the old driver.

## Creating a cluster

Once the driver is Active, create a **cloud credential** of type Proxmox VE (pvenode): supply the PVE API `url` (e.g. `https://pve.example.com:8006`), the `tokenId` (`rancher@pve!machine`), and the `tokenSecret` from the [PVE API token setup](#pve-api-token-setup) step above. If your PVE API uses a certificate that isn't trusted system-wide, either enable `insecureTls` for lab/dev use or supply `caCert` with the PEM-encoded CA certificate content (not a file path) so Rancher can verify the connection properly.

With the cloud credential in hand, create an RKE2 or K3s cluster and add one or more **machine pools** using the `pvenode` node driver. Every flag the driver exposes shows up as a form field on the machine pool (flag name shown here without the `pvenode-` prefix that Rancher adds automatically):

| Flag | Default | Description |
|------|---------|-------------|
| `url` | *(required)* | Proxmox VE API URL, e.g. `https://pve.example.com:8006` |
| `token-id` | *(required)* | PVE API token ID (`user@realm!tokenname`) |
| `token-secret` | *(required)* | PVE API token secret |
| `insecure-tls` | `false` | Skip TLS certificate verification |
| `ca-cert` | *(empty)* | PEM CA certificate to trust for the PVE API (content, not a path) |
| `node` | *(template's node)* | Target PVE node name |
| `template` | *(required)* | Template to clone: VM name or numeric VMID |
| `storage` | *(same as template)* | Target storage for full clones |
| `linked-clone` | `false` | Use a linked clone instead of a full clone |
| `cores` | `2` | Number of CPU cores |
| `memory` | `4096` | Memory in MB |
| `cpu-type` | *(PVE default)* | CPU type, e.g. `host` |
| `disk-size` | `0` | Boot disk size in GB (grow-only; `0` = keep template size) |
| `bridge` | *(template's bridge)* | Network bridge for `net0`, e.g. `vmbr0` |
| `vlan` | `0` | VLAN tag for `net0` (`0` = none). Requires `bridge` (the tag is written onto `net0`, which must name a bridge). |
| `pool` | *(none)* | PVE resource pool for created VMs |
| `ssh-user` | `rancher` | Cloud-init user for SSH provisioning |
| `agent-timeout` | `300` | Seconds to wait for the guest agent to report an IP |
| `vmid-range` | `10000-19999` | VMID allocation range `lo-hi` |
| `cipassword` | *(empty)* | Optional cloud-init password (console rescue) |
| `nameserver` | *(empty)* | Optional DNS server override via cloud-init |
| `searchdomain` | *(empty)* | Optional DNS search domain via cloud-init |
| `onboot` | `false` | Start the VM automatically when the PVE host boots |
| `tags` | *(empty)* | Extra PVE tags, comma-separated |
| `keep-on-failure` | `false` | Keep the VM when `Create` fails (standalone CLI debugging only) |

`ssh-user` must name the same cloud-init user covered under [Template preparation](#template-preparation) — passwordless sudo, `curl`, and `bash`. `vmid-range` should be sized and (if you run more than one driver/cluster against the same PVE) partitioned per-cluster to avoid VMID collisions; the driver allocates within the range with retry-on-conflict, but non-overlapping ranges keep contention low. Every VM the driver creates is tagged `rancher-pvenode` (plus anything you add in `tags`) — this tag is how `Remove` recognizes VMs it's allowed to delete.

Once a pool is configured, scale it up or down with the ordinary `+`/`-` counter on the machine pool in the Rancher UI — the driver handles VMID allocation, cloning, cloud-init configuration, boot, and IP discovery for each node it adds, and clean VM + disk deletion for each node removed.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Node stuck "waiting for IP" | `qemu-guest-agent` isn't in the template image (or wasn't running), the agent is disabled in the VM's PVE config (`--agent 1` missing), or DHCP itself is broken/unreachable on the configured bridge/VLAN. See [Template preparation](#template-preparation). |
| Template dropdown / list is silently empty in the Rancher UI | The API token has privilege separation (privsep) enabled but was never granted its own ACL — see [PVE API token setup](#pve-api-token-setup); PVE returns an empty result set rather than an auth error. |
| Driver stuck "Downloading" in Cluster Management > Drivers > Node Drivers | `whitelistDomains` in the NodeDriver manifest is missing one of the three required domains (`github.com`, `objects.githubusercontent.com`, `release-assets.githubusercontent.com`). See [Registering the driver in Rancher](#registering-the-driver-in-rancher). |
| VM boots, driver reports success, but the node never reaches `Ready` | The cloud-init user (`ssh-user`) can't run passwordless `sudo`, or the image is missing `curl`/`bash` — Rancher's system-agent bootstrap over SSH is failing after the driver's own work is already done. See [Template preparation](#template-preparation). |
| `Remove` (or a scale-down) fails with "refusing to delete VM ... it does not carry the ... ownership tag" | The VM at that name/VMID wasn't created by this driver — it was hand-created, cloned outside Rancher, or the name/VMID was reused by something else. This is a deliberate safety guard: the driver only deletes VMs tagged `rancher-pvenode` at create time, so it never destroys a VM it doesn't own, even if a name or VMID collides. |

## Development

Standard Go tooling:

```bash
go build ./...
go test ./...
```

Testing against a real Proxmox VE host (not just the unit tests, which run offline against a fake PVE API) is driven by an integration script that reads its target from environment variables:

```bash
PVE_URL=https://pve-lab.example.com:8006 \
PVE_TOKEN_ID='rancher@pve!machine' \
PVE_TOKEN_SECRET='...' \
  scripts/integration-test.sh
```

Releases are automated: pushing a tag matching `v*` triggers the `release` GitHub Actions workflow, which runs [`goreleaser`](https://goreleaser.com/) to build the `docker-machine-driver-pvenode` binaries for linux/darwin (amd64/arm64, per `.goreleaser.yaml`) and publish them as raw binaries plus a `checksums.txt` on the GitHub release — the exact asset layout `deploy/nodedriver.yaml`'s `url` and `checksum` fields expect.
