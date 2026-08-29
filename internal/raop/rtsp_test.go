package raop

import (
	"bufio"
	"strings"
	"testing"
)

// AirPlay's volume is dB, not a percentage. Mapping the whole -144..0 range
// linearly would squash normal listening levels into the bottom fifth of the
// Sonos dial, so -144 is special-cased as mute and the usable range is -30..0.
func TestVolumeToSonos(t *testing.T) {
	cases := []struct {
		db   float64
		want int
	}{
		{-144, 0},  // AirPlay's mute sentinel
		{-200, 0},  // below the sentinel
		{-30, 0},   // bottom of the usable range
		{-15, 50},  // midpoint
		{0, 100},   // full
		{5, 100},   // above full, clamped
		{-45, 0},   // below the floor, clamped
		{-7.5, 75}, // quarter steps land where expected
		{-22.5, 25},
	}
	for _, c := range cases {
		if got := VolumeToSonos(c.db); got != c.want {
			t.Errorf("VolumeToSonos(%.1f) = %d, want %d", c.db, got, c.want)
		}
	}
}

func TestVolumeToSonosStaysInRange(t *testing.T) {
	for db := -200.0; db <= 20.0; db += 0.25 {
		if v := VolumeToSonos(db); v < 0 || v > 100 {
			t.Fatalf("VolumeToSonos(%.2f) = %d, out of range", db, v)
		}
	}
}

func TestReadRequestParsesHeadersAndBody(t *testing.T) {
	raw := "ANNOUNCE rtsp://fe80::1/123 RTSP/1.0\r\n" +
		"CSeq: 4\r\n" +
		"Content-Type: application/sdp\r\n" +
		"Content-Length: 5\r\n" +
		"Apple-Challenge: abc==\r\n" +
		"\r\n" +
		"hello"

	req, err := readRequest(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("readRequest: %v", err)
	}
	if req.Method != "ANNOUNCE" {
		t.Errorf("method = %q", req.Method)
	}
	if req.Target != "rtsp://fe80::1/123" {
		t.Errorf("target = %q", req.Target)
	}
	// Header lookup must be case-insensitive: senders vary.
	if req.Headers["apple-challenge"] != "abc==" {
		t.Errorf("apple-challenge = %q", req.Headers["apple-challenge"])
	}
	if req.Headers["cseq"] != "4" {
		t.Errorf("cseq = %q", req.Headers["cseq"])
	}
	if string(req.Body) != "hello" {
		t.Errorf("body = %q", req.Body)
	}
}

// OPTIONS * has no body and no content-length; it must not hang waiting for one.
func TestReadRequestHandlesBodylessOptions(t *testing.T) {
	raw := "OPTIONS * RTSP/1.0\r\nCSeq: 0\r\n\r\n"
	req, err := readRequest(bufio.NewReader(strings.NewReader(raw)))
	if err != nil {
		t.Fatalf("readRequest: %v", err)
	}
	if req.Method != "OPTIONS" || req.Target != "*" {
		t.Errorf("got %s %s", req.Method, req.Target)
	}
	if len(req.Body) != 0 {
		t.Errorf("body should be empty, got %q", req.Body)
	}
}

func TestWriteResponseEchoesCSeq(t *testing.T) {
	req := &request{Headers: map[string]string{"cseq": "7"}}
	var b strings.Builder
	if err := writeResponse(&b, req, &response{
		Status:  "200 OK",
		Headers: []string{"Apple-Response: sig"},
	}); err != nil {
		t.Fatalf("writeResponse: %v", err)
	}
	out := b.String()
	for _, want := range []string{"RTSP/1.0 200 OK", "CSeq: 7", "Apple-Response: sig"} {
		if !strings.Contains(out, want) {
			t.Errorf("response missing %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "\r\n\r\n") {
		t.Error("response must end with a blank line")
	}
}
