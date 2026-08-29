package raop

import (
	"testing"
	"time"

	"github.com/sudarkoff/sonospoof/internal/audio"
)

// The reorder window and the output reserve are coupled, and getting the
// relationship backwards is what produced audible drops once already.
//
// Holding a packet to wait for a resend means producing no audio meanwhile.
// If the window is longer than the reserve, the reserve empties before the
// wait is over and the stream starves precisely when it is trying to avoid a
// gap -- trading a short dropout for a longer one.
func TestReorderWindowFitsReserve(t *testing.T) {
	// Each packet carries 352 frames at 44.1kHz.
	const framesPerPacket = 352
	window := time.Duration(seqWindow) * framesPerPacket * time.Second / audio.SampleRate
	reserve := time.Duration(audio.ReserveFrames) * time.Second / audio.SampleRate

	if window >= reserve {
		t.Errorf("reorder window %s is not shorter than the reserve %s; "+
			"waiting for a resend would starve the stream", window, reserve)
	}

	// And the reserve must not be so large it cannot fit in the ring.
	ringFrames := audio.SampleRate * 2
	if audio.ReserveFrames >= ringFrames {
		t.Errorf("reserve %d frames does not fit a %d-frame ring",
			audio.ReserveFrames, ringFrames)
	}

	t.Logf("reorder window %s, reserve %s", window, reserve)
}
