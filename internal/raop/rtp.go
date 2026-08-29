package raop

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/sudarkoff/sonospoof/internal/alac"
	"github.com/sudarkoff/sonospoof/internal/audio"
)

// RTP payload types used by RAOP.
const (
	ptTimingRequest  = 82
	ptTimingReply    = 83
	ptSync           = 84
	ptRetransmit     = 86
	ptAudio          = 96
	rtpHeaderLen     = 12
	retransmitOffset = 4 // resend responses wrap the original packet
)

var errShortPacket = errors.New("raop: packet shorter than an RTP header")

// AudioDecoder turns encrypted RAOP audio packets into PCM in a ring.
//
// One per session: it owns an ALAC decoder, which carries adaptive predictor
// state and scratch buffers and must not be shared between streams.
type AudioDecoder struct {
	// mu serialises everything below. SETUP starts a reader goroutine for the
	// audio port and another for the control port, and both deliver packets
	// here -- the control port carries retransmit responses, which are audio.
	// Without this they race the resequencer's map, the ALAC decoder's
	// adaptive state and the shared pcm buffer, which corrupts audio and can
	// panic on concurrent map write.
	mu sync.Mutex

	block cipher.Block
	iv    []byte
	dec   *alac.Decoder
	ring  *audio.Ring
	queue *resequencer

	pcm []int16

	// Diagnostics.
	Packets   uint64
	Decrypted uint64
	Errors    uint64
}

// Stats reports what the network did to this session.
func (a *AudioDecoder) Stats() (packets, reordered, lost, late, errs uint64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.Packets, a.queue.Reordered, a.queue.Lost, a.queue.Late, a.Errors
}

// OnMissing installs the callback used to ask the sender for a resend. It must
// be set before packets start arriving.
func (a *AudioDecoder) OnMissing(f func(first, count uint16)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.queue.onMissing = f
}

// NewAudioDecoder builds the audio path for one session from the session key
// and IV out of the ANNOUNCE SDP, and the ALAC config from a=fmtp.
func NewAudioDecoder(key, iv []byte, cfg alac.Config, ring *audio.Ring) (*AudioDecoder, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("raop: session key: %w", err)
	}
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("raop: IV is %d bytes, want %d", len(iv), aes.BlockSize)
	}
	return &AudioDecoder{
		block: block,
		iv:    append([]byte(nil), iv...),
		dec:   alac.NewDecoder(cfg),
		ring:  ring,
		queue: newResequencer(),
		pcm:   make([]int16, int(cfg.FrameLength)*int(cfg.NumChannels)),
	}, nil
}

// Packet processes one datagram from the audio or control port. Packets that
// are not audio are ignored rather than treated as errors: sync and timing
// arrive on their own ports and are not needed to make sound.
func (a *AudioDecoder) Packet(pkt []byte) error {
	if len(pkt) < rtpHeaderLen {
		return errShortPacket
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	a.Packets++

	pt := pkt[1] & 0x7f

	switch pt {
	case ptRetransmit:
		// A resend response carries the original packet after a 4-byte
		// wrapper; unwrap and fall through to the normal path.
		if len(pkt) < retransmitOffset+rtpHeaderLen {
			return errShortPacket
		}
		return a.audio(pkt[retransmitOffset:])
	case ptAudio:
		return a.audio(pkt)
	case ptSync, ptTimingRequest, ptTimingReply:
		return nil
	default:
		return nil
	}
}

// SeqNo returns the RTP sequence number of a packet, for loss tracking.
func SeqNo(pkt []byte) uint16 {
	if len(pkt) < 4 {
		return 0
	}
	return binary.BigEndian.Uint16(pkt[2:4])
}

// Timestamp returns the RTP timestamp of a packet.
func Timestamp(pkt []byte) uint32 {
	if len(pkt) < 8 {
		return 0
	}
	return binary.BigEndian.Uint32(pkt[4:8])
}

// audio queues one packet by sequence number and decodes whatever that makes
// ready, in order.
func (a *AudioDecoder) audio(pkt []byte) error {
	if len(pkt) <= rtpHeaderLen {
		return nil
	}

	// The packet must be copied: the reader reuses its receive buffer, and the
	// resequencer can hold a packet across many subsequent reads. Without this
	// a held packet would be silently overwritten by later traffic and decode
	// as someone else's audio.
	payload := make([]byte, len(pkt)-rtpHeaderLen)
	copy(payload, pkt[rtpHeaderLen:])

	var firstErr error
	for _, p := range a.queue.push(SeqNo(pkt), payload) {
		if err := a.decodeOne(p); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *AudioDecoder) decodeOne(payload []byte) error {
	plain := a.decrypt(payload)

	n, err := a.dec.Decode(plain, a.pcm)
	if err != nil {
		a.Errors++
		return err
	}
	a.ring.Write(a.pcm[:n*2])
	return nil
}

// decrypt undoes AES-128-CBC over the whole 16-byte blocks of the payload.
//
// The tail that does not fill a block is left untouched. RAOP does not pad:
// the sender encrypts floor(len/16) blocks and ships the remaining bytes in
// the clear, so "decrypting" them would corrupt the end of every frame. The
// IV is reset for each packet -- CBC chaining does not run across packets,
// which is what makes a lost packet recoverable instead of poisoning the rest
// of the stream.
func (a *AudioDecoder) decrypt(payload []byte) []byte {
	out := make([]byte, len(payload))
	copy(out, payload)

	whole := len(payload) / aes.BlockSize * aes.BlockSize
	if whole == 0 {
		return out
	}
	cipher.NewCBCDecrypter(a.block, a.iv).CryptBlocks(out[:whole], payload[:whole])
	a.Decrypted++
	return out
}
