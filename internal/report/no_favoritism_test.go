package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoServiceSpecificBranches enforces the campaign-reporting rule
// from ROADMAP.md: report aggregation must not contain service-specific
// branches. Per-service differences belong in adapter data, not in the
// reporter.
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
		if filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		body := string(data)
		for _, id := range knownServiceIDs {
			if strings.Contains(body, id) {
				t.Errorf("%s: contains service ID %q; report code must stay service-agnostic", name, id)
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no non-test .go files found in report package")
	}
}
