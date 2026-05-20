// Package reportsafety scans report bundles for material that should not be
// preserved in uptime-bench artifacts.
package reportsafety

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const defaultMaxFileBytes int64 = 10 << 20

var (
	realEndpointPattern = regexp.MustCompile(`(?i)(https?|svn\+ssh)://(?:jetpack\.wordpress\.com|public-api\.wordpress\.com|svn\.automattic\.com)(?:[^\s"'<>)]*)?`)
	tokenPattern        = regexp.MustCompile(`\bjm_[A-Za-z0-9_]{12,}\b`)
	keyValuePattern     = regexp.MustCompile(`(?i)\b(?:api[_-]?key|auth[_-]?token|password|passwd|secret|token)\b\s*[:=]\s*["']?([^"'\s#]+)`)
	knownSecretPattern  = regexp.MustCompile(`(?i)\b(?:jetmon_dev_password|rotate_dev_password|wrong_rotate_password|fixture-pass)\b`)
	redactedPattern     = regexp.MustCompile(`(?i)^(?:redacted|<redacted>|\[redacted\]|\*\*\*|xxxxx+|example|changeme|yes|no|true|false)$`)
)

// Options controls report bundle scanning.
type Options struct {
	Root         string
	MaxFileBytes int64
	GeneratedAt  time.Time
}

// Report contains the result of scanning a report bundle.
type Report struct {
	Root         string    `json:"root"`
	GeneratedAt  time.Time `json:"generated_at"`
	FilesScanned int       `json:"files_scanned"`
	FilesSkipped int       `json:"files_skipped"`
	Findings     []Finding `json:"findings"`
}

// Finding identifies one potentially unsafe report artifact line.
type Finding struct {
	Category string `json:"category"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Match    string `json:"match"`
	Message  string `json:"message"`
}

// Scan scans opts.Root recursively for real endpoints and credential-shaped
// strings that should not be retained in a report bundle.
func Scan(opts Options) (Report, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return Report{}, errors.New("report root is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return Report{}, fmt.Errorf("stat report root: %w", err)
	}
	if !info.IsDir() {
		return Report{}, fmt.Errorf("report root %q is not a directory", root)
	}
	maxBytes := opts.MaxFileBytes
	if maxBytes <= 0 {
		maxBytes = defaultMaxFileBytes
	}
	generatedAt := opts.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = time.Now().UTC()
	}

	report := Report{
		Root:        root,
		GeneratedAt: generatedAt.UTC(),
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if shouldSkipFile(entry.Name()) {
			report.FilesSkipped++
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			report.FilesSkipped++
			return nil
		}
		if info.Size() > maxBytes {
			report.FilesSkipped++
			return nil
		}
		findings, skipped, err := scanFile(root, path)
		if err != nil {
			return err
		}
		if skipped {
			report.FilesSkipped++
			return nil
		}
		report.FilesScanned++
		report.Findings = append(report.Findings, findings...)
		return nil
	})
	if err != nil {
		return Report{}, err
	}
	sort.Slice(report.Findings, func(i, j int) bool {
		if report.Findings[i].Path != report.Findings[j].Path {
			return report.Findings[i].Path < report.Findings[j].Path
		}
		return report.Findings[i].Line < report.Findings[j].Line
	})
	return report, nil
}

// WriteText writes a stable human-readable scan report.
func WriteText(w io.Writer, report Report) error {
	if _, err := fmt.Fprintf(w, "scan_at=%s\n", report.GeneratedAt.UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "root=%s\nfiles_scanned=%d\nfiles_skipped=%d\nfindings=%d\n", report.Root, report.FilesScanned, report.FilesSkipped, len(report.Findings)); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(w, "findings:"); err != nil {
		return err
	}
	for _, finding := range report.Findings {
		if _, err := fmt.Fprintf(w, "%s:%d\t%s\t%s\t%s\n", finding.Path, finding.Line, finding.Category, finding.Match, finding.Message); err != nil {
			return err
		}
	}
	return nil
}

// WriteJSON writes a machine-readable scan report.
func WriteJSON(w io.Writer, report Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(report)
}

func scanFile(root, path string) ([]Finding, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if isBinary(data) {
		return nil, true, nil
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	rel = filepath.ToSlash(rel)

	var findings []Finding
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Text()
		findings = append(findings, scanLine(rel, lineNumber, line)...)
	}
	if err := scanner.Err(); err != nil {
		return nil, false, err
	}
	return findings, false, nil
}

func scanLine(path string, lineNumber int, line string) []Finding {
	var findings []Finding
	for _, match := range realEndpointPattern.FindAllString(line, -1) {
		findings = append(findings, Finding{
			Category: "real_endpoint",
			Path:     path,
			Line:     lineNumber,
			Match:    match,
			Message:  "report artifact contains a real external endpoint",
		})
	}
	for _, match := range tokenPattern.FindAllString(line, -1) {
		findings = append(findings, Finding{
			Category: "token",
			Path:     path,
			Line:     lineNumber,
			Match:    redact(match),
			Message:  "report artifact contains a Jetmon token-shaped value",
		})
	}
	for _, match := range knownSecretPattern.FindAllString(line, -1) {
		findings = append(findings, Finding{
			Category: "known_secret",
			Path:     path,
			Line:     lineNumber,
			Match:    match,
			Message:  "report artifact contains a known synthetic credential value",
		})
	}
	for _, groups := range keyValuePattern.FindAllStringSubmatch(line, -1) {
		if len(groups) < 2 {
			continue
		}
		value := normalizeCredentialValue(groups[1])
		if redactedPattern.MatchString(value) || tokenPattern.MatchString(value) || knownSecretPattern.MatchString(value) {
			continue
		}
		findings = append(findings, Finding{
			Category: "credential",
			Path:     path,
			Line:     lineNumber,
			Match:    redact(groups[0]),
			Message:  "report artifact contains a credential-shaped key/value",
		})
	}
	return findings
}

func normalizeCredentialValue(value string) string {
	return strings.Trim(strings.TrimSpace(value), `.,;)]}`)
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "vendor":
		return true
	default:
		return false
	}
}

func shouldSkipFile(name string) bool {
	matched, _ := filepath.Match("90-artifact-scan-*.txt", name)
	if matched {
		return true
	}
	switch filepath.Ext(name) {
	case ".sh", ".bash":
		return true
	default:
		return false
	}
}

func isBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return true
	}
	return !utf8.Valid(data)
}

func redact(value string) string {
	if len(value) <= 8 {
		return "<redacted>"
	}
	return value[:4] + "..." + value[len(value)-4:]
}
