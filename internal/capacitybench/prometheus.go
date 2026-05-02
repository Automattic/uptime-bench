package capacitybench

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// PrometheusClient is a minimal client for Prometheus range queries.
type PrometheusClient struct {
	BaseURL string
	Client  *http.Client
}

// Query describes one PromQL expression the capacity collector should summarize.
type Query struct {
	Name string `json:"name"`
	Unit string `json:"unit"`
	Expr string `json:"expr"`
}

// Sample is one Prometheus sample.
type Sample struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// Series is one Prometheus time series returned by a query.
type Series struct {
	Metric map[string]string `json:"metric"`
	Values []Sample          `json:"values"`
}

// SeriesSummary is the compact, window-level summary for one returned series.
type SeriesSummary struct {
	Query   string            `json:"query"`
	Unit    string            `json:"unit"`
	Labels  map[string]string `json:"labels"`
	Samples int               `json:"samples"`
	Min     float64           `json:"min"`
	Avg     float64           `json:"avg"`
	P50     float64           `json:"p50"`
	P95     float64           `json:"p95"`
	Max     float64           `json:"max"`
	Last    float64           `json:"last"`
}

// Report is the collector output for a single window.
type Report struct {
	PrometheusURL string          `json:"prometheus_url"`
	Start         time.Time       `json:"start"`
	End           time.Time       `json:"end"`
	Step          string          `json:"step"`
	Instances     []string        `json:"instances"`
	Summaries     []SeriesSummary `json:"summaries"`
}

type apiResponse struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string      `json:"resultType"`
		Result     []rawSeries `json:"result"`
	} `json:"data"`
}

type rawSeries struct {
	Metric map[string]string   `json:"metric"`
	Values [][]json.RawMessage `json:"values"`
}

// RangeQuery executes one Prometheus query_range request.
func (c *PrometheusClient) RangeQuery(ctx context.Context, expr string, start, end time.Time, step time.Duration) ([]Series, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("prometheus: base URL is required")
	}
	if !end.After(start) {
		return nil, fmt.Errorf("prometheus: end must be after start")
	}
	if step <= 0 {
		return nil, fmt.Errorf("prometheus: step must be positive")
	}

	base := strings.TrimRight(c.BaseURL, "/")
	endpoint, err := url.Parse(base + "/api/v1/query_range")
	if err != nil {
		return nil, fmt.Errorf("prometheus: parse URL: %w", err)
	}
	q := endpoint.Query()
	q.Set("query", expr)
	q.Set("start", promTimestamp(start))
	q.Set("end", promTimestamp(end))
	q.Set("step", promDuration(step))
	endpoint.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prometheus: query_range: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prometheus: query_range: status %d", resp.StatusCode)
	}

	var decoded apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("prometheus: decode response: %w", err)
	}
	if decoded.Status != "success" {
		if decoded.Error != "" {
			return nil, fmt.Errorf("prometheus: %s: %s", decoded.ErrorType, decoded.Error)
		}
		return nil, fmt.Errorf("prometheus: query failed with status %q", decoded.Status)
	}
	if decoded.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("prometheus: query returned %q, want matrix", decoded.Data.ResultType)
	}

	out := make([]Series, 0, len(decoded.Data.Result))
	for _, rs := range decoded.Data.Result {
		values := make([]Sample, 0, len(rs.Values))
		for _, pair := range rs.Values {
			s, err := parseSample(pair)
			if err != nil {
				return nil, err
			}
			values = append(values, s)
		}
		out = append(out, Series{Metric: rs.Metric, Values: values})
	}
	return out, nil
}

// Collect executes every query and returns window summaries for each series.
func Collect(ctx context.Context, client *PrometheusClient, queries []Query, start, end time.Time, step time.Duration) ([]SeriesSummary, error) {
	var out []SeriesSummary
	for _, q := range queries {
		series, err := client.RangeQuery(ctx, q.Expr, start, end, step)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", q.Name, err)
		}
		for _, s := range series {
			summary, ok := SummarizeSeries(q, s)
			if ok {
				out = append(out, summary)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Query != out[j].Query {
			return out[i].Query < out[j].Query
		}
		return SeriesLabel(out[i].Labels) < SeriesLabel(out[j].Labels)
	})
	return out, nil
}

