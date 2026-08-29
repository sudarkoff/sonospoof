package raop

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sudarkoff/sonospoof/internal/audio"
)

// Handler is what a receiver tells the rest of the program.
//
// Start is called on RECORD, once the session is fully negotiated and audio is
// about to flow; the returned error aborts the session. Stop is called on
// TEARDOWN or when the control connection drops. SetVolume carries AirPlay's
// dB value through unchanged so the mapping stays in one place.
type Handler interface {
	Start(sessionName string) error
	Stop()
	SetVolume(db float64)
}

// Receiver is one AirPlay target: one RTSP listener, one audio pipeline.
//
// One Receiver per Sonos zone. Nothing here is shared between zones -- the
// ALAC decoder carries adaptive state and the ring holds that zone's audio,
// so sharing either would cross the streams.
type Receiver struct {
	// Name is shown in the AirPlay menu.
	Name string
	// ID is this target's AirPlay hardware identity. The Apple-Response is
	// signed with it, because the mDNS instance name is the only place a
	// sender can learn our MAC.
	ID net.HardwareAddr

	Ring    *audio.Ring
	Handler Handler
	Logf    func(string, ...any)

	ln net.Listener

	mu          sync.Mutex
	dec         *AudioDecoder
	udp         []*net.UDPConn
	running     bool
	sessionName string // from the ANNOUNCE i= line, e.g. "G's iPhone"
}

// Listen binds the RTSP port. Passing port 0 lets the OS choose, which is what
// multi-zone wants: the real port goes out in the mDNS SRV record, so nothing
// needs a fixed number and three zones cannot collide.
func (r *Receiver) Listen(port int) (int, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		return 0, err
	}
	r.ln = ln
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// Serve accepts RTSP sessions until the listener is closed.
func (r *Receiver) Serve() error {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return err
		}
		go r.session(conn)
	}
}

// Close stops the listener and tears down any live session.
func (r *Receiver) Close() error {
	if r.ln != nil {
		_ = r.ln.Close()
	}
	r.teardown()
	return nil
}

func (r *Receiver) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}

func (r *Receiver) session(conn net.Conn) {
	defer conn.Close()
	defer r.teardown()

	br := bufio.NewReader(conn)
	for {
		// A sender holds the control connection open for the whole session
		// and can be quiet for long stretches between commands.
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Minute))

		req, err := readRequest(br)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				r.logf("%s: rtsp read: %v", r.Name, err)
			}
			return
		}

		resp := r.handle(conn, req)
		if err := writeResponse(conn, req, resp); err != nil {
			r.logf("%s: rtsp write: %v", r.Name, err)
			return
		}
	}
}

type request struct {
	Method  string
	Target  string
	Headers map[string]string
	Body    []byte
}

type response struct {
	Status  string
	Headers []string
}

func readRequest(br *bufio.Reader) (*request, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return nil, errors.New("raop: empty request line")
	}

	req := &request{Headers: map[string]string{}}
	if f := strings.Fields(line); len(f) >= 2 {
		req.Method, req.Target = f[0], f[1]
	} else {
		return nil, fmt.Errorf("raop: malformed request line %q", line)
	}

	for {
		h, err := br.ReadString('\n')
		if err != nil {
			return nil, err
		}
		h = strings.TrimRight(h, "\r\n")
		if h == "" {
			break
		}
		k, v, ok := strings.Cut(h, ":")
		if !ok {
			continue
		}
		req.Headers[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}

	if n, _ := strconv.Atoi(req.Headers["content-length"]); n > 0 {
		req.Body = make([]byte, n)
		if _, err := io.ReadFull(br, req.Body); err != nil {
			return nil, err
		}
	}
	return req, nil
}

