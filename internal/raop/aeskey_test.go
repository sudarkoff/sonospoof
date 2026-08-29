package raop

import "testing"

// Captured from a real ANNOUNCE sent by an iPhone (AirPlay/960.13.1) during
// the M0 probe. If the embedded key were wrong or the padding mode were
// PKCS#1 v1.5 rather than OAEP, this would fail to unwrap -- which makes it
// the strongest single check that the whole crypto path is right.
const (
	capturedRSAAESKey = "W1FWCKNg20gpUXbGW2eN3lsttFwHeRsKLMz7d8jzXS5UVNS/g7HzMIJQ5gex170DyHjMzxxjkeR3J0b+qFjMGhR9vU4YegoNfHshIg/LjI7xYc9Tt4n4OoWSea5Uw8D7dmenyC4cs1PFeV+ouSY3p/ea4j7BWu9s6sUwdHPKoMhLPGu9u8/ezDJUVoh+KV19urQ5RQ37SGq/5QPhPlu9zC/v0GdUtBc0pERsjZqOLPU0dg8ZkLAEpEc7ItuVu4yLNZ8/TIaDGR2zPaiGNYAR9G8oTMAU9hnTXej2pfEyTtyBNAFOzpGeWdzjYlwLv4YS0l16JtcsmGOxbayyOMikGg=="
	capturedAESIV     = "RmQ0LuN4Ww7QCp3qOdDcnQ=="
)

func TestSessionKeyUnwrapsCapturedAnnounce(t *testing.T) {
	key, err := SessionKey(capturedRSAAESKey)
	if err != nil {
		t.Fatalf("SessionKey: %v", err)
	}
	if len(key) != 16 {
		t.Errorf("expected 16 bytes, got %d", len(key))
	}
}

func TestSessionIVDecodesCapturedAnnounce(t *testing.T) {
	iv, err := SessionIV(capturedAESIV)
	if err != nil {
		t.Fatalf("SessionIV: %v", err)
	}
	if len(iv) != 16 {
		t.Errorf("expected a 16-byte IV, got %d", len(iv))
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
