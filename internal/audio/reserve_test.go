package audio

import (
	"testing"
	"time"
)

// The regression this guards: with no reserve the ring sits at zero depth,
// because the speaker accepts bytes far faster than realtime while its own
// buffer fills. Any upstream stall then starves the stream immediately, which
// is audible as a discrete drop even when no packet was ever lost.
func TestStreamPCMKeepsAReserveBuffered(t *testing.T) {
	r := NewRing(SampleRate * Channels * 2)

	const chunkFrames = 64
	need := chunkFrames*Channels + ReserveFrames*Channels

	// Supply exactly one chunk -- less than chunk+reserve. A reader that does
	// not hold a reserve would consume it instantly.
	r.Write(make([]int16, chunkFrames*Channels))

	done := make(chan struct{})
	w := &countingWriter{}
	go func() {
		_ = StreamPCM(w, r, chunkFrames)
		close(done)
	}()

	// Within the reserve wait it should not have drained the ring to nothing.
	time.Sleep(SilenceAfter / 2)
	if got := r.Len(); got == 0 {
		t.Error("reader drained the ring instead of waiting for a reserve")
	}

	// Now supply enough to clear the reserve and it should proceed.
	r.Write(make([]int16, need))
	time.Sleep(SilenceAfter)
	if w.Writes() == 0 {
		t.Error("reader never wrote once the reserve was available")
	}

	w.Fail()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("StreamPCM did not return after a write error")
	}
}

// The reserve must not be so large it defeats the point: it has to fit inside
// the ring alongside a chunk, or the reader waits for something the ring can
// never hold.
func TestReserveFitsInTheRing(t *testing.T) {
	ringSamples := SampleRate * Channels * 2 // what bridge allocates
	chunk := (SampleRate / 10) * Channels
	if ReserveFrames*Channels+chunk >= ringSamples {
		t.Errorf("reserve %d + chunk %d does not fit in a %d-sample ring",
			ReserveFrames*Channels, chunk, ringSamples)
	}
}
