package raop

// Resequencing for RTP audio.
//
// Packets arrive over UDP, so they are reordered and lost as a matter of
// course on Wi-Fi. Decoding them in arrival order plays audio with the frames
// shuffled, which sounds like continuous glitching rather than like a dropout,
// and no amount of buffering downstream can repair it -- by then the samples
// are already in the wrong order.
//
// Reordering alone only fixes packets that arrive late but do arrive. For
// packets that have genuinely gone missing, a resend is requested on the
// control port as soon as the gap appears -- see resend.go. Anything still
// missing when the window expires is skipped, because waiting longer would
// starve the stream for something that is never coming.

// seqWindow is how many packets may be held waiting for a missing one.
//
// Each packet is 352 frames, ~8ms, so 32 packets is ~256ms of slack: far more
// reordering than a healthy network produces, and still comfortably inside the
// speaker's buffer. Beyond this we conclude the gap is a real loss rather than
// a late arrival and skip forward, because continuing to wait would starve the
// stream for something that is never coming.
const seqWindow = 32

type resequencer struct {
	next    uint16
	started bool
	pending map[uint16][]byte

	// onMissing, when set, is called as soon as a gap appears, so the caller
	// can ask the sender to resend before the window runs out. Requesting
	// immediately rather than at window expiry is what makes recovery
	// possible at all: the packet has to arrive back inside the window to be
	// any use.
	onMissing func(first, count uint16)

	// requested remembers the last gap we asked about, so a burst of
	// subsequent packets does not re-request the same range on every arrival.
	requested     uint16
	haveRequested bool

	// Diagnostics: reordered counts packets that arrived out of order and
	// were successfully put back; lost counts positions skipped because the
	// packet never turned up; late counts packets that arrived after we had
	// already moved past them.
	Reordered uint64
	Lost      uint64
	Late      uint64
}

func newResequencer() *resequencer {
	return &resequencer{pending: make(map[uint16][]byte, seqWindow*2)}
}

// ahead reports whether a is at or after b in RTP sequence space, which wraps
// at 16 bits. Comparing the raw uint16s would treat a wrap as a huge backwards
// jump and stall the stream once every 65536 packets -- about every nine
// minutes at AirPlay's rate.
func ahead(a, b uint16) bool {
	return int16(a-b) >= 0
}

// push accepts one packet and returns those now ready to decode, in order.
//
// The caller must not reuse the slice it passes: packets can be held here
// across calls.
func (q *resequencer) push(seq uint16, pkt []byte) [][]byte {
	if !q.started {
		q.started = true
		q.next = seq
	}

	if !ahead(seq, q.next) {
		// Already past this position: a duplicate, or a straggler that lost
		// its race. Either way it is too late to use.
		q.Late++
		return nil
	}

	if seq != q.next {
		q.Reordered++
		// A gap has opened. Ask for the missing range now rather than when
		// the window expires: a resend is only useful if it arrives before we
		// give up and skip forward.
		q.requestMissing(seq)
	}
	q.pending[seq] = pkt

	ready := q.drain()

	// Still blocked past the window: give up on the missing packet.
	if len(q.pending) > seqWindow {
		q.skipToNextPending()
		ready = append(ready, q.drain()...)
	}
	return ready
}

// requestMissing asks for everything between next and seq, once per gap.
//
// Re-requesting on every subsequent arrival would flood the sender: at 125
// packets a second a single loss would otherwise produce a request per packet
// until the window expired.
func (q *resequencer) requestMissing(seq uint16) {
	if q.onMissing == nil {
		return
	}
	if q.haveRequested && q.requested == q.next {
		return // already asked about this gap
	}
	count := seq - q.next
	if count == 0 || count > seqWindow {
		return
	}
	q.requested, q.haveRequested = q.next, true
	q.onMissing(q.next, count)
}

// drain removes consecutively-numbered packets starting at next.
func (q *resequencer) drain() [][]byte {
	var ready [][]byte
	for {
		pkt, ok := q.pending[q.next]
		if !ok {
			return ready
		}
		delete(q.pending, q.next)
		q.next++
		ready = append(ready, pkt)
	}
}

// skipToNextPending advances past a hole to the earliest packet we hold.
func (q *resequencer) skipToNextPending() {
	if len(q.pending) == 0 {
		return
	}
	// Find the held packet closest ahead of next.
	var best uint16
	first := true
	for seq := range q.pending {
		if !ahead(seq, q.next) {
			continue
		}
		if first || !ahead(seq, best) {
			best, first = seq, false
		}
	}
	if first {
		return
	}
	q.Lost += uint64(best - q.next)
	q.next = best
}

// reset clears state between sessions, so a new stream's sequence numbers are
// not compared against the previous one's.
func (q *resequencer) reset() {
	q.started = false
	q.next = 0
	q.pending = make(map[uint16][]byte, seqWindow*2)
	q.Reordered, q.Lost, q.Late = 0, 0, 0
}
