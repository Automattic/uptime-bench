package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Automattic/uptime-bench/internal/capacitybench"
)

const (
	defaultV1Addr        = "10.0.0.172:7801"
	defaultV2Addr        = "10.0.0.173:7803"
	defaultTargetURL     = "http://10.0.0.176/"
	defaultFailureURL    = "http://10.0.0.176:1/"
	defaultPrometheusURL = "http://10.0.0.67:9091"
)

type legacyV1Request struct {
	AuthToken string                 `json:"auth_token"`
	Checks    []legacyV1RequestCheck `json:"checks"`
}

type legacyV1RequestCheck struct {
	BlogID     int64  `json:"blog_id"`
	MonitorURL string `json:"monitor_url"`
}

type legacyV1Ack struct {
	Veriflier string `json:"veriflier,omitempty"`
	Status    int    `json:"status"`
	Error     string `json:"error,omitempty"`
}

type legacyV1Callback struct {
	AuthToken string                `json:"auth_token,omitempty"`
	Checks    []legacyV1CallbackRow `json:"checks"`
}

type legacyV1CallbackRow struct {
	BlogID     int64  `json:"blog_id"`
	MonitorURL string `json:"monitor_url"`
	Status     int    `json:"status"`
	Code       int    `json:"code"`
	RTT        int64  `json:"rtt"`
}

type v2BatchRequest struct {
	BatchID    string           `json:"batch_id,omitempty"`
	DeadlineMS int64            `json:"deadline_ms,omitempty"`
	Requests   []v2CheckRequest `json:"requests"`
}

type v2CheckRequest struct {
	RequestID        string            `json:"request_id,omitempty"`
	BlogID           int64             `json:"blog_id"`
	URL              string            `json:"url"`
	TimeoutMS        int64             `json:"timeout_ms,omitempty"`
	Method           string            `json:"method,omitempty"`
	DetectionProfile string            `json:"detection_profile,omitempty"`
	Headers          map[string]string `json:"headers,omitempty"`
	RedirectPolicy   string            `json:"redirect_policy,omitempty"`
	BodyReadMaxBytes int64             `json:"body_read_max_bytes,omitempty"`
}

type v2BatchResponse struct {
	BatchID string          `json:"batch_id,omitempty"`
	Vantage v2Vantage       `json:"vantage"`
	Agent   v2Agent         `json:"agent"`
	Results []v2CheckResult `json:"results"`
}

type v2Vantage struct {
	ID       string `json:"id"`
	Region   string `json:"region,omitempty"`
	Provider string `json:"provider,omitempty"`
}

type v2Agent struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Version  string `json:"version"`
	Protocol string `json:"protocol,omitempty"`
}

type v2Capacity struct {
	MaxConcurrency int `json:"max_concurrency"`
	QueueCapacity  int `json:"queue_capacity"`
	QueueDepth     int `json:"queue_depth"`
	Active         int `json:"active"`
	InFlight       int `json:"in_flight"`
}

type v2Status struct {
	Status    string     `json:"status"`
	Version   string     `json:"version"`
	Protocols []string   `json:"protocols"`
	Vantage   v2Vantage  `json:"vantage"`
	Agent     v2Agent    `json:"agent"`
	Capacity  v2Capacity `json:"capacity"`
}

type v2CheckResult struct {
	RequestID string `json:"request_id"`
	BlogID    int64  `json:"blog_id"`
	URL       string `json:"url"`
	VantageID string `json:"vantage_id"`
	AgentID   string `json:"agent_id"`
	Outcome   string `json:"outcome"`
	Success   bool   `json:"success"`
	HTTPCode  int32  `json:"http_code"`
	ErrorCode int32  `json:"error_code"`
	RTTMs     int64  `json:"rtt_ms"`
}

type endpointConfig struct {
	Name       string       `json:"name"`
	Protocol   string       `json:"protocol"`
	Addr       string       `json:"addr"`
	Token      string       `json:"-"`
	SSHHost    string       `json:"ssh_host,omitempty"`
	PIDPattern string       `json:"pid_pattern,omitempty"`
	Instance   string       `json:"prometheus_instance,omitempty"`
	BatchSize  int          `json:"batch_size"`
	HTTPClient *http.Client `json:"-"`
}

type tierSpec struct {
	Name              string            `json:"name"`
	RatePerMin        int               `json:"rate_per_min"`
	Duration          time.Duration     `json:"-"`
	DurationStr       string            `json:"duration"`
	Concurrency       int               `json:"concurrency"`
	URL               string            `json:"url"`
	ExpectUp          bool              `json:"expect_up"`
	Method            string            `json:"method,omitempty"`
	DetectionProfile  string            `json:"detection_profile,omitempty"`
	BodyReadMaxBytes  int64             `json:"body_read_max_bytes,omitempty"`
	TrafficMix        []trafficMixEntry `json:"traffic_mix,omitempty"`
	TargetCounterPath string            `json:"-"`
}

type trafficMixEntry struct {
	Method           string `json:"method"`
	DetectionProfile string `json:"detection_profile"`
	BodyReadMaxBytes int64  `json:"body_read_max_bytes,omitempty"`
	Weight           int    `json:"weight"`
}

type preflightResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type runReport struct {
	GeneratedAt    time.Time            `json:"generated_at"`
	StartedAt      time.Time            `json:"started_at"`
	FinishedAt     time.Time            `json:"finished_at"`
	OutDir         string               `json:"out_dir"`
	TargetLocality string               `json:"target_locality"`
	CountedTarget  *countedTargetConfig `json:"counted_target,omitempty"`
	Endpoints      []endpointConfig     `json:"endpoints"`
	Tiers          []tierSpec           `json:"tiers"`
	Preflight      []preflightResult    `json:"preflight"`
	Results        []tierResult         `json:"results"`
	Summary        summary              `json:"summary"`
	Notes          []string             `json:"notes,omitempty"`
}

type tierResult struct {
	Endpoint               string                        `json:"endpoint"`
	Protocol               string                        `json:"protocol"`
	Tier                   string                        `json:"tier"`
	URL                    string                        `json:"url"`
	ExpectUp               bool                          `json:"expect_up"`
	Method                 string                        `json:"method,omitempty"`
	DetectionProfile       string                        `json:"detection_profile,omitempty"`
	BodyReadMaxBytes       int64                         `json:"body_read_max_bytes,omitempty"`
	TrafficMix             []trafficMixEntry             `json:"traffic_mix,omitempty"`
	Start                  time.Time                     `json:"start"`
	End                    time.Time                     `json:"end"`
	ConfiguredRatePerMin   int                           `json:"configured_rate_per_min"`
	ConfiguredDuration     string                        `json:"configured_duration"`
	ConfiguredConcurrency  int                           `json:"configured_concurrency"`
	BatchSize              int                           `json:"batch_size"`
	RPCRequests            int                           `json:"rpc_requests"`
	Attempted              int                           `json:"attempted"`
	Submitted              int                           `json:"submitted"`
	Completed              int                           `json:"completed"`
	ExpectedMatches        int                           `json:"expected_matches"`
	UnexpectedResults      int                           `json:"unexpected_results"`
	MissingResults         int                           `json:"missing_results"`
	TransportErrors        int                           `json:"transport_errors"`
	OverloadResponses      int                           `json:"overload_responses"`
	ThroughputPerSecond    float64                       `json:"throughput_per_second"`
	CompletionRate         float64                       `json:"completion_rate"`
	UnexpectedRate         float64                       `json:"unexpected_rate"`
	TransportErrorRate     float64                       `json:"transport_error_rate"`
	EndToEndLatencyMS      statBlock                     `json:"end_to_end_latency_ms"`
	ProbeRTTMS             statBlock                     `json:"probe_rtt_ms"`
	ResourceSummary        resourceSummary               `json:"resource_summary"`
	DisplayResourceSummary resourceSummary               `json:"display_resource_summary,omitempty"`
	DisplayResourceSource  string                        `json:"display_resource_source,omitempty"`
	TargetObservation      *targetObservation            `json:"target_observation,omitempty"`
	PrometheusStatus       string                        `json:"prometheus_status,omitempty"`
	PrometheusError        string                        `json:"prometheus_error,omitempty"`
	PrometheusSummary      []capacitybench.SeriesSummary `json:"prometheus_summary,omitempty"`
	Sustainable            bool                          `json:"sustainable"`
	SaturationReason       string                        `json:"saturation_reason,omitempty"`
	ErrorsByKind           map[string]int                `json:"errors_by_kind,omitempty"`
}

type summary struct {
	Status                     string            `json:"status"`
	MaxSustainableByEndpoint   map[string]int    `json:"max_sustainable_rate_per_min_by_endpoint"`
	SaturationReasonByEndpoint map[string]string `json:"saturation_reason_by_endpoint,omitempty"`
}

type checkResult struct {
	BlogID       int64
	Success      bool
	HTTPCode     int
	ErrorCode    int
	Outcome      string
	ProbeRTT     time.Duration
	EndToEnd     time.Duration
	TransportErr string
	Missing      bool
	Overloaded   bool
}

type countedTargetConfig struct {
	Listen    string `json:"listen"`
	BaseURL   string `json:"base_url"`
	BodyBytes int    `json:"body_bytes"`
	RunID     string `json:"run_id"`
}

