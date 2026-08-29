package audio

import (
	"sync"
	"testing"
	"time"
)

// The regression this exists to prevent: delivering below realtime drains the
// speaker's buffer until it falls silent while still reporting that it plays.
//
// The loop emits one chunk per iteration, so a design that waits T to emit C
// delivers C/T. A 250ms timeout on 100ms chunks is 40% of realtime; 2s is 5%.
// Both were measured in the field, the latter at 21% end to end. Pacing to an
// absolute deadline instead makes the rate exact regardless of what arrives.
func TestStreamPCMHoldsRealtimeWhileStarved(t *testing.T) {
	// A ring that never receives audio: the worst case.
	r := NewRing(SampleRate * Channels)

	const chunkFrames = SampleRate / 10 // 100ms
	w := &timedWriter{}

	go func() { _ = StreamPCM(w, r, chunkFrames) }()

	const observe = 1200 * time.Millisecond
	time.Sleep(observe)
	w.Fail()

	bytes := w.Bytes()
	// Realtime for the observation window, allowing for scheduling slop and
	// the fact we start mid-chunk.
	want := int(float64(BytesPerSecond) * observe.Seconds() * 0.75)

	if bytes < want {
		t.Errorf("starved stream delivered %d bytes in %s, want at least %d "+
			"(%.0f%% of realtime); the speaker would drain and fall silent",
			bytes, observe, want, 100*float64(bytes)/(float64(BytesPerSecond)*observe.Seconds()))
	}
	t.Logf("starved delivery: %.0f%% of realtime",
		100*float64(bytes)/(float64(BytesPerSecond)*observe.Seconds()))
}

// And it must not run *faster* than realtime when starved either, or it would
// spew silence at the speaker as fast as TCP allows.
func TestStreamPCMDoesNotOutrunRealtimeWhileStarved(t *testing.T) {
	r := NewRing(SampleRate * Channels)
	w := &timedWriter{}
	go func() { _ = StreamPCM(w, r, SampleRate/10) }()

	const observe = 1200 * time.Millisecond
	time.Sleep(observe)
	w.Fail()

	ratio := float64(w.Bytes()) / (float64(BytesPerSecond) * observe.Seconds())
	if ratio > 2.0 {
		t.Errorf("starved stream delivered %.1fx realtime; it is not pacing", ratio)
	}
}

type timedWriter struct {
	mu     sync.Mutex
	n      int
	failed bool
}

func (t *timedWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failed {
		return 0, errClosed
	}
	t.n += len(p)
	return len(p), nil
}

func (t *timedWriter) Bytes() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.n
}

func (t *timedWriter) Fail() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failed = true
}
