package sonos

import (
	"net"
	"sort"
)

// Zone is one AirPlay target: exactly one Sonos group, addressed through its
// coordinator.
//
// A group is collapsed into a single Zone rather than exposed as one target
// per speaker, because grouped speakers physically cannot play different
// things -- the coordinator drives the group and transport commands sent to a
// member are silently ignored. Advertising a target per member would offer
// the user independence the hardware will not honour.
type Zone struct {
	// Name is what appears in the AirPlay menu.
	Name string
	// CoordinatorUUID identifies the zone across restarts and IP changes.
	// The group ID is *not* stable or meaningful: a group can carry another
	// speaker's UUID as its prefix long after that grouping is gone.
	CoordinatorUUID string
	// IP is the coordinator's address -- the only one to send transport
	// commands to.
	IP string
	// RAOPID is the AirPlay hardware identity, derived from the
	// coordinator's own MAC (embedded in its Sonos UUID).
	RAOPID net.HardwareAddr
	// Members lists every speaker in the group, coordinator included.
	Members []string
}

// Grouped reports whether this zone spans more than one speaker.
func (z Zone) Grouped() bool { return len(z.Members) > 1 }

// Zones folds discovered players and the group topology into the set of
// AirPlay targets to advertise.
//
// Players not mentioned in the topology are still returned as their own
// single-speaker zone: a player we can reach but cannot place is better
// advertised than dropped.
func Zones(players []Player, groups []Group) []Zone {
	byUUID := make(map[string]Player, len(players))
	for _, p := range players {
		byUUID[p.UUID()] = p
	}

	claimed := make(map[string]bool)
	var zones []Zone

	for _, g := range groups {
		coord, ok := byUUID[g.Coordinator]
		if !ok {
			// The topology names a coordinator we did not discover -- it may
			// be on another subnet or powered down. Skip rather than
			// advertise a target we cannot drive.
			continue
		}

		z := Zone{
			Name:            coord.RoomName,
			CoordinatorUUID: g.Coordinator,
			IP:              coord.IP,
		}
		if mac, ok := coord.RAOPID(); ok {
			z.RAOPID = mac
		}

		for _, m := range g.Members {
			name := m.ZoneName
			if p, ok := byUUID[m.UUID]; ok {
				name = p.RoomName
			}
			z.Members = append(z.Members, name)
			claimed[m.UUID] = true
		}
		sort.Strings(z.Members)

		// A multi-speaker group gets the coordinator's name, which is what
		// the Sonos app shows for it too.
		zones = append(zones, z)
	}

	for _, p := range players {
		if claimed[p.UUID()] {
			continue
		}
		z := Zone{
			Name:            p.RoomName,
			CoordinatorUUID: p.UUID(),
			IP:              p.IP,
			Members:         []string{p.RoomName},
		}
		if mac, ok := p.RAOPID(); ok {
			z.RAOPID = mac
		}
		zones = append(zones, z)
	}

	sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
	return zones
}
