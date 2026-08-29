// Command spike-sonos is a throwaway M0 probe for the Sonos half of the pipeline.
//
// It answers three questions before we commit to any structure:
//
//  1. Can we find Gen1 ZonePlayers on this LAN via SSDP?
//  2. Can we read the zone/group topology, so we know which player is the
//     coordinator we must actually talk to?
//  3. Will a zone play an arbitrary HTTP URL we hand it, and does the
//     x-rincon-mp3radio:// scheme behave better than plain http:// for an
//     endless stream?
//
// Nothing here is meant to survive into the real daemon. Stdlib only.
//
// Usage:
//
//	spike-sonos                                  # discover + dump topology
//	spike-sonos -play <url> -zone "Kitchen"      # push a stream to one zone
//	spike-sonos -play <url> -zone "Kitchen" -raw # ...as plain http://
//	spike-sonos -stop -zone "Kitchen"
//
//	spike-sonos -hosts 192.168.30.252,192.168.30.244   # skip SSDP entirely
//	spike-sonos -src 192.168.20.20                     # pin the egress iface
//
// -hosts exists because SSDP is link-scoped: it does not cross a VLAN
// boundary, and no consumer router reflects it (UniFi reflects mDNS only).
// When the speakers and this host are on different subnets, M-SEARCH is
// simply never answered while unicast HTTP to port 1400 works fine, so
// naming the players directly is the only way to reach them.
package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	ssdpMulticast = "239.255.255.250:1900"
	ssdpTarget    = "urn:schemas-upnp-org:device:ZonePlayer:1"
	sonosPort     = 1400

	avTransportPath = "/MediaRenderer/AVTransport/Control"
	avTransportType = "urn:schemas-upnp-org:service:AVTransport:1"
	topologyPath    = "/ZoneGroupTopology/Control"
	topologyType    = "urn:schemas-upnp-org:service:ZoneGroupTopology:1"
)

