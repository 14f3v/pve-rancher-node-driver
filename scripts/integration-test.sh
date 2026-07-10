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
