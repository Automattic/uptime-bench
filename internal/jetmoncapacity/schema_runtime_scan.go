package jetmoncapacity

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	StrictSchemaRuntimeScanClean = "clean"
	StrictSchemaRuntimeScanFound = "findings"
)

// SchemaRuntimeScanFinding is one strict schema/runtime issue found in a log.
type SchemaRuntimeScanFinding struct {
	LineNumber int    `json:"line_number"`
	Pattern    string `json:"pattern"`
	Line       string `json:"line"`
}

// SchemaRuntimeScanResult summarizes a strict schema/runtime log scan.
type SchemaRuntimeScanResult struct {
	Status   string                     `json:"status"`
	Findings []SchemaRuntimeScanFinding `json:"findings,omitempty"`
}

type strictSchemaRuntimeScanRule struct {
	label   string
	pattern *regexp.Regexp
}

var strictSchemaRuntimeScanRules = []strictSchemaRuntimeScanRule{
	{label: "unknown column", pattern: regexp.MustCompile(`(?i)\bunknown column\b`)},
	{label: "missing table", pattern: regexp.MustCompile(`(?i)\btable\b.*\bdoes(?:n't| not) exist\b`)},
	{label: "mysql schema error", pattern: regexp.MustCompile(`(?i)\berror\s+(1054|1146)\b`)},
	{label: "sql syntax", pattern: regexp.MustCompile(`(?i)\bsql syntax\b`)},
	{label: "migration failed", pattern: regexp.MustCompile(`(?i)\bmigration failed\b`)},
	{label: "panic", pattern: regexp.MustCompile(`(?i)\bpanic:`)},
	{label: "fatal error", pattern: regexp.MustCompile(`(?i)\bfatal error\b`)},
	{label: "runtime error", pattern: regexp.MustCompile(`(?i)\bruntime error\b`)},
	{label: "deadlock", pattern: regexp.MustCompile(`(?i)\bdeadlock\b`)},
	{label: "lock wait timeout", pattern: regexp.MustCompile(`(?i)\block wait timeout\b`)},
	{label: "context deadline exceeded", pattern: regexp.MustCompile(`(?i)\bcontext deadline exceeded\b`)},
	{label: "no such host", pattern: regexp.MustCompile(`(?i)\bno such host\b`)},
	{label: "duplicate column", pattern: regexp.MustCompile(`(?i)\bduplicate column\b`)},
	{label: "out of range value", pattern: regexp.MustCompile(`(?i)\bout of range value\b`)},
	{label: "data too long", pattern: regexp.MustCompile(`(?i)\bdata too long\b`)},
	{label: "cannot be null", pattern: regexp.MustCompile(`(?i)\bcannot be null\b`)},
	{label: "foreign key constraint", pattern: regexp.MustCompile(`(?i)\bforeign key constraint\b`)},
}

// ScanStrictSchemaRuntimeLog scans logs for schema/runtime failures while
// ignoring normal Jetmon metric names such as error_timeout and error_keyword.
func ScanStrictSchemaRuntimeLog(text string) SchemaRuntimeScanResult {
	result := SchemaRuntimeScanResult{Status: StrictSchemaRuntimeScanClean}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		for _, rule := range strictSchemaRuntimeScanRules {
			if !rule.pattern.MatchString(line) {
				continue
			}
			result.Findings = append(result.Findings, SchemaRuntimeScanFinding{
				LineNumber: i + 1,
				Pattern:    rule.label,
				Line:       line,
			})
			break
		}
	}
	if len(result.Findings) > 0 {
		result.Status = StrictSchemaRuntimeScanFound
	}
	return result
}

// StrictSchemaRuntimeScanSummary formats the pass/fail line used in Jetmon v2
// reporting.
func StrictSchemaRuntimeScanSummary(result SchemaRuntimeScanResult) string {
	if len(result.Findings) == 0 {
		return "Strict schema/runtime scan: clean"
	}
	return fmt.Sprintf("Strict schema/runtime scan: %d finding(s)", len(result.Findings))
}
