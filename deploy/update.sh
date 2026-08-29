#!/usr/bin/env bash
#
# Push a rebuilt binary into an existing container and restart it.
#
#   deploy/update.sh                       # defaults below
#   PVE=10.0.0.5 CTID=311 deploy/update.sh
#
# The service is stopped before the push: pct push into a running binary fails
# with "Text file busy", and because that failure does not stop the restart
# that follows, the container silently carries on running the old code.
set -euo pipefail

cd "$(dirname "$0")/.."

PVE="${PVE:-192.168.10.10}"
CTID="${CTID:-310}"
SSH_USER="${SSH_USER:-root}"

echo "building"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags='-s -w' -o /tmp/sonospoof-linux-amd64 ./cmd/sonospoof

echo "copying to ${PVE}"
scp -q /tmp/sonospoof-linux-amd64 "${SSH_USER}@${PVE}:/tmp/"

echo "installing into CT ${CTID}"
ssh "${SSH_USER}@${PVE}" bash -s "$CTID" <<'EOS'
set -euo pipefail
ctid="$1"
pct exec "$ctid" -- systemctl stop sonospoof
sleep 1
pct push "$ctid" /tmp/sonospoof-linux-amd64 /usr/local/bin/sonospoof --perms 755
pct exec "$ctid" -- systemctl start sonospoof
sleep 5
pct exec "$ctid" -- journalctl -u sonospoof --no-pager -n 8 -o cat
EOS