type targetObservation struct {
	Path             string  `json:"path"`
	Requests         int64   `json:"requests"`
	HEADRequests     int64   `json:"head_requests"`
	GETRequests      int64   `json:"get_requests"`
	OtherRequests    int64   `json:"other_requests"`
	BytesWritten     int64   `json:"bytes_written"`
	RequestRatio     float64 `json:"request_ratio"`
	ExpectedRequests int     `json:"expected_requests"`
}

type targetCounterStats struct {
	Requests      int64
	HEADRequests  int64
	GETRequests   int64
	OtherRequests int64
	BytesWritten  int64
}

type countedTarget struct {
	cfg      countedTargetConfig
	srv      *http.Server
	listener net.Listener
	body     []byte
	mu       sync.Mutex
	stats    map[string]targetCounterStats
}

type callbackRow struct {
	Row        legacyV1CallbackRow
	ReceivedAt time.Time
}

type callbackServer struct {
	srv       *http.Server
	listener  net.Listener
	mu        sync.Mutex
	callbacks map[int64]callbackRow
	seen      chan struct{}
}

type resourceSampler struct {
	endpoint endpointConfig
	interval time.Duration
	mu       sync.Mutex
	samples  []resourceSample
	errs     []string
	stop     chan struct{}
	done     chan struct{}
}

type resourceSample struct {
	At               time.Time `json:"at"`
	Host             string    `json:"host"`
	Missing          bool      `json:"missing"`
	PID              int       `json:"pid,omitempty"`
	TimeNS           int64     `json:"time_ns,omitempty"`
	ClockTicks       float64   `json:"clock_ticks,omitempty"`
	HostTotalJiffies float64   `json:"host_total_jiffies,omitempty"`
	HostIdleJiffies  float64   `json:"host_idle_jiffies,omitempty"`
	ProcJiffies      float64   `json:"proc_jiffies,omitempty"`
	RSSBytes         float64   `json:"rss_bytes,omitempty"`
	OpenFDs          float64   `json:"open_fds,omitempty"`
	Threads          float64   `json:"threads,omitempty"`
	ReadBytes        float64   `json:"read_bytes,omitempty"`
	WriteBytes       float64   `json:"write_bytes,omitempty"`
	NetRXBytes       float64   `json:"net_rx_bytes,omitempty"`
	NetTXBytes       float64   `json:"net_tx_bytes,omitempty"`
}

type resourceSummary struct {
	Samples                    int       `json:"samples"`
	Error                      string    `json:"error,omitempty"`
	ProcessCPUPercentCore      statBlock `json:"process_cpu_percent_core"`
	HostCPUPercent             statBlock `json:"host_cpu_percent"`
	RSSBytes                   statBlock `json:"rss_bytes"`
	OpenFDs                    statBlock `json:"open_fds"`
	Threads                    statBlock `json:"threads"`
	ProcessReadBytesPerSecond  statBlock `json:"process_read_bytes_per_second"`
	ProcessWriteBytesPerSecond statBlock `json:"process_write_bytes_per_second"`
	HostNetRXBytesPerSecond    statBlock `json:"host_net_rx_bytes_per_second"`
	HostNetTXBytesPerSecond    statBlock `json:"host_net_tx_bytes_per_second"`
}

type statBlock struct {
	Count  int       `json:"count"`
	Min    float64   `json:"min,omitempty"`
	Avg    float64   `json:"avg,omitempty"`
	P50    float64   `json:"p50,omitempty"`
	P95    float64   `json:"p95,omitempty"`
	P99    float64   `json:"p99,omitempty"`
	Max    float64   `json:"max,omitempty"`
	values []float64 `json:"-"`
}

