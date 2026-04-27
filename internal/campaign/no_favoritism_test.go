package campaign

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoServiceSpecificBranches enforces the methodology rule from
// ROADMAP.md "Automated randomized testing campaigns":
//
//	The campaign generator and reporter contain **no service-specific
//	branches**. No `if serviceID == "jetmon-v1"` anywhere in the
//	campaign or reporting code, ever. A simple lint test in CI
//	grepping for known service IDs in those files would enforce this.
//
// The test fails if any present-day service ID appears in non-test
// source files in the campaign package. Tests legitimately use these
// strings (fixture configs, etc.) and are excluded.
//
// When new services are added to the project, the maintainer must
// update knownServiceIDs below — that's the deliberate review point
// that makes the policy explicit rather than implicit.
//
// To intentionally name a service in production code (e.g. a future
// adapter-version migration helper), document why and add a narrow
// exception here.
func TestNoServiceSpecificBranches(t *testing.T) {
	knownServiceIDs := []string{
		"jetmon-v1",
		"jetmon-v2",
		"pingdom",
		"uptimerobot",
		"datadog-synthetics",
		"better-uptime",
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	checked := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".go" {
			continue
		}
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(data)
		for _, id := range knownServiceIDs {
			if strings.Contains(body, id) {
				t.Errorf("%s: contains service ID %q — campaign code must stay service-agnostic; "+
					"if this is a deliberate exception, document why and update this test",
					name, id)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no non-test .go files found in campaign package — test setup is wrong")
	}
}
