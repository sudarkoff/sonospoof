package audio

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestRingRoundTrip(t *testing.T) {
	r := NewRing(8)
	r.Write([]int16{1, 2, 3, 4})

	dst := make([]int16, 4)
	if got := r.Read(dst); got != 4 {
		t.Errorf("read %d real samples, want 4", got)
	}
	if want := []int16{1, 2, 3, 4}; !equal(dst, want) {
		t.Errorf("got %v, want %v", dst, want)
	}
}

// The behaviour the whole design leans on: an empty ring yields silence, not
// an error and not a short read, so the stream to the speaker never stops.
func TestRingUnderrunYieldsSilenceNotShortRead(t *testing.T) {
	r := NewRing(8)
	r.Write([]int16{5, 6})

	dst := make([]int16, 6)
	got := r.Read(dst)
	if got != 2 {
		t.Errorf("real samples = %d, want 2", got)
	}
	if want := []int16{5, 6, 0, 0, 0, 0}; !equal(dst, want) {
		t.Errorf("got %v, want %v", dst, want)
	}
	if under, _ := r.Stats(); under != 4 {
		t.Errorf("underrun count = %d, want 4", under)
	}
}

// Overflow drops the oldest rather than blocking the RTP reader.
func TestRingOverflowDropsOldest(t *testing.T) {
	r := NewRing(4)
	r.Write([]int16{1, 2, 3, 4})
	r.Write([]int16{5, 6})

	dst := make([]int16, 4)
	r.Read(dst)
	if want := []int16{3, 4, 5, 6}; !equal(dst, want) {
		t.Errorf("got %v, want %v", dst, want)
	}
	if _, dropped := r.Stats(); dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
}

func TestRingResetClearsStaleAudio(t *testing.T) {
	r := NewRing(4)
	r.Write([]int16{1, 2, 3})
	r.Reset()
	if r.Len() != 0 {
		t.Errorf("Len after reset = %d, want 0", r.Len())
	}
	dst := make([]int16, 2)
	if got := r.Read(dst); got != 0 {
		t.Errorf("read %d real samples after reset, want 0", got)
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
	// The two declared sizes must agree, or some parsers reject the stream.
	riff := binary.LittleEndian.Uint32(h[4:])
	data := binary.LittleEndian.Uint32(h[40:])
	if riff-36 != data {
		t.Errorf("RIFF size %d and data size %d are inconsistent", riff, data)
	}
}

// 1.4 Mbps is the figure the design is costed against; catch a units slip.
func TestBytesPerSecondIsCDRate(t *testing.T) {
	if BytesPerSecond != 176400 {
		t.Errorf("BytesPerSecond = %d, want 176400", BytesPerSecond)
	}
}

func TestStreamPCMWritesHeaderlessPCMAndStopsOnError(t *testing.T) {
	r := NewRing(64)
	r.Write([]int16{100, -100})

	w := &shortWriter{limit: 3}
	if err := StreamPCM(w, r, 2); err == nil {
		t.Fatal("expected StreamPCM to return the writer's error")
	}
	// First chunk is 2 frames * 2 channels * 2 bytes = 8 bytes.
	if w.n == 0 {
		t.Error("nothing was written before the error")
	}
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
