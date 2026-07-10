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
