package dnsserver

import (
	"net"
	"testing"

	"github.com/Automattic/uptime-bench/internal/fleet"
)

func TestBuildFromFleet_EmitsNameserverAForEveryMember(t *testing.T) {
	cfg := &fleet.Config{
		Nameservers: []fleet.Nameserver{
			{
				ID:          "ns-01",
				Address:     "10.0.0.1",
				ControlPort: 9100,
				DNSPort:     53,
				Domains:     []string{"example.com"},
				Hosts:       []string{"ns1.example.com"},
			},
			{
				ID:          "ns-02",
				Address:     "10.0.0.2",
				ControlPort: 9100,
				DNSPort:     53,
				Domains:     []string{"example.com"},
				Hosts:       []string{"ns2.example.com"},
			},
		},
		Targets: []fleet.Target{
			{
				ID:      "target-01",
				Address: "10.0.0.10",
				Sites: []fleet.Site{
					{ID: "bench-a", Host: "bench-a.example.com"},
				},
			},
		},
		Domains: []fleet.Domain{
			{Name: "example.com", Nameservers: []string{"ns-01", "ns-02"}, TTL: 30},
		},
	}

	// Each member's zone map must include A records for *every*
	// nameserver — a resolver bypassing the parent's glue will ask
	// whichever member it reaches first.
	for _, memberID := range []string{"ns-01", "ns-02"} {
		t.Run(memberID, func(t *testing.T) {
			m, err := BuildFromFleet(cfg, memberID, 1700000000)
			if err != nil {
				t.Fatalf("BuildFromFleet: %v", err)
			}
			ns1, ok := m.Records["ns1.example.com"]
			if !ok || !ns1.IP.Equal(net.ParseIP("10.0.0.1")) {
				t.Fatalf("ns1.example.com = %+v, want 10.0.0.1", ns1)
			}
			ns2, ok := m.Records["ns2.example.com"]
			if !ok || !ns2.IP.Equal(net.ParseIP("10.0.0.2")) {
				t.Fatalf("ns2.example.com = %+v, want 10.0.0.2", ns2)
			}
			if _, ok := m.Records["bench-a.example.com"]; !ok {
				t.Fatal("target A record disappeared after the nameserver-host change")
			}
		})
	}
}

func TestBuildFromFleet_NameserverHostsRespectServedDomains(t *testing.T) {
	cfg := &fleet.Config{
		Nameservers: []fleet.Nameserver{
			{
				ID:          "ns-01",
				Address:     "10.0.0.1",
				ControlPort: 9100,
				DNSPort:     53,
				Domains:     []string{"example.com"},
				// One under served domain, one outside it. The off-domain
				// host is the operator listing every public host the
				// member answers across multiple zones; this member only
				// serves example.com so other.example must be silently
				// skipped here.
				Hosts: []string{"ns1.example.com", "ns1.other.example"},
			},
		},
		Domains: []fleet.Domain{
			{Name: "example.com", Nameservers: []string{"ns-01"}, TTL: 30},
		},
	}

	m, err := BuildFromFleet(cfg, "ns-01", 1700000000)
	if err != nil {
		t.Fatalf("BuildFromFleet: %v", err)
	}
	if _, ok := m.Records["ns1.example.com"]; !ok {
		t.Fatal("ns1.example.com missing — host under served domain should be emitted")
	}
	if _, ok := m.Records["ns1.other.example"]; ok {
		t.Fatal("ns1.other.example was emitted — hosts outside served domains must be skipped")
	}
}

func TestBuildFromFleet_UsesDomainNameserverHostsForOutOfZoneDelegation(t *testing.T) {
	cfg := &fleet.Config{
		Nameservers: []fleet.Nameserver{
			{
				ID:          "ns-01",
				Address:     "10.0.0.1",
				ControlPort: 9100,
				DNSPort:     53,
				Domains:     []string{"example.org"},
				Hosts:       []string{"ns1.example.com"},
			},
			{
				ID:          "ns-02",
				Address:     "10.0.0.2",
				ControlPort: 9100,
				DNSPort:     53,
				Domains:     []string{"example.org"},
				Hosts:       []string{"ns2.example.com"},
			},
		},
		Targets: []fleet.Target{
			{
				ID:      "target-01",
				Address: "10.0.0.10",
				Sites:   []fleet.Site{{ID: "bench-a", Host: "bench-a.example.org"}},
			},
		},
		Domains: []fleet.Domain{
			{
				Name:            "example.org",
				Nameservers:     []string{"ns-01", "ns-02"},
				NameserverHosts: []string{"ns1.example.com", "ns2.example.com"},
				TTL:             30,
			},
		},
	}

	z, err := BuildFromFleet(cfg, "ns-01", 1700000000)
	if err != nil {
		t.Fatalf("BuildFromFleet: %v", err)
	}
	apex := z.Apex["example.org"]
	if len(apex.NSHostnames) != 2 || apex.NSHostnames[0] != "ns1.example.com" || apex.NSHostnames[1] != "ns2.example.com" {
		t.Fatalf("NSHostnames = %v, want out-of-zone harmonic-style hosts", apex.NSHostnames)
	}
	if _, ok := z.Records["bench-a.example.org"]; !ok {
		t.Fatal("target A record missing for out-of-zone delegation domain")
	}
	if _, ok := z.Records["ns1.example.com"]; ok {
		t.Fatal("out-of-zone nameserver A record should not be emitted into example.org")
	}
}

