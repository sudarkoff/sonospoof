// Package audio buffers decoded PCM and serves it to a Sonos as endless WAV.
package audio

import (
	"sync"
	"time"
)

// Ring is a fixed-capacity circular buffer of interleaved int16 samples,
// written by the RTP/decode goroutine and drained by the HTTP goroutine.
//
// Read waits briefly for audio before padding with silence, and that balance
// is the whole point of this type. Two failure modes pull in opposite
// directions:
//
//   - Never pad, and the stream to the speaker stalls whenever AirPlay
//     pauses. Sonos tears a dry stream down and takes seconds to recover.
//   - Pad immediately, and silence gets spliced into the middle of live
//     audio. The speaker buffers several seconds and will accept bytes far
//     faster than 44.1kHz, so a non-blocking reader races ahead of the sender
//     and injects silence between every real chunk. That sounds like
//     continuous glitching and scratching rather than like a gap.
//
// So Read blocks up to a timeout, which lets normal jitter pass unnoticed and
// only emits silence when the sender has genuinely stopped.
type Ring struct {
	mu   sync.Mutex
	buf  []int16
	r, w int
	n    int

	// wake is signalled on every write so a waiting reader starts promptly
	// instead of polling.
	wake chan struct{}

	underrun uint64
	dropped  uint64
}

// NewRing makes a ring holding capacity interleaved samples. For stereo,
// capacity is 2 * frames.
func NewRing(capacity int) *Ring {
	if capacity <= 0 {
		panic("audio: ring capacity must be positive")
	}
	return &Ring{
		buf:  make([]int16, capacity),
		wake: make(chan struct{}, 1),
	}
}

// Write appends samples, discarding the oldest if the buffer is full.
//
// Dropping the oldest rather than blocking is the right trade for live audio:
// blocking would stall the RTP reader and back the loss up into the network,
// and stale audio is worth less than fresh audio. A steadily rising drop count
// means the sender's clock is running ahead of the speaker's.
func (r *Ring) Write(samples []int16) {
	r.mu.Lock()
	for _, s := range samples {
		if r.n == len(r.buf) {
			r.r = (r.r + 1) % len(r.buf)
			r.n--
			r.dropped++
		}
		r.buf[r.w] = s
		r.w = (r.w + 1) % len(r.buf)
		r.n++
	}
	r.mu.Unlock()

	// Non-blocking: a pending signal is as good as another.
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

// take moves up to len(dst) samples out, returning how many it got.
func (r *Ring) take(dst []int16) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	got := 0
	for got < len(dst) && r.n > 0 {
		dst[got] = r.buf[r.r]
		r.r = (r.r + 1) % len(r.buf)
		r.n--
		got++
	}
	return got
}

// Read fills dst completely, waiting up to wait for real audio before padding
// the remainder with silence. It returns how many samples were real.
//
// wait should be comfortably longer than the sender's packet interval (AirPlay
// sends 352-frame packets, ~125/second) and comfortably shorter than the
// speaker's buffer, so that ordinary jitter never produces a splice.
func (r *Ring) Read(dst []int16, wait time.Duration) int {
	got := r.take(dst)
	if got == len(dst) {
		return got
	}

	// A non-positive wait means "take what is there and pad the rest now",
	// which is what a deadline-paced caller wants once its deadline is spent.
	if wait <= 0 {
		r.mu.Lock()
		r.underrun += uint64(len(dst) - got)
		r.mu.Unlock()
		for i := got; i < len(dst); i++ {
			dst[i] = 0
		}
		return got
	}

	deadline := time.NewTimer(wait)
	defer deadline.Stop()

	for got < len(dst) {
		select {
		case <-r.wake:
			got += r.take(dst[got:])
		case <-deadline.C:
			// The sender really has stopped. Pad the rest so the stream to
			// the speaker keeps flowing.
			r.mu.Lock()
			r.underrun += uint64(len(dst) - got)
			r.mu.Unlock()
			for i := got; i < len(dst); i++ {
				dst[i] = 0
			}
			return got
		}
	}
	return got
}

// WaitFor blocks until at least n samples are buffered or timeout elapses,
// reporting whether the buffer filled.
//
// Used to prime the buffer before telling the speaker to start: beginning
// playback against an empty ring guarantees an immediate underrun, and the
// speaker's own buffering turns that into a stutter at the head of every
// track.
func (r *Ring) WaitFor(n int, timeout time.Duration) bool {
	if r.Len() >= n {
		return true
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	for {
		select {
		case <-r.wake:
			if r.Len() >= n {
				return true
			}
		case <-deadline.C:
			return r.Len() >= n
		}
	}
}

// Len is the number of samples currently buffered.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Stats returns the cumulative silence-padded and dropped sample counts since
// the last Reset.
func (r *Ring) Stats() (underrun, dropped uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.underrun, r.dropped
}

// Reset empties the buffer and clears the counters, so one session's numbers
// do not get read as the next one's.
func (r *Ring) Reset() {
	r.mu.Lock()
	r.r, r.w, r.n = 0, 0, 0
	r.underrun, r.dropped = 0, 0
	r.mu.Unlock()
}
