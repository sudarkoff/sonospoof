package audio

import (
	"bytes"
	"encoding/binary"
	"sync"
	"testing"
	"time"
)

// Long enough that a correct implementation never hits it, short enough that
// a regression fails the suite quickly rather than hanging it.
const testWait = 50 * time.Millisecond

func TestRingRoundTrip(t *testing.T) {
	r := NewRing(8)
	r.Write([]int16{1, 2, 3, 4})

	dst := make([]int16, 4)
	if got := r.Read(dst, testWait); got != 4 {
		t.Errorf("read %d real samples, want 4", got)
	}
	if want := []int16{1, 2, 3, 4}; !equal(dst, want) {
		t.Errorf("got %v, want %v", dst, want)
	}
}

// The regression that produced audible glitching: a reader that pads the
// instant the ring is momentarily empty splices silence into live audio,
// because the speaker accepts bytes far faster than 44.1kHz while its buffer
// fills. Read must wait for audio that is merely late.
func TestRingReadWaitsForLateAudioRatherThanPadding(t *testing.T) {
	r := NewRing(64)

	go func() {
		time.Sleep(10 * time.Millisecond)
		r.Write([]int16{7, 8, 9, 10})
	}()

	dst := make([]int16, 4)
	got := r.Read(dst, time.Second)

	if got != 4 {
		t.Fatalf("got %d real samples, want 4 -- Read padded instead of waiting", got)
	}
	if want := []int16{7, 8, 9, 10}; !equal(dst, want) {
		t.Errorf("got %v, want %v", dst, want)
	}
	if under, _ := r.Stats(); under != 0 {
		t.Errorf("padded %d samples while audio was merely late", under)
	}
}

// A partially-filled read must still wait for the rest rather than padding
// the tail immediately.
func TestRingReadWaitsForTheRemainder(t *testing.T) {
	r := NewRing(64)
	r.Write([]int16{1, 2})

	go func() {
		time.Sleep(10 * time.Millisecond)
		r.Write([]int16{3, 4})
	}()

	dst := make([]int16, 4)
	if got := r.Read(dst, time.Second); got != 4 {
		t.Fatalf("got %d real samples, want 4", got)
	}
	if want := []int16{1, 2, 3, 4}; !equal(dst, want) {
		t.Errorf("got %v, want %v", dst, want)
	}
}

// The other half of the balance: when the sender has genuinely stopped, the
// stream must keep flowing or Sonos tears it down.
func TestRingPadsSilenceOnceTheSenderStops(t *testing.T) {
	r := NewRing(8)
	r.Write([]int16{5, 6})

	dst := make([]int16, 6)
	start := time.Now()
	got := r.Read(dst, testWait)
	elapsed := time.Since(start)

	if got != 2 {
		t.Errorf("real samples = %d, want 2", got)
	}
	if want := []int16{5, 6, 0, 0, 0, 0}; !equal(dst, want) {
		t.Errorf("got %v, want %v", dst, want)
	}
	if elapsed < testWait {
		t.Errorf("padded after %s, should have waited at least %s", elapsed, testWait)
	}
	if under, _ := r.Stats(); under != 4 {
		t.Errorf("underrun count = %d, want 4", under)
	}
}

func TestRingOverflowDropsOldest(t *testing.T) {
	r := NewRing(4)
	r.Write([]int16{1, 2, 3, 4})
	r.Write([]int16{5, 6})

	dst := make([]int16, 4)
	r.Read(dst, testWait)
	if want := []int16{3, 4, 5, 6}; !equal(dst, want) {
		t.Errorf("got %v, want %v", dst, want)
	}
	if _, dropped := r.Stats(); dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
}

