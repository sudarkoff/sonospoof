// Package sonos talks to Sonos ZonePlayers over UPnP.
//
// Everything here is unicast HTTP to port 1400. Only Discover uses multicast,
// and it is link-scoped: it will not find speakers on another VLAN. See
// CLAUDE.md -- the bridge is expected to run on the speakers' subnet.
package sonos

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	ssdpMulticast = "239.255.255.250:1900"
	ssdpTarget    = "urn:schemas-upnp-org:device:ZonePlayer:1"
	port          = 1400

	avTransportPath = "/MediaRenderer/AVTransport/Control"
	avTransportType = "urn:schemas-upnp-org:service:AVTransport:1"
	renderingPath   = "/MediaRenderer/RenderingControl/Control"
	renderingType   = "urn:schemas-upnp-org:service:RenderingControl:1"
	topologyPath    = "/ZoneGroupTopology/Control"
	topologyType    = "urn:schemas-upnp-org:service:ZoneGroupTopology:1"
)

// Player is one ZonePlayer.
type Player struct {
	IP        string
	RoomName  string
	ModelName string
	UDN       string // e.g. uuid:RINCON_B8E937EFDE1401400
}

// UUID strips the "uuid:" prefix from the UDN, giving the RINCON_… form used
// as the coordinator key in the topology.
func (p Player) UUID() string {
	return strings.TrimPrefix(p.UDN, "uuid:")
}

// RAOPID returns the 12 uppercase hex characters to use as this zone's AirPlay
// identity. The Sonos UUID embeds the unit's own MAC address --
// RINCON_B8E937EFDE14_01400 -- so using it gives every zone an id that is
// unique, stable across restarts and DHCP changes, and not tied to whatever
// NIC the bridge happens to run on. That matters because the host has one NIC
// but advertises many targets, and because macOS rotates its MAC per network.
//
// The Apple-Response must be signed with this same value: the mDNS instance
// name is the only place a sender can learn our "hardware" address.
func (p Player) RAOPID() (net.HardwareAddr, bool) {
	u := p.UUID()
	s, ok := strings.CutPrefix(u, "RINCON_")
	if !ok || len(s) < 12 {
		return nil, false
	}
	mac, err := net.ParseMAC(insertColons(s[:12]))
	if err != nil {
		return nil, false
	}
	return mac, true
}

func insertColons(hex12 string) string {
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hex12[i : i+2])
	}
	return b.String()
}

// Group is one zone group. Transport commands must go to the coordinator;
// sending them to a member is silently ignored.
type Group struct {
	ID          string
	Coordinator string // UUID
	Members     []GroupMember
}

type GroupMember struct {
	UUID     string
	ZoneName string
	IP       string
}