func main() {
	var (
		v1Addr           = flag.String("v1-addr", defaultV1Addr, "Jetmon v1 Veriflier legacy TLS address")
		v2Addr           = flag.String("v2-addr", defaultV2Addr, "Jetmon v2 Veriflier v2 HTTP address")
		v1Token          = flag.String("v1-token", "", "Jetmon v1 Veriflier auth token; prefer V1_VERIFLIER_TOKEN or VERIFLIER_AUTH_TOKEN")
		v2Token          = flag.String("v2-token", "", "Jetmon v2 Veriflier auth token; prefer V2_VERIFLIER_TOKEN or VERIFLIER_AUTH_TOKEN")
		targetURL        = flag.String("target-url", defaultTargetURL, "healthy internal target URL")
		failureURL       = flag.String("failure-url", defaultFailureURL, "internal failure target URL")
		tiersFlag        = flag.String("tiers", "smoke:100:30s:20,baseline-1kpm:1000:2m:80,ramp-5kpm:5000:2m:160,ramp-10kpm:10000:2m:240", "comma-separated tiers as name:checks_per_min:duration:concurrency[:healthy|failure]")
		includeFailure   = flag.Bool("include-failure-tier", true, "append a short failure-target tier")
		method           = flag.String("method", "HEAD", "request method for generated checks: HEAD or GET")
		detectionProfile = flag.String("detection-profile", "legacy", "v2 detection profile for generated checks: legacy, simple_http, or full")
		bodyReadMaxBytes = flag.Int64("body-read-max-bytes", 0, "v2 body_read_max_bytes for generated checks; 0 lets Jetmon use its default")
		trafficMixFlag   = flag.String("traffic-mix", "", "optional v2-only weighted mix as method:profile:body_read_max_bytes:weight, comma-separated")
		countedListen    = flag.String("counted-target-listen", "", "optional listen address for an internal counted HTTP target served by this process, such as :18081")
		countedBaseURL   = flag.String("counted-target-base-url", "", "base URL that Verifliers can reach for the counted target; inferred from counted-target-listen when empty")
		countedBodyBytes = flag.Int("counted-target-body-bytes", 0, "response body bytes served by the counted target for GET requests")
		failureRate      = flag.Int("failure-rate-per-min", 1000, "checks/min for the appended failure tier")
		failureDuration  = flag.Duration("failure-duration", 1*time.Minute, "duration for the appended failure tier")
		failureConc      = flag.Int("failure-concurrency", 80, "concurrency for the appended failure tier")
		v1BatchSize      = flag.Int("v1-batch-size", 50, "checks per v1 legacy request")
		v2BatchSize      = flag.Int("v2-batch-size", 50, "checks per v2 /v2/check request")
		v2MaxIdleConns   = flag.Int("v2-max-idle-conns", 100, "max idle connections for the shared v2 benchmark HTTP transport")
		v2MaxIdlePerHost = flag.Int("v2-max-idle-conns-per-host", 20, "max idle connections per host for the shared v2 benchmark HTTP transport")
		requestTimeout   = flag.Duration("request-timeout", 8*time.Second, "per-request transport timeout")
		drainTimeout     = flag.Duration("drain-timeout", 45*time.Second, "extra time to wait for v1 callbacks after a tier")
		cooldown         = flag.Duration("cooldown", 15*time.Second, "sleep between endpoint tiers")
		callbackListen   = flag.String("callback-listen", ":7800", "TLS callback listen address for v1 results")
		promURL          = flag.String("prometheus-url", defaultPrometheusURL, "Prometheus base URL; empty disables Prometheus capture")
		promStep         = flag.Duration("prometheus-step", 15*time.Second, "Prometheus query_range step")
		promRateWindow   = flag.Duration("prometheus-rate-window", 30*time.Second, "Prometheus rate window")
		sampleInterval   = flag.Duration("resource-sample-interval", 5*time.Second, "SSH /proc resource sample interval")
		v1SSHHost        = flag.String("v1-ssh-host", "jetmon-vm-host-1", "SSH host for v1 Veriflier resource sampling")
		v1PIDPattern     = flag.String("v1-pid-pattern", "^./veriflier start$", "pgrep -f pattern for v1 Veriflier")
		v1Instance       = flag.String("v1-prometheus-instance", "jetmon-vm-host-1", "Prometheus instance label for the v1 Veriflier host")
		v2SSHHost        = flag.String("v2-ssh-host", "jetmon-vm-host-2", "SSH host for v2 Veriflier resource sampling")
		v2PIDPattern     = flag.String("v2-pid-pattern", "^./veriflier2$", "pgrep -f pattern for v2 Veriflier")
		v2Instance       = flag.String("v2-prometheus-instance", "jetmon-vm-host-2", "Prometheus instance label for the v2 Veriflier host")
		endpointsFlag    = flag.String("endpoints", "v1,v2", "comma-separated endpoints to test: v1,v2")
		outDir           = flag.String("out-dir", "", "report output directory")
	)
	flag.Parse()

	if *v1Token == "" {
		*v1Token = firstNonEmpty(os.Getenv("V1_VERIFLIER_TOKEN"), os.Getenv("VERIFLIER_AUTH_TOKEN"))
	}
	if *v2Token == "" {
		*v2Token = firstNonEmpty(os.Getenv("V2_VERIFLIER_TOKEN"), os.Getenv("VERIFLIER_AUTH_TOKEN"))
	}
	if *v1Token == "" || *v2Token == "" {
		log.Fatal("set v1/v2 Veriflier tokens via flags, V1_VERIFLIER_TOKEN/V2_VERIFLIER_TOKEN, or VERIFLIER_AUTH_TOKEN")
	}
	if *outDir == "" {
		*outDir = filepath.Join("reports", time.Now().UTC().Format("20060102T150405Z")+"-jetmon-veriflier-bench")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("create report dir: %v", err)
	}

	normalizedMethod, normalizedProfile, err := validateCheckMode(*method, *detectionProfile)
	if err != nil {
		log.Fatal(err)
	}
	trafficMix, err := parseTrafficMix(*trafficMixFlag)
	if err != nil {
		log.Fatal(err)
	}
	var counter *countedTarget
	if *countedListen != "" {
		counter, err = startCountedTarget(*countedListen, *countedBaseURL, *countedBodyBytes)
		if err != nil {
			log.Fatalf("start counted target: %v", err)
		}
		defer counter.Close()
		log.Printf("counted target listening on %s advertised as %s", counter.cfg.Listen, counter.cfg.BaseURL)
	}

	tiers, err := parseTiers(*tiersFlag, *targetURL, *failureURL)
	if err != nil {
		log.Fatal(err)
	}
	if *includeFailure {
		tiers = append(tiers, tierSpec{
			Name:        "failure-port-refused",
			RatePerMin:  *failureRate,
			Duration:    *failureDuration,
			DurationStr: failureDuration.String(),
			Concurrency: *failureConc,
			URL:         *failureURL,
			ExpectUp:    false,
		})
	}
	for i := range tiers {
		tiers[i].Method = normalizedMethod
		tiers[i].DetectionProfile = normalizedProfile
		tiers[i].BodyReadMaxBytes = *bodyReadMaxBytes
		tiers[i].TrafficMix = trafficMix
	}

	allEndpoints := []endpointConfig{
		{Name: "v1", Protocol: "v1-legacy-tls-callback", Addr: *v1Addr, Token: *v1Token, SSHHost: *v1SSHHost, PIDPattern: *v1PIDPattern, Instance: *v1Instance, BatchSize: *v1BatchSize},
		{Name: "v2", Protocol: "v2-json-http", Addr: *v2Addr, Token: *v2Token, SSHHost: *v2SSHHost, PIDPattern: *v2PIDPattern, Instance: *v2Instance, BatchSize: *v2BatchSize, HTTPClient: newV2BenchmarkHTTPClient(*v2MaxIdleConns, *v2MaxIdlePerHost)},
	}
	endpoints := selectEndpoints(allEndpoints, *endpointsFlag)
	if len(endpoints) == 0 {
		log.Fatal("no endpoints selected")
	}
	if len(trafficMix) > 0 && hasProtocol(endpoints, "v1") {
		log.Fatal("-traffic-mix is v2-only; run v1 legacy tiers separately")
	}

	targetLocality := "internal-only direct HTTP target by private IP; capacity.internal DNS is not used because the current v1 Veriflier host does not resolve it"
	var countedConfig *countedTargetConfig
	notes := []string{
		"The first campaign uses direct internal HTTP targets to avoid public internet variability.",
		"DNS-specific comparison is deferred until v1 and v2 resolve the same internal DNS zone from their Veriflier hosts.",
	}
	if counter != nil {
		targetLocality = "internal-only counted HTTP target served by uptime-bench and reached by private LAN address"
		cfgCopy := counter.cfg
		countedConfig = &cfgCopy
		notes = append(notes, "Target-side request counts come from the counted HTTP target in this harness; each endpoint/tier uses a unique path.")
		if *countedBodyBytes > 0 {
			notes = append(notes, fmt.Sprintf("Counted target serves %d response body bytes on GET requests.", *countedBodyBytes))
		}
	}

	started := time.Now().UTC()
	rep := runReport{
		GeneratedAt:    started,
		StartedAt:      started,
		OutDir:         *outDir,
		TargetLocality: targetLocality,
		CountedTarget:  countedConfig,
		Endpoints:      scrubEndpoints(endpoints),
		Tiers:          tiers,
		Notes:          notes,
	}

	ctx := context.Background()
	var cb *callbackServer
	if hasProtocol(endpoints, "v1") {
		cb, err = startCallbackServer(*callbackListen)
		if err != nil {
			log.Fatalf("start v1 callback server: %v", err)
		}
		defer cb.Close()
	}

	if counter != nil {
		rep.Preflight = append(rep.Preflight, preflightTarget(ctx, counter.URLFor("preflight", "healthy", normalizedMethod, normalizedProfile), true))
	} else {
		rep.Preflight = append(rep.Preflight, preflightTarget(ctx, *targetURL, true))
	}
	rep.Preflight = append(rep.Preflight, preflightTarget(ctx, *failureURL, false))
	for _, ep := range endpoints {
		switch ep.Name {
		case "v1":
			rep.Preflight = append(rep.Preflight, preflightV1(ctx, ep.Addr))
		case "v2":
			rep.Preflight = append(rep.Preflight, preflightV2(ctx, ep.Addr))
		}
		rep.Preflight = append(rep.Preflight, preflightResourceSampler(ctx, ep))
	}

	for _, tier := range tiers {
		for _, ep := range endpoints {
			runTier := tier
			if counter != nil && runTier.ExpectUp {
				method, profile := runTier.Method, runTier.DetectionProfile
				if len(runTier.TrafficMix) > 0 {
					method, profile = "MIXED", "traffic_mix"
				}
				runTier.URL, runTier.TargetCounterPath = counter.URLAndPathFor(ep.Name, runTier.Name, method, profile)
			}
			if *cooldown > 0 && len(rep.Results) > 0 {
				time.Sleep(*cooldown)
			}
			log.Printf("running endpoint=%s tier=%s method=%s profile=%s rate=%d/min duration=%s", ep.Name, runTier.Name, runTier.Method, runTier.DetectionProfile, runTier.RatePerMin, runTier.Duration)
			result := runEndpointTier(ctx, ep, runTier, cb, *requestTimeout, *drainTimeout, *sampleInterval)
			if counter != nil && runTier.TargetCounterPath != "" {
				result.TargetObservation = counter.Observation(runTier.TargetCounterPath, result.Attempted)
			}
			if *promURL != "" {
				result.PrometheusStatus = "pass"
				result.PrometheusSummary, err = collectPrometheus(ctx, *promURL, ep.Instance, result.Start, result.End, *promStep, *promRateWindow)
				if err != nil {
					result.PrometheusStatus = "fail"
					result.PrometheusError = err.Error()
				}
			}
			result.DisplayResourceSummary, result.DisplayResourceSource = displayResourceSummary(result)
			result.Sustainable, result.SaturationReason = classifySustainability(result)
			rep.Results = append(rep.Results, result)
			if err := writeReports(*outDir, rep); err != nil {
				log.Printf("write interim report: %v", err)
			}
		}
	}

	rep.FinishedAt = time.Now().UTC()
	rep.Summary = summarize(rep.Results)
	if err := writeReports(*outDir, rep); err != nil {
		log.Fatalf("write reports: %v", err)
	}
	log.Printf("veriflier benchmark complete report_dir=%s status=%s", *outDir, rep.Summary.Status)
}

func parseTiers(spec, targetURL, failureURL string) ([]tierSpec, error) {
	var tiers []tierSpec
	for _, raw := range strings.Split(spec, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.Split(raw, ":")
		if len(parts) < 4 || len(parts) > 5 {
			return nil, fmt.Errorf("tier %q must be name:checks_per_min:duration:concurrency[:healthy|failure]", raw)
		}
		rate, err := strconv.Atoi(parts[1])
		if err != nil || rate <= 0 {
			return nil, fmt.Errorf("tier %q has invalid checks_per_min", raw)
		}
		duration, err := time.ParseDuration(parts[2])
		if err != nil || duration <= 0 {
			return nil, fmt.Errorf("tier %q has invalid duration", raw)
		}
		concurrency, err := strconv.Atoi(parts[3])
		if err != nil || concurrency <= 0 {
			return nil, fmt.Errorf("tier %q has invalid concurrency", raw)
		}
		expectUp := true
		url := targetURL
		if len(parts) == 5 {
			switch parts[4] {
			case "healthy":
			case "failure":
				expectUp = false
				url = failureURL
			default:
				return nil, fmt.Errorf("tier %q has invalid target kind %q", raw, parts[4])
			}
		}
		tiers = append(tiers, tierSpec{Name: parts[0], RatePerMin: rate, Duration: duration, DurationStr: duration.String(), Concurrency: concurrency, URL: url, ExpectUp: expectUp})
	}
	return tiers, nil
}

func validateCheckMode(method, profile string) (string, string, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = http.MethodHead
	}
	switch method {
	case http.MethodHead, http.MethodGet:
	default:
		return "", "", fmt.Errorf("method must be HEAD or GET")
	}
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile == "" {
		profile = "legacy"
	}
	switch profile {
	case "legacy", "simple_http", "full":
	default:
		return "", "", fmt.Errorf("detection-profile must be legacy, simple_http, or full")
	}
	if method == http.MethodHead && profile == "full" {
		profile = "simple_http"
	}
	return method, profile, nil
}

func parseTrafficMix(spec string) ([]trafficMixEntry, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var entries []trafficMixEntry
	for _, raw := range strings.Split(spec, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parts := strings.Split(raw, ":")
		if len(parts) != 4 {
			return nil, fmt.Errorf("traffic mix entry %q must be method:profile:body_read_max_bytes:weight", raw)
		}
		method, profile, err := validateCheckMode(parts[0], parts[1])
		if err != nil {
			return nil, fmt.Errorf("traffic mix entry %q: %w", raw, err)
		}
		bodyBytes, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || bodyBytes < 0 {
			return nil, fmt.Errorf("traffic mix entry %q has invalid body_read_max_bytes", raw)
		}
		weight, err := strconv.Atoi(parts[3])
		if err != nil || weight <= 0 {
			return nil, fmt.Errorf("traffic mix entry %q has invalid weight", raw)
		}
		entries = append(entries, trafficMixEntry{
			Method:           method,
			DetectionProfile: profile,
			BodyReadMaxBytes: bodyBytes,
			Weight:           weight,
		})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("traffic mix must contain at least one entry")
	}
	return entries, nil
}

