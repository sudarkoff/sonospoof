package raop

import "testing"

// Captured from real ANNOUNCEs during the M0 probe. Two independent senders
// on different AirPlay versions, because one sender agreeing proves less than
// two: if the embedded key were wrong, or the padding mode were PKCS#1 v1.5
// rather than OAEP, neither would unwrap.
var capturedAnnounces = []struct {
	sender    string
	rsaAESKey string
	aesIV     string
}{
	{
		sender:    "iPhone, AirPlay/960.13.1, over IPv6 link-local",
		rsaAESKey: "W1FWCKNg20gpUXbGW2eN3lsttFwHeRsKLMz7d8jzXS5UVNS/g7HzMIJQ5gex170DyHjMzxxjkeR3J0b+qFjMGhR9vU4YegoNfHshIg/LjI7xYc9Tt4n4OoWSea5Uw8D7dmenyC4cs1PFeV+ouSY3p/ea4j7BWu9s6sUwdHPKoMhLPGu9u8/ezDJUVoh+KV19urQ5RQ37SGq/5QPhPlu9zC/v0GdUtBc0pERsjZqOLPU0dg8ZkLAEpEc7ItuVu4yLNZ8/TIaDGR2zPaiGNYAR9G8oTMAU9hnTXej2pfEyTtyBNAFOzpGeWdzjYlwLv4YS0l16JtcsmGOxbayyOMikGg==",
		aesIV:     "RmQ0LuN4Ww7QCp3qOdDcnQ==",
	},
	{
		sender:    "MacBook Air, AirPlay/950.7.1, over IPv6 loopback",
		rsaAESKey: "zBkQxUrlEVqs7mUiDwDUWxdqzAUreHVhmIgj/ZfWefEXJfKN8+j97W1Vu8CNITZZXCntoqgwRJVRiWlg1+NSwEuY6pU9CT4UwDOCRsX0fDV7vzJe38VRGmdJcoHlLLbuztXKnbi6cd7nCo3KYB7ZueEdd+lUgPBsMz6oHnX2zgHk5rtsebUjlF9qO+WXcZbWm4EMWezajx/DBdqCQ89Jv10QpIH+YlCdypaIqx+JaGj+ZeZ2IqEfri73a8+ydeOgHTSvFCBsnIO3XwxkXyA/wYDDuALjIlRaiWlz0QLhIsYXoszT1dHN6jBUKajk1kxdkscSAoRPFJFfJ7wIbeTKOw==",
		aesIV:     "hagv3iT1fOObLX2cjfqZlA==",
	},
}

func TestSessionKeyUnwrapsCapturedAnnounce(t *testing.T) {
	for _, tc := range capturedAnnounces {
		t.Run(tc.sender, func(t *testing.T) {
			key, err := SessionKey(tc.rsaAESKey)
			if err != nil {
				t.Fatalf("SessionKey: %v", err)
			}
			if len(key) != 16 {
				t.Errorf("expected 16 bytes, got %d", len(key))
			}
		})
	}
}

func TestSessionIVDecodesCapturedAnnounce(t *testing.T) {
	for _, tc := range capturedAnnounces {
		t.Run(tc.sender, func(t *testing.T) {
			iv, err := SessionIV(tc.aesIV)
			if err != nil {
				t.Fatalf("SessionIV: %v", err)
			}
			if len(iv) != 16 {
				t.Errorf("expected a 16-byte IV, got %d", len(iv))
			}
		})
	}
}

// The two senders must not produce the same session key; if they did, the
// "unwrap" would be returning something constant rather than decrypting.
func TestSessionKeysDifferBetweenSenders(t *testing.T) {
	a, err := SessionKey(capturedAnnounces[0].rsaAESKey)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	b, err := SessionKey(capturedAnnounces[1].rsaAESKey)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if string(a) == string(b) {
		t.Error("two different senders unwrapped to the same key")
	}
}

func TestSessionKeyRejectsGarbage(t *testing.T) {
	if _, err := SessionKey("not base64 at all!!"); err == nil {
		t.Error("expected an error for undecodable input")
	}
	// Valid base64, but not a ciphertext this key can unwrap.
	if _, err := SessionKey("AAAA"); err == nil {
		t.Error("expected an error for a non-ciphertext")
	}
}
