#!/usr/bin/env bash
#
# Build the static Linux binary for the LXC and stage it for deployment.
#
# CGO is off so the result has no libc dependency and runs on any Debian
# template regardless of its glibc version.
set -euo pipefail

cd "$(dirname "$0")/.."
out="${1:-./dist}"
mkdir -p "$out"

GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags='-s -w' -o "$out/sonospoof-linux-amd64" ./cmd/sonospoof

cp deploy/sonospoof.service deploy/install-lxc.sh "$out/"

echo "staged in $out:"
ls -lh "$out"
cat <<EOF

Copy to the Proxmox host and run it there:

  scp $out/sonospoof-linux-amd64 $out/sonospoof.service $out/install-lxc.sh pve:/tmp/
  ssh pve 'bash /tmp/install-lxc.sh'

Override defaults with environment variables, e.g.

  ssh pve 'CTID=311 VLAN=30 IPV4=192.168.30.10/24 GATEWAY=192.168.30.1 bash /tmp/install-lxc.sh'
EOF
