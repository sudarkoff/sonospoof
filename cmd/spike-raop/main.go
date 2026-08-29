// Command spike-raop is a throwaway M0 probe for the Apple half of the pipeline.
//
// It advertises a fake AirPlay 1 (RAOP) receiver over mDNS and dumps every byte
// of the RTSP conversation that follows. It deliberately does NOT implement the
// handshake -- the point is reconnaissance:
//
//  1. Does a current iOS/macOS device even list us as an AirPlay target?
//  2. Which auth path does it pick? Legacy RSA (an "Apple-Challenge" header on
//     OPTIONS, answerable with the leaked AirPort Express key) or FairPlay
//     (a POST to /fp-setup, which is a different and much bigger fight)?
//  3. What does the ANNOUNCE SDP actually declare -- codec, framing, AES key?
//
// That third answer is what the real decoder gets written against. The TXT
// records below are the single most likely thing to be wrong; -et and -cn exist
// so we can bisect them against a device that refuses to connect.
//
// Usage:
//
//	spike-raop -name "Kitchen (spoof)" -iface eth0
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/brutella/dnssd"

	"github.com/sudarkoff/sonospoof/internal/raop"
)

func main() {
	log.SetFlags(log.Ltime)

	var (
		name  = flag.String("name", "sonospoof probe", "AirPlay device name shown on the iPhone")
		port  = flag.Int("port", 5000, "RTSP listen port")
		iface = flag.String("iface", "", "interface to advertise on (default: first non-loopback up iface)")
		et    = flag.String("et", "0,1", "TXT et= encryption types (0=none, 1=RSA, 3=FairPlay)")
		cn    = flag.String("cn", "0,1", "TXT cn= codecs (0=PCM, 1=ALAC, 2=AAC)")
	)
	flag.Parse()

	nic, err := pickInterface(*iface)
	if err != nil {
		log.Fatalf("interface: %v", err)
	}
	mac := macHex(nic)
	log.Printf("advertising on %s (mac-ish id %s)", nic.Name, mac)

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("listening on :%d: %v", *port, err)
	}
	defer ln.Close()

	// RAOP requires the mDNS instance name to be "<12 hex>@<display name>".
	// Anything else and the device is either invisible or unselectable.
	cfg := dnssd.Config{
		Name:   mac + "@" + *name,
		Type:   "_raop._tcp",
		Domain: "local",
		Port:   *port,
		Ifaces: []string{nic.Name},
		Text: map[string]string{
			"txtvers": "1",
			"ch":      "2", // channels
			"cn":      *cn, // codecs supported
			"et":      *et, // encryption types supported
			"sv":      "false",
			"da":      "true",
			"sr":      "44100", // sample rate
			"ss":      "16",    // sample size
			"pw":      "false", // password required
			"vn":      "65537",
			"tp":      "UDP",   // transport for the audio stream
			"vs":      "105.1", // "AirTunes" version; iOS gates behavior on this
			"am":      "AirPort4,107",
			"md":      "0,1,2", // metadata: text, artwork, progress
			"sf":      "0x4",
		},
	}

	svc, err := dnssd.NewService(cfg)
	if err != nil {
		log.Fatalf("building mDNS service: %v", err)
	}
	responder, err := dnssd.NewResponder()
	if err != nil {
		log.Fatalf("mDNS responder: %v", err)
	}
	if _, err := responder.Add(svc); err != nil {
		log.Fatalf("registering service: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		log.Printf("shutting down")
		cancel()
		ln.Close()
	}()

	go func() {
		if err := responder.Respond(ctx); err != nil && ctx.Err() == nil {
			log.Printf("mDNS responder stopped: %v", err)
		}
	}()

	fmt.Printf("\n  Advertised as %q on %s:%d\n", *name, nic.Name, *port)
	fmt.Printf("  Now pick it from the AirPlay menu on an iPhone or Mac.\n")
	fmt.Printf("  Everything that arrives gets dumped below. Ctrl-C to stop.\n\n")

	var sessions int64
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("accept: %v", err)
			continue
		}
		go handle(conn, atomic.AddInt64(&sessions, 1), nic.HardwareAddr)
	}
}

// handle dumps one RTSP session. It answers just enough to keep the client
// talking, which -- as M0 established -- means answering the Apple-Challenge:
// iOS will not send ANNOUNCE without a valid Apple-Response.
func handle(conn net.Conn, id int64, mac net.HardwareAddr) {
	defer conn.Close()
	peer := conn.RemoteAddr().String()
	log.Printf("[%d] connection from %s", id, peer)
	defer log.Printf("[%d] closed", id)

	br := bufio.NewReader(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Minute))

		line, err := br.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				log.Printf("[%d] read: %v", id, err)
			}
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}

		method, target := splitRequestLine(line)
		headers, err := readHeaders(br)
		if err != nil {
			log.Printf("[%d] headers: %v", id, err)
			return
		}

		var body []byte
		if n, _ := strconv.Atoi(headers["content-length"]); n > 0 {
			body = make([]byte, n)
			if _, err := io.ReadFull(br, body); err != nil {
				log.Printf("[%d] body: %v", id, err)
				return
			}
		}

		dumpRequest(id, line, headers, body)
		flagAuthPath(id, method, target, headers)

		if err := respond(conn, method, headers, mac, id); err != nil {
			log.Printf("[%d] write: %v", id, err)
			return
		}
	}
}

