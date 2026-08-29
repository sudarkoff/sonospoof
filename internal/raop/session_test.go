package raop

import (
	"net"
	"testing"

	"github.com/sudarkoff/sonospoof/internal/audio"
)

// pipeConn gives two net.Conn values that are distinct but need no listener.
func pipeConn(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

// Senders open several RTSP connections -- an iPhone opened four during M0.
// Only the one that sent ANNOUNCE may end the session; a stale connection
// closing must not take down a live stream's decoder and UDP sockets.
func TestOnlyTheOwningConnectionTearsDownTheSession(t *testing.T) {
	r := &Receiver{
		Name: "Test",
		Ring: audio.NewRing(1024),
		Logf: func(string, ...any) {},
	}

	owner, _ := pipeConn(t)
	other, _ := pipeConn(t)

	sdp := []byte("a=fmtp:96 352 0 16 40 10 14 2 255 0 0 44100\r\n" +
		"a=rsaaeskey:" + capturedAnnounces[0].rsaAESKey + "\r\n" +
		"a=aesiv:" + capturedAnnounces[0].aesIV + "\r\n")

	if err := r.announce(owner, sdp); err != nil {
		t.Fatalf("announce: %v", err)
	}
	if r.dec == nil {
		t.Fatal("announce did not build a decoder")
	}

	// A different connection closing must leave the session intact.
	r.closeIfOwner(other)
	if r.dec == nil {
		t.Error("a non-owning connection tore down the live session")
	}

	// The owner closing must end it.
	r.closeIfOwner(owner)
	if r.dec != nil {
		t.Error("the owning connection did not tear down the session")
	}
}

// With no ANNOUNCE anywhere there is nothing to protect, so a closing
// connection should still run the teardown path rather than leak sockets.
func TestCloseWithNoOwnerStillTearsDown(t *testing.T) {
	r := &Receiver{
		Name: "Test",
		Ring: audio.NewRing(1024),
		Logf: func(string, ...any) {},
	}
	conn, _ := pipeConn(t)
	r.closeIfOwner(conn) // must not panic
}

// Ownership must not survive a teardown, or the next session's ANNOUNCE would
// arrive while a stale owner is still recorded.
func TestOwnershipClearsOnTeardown(t *testing.T) {
	r := &Receiver{
		Name: "Test",
		Ring: audio.NewRing(1024),
		Logf: func(string, ...any) {},
	}
	owner, _ := pipeConn(t)
	sdp := []byte("a=fmtp:96 352 0 16 40 10 14 2 255 0 0 44100\r\n" +
		"a=rsaaeskey:" + capturedAnnounces[0].rsaAESKey + "\r\n" +
		"a=aesiv:" + capturedAnnounces[0].aesIV + "\r\n")

	if err := r.announce(owner, sdp); err != nil {
		t.Fatalf("announce: %v", err)
	}
	r.teardown()

	r.mu.Lock()
	got := r.owner
	r.mu.Unlock()
	if got != nil {
		t.Error("owner survived teardown")
	}
}
