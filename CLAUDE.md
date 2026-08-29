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

## State: end of M0

Two throwaway reconnaissance probes exist under `cmd/`. There is no daemon yet
and no `internal/` package tree — do not assume one.

- `cmd/spike-sonos` — SSDP discovery, zone-group topology, push a URL to a zone.
  Stdlib only. Verified locally only against the no-speakers path.
- `cmd/spike-raop` — fake `_raop._tcp` advertisement plus a full RTSP dumper.
  Verified locally against a synthetic iOS handshake; never yet run against a
  real phone.

Neither probe has been run against real hardware. Both are throwaway; when M1
lands, delete them rather than growing them into the daemon.

## Next action, and what it is blocked on

**M1 cannot start until the probes run on George's LAN.** He is doing this from
the command line. Ask for the results before writing receiver code.

1. `spike-sonos` from the Proxmox LXC. Does it see the Gen1s? That is the
   multicast/bridging question and everything else is moot if it fails. Then
   `-play` with any internet radio URL, with and without `-raw`, to settle
   whether `x-rincon-mp3radio://` beats plain `http://` for an endless stream.
2. `spike-raop -iface <lan>`, then select it from the iPhone's AirPlay menu.
   Three outcomes, pointing three different directions:
   - **Not listed** → mDNS or interface problem, not protocol.
   - **`Apple-Challenge` header on OPTIONS** → legacy RSA. Expected case.
     M1 is then a straight line: leaked AirPort Express key answers the
     challenge, AES-128-CBC decrypt, ALAC decode, ring buffer, WAV out.
   - **`POST /fp-setup`** → FairPlay. Bisect the mDNS TXT records with `-et`
     and `-cn` (and try `vs=` values) until it stops. This is the one outcome
     that meaningfully changes the M1 estimate.

The `ANNOUNCE` SDP from step 2 is the real prize. Its `a=fmtp:96` line is the
ALAC config the decoder gets written against — fields are: frame length,
compatible version, bit depth, pb, mb, kb, channels, maxRun, maxFrameBytes,
avgBitRate, sample rate. Expect `352 0 16 40 10 14 2 255 0 0 44100`.
`a=rsaaeskey` and `a=aesiv` carry the wrapped AES key for the audio stream.

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
- **Unique URL per session** (nonce), or Sonos caches and refuses to reopen.
- **Clock drift.** The phone's 44.1 kHz is not the speaker's. TCP backpressure
  covers most of it; an elastic buffer handles the rest by dropping or
  duplicating a frame when it drifts out of band.
- **RTP retransmits.** RAOP has a resend-request mechanism on the control port.
  Without it every Wi-Fi hiccup is an audible dropout.
- **Volume is a curve, not a rescale.** AirPlay sends −144.0…0.0 dB, Sonos wants
  0–100 linear.
- **Yield the speaker.** Subscribe to Sonos GENA events so taking the zone from
  the Sonos app drops the AirPlay session instead of fighting over it.
- **mDNS TXT feature bits are the most likely thing to be wrong** in the whole
  project. When a device is invisible or refuses to connect, suspect them first.

## Conventions

- `go build ./... && go vet ./... && gofmt -l .` before every commit.
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
