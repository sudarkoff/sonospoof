package raop

import (
	"fmt"
	"testing"
)

func pkt(id int) []byte { return []byte(fmt.Sprintf("p%d", id)) }

func ids(pkts [][]byte) []string {
	out := make([]string, 0, len(pkts))
	for _, p := range pkts {
		out = append(out, string(p))
	}
	return out
}

func TestResequencerPassesOrderedPacketsStraightThrough(t *testing.T) {
	q := newResequencer()
	for i := 0; i < 5; i++ {
		got := q.push(uint16(100+i), pkt(i))
		if len(got) != 1 || string(got[0]) != string(pkt(i)) {
			t.Fatalf("packet %d: got %v", i, ids(got))
		}
	}
	if q.Reordered != 0 || q.Lost != 0 || q.Late != 0 {
		t.Errorf("clean stream reported reordered=%d lost=%d late=%d",
			q.Reordered, q.Lost, q.Late)
	}
}

// The case that makes audio glitch: a packet arrives after the one that
// follows it. Decoding in arrival order shuffles the audio; the buffer must
// hold the early one back and emit both in sequence.
func TestResequencerRepairsSwappedPackets(t *testing.T) {
	q := newResequencer()

	q.push(10, pkt(0))

	// 12 arrives before 11 and must be held.
	if got := q.push(12, pkt(2)); len(got) != 0 {
		t.Fatalf("out-of-order packet was emitted early: %v", ids(got))
	}

	// 11 arrives and unblocks both, in order.
	got := q.push(11, pkt(1))
	if len(got) != 2 || string(got[0]) != "p1" || string(got[1]) != "p2" {
		t.Fatalf("got %v, want [p1 p2]", ids(got))
	}
	if q.Reordered == 0 {
		t.Error("reordering was not counted")
	}
	if q.Lost != 0 {
		t.Errorf("lost = %d, want 0 -- nothing was actually lost", q.Lost)
	}
}

// A packet that never arrives must not stall the stream forever.
func TestResequencerSkipsPastAPermanentGap(t *testing.T) {
	q := newResequencer()
	q.push(0, pkt(0))

	// Everything from 2 onward arrives; 1 never does.
	var emitted int
	for i := 2; i <= seqWindow+3; i++ {
		emitted += len(q.push(uint16(i), pkt(i)))
	}

	if emitted == 0 {
		t.Fatal("stream stalled waiting for a packet that never came")
	}
	if q.Lost == 0 {
		t.Error("the skipped packet was not counted as lost")
	}
	if len(q.pending) > seqWindow {
		t.Errorf("pending grew to %d, beyond the %d window", len(q.pending), seqWindow)
	}
}

// A straggler that arrives after we have moved past it must be dropped, not
// emitted out of order.
func TestResequencerDropsLatePackets(t *testing.T) {
	q := newResequencer()
	q.push(10, pkt(10))
	q.push(11, pkt(11))

	if got := q.push(10, pkt(10)); len(got) != 0 {
		t.Errorf("a duplicate was re-emitted: %v", ids(got))
	}
	if q.Late == 0 {
		t.Error("late packet was not counted")
	}
}

// RTP sequence numbers are 16-bit and wrap. Naive comparison would treat the
// wrap as an enormous backwards jump and stall the stream once every 65536
// packets -- about every nine minutes at AirPlay's rate.
func TestResequencerHandlesSequenceWraparound(t *testing.T) {
	q := newResequencer()

	q.push(65534, pkt(1))
	q.push(65535, pkt(2))

	got := q.push(0, pkt(3))
	if len(got) != 1 || string(got[0]) != "p3" {
		t.Fatalf("wraparound packet not emitted: %v", ids(got))
	}

	got = q.push(1, pkt(4))
	if len(got) != 1 || string(got[0]) != "p4" {
		t.Fatalf("packet after wraparound not emitted: %v", ids(got))
	}
	if q.Lost != 0 {
		t.Errorf("wraparound counted %d lost packets", q.Lost)
	}
}

func TestAheadHandlesWraparound(t *testing.T) {
	cases := []struct {
		a, b uint16
		want bool
	}{
		{5, 3, true},
		{3, 5, false},
		{0, 65535, true},    // just wrapped
		{65535, 0, false},   // the other direction
		{7, 7, true},        // equal counts as at-or-after
		{1000, 60000, true}, // far apart across the wrap
		{60000, 1000, false},
	}
	for _, c := range cases {
		if got := ahead(c.a, c.b); got != c.want {
			t.Errorf("ahead(%d, %d) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestResequencerResetClearsState(t *testing.T) {
	q := newResequencer()
	q.push(100, pkt(0))
	q.push(105, pkt(5)) // leaves something pending
	q.reset()

	if q.started || len(q.pending) != 0 {
		t.Error("reset left state behind")
	}
	if q.Reordered != 0 || q.Lost != 0 || q.Late != 0 {
		t.Error("reset left counters behind")
	}

	// A new session starting at an unrelated sequence must just work.
	if got := q.push(42, pkt(9)); len(got) != 1 {
		t.Errorf("first packet of a new session was not emitted: %v", ids(got))
	}
}
