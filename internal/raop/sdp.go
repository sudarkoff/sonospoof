package raop

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sudarkoff/sonospoof/internal/alac"
)

// SDP is the part of an ANNOUNCE body this receiver needs.
type SDP struct {
	Config     alac.Config
	SessionKey []byte
	IV         []byte
	Name       string // i= line, e.g. "G's iPhone"

	MinLatency int
	MaxLatency int
}

// ParseSDP reads an RAOP ANNOUNCE body. A real capture looks like:
//
//	v=0
//	o=AirTunes 12494486997151664785 0 IN IP6 fe80::…
//	s=AirTunes
//	i=G's iPhone
//	c=IN IP6 fe80::…
//	t=0 0
//	m=audio 0 RTP/AVP 96
//	a=rtpmap:96 AppleLossless
//	a=fmtp:96 352 0 16 40 10 14 2 255 0 0 44100
//	a=rsaaeskey:…
//	a=aesiv:…
//	a=min-latency:11025
//	a=max-latency:88200
func ParseSDP(body []byte) (*SDP, error) {
	var s SDP
	sawFmtp := false

	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimRight(raw, "\r")
		if len(line) < 2 || line[1] != '=' {
			continue
		}
		kind, val := line[0], line[2:]

		switch kind {
		case 'i':
			s.Name = val
		case 'a':
			key, rest, ok := strings.Cut(val, ":")
			if !ok {
				continue
			}
			switch key {
			case "fmtp":
				cfg, err := parseFmtp(rest)
				if err != nil {
					return nil, err
				}
				s.Config = cfg
				sawFmtp = true
			case "rsaaeskey":
				k, err := SessionKey(rest)
				if err != nil {
					return nil, err
				}
				s.SessionKey = k
			case "aesiv":
				iv, err := SessionIV(rest)
				if err != nil {
					return nil, err
				}
				s.IV = iv
			case "min-latency":
				s.MinLatency, _ = strconv.Atoi(strings.TrimSpace(rest))
			case "max-latency":
				s.MaxLatency, _ = strconv.Atoi(strings.TrimSpace(rest))
			case "rtpmap":
				if !strings.Contains(rest, "AppleLossless") {
					return nil, fmt.Errorf("raop: unsupported codec in %q", rest)
				}
			}
		}
	}

	if !sawFmtp {
		return nil, fmt.Errorf("raop: ANNOUNCE has no a=fmtp line")
	}
	// An unencrypted session omits both; either alone is malformed.
	if (s.SessionKey == nil) != (s.IV == nil) {
		return nil, fmt.Errorf("raop: ANNOUNCE has a key without an IV, or vice versa")
	}
	return &s, nil
}

// parseFmtp reads the ALAC magic cookie carried on the a=fmtp line. The
// payload-type prefix is dropped by the caller's Cut, so rest looks like
// "96 352 0 16 40 10 14 2 255 0 0 44100".
func parseFmtp(rest string) (alac.Config, error) {
	f := strings.Fields(rest)
	// payload type, then the eleven cookie fields.
	if len(f) != 12 {
		return alac.Config{}, fmt.Errorf("raop: a=fmtp has %d fields, want 12: %q", len(f), rest)
	}
	n := make([]uint64, 11)
	for i := 0; i < 11; i++ {
		v, err := strconv.ParseUint(f[i+1], 10, 32)
		if err != nil {
			return alac.Config{}, fmt.Errorf("raop: a=fmtp field %d (%q): %w", i, f[i+1], err)
		}
		n[i] = v
	}

	cfg := alac.Config{
		FrameLength:       uint32(n[0]),
		CompatibleVersion: uint8(n[1]),
		BitDepth:          uint8(n[2]),
		PB:                uint8(n[3]),
		MB:                uint8(n[4]),
		KB:                uint8(n[5]),
		NumChannels:       uint8(n[6]),
		MaxRun:            uint16(n[7]),
		MaxFrameBytes:     uint32(n[8]),
		AvgBitRate:        uint32(n[9]),
		SampleRate:        uint32(n[10]),
	}

	if cfg.FrameLength == 0 {
		return alac.Config{}, fmt.Errorf("raop: a=fmtp declares a zero frame length")
	}
	if cfg.NumChannels == 0 {
		return alac.Config{}, fmt.Errorf("raop: a=fmtp declares zero channels")
	}
	return cfg, nil
}
