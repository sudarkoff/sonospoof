package raop

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"fmt"
	"net"
	"strings"
)

// challengeBlockMin is the length the signed block is zero-padded up to.
// Over IPv4 the block is 16+4+6 = 26 bytes and needs padding; over IPv6 it is
// 16+16+6 = 38 and is already longer, so the pad is a floor, not a fixed size.
const challengeBlockMin = 32

// AppleResponse answers an RTSP Apple-Challenge header.
//
// The sender challenges us to prove we hold the AirPort Express key. The
// block it expects signed is the raw challenge, then the address the sender
// reached us on, then our hardware address:
//
//	response = RSA-sign( challenge || localIP || mac )
//
// signed with PKCS#1 v1.5 padding and *no* hash -- the block is signed
// directly rather than digested, which is why crypto.Hash(0) is passed to
// SignPKCS1v15. Apple's own receivers zero-pad the block to 32 bytes.
//
// localIP must be the address on the interface the sender actually connected
// to. Getting it wrong produces a well-formed signature that the sender
// silently rejects, so callers should take it from the accepted connection
// rather than from a config value.
//
// The returned string is base64 with the '=' padding stripped, which is the
// form the Apple-Response header carries.
func AppleResponse(challenge string, localIP net.IP, mac net.HardwareAddr) (string, error) {
	raw, err := decodeChallenge(challenge)
	if err != nil {
		return "", fmt.Errorf("decoding Apple-Challenge: %w", err)
	}
	if len(mac) != 6 {
		return "", fmt.Errorf("hardware address must be 6 bytes, got %d", len(mac))
	}

	// Use the 4-byte form for IPv4 so the block matches what the sender built;
	// To4 returns nil for a genuine IPv6 address, which we take as-is.
	ip := localIP
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	} else {
		ip = ip.To16()
	}
	if len(ip) == 0 {
		return "", fmt.Errorf("local IP %v is neither IPv4 nor IPv6", localIP)
	}

	block := make([]byte, 0, len(raw)+len(ip)+len(mac))
	block = append(block, raw...)
	block = append(block, ip...)
	block = append(block, mac...)
	for len(block) < challengeBlockMin {
		block = append(block, 0)
	}

	sig, err := rsa.SignPKCS1v15(rand.Reader, airportExpressKey, crypto.Hash(0), block)
	if err != nil {
		return "", fmt.Errorf("signing challenge block: %w", err)
	}
	return strings.TrimRight(base64.StdEncoding.EncodeToString(sig), "="), nil
}

// decodeChallenge accepts the header value with or without base64 padding;
// senders differ on whether they include it.
func decodeChallenge(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}
