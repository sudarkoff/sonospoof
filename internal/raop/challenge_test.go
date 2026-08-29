package raop

import (
	"crypto"
	"crypto/rsa"
	"encoding/base64"
	"net"
	"strings"
	"testing"
)

// A real challenge captured from an iPhone (AirPlay/960.13.1) during the M0
// probe, so the tests run against the shape a sender actually sends.
const realChallenge = "x6Bffo+ao2nrB1PSn2knNQ=="

func TestAppleResponseVerifiesAgainstPublicKey(t *testing.T) {
	ip := net.ParseIP("192.168.30.134")
	mac, err := net.ParseMAC("f6:d0:66:2b:9a:2a")
	if err != nil {
		t.Fatalf("parsing mac: %v", err)
	}

	got, err := AppleResponse(realChallenge, ip, mac)
	if err != nil {
		t.Fatalf("AppleResponse: %v", err)
	}
	if strings.Contains(got, "=") {
		t.Errorf("response should have base64 padding stripped, got %q", got)
	}

	// The sender verifies with the matching public key, so we must too --
	// this is what catches a wrong padding mode or a hashed block.
	sig, err := base64.RawStdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	want := append([]byte{}, mustDecode(t, realChallenge)...)
	want = append(want, ip.To4()...)
	want = append(want, mac...)
	for len(want) < challengeBlockMin {
		want = append(want, 0)
	}

	if err := rsa.VerifyPKCS1v15(&airportExpressKey.PublicKey, crypto.Hash(0), want, sig); err != nil {
		t.Errorf("signature does not verify over challenge||ip||mac: %v", err)
	}
}

// Over IPv6 the block is already longer than the pad floor. The iPhone
// reached the probe on a link-local address, so this is the live path, not a
// theoretical one.
func TestAppleResponseIPv6BlockIsNotPadded(t *testing.T) {
	ip := net.ParseIP("fe80::94:e100:4e8d:873f")
	mac, _ := net.ParseMAC("f6:d0:66:2b:9a:2a")

	got, err := AppleResponse(realChallenge, ip, mac)
	if err != nil {
		t.Fatalf("AppleResponse: %v", err)
	}
	sig, err := base64.RawStdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	want := append([]byte{}, mustDecode(t, realChallenge)...)
	want = append(want, ip.To16()...)
	want = append(want, mac...)
	if len(want) != 16+16+6 {
		t.Fatalf("expected an unpadded 38-byte block, got %d", len(want))
	}
	if err := rsa.VerifyPKCS1v15(&airportExpressKey.PublicKey, crypto.Hash(0), want, sig); err != nil {
		t.Errorf("IPv6 signature does not verify: %v", err)
	}
}

func TestAppleResponseAcceptsUnpaddedChallenge(t *testing.T) {
	ip := net.ParseIP("10.0.0.1")
	mac, _ := net.ParseMAC("f6:d0:66:2b:9a:2a")

	padded, err := AppleResponse(realChallenge, ip, mac)
	if err != nil {
		t.Fatalf("padded: %v", err)
	}
	unpadded, err := AppleResponse(strings.TrimRight(realChallenge, "="), ip, mac)
	if err != nil {
		t.Fatalf("unpadded: %v", err)
	}
	if padded != unpadded {
		t.Errorf("padding of the challenge changed the response:\n padded=%s\n unpadded=%s", padded, unpadded)
	}
}

func TestAppleResponseRejectsBadMAC(t *testing.T) {
	if _, err := AppleResponse(realChallenge, net.ParseIP("10.0.0.1"), []byte{1, 2, 3}); err == nil {
		t.Error("expected an error for a 3-byte hardware address")
	}
}

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decoding %q: %v", s, err)
	}
	return b
}