// SummarizeSeries reduces one time series into min/avg/percentile/max/last.
func SummarizeSeries(q Query, s Series) (SeriesSummary, bool) {
	values := make([]float64, 0, len(s.Values))
	var last float64
	for _, sample := range s.Values {
		if math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			continue
		}
		values = append(values, sample.Value)
		last = sample.Value
	}
	if len(values) == 0 {
		return SeriesSummary{}, false
	}

	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	labels := make(map[string]string, len(s.Metric))
	for k, v := range s.Metric {
		if k != "__name__" {
			labels[k] = v
		}
	}
	return SeriesSummary{
		Query:   q.Name,
		Unit:    q.Unit,
		Labels:  labels,
		Samples: len(values),
		Min:     sorted[0],
		Avg:     sum / float64(len(values)),
		P50:     percentile(sorted, 0.50),
		P95:     percentile(sorted, 0.95),
		Max:     sorted[len(sorted)-1],
		Last:    last,
	}, true
}

// DefaultQueries returns the first-pass capacity metrics for Jetmon host/container comparison.
func DefaultQueries(instanceRegex string, rateWindow time.Duration) []Query {
	window := promDuration(rateWindow)
	match := fmt.Sprintf(`instance=~"%s"`, instanceRegex)
	return []Query{
		{
			Name: "host_cpu_used",
			Unit: "percent",
			Expr: fmt.Sprintf(`100 * (1 - avg by(instance) (rate(node_cpu_seconds_total{job="node",mode="idle",%s}[%s])))`, match, window),
		},
		{
			Name: "host_memory_used",
			Unit: "percent",
			Expr: fmt.Sprintf(`100 * (1 - (node_memory_MemAvailable_bytes{job="node",%s} / node_memory_MemTotal_bytes{job="node",%s}))`, match, match),
		},
		{
			Name: "host_root_disk_used",
			Unit: "percent",
			Expr: fmt.Sprintf(`100 * (1 - (node_filesystem_avail_bytes{job="node",mountpoint="/",fstype!~"tmpfs|overlay|squashfs|ramfs",%s} / node_filesystem_size_bytes{job="node",mountpoint="/",fstype!~"tmpfs|overlay|squashfs|ramfs",%s}))`, match, match),
		},
		{
			Name: "host_net_rx",
			Unit: "bytes_per_second",
			Expr: fmt.Sprintf(`sum by(instance) (rate(node_network_receive_bytes_total{job="node",device!~"lo|docker.*|veth.*|br-.*",%s}[%s]))`, match, window),
		},
		{
			Name: "host_net_tx",
			Unit: "bytes_per_second",
			Expr: fmt.Sprintf(`sum by(instance) (rate(node_network_transmit_bytes_total{job="node",device!~"lo|docker.*|veth.*|br-.*",%s}[%s]))`, match, window),
		},
		{
			Name: "container_cpu_used",
			Unit: "percent_core",
			Expr: fmt.Sprintf(`100 * sum by(instance,name) (rate(container_cpu_usage_seconds_total{job="cadvisor",cpu="total",name!="",image!="",%s}[%s]))`, match, window),
		},
		{
			Name: "container_memory_working_set",
			Unit: "bytes",
			Expr: fmt.Sprintf(`sum by(instance,name) (container_memory_working_set_bytes{job="cadvisor",name!="",image!="",%s})`, match),
		},
		{
			Name: "docker_container_cpu_used",
			Unit: "percent_core",
			Expr: fmt.Sprintf(`avg by(instance,container) (uptime_bench_docker_container_cpu_percent{job="dockerstats",container!="",%s})`, match),
		},
		{
			Name: "docker_container_cpu_rate",
			Unit: "percent_core",
			Expr: fmt.Sprintf(`100 * sum by(instance,container) (rate(uptime_bench_docker_container_cpu_usage_seconds_total{job="dockerstats",container!="",%s}[%s]))`, match, window),
		},
		{
			Name: "docker_container_memory_working_set",
			Unit: "bytes",
			Expr: fmt.Sprintf(`sum by(instance,container) (uptime_bench_docker_container_memory_working_set_bytes{job="dockerstats",container!="",%s})`, match),
		},
		{
			Name: "docker_container_net_rx",
			Unit: "bytes_per_second",
			Expr: fmt.Sprintf(`sum by(instance,container) (rate(uptime_bench_docker_container_network_receive_bytes_total{job="dockerstats",container!="",%s}[%s]))`, match, window),
		},
		{
			Name: "docker_container_net_tx",
			Unit: "bytes_per_second",
			Expr: fmt.Sprintf(`sum by(instance,container) (rate(uptime_bench_docker_container_network_transmit_bytes_total{job="dockerstats",container!="",%s}[%s]))`, match, window),
		},
		{
			Name: "dockerstats_scrape_success",
			Unit: "state",
			Expr: fmt.Sprintf(`uptime_bench_dockerstats_scrape_success{job="dockerstats",%s}`, match),
		},
		{
			Name: "process_cpu_used",
			Unit: "percent_core",
			Expr: fmt.Sprintf(`100 * sum by(instance,groupname) (rate(namedprocess_namegroup_cpu_seconds_total{job="process",%s}[%s]))`, match, window),
		},
		{
			Name: "process_memory_resident",
			Unit: "bytes",
			Expr: fmt.Sprintf(`sum by(instance,groupname) (namedprocess_namegroup_memory_bytes{job="process",memtype="resident",%s})`, match),
		},
		{
			Name: "process_count",
			Unit: "count",
			Expr: fmt.Sprintf(`sum by(instance,groupname) (namedprocess_namegroup_num_procs{job="process",%s})`, match),
		},
		{
			Name: "process_threads",
			Unit: "count",
			Expr: fmt.Sprintf(`sum by(instance,groupname) (namedprocess_namegroup_num_threads{job="process",%s})`, match),
		},
		{
			Name: "process_open_fds",
			Unit: "count",
			Expr: fmt.Sprintf(`sum by(instance,groupname) (namedprocess_namegroup_open_filedesc{job="process",%s})`, match),
		},
		{
			Name: "scrape_up",
			Unit: "state",
			Expr: fmt.Sprintf(`up{job=~"node|cadvisor|dockerstats|process",%s}`, match),
		},
	}
}