func selectEndpoints(all []endpointConfig, spec string) []endpointConfig {
	allowed := map[string]bool{}
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			allowed[item] = true
		}
	}
	var out []endpointConfig
	for _, ep := range all {
		if allowed[ep.Name] {
			out = append(out, ep)
		}
	}
	return out
}

func scrubEndpoints(in []endpointConfig) []endpointConfig {
	out := append([]endpointConfig(nil), in...)
	for i := range out {
		out[i].Token = ""
	}
	return out
}

func hasProtocol(endpoints []endpointConfig, name string) bool {
	for _, ep := range endpoints {
		if ep.Name == name {
			return true
		}
	}
	return false
}

func runEndpointTier(ctx context.Context, ep endpointConfig, tier tierSpec, cb *callbackServer, requestTimeout, drainTimeout, sampleInterval time.Duration) tierResult {
	start := time.Now().UTC()
	total := int(math.Round(float64(tier.RatePerMin) * tier.Duration.Minutes()))
	if total < 1 {
		total = 1
	}
	batchSize := ep.BatchSize
	if batchSize <= 0 {
		batchSize = 1
	}
	inFlightBatches := tier.Concurrency / batchSize
	if inFlightBatches < 1 {
		inFlightBatches = 1
	}
	baseID := firstBlogID(ep.Name, tier.Name)
	batches := buildBatches(total, batchSize, tier.URL, baseID, tier.Method, tier.DetectionProfile, tier.BodyReadMaxBytes, tier.TrafficMix)

	sampler := newResourceSampler(ep, sampleInterval)
	sampler.start(ctx)
	defer sampler.stopAndWait()

	var (
		mu           sync.Mutex
		results      []checkResult
		submitted    int
		errorsByKind = map[string]int{}
	)
	addResults := func(items []checkResult) {
		mu.Lock()
		defer mu.Unlock()
		for _, item := range items {
			if item.TransportErr != "" {
				errorsByKind[item.TransportErr]++
			}
			if item.Missing {
				errorsByKind["missing_result"]++
			}
			if item.Overloaded {
				errorsByKind["overload"]++
			}
			results = append(results, item)
		}
	}
	addSubmitted := func(n int) {
		mu.Lock()
		submitted += n
		mu.Unlock()
	}

	if ep.Name == "v1" && cb != nil {
		cb.ForgetRange(baseID, total)
	}

	pace := tier.Duration / time.Duration(len(batches))
	if pace <= 0 {
		pace = time.Nanosecond
	}
	sem := make(chan struct{}, inFlightBatches)
	var wg sync.WaitGroup
	tierCtx, cancel := context.WithTimeout(ctx, tier.Duration+drainTimeout+requestTimeout)
	defer cancel()
	base := time.Now()
	for i, batch := range batches {
		wait := base.Add(time.Duration(i) * pace).Sub(time.Now())
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-tierCtx.Done():
				timer.Stop()
				break
			case <-timer.C:
			}
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(batch []benchCheck) {
			defer wg.Done()
			defer func() { <-sem }()
			subCtx, subCancel := context.WithTimeout(tierCtx, requestTimeout)
			defer subCancel()
			switch ep.Name {
			case "v1":
				if err := sendV1Batch(subCtx, ep.Addr, ep.Token, batch); err != nil {
					addResults(transportFailures(batch, classifyTransportErr(err)))
					return
				}
				addSubmitted(len(batch))
			case "v2":
				res := sendV2Batch(subCtx, ep, batch, requestTimeout)
				submittedNow := 0
				for _, r := range res {
					if r.TransportErr == "" {
						submittedNow++
					}
				}
				addSubmitted(submittedNow)
				addResults(res)
			}
		}(batch)
	}
	wg.Wait()
	if remaining := base.Add(tier.Duration).Sub(time.Now()); remaining > 0 {
		timer := time.NewTimer(remaining)
		select {
		case <-tierCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}

	if ep.Name == "v1" && cb != nil {
		v1Results := cb.ResultsForRange(baseID, total, drainTimeout)
		submitTimes := map[int64]time.Time{}
		for _, batch := range batches {
			for _, check := range batch {
				submitTimes[check.BlogID] = check.SubmittedAt
			}
		}
		converted := make([]checkResult, 0, total)
		seen := map[int64]bool{}
		for _, row := range v1Results {
			seen[row.Row.BlogID] = true
			e2e := row.ReceivedAt.Sub(submitTimes[row.Row.BlogID])
			if e2e < 0 {
				e2e = 0
			}
			converted = append(converted, checkResult{
				BlogID:   row.Row.BlogID,
				Success:  row.Row.Status == 1,
				HTTPCode: row.Row.Code,
				ProbeRTT: time.Duration(row.Row.RTT) * time.Millisecond,
				EndToEnd: e2e,
			})
		}
		for _, batch := range batches {
			for _, check := range batch {
				if !seen[check.BlogID] {
					converted = append(converted, checkResult{BlogID: check.BlogID, Missing: true})
				}
			}
		}
		addResults(converted)
	}

	sampler.stopAndWait()
	end := time.Now().UTC()

	mu.Lock()
	finalResults := append([]checkResult(nil), results...)
	finalSubmitted := submitted
	finalErrors := copyStringInt(errorsByKind)
	mu.Unlock()

	tr := tierResult{
		Endpoint:              ep.Name,
		Protocol:              ep.Protocol,
		Tier:                  tier.Name,
		URL:                   tier.URL,
		ExpectUp:              tier.ExpectUp,
		Method:                tier.Method,
		DetectionProfile:      tier.DetectionProfile,
		BodyReadMaxBytes:      tier.BodyReadMaxBytes,
		Start:                 start,
		End:                   end,
		ConfiguredRatePerMin:  tier.RatePerMin,
		ConfiguredDuration:    tier.Duration.String(),
		ConfiguredConcurrency: tier.Concurrency,
		BatchSize:             batchSize,
		RPCRequests:           len(batches),
		Attempted:             total,
		Submitted:             finalSubmitted,
		ErrorsByKind:          finalErrors,
		TrafficMix:            tier.TrafficMix,
		ResourceSummary:       summarizeResources(sampler.samplesCopy(), sampler.errorsCopy()),
	}
	for _, r := range finalResults {
		if r.TransportErr != "" {
			tr.TransportErrors++
			continue
		}
		if r.Overloaded {
			tr.OverloadResponses++
		}
		if r.Missing {
			tr.MissingResults++
			continue
		}
		tr.Completed++
		if r.Success == tier.ExpectUp {
			tr.ExpectedMatches++
		} else {
			tr.UnexpectedResults++
		}
		if r.EndToEnd > 0 {
			tr.EndToEndLatencyMS = appendStat(tr.EndToEndLatencyMS, float64(r.EndToEnd)/float64(time.Millisecond))
		}
		if r.ProbeRTT > 0 {
			tr.ProbeRTTMS = appendStat(tr.ProbeRTTMS, float64(r.ProbeRTT)/float64(time.Millisecond))
		}
	}
	elapsed := tr.End.Sub(tr.Start).Seconds()
	if elapsed > 0 {
		tr.ThroughputPerSecond = float64(tr.Completed) / elapsed
	}
	if tr.Attempted > 0 {
		tr.CompletionRate = float64(tr.Completed) / float64(tr.Attempted)
		tr.UnexpectedRate = float64(tr.UnexpectedResults) / float64(tr.Attempted)
		tr.TransportErrorRate = float64(tr.TransportErrors) / float64(tr.Attempted)
	}
	tr.EndToEndLatencyMS = finalizeStat(tr.EndToEndLatencyMS)
	tr.ProbeRTTMS = finalizeStat(tr.ProbeRTTMS)
	return tr
}

type benchCheck struct {
	BlogID           int64
	RequestID        string
	URL              string
	Method           string
	DetectionProfile string
	BodyReadMaxBytes int64
	SubmittedAt      time.Time
}

func buildBatches(total, batchSize int, targetURL string, baseID int64, method, profile string, bodyReadMaxBytes int64, trafficMix []trafficMixEntry) [][]benchCheck {
	mix := trafficMix
	if len(mix) == 0 {
		mix = []trafficMixEntry{{
			Method:           method,
			DetectionProfile: profile,
			BodyReadMaxBytes: bodyReadMaxBytes,
			Weight:           1,
		}}
	}
	totalWeight := 0
	for _, entry := range mix {
		totalWeight += entry.Weight
	}
	checks := make([]benchCheck, total)
	for i := 0; i < total; i++ {
		entry := pickTrafficMixEntry(mix, totalWeight, i)
		checks[i] = benchCheck{
			BlogID:           baseID + int64(i),
			RequestID:        "veriflier-bench-" + newID(),
			URL:              targetURL,
			Method:           entry.Method,
			DetectionProfile: entry.DetectionProfile,
			BodyReadMaxBytes: entry.BodyReadMaxBytes,
		}
	}
	var batches [][]benchCheck
	for len(checks) > 0 {
		n := batchSize
		if n > len(checks) {
			n = len(checks)
		}
		batches = append(batches, checks[:n])
		checks = checks[n:]
	}
	return batches
}

