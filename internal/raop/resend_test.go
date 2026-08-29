package raop

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// The wire format the sender expects. Getting any field wrong means the
// request is silently ignored and losses stay audible, with nothing logged.
func TestResendRequestWireFormat(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer server.Close()

	client, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer client.Close()

	r := newResendRequester(client, server.LocalAddr().(*net.UDPAddr))
	r.request(1234, 5)

	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, _, err := server.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("no request arrived: %v", err)
	}
	if n != 8 {
		t.Fatalf("request is %d bytes, want 8", n)
	}
	if buf[0] != 0x80 {
		t.Errorf("byte 0 = %#x, want 0x80 (RTP version 2)", buf[0])
	}
	if buf[1] != 0x80|ptResendRequest {
		t.Errorf("byte 1 = %#x, want %#x (marker + type 85)", buf[1], 0x80|ptResendRequest)
	}
	if got := binary.BigEndian.Uint16(buf[4:]); got != 1234 {
		t.Errorf("first missing = %d, want 1234", got)
	}
	if got := binary.BigEndian.Uint16(buf[6:]); got != 5 {
		t.Errorf("count = %d, want 5", got)
	}
	if r.Requested.Load() != 5 {
		t.Errorf("Requested = %d, want 5", r.Requested.Load())
	}
}

// A gap must produce exactly one request, not one per subsequent packet. At
// 125 packets a second the naive version would flood the sender.
func TestResequencerRequestsEachGapOnce(t *testing.T) {
	q := newResequencer()

	type call struct{ first, count uint16 }
	var calls []call
	q.onMissing = func(first, count uint16) {
		calls = append(calls, call{first, count})
	}

	q.push(10, pkt(10)) // establishes next = 11

	// 11 is missing; 12..20 all arrive.
	for i := 12; i <= 20; i++ {
		q.push(uint16(i), pkt(i))
	}

	if len(calls) != 1 {
		t.Fatalf("made %d resend requests for one gap: %v", len(calls), calls)
	}
	if calls[0].first != 11 || calls[0].count != 1 {
		t.Errorf("requested %d packets from %d, want 1 from 11", calls[0].count, calls[0].first)
	}
}

// A resend that arrives in time must fill the hole and be emitted in order.
func TestResequencerAcceptsARecoveredPacket(t *testing.T) {
	q := newResequencer()
	q.onMissing = func(uint16, uint16) {}

	q.push(1, pkt(1))
	q.push(3, pkt(3)) // 2 is missing, held

	got := q.push(2, pkt(2)) // the resend arrives
	if len(got) != 2 || string(got[0]) != "p2" || string(got[1]) != "p3" {
		t.Fatalf("got %v, want [p2 p3]", ids(got))
	}
	if q.Lost != 0 {
		t.Errorf("lost = %d, but the packet was recovered", q.Lost)
	}
}

// A requester with no destination must be inert rather than panic: a sender
// that omits control_port is unusual but not fatal.
func TestResendRequesterTolerAtesNoDestination(t *testing.T) {
	var r *resendRequester
	r.request(1, 1) // nil receiver

	r2 := newResendRequester(nil, nil)
	r2.request(1, 1)
	if r2.Requested.Load() != 0 {
		t.Error("counted a request that could not be sent")
	}
}