// InstanceRegex returns a Prometheus-safe regex matching the configured instances.
func InstanceRegex(instances []string) (string, error) {
	var parts []string
	for _, instance := range instances {
		instance = strings.TrimSpace(instance)
		if instance == "" {
			continue
		}
		parts = append(parts, regexp.QuoteMeta(instance))
	}
	if len(parts) == 0 {
		return "", fmt.Errorf("at least one instance is required")
	}
	return strings.Join(parts, "|"), nil
}

// SeriesLabel returns a compact human label for a summarized series.
func SeriesLabel(labels map[string]string) string {
	var parts []string
	if v := labels["instance"]; v != "" {
		parts = append(parts, v)
	}
	if v := labels["name"]; v != "" {
		parts = append(parts, v)
	}
	if v := labels["container"]; v != "" {
		parts = append(parts, v)
	}
	if v := labels["groupname"]; v != "" {
		parts = append(parts, v)
	}
	if v := labels["job"]; v != "" {
		parts = append(parts, "job="+v)
	}
	if len(parts) > 0 {
		return strings.Join(parts, "/")
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, ",")
}

func parseSample(pair []json.RawMessage) (Sample, error) {
	if len(pair) != 2 {
		return Sample{}, fmt.Errorf("prometheus: sample has %d fields, want 2", len(pair))
	}
	var ts float64
	if err := json.Unmarshal(pair[0], &ts); err != nil {
		return Sample{}, fmt.Errorf("prometheus: parse sample timestamp: %w", err)
	}
	var rawValue string
	if err := json.Unmarshal(pair[1], &rawValue); err != nil {
		return Sample{}, fmt.Errorf("prometheus: parse sample value: %w", err)
	}
	value, err := strconv.ParseFloat(rawValue, 64)
	if err != nil {
		return Sample{}, fmt.Errorf("prometheus: parse sample value %q: %w", rawValue, err)
	}
	sec, frac := math.Modf(ts)
	return Sample{
		Timestamp: time.Unix(int64(sec), int64(frac*1e9)).UTC(),
		Value:     value,
	}, nil
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := p * float64(len(sorted)-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))
	if lower == upper {
		return sorted[lower]
	}
	weight := pos - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

func promTimestamp(t time.Time) string {
	return strconv.FormatFloat(float64(t.UTC().UnixNano())/1e9, 'f', 3, 64)
}

func promDuration(d time.Duration) string {
	seconds := int64(d.Round(time.Second) / time.Second)
	if seconds <= 0 {
		seconds = 1
	}
	return strconv.FormatInt(seconds, 10) + "s"
}
