package reportsafety

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScanCleanReportProse(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "REPORT.md", `# Report

No real WPCOM, real SVN, production SVN, or production-like DBs were contacted.
The token was [redacted] and password=<redacted>.
`)

	report, err := Scan(Options{Root: root, GeneratedAt: time.Unix(0, 0).UTC()})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("Scan() findings = %#v, want none", report.Findings)
	}
	if report.FilesScanned != 1 {
		t.Fatalf("FilesScanned = %d, want 1", report.FilesScanned)
	}
}

func TestScanFindsUnsafeArtifacts(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "logs/driver.log", strings.Join([]string{
		`fixture called https://public-api.wordpress.com/rest/v1.1/`,
		`api_key=abcd1234`,
		`token=jm_abcdefghijklmnopqrstuvwxyz`,
		`password=fixture-pass`,
	}, "\n"))
	writeFile(t, root, "90-artifact-scan-old.txt", "token=jm_this_file_is_ignored\n")

	report, err := Scan(Options{Root: root})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	got := findingCategories(report.Findings)
	want := []string{"real_endpoint", "credential", "token", "known_secret"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("categories = %v, want %v\nfindings=%#v", got, want, report.Findings)
	}
	if report.FilesSkipped != 1 {
		t.Fatalf("FilesSkipped = %d, want ignored old scan", report.FilesSkipped)
	}
}

func TestScanSkipsBinaryFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte{0, 'j', 'm', '_'}, 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := Scan(Options{Root: root})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if report.FilesScanned != 0 || report.FilesSkipped != 1 {
		t.Fatalf("scanned/skipped = %d/%d, want 0/1", report.FilesScanned, report.FilesSkipped)
	}
}

func TestScanSkipsShellArtifactsAndPasswordYesDiagnostics(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "run.sh", "password=fixture-pass\n")
	writeFile(t, root, "db.log", "Error 1045: Access denied for user 'u' (using password: YES)\n")

	report, err := Scan(Options{Root: root})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if len(report.Findings) != 0 {
		t.Fatalf("Scan() findings = %#v, want none", report.Findings)
	}
	if report.FilesScanned != 1 || report.FilesSkipped != 1 {
		t.Fatalf("scanned/skipped = %d/%d, want 1/1", report.FilesScanned, report.FilesSkipped)
	}
}

func TestWriters(t *testing.T) {
	report := Report{
		Root:         "/tmp/report",
		GeneratedAt:  time.Unix(0, 0).UTC(),
		FilesScanned: 1,
		Findings: []Finding{{
			Category: "real_endpoint",
			Path:     "REPORT.md",
			Line:     3,
			Match:    "https://jetpack.wordpress.com/",
			Message:  "report artifact contains a real external endpoint",
		}},
	}

	var text bytes.Buffer
	if err := WriteText(&text, report); err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}
	if !strings.Contains(text.String(), "findings=1") {
		t.Fatalf("WriteText() = %q, want findings count", text.String())
	}

	var js bytes.Buffer
	if err := WriteJSON(&js, report); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}
	if !strings.Contains(js.String(), `"findings"`) {
		t.Fatalf("WriteJSON() = %q, want findings key", js.String())
	}
}

func findingCategories(findings []Finding) []string {
	categories := make([]string, len(findings))
	for i, finding := range findings {
		categories[i] = finding.Category
	}
	return categories
}

func writeFile(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
