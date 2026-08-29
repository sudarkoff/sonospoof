package audio

import (
	"testing"
	"time"
)

// With a steady realtime supply the reader should hold roughly the reserve in
// hand rather than handing everything over the moment it exists.
//
// That buffer is what absorbs an upstream stall -- most often the resequencer
// waiting on a resend -- so that the deadline-paced writer finds audio ready
// when a chunk falls due and does not have to pad.
//
// Note the reader will still drain the ring when a deadline arrives and audio
// is genuinely absent: holding realtime matters more than holding the reserve.
// An earlier version of this test asserted the ring was never drained, which
// encoded the wrong contract.
func TestReserveIsHeldUnderSteadySupply(t *testing.T) {
	r := NewRing(SampleRate * Channels * 2)
	const chunkFrames = SampleRate / 10 // 100ms

	w := &countingWriter{}
	done := make(chan struct{})
	go func() {
		_ = StreamPCM(w, r, chunkFrames)
		close(done)
	}()

	// Feed at realtime in small packets, as the RTP path does.
	stop := make(chan struct{})
	go func() {
		tick := time.NewTicker(8 * time.Millisecond) // ~AirPlay's packet rate
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				r.Write(make([]int16, 352*Channels))
			}
		}
	}()

	// Let it reach steady state, then sample the depth.
	time.Sleep(1500 * time.Millisecond)
	depth := r.Len()
	close(stop)
	w.Fail()

	<-done

	depthMS := float64(depth) / Channels / SampleRate * 1000
	reserveMS := float64(ReserveFrames) / SampleRate * 1000

	if depth == 0 {
		t.Errorf("ring was empty at steady state; no reserve is being held")
	}
	// Generous bound: the point is that a meaningful buffer exists, not that
	// it tracks the target precisely.
	if depthMS < reserveMS/4 {
		t.Errorf("steady-state depth %.0fms is far below the %.0fms reserve",
			depthMS, reserveMS)
	}
	t.Logf("steady-state depth %.0fms against a %.0fms reserve", depthMS, reserveMS)
}

// The reserve has to fit alongside a chunk in the ring, or the reader waits
// for something the ring can never hold.
func TestReserveFitsInTheRing(t *testing.T) {
	ringSamples := SampleRate * Channels * 2 // what bridge allocates
	chunk := (SampleRate / 10) * Channels
	if ReserveFrames*Channels+chunk >= ringSamples {
		t.Errorf("reserve %d + chunk %d does not fit in a %d-sample ring",
			ReserveFrames*Channels, chunk, ringSamples)
	}
}
