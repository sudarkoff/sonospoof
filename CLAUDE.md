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

## State: M0 complete (2026-08-29)

Both probes have now been run against real hardware on George's LAN. Every M0
question is answered; see "M0 findings" below. `internal/raop` exists and is
real M1 code with tests — the probes under `cmd/` are still throwaway and
should be deleted when M1 lands, but do not delete `internal/`.

- `cmd/spike-sonos` — SSDP discovery, topology, push a URL to a zone. Now also
  has `-hosts` (skip SSDP, name players directly) and `-src` (pin the egress
  interface). Verified against three Play:1 units.
- `cmd/spike-raop` — `_raop._tcp` advertisement plus RTSP dumper. Now answers
  the Apple-Challenge via `internal/raop`, which is what got us a real
  ANNOUNCE. An iPhone connects and plays to it.
- `internal/raop` — the embedded AirPort Express key, the Apple-Response, and
  session key/IV unwrapping. 7 tests, all against captured real values.

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

## Next action: M1

Single hardcoded zone, WAV out, end to end. The handshake is solved; what
remains is the audio path.

1. Bind the three UDP ports and return the real ones from SETUP. The client's
   `timing_port` and `control_port` come in on the SETUP `Transport:` header.
2. AES-128-CBC decrypt each RTP payload with `raop.SessionKey`/`SessionIV`.
   Note the trailing partial block is left in the clear — do not pad it.
3. ALAC decode against the `a=fmtp:96` config above.
4. Ring buffer, then the WAV writer feeding the Sonos over plain `http://`.
5. Feed digital silence when AirPlay pauses; never let the stream run dry.

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