// flagAuthPath calls out which of the two handshakes the client chose, since
// that decides how much work the real receiver is.
func flagAuthPath(id int64, method, target string, headers map[string]string) {
	if c, ok := headers["apple-challenge"]; ok {
		log.Printf("[%d] >>> LEGACY RSA PATH: Apple-Challenge=%s", id, c)
		log.Printf("[%d]     Answerable with the leaked AirPort Express key. This is the good case.", id)
	}
	if strings.Contains(target, "fp-setup") || strings.Contains(headers["content-type"], "fairplay") {
		log.Printf("[%d] >>> FAIRPLAY PATH (%s %s)", id, method, target)
		log.Printf("[%d]     Client rejected legacy auth. Try different -et/-vs TXT values.", id)
	}
}

func dumpRequest(id int64, requestLine string, headers map[string]string, body []byte) {
	fmt.Printf("\n--- [%d] %s\n", id, requestLine)
	for _, k := range sortedKeys(headers) {
		fmt.Printf("      %s: %s\n", k, headers[k])
	}
	if len(body) == 0 {
		return
	}
	ct := headers["content-type"]
	// SDP from ANNOUNCE is the prize: it carries the codec params and the
	// RSA-wrapped AES key, so print it readably rather than as hex.
	if strings.Contains(ct, "sdp") || isPrintable(body) {
		fmt.Printf("      body (%s, %d bytes):\n", ct, len(body))
		for _, l := range strings.Split(strings.TrimRight(string(body), "\r\n"), "\n") {
			fmt.Printf("        %s\n", strings.TrimRight(l, "\r"))
		}
		return
	}
	fmt.Printf("      body (%s, %d bytes):\n%s", ct, len(body), indentHex(body))
}

// respond sends the minimum that keeps a client progressing through the
// handshake, so we can observe as many request types as possible.
func respond(conn net.Conn, method string, headers map[string]string, mac net.HardwareAddr, id int64) error {
	var extra []string

	// Answering the challenge is not optional. M0 showed both that iOS quits
	// after OPTIONS without an Apple-Response, and that advertising et=0 to
	// dodge the challenge just makes it quit without asking.
	if ch, ok := headers["apple-challenge"]; ok {
		local, _ := conn.LocalAddr().(*net.TCPAddr)
		if local == nil {
			log.Printf("[%d] cannot answer challenge: local address is not TCP", id)
		} else if resp, err := raop.AppleResponse(ch, local.IP, mac); err != nil {
			log.Printf("[%d] answering Apple-Challenge: %v", id, err)
		} else {
			extra = append(extra, "Apple-Response: "+resp)
			log.Printf("[%d] answered Apple-Challenge (local %s, mac %s)", id, local.IP, mac)
		}
	}

	switch method {
	case "OPTIONS":
		extra = append(extra, "Public: ANNOUNCE, SETUP, RECORD, PAUSE, FLUSH, TEARDOWN, OPTIONS, GET_PARAMETER, SET_PARAMETER")
	case "SETUP":
		// Fake ports. A real receiver would bind these and return the real ones;
		// returning anything lets us see whether the client proceeds to RECORD.
		extra = append(extra, "Transport: RTP/AVP/UDP;unicast;mode=record;server_port=6000;control_port=6001;timing_port=6002")
		extra = append(extra, "Session: 1")
	}

	resp := "RTSP/1.0 200 OK\r\n" +
		"Server: AirTunes/105.1\r\n" +
		"CSeq: " + headers["cseq"] + "\r\n" +
		"Audio-Jack-Status: connected; type=analog\r\n"
	for _, e := range extra {
		resp += e + "\r\n"
	}
	resp += "\r\n"

	_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := io.WriteString(conn, resp)
	return err
}

// ---------------------------------------------------------------- helpers

func splitRequestLine(line string) (method, target string) {
	parts := strings.Fields(line)
	if len(parts) >= 2 {
		return parts[0], parts[1]
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return "", ""
}

func readHeaders(br *bufio.Reader) (map[string]string, error) {
	headers := map[string]string{}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return headers, nil
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Insertion sort; the map is tiny and this avoids another import.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func isPrintable(b []byte) bool {
	for _, c := range b {
		if c < 0x20 && c != '\r' && c != '\n' && c != '\t' {
			return false
		}
	}
	return true
}

func indentHex(b []byte) string {
	var sb strings.Builder
	for _, line := range strings.Split(strings.TrimRight(hex.Dump(b), "\n"), "\n") {
		sb.WriteString("        " + line + "\n")
	}
	return sb.String()
}

// pickInterface returns the named interface, or the first up, non-loopback one
// carrying an IPv4 address. Guessing wrong here means the service is advertised
// where nobody is listening, so the choice is logged.
func pickInterface(name string) (*net.Interface, error) {
	if name != "" {
		return net.InterfaceByName(name)
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for i := range ifaces {
		nic := &ifaces[i]
		if nic.Flags&net.FlagUp == 0 || nic.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := nic.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil {
				return nic, nil
			}
		}
	}
	return nil, fmt.Errorf("no suitable interface found; pass -iface")
}

// macHex renders the interface MAC as the 12 uppercase hex chars RAOP wants,
// falling back to random bytes for interfaces without one.
func macHex(nic *net.Interface) string {
	if len(nic.HardwareAddr) == 6 {
		return strings.ToUpper(hex.EncodeToString(nic.HardwareAddr))
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}
