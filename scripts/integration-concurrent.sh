#!/usr/bin/env bash
# Exercises the VMID race and the template clone lock: three concurrent
# creates from separate rancher-machine processes (exactly what Rancher
# does when a pool scales 1 -> 3), then removes them all.
#
# The driver binary and rancher-machine are built/fetched ONCE up front
# (serially), then the three workers run with PVENODE_SKIP_BUILD=1 so they
# never race on the shared build output or the rancher-machine download —
# otherwise a corrupted binary would masquerade as a PVE-side race failure.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SCRIPT="$ROOT/scripts/integration-test.sh"

echo "==> warming up (build driver + fetch rancher-machine, once)"
PVENODE_WARM_ONLY=1 "$SCRIPT"

pids=()
names=()
for i in 1 2 3; do
  name="pvenode-race-$i-$RANDOM"
  names+=("$name")
  PVENODE_SKIP_BUILD=1 "$SCRIPT" "$name" &
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
