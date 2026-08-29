package raop

import (
	"testing"
	"time"

	"github.com/sudarkoff/sonospoof/internal/audio"
)

// The reorder window and the output reserve are coupled.
//
// Holding a packet to wait for a resend means producing no audio meanwhile.
// The reader is paced to a deadline and will pad whatever is missing when a
// chunk falls due, so if the window outlasts the reserve, waiting for a resend
// empties the ring and forces the very gap the resend was meant to avoid.
func TestReorderWindowFitsReserve(t *testing.T) {
	const framesPerPacket = 352
	window := time.Duration(seqWindow) * framesPerPacket * time.Second / audio.SampleRate
	reserve := time.Duration(audio.ReserveFrames) * time.Second / audio.SampleRate

	if window >= reserve {
		t.Errorf("reorder window %s is not shorter than the reserve %s; "+
			"waiting for a resend would empty the ring and force a pad",
			window, reserve)
	}

	ringFrames := audio.SampleRate * 2
	if audio.ReserveFrames >= ringFrames {
		t.Errorf("reserve %d frames does not fit a %d-frame ring",
			audio.ReserveFrames, ringFrames)
	}

	t.Logf("reorder window %s, reserve %s", window, reserve)
}