func pickTrafficMixEntry(mix []trafficMixEntry, totalWeight int, idx int) trafficMixEntry {
	if totalWeight <= 0 {
		return mix[0]
	}
	offset := idx % totalWeight
	for _, entry := range mix {
		if offset < entry.Weight {
			return entry
		}
		offset -= entry.Weight
	}
	return mix[len(mix)-1]
}

func firstBlogID(parts ...string) int64 {
	h := uint64(1469598103934665603)
	for _, part := range parts {
		for i := 0; i < len(part); i++ {
			h ^= uint64(part[i])
			h *= 1099511628211
		}
	}
	// The v1 Veriflier parses blog_id with Qt's toInt(), so benchmark IDs must
	// stay inside signed 32-bit range or callbacks cannot be matched.
	return int64(920000000 + h%900000000)
}

func sendV1Batch(ctx context.Context, addr, token string, checks []benchCheck) error {
	reqChecks := make([]legacyV1RequestCheck, len(checks))
	now := time.Now()
	for i, check := range checks {
		checks[i].SubmittedAt = now
		reqChecks[i] = legacyV1RequestCheck{BlogID: check.BlogID, MonitorURL: check.URL}
	}
	body, err := json.Marshal(legacyV1Request{AuthToken: token, Checks: reqChecks})
	if err != nil {
		return err
	}
	statusCode, responseBody, err := rawTLSLegacyRequest(ctx, addr, "POST", "/get/host-status", body, 10*time.Second)
	if err != nil {
		return err
	}
	var ack legacyV1Ack
	if err := json.Unmarshal(responseBody, &ack); err != nil {
		return fmt.Errorf("decode ack: %w", err)
	}
	if statusCode != http.StatusOK || ack.Status != 1 {
		return fmt.Errorf("ack status=%d body_status=%d error=%q", statusCode, ack.Status, ack.Error)
	}
	return nil
}

func sendV2Batch(ctx context.Context, ep endpointConfig, checks []benchCheck, timeout time.Duration) []checkResult {
	now := time.Now()
	requests := make([]v2CheckRequest, len(checks))
	for i := range checks {
		checks[i].SubmittedAt = now
		requests[i] = v2CheckRequest{
			RequestID:        checks[i].RequestID,
			BlogID:           checks[i].BlogID,
			URL:              checks[i].URL,
			TimeoutMS:        int64(timeout / time.Millisecond),
			Method:           checks[i].Method,
			DetectionProfile: checks[i].DetectionProfile,
			BodyReadMaxBytes: checks[i].BodyReadMaxBytes,
		}
	}
	body, err := json.Marshal(v2BatchRequest{BatchID: "bench-" + newID(), DeadlineMS: int64((timeout + 2*time.Second) / time.Millisecond), Requests: requests})
	if err != nil {
		return transportFailures(checks, "encode_request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+ep.Addr+"/v2/check", bytes.NewReader(body))
	if err != nil {
		return transportFailures(checks, "build_request")
	}
	req.Header.Set("Authorization", "Bearer "+ep.Token)
	req.Header.Set("Content-Type", "application/json")
	client := ep.HTTPClient
	if client == nil {
		client = newV2BenchmarkHTTPClient(100, 20)
	}
	start := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(start)
	if err != nil {
		return transportFailures(checks, classifyTransportErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusServiceUnavailable {
		return overloadFailures(checks, elapsed)
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 2048))
		return transportFailures(checks, fmt.Sprintf("http_%d", resp.StatusCode))
	}
	var decoded v2BatchResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return transportFailures(checks, "decode_response")
	}
	byID := map[int64]v2CheckResult{}
	for _, res := range decoded.Results {
		byID[res.BlogID] = res
	}
	out := make([]checkResult, 0, len(checks))
	for _, check := range checks {
		res, ok := byID[check.BlogID]
		if !ok {
			out = append(out, checkResult{BlogID: check.BlogID, Missing: true})
			continue
		}
		out = append(out, checkResult{
			BlogID:    check.BlogID,
			Success:   res.Success,
			HTTPCode:  int(res.HTTPCode),
			ErrorCode: int(res.ErrorCode),
			Outcome:   res.Outcome,
			ProbeRTT:  time.Duration(res.RTTMs) * time.Millisecond,
			EndToEnd:  elapsed,
		})
	}
	return out
}

func transportFailures(checks []benchCheck, kind string) []checkResult {
	out := make([]checkResult, len(checks))
	for i, check := range checks {
		out[i] = checkResult{BlogID: check.BlogID, TransportErr: kind}
	}
	return out
}

func overloadFailures(checks []benchCheck, elapsed time.Duration) []checkResult {
	out := make([]checkResult, len(checks))
	for i, check := range checks {
		out[i] = checkResult{BlogID: check.BlogID, TransportErr: "overload_503", Overloaded: true, EndToEnd: elapsed}
	}
	return out
}

func classifyTransportErr(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline"):
		return "transport_timeout"
	case strings.Contains(msg, "connection refused"):
		return "connection_refused"
	case strings.Contains(msg, "reset"):
		return "connection_reset"
	case strings.Contains(msg, "decode"):
		return "decode_error"
	default:
		return "transport_error"
	}
}

func rawTLSLegacyRequest(ctx context.Context, addr, method, path string, body []byte, timeout time.Duration) (int, []byte, error) {
	data, err := rawTLSExchange(ctx, addr, method, path, body, timeout)
	if err != nil {
		return 0, nil, err
	}
	status, payload, parseErr := parseRawHTTPResponse(data)
	if parseErr != nil {
		return 0, nil, parseErr
	}
	return status, payload, nil
}

func rawTLSExchange(ctx context.Context, addr, method, path string, body []byte, timeout time.Duration) ([]byte, error) {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
		MaxVersion:         tls.VersionTLS12,
	})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	req := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		method, path, addr, len(body))
	if _, err := conn.Write(append([]byte(req), body...)); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(conn)
	if err != nil && len(data) == 0 {
		return nil, err
	}
	return data, nil
}

func parseRawHTTPResponse(data []byte) (int, []byte, error) {
	head, body, ok := bytes.Cut(data, []byte("\r\n\r\n"))
	if !ok {
		return 0, nil, fmt.Errorf("response missing header terminator: %q", truncateBytes(data, 200))
	}
	lines := strings.Split(string(head), "\r\n")
	if len(lines) == 0 {
		return 0, nil, fmt.Errorf("empty response headers")
	}
	var status int
	if _, err := fmt.Sscanf(lines[0], "HTTP/1.1 %d", &status); err != nil {
		return 0, nil, fmt.Errorf("parse response status %q: %w", lines[0], err)
	}
	return status, bytes.TrimSpace(body), nil
}

func truncateBytes(data []byte, n int) string {
	if len(data) <= n {
		return string(data)
	}
	return string(data[:n]) + "..."
}

func preflightV1(ctx context.Context, addr string) preflightResult {
	raw, err := rawTLSExchange(ctx, addr, "GET", "/get/status", nil, 5*time.Second)
	if err != nil {
		return preflightResult{Name: "v1 status", Status: "fail", Detail: err.Error()}
	}
	if strings.TrimSpace(string(raw)) == "OK" {
		return preflightResult{Name: "v1 status", Status: "pass", Detail: "legacy TLS /get/status returned raw OK"}
	}
	statusCode, body, err := parseRawHTTPResponse(raw)
	if err != nil {
		return preflightResult{Name: "v1 status", Status: "fail", Detail: err.Error()}
	}
	if statusCode == http.StatusOK && strings.TrimSpace(string(body)) == "OK" {
		return preflightResult{Name: "v1 status", Status: "pass", Detail: "legacy TLS /get/status returned OK"}
	}
	return preflightResult{Name: "v1 status", Status: "fail", Detail: fmt.Sprintf("status=%d body=%q", statusCode, strings.TrimSpace(string(body)))}
}

func preflightV2(ctx context.Context, addr string) preflightResult {
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/v2/status", nil)
	resp, err := client.Do(req)
	if err != nil {
		return preflightResult{Name: "v2 status", Status: "fail", Detail: err.Error()}
	}
	defer resp.Body.Close()
	var status v2Status
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return preflightResult{Name: "v2 status", Status: "fail", Detail: err.Error()}
	}
	if resp.StatusCode != http.StatusOK || status.Status != "OK" {
		return preflightResult{Name: "v2 status", Status: "fail", Detail: fmt.Sprintf("status=%d body_status=%q", resp.StatusCode, status.Status)}
	}
	return preflightResult{Name: "v2 status", Status: "pass", Detail: fmt.Sprintf("version=%s vantage=%s agent=%s capacity=%d+%d", status.Version, status.Vantage.ID, status.Agent.ID, status.Capacity.MaxConcurrency, status.Capacity.QueueCapacity)}
}

