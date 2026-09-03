package bridge

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/brutella/dnssd"
)

// Advertiser publishes every zone as its own _raop._tcp target.
//
// One responder carries all the services. Running a responder per zone -- or
// alongside Avahi -- is a reliable way to produce intermittent, unexplainable
// discovery failures.
type Advertiser struct {
	responder dnssd.Responder
	iface     string
}

func NewAdvertiser(iface string) (*Advertiser, error) {
	r, err := dnssd.NewResponder()
	if err != nil {
		return nil, err
	}
	return &Advertiser{responder: r, iface: iface}, nil
}

// Add registers one AirPlay target.
//
// The instance name must be "<12 hex>@<display name>" or the device is either
// invisible or unselectable. The hex is the zone's own id, derived from the
// Sonos MAC, so every target is distinct and stable across restarts.
// IDSalt, when non-zero, perturbs the last byte of every advertised RAOP id.
//
// This exists for one specific failure: a sender that has decided not to talk
// to a particular device and will not be argued out of it. macOS was observed
// resolving a zone's advert correctly, with the listener answering a
// hand-rolled RTSP OPTIONS from that same machine, and still never emitting a
// SYN for it -- while other zones from the same daemon worked. Flushing the
// mDNS cache, restarting ControlCenter and rebooting the speaker changed
// nothing, because the decision is made inside the client before any packet is
// sent.
//
// Changing the id makes the target a device the sender has no history with.
// It is a workaround, not an explanation, and it costs the stable identity
// that keeps senders from seeing a new device after every restart -- so change
// it only when a sender is actually stuck, and then leave it alone.
var IDSalt byte

// SaltedID applies IDSalt to a zone's RAOP id.
//
// Both the mDNS advertisement and the Apple-Response must use the same value:
// the sender verifies the signature over challenge||localIP||mac, and the only
// mac it knows is the one in the instance name we published. Salt one and not
// the other and the signature is well-formed but wrong, which the sender
// rejects silently.
func SaltedID(id net.HardwareAddr) net.HardwareAddr {
	if IDSalt == 0 || len(id) == 0 {
		return id
	}
	out := append(net.HardwareAddr(nil), id...)
	out[len(out)-1] ^= IDSalt
	return out
}

func (a *Advertiser) Add(name string, id net.HardwareAddr, port int) error {
	if id == nil {
		return fmt.Errorf("advertise %q: no RAOP id", name)
	}
	hex := strings.ToUpper(strings.ReplaceAll(SaltedID(id).String(), ":", ""))

	cfg := dnssd.Config{
		Name:   hex + "@" + name,
		Type:   "_raop._tcp",
		Domain: "local",
		Port:   port,
		Text:   txtRecords(),
	}
	if a.iface != "" {
		cfg.Ifaces = []string{a.iface}
	}

	svc, err := dnssd.NewService(cfg)
	if err != nil {
		return fmt.Errorf("advertise %q: %w", name, err)
	}
	if _, err := a.responder.Add(svc); err != nil {
		return fmt.Errorf("advertise %q: %w", name, err)
	}
	return nil
}

// Run serves mDNS until ctx is cancelled.
func (a *Advertiser) Run(ctx context.Context) error {
	return a.responder.Respond(ctx)
}

// Retire announces the pre-salt identities and immediately withdraws them, so
// the mDNS goodbye tells every client to forget them.
//
// Changing IDSalt leaves the previous identity in senders' caches, where it
// shows up as a duplicate entry in the AirPlay picker pointing at a device
// that no longer exists. Restarting the daemon does not clear it: the old
// service is never withdrawn, it simply stops being announced, and clients
// hold what they last saw until it ages out. A goodbye purges it at once and
// everywhere, rather than asking each user to reset their own machine.
//
// Cheap and idempotent: with no salt there is nothing to retire.
func (a *Advertiser) Retire(ctx context.Context, zones []RetiredZone) {
	if IDSalt == 0 || len(zones) == 0 {
		return
	}
	var handles []dnssd.ServiceHandle
	for _, z := range zones {
		if z.ID == nil {
			continue
		}
		hex := strings.ToUpper(strings.ReplaceAll(z.ID.String(), ":", ""))
		cfg := dnssd.Config{
			Name:   hex + "@" + z.Name,
			Type:   "_raop._tcp",
			Domain: "local",
			Port:   z.Port,
		}
		if a.iface != "" {
			cfg.Ifaces = []string{a.iface}
		}
		svc, err := dnssd.NewService(cfg)
		if err != nil {
			continue
		}
		h, err := a.responder.Add(svc)
		if err != nil {
			continue
		}
		handles = append(handles, h)
	}
	if len(handles) == 0 {
		return
	}
	// Give the responder a moment to announce before withdrawing; a goodbye
	// for something never announced teaches listeners nothing.
	time.Sleep(2 * time.Second)
	for _, h := range handles {
		a.responder.Remove(h)
	}
}

// RetiredZone names an identity that should be withdrawn from the network.
type RetiredZone struct {
	Name string
	ID   net.HardwareAddr
	Port int
}

// txtRecords is the feature advertisement. These bits are the single most
// likely thing to be wrong when a device is invisible or refuses to connect,
// so they are spelled out rather than copied blind.
//
// et=0,1 offers "none or RSA". Note that et=0 is not a way to avoid the
// Apple-Challenge: iOS simply declines to proceed at all, which was measured
// during M0 rather than assumed.
func txtRecords() map[string]string {
	return map[string]string{
		"txtvers": "1",
		"ch":      "2",   // channels
		"cn":      "0,1", // codecs: PCM, ALAC
		"et":      "0,1", // encryption: none, RSA
		"sv":      "false",
		"da":      "true",
		"sr":      "44100", // sample rate
		"ss":      "16",    // sample size
		"pw":      "false", // no password
		"vn":      "65537",
		"tp":      "UDP",   // audio transport
		"vs":      "105.1", // AirTunes version; iOS gates behaviour on this
		// am is the model identifier, and iOS picks the AirPlay picker glyph
		// from it. AudioAccessory5,1 is Apple's HomePod identifier, and is
		// what a genuine AirPlay-capable Sonos advertises -- read off one on
		// this network rather than guessed. AirPort4,107 gets an AirPort
		// Express icon, which is accurate about our lineage and wrong about
		// what the user is actually selecting.
		//
		// iOS renders from its own icon set, so this yields the speaker glyph
		// rather than a literal Sonos rendering. There is no way to supply a
		// custom image over RAOP.
		"am": "AudioAccessory5,1",
		"md": "0,1,2", // metadata: text, artwork, progress
		"sf": "0x4",
	}
}
