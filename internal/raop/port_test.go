package raop

import "testing"

// The bug this prevents: senders cache the mDNS SRV record, so a zone that
// comes back on a different port after a restart is dialled at the old one.
// Nothing arrives at the daemon and the sender reports that it cannot connect,
// which is indistinguishable from the sender refusing to try.
//
// Observed in the field: after a dozen restarts one zone stopped working from
// a Mac while the others carried on, purely because their cached ports still
// happened to match.
func TestStablePortIsDeterministic(t *testing.T) {
	const uuid = "RINCON_B8E9378E388401400"
	first := StablePort(uuid)
	for i := 0; i < 100; i++ {
		if got := StablePort(uuid); got != first {
			t.Fatalf("port moved between calls: %d then %d", first, got)
		}
	}
}

// Different zones must not land on the same port, or the second to bind falls
// back to an ephemeral one and loses the stability this exists to provide.
func TestStablePortSeparatesRealZones(t *testing.T) {
	uuids := []string{
		"RINCON_B8E937EFDE1401400", // Living Room
		"RINCON_5CAAFD292DE601400", // Austin Bedroom
		"RINCON_B8E9378E388401400", // Garage
	}
	seen := map[int]string{}
	for _, u := range uuids {
		p := StablePort(u)
		if prev, dup := seen[p]; dup {
			t.Errorf("%s and %s both map to port %d", prev, u, p)
		}
		seen[p] = u
		t.Logf("%s -> %d", u, p)
	}
}

// Stay out of the privileged range and out of the ephemeral range the kernel
// allocates from, so a stable port is unlikely to be taken by something else.
func TestStablePortIsInASafeRange(t *testing.T) {
	for _, key := range []string{"", "a", "RINCON_000000000000001400", "zzzzzzzzzzzz"} {
		p := StablePort(key)
		if p < 1024 {
			t.Errorf("%q -> %d, inside the privileged range", key, p)
		}
		if p > 49151 {
			t.Errorf("%q -> %d, inside the ephemeral range", key, p)
		}
	}
}
