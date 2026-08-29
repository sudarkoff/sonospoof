package sonos

import "testing"

// The three Play:1 units on George's LAN, with the real UUIDs and the real
// group IDs read off the household. Note Garage's group ID carries Austin
// Bedroom's UUID as its prefix -- a leftover from a past grouping -- which is
// exactly why the coordinator UUID, not the group ID, is the zone key.
func realPlayers() []Player {
	return []Player{
		{IP: "192.168.30.252", RoomName: "Living Room", ModelName: "Sonos Play:1", UDN: "uuid:RINCON_B8E937EFDE1401400"},
		{IP: "192.168.30.158", RoomName: "Austin Bedroom", ModelName: "Sonos Play:1", UDN: "uuid:RINCON_5CAAFD292DE601400"},
		{IP: "192.168.30.244", RoomName: "Garage", ModelName: "Sonos Play:1", UDN: "uuid:RINCON_B8E9378E388401400"},
	}
}

func ungrouped() []Group {
	return []Group{
		{ID: "RINCON_B8E937EFDE1401400:4183059217", Coordinator: "RINCON_B8E937EFDE1401400",
			Members: []GroupMember{{UUID: "RINCON_B8E937EFDE1401400", ZoneName: "Living Room", IP: "192.168.30.252"}}},
		{ID: "RINCON_5CAAFD292DE601400:3104264952", Coordinator: "RINCON_5CAAFD292DE601400",
			Members: []GroupMember{{UUID: "RINCON_5CAAFD292DE601400", ZoneName: "Austin Bedroom", IP: "192.168.30.158"}}},
		{ID: "RINCON_5CAAFD292DE601400:3104264951", Coordinator: "RINCON_B8E9378E388401400",
			Members: []GroupMember{{UUID: "RINCON_B8E9378E388401400", ZoneName: "Garage", IP: "192.168.30.244"}}},
	}
}

func TestZonesUngroupedGivesOneTargetEach(t *testing.T) {
	zones := Zones(realPlayers(), ungrouped())
	if len(zones) != 3 {
		t.Fatalf("expected 3 independent targets, got %d", len(zones))
	}
	want := []string{"Austin Bedroom", "Garage", "Living Room"}
	for i, z := range zones {
		if z.Name != want[i] {
			t.Errorf("zone %d: got %q, want %q", i, z.Name, want[i])
		}
		if z.Grouped() {
			t.Errorf("%s should not be grouped", z.Name)
		}
	}
}

// Garage's coordinator must come from the Coordinator attribute, not from the
// group ID's prefix -- those disagree in the real household.
func TestZonesUseCoordinatorNotGroupIDPrefix(t *testing.T) {
	zones := Zones(realPlayers(), ungrouped())
	for _, z := range zones {
		if z.Name != "Garage" {
			continue
		}
		if z.CoordinatorUUID != "RINCON_B8E9378E388401400" {
			t.Errorf("Garage coordinator = %q, want its own UUID", z.CoordinatorUUID)
		}
		if z.IP != "192.168.30.244" {
			t.Errorf("Garage IP = %q, want 192.168.30.244", z.IP)
		}
		return
	}
	t.Fatal("Garage zone missing")
}

// Grouping two speakers must collapse them to one target, because grouped
// speakers cannot play different streams.
func TestZonesGroupedCollapseToOneTarget(t *testing.T) {
	groups := []Group{
		{ID: "g1", Coordinator: "RINCON_B8E937EFDE1401400", Members: []GroupMember{
			{UUID: "RINCON_B8E937EFDE1401400", ZoneName: "Living Room", IP: "192.168.30.252"},
			{UUID: "RINCON_B8E9378E388401400", ZoneName: "Garage", IP: "192.168.30.244"},
		}},
		{ID: "g2", Coordinator: "RINCON_5CAAFD292DE601400", Members: []GroupMember{
			{UUID: "RINCON_5CAAFD292DE601400", ZoneName: "Austin Bedroom", IP: "192.168.30.158"},
		}},
	}
	zones := Zones(realPlayers(), groups)
	if len(zones) != 2 {
		t.Fatalf("expected 2 targets when two speakers are grouped, got %d", len(zones))
	}
	for _, z := range zones {
		if z.Name == "Living Room" {
			if !z.Grouped() || len(z.Members) != 2 {
				t.Errorf("Living Room should be a 2-member group, got %v", z.Members)
			}
			if z.IP != "192.168.30.252" {
				t.Errorf("grouped zone must address the coordinator, got %s", z.IP)
			}
		}
	}
}

// The RAOP id is the coordinator's own MAC, taken out of its Sonos UUID.
func TestRAOPIDComesFromSonosUUID(t *testing.T) {
	cases := map[string]string{
		"uuid:RINCON_B8E937EFDE1401400": "b8:e9:37:ef:de:14",
		"uuid:RINCON_5CAAFD292DE601400": "5c:aa:fd:29:2d:e6",
		"uuid:RINCON_B8E9378E388401400": "b8:e9:37:8e:38:84",
	}
	for udn, want := range cases {
		p := Player{UDN: udn}
		mac, ok := p.RAOPID()
		if !ok {
			t.Errorf("%s: no RAOP id", udn)
			continue
		}
		if mac.String() != want {
			t.Errorf("%s: got %s, want %s", udn, mac, want)
		}
	}
}

// Every advertised target must have a distinct identity, or iOS will show
// one device where there should be several.
func TestRAOPIDsAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, z := range Zones(realPlayers(), ungrouped()) {
		if z.RAOPID == nil {
			t.Fatalf("%s has no RAOP id", z.Name)
		}
		id := z.RAOPID.String()
		if prev, dup := seen[id]; dup {
			t.Errorf("%s and %s share RAOP id %s", prev, z.Name, id)
		}
		seen[id] = z.Name
	}
}

// A coordinator we could not discover must not become a target we cannot drive.
func TestZonesSkipUndiscoverableCoordinator(t *testing.T) {
	groups := []Group{
		{ID: "g", Coordinator: "RINCON_DEADBEEF000001400", Members: []GroupMember{
			{UUID: "RINCON_DEADBEEF000001400", ZoneName: "Elsewhere"},
		}},
	}
	if zones := Zones(nil, groups); len(zones) != 0 {
		t.Errorf("expected no targets, got %d", len(zones))
	}
}
