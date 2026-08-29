# sonospoof — working notes

AirPlay bridge for first-generation Sonos speakers. Gen1 hardware (Play:1,
Play:3, Play:5 gen1, Connect, ZP80/90/100) will never get AirPlay firmware, but
every Sonos is a UPnP/AV MediaRenderer on port 1400 that plays arbitrary HTTP
URLs. We pretend to be an AirPlay receiver and relay to the speaker over HTTP.

```
iPhone --AirPlay/RAOP--> [ sonospoof ] --HTTP stream--> Sonos
                              |
                              +--SOAP SetAVTransportURI--> Sonos
```

Develop on `claude/airplay-sonos-proxmox-app-yk8clo`. Do not open a PR unless
asked.

## State: M1 works end to end (2026-08-29)

`cmd/sonospoof` advertises every discovered zone as its own AirPlay target and
plays what the phone sends. Verified on the three Play:1 units, with both an
iPhone and a Mac as sender.

```
cmd/sonospoof     the daemon
internal/raop     RTSP, the Apple-Challenge, SDP, RTP + AES
internal/alac     ALAC decoder, byte-identical to Apple's reference
internal/audio    ring buffer and the endless WAV writer
internal/sonos    discovery, topology, transport control
internal/bridge   joins one receiver to one zone
```

Multi-zone was pulled forward from M2. Each zone has its own advertisement,
receiver, ALAC decoder and ring, and nothing is shared between them — the
decoder carries adaptive predictor state, so sharing one would cross the
streams.

**One target per Sonos *group*, not per speaker.** Grouped speakers physically
cannot play different streams: the coordinator drives the group and transport
commands to a member are silently ignored. `sonos.Zones` therefore collapses a
group into a single target rather than offering independence the hardware will
not honour. Zones are keyed by coordinator UUID, never by group ID — those
disagree in practice, e.g. Garage's group is
`RINCON_5CAAFD292DE601400:3104264951`, carrying Austin Bedroom's UUID from some
past grouping.

Each zone's AirPlay identity is the coordinator's own MAC, which the Sonos UUID
embeds (`RINCON_B8E937EFDE14_01400`). That is unique, stable across restarts and
DHCP, and independent of the one NIC the daemon runs on — which matters because
one host now advertises several targets, and macOS was seen rotating its MAC
per VLAN.

The M0 probes are gone — deleted once M1 landed, as planned.

Deployed and running: unprivileged Proxmox LXC 310 (`sonospoof`) on the IoT
VLAN. See `deploy/`. Playback is clean: three minutes with zero gaps, verified
by capturing the served PCM, not by ear alone.

## M0 findings

**The network was the hard part, not the protocol.**

The Sonos live on the **IoT VLAN, 192.168.30.0/24**; George's dev machine was
on Hades, 192.168.20.0/24. Two independent failures came out of that:

1. **SSDP cannot cross a VLAN.** It is link-scoped multicast and UniFi
   reflects mDNS only, so M-SEARCH is never answered — multicast *or* unicast
   form, any `ST`. Unicast HTTP to port 1400 works throughout. Use `-hosts`,
   or put the host on the speakers' VLAN.
2. **IoT → Internal is return-traffic only**, so a speaker cannot open a
   connection back to a stream server on another VLAN. `SetAVTransportURI`
   succeeds and the title shows in the Sonos app, but no SYN ever arrives and
   the transport sits in STOPPED. **This is why the bridge must live on the
   IoT VLAN.** Both `Internal→IoT` and `Users→IoT` are allowed, so the phone
   still reaches it from anywhere but Guest.

**`http://` beats `x-rincon-mp3radio://` for WAV, decisively.** The rincon
scheme flips the speaker into Shoutcast mode (it connects as `Nullsoft Winamp3
(compatible)`), expects MP3 framing, and tears the stream down after ~11s.
Plain `http://` held a single connection for 75s+ still PLAYING. The hunch in
the old notes was backwards: rincon is for endless *MP3*, not endless WAV.

**Auth is the legacy RSA path.** `Apple-Challenge` on OPTIONS, no `/fp-setup`
ever. Sender is `AirPlay/960.13.1`. Note that **answering the challenge is
mandatory, not merely expected**: advertising `et=0` to sidestep it makes iOS
skip the challenge and then refuse to proceed past OPTIONS anyway. There is no
unencrypted shortcut.

**The captured ANNOUNCE**, which the decoder gets written against:

```
a=rtpmap:96 AppleLossless
a=fmtp:96 352 0 16 40 10 14 2 255 0 0 44100
a=min-latency:11025
a=max-latency:88200
```

