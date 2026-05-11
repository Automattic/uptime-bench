package dockerstats

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Exporter renders Docker stats samples as Prometheus text exposition.
type Exporter struct {
	Client   *Client
	Timeout  time.Duration
	CacheTTL time.Duration

	mu        sync.Mutex
	cachedAt  time.Time
	cached    []byte
	cacheErr  error
	timeNow   func() time.Time
	collector sampleCollector
}

type sampleCollector interface {
	Collect(context.Context) ([]Sample, error)
}

// WritePrometheus writes a Prometheus text response, using a short cache to
// avoid making concurrent scrapes fan out into duplicate Docker API calls.
func (e *Exporter) WritePrometheus(ctx context.Context, w io.Writer) {
	body, err := e.body(ctx)
	if err != nil {
		writeFailure(w, err)
		return
	}
	_, _ = w.Write(body)
}

func (e *Exporter) body(ctx context.Context) ([]byte, error) {
	now := e.now()
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.CacheTTL > 0 && len(e.cached) > 0 && now.Sub(e.cachedAt) < e.CacheTTL {
		if e.cacheErr != nil {
			return nil, e.cacheErr
		}
		return append([]byte(nil), e.cached...), nil
	}

	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	collectCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := e.now()
	samples, err := e.collectorForUse().Collect(collectCtx)
	duration := e.now().Sub(start)
	if err != nil {
		e.cachedAt = now
		e.cached = nil
		e.cacheErr = err
		return nil, err
	}

	var buf bytes.Buffer
	writeHeader(&buf)
	writeGauge(&buf, "uptime_bench_dockerstats_scrape_success", nil, 1)
	writeGauge(&buf, "uptime_bench_dockerstats_scrape_duration_seconds", nil, duration.Seconds())
	writeGauge(&buf, "uptime_bench_dockerstats_containers", nil, float64(len(samples)))
	for _, sample := range samples {
		writeSample(&buf, sample)
	}

	e.cachedAt = now
	e.cached = append([]byte(nil), buf.Bytes()...)
	e.cacheErr = nil
	return buf.Bytes(), nil
}

func (e *Exporter) collectorForUse() sampleCollector {
	if e.collector != nil {
		return e.collector
	}
	if e.Client != nil {
		return e.Client
	}
	return &Client{}
}

func (e *Exporter) now() time.Time {
	if e.timeNow != nil {
		return e.timeNow()
	}
	return time.Now()
}

func writeFailure(w io.Writer, err error) {
	writeHeader(w)
	writeGauge(w, "uptime_bench_dockerstats_scrape_success", nil, 0)
	writeInfo(w, "uptime_bench_dockerstats_scrape_error", map[string]string{"error": err.Error()}, 1)
}

func writeHeader(w io.Writer) {
	fmt.Fprintln(w, "# HELP uptime_bench_dockerstats_scrape_success Whether the last Docker stats scrape succeeded.")
	fmt.Fprintln(w, "# TYPE uptime_bench_dockerstats_scrape_success gauge")
	fmt.Fprintln(w, "# HELP uptime_bench_dockerstats_scrape_duration_seconds Time spent collecting Docker stats.")
	fmt.Fprintln(w, "# TYPE uptime_bench_dockerstats_scrape_duration_seconds gauge")
	fmt.Fprintln(w, "# HELP uptime_bench_dockerstats_containers Number of running Docker containers included in the scrape.")
	fmt.Fprintln(w, "# TYPE uptime_bench_dockerstats_containers gauge")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_info Docker container metadata.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_info gauge")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_cpu_usage_seconds_total Total container CPU time consumed.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_cpu_usage_seconds_total counter")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_cpu_percent Instantaneous Docker CPU percent where 100 is one full core.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_cpu_percent gauge")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_memory_usage_bytes Container memory usage from Docker stats.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_memory_usage_bytes gauge")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_memory_working_set_bytes Container memory usage minus inactive file cache when reported.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_memory_working_set_bytes gauge")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_memory_limit_bytes Container memory limit from Docker stats.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_memory_limit_bytes gauge")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_network_receive_bytes_total Container network bytes received.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_network_receive_bytes_total counter")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_network_transmit_bytes_total Container network bytes transmitted.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_network_transmit_bytes_total counter")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_block_read_bytes_total Container block device bytes read.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_block_read_bytes_total counter")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_block_write_bytes_total Container block device bytes written.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_block_write_bytes_total counter")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_block_reads_total Container block device read operations.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_block_reads_total counter")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_block_writes_total Container block device write operations.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_block_writes_total counter")
	fmt.Fprintln(w, "# HELP uptime_bench_docker_container_pids Container process count from Docker stats.")
	fmt.Fprintln(w, "# TYPE uptime_bench_docker_container_pids gauge")
}

func writeSample(w io.Writer, sample Sample) {
	labels := sampleLabels(sample.Container)
	writeInfo(w, "uptime_bench_docker_container_info", labels, 1)
	writeGauge(w, "uptime_bench_docker_container_cpu_usage_seconds_total", labels, sample.CPUUsageSeconds)
	writeGauge(w, "uptime_bench_docker_container_cpu_percent", labels, sample.CPUPercent)
	writeGauge(w, "uptime_bench_docker_container_memory_usage_bytes", labels, sample.MemoryUsageBytes)
	writeGauge(w, "uptime_bench_docker_container_memory_working_set_bytes", labels, sample.MemoryWorkingSetBytes)
	writeGauge(w, "uptime_bench_docker_container_memory_limit_bytes", labels, sample.MemoryLimitBytes)
	writeGauge(w, "uptime_bench_docker_container_network_receive_bytes_total", labels, sample.NetworkReceiveBytes)
	writeGauge(w, "uptime_bench_docker_container_network_transmit_bytes_total", labels, sample.NetworkTransmitBytes)
	writeGauge(w, "uptime_bench_docker_container_block_read_bytes_total", labels, sample.BlockReadBytes)
	writeGauge(w, "uptime_bench_docker_container_block_write_bytes_total", labels, sample.BlockWriteBytes)
	writeGauge(w, "uptime_bench_docker_container_block_reads_total", labels, sample.BlockReadOps)
	writeGauge(w, "uptime_bench_docker_container_block_writes_total", labels, sample.BlockWriteOps)
	writeGauge(w, "uptime_bench_docker_container_pids", labels, sample.PIDs)
}

func sampleLabels(container Container) map[string]string {
	labels := map[string]string{
		"container":    container.Name,
		"container_id": shortID(container.ID),
		"image":        container.Image,
	}
	if container.Labels != nil {
		if v := container.Labels["com.docker.compose.project"]; v != "" {
			labels["compose_project"] = v
		}
		if v := container.Labels["com.docker.compose.service"]; v != "" {
			labels["compose_service"] = v
		}
	}
	return labels
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func writeInfo(w io.Writer, name string, labels map[string]string, value float64) {
	writeGauge(w, name, labels, value)
}

func writeGauge(w io.Writer, name string, labels map[string]string, value float64) {
	fmt.Fprintf(w, "%s%s %s\n", name, formatLabels(labels), formatFloat(value))
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+`="`+escapeLabelValue(labels[k])+`"`)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
