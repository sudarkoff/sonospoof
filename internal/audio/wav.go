package audio

import (
	"encoding/binary"
	"io"
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

// StreamPCM copies from the ring to w until w errors, which is how a Sonos
// disconnect surfaces.
//
// Pacing is left entirely to TCP backpressure rather than a ticker. The
// speaker buffers a few seconds and then stops reading, which blocks these
// writes at exactly the speaker's own clock rate -- no drift to accumulate and
// no timer to get wrong. A ticker would have to guess the rate and would
// slowly diverge from whatever the hardware is actually doing.
//
// chunk is in sample frames; 4410 is 100ms.
func StreamPCM(w io.Writer, r *Ring, chunkFrames int) error {
	if chunkFrames <= 0 {
		chunkFrames = SampleRate / 10
	}
	samples := make([]int16, chunkFrames*Channels)
	raw := make([]byte, len(samples)*2)

	for {
		r.Read(samples) // always fills; pads silence on underrun
		for i, s := range samples {
			binary.LittleEndian.PutUint16(raw[i*2:], uint16(s))
		}
		if _, err := w.Write(raw); err != nil {
			return err
		}
	}
}
