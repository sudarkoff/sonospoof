package bridge

import (
	"path/filepath"
	"strings"
	"testing"
)

// A zone name comes from a discovered device's description XML, so anything
// that can answer an SSDP search chooses it. It must never be able to steer
// where the dump file lands.
func TestSafeFilenameCannotEscapeTheDumpDirectory(t *testing.T) {
	hostile := []string{
		"../../etc/cron.d/evil",
		"/etc/passwd",
		`..\..\windows\system32`,
		"....//....//tmp/x",
		"a/b/c",
		"..",
		".",
		"...",
		"\x00etc",
		"nul\x00.wav",
		strings.Repeat("../", 40) + "root",
	}

	const dir = "/var/tmp/dump"
	for _, name := range hostile {
		got := safeFilename(name)

		if strings.ContainsAny(got, `/\`) {
			t.Errorf("%q -> %q still contains a separator", name, got)
		}
		if strings.Contains(got, "..") {
			t.Errorf("%q -> %q still contains ..", name, got)
		}
		if strings.ContainsRune(got, 0) {
			t.Errorf("%q -> %q still contains NUL", name, got)
		}
		if got == "" {
			t.Errorf("%q produced an empty filename", name)
		}

		// The decisive check: the joined path must stay inside the directory.
		full := filepath.Clean(filepath.Join(dir, got+"-1.wav"))
		if !strings.HasPrefix(full, dir+"/") {
			t.Errorf("%q -> %q escapes to %q", name, got, full)
		}
	}
}

// Ordinary names must still be recognisable, or the dumps are useless.
func TestSafeFilenameKeepsRealZoneNames(t *testing.T) {
	cases := map[string]string{
		"Garage":         "Garage",
		"Living Room":    "Living_Room",
		"Austin Bedroom": "Austin_Bedroom",
		"Kids' Room":     "Kids_Room",
		"Café":           "Caf",
	}
	for in, want := range cases {
		if got := safeFilename(in); got != want {
			t.Errorf("safeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSafeFilenameIsBounded(t *testing.T) {
	if got := safeFilename(strings.Repeat("A", 500)); len(got) != 64 {
		t.Errorf("length %d, want 64", len(got))
	}
}
