# sonospoof

AirPlay to first-generation Sonos speakers.

Gen1 Sonos hardware (Play:1, Play:3, Play:5 gen1, Connect, ZP80/90/100) will
never get AirPlay firmware — AirPlay 2 only ever shipped on gen2 and later. But
every Sonos is a UPnP/AV MediaRenderer on port 1400 that will happily play an
arbitrary HTTP URL. `sonospoof` sits in that gap: it pretends to be an AirPlay
receiver, and relays what it receives to the speaker as an HTTP stream.

```
iPhone --AirPlay/RAOP--> [ sonospoof ] --HTTP stream--> Sonos
                              |
                              +--SOAP SetAVTransportURI--> Sonos
```

Every discovered zone appears as its own AirPlay target and can be streamed to
independently. Verified against three Play:1 units with both an iPhone and a
Mac as sender.

## Expect 2–4 seconds of latency

AirPlay 1 buffers about two seconds by design, and Sonos adds its own buffer on
HTTP streams that cannot be turned off. This is fine for music and useless for
video — lip-sync is not achievable on this architecture. That is a property of
the approach, not a bug to be filed.

## Running it

```sh
go build ./cmd/sonospoof
./sonospoof                      # every discovered zone
./sonospoof -zone Garage         # just one
./sonospoof -hosts 192.168.1.50  # skip SSDP, name the players directly
./sonospoof -dump /tmp/dump      # tee served PCM to .wav, for diagnosing audio
```

### The network requirement, which is not optional

**The daemon must be on the same subnet as the speakers.** Two separate things
break otherwise, and only one of them is obvious:

1. SSDP discovery is link-scoped multicast. It does not cross a VLAN and is not
   reflected the way mDNS is, so nothing is discovered at all.
2. Far worse, the speaker has to open a TCP connection *back* to the daemon to
   fetch its audio. A segmented network typically permits the outbound
   direction only, so `SetAVTransportURI` succeeds, the track title appears in
   the Sonos app, and no sound ever comes out. That failure looks like a bug in
   this program and is not one.

The phone does not need to be on that subnet: mDNS reflection carries the
advertisement, and the phone opens its connections inbound.

## Deployment

`deploy/` builds a static Linux binary and provisions an unprivileged Proxmox
LXC on the speakers' VLAN:

```sh
deploy/build.sh
scp dist/* pve:/tmp/
ssh pve 'bash /tmp/install-lxc.sh'      # override CTID, VLAN, IPV4, ...
deploy/update.sh                        # push a rebuilt binary later
```

The installer removes `avahi-daemon` if the template carries it — the daemon
has its own mDNS responder, and two responders on one host is a flake factory.

## How it fits together

```
cmd/sonospoof     the daemon
internal/raop     RTSP, the Apple-Challenge, SDP, RTP + AES, resends
internal/alac     ALAC decoder, byte-identical to Apple's reference
internal/audio    ring buffer and the endless WAV writer
internal/sonos    discovery, topology, transport control
internal/bridge   joins one receiver to one zone
```

**One target per Sonos *group*, not per speaker.** Grouped speakers physically
cannot play different streams — the coordinator drives the group and transport
commands to a member are silently ignored — so a group is collapsed into a
single target rather than offering independence the hardware will not honour.

Each zone's AirPlay identity is the coordinator's own MAC, which its Sonos UUID
embeds (`RINCON_B8E937EFDE14_01400`). Unique, stable across restarts and DHCP,
and independent of whatever NIC the daemon runs on.

## Known hazards

Collected here so they are not rediscovered the hard way. Several of these were
learned expensively.

- **Group coordinators.** Transport commands sent to a non-coordinator member of
  a group are silently ignored. Topology awareness is load-bearing, not polish.
- **Never let the stream run dry — and that means the *rate*, not just the
  connection.** The writer emits one chunk per iteration, so pacing with a
  timeout delivers `chunk/timeout` and is below realtime by construction. A 2s
  timeout on 100ms chunks delivers 5%; the speaker drains its buffer and falls
  silent while still reporting that it is playing. Each chunk is tied to an
  absolute deadline instead.
- **A buffer can only be established at startup.** Exact pacing preserves depth
  but cannot create it: supply equals demand, so a ring that starts empty stays
  empty and every jitter spike becomes a gap. The stream opens with silence
  while the ring fills behind it.
- **Unique URL per session.** Add a nonce, or Sonos caches and refuses to reopen.
- **RTP resends.** Request them the moment a gap appears, not when the reorder
  window expires — a resend only helps if it lands before you skip forward.
- **The reorder window must be shorter than the reserve**, or waiting for a
  resend empties the ring and forces the gap the resend was meant to avoid.
- **Two UDP readers feed one decoder.** SETUP starts a reader for the audio port
  and another for the control port, and the control port carries resend
  responses, which are audio. Anything reachable from the packet path must be
  serialised. Run the tests with `-race`.
- **Volume is a curve, not a rescale.** AirPlay sends −144.0…0.0 dB; Sonos wants
  0–100 linear.
- **Yield the speaker.** Subscribe to Sonos GENA events so that when someone
  takes the zone from the Sonos app, the AirPlay session drops instead of
  fighting over it. Not yet implemented.

## How this differs from AirConnect

[AirConnect](https://github.com/philippe44/AirConnect) solves the same problem
and has solved it for years. It is the honest recommendation if you want music
playing tonight, and this project borrows its hard-won protocol knowledge
freely — the startup silence fill, in particular, is its idea and is what
finally made playback clean here.

Where AirConnect is straightforwardly ahead: it is mature and handles edge
cases this does not; it targets any UPnP renderer plus Chromecast, not just
Sonos; it implements the RTP timing and sync protocol properly, computing each
frame's playtime from the sender's clock rather than pacing to a local
deadline; and it offers output format choices where this only emits WAV.

What this does that AirConnect does not is know it is talking to Sonos:

- **Zone groups.** Grouped Sonos speakers physically cannot play different
  streams — the coordinator drives the group and transport commands to a member
  are silently ignored. AirConnect treats each renderer independently, so it
  will offer you targets that the hardware cannot honour. `sonospoof` reads the
  zone topology and collapses a group into a single target addressed at its
  coordinator.
- **Stable identity from the hardware.** Each target's AirPlay id is the
  coordinator's own MAC, which its Sonos UUID embeds. Unique per zone, stable
  across restarts and DHCP leases, and independent of the host's NIC — which
  matters when one host advertises several targets.
- **A single static binary** with no runtime dependencies, which is convenient
  in a container and is most of why Go was chosen.

If you have Sonos and use grouping, the topology awareness is the reason to
prefer this. Otherwise AirConnect is the more complete program.

`shairport-sync` is the other reference worth reading for the RAOP receiver
end, and Apple's ALAC decoder is Apache 2.0, so `internal/alac` is a mechanical
port of it rather than reverse engineering.
