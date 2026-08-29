package audio

import (
	"encoding/binary"
	"io"
	"time"
)

const (
	SampleRate = 44100
	Channels   = 2
	BitDepth   = 16

	// BytesPerSecond is the wire rate of the stream, ~1.4 Mbps.
	BytesPerSecond = SampleRate * Channels * BitDepth / 8
)

// endlessSize is the size claimed in the RIFF header. The stream never ends,
// so no honest length exists; Sonos does not validate it for a streamed body.
// Using the 32-bit maximum rounded down keeps the two length fields
// consistent with each other.
const endlessSize = 0xFFFFFFF0

// WAVHeader returns the 44-byte RIFF/WAVE header for 44.1kHz 16-bit stereo,
// declaring a near-infinite payload.
func WAVHeader() []byte {
	h := make([]byte, 44)

	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], endlessSize)
	copy(h[8:], "WAVE")

	copy(h[12:], "fmt ")
	binary.LittleEndian.PutUint32(h[16:], 16) // PCM chunk size
	binary.LittleEndian.PutUint16(h[20:], 1)  // format: PCM
	binary.LittleEndian.PutUint16(h[22:], Channels)
	binary.LittleEndian.PutUint32(h[24:], SampleRate)
	binary.LittleEndian.PutUint32(h[28:], BytesPerSecond)
	binary.LittleEndian.PutUint16(h[32:], Channels*BitDepth/8) // block align
	binary.LittleEndian.PutUint16(h[34:], BitDepth)

	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], endlessSize-36)

	return h
}

// SilenceAfter is how long StreamPCM waits for audio before deciding the
// sender has stopped and padding with silence.
//
// AirPlay sends about 125 packets a second, so any value well above 8ms
// absorbs ordinary jitter. It must also stay well under the speaker's own
// buffer so that a genuine pause is covered before the stream runs dry.
const SilenceAfter = 250 * time.Millisecond

// ReserveFrames is how much audio to keep buffered rather than hand straight
// to the speaker.
//
// Without a reserve the ring sits at zero depth: Sonos accepts bytes far
// faster than 44.1kHz until its own buffer fills, so everything produced is
// given away immediately. Any upstream stall then starves the stream at once
// -- and there is a routine one, because the resequencer holds packets while
// it waits for a resend to arrive. That produced a handful of discrete drops
// per minute even with zero packets ultimately lost.
//
// 300ms comfortably covers a resend round trip on a LAN while adding little
// to the two to four seconds of latency AirPlay and Sonos already impose
// between them.
const ReserveFrames = SampleRate * 3 / 10

// StreamPCM copies from the ring to w until w errors, which is how a Sonos
// disconnect surfaces.
//
// Rate is set by the reader waiting on the ring, not by a ticker and not by
// TCP backpressure alone. Backpressure looks like it should pace this -- the
// speaker stops reading when its buffer is full -- but that buffer is several
// seconds deep, so at the start of a session the speaker will swallow
// everything offered as fast as it is produced. A reader that never waits
// therefore outruns the sender's 44.1kHz and splices silence between every
// real chunk, which is audible as continuous glitching rather than as a gap.
//
// chunk is in sample frames; 4410 is 100ms.
func StreamPCM(w io.Writer, r *Ring, chunkFrames int) error {
	if chunkFrames <= 0 {
		chunkFrames = SampleRate / 10
	}
	samples := make([]int16, chunkFrames*Channels)
	raw := make([]byte, len(samples)*2)

	reserve := ReserveFrames * Channels

	for {
		// Hold a reserve back rather than handing the speaker everything the
		// moment it is available. Waiting for chunk+reserve keeps that much
		// audio buffered at all times, which is what absorbs an upstream
		// stall -- most often the resequencer holding packets while a resend
		// is in flight. On a timeout we fall through and Read pads, so a
		// genuinely stopped sender still cannot dry the stream out.
		r.WaitFor(len(samples)+reserve, SilenceAfter)

		r.Read(samples, SilenceAfter)
		for i, s := range samples {
			binary.LittleEndian.PutUint16(raw[i*2:], uint16(s))
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}
	}
}
