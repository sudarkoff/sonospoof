#!/usr/bin/env bash
#
# Create and provision the sonospoof LXC on a Proxmox host.
#
# Run this ON the Proxmox host, not on a workstation:
#
#   scp sonospoof-linux-amd64 sonospoof.service install-lxc.sh pve:/tmp/
#   ssh pve 'bash /tmp/install-lxc.sh'
#
# The container MUST sit on the same VLAN as the speakers. This is not a
# preference. Two independent things break otherwise, and only one of them is
# obvious:
#
#   1. SSDP discovery is link-scoped multicast. It does not cross a VLAN and
#      is not reflected by consumer routers the way mDNS is, so the daemon
#      finds no speakers at all from another subnet.
#
#   2. Far worse, the speaker has to open a TCP connection *back* to the
#      daemon to fetch its audio. A segmented network typically allows the
#      outbound direction only, so SetAVTransportURI succeeds, the track title
#      appears in the Sonos app, and no sound ever comes out. That failure
#      looks like a bug in this program and is not one.
#
# The phone does not need to be on that VLAN: mDNS reflection carries the
# advertisement, and the phone opens its connections inbound, which segmented
# networks generally permit.
set -euo pipefail

# ---------------------------------------------------------------- config

CTID="${CTID:-310}"
# Not HOSTNAME: bash sets that to the host's own name, so a default would
# never apply and the container would be created named after the hypervisor.
CTNAME="${CTNAME:-sonospoof}"
STORAGE="${STORAGE:-local-lvm}"
TEMPLATE_STORAGE="${TEMPLATE_STORAGE:-local}"
TEMPLATE="${TEMPLATE:-debian-12-standard_12.12-1_amd64.tar.zst}"
BRIDGE="${BRIDGE:-vmbr0}"

# VLAN tag for the speakers' network. 30 is George's IoT VLAN.
#
# Set VLAN="" for an untagged bridge -- which is what you want when the bridge
# is already VLAN-specific, e.g. a vmbr30 built on a tagged sub-interface.
# Note ${VLAN-30} and not ${VLAN:-30}: the latter also substitutes for an
# empty value, so VLAN="" would silently become 30 again.
VLAN="${VLAN-30}"

# DHCP by default. For a static address set e.g.
#   IPV4="192.168.30.10/24" GATEWAY="192.168.30.1"
IPV4="${IPV4:-dhcp}"
GATEWAY="${GATEWAY:-}"

MEMORY="${MEMORY:-512}"
DISK="${DISK:-4}"
CORES="${CORES:-2}"

BINARY="${BINARY:-/tmp/sonospoof-linux-amd64}"
UNIT="${UNIT:-/tmp/sonospoof.service}"

# ---------------------------------------------------------------- checks

command -v pct >/dev/null || { echo "pct not found -- run this on the Proxmox host" >&2; exit 1; }
[ -f "$BINARY" ] || { echo "binary not found at $BINARY" >&2; exit 1; }
[ -f "$UNIT" ] || { echo "unit file not found at $UNIT" >&2; exit 1; }

if pct status "$CTID" >/dev/null 2>&1; then
  echo "container $CTID already exists. Remove it first, or set CTID=<other>." >&2
  exit 1
fi

tpl="${TEMPLATE_STORAGE}:vztmpl/${TEMPLATE}"
if ! pveam list "$TEMPLATE_STORAGE" 2>/dev/null | grep -q "$TEMPLATE"; then
  echo "template $TEMPLATE not present; downloading"
  pveam update
  pveam download "$TEMPLATE_STORAGE" "$TEMPLATE"
fi

# ---------------------------------------------------------------- create

net="name=eth0,bridge=${BRIDGE},firewall=0"
[ -n "$VLAN" ] && net="${net},tag=${VLAN}"
if [ "$IPV4" = "dhcp" ]; then
  net="${net},ip=dhcp"
else
  net="${net},ip=${IPV4}"
  [ -n "$GATEWAY" ] && net="${net},gw=${GATEWAY}"
fi

echo "creating container $CTID ($CTNAME) on ${BRIDGE}${VLAN:+ VLAN $VLAN}"

# Unprivileged: the daemon needs no elevated capabilities. Every port it binds
# is above 1024 and it writes nothing outside its own tmp.
pct create "$CTID" "$tpl" \
  --hostname "$CTNAME" \
  --cores "$CORES" \
  --memory "$MEMORY" \
  --swap 0 \
  --rootfs "${STORAGE}:${DISK}" \
  --net0 "$net" \
  --unprivileged 1 \
  --features nesting=0 \
  --onboot 1 \
  --description "AirPlay bridge for Gen1 Sonos. Must stay on the speakers' VLAN -- see install-lxc.sh."

pct start "$CTID"

echo "waiting for the container to get an address"
for _ in $(seq 1 30); do
  addr=$(pct exec "$CTID" -- ip -4 -o addr show dev eth0 2>/dev/null | awk '{print $4}' | cut -d/ -f1)
  [ -n "${addr:-}" ] && break
  sleep 1
done
[ -n "${addr:-}" ] || { echo "container never got an IPv4 address; check the bridge and VLAN tag" >&2; exit 1; }
echo "container address: $addr"

# ---------------------------------------------------------------- provision

echo "installing binary and unit"
pct push "$CTID" "$BINARY" /usr/local/bin/sonospoof --perms 755
pct push "$CTID" "$UNIT" /etc/systemd/system/sonospoof.service --perms 644

# A second mDNS responder on the same host is a reliable way to produce
# intermittent, unexplainable discovery failures. The daemon carries its own,
# so Avahi must not be present.
pct exec "$CTID" -- bash -eu <<'EOS'
if systemctl is-enabled avahi-daemon >/dev/null 2>&1 || dpkg -s avahi-daemon >/dev/null 2>&1; then
  echo "removing avahi-daemon: two mDNS responders on one host conflict"
  systemctl disable --now avahi-daemon avahi-daemon.socket 2>/dev/null || true
  DEBIAN_FRONTEND=noninteractive apt-get -y purge avahi-daemon >/dev/null 2>&1 || true
fi

id -u sonospoof >/dev/null 2>&1 || useradd --system --no-create-home --shell /usr/sbin/nologin sonospoof

systemctl daemon-reload
systemctl enable --now sonospoof.service
EOS

echo
echo "waiting for the daemon to discover speakers"
sleep 8
pct exec "$CTID" -- systemctl --no-pager --lines=25 status sonospoof.service || true

cat <<EOF

Container $CTID is up at $addr.

  logs:     pct exec $CTID -- journalctl -u sonospoof -f
  restart:  pct exec $CTID -- systemctl restart sonospoof
  shell:    pct enter $CTID

If it reports no ZonePlayers found, the container is on the wrong VLAN --
check that net0 carries tag=${VLAN:-<none>} and that $addr is on the same
subnet as the speakers.
EOF
