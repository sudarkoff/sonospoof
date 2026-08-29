package raop

import (
	"encoding/binary"
	"sync"
	"testing"

	"github.com/sudarkoff/sonospoof/internal/alac"
	"github.com/sudarkoff/sonospoof/internal/audio"
)

// SETUP starts one reader goroutine for the audio port and another for the
// control port, and both deliver packets into the same AudioDecoder -- the
// control port carries retransmit responses, which are audio. Run under
// -race, this fails on the unsynchronised version: the resequencer's map, the
// ALAC decoder's adaptive state and the shared pcm buffer are all shared.
//
// The symptom in the field was glitchy audio rather than a crash, which is
// the worse outcome: a concurrent map write panics loudly, but a raced
// decoder just produces slightly wrong samples.
func TestAudioDecoderIsSafeForConcurrentPackets(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 16)
	cfg := alac.Config{
		FrameLength: 352, BitDepth: 16, PB: 40, MB: 10, KB: 14,
		NumChannels: 2, MaxRun: 255, SampleRate: 44100,
	}
	dec, err := NewAudioDecoder(key, iv, cfg, audio.NewRing(1<<16))
	if err != nil {
		t.Fatalf("NewAudioDecoder: %v", err)
	}

	makePacket := func(seq uint16) []byte {
		p := make([]byte, rtpHeaderLen+64)
		p[0] = 0x80
		p[1] = ptAudio
		binary.BigEndian.PutUint16(p[2:], seq)
		return p
	}

	// Two writers, as the two sockets would be. Decode errors are expected
	// and irrelevant here -- the payloads are not real ALAC. What matters is
	// that concurrent delivery does not race or panic.
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				_ = dec.Packet(makePacket(uint16(base + i*2)))
			}
		}(w)
	}
	wg.Wait()

	// Stats must also be safe to read while packets are arriving.
	var wg2 sync.WaitGroup
	stop := make(chan struct{})
	wg2.Add(1)
	go func() {
		defer wg2.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _, _, _, _ = dec.Stats()
			}
		}
	}()
	for i := 0; i < 200; i++ {
		_ = dec.Packet(makePacket(uint16(1000 + i)))
	}
	close(stop)
	wg2.Wait()
}
