package raop

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"testing"

	"github.com/sudarkoff/sonospoof/internal/alac"
	"github.com/sudarkoff/sonospoof/internal/audio"
)

func testDecoder(t *testing.T) *AudioDecoder {
	t.Helper()
	key := bytes.Repeat([]byte{0xA5}, 16)
	iv := bytes.Repeat([]byte{0x3C}, 16)
	cfg := alac.Config{
		FrameLength: 352, BitDepth: 16, PB: 40, MB: 10, KB: 14,
		NumChannels: 2, MaxRun: 255, SampleRate: 44100,
	}
	a, err := NewAudioDecoder(key, iv, cfg, audio.NewRing(4096))
	if err != nil {
		t.Fatalf("NewAudioDecoder: %v", err)
	}
	return a
}

// The detail that would silently corrupt the end of every frame: RAOP does
// not pad. The sender encrypts only whole 16-byte blocks and ships the
// remainder in the clear, so the tail must survive untouched.
func TestDecryptLeavesTrailingPartialBlockInTheClear(t *testing.T) {
	a := testDecoder(t)

	plain := make([]byte, 100) // 6 whole blocks + 4 loose bytes
	for i := range plain {
		plain[i] = byte(i)
	}

	// Build what a sender would put on the wire.
	key := bytes.Repeat([]byte{0xA5}, 16)
	iv := bytes.Repeat([]byte{0x3C}, 16)
	block, _ := aes.NewCipher(key)
	wire := make([]byte, len(plain))
	copy(wire, plain)
	whole := len(plain) / 16 * 16
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(wire[:whole], plain[:whole])

	if bytes.Equal(wire[:whole], plain[:whole]) {
		t.Fatal("test setup did not actually encrypt anything")
	}
	if !bytes.Equal(wire[whole:], plain[whole:]) {
		t.Fatal("test setup wrongly encrypted the tail")
	}

	got := a.decrypt(wire)
	if !bytes.Equal(got, plain) {
		t.Errorf("round trip failed\n got %v\nwant %v", got[whole:], plain[whole:])
	}
}

// CBC chaining must not run across packets, or one lost datagram would poison
// everything after it.
func TestDecryptResetsIVEachPacket(t *testing.T) {
	a := testDecoder(t)
	payload := bytes.Repeat([]byte{0x11}, 32)

	first := append([]byte(nil), a.decrypt(payload)...)
	second := a.decrypt(payload)

	if !bytes.Equal(first, second) {
		t.Error("decrypting the same payload twice gave different results; IV is carrying over")
	}
}

// A payload with no whole block at all must pass through untouched rather
// than panic or truncate.
func TestDecryptShorterThanOneBlock(t *testing.T) {
	a := testDecoder(t)
	payload := []byte{1, 2, 3}
	if got := a.decrypt(payload); !bytes.Equal(got, payload) {
		t.Errorf("got %v, want %v", got, payload)
	}
}

func TestPacketIgnoresNonAudioTypes(t *testing.T) {
	a := testDecoder(t)
	for _, pt := range []byte{ptSync, ptTimingRequest, ptTimingReply} {
		pkt := make([]byte, 32)
		pkt[1] = pt
		if err := a.Packet(pkt); err != nil {
			t.Errorf("payload type %d: unexpected error %v", pt, err)
		}
	}
}

func TestPacketRejectsRunts(t *testing.T) {
	a := testDecoder(t)
	if err := a.Packet([]byte{0x80, 0x60}); err == nil {
		t.Error("expected an error for a packet shorter than the RTP header")
	}
}

func TestSeqAndTimestamp(t *testing.T) {
	pkt := make([]byte, rtpHeaderLen)
	pkt[1] = ptAudio
	binary.BigEndian.PutUint16(pkt[2:], 0xBEEF)
	binary.BigEndian.PutUint32(pkt[4:], 0xDEADBEEF)

	if got := SeqNo(pkt); got != 0xBEEF {
		t.Errorf("SeqNo = %#x, want 0xBEEF", got)
	}
	if got := Timestamp(pkt); got != 0xDEADBEEF {
		t.Errorf("Timestamp = %#x, want 0xDEADBEEF", got)
	}
}