func writeResponse(w io.Writer, req *request, resp *response) error {
	var b strings.Builder
	b.WriteString("RTSP/1.0 " + resp.Status + "\r\n")
	b.WriteString("Server: AirTunes/105.1\r\n")
	b.WriteString("CSeq: " + req.Headers["cseq"] + "\r\n")
	for _, h := range resp.Headers {
		b.WriteString(h + "\r\n")
	}
	b.WriteString("\r\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func (r *Receiver) handle(conn net.Conn, req *request) *response {
	resp := &response{Status: "200 OK"}

	// Answer the challenge on every request that carries one. Without a valid
	// Apple-Response iOS will not proceed past OPTIONS -- established in M0,
	// including that advertising et=0 does not avoid it.
	if ch, ok := req.Headers["apple-challenge"]; ok {
		if local, ok := conn.LocalAddr().(*net.TCPAddr); ok {
			// The local address must come from this connection: senders sign
			// the address they dialled, and iOS reaches us on IPv6
			// link-local, where the block is 38 bytes and unpadded.
			if sig, err := AppleResponse(ch, local.IP, r.ID); err != nil {
				r.logf("%s: apple-response: %v", r.Name, err)
			} else {
				resp.Headers = append(resp.Headers, "Apple-Response: "+sig)
			}
		}
	}

	switch req.Method {
	case "OPTIONS":
		resp.Headers = append(resp.Headers,
			"Public: ANNOUNCE, SETUP, RECORD, PAUSE, FLUSH, TEARDOWN, OPTIONS, GET_PARAMETER, SET_PARAMETER")

	case "ANNOUNCE":
		if err := r.announce(req.Body); err != nil {
			r.logf("%s: ANNOUNCE: %v", r.Name, err)
			return &response{Status: "400 Bad Request"}
		}

	case "SETUP":
		transport, err := r.setup(req.Headers["transport"])
		if err != nil {
			r.logf("%s: SETUP: %v", r.Name, err)
			return &response{Status: "461 Unsupported Transport"}
		}
		resp.Headers = append(resp.Headers, transport, "Session: 1")

	case "RECORD":
		if err := r.record(); err != nil {
			r.logf("%s: RECORD: %v", r.Name, err)
			return &response{Status: "500 Internal Server Error"}
		}
		// Latency in frames, reported back to the sender.
		resp.Headers = append(resp.Headers, "Audio-Latency: 11025")

	case "FLUSH":
		// The sender seeked or paused; drop what we hold so the speaker does
		// not play stale audio when it resumes.
		r.Ring.Reset()

	case "TEARDOWN":
		r.teardown()

	case "SET_PARAMETER":
		r.setParameter(req.Body)

	case "GET_PARAMETER", "PAUSE":
		// Nothing to report; a bare 200 keeps the sender happy.

	default:
		return &response{Status: "501 Not Implemented"}
	}

	return resp
}

func (r *Receiver) announce(body []byte) error {
	sdp, err := ParseSDP(body)
	if err != nil {
		return err
	}
	if sdp.SessionKey == nil {
		return errors.New("raop: unencrypted sessions are not supported")
	}

	dec, err := NewAudioDecoder(sdp.SessionKey, sdp.IV, sdp.Config, r.Ring)
	if err != nil {
		return err
	}

	r.mu.Lock()
	r.dec = dec
	r.sessionName = sdp.Name
	r.mu.Unlock()

	r.logf("%s: session from %q, ALAC %d frames %d-bit %dch @ %dHz",
		r.Name, sdp.Name, sdp.Config.FrameLength, sdp.Config.BitDepth,
		sdp.Config.NumChannels, sdp.Config.SampleRate)
	return nil
}

// setup binds the three UDP ports and reports the real numbers back.
//
// The reference receivers bind fixed ports; we bind port 0 and advertise what
// the OS gave us, because several zones run in one process and fixed ports
// would collide on the second one.
func (r *Receiver) setup(transport string) (string, error) {
	if !strings.Contains(transport, "UDP") {
		return "", fmt.Errorf("unsupported transport %q", transport)
	}

	r.closeUDP()

	server, err := r.bindUDP(true)
	if err != nil {
		return "", err
	}
	control, err := r.bindUDP(true)
	if err != nil {
		return "", err
	}
	timing, err := r.bindUDP(false)
	if err != nil {
		return "", err
	}

	r.mu.Lock()
	r.udp = []*net.UDPConn{server, control, timing}
	r.mu.Unlock()

	return fmt.Sprintf(
		"Transport: RTP/AVP/UDP;unicast;mode=record;server_port=%d;control_port=%d;timing_port=%d",
		port(server), port(control), port(timing)), nil
}

// bindUDP opens an ephemeral UDP port. When receive is true a goroutine reads
// packets into the decoder; the timing port is bound but not read, since
// nothing in M1 needs the clock and an unbound port would make SETUP a lie.
func (r *Receiver) bindUDP(receive bool) (*net.UDPConn, error) {
	c, err := net.ListenUDP("udp", &net.UDPAddr{})
	if err != nil {
		return nil, err
	}
	if receive {
		go r.readPackets(c)
	}
	return c, nil
}

func port(c *net.UDPConn) int { return c.LocalAddr().(*net.UDPAddr).Port }

func (r *Receiver) readPackets(c *net.UDPConn) {
	// An ALAC frame of 352 stereo samples cannot exceed a couple of KiB, but
	// senders may use a larger MTU; 2048 covers it with room to spare.
	buf := make([]byte, 2048)
	for {
		n, _, err := c.ReadFromUDP(buf)
		if err != nil {
			return // closed
		}
		r.mu.Lock()
		dec := r.dec
		r.mu.Unlock()
		if dec == nil {
			continue
		}
		if err := dec.Packet(buf[:n]); err != nil {
			// A bad packet is a dropout, not a reason to end the session.
			r.logf("%s: packet: %v", r.Name, err)
		}
	}
}

func (r *Receiver) record() error {
	r.mu.Lock()
	name := r.sessionName
	already := r.running
	r.running = true
	r.mu.Unlock()

	if already {
		return nil
	}
	r.Ring.Reset()
	if r.Handler != nil {
		return r.Handler.Start(name)
	}
	return nil
}

func (r *Receiver) teardown() {
	r.mu.Lock()
	wasRunning := r.running
	r.running = false
	r.dec = nil
	r.mu.Unlock()

	r.closeUDP()
	r.Ring.Reset()

	if wasRunning && r.Handler != nil {
		r.Handler.Stop()
	}
}

func (r *Receiver) closeUDP() {
	r.mu.Lock()
	conns := r.udp
	r.udp = nil
	r.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

func (r *Receiver) setParameter(body []byte) {
	for _, line := range strings.Split(string(body), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || k != "volume" {
			continue
		}
		db, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			continue
		}
		if r.Handler != nil {
			r.Handler.SetVolume(db)
		}
	}
}

// VolumeToSonos converts AirPlay's dB volume to the 0-100 Sonos scale.
//
// AirPlay sends -144.0 for mute and otherwise a value in roughly -30..0 dB.
// This is a curve, not a rescale: mapping the full -144..0 range linearly
// would put normal listening levels in the bottom fifth of the dial.
func VolumeToSonos(db float64) int {
	if db <= -144 || math.IsInf(db, -1) {
		return 0
	}
	const floor = -30.0
	if db < floor {
		db = floor
	}
	if db > 0 {
		db = 0
	}
	return int(math.Round(100 * (db - floor) / -floor))
}
