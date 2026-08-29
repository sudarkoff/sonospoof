package raop

import "testing"

// Verbatim ANNOUNCE body captured from an iPhone (AirPlay/960.13.1) during
// M0, IPv6 link-local and all.
var capturedSDP = "v=0\r\n" +
	"o=AirTunes 12494486997151664785 0 IN IP6 fe80::94:e100:4e8d:873f\r\n" +
	"s=AirTunes\r\n" +
	"i=G’s iPhone\r\n" +
	"c=IN IP6 fe80::94:e100:4e8d:873f\r\n" +
	"t=0 0\r\n" +
	"m=audio 0 RTP/AVP 96\r\n" +
	"a=rtpmap:96 AppleLossless\r\n" +
	"a=fmtp:96 352 0 16 40 10 14 2 255 0 0 44100\r\n" +
	"a=rsaaeskey:" + capturedAnnounces[0].rsaAESKey + "\r\n" +
	"a=aesiv:" + capturedAnnounces[0].aesIV + "\r\n" +
	"a=min-latency:11025\r\n" +
	"a=max-latency:88200\r\n"

func TestParseSDPFromRealAnnounce(t *testing.T) {
	s, err := ParseSDP([]byte(capturedSDP))
	if err != nil {
		t.Fatalf("ParseSDP: %v", err)
	}

	c := s.Config
	if c.FrameLength != 352 || c.BitDepth != 16 || c.NumChannels != 2 ||
		c.PB != 40 || c.MB != 10 || c.KB != 14 || c.MaxRun != 255 ||
		c.SampleRate != 44100 {
		t.Errorf("ALAC config mismatch: %+v", c)
	}
	if len(s.SessionKey) != 16 {
		t.Errorf("session key is %d bytes, want 16", len(s.SessionKey))
	}
	if len(s.IV) != 16 {
		t.Errorf("IV is %d bytes, want 16", len(s.IV))
	}
	if s.Name != "G’s iPhone" {
		t.Errorf("name = %q", s.Name)
	}
	if s.MinLatency != 11025 || s.MaxLatency != 88200 {
		t.Errorf("latency = %d..%d, want 11025..88200", s.MinLatency, s.MaxLatency)
	}
}

// The Mac sender's ANNOUNCE must parse too -- same shape, different values.
func TestParseSDPFromMacSender(t *testing.T) {
	sdp := "v=0\r\nm=audio 0 RTP/AVP 96\r\n" +
		"a=rtpmap:96 AppleLossless\r\n" +
		"a=fmtp:96 352 0 16 40 10 14 2 255 0 0 44100\r\n" +
		"a=rsaaeskey:" + capturedAnnounces[1].rsaAESKey + "\r\n" +
		"a=aesiv:" + capturedAnnounces[1].aesIV + "\r\n"

	s, err := ParseSDP([]byte(sdp))
	if err != nil {
		t.Fatalf("ParseSDP: %v", err)
	}
	if len(s.SessionKey) != 16 || len(s.IV) != 16 {
		t.Errorf("key/iv wrong size: %d/%d", len(s.SessionKey), len(s.IV))
	}

	first, _ := ParseSDP([]byte(capturedSDP))
	if string(first.SessionKey) == string(s.SessionKey) {
		t.Error("two senders produced the same session key")
	}
}

func TestParseSDPRejectsBadInput(t *testing.T) {
	cases := map[string]string{
		"no fmtp":       "v=0\r\na=rtpmap:96 AppleLossless\r\n",
		"short fmtp":    "a=fmtp:96 352 0 16\r\n",
		"zero frames":   "a=fmtp:96 0 0 16 40 10 14 2 255 0 0 44100\r\n",
		"zero channels": "a=fmtp:96 352 0 16 40 10 14 0 255 0 0 44100\r\n",
		"not alac":      "a=rtpmap:96 AAC-eld\r\na=fmtp:96 352 0 16 40 10 14 2 255 0 0 44100\r\n",
	}
	for name, body := range cases {
		if _, err := ParseSDP([]byte(body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// A key without an IV is malformed and must not be treated as unencrypted.
func TestParseSDPRejectsKeyWithoutIV(t *testing.T) {
	body := "a=fmtp:96 352 0 16 40 10 14 2 255 0 0 44100\r\n" +
		"a=rsaaeskey:" + capturedAnnounces[0].rsaAESKey + "\r\n"
	if _, err := ParseSDP([]byte(body)); err == nil {
		t.Error("expected an error for a key with no IV")
	}
}
