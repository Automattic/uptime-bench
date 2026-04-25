package scenario

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRepoScenariosParse asserts every TOML scenario shipped in the repo's
// scenarios/ directory parses cleanly. Catches schema drift before it
// becomes a deploy-time surprise: if someone adds a new failure field
// without updating the parser, the affected scenario file fails here.
func TestRepoScenariosParse(t *testing.T) {
	// Walk up from internal/scenario to the repo root.
	root, err := filepath.Abs(filepath.Join("..", "..", "scenarios"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read scenarios dir: %v", err)
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".toml" {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			sc, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse(%s): %v", name, err)
			}
			if sc.ID == "" || sc.Version == "" {
				t.Fatalf("Parse(%s) returned empty id/version", name)
			}
			if len(sc.Failures) == 0 {
				t.Fatalf("Parse(%s): no failures", name)
			}
		})
		count++
	}
	if count == 0 {
		t.Fatal("no scenario files found — test setup wrong")
	}
}
