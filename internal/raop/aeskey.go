package raop

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"strings"
)

// SessionKey unwraps the AES key a sender puts in the ANNOUNCE SDP.
//
// The a=rsaaeskey attribute is the session's AES-128 key encrypted to the
// AirPort Express public key with RSA-OAEP/SHA-1 -- OAEP, not the PKCS#1 v1.5
// used for the Apple-Response, so the two are not interchangeable. Paired with
// a=aesiv it decrypts the audio payload as AES-128-CBC.
func SessionKey(rsaAESKey string) ([]byte, error) {
	ct, err := decodeB64(rsaAESKey)
	if err != nil {
		return nil, fmt.Errorf("decoding a=rsaaeskey: %w", err)
	}
	key, err := rsa.DecryptOAEP(sha1.New(), rand.Reader, airportExpressKey, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("unwrapping session key: %w", err)
	}
	if len(key) != 16 {
		return nil, fmt.Errorf("expected a 16-byte AES-128 key, got %d bytes", len(key))
	}
	return key, nil
}

// SessionIV decodes the a=aesiv attribute into the 16-byte CBC IV.
func SessionIV(aesIV string) ([]byte, error) {
	iv, err := decodeB64(aesIV)
	if err != nil {
		return nil, fmt.Errorf("decoding a=aesiv: %w", err)
	}
	if len(iv) != 16 {
		return nil, fmt.Errorf("expected a 16-byte IV, got %d bytes", len(iv))
	}
	return iv, nil
}

// decodeB64 tolerates the missing '=' padding some senders omit in SDP.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	if b, err := base64.StdEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(strings.TrimRight(s, "="))
}