// Discover runs an SSDP M-SEARCH and describes every responder.
func Discover(ctx context.Context, wait time.Duration) ([]Player, error) {
	conn, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	dst, err := net.ResolveUDPAddr("udp4", ssdpMulticast)
	if err != nil {
		return nil, err
	}

	msearch := strings.Join([]string{
		"M-SEARCH * HTTP/1.1",
		"HOST: " + ssdpMulticast,
		`MAN: "ssdp:discover"`,
		"MX: 1",
		"ST: " + ssdpTarget,
		"", "",
	}, "\r\n")

	// Sent three times because multicast drops are routine.
	for i := 0; i < 3; i++ {
		if _, err := conn.WriteTo([]byte(msearch), dst); err != nil {
			return nil, fmt.Errorf("sending M-SEARCH: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}

	seen := map[string]bool{}
	var ips []string
	_ = conn.SetReadDeadline(time.Now().Add(wait))
	buf := make([]byte, 2048)
	for {
		n, from, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		if !bytes.Contains(buf[:n], []byte("ZonePlayer")) {
			continue
		}
		host, _, _ := net.SplitHostPort(from.String())
		if host != "" && !seen[host] {
			seen[host] = true
			ips = append(ips, host)
		}
	}
	sort.Strings(ips)

	return DescribeAll(ctx, ips), nil
}

// DescribeAll fetches descriptions for explicitly named hosts, skipping any
// that fail rather than failing the whole set.
func DescribeAll(ctx context.Context, ips []string) []Player {
	var players []Player
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		p, err := Describe(ctx, ip)
		if err != nil {
			continue
		}
		players = append(players, p)
	}
	return players
}

// Describe fetches the UPnP device description.
func Describe(ctx context.Context, ip string) (Player, error) {
	u := fmt.Sprintf("http://%s:%d/xml/device_description.xml", ip, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Player{}, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return Player{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Player{}, err
	}

	var doc struct {
		Device struct {
			RoomName  string `xml:"roomName"`
			ModelName string `xml:"modelName"`
			UDN       string `xml:"UDN"`
		} `xml:"device"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return Player{}, err
	}
	return Player{
		IP:        ip,
		RoomName:  doc.Device.RoomName,
		ModelName: doc.Device.ModelName,
		UDN:       doc.Device.UDN,
	}, nil
}

// Topology reads the zone groups. Any player can answer; the response
// describes the whole household.
func Topology(ctx context.Context, ip string) ([]Group, error) {
	body, err := soap(ctx, ip, topologyPath, topologyType, "GetZoneGroupState", "")
	if err != nil {
		return nil, err
	}

	var outer struct {
		State string `xml:"Body>GetZoneGroupStateResponse>ZoneGroupState"`
	}
	if err := xml.Unmarshal([]byte(body), &outer); err != nil {
		return nil, fmt.Errorf("parsing envelope: %w", err)
	}

	var state struct {
		Groups []struct {
			Coordinator string `xml:"Coordinator,attr"`
			ID          string `xml:"ID,attr"`
			Members     []struct {
				UUID     string `xml:"UUID,attr"`
				Location string `xml:"Location,attr"`
				ZoneName string `xml:"ZoneName,attr"`
			} `xml:"ZoneGroupMember"`
		} `xml:"ZoneGroups>ZoneGroup"`
	}
	if err := xml.Unmarshal([]byte(outer.State), &state); err != nil {
		return nil, fmt.Errorf("parsing ZoneGroupState: %w", err)
	}

	groups := make([]Group, 0, len(state.Groups))
	for _, g := range state.Groups {
		out := Group{ID: g.ID, Coordinator: g.Coordinator}
		for _, m := range g.Members {
			host := ""
			if u, err := url.Parse(m.Location); err == nil {
				host = u.Hostname()
			}
			out.Members = append(out.Members, GroupMember{
				UUID: m.UUID, ZoneName: m.ZoneName, IP: host,
			})
		}
		groups = append(groups, out)
	}
	return groups, nil
}

// SetStreamAndPlay points a zone at a URL and starts it.
//
// The URI is handed over as plain http://. The x-rincon-mp3radio:// scheme
// puts the player into Shoutcast mode, where it expects MP3 framing and tears
// an endless WAV down after about eleven seconds -- measured, not assumed.
func SetStreamAndPlay(ctx context.Context, ip, streamURL, title string) error {
	args := "<InstanceID>0</InstanceID>" +
		"<CurrentURI>" + escape(streamURL) + "</CurrentURI>" +
		"<CurrentURIMetaData>" + escape(didl(title)) + "</CurrentURIMetaData>"
	if _, err := soap(ctx, ip, avTransportPath, avTransportType, "SetAVTransportURI", args); err != nil {
		return fmt.Errorf("SetAVTransportURI: %w", err)
	}
	if _, err := soap(ctx, ip, avTransportPath, avTransportType, "Play",
		"<InstanceID>0</InstanceID><Speed>1</Speed>"); err != nil {
		return fmt.Errorf("Play: %w", err)
	}
	return nil
}

// Stop halts playback.
func Stop(ctx context.Context, ip string) error {
	_, err := soap(ctx, ip, avTransportPath, avTransportType, "Stop",
		"<InstanceID>0</InstanceID><Speed>1</Speed>")
	return err
}

// TransportState returns e.g. PLAYING, STOPPED, TRANSITIONING.
func TransportState(ctx context.Context, ip string) (string, error) {
	body, err := soap(ctx, ip, avTransportPath, avTransportType, "GetTransportInfo",
		"<InstanceID>0</InstanceID>")
	if err != nil {
		return "", err
	}
	var doc struct {
		State string `xml:"Body>GetTransportInfoResponse>CurrentTransportState"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		return "", err
	}
	return doc.State, nil
}

// SetVolume sets the master volume, 0-100.
func SetVolume(ctx context.Context, ip string, vol int) error {
	if vol < 0 {
		vol = 0
	}
	if vol > 100 {
		vol = 100
	}
	_, err := soap(ctx, ip, renderingPath, renderingType, "SetVolume",
		"<InstanceID>0</InstanceID><Channel>Master</Channel><DesiredVolume>"+
			strconv.Itoa(vol)+"</DesiredVolume>")
	return err
}

// Volume reads the master volume.
func Volume(ctx context.Context, ip string) (int, error) {
	body, err := soap(ctx, ip, renderingPath, renderingType, "GetVolume",
		"<InstanceID>0</InstanceID><Channel>Master</Channel>")
	if err != nil {
		return 0, err
	}
	var doc struct {
		Vol int `xml:"Body>GetVolumeResponse>CurrentVolume"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		return 0, err
	}
	return doc.Vol, nil
}

func soap(ctx context.Context, ip, path, serviceType, action, args string) (string, error) {
	envelope := `<?xml version="1.0" encoding="utf-8"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>` +
		`<u:` + action + ` xmlns:u="` + serviceType + `">` + args + `</u:` + action + `>` +
		`</s:Body></s:Envelope>`

	u := fmt.Sprintf("http://%s:%d%s", ip, port, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(envelope))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPACTION", `"`+serviceType+"#"+action+`"`)

	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s -> HTTP %d: %s", action, resp.StatusCode, squeeze(string(body)))
	}
	return string(body), nil
}

func didl(title string) string {
	return `<DIDL-Lite xmlns:dc="http://purl.org/dc/elements/1.1/" ` +
		`xmlns:upnp="urn:schemas-upnp-org:metadata-1-0/upnp/" ` +
		`xmlns="urn:schemas-upnp-org:metadata-1-0/DIDL-Lite/">` +
		`<item id="R:0/0/0" parentID="R:0/0" restricted="true">` +
		`<dc:title>` + escape(title) + `</dc:title>` +
		`<upnp:class>object.item.audioItem.audioBroadcast</upnp:class>` +
		`<desc id="cdudn" nameSpace="urn:schemas-rinconnetworks-com:metadata-1-0/">` +
		`SA_RINCON65031_</desc></item></DIDL-Lite>`
}

func escape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func squeeze(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}
