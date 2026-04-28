package campaign

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigSamplesParseGenerateAndTranslate(t *testing.T) {
	const dir = "../../configs/campaign"

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	found := false
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".toml") {
			continue
		}
		found = true
		path := filepath.Join(dir, entry.Name())
		t.Run(entry.Name(), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			c, err := Parse(data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(c.Targets.Patterns) != 1 || c.Targets.Patterns[0] != HostPatternSingle {
				t.Fatalf("sample config must stay runner-safe while campaign execution is single-target; patterns = %v", c.Targets.Patterns)
			}
			plan, err := Generate(c, c.Seed)
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if len(plan.Designs) == 0 {
				t.Fatal("Generate produced no designs")
			}
			if len(plan.Schedule) == 0 {
				t.Fatal("Generate produced no schedule")
			}
			for _, design := range plan.Designs {
				sc, err := design.ToScenario(design.ID+"-r0", []string{"jetmon-v2"}, c.CheckFrequency, c.GracePeriod)
				if err != nil {
					t.Fatalf("ToScenario(%s): %v", design.ID, err)
				}
				if sc.Target == "" {
					t.Fatalf("ToScenario(%s) produced empty target", design.ID)
				}
				if len(sc.Failures) == 0 {
					t.Fatalf("ToScenario(%s) produced no failures", design.ID)
				}
			}
		})
	}

	if !found {
		t.Fatalf("no campaign TOML samples found in %s", dir)
	}
}
