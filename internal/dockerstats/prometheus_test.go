package dockerstats

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeCollector struct {
	samples []Sample
	err     error
	calls   int
}

func (f *fakeCollector) Collect(context.Context) ([]Sample, error) {
	f.calls++
	return f.samples, f.err
}

func TestExporterWritesPrometheusMetrics(t *testing.T) {
	collector := &fakeCollector{
		samples: []Sample{
			{
				Container: Container{
					ID:    "1234567890abcdef",
					Name:  "jetmon-1",
					Image: `repo/image:"test"`,
					Labels: map[string]string{
						"com.docker.compose.project": "jetmon",
						"com.docker.compose.service": "app",
					},
				},
				CPUUsageSeconds:       12.5,
				CPUPercent:            25,
				MemoryUsageBytes:      2048,
				MemoryWorkingSetBytes: 1024,
				MemoryLimitBytes:      4096,
				NetworkReceiveBytes:   100,
				NetworkTransmitBytes:  200,
				BlockReadBytes:        4096,
				BlockWriteBytes:       8192,
				BlockReadOps:          3,
				BlockWriteOps:         5,
				PIDs:                  7,
			},
		},
	}
	exporter := &Exporter{
		collector: collector,
		CacheTTL:  10 * time.Second,
		timeNow:   func() time.Time { return time.Unix(100, 0) },
	}

	var buf bytes.Buffer
	exporter.WritePrometheus(context.Background(), &buf)
	body := buf.String()

	for _, want := range []string{
		"uptime_bench_dockerstats_scrape_success 1\n",
		"uptime_bench_dockerstats_containers 1\n",
		`uptime_bench_docker_container_info{compose_project="jetmon",compose_service="app",container="jetmon-1",container_id="1234567890ab",image="repo/image:\"test\""} 1`,
		`uptime_bench_docker_container_cpu_usage_seconds_total{compose_project="jetmon",compose_service="app",container="jetmon-1",container_id="1234567890ab",image="repo/image:\"test\""} 12.5`,
		`uptime_bench_docker_container_memory_working_set_bytes{compose_project="jetmon",compose_service="app",container="jetmon-1",container_id="1234567890ab",image="repo/image:\"test\""} 1024`,
		`uptime_bench_docker_container_block_read_bytes_total{compose_project="jetmon",compose_service="app",container="jetmon-1",container_id="1234567890ab",image="repo/image:\"test\""} 4096`,
		`uptime_bench_docker_container_block_writes_total{compose_project="jetmon",compose_service="app",container="jetmon-1",container_id="1234567890ab",image="repo/image:\"test\""} 5`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics body missing %q:\n%s", want, body)
		}
	}
}

func TestExporterCachesSuccessfulScrapes(t *testing.T) {
	collector := &fakeCollector{}
	now := time.Unix(100, 0)
	exporter := &Exporter{
		collector: collector,
		CacheTTL:  10 * time.Second,
		timeNow:   func() time.Time { return now },
	}

	exporter.WritePrometheus(context.Background(), &bytes.Buffer{})
	now = now.Add(time.Second)
	exporter.WritePrometheus(context.Background(), &bytes.Buffer{})

	if collector.calls != 1 {
		t.Fatalf("collector calls = %d, want 1", collector.calls)
	}
}

func TestExporterWritesFailureMetric(t *testing.T) {
	exporter := &Exporter{
		collector: &fakeCollector{err: errors.New("docker unavailable")},
	}

	var buf bytes.Buffer
	exporter.WritePrometheus(context.Background(), &buf)
	body := buf.String()

	if !strings.Contains(body, "uptime_bench_dockerstats_scrape_success 0\n") {
		t.Fatalf("body missing scrape failure:\n%s", body)
	}
	if !strings.Contains(body, `uptime_bench_dockerstats_scrape_error{error="docker unavailable"} 1`) {
		t.Fatalf("body missing scrape error:\n%s", body)
	}
}
