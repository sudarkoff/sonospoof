package alac

import (
	"encoding/binary"
	"os"
	"testing"
)

// airplayConfig is the config AirPlay 1 negotiates, taken verbatim from the
// SDP an iPhone sent us: a=fmtp:96 352 0 16 40 10 14 2 255 0 0 44100
func airplayConfig() Config {
	return Config{
		FrameLength:       352,
		CompatibleVersion: 0,
		BitDepth:          16,
		PB:                40,
		MB:                10,
		KB:                14,
		NumChannels:       2,
		MaxRun:            255,
		MaxFrameBytes:     0,
		AvgBitRate:        0,
		SampleRate:        44100,
	}
}

// TestDecodeMatchesReference is the test that actually establishes the port is
// correct. testdata was produced by Apple's own encoder and decoder (see
// testdata/generate_vectors.cpp) at exactly AirPlay's parameters; this decodes
// the same frames and demands byte-identical PCM.
//
// "It sounds fine" is not a substitute: a subtly wrong Rice decoder still
// produces numbers, and the error only shows on unusual input.
func TestDecodeMatchesReference(t *testing.T) {
	frames, err := os.ReadFile("testdata/vectors.frames")
	if err != nil {
		t.Fatalf("reading frames: %v", err)
	}
	wantPCM, err := os.ReadFile("testdata/vectors.pcm")
	if err != nil {
		t.Fatalf("reading reference pcm: %v", err)
	}

	cfg := airplayConfig()
	d := NewDecoder(cfg)

	const perFrame = 352 * 2 // int16 samples per frame, stereo
	out := make([]int16, perFrame)

	off, pcmOff, frameNo := 0, 0, 0
	for off < len(frames) {
		if off+4 > len(frames) {
			t.Fatalf("frame %d: truncated length prefix", frameNo)
		}
		n := int(binary.LittleEndian.Uint32(frames[off:]))
		off += 4
		if off+n > len(frames) {
			t.Fatalf("frame %d: declares %d bytes, only %d left", frameNo, n, len(frames)-off)
		}
		packet := frames[off : off+n]
		off += n

		got, err := d.Decode(packet, out)
		if err != nil {
			t.Fatalf("frame %d: Decode: %v", frameNo, err)
		}
		if got != 352 {
			t.Fatalf("frame %d: decoded %d samples, want 352", frameNo, got)
		}

		for i := 0; i < perFrame; i++ {
			want := int16(binary.LittleEndian.Uint16(wantPCM[pcmOff+i*2:]))
			if out[i] != want {
				t.Fatalf("frame %d, sample %d (ch %d): got %d, want %d",
					frameNo, i/2, i%2, out[i], want)
			}
		}
		pcmOff += perFrame * 2
		frameNo++
	}

	if frameNo == 0 {
		t.Fatal("no frames decoded -- testdata missing?")
	}
	if pcmOff != len(wantPCM) {
		t.Errorf("consumed %d bytes of reference PCM, file has %d", pcmOff, len(wantPCM))
	}
	t.Logf("decoded %d frames identically to Apple's reference decoder", frameNo)
}

// A truncated frame must be reported, not silently decoded as zeros.
func TestDecodeRejectsTruncatedFrame(t *testing.T) {
	frames, err := os.ReadFile("testdata/vectors.frames")
	if err != nil {
		t.Fatalf("reading frames: %v", err)
	}
	n := int(binary.LittleEndian.Uint32(frames[0:]))
	packet := frames[4 : 4+n]

	d := NewDecoder(airplayConfig())
	out := make([]int16, 352*2)

	if _, err := d.Decode(packet[:len(packet)/2], out); err == nil {
		t.Error("expected an error decoding a half-length frame")
	}
}
