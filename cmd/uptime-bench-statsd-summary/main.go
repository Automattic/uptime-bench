package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	tcpdumpPacketPattern = regexp.MustCompile(`(?m) UDP, length ([0-9]+)`)
	metricPattern        = regexp.MustCompile(`([A-Za-z0-9_.-]+):[^[:space:]]+\|[A-Za-z]`)
)

type summary struct {
	GeneratedAt        time.Time  `json:"generated_at"`
	Input              string     `json:"input"`
	Source             string     `json:"source"`
	Datagrams          int        `json:"datagrams"`
	Bytes              int        `json:"bytes"`
	MetricLines        int        `json:"metric_lines"`
	UniqueMetricNames  int        `json:"unique_metric_names"`
	ByName             []countRow `json:"by_name,omitempty"`
	ByJetmonCategory   []countRow `json:"by_jetmon_category,omitempty"`
	ByPrefix           []countRow `json:"by_prefix,omitempty"`
	LimitPerCollection int        `json:"limit_per_collection,omitempty"`
	Notes              []string   `json:"notes,omitempty"`
}

type countRow struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func main() {
	log.SetFlags(0)
	input := flag.String("input", "", "tcpdump text capture")
	output := flag.String("out", "", "JSON output path")
	limit := flag.Int("limit", 200, "max rows per collection; 0 keeps all")
	flag.Parse()
	if *input == "" || *output == "" {
		log.Fatal("statsd-summary: -input and -out are required")
	}
	data, err := os.ReadFile(*input)
	if err != nil {
		log.Fatalf("statsd-summary: read: %v", err)
	}
	sum := summarize(*input, string(data), *limit)
	if err := writeJSON(*output, sum); err != nil {
		log.Fatalf("statsd-summary: write: %v", err)
	}
}

func summarize(input, text string, limit int) summary {
	names := map[string]int{}
	categories := map[string]int{}
	prefixes := map[string]int{}
	for _, match := range metricPattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		name := strings.Trim(match[1], ".")
		if name == "" {
			continue
		}
		names[name]++
		categories[jetmonCategory(name)]++
		prefixes[prefix(name, 5)]++
	}
	packetLengths := tcpdumpPacketPattern.FindAllStringSubmatch(text, -1)
	sum := summary{
		GeneratedAt:        time.Now().UTC(),
		Input:              input,
		Source:             "tcpdump text capture of UDP/8125 traffic on the Monitor host",
		Datagrams:          len(packetLengths),
		Bytes:              totalPacketBytes(packetLengths),
		MetricLines:        totalCounts(names),
		UniqueMetricNames:  len(names),
		ByName:             sortedCounts(names, limit),
		ByJetmonCategory:   sortedCounts(categories, limit),
		ByPrefix:           sortedCounts(prefixes, limit),
		LimitPerCollection: limit,
	}
	if sum.Datagrams == 0 && sum.MetricLines > 0 {
		sum.Notes = append(sum.Notes, "metric-looking lines were found but tcpdump packet headers were not recognized")
	}
	if sum.Datagrams > 0 && sum.MetricLines == 0 {
		sum.Notes = append(sum.Notes, "UDP/8125 datagrams were captured but no StatsD metric lines matched the parser")
	}
	return sum
}

func totalPacketBytes(matches [][]string) int {
	total := 0
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(match[1], "%d", &n); err == nil {
			total += n
		}
	}
	return total
}

func jetmonCategory(name string) string {
	parts := strings.Split(name, ".")
	if len(parts) >= 5 && parts[0] == "com" && parts[1] == "jetpack" && parts[2] == "jetmon" {
		return "com.jetpack.jetmon.<host>." + parts[4]
	}
	if len(parts) >= 4 {
		return strings.Join(parts[:4], ".")
	}
	return name
}

func prefix(name string, n int) string {
	parts := strings.Split(name, ".")
	if len(parts) <= n {
		return name
	}
	return strings.Join(parts[:n], ".")
}

func totalCounts(counts map[string]int) int {
	total := 0
	for _, count := range counts {
		total += count
	}
	return total
}

func sortedCounts(counts map[string]int, limit int) []countRow {
	rows := make([]countRow, 0, len(counts))
	for name, count := range counts {
		rows = append(rows, countRow{Name: name, Count: count})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Count == rows[j].Count {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].Count > rows[j].Count
	})
	if limit > 0 && len(rows) > limit {
		return rows[:limit]
	}
	return rows
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
