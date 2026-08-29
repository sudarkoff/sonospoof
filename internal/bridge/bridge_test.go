package bridge

import (
	"net"
	"strings"
	"testing"

	"github.com/sudarkoff/sonospoof/internal/raop"
	"github.com/sudarkoff/sonospoof/internal/sonos"
)

func testZone() sonos.Zone {
	mac, _ := net.ParseMAC("b8:e9:37:8e:38:84")
	return sonos.Zone{
		Name:            "Garage",
		CoordinatorUUID: "RINCON_B8E9378E388401400",
		IP:              "192.168.30.244",
		RAOPID:          mac,
		Members:         []string{"Garage"},
	}
}

// Sonos caches by URL and refuses to reopen one it has already fetched, so a
// repeated path makes the second session of a run play silence. The nonce is
// the only thing preventing that, and the failure is silent, so it is worth a
// test of its own.
func TestNonceChangesEverySession(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		n, err := newNonce()
		if err != nil {
			t.Fatalf("newNonce: %v", err)
		}
		if n == "" {
			t.Fatal("empty nonce")
		}
		if seen[n] {
			t.Fatalf("nonce %q repeated after %d draws", n, i)
		}
		seen[n] = true
	}
}

// The path must stay under the zone's own prefix, or the mux routes a
// session's audio to the wrong speaker.
func TestStreamPathIsScopedToTheZone(t *testing.T) {
	b := New(testZone(), "192.168.30.134:8000")

	prefix := "/zone/RINCON_B8E9378E388401400/"
	first := b.StreamPath()
	if !strings.HasPrefix(first, prefix) {
		t.Errorf("path %q is not under %q", first, prefix)
	}
	if !strings.HasSuffix(first, ".wav") {
		t.Errorf("path %q should end in .wav so Sonos recognises the format", first)
	}
}

// Bridge is what the receiver calls back into; a signature drift here would
// only show up at runtime.
func TestBridgeImplementsHandler(t *testing.T) {
	var _ raop.Handler = New(testZone(), "192.168.30.134:8000")
}

// The ring must be sized in samples, not frames: getting this wrong halves
// the buffer and doubles the underrun rate.
func TestRingIsSizedInSamplesNotFrames(t *testing.T) {
	b := New(testZone(), "host:1")
	// Fill past what a frame-sized ring would hold, and confirm nothing was
	// dropped -- a half-sized ring would have discarded the excess.
	want := 44100 * 2 * ringSeconds
	b.ring.Write(make([]int16, want))
	if _, dropped := b.ring.Stats(); dropped != 0 {
		t.Errorf("ring dropped %d samples when given exactly its stated capacity", dropped)
	}
}

// LocalIPFor must yield an address the speaker could actually reach back on,
// never loopback -- the speaker fetches audio from it.
func TestLocalIPForIsNotLoopback(t *testing.T) {
	ip, err := LocalIPFor("192.168.30.244")
	if err != nil {
		t.Skipf("no route to the test address: %v", err)
	}
	if ip.IsLoopback() {
		t.Errorf("got loopback %v; the speaker cannot fetch from that", ip)
	}
	if ip.IsUnspecified() {
		t.Errorf("got unspecified address %v", ip)
	}
}