func preflightTarget(ctx context.Context, targetURL string, expectUp bool) preflightResult {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, targetURL, nil)
	if err != nil {
		return preflightResult{Name: "target " + targetURL, Status: "fail", Detail: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		if !expectUp {
			return preflightResult{Name: "failure target " + targetURL, Status: "pass", Detail: "target is unreachable as expected: " + err.Error()}
		}
		return preflightResult{Name: "target " + targetURL, Status: "fail", Detail: err.Error()}
	}
	defer resp.Body.Close()
	if expectUp && resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return preflightResult{Name: "target " + targetURL, Status: "pass", Detail: fmt.Sprintf("HEAD status=%d", resp.StatusCode)}
	}
	if !expectUp && resp.StatusCode >= 400 {
		return preflightResult{Name: "failure target " + targetURL, Status: "pass", Detail: fmt.Sprintf("HEAD status=%d", resp.StatusCode)}
	}
	return preflightResult{Name: "target " + targetURL, Status: "fail", Detail: fmt.Sprintf("unexpected HEAD status=%d expect_up=%t", resp.StatusCode, expectUp)}
}

func preflightResourceSampler(ctx context.Context, ep endpointConfig) preflightResult {
	sample, err := captureResourceSample(ctx, ep)
	if err != nil {
		return preflightResult{Name: ep.Name + " resource sampler", Status: "fail", Detail: err.Error()}
	}
	if sample.Missing || sample.PID == 0 {
		return preflightResult{Name: ep.Name + " resource sampler", Status: "fail", Detail: "process not found"}
	}
	return preflightResult{Name: ep.Name + " resource sampler", Status: "pass", Detail: fmt.Sprintf("host=%s pid=%d rss=%.1fMiB fds=%.0f threads=%.0f", ep.SSHHost, sample.PID, sample.RSSBytes/1024/1024, sample.OpenFDs, sample.Threads)}
}

func newV2BenchmarkHTTPClient(maxIdleConns, maxIdleConnsPerHost int) *http.Client {
	if maxIdleConns <= 0 {
		maxIdleConns = 100
	}
	if maxIdleConnsPerHost <= 0 {
		maxIdleConnsPerHost = 20
	}
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          maxIdleConns,
		MaxIdleConnsPerHost:   maxIdleConnsPerHost,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &http.Client{Transport: transport}
}

func startCountedTarget(listen, baseURL string, bodyBytes int) (*countedTarget, error) {
	if bodyBytes < 0 {
		return nil, fmt.Errorf("counted target body bytes must be non-negative")
	}
	if baseURL == "" {
		var err error
		baseURL, err = inferCountedTargetBaseURL(listen)
		if err != nil {
			return nil, err
		}
	}
	body := bytes.Repeat([]byte("x"), bodyBytes)
	target := &countedTarget{
		cfg: countedTargetConfig{
			Listen:    listen,
			BaseURL:   strings.TrimRight(baseURL, "/"),
			BodyBytes: bodyBytes,
			RunID:     time.Now().UTC().Format("20060102T150405Z") + "-" + newID(),
		},
		body:  body,
		stats: map[string]targetCounterStats{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", target.handle)
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, err
	}
	target.listener = ln
	target.srv = &http.Server{Handler: mux}
	go func() {
		if err := target.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("counted target server: %v", err)
		}
	}()
	return target, nil
}

func inferCountedTargetBaseURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		if strings.HasPrefix(listen, ":") {
			host = ""
			port = strings.TrimPrefix(listen, ":")
		} else {
			return "", fmt.Errorf("parse counted target listen address: %w", err)
		}
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host, err = firstPrivateIPv4()
		if err != nil {
			return "", err
		}
	}
	if port == "" {
		return "", fmt.Errorf("counted target listen address must include a port")
	}
	return "http://" + host + ":" + port, nil
}

func firstPrivateIPv4() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip = ip.To4()
			if ip == nil || !ip.IsPrivate() {
				continue
			}
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("could not infer a private IPv4 address for counted target; set -counted-target-base-url")
}

func (c *countedTarget) handle(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Length", strconv.Itoa(len(c.body)))
	switch r.Method {
	case http.MethodHead:
		c.add(path, r.Method, 0)
	case http.MethodGet:
		n, _ := w.Write(c.body)
		c.add(path, r.Method, int64(n))
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		c.add(path, r.Method, 0)
	}
}

func (c *countedTarget) URLFor(endpoint, tier, method, profile string) string {
	url, _ := c.URLAndPathFor(endpoint, tier, method, profile)
	return url
}

func (c *countedTarget) URLAndPathFor(endpoint, tier, method, profile string) (string, string) {
	path := "/veriflier-bench/" + c.cfg.RunID + "/" + safePathPart(endpoint) + "/" + safePathPart(tier) + "/" + safePathPart(method+"-"+profile)
	return c.cfg.BaseURL + path, path
}

func (c *countedTarget) add(path, method string, bytesWritten int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := c.stats[path]
	stats.Requests++
	switch method {
	case http.MethodHead:
		stats.HEADRequests++
	case http.MethodGet:
		stats.GETRequests++
	default:
		stats.OtherRequests++
	}
	stats.BytesWritten += bytesWritten
	c.stats[path] = stats
}

func (c *countedTarget) Observation(path string, expected int) *targetObservation {
	c.mu.Lock()
	stats := c.stats[path]
	c.mu.Unlock()
	obs := &targetObservation{
		Path:             path,
		Requests:         stats.Requests,
		HEADRequests:     stats.HEADRequests,
		GETRequests:      stats.GETRequests,
		OtherRequests:    stats.OtherRequests,
		BytesWritten:     stats.BytesWritten,
		ExpectedRequests: expected,
	}
	if expected > 0 {
		obs.RequestRatio = float64(stats.Requests) / float64(expected)
	}
	return obs
}

func (c *countedTarget) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.srv.Shutdown(ctx)
}

func safePathPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastDash := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		ok := (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9')
		if ok {
			b.WriteByte(ch)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unnamed"
	}
	return out
}

func startCallbackServer(addr string) (*callbackServer, error) {
	cert, err := selfSignedCert()
	if err != nil {
		return nil, err
	}
	cb := &callbackServer{
		callbacks: map[int64]callbackRow{},
		seen:      make(chan struct{}, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/put/host-status", cb.handleStatus)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	ln, err := tls.Listen("tcp", addr, &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		return nil, err
	}
	cb.listener = ln
	cb.srv = &http.Server{Handler: mux}
	go func() {
		if err := cb.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("v1 callback server: %v", err)
		}
	}()
	return cb, nil
}

func (c *callbackServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var payload legacyV1Callback
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "bad callback", http.StatusBadRequest)
		return
	}
	now := time.Now()
	c.mu.Lock()
	for _, row := range payload.Checks {
		c.callbacks[row.BlogID] = callbackRow{Row: row, ReceivedAt: now}
	}
	c.mu.Unlock()
	select {
	case c.seen <- struct{}{}:
	default:
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"response":1}`))
}

func (c *callbackServer) ForgetRange(start int64, count int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := 0; i < count; i++ {
		delete(c.callbacks, start+int64(i))
	}
}

func (c *callbackServer) ResultsForRange(start int64, count int, timeout time.Duration) []callbackRow {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		rows := c.getRange(start, count)
		if len(rows) >= count {
			return rows
		}
		select {
		case <-c.seen:
		case <-tick.C:
		case <-deadline.C:
			return rows
		}
	}
}

func (c *callbackServer) getRange(start int64, count int) []callbackRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]callbackRow, 0, count)
	for i := 0; i < count; i++ {
		if row, ok := c.callbacks[start+int64(i)]; ok {
			out = append(out, row)
		}
	}
	return out
}

func (c *callbackServer) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.srv.Shutdown(ctx)
}

func selfSignedCert() (tls.Certificate, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "uptime-bench-veriflier-bench"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return tls.X509KeyPair(certPEM, keyPEM)
}

func newResourceSampler(ep endpointConfig, interval time.Duration) *resourceSampler {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &resourceSampler{endpoint: ep, interval: interval, stop: make(chan struct{}), done: make(chan struct{})}
}

func (s *resourceSampler) start(ctx context.Context) {
	go func() {
		defer close(s.done)
		s.capture(ctx)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				s.capture(ctx)
				return
			case <-ticker.C:
				s.capture(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (s *resourceSampler) capture(ctx context.Context) {
	sample, err := captureResourceSample(ctx, s.endpoint)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.errs = append(s.errs, err.Error())
		return
	}
	s.samples = append(s.samples, sample)
}

func (s *resourceSampler) stopAndWait() {
	select {
	case <-s.done:
		return
	default:
	}
	close(s.stop)
	<-s.done
}

func (s *resourceSampler) samplesCopy() []resourceSample {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]resourceSample(nil), s.samples...)
}

func (s *resourceSampler) errorsCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.errs...)
}

func captureResourceSample(ctx context.Context, ep endpointConfig) (resourceSample, error) {
	if ep.SSHHost == "" || ep.PIDPattern == "" {
		return resourceSample{}, fmt.Errorf("ssh host and pid pattern are required")
	}
	script := fmt.Sprintf(`pattern=%s
pid=$(pgrep -f "$pattern" | head -n1)
if [ -z "$pid" ]; then
  echo missing=1
  exit 0