func TestBuildFromFleet_NoHostsIsBackwardsCompatible(t *testing.T) {
	cfg := &fleet.Config{
		Nameservers: []fleet.Nameserver{
			{
				ID:          "ns-01",
				Address:     "10.0.0.1",
				ControlPort: 9100,
				DNSPort:     53,
				Domains:     []string{"example.com"},
			},
		},
		Targets: []fleet.Target{
			{
				ID:      "target-01",
				Address: "10.0.0.10",
				Sites:   []fleet.Site{{ID: "bench-a", Host: "bench-a.example.com"}},
			},
		},
		Domains: []fleet.Domain{
			{Name: "example.com", Nameservers: []string{"ns-01"}, TTL: 30},
		},
	}

	m, err := BuildFromFleet(cfg, "ns-01", 1700000000)
	if err != nil {
		t.Fatalf("BuildFromFleet: %v", err)
	}
	for name := range m.Records {
		if name == "ns1.example.com" || name == "ns2.example.com" {
			t.Fatalf("nameserver A record %q emitted with no Hosts configured", name)
		}
	}
	if _, ok := m.Records["bench-a.example.com"]; !ok {
		t.Fatal("target A record missing on backwards-compatible config")
	}
}

func TestBuildFromFleet_GeneratedSitesAreNotExpanded(t *testing.T) {
	cfg := &fleet.Config{
		Nameservers: []fleet.Nameserver{
			{
				ID:          "ns-01",
				Address:     "10.0.0.1",
				ControlPort: 9100,
				DNSPort:     53,
				Domains:     []string{"example.com"},
				Hosts:       []string{"ns1.example.com"},
			},
		},
		Targets: []fleet.Target{
			{
				ID:      "target-01",
				Address: "10.0.0.10",
				GeneratedSites: []fleet.GeneratedSiteRange{
					{
						ID:          "load",
						HostPattern: "site-%07d.load.example.com",
						Start:       1,
						Count:       1_000_000,
						Paths:       []string{"/"},
					},
				},
			},
		},
		Domains: []fleet.Domain{
			{Name: "example.com", Nameservers: []string{"ns-01"}, TTL: 30},
		},
	}

	z, err := BuildFromFleet(cfg, "ns-01", 1700000000)
	if err != nil {
		t.Fatalf("BuildFromFleet: %v", err)
	}
	if len(z.Generated) != 1 {
		t.Fatalf("generated ranges = %d, want 1", len(z.Generated))
	}
	if _, ok := z.Records["site-0000001.load.example.com"]; ok {
		t.Fatal("generated host was expanded into static Records map")
	}
	got, ok := z.LookupA("site-0000001.load.example.com")
	if !ok {
		t.Fatal("generated host did not resolve")
	}
	if !got.IP.Equal(net.ParseIP("10.0.0.10")) {
		t.Fatalf("generated IP = %s, want 10.0.0.10", got.IP)
	}
}

func TestBuildFromFleet_GeneratedSitesRespectServedDomains(t *testing.T) {
	cfg := &fleet.Config{
		Nameservers: []fleet.Nameserver{
			{
				ID:          "ns-01",
				Address:     "10.0.0.1",
				ControlPort: 9100,
				DNSPort:     53,
				Domains:     []string{"example.com"},
			},
		},
		Targets: []fleet.Target{
			{
				ID:      "target-01",
				Address: "10.0.0.10",
				GeneratedSites: []fleet.GeneratedSiteRange{
					{ID: "served", HostPattern: "site-%07d.load.example.com", Start: 1, Count: 10},
					{ID: "skipped", HostPattern: "site-%07d.other.example", Start: 1, Count: 10},
				},
			},
		},
		Domains: []fleet.Domain{
			{Name: "example.com", Nameservers: []string{"ns-01"}, TTL: 30},
		},
	}

	z, err := BuildFromFleet(cfg, "ns-01", 1700000000)
	if err != nil {
		t.Fatalf("BuildFromFleet: %v", err)
	}
	if len(z.Generated) != 1 {
		t.Fatalf("generated ranges = %d, want 1", len(z.Generated))
	}
	if z.Generated[0].ID != "served" {
		t.Fatalf("generated range ID = %q, want served", z.Generated[0].ID)
	}
}
