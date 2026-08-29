// Command sonospoof presents each Sonos zone as its own AirPlay target.
//
//	sonospoof                      # every discovered zone
//	sonospoof -zone Garage         # just one, for bringing the pipeline up
//	sonospoof -hosts 192.168.30.244  # skip SSDP, name the players directly
//
// SSDP is link-scoped, so -hosts exists for the case where the daemon is not
// on the speakers' subnet. Note that being off their subnet usually breaks
// more than discovery: the speaker has to open a connection back to us to
// fetch its audio, and a segmented network typically permits the outbound
// direction only. See CLAUDE.md.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sudarkoff/sonospoof/internal/bridge"
	"github.com/sudarkoff/sonospoof/internal/sonos"
)

func main() {
	log.SetFlags(log.Ltime)

	var (
		only       = flag.String("zone", "", "only bridge this zone (default: all)")
		hosts      = flag.String("hosts", "", "comma-separated ZonePlayer IPs; skips SSDP")
		iface      = flag.String("iface", "", "interface to advertise on (default: all)")
		streamPort = flag.Int("stream-port", 0, "HTTP port for audio streams (0 = any)")
		wait       = flag.Duration("wait", 3*time.Second, "SSDP listen time")
		dumpDir    = flag.String("dump", "", "write each stream connection to a .wav here, for diagnosing audio faults")
	)
	flag.Parse()
	bridge.DumpDir = *dumpDir

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	zones, err := discover(ctx, *hosts, *wait, *only)
	if err != nil {
		log.Fatal(err)
	}

	// The speaker fetches audio from us, so the URL must carry an address it
	// can route to. Derive it from the route to the first zone rather than
	// guessing an interface.
	localIP, err := bridge.LocalIPFor(zones[0].IP)
	if err != nil {
		log.Fatalf("determining our address: %v", err)
	}

	streamLn, err := net.Listen("tcp", fmt.Sprintf(":%d", *streamPort))
	if err != nil {
		log.Fatalf("stream listener: %v", err)
	}
	streamHost := net.JoinHostPort(localIP.String(), fmt.Sprint(streamLn.Addr().(*net.TCPAddr).Port))

	adv, err := bridge.NewAdvertiser(*iface)
	if err != nil {
		log.Fatalf("mDNS: %v", err)
	}

	mux := http.NewServeMux()
	var bridges []*bridge.Bridge

	for _, z := range zones {
		b := bridge.New(z, streamHost)

		port, err := b.Receiver().Listen(0)
		if err != nil {
			log.Fatalf("%s: rtsp listen: %v", z.Name, err)
		}
		if err := adv.Add(z.Name, z.RAOPID, port); err != nil {
			log.Fatalf("%s: %v", z.Name, err)
		}

		// Each zone owns a path prefix; the nonce inside it changes per
		// session so Sonos cannot serve a cached stream.
		mux.HandleFunc("/zone/"+z.CoordinatorUUID+"/", b.ServeStream)

		go func(b *bridge.Bridge, name string) {
			if err := b.Receiver().Serve(); err != nil {
				log.Printf("%s: rtsp serve stopped: %v", name, err)
			}
		}(b, z.Name)

		bridges = append(bridges, b)

		grouped := ""
		if z.Grouped() {
			grouped = fmt.Sprintf("  [group: %s]", strings.Join(z.Members, ", "))
		}
		log.Printf("target %-18s rtsp :%-5d -> %s%s", z.Name, port, z.IP, grouped)
	}

	srv := &http.Server{Handler: mux}
	go func() {
		if err := srv.Serve(streamLn); err != nil && err != http.ErrServerClosed {
			log.Printf("stream server: %v", err)
		}
	}()

	go func() {
		if err := adv.Run(ctx); err != nil && ctx.Err() == nil {
			log.Printf("mDNS responder stopped: %v", err)
		}
	}()

	log.Printf("streaming from http://%s/ -- %d target(s) advertised", streamHost, len(bridges))

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	log.Printf("shutting down")
	cancel()
	for _, b := range bridges {
		_ = b.Receiver().Close()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = srv.Shutdown(shutdownCtx)
}

// discover finds the zones to advertise, honouring -hosts and -zone.
func discover(ctx context.Context, hosts string, wait time.Duration, only string) ([]sonos.Zone, error) {
	var players []sonos.Player
	if hosts != "" {
		players = sonos.DescribeAll(ctx, strings.Split(hosts, ","))
	} else {
		var err error
		players, err = sonos.Discover(ctx, wait)
		if err != nil {
			return nil, fmt.Errorf("discovery: %w", err)
		}
	}
	if len(players) == 0 {
		return nil, fmt.Errorf("no ZonePlayers found.\n" +
			"SSDP is link-scoped multicast and is not reflected between VLANs the\n" +
			"way mDNS is, so it finds nothing from another subnet even when unicast\n" +
			"to port 1400 works. Locate the speakers with\n" +
			"  dns-sd -B _sonos._tcp        (macOS)\n" +
			"  avahi-browse -rt _sonos._tcp (Linux)\n" +
			"and pass them with -hosts -- but note the speakers must also be able to\n" +
			"open connections back to this host to fetch audio.")
	}

	groups, err := sonos.Topology(ctx, players[0].IP)
	if err != nil {
		// Without topology we cannot tell grouped speakers apart, and would
		// offer independent targets the hardware will not honour.
		return nil, fmt.Errorf("reading zone topology: %w", err)
	}

	zones := sonos.Zones(players, groups)
	if only != "" {
		var kept []sonos.Zone
		for _, z := range zones {
			if strings.EqualFold(z.Name, only) {
				kept = append(kept, z)
			}
		}
		if len(kept) == 0 {
			return nil, fmt.Errorf("no zone named %q among %s", only, names(zones))
		}
		zones = kept
	}
	if len(zones) == 0 {
		return nil, fmt.Errorf("no zones to advertise")
	}
	return zones, nil
}

func names(zones []sonos.Zone) string {
	var n []string
	for _, z := range zones {
		n = append(n, z.Name)
	}
	return strings.Join(n, ", ")
}
