package bridge

import (
	"context"
	"fmt"
	"net"
	"strings"

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
func (a *Advertiser) Add(name string, id net.HardwareAddr, port int) error {
	if id == nil {
		return fmt.Errorf("advertise %q: no RAOP id", name)
	}
	hex := strings.ToUpper(strings.ReplaceAll(id.String(), ":", ""))

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