// Counters must not leak across sessions, or one session's numbers get read
// as the next one's -- which is exactly how the glitch was first misdiagnosed.
func TestRingResetClearsBufferAndCounters(t *testing.T) {
	r := NewRing(4)
	r.Write([]int16{1, 2, 3})
	dst := make([]int16, 6)
	r.Read(dst, testWait) // forces some padding
	if under, _ := r.Stats(); under == 0 {
		t.Fatal("expected some padding before reset")
	}

	r.Reset()
	if r.Len() != 0 {
		t.Errorf("Len after reset = %d, want 0", r.Len())
	}
	if under, dropped := r.Stats(); under != 0 || dropped != 0 {
		t.Errorf("counters after reset = %d/%d, want 0/0", under, dropped)
	}
}

// Priming the buffer before playback starts avoids an immediate underrun at
// the head of every track.
func TestWaitForPrimesTheBuffer(t *testing.T) {
	r := NewRing(64)
	go func() {
		time.Sleep(10 * time.Millisecond)
		r.Write(make([]int16, 32))
	}()
	if !r.WaitFor(32, time.Second) {
		t.Error("WaitFor timed out although the audio arrived")
	}
}

func TestWaitForGivesUp(t *testing.T) {
	r := NewRing(64)
	if r.WaitFor(32, testWait) {
		t.Error("WaitFor claimed success with an empty ring")
	}
}

func TestWAVHeader(t *testing.T) {
	h := WAVHeader()
	if len(h) != 44 {
		t.Fatalf("header is %d bytes, want 44", len(h))
	}
	if !bytes.Equal(h[0:4], []byte("RIFF")) ||
		!bytes.Equal(h[8:12], []byte("WAVE")) ||
		!bytes.Equal(h[12:16], []byte("fmt ")) ||
		!bytes.Equal(h[36:40], []byte("data")) {
		t.Error("chunk identifiers are wrong")
	}
	if got := binary.LittleEndian.Uint16(h[22:]); got != Channels {
		t.Errorf("channels = %d, want %d", got, Channels)
	}
	if got := binary.LittleEndian.Uint32(h[24:]); got != SampleRate {
		t.Errorf("sample rate = %d, want %d", got, SampleRate)
	}
	if got := binary.LittleEndian.Uint32(h[28:]); got != BytesPerSecond {
		t.Errorf("byte rate = %d, want %d", got, BytesPerSecond)
	}
	if got := binary.LittleEndian.Uint16(h[32:]); got != Channels*BitDepth/8 {
		t.Errorf("block align = %d, want %d", got, Channels*BitDepth/8)
	}
	if got := binary.LittleEndian.Uint16(h[34:]); got != BitDepth {
		t.Errorf("bit depth = %d, want %d", got, BitDepth)
	}
	riff := binary.LittleEndian.Uint32(h[4:])
	data := binary.LittleEndian.Uint32(h[40:])
	if riff-36 != data {
		t.Errorf("RIFF size %d and data size %d are inconsistent", riff, data)
	}
}

func TestBytesPerSecondIsCDRate(t *testing.T) {
	if BytesPerSecond != 176400 {
		t.Errorf("BytesPerSecond = %d, want 176400", BytesPerSecond)
	}
}

func TestStreamPCMStopsOnWriteError(t *testing.T) {
	r := NewRing(64)
	r.Write(make([]int16, 512))

	w := &shortWriter{limit: 3}
	if err := StreamPCM(w, r, 2); err == nil {
		t.Fatal("expected StreamPCM to return the writer's error")
	}
	if w.n == 0 {
		t.Error("nothing was written before the error")
	}
}

// countingWriter accepts writes until Fail is called, then errors so a
// StreamPCM goroutine can be unwound.
type countingWriter struct {
	mu     sync.Mutex
	writes int
	failed bool
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed {
		return 0, errClosed
	}
	c.writes++
	return len(p), nil
}

func (c *countingWriter) Writes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes
}

func (c *countingWriter) Fail() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failed = true
}

type shortWriter struct {
	n     int
	limit int
}

func (s *shortWriter) Write(p []byte) (int, error) {
	s.n++
	if s.n >= s.limit {
		return 0, errClosed
	}
	return len(p), nil
}

var errClosed = &closedErr{}

type closedErr struct{}

func (*closedErr) Error() string { return "closed" }

func equal(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
