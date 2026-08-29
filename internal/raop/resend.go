package raop

import (
	"encoding/binary"
	"net"
	"sync/atomic"
)

// RAOP's resend request, sent by the receiver on the control port.
//
// Layout, 8 bytes big-endian:
//
//	0      0x80          RTP version 2
//	1      0x80|0x55     marker set, payload type 85 (resend request)
//	2..3   our own sequence for the request itself
//	4..5   first missing audio sequence number
//	6..7   how many consecutive packets are missing
//
// The sender replies on the same control port with payload type 86, which
// wraps the original audio packet after a four-byte header -- already handled
// on the receive side.
const ptResendRequest = 85

type resendRequester struct {
	conn *net.UDPConn
	dst  *net.UDPAddr
	seq  atomic.Uint32

	Requested atomic.Uint64
}

func newResendRequester(conn *net.UDPConn, dst *net.UDPAddr) *resendRequester {
	return &resendRequester{conn: conn, dst: dst}
}

// request asks the sender to resend count packets starting at first.
func (r *resendRequester) request(first, count uint16) {
	if r == nil || r.conn == nil || r.dst == nil {
		return
	}
	var pkt [8]byte
	pkt[0] = 0x80
	pkt[1] = 0x80 | ptResendRequest
	binary.BigEndian.PutUint16(pkt[2:], uint16(r.seq.Add(1)))
	binary.BigEndian.PutUint16(pkt[4:], first)
	binary.BigEndian.PutUint16(pkt[6:], count)

	// Best effort by design: this is a request for data that was already lost
	// once, so a failure to send it is not worth failing the stream over.
	if _, err := r.conn.WriteToUDP(pkt[:], r.dst); err == nil {
		r.Requested.Add(uint64(count))
	}
}
