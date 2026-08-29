// Package bridge joins one AirPlay receiver to one Sonos zone.
package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/sudarkoff/sonospoof/internal/audio"
	"github.com/sudarkoff/sonospoof/internal/raop"
	"github.com/sudarkoff/sonospoof/internal/sonos"
)

// ringSeconds is how much audio the buffer holds. AirPlay itself buffers
// around two seconds and Sonos adds its own, so this only needs to absorb
// jitter, not latency; oversizing it would just add delay on top of the two to
// four seconds that are already inherent.
const ringSeconds = 2

// Bridge is one zone's pipeline: an AirPlay target, a ring, and the Sonos it
// pushes to.
type Bridge struct {
	Zone sonos.Zone

	ring *audio.Ring
	recv *raop.Receiver

	// streamHost is the address the speaker will fetch from, i.e. ours.
	streamHost string

	mu      sync.Mutex
	nonce   string
	playing bool
}

// New builds a bridge for one zone. streamHost is "ip:port" of the shared
// stream server.
func New(z sonos.Zone, streamHost string) *Bridge {
	b := &Bridge{
		Zone:       z,
		ring:       audio.NewRing(audio.SampleRate * audio.Channels * ringSeconds),
		streamHost: streamHost,
	}
	b.recv = &raop.Receiver{
		Name:    z.Name,
		ID:      z.RAOPID,
		Ring:    b.ring,
		Handler: b,
		Logf:    log.Printf,
	}
	return b
}

// Receiver exposes the RAOP receiver so the caller can bind and advertise it.
func (b *Bridge) Receiver() *raop.Receiver { return b.recv }

// StreamPath is the current session's URL path, unique per session.
func (b *Bridge) StreamPath() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pathLocked()
}

func (b *Bridge) pathLocked() string {
	return "/zone/" + b.Zone.CoordinatorUUID + "/" + b.nonce + ".wav"
}

// Start is called when the sender sends RECORD. It points the Sonos at a
// freshly-nonced URL and starts playback.
//
// The nonce is not decoration: Sonos caches by URL and will refuse to reopen
// one it has seen, so a second session on the same path plays nothing.
func (b *Bridge) Start(sessionName string) error {
	nonce, err := newNonce()
	if err != nil {
		return err
	}

	b.mu.Lock()
	b.nonce = nonce
	url := "http://" + b.streamHost + b.pathLocked()
	b.mu.Unlock()

	title := "AirPlay"
	if sessionName != "" {
		title = sessionName
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := sonos.SetStreamAndPlay(ctx, b.Zone.IP, url, title); err != nil {
		return fmt.Errorf("bridge %s: %w", b.Zone.Name, err)
	}

	b.mu.Lock()
	b.playing = true
	b.mu.Unlock()

	log.Printf("%s: streaming to %s (%s)", b.Zone.Name, b.Zone.IP, url)
	return nil
}

// Stop is called on TEARDOWN.
func (b *Bridge) Stop() {
	b.mu.Lock()
	wasPlaying := b.playing
	b.playing = false
	b.mu.Unlock()

	if !wasPlaying {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sonos.Stop(ctx, b.Zone.IP); err != nil {
		log.Printf("%s: stop: %v", b.Zone.Name, err)
	}
	log.Printf("%s: session ended", b.Zone.Name)
}

// SetVolume maps AirPlay's dB onto the Sonos scale.
func (b *Bridge) SetVolume(db float64) {
	vol := raop.VolumeToSonos(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sonos.SetVolume(ctx, b.Zone.IP, vol); err != nil {
		log.Printf("%s: volume: %v", b.Zone.Name, err)
	}
}

// ServeStream writes the endless WAV. It returns when the speaker disconnects.
func (b *Bridge) ServeStream(w http.ResponseWriter, r *http.Request) {
	log.Printf("%s: speaker connected from %s (%s)",
		b.Zone.Name, r.RemoteAddr, r.Header.Get("User-Agent"))

	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	// Deliberately no Content-Length: the body never ends.
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(audio.WAVHeader()); err != nil {
		return
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	start := time.Now()
	err := audio.StreamPCM(w, b.ring, audio.SampleRate/10)
	under, dropped := b.ring.Stats()
	log.Printf("%s: speaker disconnected after %s (silence-padded %d, dropped %d): %v",
		b.Zone.Name, time.Since(start).Round(time.Second), under, dropped, err)
}

func newNonce() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// LocalIPFor returns the local address that would be used to reach peer. The
// speaker has to fetch from us, so the URL must carry an address it can
// actually route to -- not a guess and not a loopback.
func LocalIPFor(peer string) (net.IP, error) {
	c, err := net.Dial("udp", net.JoinHostPort(peer, "1400"))
	if err != nil {
		return nil, err
	}
	defer c.Close()
	return c.LocalAddr().(*net.UDPAddr).IP, nil
}