The ALAC params are exactly as predicted. Post-RECORD the phone sends
`SET_PARAMETER` with `volume: -20.000000`, `progress: <start>/<now>/<end>` in
RTP timestamps, `application/x-dmap-tagged` metadata and `image/jpeg` artwork
— M3 material, but it arrives whether or not we want it yet.

## Next action: M2

1. **React to topology changes.** Grouping is read once at startup, so grouping
   or ungrouping from the Sonos app leaves a target that silently does nothing.
2. **Yield the speaker.** Subscribe to Sonos GENA events so taking a zone from
   the Sonos app drops the AirPlay session instead of fighting over it.
3. **Concurrent stream readers.** Sonos opens three HTTP connections at the
   start of a session and keeps one — consistently the *last*. All three run
   their own reader against the same ring, and `Ring.Read` consumes, so for
   about a second they steal samples from each other. Only a startup artifact,
   but real. The fix is to let one connection consume and hand the others
   silence; not done because it risks a working pipeline on an assumption
   about which connection survives that is only supported by observation.
4. **RTP timing and sync.** We bind the timing port but never read it, and
   discard sync packets. AirConnect computes each frame's playtime from the
   sender's clock and refuses to output until synced. Not needed for clean
   playback, as it turns out — deadline pacing was the actual fix — but it is
   the correct way to do this and would make drift handling principled rather
   than emergent.
5. Config file for zone names and volume mapping.

Unverified: `raop.VolumeToSonos` maps −30…0 dB onto 0–100 with −144 as mute,
which is a judgement call rather than something measured. Group collapse has
unit tests but has not been exercised against a real grouping.

## Decided — do not re-litigate

- **Go.** Non-negotiable, George was explicit. Single static binary for the LXC.
- **AirPlay 1 (RAOP) first**, behind an interface clean enough that AirPlay 2
  can slot in later. AP2 means HomeKit pairing and buffered audio: better sync
  and native multi-room from iOS, but far more work before any sound comes out.
- **WAV on the wire to the speaker for M1.** RIFF header with a bogus giant
  size, streamed forever. No encoder code, lossless, ~1.4 Mbps. FLAC becomes the
  default later; MP3 stays as a fallback for unhappy zones.
- **One AirPlay target per Sonos zone**, each with its own advertisement and
  pipeline.
- **Our own mDNS responder, never Avahi.** Two responders on one host is a flake
  factory.
- **Unprivileged Proxmox LXC bridged to `vmbr0`** with a real LAN IP.
- **2–4 seconds of latency is inherent**, not a bug. AirPlay 1 buffers ~2s by
  design and Sonos adds its own buffer that cannot be disabled. Fine for music,
  useless for video. Do not accept an issue claiming otherwise.

## Milestones after M1

- **M2** — multi-zone auto-discovery, group coordinators, config file, systemd
  unit, `pct` install script.
- **M3** — DAAP metadata and cover art into the Sonos app, RTP retransmits,
  FLAC output, GENA event subscription, small status page.
- **M4** — AirPlay 2 receiver.

## Hazards

Each of these was reasoned out during design; they are not speculative.

- **Group coordinators.** Transport commands sent to a non-coordinator member of
  a group are silently ignored. Topology awareness belongs in M2, not "polish".
- **Never let the stream run dry.** If bytes stop while AirPlay is paused, Sonos
  tears the stream down and takes seconds to recover. Feed digital silence.
- **"Do not run dry" means the delivery *rate*, not just the connection.** The
  writer emits one chunk per iteration, so pacing with a timeout delivers
  chunk/timeout and is below realtime by construction — 250ms on 100ms chunks
  is 40%, 2s is 5%, measured end to end at 21%. Below realtime the speaker
  drains and falls silent while still reporting that it is playing, which
  looks nothing like a buffer problem from the outside. Chunks are tied to an
  absolute deadline for exactly this reason; do not reintroduce a timeout.
- **A buffer can only be established at startup.** Exact pacing preserves the
  ring's depth but cannot create it: once running, supply equals demand, so a
  ring that starts empty stays empty and every jitter spike becomes a gap. The
  reserve was measured at 4ms against a 700ms target and had been decorative
  for a whole debugging session. The stream now opens with silence while the
  ring fills behind it — silence does not consume the ring, so this holds
  realtime from the first byte instead of stalling the speaker at connect.
  AirConnect calls the same thing the http startup silence fill.
- **When the counters are clean and it still sounds wrong, capture the audio.**
  `-dump` writes the served PCM. Starvation appears as runs of exact zeros;
  mis-ordering appears as waveform splices clustered at 352-frame boundaries;
  loud music produces large sample deltas spread evenly across all offsets and
  is not a fault at all. Those are trivially distinguishable in the samples and
  indistinguishable in the statistics. Three rounds of inference were spent
  before doing this, and the capture immediately ruled out decode, ordering and
  loss, leaving pacing.
