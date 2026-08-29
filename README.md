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

## Expect 2–4 seconds of latency

AirPlay 1 buffers about two seconds by design, and Sonos adds its own buffer on
HTTP streams that cannot be turned off. This is fine for music and useless for
video — lip-sync is not achievable on this architecture. That is a property of
the approach, not a bug to be filed.

## Status

Milestone 0: two throwaway probes that de-risk each half independently. No
daemon yet.

## Probes

Both are reconnaissance tools under `cmd/`, not part of the eventual daemon.

### `spike-sonos` — the Sonos half

Discovers ZonePlayers over SSDP, dumps the zone-group topology (marking each
group's coordinator), and optionally pushes a stream URL to one zone.

```sh
go run ./cmd/spike-sonos
go run ./cmd/spike-sonos -play http://stream.example/radio.mp3 -zone "Kitchen"
go run ./cmd/spike-sonos -play http://stream.example/radio.mp3 -zone "Kitchen" -raw
go run ./cmd/spike-sonos -stop -zone "Kitchen"
```

`-raw` sends a plain `http://` URI instead of `x-rincon-mp3radio://`. The latter
marks the stream endless and non-seekable; comparing the two is the point.

### `spike-raop` — the Apple half

Advertises a fake AirPlay 1 receiver over mDNS and dumps every byte of the RTSP
conversation that follows. It answers just enough to keep a client progressing;
it does not implement the handshake.

```sh
go run ./cmd/spike-raop -name "Kitchen (spoof)" -iface eth0
```

Then pick the device from the AirPlay menu on an iPhone or Mac. What to watch
for in the dump:

- An `Apple-Challenge` header on `OPTIONS` means the client chose **legacy RSA**,
  which is answerable with the long-leaked AirPort Express key. This is the good
  case.
- A `POST /fp-setup` means it chose **FairPlay** instead, a much bigger fight.
  Bisect the mDNS TXT records with `-et` and `-cn` until it stops doing that.
- The `ANNOUNCE` SDP is the prize. Its `a=fmtp:` line carries the ALAC frame
  configuration the real decoder gets written against, and `a=rsaaeskey`/`a=aesiv`
  carry the wrapped AES key for the audio stream.

## Design decisions so far

**Go.** Single static binary, trivial systemd unit, no runtime dependencies
inside a container. The one gap is ALAC decoding; Apple open-sourced the
reference decoder under Apache 2.0, so that is a mechanical port rather than
reverse engineering.

**AirPlay 1 (RAOP) first, with a clean seam for AirPlay 2.** AP1 is documented
and still works against current iOS. AP2 means HomeKit pairing and buffered
audio mode — better sync and native multi-room grouping, but far more protocol
work before any sound comes out. The receiver lives behind an interface so it
can be swapped later.

**WAV on the wire to the speaker, at least at first.** A RIFF header with a
bogus giant size, streamed forever. No encoder code, lossless, no added latency,
about 1.4 Mbps. FLAC comes later as the default (halves the bandwidth, still
lossless); MP3 stays available as a fallback for unhappy zones.

**One AirPlay target per Sonos zone.** Each zone gets its own `_raop._tcp`
advertisement and its own pipeline.

**Our own mDNS responder, no Avahi dependency.** Two responders on one host is a
flake factory.

## Deployment shape

An unprivileged Proxmox LXC bridged onto `vmbr0` with a real LAN IP.

mDNS and SSDP are multicast and do not survive NAT or bridge networking. The
container must be on the same L2 segment as both the speakers and the phone. If
you run this under Docker instead, it must be `network_mode: host` or macvlan —
default bridge networking breaks discovery silently, which is a miserable thing
to debug.

## Known hazards

Collected here so they are not rediscovered the hard way:

- **Group coordinators.** Transport commands sent to a non-coordinator member of
  a group are silently ignored. Topology awareness is load-bearing, not polish.
- **Never let the stream run dry.** If bytes stop while AirPlay is paused, Sonos
  tears the stream down and takes seconds to recover. Feed digital silence.
- **Unique URL per session.** Add a nonce, or Sonos caches and refuses to reopen.
- **Clock drift.** The phone's 44.1 kHz is not the speaker's 44.1 kHz. TCP
  backpressure handles most of it; an elastic buffer covers the rest by dropping
  or duplicating a frame when it drifts out of band.
- **RTP retransmits.** RAOP has a resend-request mechanism on the control port.
  Skip it and every Wi-Fi hiccup becomes an audible dropout.
- **Volume is a curve, not a rescale.** AirPlay sends −144.0…0.0 dB; Sonos wants
  0–100 linear.
- **Yield the speaker.** Subscribe to Sonos GENA events so that when someone
  takes the zone from the Sonos app, the AirPlay session drops instead of
  fighting over it.

## Prior art

[AirConnect](https://github.com/philippe44/AirConnect) already does this and
works today; if you want music in the kitchen tonight, use it. `shairport-sync`
is the reference for the RAOP receiver end. This project is a from-scratch
implementation that borrows their hard-won protocol knowledge.
