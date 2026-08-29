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

// There is deliberately no "how long to wait before padding" constant here.
// StreamPCM used to have one and it was the wrong shape: the loop emits one
// chunk per iteration, so waiting T to emit C delivers C/T, and any T above
// the chunk duration is below realtime by construction. 250ms on 100ms chunks
// gives 40%; 2s gives 5%. Both drain the speaker until it goes silent while
// still reporting that it is playing. Each chunk is now tied to a deadline
// instead, which makes the rate exact whatever the network does.

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
// Since padding is what holds the delivery rate when audio is late, the way
// to pad rarely is to keep audio in hand. This is that.
//
// It must exceed the resequencer's reorder window, so that waiting for a
// resend does not empty the ring and force a pad. 700ms covers the 512ms
// window with margin, and is small against the two to four seconds AirPlay
// and Sonos already impose between them. Measured at one pad in 7m50s.
const ReserveFrames = SampleRate * 7 / 10

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
	chunk := time.Duration(chunkFrames) * time.Second / SampleRate

	// Pace against an absolute clock rather than a per-iteration timeout.
	//
	// A timeout cannot hold realtime: the loop emits one chunk per iteration,
	// so waiting T to emit C delivers C/T, and any T above the chunk duration
	// is below realtime by construction. A 250ms timeout on 100ms chunks
	// delivers 40% when starved; 2s delivers 5%. Both drain the speaker's
	// buffer until it falls silent while still reporting that it is playing.
	//
	// Tying each chunk to a deadline instead makes the rate exact: we wait for
	// audio only until the chunk is due, then send whatever we have with the
	// remainder padded. Late audio still gets every millisecond it can have,
	// and the stream never falls behind realtime whatever the network does.
	next := time.Now()

	// Open with silence while the ring fills, rather than waiting in place.
	//
	// Exact pacing preserves whatever depth the ring has but cannot create it:
	// once running, supply equals demand, so a ring that starts empty stays
	// empty and every jitter spike becomes a pad. Startup is the only moment
	// the buffer can be established. Simply waiting would work but stalls the
	// speaker at connect; sending silence instead keeps the stream at realtime
	// from the first byte while the ring builds behind it, because silence
	// does not consume the ring. AirConnect does the same thing, and calls it
	// the http startup silence fill.
	//
	// The cost is inaudible: it is under a second, against the two to four
	// seconds AirPlay and Sonos already impose between them.
	silence := make([]byte, len(raw))
	for filled := 0; filled < ReserveFrames && r.Len() < reserve; filled += chunkFrames {
		next = next.Add(chunk)
		if d := time.Until(next); d > 0 {
			time.Sleep(d)
		}
		if _, err := w.Write(silence); err != nil {
			return err
		}
	}

	for {
		next = next.Add(chunk)

		// Hold a reserve back rather than handing the speaker everything the
		// moment it exists, so an upstream stall -- most often the
		// resequencer waiting on a resend -- does not force a pad. But never
		// wait past this chunk's deadline.
		if wait := time.Until(next); wait > 0 {
			r.WaitFor(len(samples)+reserve, wait)
		}

		// Take whatever is there now, padding the rest. Any remaining wait is
		// already spent, so this must not block.
		r.Read(samples, 0)

		for i, s := range samples {
			binary.LittleEndian.PutUint16(raw[i*2:], uint16(s))
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}

		// If writes fell behind -- TCP backpressure, a slow speaker -- do not
		// try to make the time up in a burst the speaker did not ask for.
		if late := time.Since(next); late > chunk {
			next = time.Now()
		}
	}
}
