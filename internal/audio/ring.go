// Package audio buffers decoded PCM and serves it to a Sonos as endless WAV.
package audio

import "sync"

// Ring is a fixed-capacity circular buffer of interleaved int16 samples,
// written by the RTP/decode goroutine and drained by the HTTP goroutine.
//
// Read never blocks and never reports underrun as an error: when the buffer is
// empty it returns digital silence. That is deliberate and load-bearing. If
// bytes stop arriving while AirPlay is paused, Sonos tears the stream down and
// takes seconds to recover, so the stream must keep flowing whether or not
// there is anything to say.
type Ring struct {
	mu   sync.Mutex
	buf  []int16
	r, w int
	n    int // samples currently held

	// underrun and dropped are diagnostics: a stream that is constantly
	// doing either is drifting, and the numbers say which way.
	underrun uint64
	dropped  uint64
}

// NewRing makes a ring holding capacity interleaved samples. For stereo,
// capacity is 2 * frames.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		panic("audio: ring capacity must be positive")
	}
	return &Ring{buf: make([]int16, capacity)}
}

// Write appends samples, discarding the oldest if the buffer is full.
//
// Dropping the oldest rather than blocking is the right trade for live audio:
// blocking would stall the RTP reader and back the loss up into the network,
// and stale audio is worth less than fresh audio. A steadily rising drop count
// means the sender's clock is faster than the speaker's.
func (r *Ring) Write(samples []int16) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for _, s := range samples {
		if r.n == len(r.buf) {
			// Full: advance the read cursor, overwriting the oldest sample.
			r.r = (r.r + 1) % len(r.buf)
			r.n--
			r.dropped++
		}
		r.buf[r.w] = s
		r.w = (r.w + 1) % len(r.buf)
		r.n++
	}
}

// Read fills dst, padding with silence when the buffer runs dry. It always
// fills dst completely and returns how many samples were real audio.
func (r *Ring) Read(dst []int16) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	got := 0
	for i := range dst {
		if r.n == 0 {
			dst[i] = 0
			r.underrun++
			continue
		}
		dst[i] = r.buf[r.r]
		r.r = (r.r + 1) % len(r.buf)
		r.n--
		got++
	}
	return got
}

// Len is the number of samples currently buffered.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Stats returns the cumulative silence-padded and dropped sample counts.
func (r *Ring) Stats() (underrun, dropped uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.underrun, r.dropped
}

// Reset empties the buffer, for when a session ends and the next one must not
// inherit stale audio.
func (r *Ring) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.r, r.w, r.n = 0, 0, 0
}