- **But do not pad silence eagerly either.** This is the sharp edge of the rule
  above. Sonos accepts bytes far faster than 44.1kHz while its own buffer
  fills, so a reader that pads the instant the ring is momentarily empty
  outruns the sender and splices silence between every real chunk — audible as
  continuous glitching rather than as a gap. The deadline resolves both halves:
  we wait for audio right up until the chunk is due, and pad only what is still
  missing then. Never earlier, never later.
- **One session's decoder is single-threaded, and SETUP starts two readers.**
  The audio port and the control port both deliver into `AudioDecoder` — the
  control port is not idle traffic, it carries retransmit responses, which are
  audio. Anything reachable from `Packet` must be serialised. Do not add a
  third reader without checking this.
- **Diagnostics must reset per session.** The underrun counter originally
  spanned the process lifetime, which made a working stream look like it had
  never received a single sample and sent the first investigation to entirely
  the wrong layer. A statistic that silently spans sessions is worse than no
  statistic.
- **Unique URL per session** (nonce), or Sonos caches and refuses to reopen.
- **Clock drift.** The phone's 44.1 kHz is not the speaker's. TCP backpressure
  covers most of it; an elastic buffer handles the rest by dropping or
  duplicating a frame when it drifts out of band.
- **RTP retransmits.** Implemented. Requests go out the moment a gap appears
  rather than at window expiry, because a resend only helps if it lands before
  we skip forward, and once per gap because at 125 packets/sec re-requesting on
  every arrival would flood the sender.
- **"Glitchy audio" had three separate causes**, and each one masked the next.
  Worth knowing because the symptom was identical every time and only the
  counters told them apart: frames decoded in arrival order (fixed by
  resequencing), a data race between the two UDP readers (fixed by a mutex,
  found only by running `-race`), and genuine packet loss (fixed by resend
  requests). The lesson is that "it sounds glitchy" is one symptom with many
  causes, so instrument first — the counters at teardown exist for this and
  each cause showed up as a different line in them.
- **Volume is a curve, not a rescale.** AirPlay sends −144.0…0.0 dB, Sonos wants
  0–100 linear.
- **Yield the speaker.** Subscribe to Sonos GENA events so taking the zone from
  the Sonos app drops the AirPlay session instead of fighting over it.
- **mDNS TXT feature bits are the most likely thing to be wrong** in the whole
  project. When a device is invisible or refuses to connect, suspect them first.
- **iOS connects over IPv6 link-local**, not IPv4 — observed throughout M0
  (`fe80::…%en0`). Two consequences. The Apple-Response signs
  `challenge || localIP || mac`, and over IPv6 that block is 38 bytes and is
  *not* zero-padded (the 32-byte pad is a floor, not a fixed size); using the
  IPv4 form yields a well-formed signature the sender silently rejects. And
  link-local traffic never reaches the router, so inter-VLAN firewall
  reasoning does not govern the phone→bridge path.
- **Take the local IP from the accepted connection**, never from config, for
  the same reason.
- **Do not derive the RAOP id from the NIC MAC.** macOS rotates its private
  Wi-Fi MAC per network, so the advertised instance name changed when the host
  changed VLAN and iOS would see a different device. The LXC is stable, but
  the daemon should use a persistent configured id regardless.
- **Two RSA padding modes, not one.** Apple-Response is PKCS#1 v1.5 with *no*
  hash (`crypto.Hash(0)` — the block is signed directly, not digested).
  `a=rsaaeskey` is OAEP/SHA-1. They are not interchangeable.

## Conventions

- `go build ./... && go vet ./... && gofmt -l . && go test -race ./...` before
  every commit. `-race` is not optional here. Two UDP readers feeding one
  session's decoder raced its scratch buffers for an entire debugging round,
  and it presented as glitchy audio rather than a crash — a concurrent map
  write panics loudly and gets fixed, but raced scratch buffers just emit
  slightly wrong samples and look like a network fault.
- Stdlib where practical. Current only dependency is `github.com/brutella/dnssd`
  for mDNS, chosen over `grandcat/zeroconf` for maintenance and interface
  control. Expect to replace it with our own responder eventually.
- Comments explain why, not what. The protocol constants here are unobvious;
  say where each came from.

## Prior art worth reading, not copying

[AirConnect](https://github.com/philippe44/AirConnect) (its `airupnp` is this
exact architecture) and `shairport-sync` for the RAOP receiver end. We borrow
their protocol knowledge, not their code. Apple's ALAC reference decoder is
Apache 2.0, so porting it is legitimate and mechanical.