func main() {
	log.SetFlags(0)

	var (
		playURL  = flag.String("play", "", "stream URL to push to the target zone")
		zoneName = flag.String("zone", "", "target zone (room) name; required with -play/-stop")
		raw      = flag.Bool("raw", false, "use plain http:// instead of x-rincon-mp3radio://")
		stop     = flag.Bool("stop", false, "send Stop to the target zone")
		waitFor  = flag.Duration("wait", 3*time.Second, "how long to listen for SSDP replies")
		hosts    = flag.String("hosts", "", "comma-separated ZonePlayer IPs; skips SSDP discovery")
		srcIP    = flag.String("src", "", "local IP to send SSDP from, to pin the egress interface")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var players []player
	var err error
	if *hosts != "" {
		players = describeAll(ctx, strings.Split(*hosts, ","))
	} else {
		players, err = discover(ctx, *waitFor, *srcIP)
		if err != nil {
			log.Fatalf("discovery failed: %v", err)
		}
	}
	if len(players) == 0 {
		log.Fatalf("no ZonePlayers found.\n" +
			"SSDP is link-scoped multicast: it does not cross a VLAN boundary and is\n" +
			"not reflected by consumer routers the way mDNS is. If the speakers sit on\n" +
			"a different subnet than this host, M-SEARCH will never be answered even\n" +
			"though unicast HTTP to port 1400 works. Find them with\n" +
			"  dns-sd -B _sonos._tcp        (macOS)\n" +
			"  avahi-browse -rt _sonos._tcp (Linux)\n" +
			"and pass the addresses with -hosts, or put this host on the speakers' VLAN.")
	}

	fmt.Printf("\n=== Discovered %d ZonePlayer(s) ===\n", len(players))
	for _, p := range players {
		fmt.Printf("  %-16s %-22s %s\n", p.IP, p.RoomName, p.ModelName)
	}

	// Topology tells us the coordinator per group. Sending transport commands to
	// a non-coordinator member is silently ignored, so this is load-bearing.
	fmt.Printf("\n=== Zone group topology ===\n")
	if err := dumpTopology(ctx, players[0].IP); err != nil {
		log.Printf("  topology query failed: %v", err)
	}

	if *playURL == "" && !*stop {
		fmt.Printf("\nNo action requested. Re-run with -play <url> -zone <name> to test playback.\n")
		return
	}

	if *zoneName == "" {
		log.Fatalf("-zone is required with -play/-stop")
	}
	target := findZone(players, *zoneName)
	if target == nil {
		log.Fatalf("no zone named %q among the discovered players", *zoneName)
	}

	if *stop {
		if _, err := soap(ctx, target.IP, avTransportPath, avTransportType, "Stop",
			"<InstanceID>0</InstanceID><Speed>1</Speed>"); err != nil {
			log.Fatalf("Stop failed: %v", err)
		}
		fmt.Printf("\nStop sent to %s (%s)\n", target.RoomName, target.IP)
		return
	}

	uri := *playURL
	if !*raw {
		uri = toRinconRadio(uri)
	}
	fmt.Printf("\n=== Pushing to %s (%s) ===\n  URI: %s\n", target.RoomName, target.IP, uri)

	args := "<InstanceID>0</InstanceID>" +
		"<CurrentURI>" + escape(uri) + "</CurrentURI>" +
		"<CurrentURIMetaData>" + escape(radioDIDL("sonospoof probe")) + "</CurrentURIMetaData>"
	if _, err := soap(ctx, target.IP, avTransportPath, avTransportType, "SetAVTransportURI", args); err != nil {
		log.Fatalf("SetAVTransportURI failed: %v", err)
	}
	if _, err := soap(ctx, target.IP, avTransportPath, avTransportType, "Play",
		"<InstanceID>0</InstanceID><Speed>1</Speed>"); err != nil {
		log.Fatalf("Play failed: %v", err)
	}
	fmt.Printf("  OK -- you should hear audio. Stop with: spike-sonos -stop -zone %q\n", target.RoomName)
}

// ---------------------------------------------------------------- discovery

type player struct {
	IP        string
	RoomName  string
	ModelName string
	UDN       string
}

// describeAll fetches the device description for each explicitly named host.
// This is the path that works across a subnet boundary, where SSDP cannot go.
func describeAll(ctx context.Context, ips []string) []player {
	var players []player
	for _, ip := range ips {
		ip = strings.TrimSpace(ip)
		if ip == "" {
			continue
		}
		p, err := describe(ctx, ip)
		if err != nil {
			log.Printf("  %s: description fetch failed: %v", ip, err)
			continue
		}
		players = append(players, p)
	}
	return players
}

// discover runs an SSDP M-SEARCH for ZonePlayers and describes each responder.
// src, when set, is the local address to bind to; on a multi-homed host that
// is what decides which interface the multicast actually leaves by.
func discover(ctx context.Context, wait time.Duration, src string) ([]player, error) {
	conn, err := net.ListenPacket("udp4", net.JoinHostPort(src, "0"))
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	dst, err := net.ResolveUDPAddr("udp4", ssdpMulticast)
	if err != nil {
		return nil, err
	}

	// MX is the max jitter the device may wait before replying; keep it under
	// our read deadline. Sent three times because UDP multicast drops happen.
	msearch := strings.Join([]string{
		"M-SEARCH * HTTP/1.1",
		"HOST: " + ssdpMulticast,
		"MAN: \"ssdp:discover\"",
		"MX: 1",
		"ST: " + ssdpTarget,
		"", "",
	}, "\r\n")

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
			break // deadline
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

	var players []player
	for _, ip := range ips {
		p, err := describe(ctx, ip)
		if err != nil {
			log.Printf("  %s: description fetch failed: %v", ip, err)
			continue
		}
		players = append(players, p)
	}
	return players, nil
}

// describe fetches the UPnP device description to learn the room name.
func describe(ctx context.Context, ip string) (player, error) {
	u := fmt.Sprintf("http://%s:%d/xml/device_description.xml", ip, sonosPort)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return player{}, err
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return player{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return player{}, err
	}

	var doc struct {
		Device struct {
			RoomName  string `xml:"roomName"`
			ModelName string `xml:"modelName"`
			UDN       string `xml:"UDN"`
		} `xml:"device"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil {
		return player{}, err
	}
	return player{
		IP:        ip,
		RoomName:  doc.Device.RoomName,
		ModelName: doc.Device.ModelName,
		UDN:       doc.Device.UDN,
	}, nil
}

func findZone(players []player, name string) *player {
	for i := range players {
		if strings.EqualFold(players[i].RoomName, name) {
			return &players[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------- topology

// dumpTopology prints each zone group and marks its coordinator. The response
// carries the real payload as an XML-escaped string inside ZoneGroupState, so
// this is a two-stage unmarshal.
func dumpTopology(ctx context.Context, ip string) error {
	body, err := soap(ctx, ip, topologyPath, topologyType, "GetZoneGroupState", "")
	if err != nil {
		return err
	}

	var outer struct {
		State string `xml:"Body>GetZoneGroupStateResponse>ZoneGroupState"`
	}
	if err := xml.Unmarshal([]byte(body), &outer); err != nil {
		return fmt.Errorf("parsing envelope: %w", err)
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
		return fmt.Errorf("parsing ZoneGroupState: %w", err)
	}

	for _, g := range state.Groups {
		fmt.Printf("  group %s\n", g.ID)
		for _, m := range g.Members {
			role := "member"
			if m.UUID == g.Coordinator {
				role = "COORDINATOR"
			}
			host := ""
			if u, err := url.Parse(m.Location); err == nil {
				host = u.Hostname()
			}
			fmt.Printf("    %-11s %-22s %s\n", role, m.ZoneName, host)
		}
	}
	return nil
}

// ---------------------------------------------------------------- SOAP

// soap issues one UPnP action against a ZonePlayer and returns the raw body.
func soap(ctx context.Context, ip, path, serviceType, action, args string) (string, error) {
	envelope := `<?xml version="1.0" encoding="utf-8"?>` +
		`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" ` +
		`s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/"><s:Body>` +
		`<u:` + action + ` xmlns:u="` + serviceType + `">` + args + `</u:` + action + `>` +
		`</s:Body></s:Envelope>`

	u := fmt.Sprintf("http://%s:%d%s", ip, sonosPort, path)
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
		// UPnP faults carry a errorCode in the body; surfacing it beats the status.
		return "", fmt.Errorf("%s -> HTTP %d: %s", action, resp.StatusCode, squeeze(string(body)))
	}
	return string(body), nil
}

// ---------------------------------------------------------------- helpers

// toRinconRadio rewrites http(s):// to Sonos's radio scheme, which marks the
// stream as endless and non-seekable. Whether this beats plain http:// for our
// case is exactly what the probe is here to find out.
func toRinconRadio(u string) string {
	if s, ok := strings.CutPrefix(u, "http://"); ok {
		return "x-rincon-mp3radio://" + s
	}
	if s, ok := strings.CutPrefix(u, "https://"); ok {
		return "x-rincon-mp3radio://" + s
	}
	return u
}

func radioDIDL(title string) string {
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