fi
echo missing=0
echo pid=$pid
echo time_ns=$(date +%%s%%N)
echo clk_tck=$(getconf CLK_TCK)
awk '/^cpu /{total=0; for(i=2;i<=NF;i++) total+=$i; print "host_total_jiffies=" total; print "host_idle_jiffies=" $5}' /proc/stat
sudo -n awk '{print "proc_jiffies=" $14+$15}' /proc/$pid/stat
sudo -n awk '/VmRSS:/{print "rss_bytes=" $2*1024} /Threads:/{print "threads=" $2}' /proc/$pid/status
echo open_fds=$(sudo -n find /proc/$pid/fd -maxdepth 1 -type l 2>/dev/null | wc -l)
sudo -n awk '/^read_bytes:/{print "read_bytes=" $2} /^write_bytes:/{print "write_bytes=" $2}' /proc/$pid/io
awk -F'[: ]+' 'BEGIN{rx=0;tx=0} $2 != "" {dev=$2; if (dev !~ /^(lo|docker|veth|br-|virbr|tailscale|wwan|wlp)/) {rx+=$3; tx+=$11}} END{print "net_rx_bytes=" rx; print "net_tx_bytes=" tx}' /proc/net/dev
`, shellQuote(ep.PIDPattern))
	cmd := exec.CommandContext(ctx, "ssh", ep.SSHHost, "bash -lc "+shellQuote(script))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return resourceSample{}, fmt.Errorf("ssh resource sample %s: %w: %s", ep.SSHHost, err, strings.TrimSpace(string(out)))
	}
	sample := resourceSample{At: time.Now().UTC(), Host: ep.SSHHost}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		key, val, _ := strings.Cut(line, "=")
		f, _ := strconv.ParseFloat(strings.TrimSpace(val), 64)
		switch strings.TrimSpace(key) {
		case "missing":
			sample.Missing = strings.TrimSpace(val) == "1"
		case "pid":
			sample.PID = int(f)
		case "time_ns":
			sample.TimeNS = int64(f)
		case "clk_tck":
			sample.ClockTicks = f
		case "host_total_jiffies":
			sample.HostTotalJiffies = f
		case "host_idle_jiffies":
			sample.HostIdleJiffies = f
		case "proc_jiffies":
			sample.ProcJiffies = f
		case "rss_bytes":
			sample.RSSBytes = f
		case "threads":
			sample.Threads = f
		case "open_fds":
			sample.OpenFDs = f
		case "read_bytes":
			sample.ReadBytes = f
		case "write_bytes":
			sample.WriteBytes = f
		case "net_rx_bytes":
			sample.NetRXBytes = f
		case "net_tx_bytes":
			sample.NetTXBytes = f
		}
	}
	return sample, nil
}

func summarizeResources(samples []resourceSample, errs []string) resourceSummary {
	out := resourceSummary{Samples: len(samples)}
	if len(errs) > 0 {
		out.Error = strings.Join(errs, "; ")
	}
	if len(samples) == 0 {
		return out
	}
	for _, sample := range samples {
		if !sample.Missing {
			out.RSSBytes = appendStat(out.RSSBytes, sample.RSSBytes)
			out.OpenFDs = appendStat(out.OpenFDs, sample.OpenFDs)
			out.Threads = appendStat(out.Threads, sample.Threads)
		}
	}
	for i := 1; i < len(samples); i++ {
		prev, cur := samples[i-1], samples[i]
		if prev.Missing || cur.Missing || cur.TimeNS <= prev.TimeNS {
			continue
		}
		elapsed := float64(cur.TimeNS-prev.TimeNS) / float64(time.Second)
		if elapsed <= 0 {
			continue
		}
		if cur.ClockTicks > 0 && cur.ProcJiffies >= prev.ProcJiffies {
			out.ProcessCPUPercentCore = appendStat(out.ProcessCPUPercentCore, ((cur.ProcJiffies-prev.ProcJiffies)/cur.ClockTicks)/elapsed*100)
		}
		hostDelta := cur.HostTotalJiffies - prev.HostTotalJiffies
		idleDelta := cur.HostIdleJiffies - prev.HostIdleJiffies
		if hostDelta > 0 && idleDelta >= 0 {
			out.HostCPUPercent = appendStat(out.HostCPUPercent, (1-(idleDelta/hostDelta))*100)
		}
		if cur.ReadBytes >= prev.ReadBytes {
			out.ProcessReadBytesPerSecond = appendStat(out.ProcessReadBytesPerSecond, (cur.ReadBytes-prev.ReadBytes)/elapsed)
		}
		if cur.WriteBytes >= prev.WriteBytes {
			out.ProcessWriteBytesPerSecond = appendStat(out.ProcessWriteBytesPerSecond, (cur.WriteBytes-prev.WriteBytes)/elapsed)
		}
		if cur.NetRXBytes >= prev.NetRXBytes {
			out.HostNetRXBytesPerSecond = appendStat(out.HostNetRXBytesPerSecond, (cur.NetRXBytes-prev.NetRXBytes)/elapsed)
		}
		if cur.NetTXBytes >= prev.NetTXBytes {
			out.HostNetTXBytesPerSecond = appendStat(out.HostNetTXBytesPerSecond, (cur.NetTXBytes-prev.NetTXBytes)/elapsed)
		}
	}
	out.ProcessCPUPercentCore = finalizeStat(out.ProcessCPUPercentCore)
	out.HostCPUPercent = finalizeStat(out.HostCPUPercent)
	out.RSSBytes = finalizeStat(out.RSSBytes)
	out.OpenFDs = finalizeStat(out.OpenFDs)
	out.Threads = finalizeStat(out.Threads)
	out.ProcessReadBytesPerSecond = finalizeStat(out.ProcessReadBytesPerSecond)
	out.ProcessWriteBytesPerSecond = finalizeStat(out.ProcessWriteBytesPerSecond)
	out.HostNetRXBytesPerSecond = finalizeStat(out.HostNetRXBytesPerSecond)
	out.HostNetTXBytesPerSecond = finalizeStat(out.HostNetTXBytesPerSecond)
	return out
}

func collectPrometheus(ctx context.Context, promURL, instance string, start, end time.Time, step, rateWindow time.Duration) ([]capacitybench.SeriesSummary, error) {
	if instance == "" {
		return nil, fmt.Errorf("prometheus instance is required")
	}
	if !end.After(start) {
		return nil, fmt.Errorf("invalid prometheus window")
	}
	regex, err := capacitybench.InstanceRegex([]string{instance})
	if err != nil {
		return nil, err
	}
	client := &capacitybench.PrometheusClient{BaseURL: promURL, Client: &http.Client{Timeout: 30 * time.Second}}
	return capacitybench.Collect(ctx, client, capacitybench.DefaultQueries(regex, rateWindow), start, end, step)
}

func displayResourceSummary(result tierResult) (resourceSummary, string) {
	out := result.ResourceSummary
	source := "ssh-proc"
	container := preferredContainer(result)
	if container == "" {
		return out, source
	}
	applied := false
	if s, ok := findPromSummary(result.PrometheusSummary, "docker_container_cpu_rate", "container", container); ok {
		out.ProcessCPUPercentCore = statFromPromSummary(s)
		applied = true
	}
	if s, ok := findPromSummary(result.PrometheusSummary, "docker_container_memory_working_set", "container", container); ok {
		out.RSSBytes = statFromPromSummary(s)
		applied = true
	}
	if applied {
		source = "dockerstats:" + container + " for CPU/RSS; ssh-proc for FDs/threads/network/I/O"
	}
	return out, source
}

func preferredContainer(result tierResult) string {
	if result.Endpoint != "v2" {
		return ""
	}
	for _, want := range []string{"docker-veriflier-1", "jetmon-v2-veriflier", "veriflier"} {
		for _, summary := range result.PrometheusSummary {
			if summary.Query == "docker_container_cpu_rate" && summary.Labels["container"] == want {
				return want
			}
		}
	}
	for _, summary := range result.PrometheusSummary {
		if summary.Query == "docker_container_cpu_rate" && strings.Contains(strings.ToLower(summary.Labels["container"]), "veriflier") {
			return summary.Labels["container"]
		}
	}
	return ""
}

func findPromSummary(summaries []capacitybench.SeriesSummary, query, label, value string) (capacitybench.SeriesSummary, bool) {
	for _, summary := range summaries {
		if summary.Query == query && summary.Labels[label] == value {
			return summary, true
		}
	}
	return capacitybench.SeriesSummary{}, false
}

func statFromPromSummary(summary capacitybench.SeriesSummary) statBlock {
	return statBlock{
		Count: summary.Samples,
		Min:   summary.Min,
		Avg:   summary.Avg,
		P50:   summary.P50,
		P95:   summary.P95,
		P99:   summary.P95,
		Max:   summary.Max,
	}
}

func appendStat(block statBlock, value float64) statBlock {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return block
	}
	block.values = append(block.values, value)
	return block
}

func finalizeStat(block statBlock) statBlock {
	if len(block.values) == 0 {
		return statBlock{}
	}
	final := statFromValues(block.values)
	block.Count = final.Count
	block.Min = final.Min
	block.Avg = final.Avg
	block.P50 = final.P50
	block.P95 = final.P95
	block.P99 = final.P99
	block.Max = final.Max
	block.values = nil
	return block
}

func statFromValues(values []float64) statBlock {
	if len(values) == 0 {
		return statBlock{}
	}
	sort.Float64s(values)
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return statBlock{
		Count: len(values),
		Min:   values[0],
		Avg:   sum / float64(len(values)),
		P50:   percentile(values, 0.50),
		P95:   percentile(values, 0.95),
		P99:   percentile(values, 0.99),
		Max:   values[len(values)-1],
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func classifySustainability(result tierResult) (bool, string) {
	switch {
	case result.TargetObservation != nil && (result.TargetObservation.RequestRatio < 0.99 || result.TargetObservation.RequestRatio > 1.01):
		return false, fmt.Sprintf("target observed %.2f%% of requested checks", result.TargetObservation.RequestRatio*100)
	case result.CompletionRate < 0.99:
		return false, fmt.Sprintf("completion rate %.2f%% below 99%%", result.CompletionRate*100)
	case result.TransportErrorRate > 0.01:
		return false, fmt.Sprintf("transport error rate %.2f%% above 1%%", result.TransportErrorRate*100)
	case result.UnexpectedRate > 0.01:
		return false, fmt.Sprintf("unexpected result rate %.2f%% above 1%%", result.UnexpectedRate*100)
	case result.EndToEndLatencyMS.Count > 0 && result.EndToEndLatencyMS.P95 > 10000:
		return false, fmt.Sprintf("p95 end-to-end latency %.0fms above 10000ms", result.EndToEndLatencyMS.P95)
	default:
		return true, ""
	}
}

func summarize(results []tierResult) summary {
	out := summary{
		Status:                     "pass",
		MaxSustainableByEndpoint:   map[string]int{},
		SaturationReasonByEndpoint: map[string]string{},
	}
	for _, result := range results {
		if result.Sustainable && result.RateComparable() {
			if result.ConfiguredRatePerMin > out.MaxSustainableByEndpoint[result.Endpoint] {
				out.MaxSustainableByEndpoint[result.Endpoint] = result.ConfiguredRatePerMin
			}
		}
		if !result.Sustainable {
			out.SaturationReasonByEndpoint[result.Endpoint] = result.SaturationReason
		}
	}
	return out
}

func (r tierResult) RateComparable() bool {
	return r.ExpectUp
}

func writeReports(dir string, rep runReport) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(renderMarkdown(rep)), 0o644)
}

func renderMarkdown(rep runReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Jetmon v1 vs v2 Veriflier Benchmark\n\n")
	fmt.Fprintf(&b, "Status: `%s`\n\n", firstNonEmpty(rep.Summary.Status, "running"))
	fmt.Fprintf(&b, "- Started: `%s`\n", rep.StartedAt.Format(time.RFC3339))
	if !rep.FinishedAt.IsZero() {
		fmt.Fprintf(&b, "- Finished: `%s`\n", rep.FinishedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "- Target locality: %s\n\n", rep.TargetLocality)
	if rep.CountedTarget != nil {
		fmt.Fprintf(&b, "- Counted target: `%s` served from `%s` with `%d` GET body bytes\n\n", rep.CountedTarget.BaseURL, rep.CountedTarget.Listen, rep.CountedTarget.BodyBytes)
	}

	fmt.Fprintf(&b, "## Endpoints\n\n")
	fmt.Fprintf(&b, "| Endpoint | Protocol | Address | SSH host | Prometheus instance | Batch size |\n|---|---|---|---|---|---:|\n")
	for _, ep := range rep.Endpoints {
		fmt.Fprintf(&b, "| %s | %s | `%s` | `%s` | `%s` | %d |\n", ep.Name, ep.Protocol, ep.Addr, ep.SSHHost, ep.Instance, ep.BatchSize)
	}

	fmt.Fprintf(&b, "\n## Preflight\n\n")
	fmt.Fprintf(&b, "| Check | Status | Detail |\n|---|---:|---|\n")
	for _, pf := range rep.Preflight {
		fmt.Fprintf(&b, "| %s | `%s` | %s |\n", escapePipe(pf.Name), pf.Status, escapePipe(pf.Detail))
	}

	fmt.Fprintf(&b, "\n## Throughput And Latency\n\n")
	fmt.Fprintf(&b, "| Endpoint | Tier | Mode | Rate/min | Attempted | Completed | RPCs | Target observed | Target/request | Checks/sec | Complete | Unexpected | Transport errors | p95 e2e ms | p99 e2e ms | p95 probe ms | Sustainable |\n|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range rep.Results {
		observed, ratio := targetObservationMarkdown(r.TargetObservation)
		fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %d | %d | %s | %s | %.2f | %.2f%% | %.2f%% | %.2f%% | %.0f | %.0f | %.0f | `%t` |\n",
			r.Endpoint, r.Tier, modeLabel(r.Method, r.DetectionProfile, r.TrafficMix), r.ConfiguredRatePerMin, r.Attempted, r.Completed, r.RPCRequests, observed, ratio, r.ThroughputPerSecond,
			r.CompletionRate*100, r.UnexpectedRate*100, r.TransportErrorRate*100, r.EndToEndLatencyMS.P95, r.EndToEndLatencyMS.P99, r.ProbeRTTMS.P95, r.Sustainable)
	}

	fmt.Fprintf(&b, "\n## Target-Side Counts\n\n")
	fmt.Fprintf(&b, "| Endpoint | Tier | Path | Requests | HEAD | GET | Other | Bytes written MiB | Request ratio |\n|---|---|---|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range rep.Results {
		if r.TargetObservation == nil {
			continue
		}
		obs := r.TargetObservation
		fmt.Fprintf(&b, "| %s | %s | `%s` | %d | %d | %d | %d | %.2f | %.4f |\n",
			r.Endpoint, r.Tier, obs.Path, obs.Requests, obs.HEADRequests, obs.GETRequests, obs.OtherRequests, bytesToMiB(float64(obs.BytesWritten)), obs.RequestRatio)
	}

	fmt.Fprintf(&b, "\n## Resource Curves\n\n")
	fmt.Fprintf(&b, "For v2, CPU and RSS prefer dockerstats for the Veriflier container when available; FDs, threads, host network, and process I/O remain from the SSH `/proc` sampler.\n\n")
	fmt.Fprintf(&b, "| Endpoint | Tier | Source | CPU avg/p95/max %%core | RSS avg/p95/max MiB | FDs avg/p95/max | Threads avg/p95/max | Net RX/TX avg MiB/s | I/O read/write avg KiB/s |\n|---|---|---|---:|---:|---:|---:|---:|---:|\n")
	for _, r := range rep.Results {
		rs := r.DisplayResourceSummary
		source := r.DisplayResourceSource
		if rs.Samples == 0 && rs.RSSBytes.Count == 0 && rs.ProcessCPUPercentCore.Count == 0 {
			rs = r.ResourceSummary
		}
		if source == "" {
			source = "ssh-proc"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %.1f / %.1f / %.1f | %.1f / %.1f / %.1f | %.0f / %.0f / %.0f | %.0f / %.0f / %.0f | %.2f / %.2f | %.1f / %.1f |\n",
			r.Endpoint, r.Tier,
			escapePipe(source),
			rs.ProcessCPUPercentCore.Avg, rs.ProcessCPUPercentCore.P95, rs.ProcessCPUPercentCore.Max,
			bytesToMiB(rs.RSSBytes.Avg), bytesToMiB(rs.RSSBytes.P95), bytesToMiB(rs.RSSBytes.Max),
			rs.OpenFDs.Avg, rs.OpenFDs.P95, rs.OpenFDs.Max,
			rs.Threads.Avg, rs.Threads.P95, rs.Threads.Max,
			bytesToMiB(rs.HostNetRXBytesPerSecond.Avg), bytesToMiB(rs.HostNetTXBytesPerSecond.Avg),
			bytesToKiB(rs.ProcessReadBytesPerSecond.Avg), bytesToKiB(rs.ProcessWriteBytesPerSecond.Avg))
	}

	fmt.Fprintf(&b, "\n## Max Sustainable Throughput\n\n")
	if len(rep.Summary.MaxSustainableByEndpoint) == 0 {
		fmt.Fprintf(&b, "No completed healthy tier has been classified yet.\n")
	} else {
		for _, endpoint := range sortedKeys(rep.Summary.MaxSustainableByEndpoint) {
			fmt.Fprintf(&b, "- `%s`: `%d checks/min` in this campaign\n", endpoint, rep.Summary.MaxSustainableByEndpoint[endpoint])
		}
	}
	if len(rep.Summary.SaturationReasonByEndpoint) > 0 {
		fmt.Fprintf(&b, "\nObserved saturation or limit signals:\n")
		for _, endpoint := range sortedKeys(rep.Summary.SaturationReasonByEndpoint) {
			fmt.Fprintf(&b, "- `%s`: %s\n", endpoint, rep.Summary.SaturationReasonByEndpoint[endpoint])
		}
	}

	fmt.Fprintf(&b, "\n## Notes\n\n")
	for _, note := range rep.Notes {
		fmt.Fprintf(&b, "- %s\n", note)
	}
	return b.String()
}

func bytesToMiB(v float64) float64 {
	return v / 1024 / 1024
}

func bytesToKiB(v float64) float64 {
	return v / 1024
}

func targetObservationMarkdown(obs *targetObservation) (string, string) {
	if obs == nil {
		return "-", "-"
	}
	return strconv.FormatInt(obs.Requests, 10), fmt.Sprintf("%.4f", obs.RequestRatio)
}

func modeLabel(method, profile string, trafficMix []trafficMixEntry) string {
	if len(trafficMix) == 0 {
		return escapePipe(firstNonEmpty(method, "-") + "/" + firstNonEmpty(profile, "-"))
	}
	parts := make([]string, 0, len(trafficMix))
	for _, entry := range trafficMix {
		parts = append(parts, fmt.Sprintf("%s/%s/%dB:%d", entry.Method, entry.DetectionProfile, entry.BodyReadMaxBytes, entry.Weight))
	}
	return escapePipe(strings.Join(parts, ", "))
}

func copyStringInt(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func escapePipe(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
