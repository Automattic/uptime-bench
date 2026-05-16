package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
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
	"hash/fnv"
	"io"
	"log"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultV1Addr    = "10.0.0.172:7801"
	defaultV2Addr    = "10.0.0.173:7803"
	defaultSQLGZPath = "/home/gaarai/code/jetpack_monitor_sites-2026-05-13-225300.sql.gz"
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
	BatchSize  int          `json:"batch_size"`
	HTTPClient *http.Client `json:"-"`
}

type checkMode struct {
	Name             string `json:"name"`
	Endpoint         string `json:"endpoint"`
	Method           string `json:"method,omitempty"`
	DetectionProfile string `json:"detection_profile,omitempty"`
	BodyReadMaxBytes int64  `json:"body_read_max_bytes,omitempty"`
}

type urlCheck struct {
	SyntheticBlogID int64     `json:"synthetic_blog_id"`
	OriginalBlogID  int64     `json:"original_blog_id,omitempty"`
	URLHash         string    `json:"url_hash"`
	Scheme          string    `json:"scheme,omitempty"`
	Host            string    `json:"host,omitempty"`
	URL             string    `json:"-"`
	SubmittedAt     time.Time `json:"-"`
}

type checkResult struct {
	BlogID       int64         `json:"blog_id"`
	Success      bool          `json:"success"`
	HTTPCode     int           `json:"http_code,omitempty"`
	ErrorCode    int           `json:"error_code,omitempty"`
	Outcome      string        `json:"outcome,omitempty"`
	ProbeRTT     time.Duration `json:"-"`
	EndToEnd     time.Duration `json:"-"`
	TransportErr string        `json:"transport_error,omitempty"`
	Missing      bool          `json:"missing,omitempty"`
	Overloaded   bool          `json:"overloaded,omitempty"`
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

type countedTargetConfig struct {
	Listen    string `json:"listen"`
	BaseURL   string `json:"base_url"`
	BodyBytes int    `json:"body_bytes"`
	RunID     string `json:"run_id"`
}

type countedTarget struct {
	cfg      countedTargetConfig
	srv      *http.Server
	listener net.Listener
	body     []byte
	mu       sync.Mutex
	stats    map[string]targetCounterStats
}

type fixtureCounter interface {
	URLFor(endpoint, tier, method, profile string) string
	URLAndPathFor(endpoint, tier, method, profile string) (string, string)
	Observation(path string, expected int) (*targetObservation, error)
	Config() countedTargetConfig
	Close()
}

type remoteCountedTarget struct {
	cfg    countedTargetConfig
	client *http.Client
}

type targetCounterStats struct {
	Requests      int64 `json:"requests"`
	HEADRequests  int64 `json:"head_requests"`
	GETRequests   int64 `json:"get_requests"`
	OtherRequests int64 `json:"other_requests"`
	BytesWritten  int64 `json:"bytes_written"`
}

type targetObservation struct {
	Path             string  `json:"path"`
	Requests         int64   `json:"requests"`
	HEADRequests     int64   `json:"head_requests"`
	GETRequests      int64   `json:"get_requests"`
	OtherRequests    int64   `json:"other_requests"`
	BytesWritten     int64   `json:"bytes_written"`
	ExpectedRequests int     `json:"expected_requests"`
	RequestRatio     float64 `json:"request_ratio"`
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

type preflightResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type modeResult struct {
	Mode                   string             `json:"mode"`
	Endpoint               string             `json:"endpoint"`
	Method                 string             `json:"method,omitempty"`
	DetectionProfile       string             `json:"detection_profile,omitempty"`
	Start                  time.Time          `json:"start"`
	End                    time.Time          `json:"end"`
	URLCount               int                `json:"url_count"`
	BatchSize              int                `json:"batch_size"`
	RPCRequests            int                `json:"rpc_requests"`
	Submitted              int                `json:"submitted"`
	Completed              int                `json:"completed"`
	ExpectedMatches        int                `json:"expected_matches"`
	Unexpected             int                `json:"unexpected"`
	Missing                int                `json:"missing"`
	TransportErrors        int                `json:"transport_errors"`
	OverloadResponses      int                `json:"overload_responses"`
	ChecksPerSecond        float64            `json:"checks_per_second"`
	CompletionRate         float64            `json:"completion_rate"`
	UnexpectedRate         float64            `json:"unexpected_rate"`
	TransportRate          float64            `json:"transport_error_rate"`
	EndToEndLatencyMS      statBlock          `json:"end_to_end_latency_ms"`
	ProbeRTTMS             statBlock          `json:"probe_rtt_ms"`
	ResourceSummary        resourceSummary    `json:"resource_summary"`
	TargetObservation      *targetObservation `json:"target_observation,omitempty"`
	TargetObservationError string             `json:"target_observation_error,omitempty"`
	ErrorsByKind           map[string]int     `json:"errors_by_kind,omitempty"`
}

type datasetSummary struct {
	Path               string         `json:"path"`
	TotalRows          int            `json:"total_rows"`
	ActiveRows         int            `json:"active_rows"`
	EligibleRows       int            `json:"eligible_rows"`
	SelectedRows       int            `json:"selected_rows"`
	SchemeCounts       map[string]int `json:"scheme_counts"`
	ActiveStatusCounts map[string]int `json:"active_status_counts"`
	SkippedInvalidURL  int            `json:"skipped_invalid_url"`
	Seed               uint64         `json:"seed"`
}

type dryRunPlan struct {
	URLCount                    int            `json:"url_count"`
	Modes                       []checkMode    `json:"modes"`
	ExpectedRequestsByMode      map[string]int `json:"expected_requests_by_mode"`
	ExpectedTotalRequests       int            `json:"expected_total_requests"`
	PerHostConcurrencyLimit     int            `json:"per_host_concurrency_limit"`
	GlobalConcurrencyLimit      int            `json:"global_concurrency_limit"`
	BatchSize                   int            `json:"batch_size"`
	RandomizationStrategy       string         `json:"randomization_strategy"`
	ResourceMeasurement         string         `json:"resource_measurement"`
	NetworkMeasurement          string         `json:"network_measurement"`
	TimeoutPolicy               string         `json:"timeout_policy"`
	RetryPolicy                 string         `json:"retry_policy"`
	OutputSchema                []string       `json:"output_schema"`
	SafetyNotes                 []string       `json:"safety_notes"`
	RealRunRequiresExplicitFlag bool           `json:"real_run_requires_explicit_flag"`
}

type urlOnceReport struct {
	GeneratedAt    time.Time            `json:"generated_at"`
	StartedAt      time.Time            `json:"started_at"`
	FinishedAt     time.Time            `json:"finished_at"`
	Phase          string               `json:"phase"`
	OutDir         string               `json:"out_dir"`
	TargetLocality string               `json:"target_locality"`
	CountedTarget  *countedTargetConfig `json:"counted_target,omitempty"`
	Endpoints      []endpointConfig     `json:"endpoints"`
	Modes          []checkMode          `json:"modes"`
	Preflight      []preflightResult    `json:"preflight"`
	FixtureResults []modeResult         `json:"fixture_results,omitempty"`
	RealResults    []modeResult         `json:"real_results,omitempty"`
	DatasetSummary *datasetSummary      `json:"dataset_summary,omitempty"`
	DryRunPlan     *dryRunPlan          `json:"dry_run_plan,omitempty"`
	Notes          []string             `json:"notes,omitempty"`
}

type siteRow struct {
	SiteID  int64
	BlogID  int64
	URL     string
	Scheme  string
	Host    string
	Status  int
	Hash    uint64
	URLHash string
}

func main() {
	var (
		phase              = flag.String("phase", "fixture-plan", "phase to run: fixture, plan, fixture-plan, real, or target-server")
		sqlGZPath          = flag.String("sql-gz", defaultSQLGZPath, "gzipped jetpack_monitor_sites SQL dump")
		maxURLs            = flag.Int("max-urls", 0, "max active http/https URLs to select; 0 means all eligible URLs for plan/real")
		seed               = flag.Uint64("seed", 20260516, "deterministic selection seed")
		v1Addr             = flag.String("v1-addr", defaultV1Addr, "Jetmon v1 Veriflier legacy TLS address")
		v2Addr             = flag.String("v2-addr", defaultV2Addr, "Jetmon v2 Veriflier v2 HTTP address")
		v1Token            = flag.String("v1-token", "", "Jetmon v1 Veriflier auth token; prefer V1_VERIFLIER_TOKEN or VERIFLIER_AUTH_TOKEN")
		v2Token            = flag.String("v2-token", "", "Jetmon v2 Veriflier auth token; prefer V2_VERIFLIER_TOKEN or VERIFLIER_AUTH_TOKEN")
		v1BatchSize        = flag.Int("v1-batch-size", 25, "checks per v1 legacy request")
		v2BatchSize        = flag.Int("v2-batch-size", 25, "checks per v2 /v2/check request")
		v2MaxIdleConns     = flag.Int("v2-max-idle-conns", 100, "max idle connections for v2 HTTP transport")
		v2MaxIdlePerHost   = flag.Int("v2-max-idle-conns-per-host", 20, "max idle connections per host for v2 HTTP transport")
		requestTimeout     = flag.Duration("request-timeout", 8*time.Second, "per-check request timeout")
		drainTimeout       = flag.Duration("drain-timeout", 45*time.Second, "time to wait for v1 callbacks")
		callbackListen     = flag.String("callback-listen", ":7800", "TLS callback listen address for v1 results")
		countedListen      = flag.String("counted-target-listen", ":18081", "listen address for internal counted fixture target")
		countedBaseURL     = flag.String("counted-target-base-url", "", "base URL Verifliers can reach for counted fixture target")
		externalCounter    = flag.Bool("external-counted-target", false, "use an already-running counted target at -counted-target-base-url")
		countedBodyBytes   = flag.Int("counted-target-body-bytes", 64, "response body bytes served by counted target GET requests")
		fixtureURLs        = flag.Int("fixture-urls", 4, "URLs per mode for the internal fixture")
		resourceInterval   = flag.Duration("resource-sample-interval", 2*time.Second, "SSH /proc resource sample interval")
		v1SSHHost          = flag.String("v1-ssh-host", "jetmon-vm-host-1", "SSH host for v1 Veriflier resource sampling")
		v1PIDPattern       = flag.String("v1-pid-pattern", "^./veriflier start$", "pgrep -f pattern for v1 Veriflier")
		v2SSHHost          = flag.String("v2-ssh-host", "jetmon-vm-host-2", "SSH host for v2 Veriflier resource sampling")
		v2PIDPattern       = flag.String("v2-pid-pattern", "^./veriflier2$", "pgrep -f pattern for v2 Veriflier")
		perHostConcurrency = flag.Int("per-host-concurrency", 1, "planned per-host concurrency for real URL run")
		globalConcurrency  = flag.Int("global-concurrency", 40, "planned global concurrency for real URL run")
		confirmRealRun     = flag.Bool("confirm-real-run", false, "required with -phase=real to contact real URLs")
		outDir             = flag.String("out-dir", "", "report output directory")
	)
	flag.Parse()

	if *phase == "target-server" {
		if err := runCountedTargetServer(*countedListen, *countedBaseURL, *countedBodyBytes); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *v1Token == "" {
		*v1Token = firstNonEmpty(os.Getenv("V1_VERIFLIER_TOKEN"), os.Getenv("VERIFLIER_AUTH_TOKEN"))
	}
	if *v2Token == "" {
		*v2Token = firstNonEmpty(os.Getenv("V2_VERIFLIER_TOKEN"), os.Getenv("VERIFLIER_AUTH_TOKEN"))
	}
	if *v1Token == "" || *v2Token == "" {
		log.Fatal("set v1/v2 Veriflier tokens via flags, V1_VERIFLIER_TOKEN/V2_VERIFLIER_TOKEN, or VERIFLIER_AUTH_TOKEN")
	}
	if *fixtureURLs <= 0 {
		log.Fatal("-fixture-urls must be positive")
	}
	if *perHostConcurrency <= 0 || *globalConcurrency <= 0 {
		log.Fatal("concurrency limits must be positive")
	}
	if *phase == "real" && !*confirmRealRun {
		log.Fatal("-phase=real requires -confirm-real-run")
	}
	if *outDir == "" {
		*outDir = filepath.Join("reports", time.Now().UTC().Format("20060102T150405Z")+"-jetmon-veriflier-url-once")
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		log.Fatalf("create report dir: %v", err)
	}

	modes := defaultModes()
	endpoints := []endpointConfig{
		{Name: "v1", Protocol: "v1-legacy-tls-callback", Addr: *v1Addr, Token: *v1Token, SSHHost: *v1SSHHost, PIDPattern: *v1PIDPattern, BatchSize: *v1BatchSize},
		{Name: "v2", Protocol: "v2-json-http", Addr: *v2Addr, Token: *v2Token, SSHHost: *v2SSHHost, PIDPattern: *v2PIDPattern, BatchSize: *v2BatchSize, HTTPClient: newV2BenchmarkHTTPClient(*v2MaxIdleConns, *v2MaxIdlePerHost)},
	}

	started := time.Now().UTC()
	rep := urlOnceReport{
		GeneratedAt:    started,
		StartedAt:      started,
		Phase:          *phase,
		OutDir:         *outDir,
		TargetLocality: "internal counted fixture plus dry-run planning from SQL backup; no public URLs are contacted unless -phase=real -confirm-real-run is used",
		Endpoints:      scrubEndpoints(endpoints),
		Modes:          modes,
		Notes: []string{
			"Direct Veriflier calls are used; the Monitor scheduler, WPCOM, alerts, and live Jetmon database state are not used by this harness.",
			"Real URL execution is opt-in only and guarded by -confirm-real-run.",
			"Real URL reports should avoid storing full URLs; rows are keyed by synthetic blog ID and URL hash.",
		},
	}

	ctx := context.Background()
	for _, ep := range endpoints {
		switch ep.Name {
		case "v1":
			rep.Preflight = append(rep.Preflight, preflightV1(ctx, ep.Addr))
		case "v2":
			rep.Preflight = append(rep.Preflight, preflightV2(ctx, ep.Addr))
		}
		rep.Preflight = append(rep.Preflight, preflightResourceSampler(ctx, ep))
	}

	var cb *callbackServer
	if phaseNeedsV1(*phase) {
		var err error
		cb, err = startCallbackServer(*callbackListen)
		if err != nil {
			log.Fatalf("start v1 callback server: %v", err)
		}
		defer cb.Close()
	}

	if phaseHasFixture(*phase) {
		var counter fixtureCounter
		if *externalCounter {
			if *countedBaseURL == "" {
				log.Fatal("-external-counted-target requires -counted-target-base-url")
			}
			counter = newRemoteCountedTarget(*countedBaseURL, *countedBodyBytes)
		} else {
			var err error
			counter, err = startCountedTarget(*countedListen, *countedBaseURL, *countedBodyBytes)
			if err != nil {
				log.Fatalf("start counted target: %v", err)
			}
		}
		defer counter.Close()
		cfgCopy := counter.Config()
		rep.CountedTarget = &cfgCopy
		rep.TargetLocality = "internal-only counted HTTP fixture target served by uptime-bench and reached by private LAN address"
		rep.Preflight = append(rep.Preflight, preflightTarget(ctx, counter.URLFor("preflight", "fixture", "HEAD", "legacy"), true))
		log.Printf("running internal fixture against %d URLs per mode", *fixtureURLs)
		rep.FixtureResults = runFixture(ctx, endpoints, modes, cb, counter, *fixtureURLs, *requestTimeout, *drainTimeout, *resourceInterval)
		if err := writeReports(*outDir, rep); err != nil {
			log.Printf("write interim report: %v", err)
		}
	}

	if phaseHasPlan(*phase) || *phase == "real" {
		log.Printf("parsing SQL dump for dry-run plan: %s", *sqlGZPath)
		summary, selected, err := loadAndSelectSites(*sqlGZPath, *seed, *maxURLs)
		if err != nil {
			log.Fatalf("load SQL dump: %v", err)
		}
		rep.DatasetSummary = &summary
		rep.DryRunPlan = buildDryRunPlan(len(selected), modes, *perHostConcurrency, *globalConcurrency, max(*v1BatchSize, *v2BatchSize), *requestTimeout)
		if err := writeReports(*outDir, rep); err != nil {
			log.Printf("write interim report: %v", err)
		}
		if *phase == "real" {
			log.Printf("running real URL one-shot comparison against %d selected URLs", len(selected))
			rep.RealResults = runRealURLComparison(ctx, endpoints, modes, cb, selected, *requestTimeout, *drainTimeout, *resourceInterval)
		}
	}

	rep.FinishedAt = time.Now().UTC()
	if err := writeReports(*outDir, rep); err != nil {
		log.Fatalf("write reports: %v", err)
	}
	log.Printf("url one-shot harness complete report_dir=%s", *outDir)
}

func defaultModes() []checkMode {
	return []checkMode{
		{Name: "v1-legacy", Endpoint: "v1"},
		{Name: "v2-head-legacy", Endpoint: "v2", Method: http.MethodHead, DetectionProfile: "legacy"},
		{Name: "v2-get-simple_http", Endpoint: "v2", Method: http.MethodGet, DetectionProfile: "simple_http"},
		{Name: "v2-get-full", Endpoint: "v2", Method: http.MethodGet, DetectionProfile: "full"},
	}
}

func phaseHasFixture(phase string) bool {
	return phase == "fixture" || phase == "fixture-plan"
}

func phaseHasPlan(phase string) bool {
	return phase == "plan" || phase == "fixture-plan"
}

func phaseNeedsV1(phase string) bool {
	return phase == "fixture" || phase == "fixture-plan" || phase == "real"
}

func runFixture(ctx context.Context, endpoints []endpointConfig, modes []checkMode, cb *callbackServer, counter fixtureCounter, count int, requestTimeout, drainTimeout, resourceInterval time.Duration) []modeResult {
	var results []modeResult
	for _, mode := range modes {
		ep, ok := endpointByName(endpoints, mode.Endpoint)
		if !ok {
			continue
		}
		urlValue, path := counter.URLAndPathFor(mode.Endpoint, "fixture", firstNonEmpty(mode.Method, "legacy"), firstNonEmpty(mode.DetectionProfile, "legacy"))
		checks := make([]urlCheck, 0, count)
		base := firstSyntheticBlogID(mode.Name)
		for i := 0; i < count; i++ {
			checks = append(checks, urlCheck{
				SyntheticBlogID: base + int64(i),
				URL:             urlValue,
				URLHash:         hashString(urlValue),
				Scheme:          "http",
				Host:            hostOf(urlValue),
			})
		}
		result := runMode(ctx, ep, mode, checks, cb, requestTimeout, drainTimeout, resourceInterval)
		obs, err := counter.Observation(path, count)
		if err != nil {
			result.TargetObservationError = err.Error()
			if result.ErrorsByKind == nil {
				result.ErrorsByKind = map[string]int{}
			}
			result.ErrorsByKind["target_observation_error"]++
		} else {
			result.TargetObservation = obs
		}
		results = append(results, result)
	}
	return results
}

func runRealURLComparison(ctx context.Context, endpoints []endpointConfig, modes []checkMode, cb *callbackServer, sites []siteRow, requestTimeout, drainTimeout, resourceInterval time.Duration) []modeResult {
	checks := make([]urlCheck, 0, len(sites))
	for i, site := range sites {
		checks = append(checks, urlCheck{
			SyntheticBlogID: int64(920000000 + i),
			OriginalBlogID:  site.BlogID,
			URL:             site.URL,
			URLHash:         site.URLHash,
			Scheme:          site.Scheme,
			Host:            site.Host,
		})
	}
	var results []modeResult
	for _, mode := range modes {
		ep, ok := endpointByName(endpoints, mode.Endpoint)
		if !ok {
			continue
		}
		results = append(results, runMode(ctx, ep, mode, checks, cb, requestTimeout, drainTimeout, resourceInterval))
	}
	return results
}

func runMode(ctx context.Context, ep endpointConfig, mode checkMode, checks []urlCheck, cb *callbackServer, requestTimeout, drainTimeout, resourceInterval time.Duration) modeResult {
	start := time.Now().UTC()
	batchSize := ep.BatchSize
	if batchSize <= 0 {
		batchSize = 1
	}
	sampler := newResourceSampler(ep, resourceInterval)
	sampler.start(ctx)
	defer sampler.stopAndWait()

	var (
		allResults   []checkResult
		submitted    int
		rpcRequests  int
		errorsByKind = map[string]int{}
	)
	for _, batch := range makeBatches(checks, batchSize) {
		rpcRequests++
		subCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		switch ep.Name {
		case "v1":
			if cb != nil {
				cb.ForgetIDs(checkIDs(batch))
			}
			err := sendV1Batch(subCtx, ep.Addr, ep.Token, batch)
			cancel()
			if err != nil {
				appendResults(&allResults, transportFailures(batch, classifyTransportErr(err)), errorsByKind)
				continue
			}
			submitted += len(batch)
			if cb == nil {
				appendResults(&allResults, transportFailures(batch, "callback_server_missing"), errorsByKind)
				continue
			}
			rows := cb.ResultsForIDs(checkIDs(batch), drainTimeout)
			appendResults(&allResults, convertV1Rows(batch, rows), errorsByKind)
		case "v2":
			res := sendV2Batch(subCtx, ep, mode, batch, requestTimeout)
			cancel()
			for _, item := range res {
				if item.TransportErr == "" {
					submitted++
				}
			}
			appendResults(&allResults, res, errorsByKind)
		default:
			cancel()
			appendResults(&allResults, transportFailures(batch, "unsupported_endpoint"), errorsByKind)
		}
	}

	sampler.stopAndWait()
	end := time.Now().UTC()
	result := modeResult{
		Mode:             mode.Name,
		Endpoint:         ep.Name,
		Method:           mode.Method,
		DetectionProfile: mode.DetectionProfile,
		Start:            start,
		End:              end,
		URLCount:         len(checks),
		BatchSize:        batchSize,
		RPCRequests:      rpcRequests,
		Submitted:        submitted,
		ResourceSummary:  summarizeResources(sampler.samplesCopy(), sampler.errorsCopy()),
		ErrorsByKind:     copyStringInt(errorsByKind),
	}
	for _, res := range allResults {
		if res.TransportErr != "" {
			result.TransportErrors++
			continue
		}
		if res.Overloaded {
			result.OverloadResponses++
		}
		if res.Missing {
			result.Missing++
			continue
		}
		result.Completed++
		if res.Success {
			result.ExpectedMatches++
		} else {
			result.Unexpected++
		}
		if res.EndToEnd > 0 {
			result.EndToEndLatencyMS = appendStat(result.EndToEndLatencyMS, float64(res.EndToEnd)/float64(time.Millisecond))
		}
		if res.ProbeRTT > 0 {
			result.ProbeRTTMS = appendStat(result.ProbeRTTMS, float64(res.ProbeRTT)/float64(time.Millisecond))
		}
	}
	elapsed := result.End.Sub(result.Start).Seconds()
	if elapsed > 0 {
		result.ChecksPerSecond = float64(result.Completed) / elapsed
	}
	if result.URLCount > 0 {
		result.CompletionRate = float64(result.Completed) / float64(result.URLCount)
		result.UnexpectedRate = float64(result.Unexpected) / float64(result.URLCount)
		result.TransportRate = float64(result.TransportErrors) / float64(result.URLCount)
	}
	result.EndToEndLatencyMS = finalizeStat(result.EndToEndLatencyMS)
	result.ProbeRTTMS = finalizeStat(result.ProbeRTTMS)
	return result
}

func appendResults(dst *[]checkResult, items []checkResult, errorsByKind map[string]int) {
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
		*dst = append(*dst, item)
	}
}

func convertV1Rows(checks []urlCheck, rows []callbackRow) []checkResult {
	submitTimes := make(map[int64]time.Time, len(checks))
	for _, check := range checks {
		submitTimes[check.SyntheticBlogID] = check.SubmittedAt
	}
	seen := map[int64]bool{}
	out := make([]checkResult, 0, len(checks))
	for _, row := range rows {
		seen[row.Row.BlogID] = true
		e2e := row.ReceivedAt.Sub(submitTimes[row.Row.BlogID])
		if e2e < 0 {
			e2e = 0
		}
		out = append(out, checkResult{
			BlogID:   row.Row.BlogID,
			Success:  row.Row.Status == 1,
			HTTPCode: row.Row.Code,
			ProbeRTT: time.Duration(row.Row.RTT) * time.Millisecond,
			EndToEnd: e2e,
		})
	}
	for _, check := range checks {
		if !seen[check.SyntheticBlogID] {
			out = append(out, checkResult{BlogID: check.SyntheticBlogID, Missing: true})
		}
	}
	return out
}

func checkIDs(checks []urlCheck) []int64 {
	out := make([]int64, 0, len(checks))
	for _, check := range checks {
		out = append(out, check.SyntheticBlogID)
	}
	return out
}

func makeBatches(checks []urlCheck, batchSize int) [][]urlCheck {
	if batchSize <= 0 {
		batchSize = 1
	}
	var out [][]urlCheck
	for len(checks) > 0 {
		n := batchSize
		if n > len(checks) {
			n = len(checks)
		}
		out = append(out, checks[:n])
		checks = checks[n:]
	}
	return out
}

func endpointByName(endpoints []endpointConfig, name string) (endpointConfig, bool) {
	for _, ep := range endpoints {
		if ep.Name == name {
			return ep, true
		}
	}
	return endpointConfig{}, false
}

func sendV1Batch(ctx context.Context, addr, token string, checks []urlCheck) error {
	reqChecks := make([]legacyV1RequestCheck, len(checks))
	now := time.Now()
	for i, check := range checks {
		checks[i].SubmittedAt = now
		reqChecks[i] = legacyV1RequestCheck{BlogID: check.SyntheticBlogID, MonitorURL: check.URL}
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

func sendV2Batch(ctx context.Context, ep endpointConfig, mode checkMode, checks []urlCheck, timeout time.Duration) []checkResult {
	now := time.Now()
	requests := make([]v2CheckRequest, len(checks))
	for i := range checks {
		checks[i].SubmittedAt = now
		requests[i] = v2CheckRequest{
			RequestID:        mode.Name + "-" + newID(),
			BlogID:           checks[i].SyntheticBlogID,
			URL:              checks[i].URL,
			TimeoutMS:        int64(timeout / time.Millisecond),
			Method:           mode.Method,
			DetectionProfile: mode.DetectionProfile,
			BodyReadMaxBytes: mode.BodyReadMaxBytes,
		}
	}
	body, err := json.Marshal(v2BatchRequest{BatchID: "url-once-" + newID(), DeadlineMS: int64((timeout + 2*time.Second) / time.Millisecond), Requests: requests})
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
		res, ok := byID[check.SyntheticBlogID]
		if !ok {
			out = append(out, checkResult{BlogID: check.SyntheticBlogID, Missing: true})
			continue
		}
		out = append(out, checkResult{
			BlogID:    check.SyntheticBlogID,
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

func transportFailures(checks []urlCheck, kind string) []checkResult {
	out := make([]checkResult, len(checks))
	for i, check := range checks {
		out[i] = checkResult{BlogID: check.SyntheticBlogID, TransportErr: kind}
	}
	return out
}

func overloadFailures(checks []urlCheck, elapsed time.Duration) []checkResult {
	out := make([]checkResult, len(checks))
	for i, check := range checks {
		out[i] = checkResult{BlogID: check.SyntheticBlogID, TransportErr: "overload_503", Overloaded: true, EndToEnd: elapsed}
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

func (c *callbackServer) ForgetIDs(ids []int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		delete(c.callbacks, id)
	}
}

func (c *callbackServer) ResultsForIDs(ids []int64, timeout time.Duration) []callbackRow {
	wanted := make(map[int64]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		rows := c.getIDs(wanted)
		if len(rows) >= len(wanted) {
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

func (c *callbackServer) getIDs(ids map[int64]bool) []callbackRow {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]callbackRow, 0, len(ids))
	for id := range ids {
		if row, ok := c.callbacks[id]; ok {
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
		Subject:      pkix.Name{CommonName: "uptime-bench-veriflier-url-once"},
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
	switch path {
	case "/_uptime_bench_counted_stats":
		c.handleStats(w, r)
		return
	case "/_uptime_bench_counted_reset":
		c.handleReset(w, r)
		return
	}
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

func (c *countedTarget) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c.mu.Lock()
	stats := make(map[string]targetCounterStats, len(c.stats))
	for path, value := range c.stats {
		stats[path] = value
	}
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(stats)
}

func (c *countedTarget) handleReset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	c.mu.Lock()
	c.stats = map[string]targetCounterStats{}
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (c *countedTarget) URLFor(endpoint, tier, method, profile string) string {
	urlValue, _ := c.URLAndPathFor(endpoint, tier, method, profile)
	return urlValue
}

func (c *countedTarget) URLAndPathFor(endpoint, tier, method, profile string) (string, string) {
	path := "/veriflier-url-once/" + c.cfg.RunID + "/" + safePathPart(endpoint) + "/" + safePathPart(tier) + "/" + safePathPart(method+"-"+profile)
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

func (c *countedTarget) Observation(path string, expected int) (*targetObservation, error) {
	c.mu.Lock()
	stats := c.stats[path]
	c.mu.Unlock()
	return targetObservationFromStats(path, stats, expected), nil
}

func (c *countedTarget) Config() countedTargetConfig {
	return c.cfg
}

func (c *countedTarget) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.srv.Shutdown(ctx)
}

func newRemoteCountedTarget(baseURL string, bodyBytes int) *remoteCountedTarget {
	return &remoteCountedTarget{
		cfg: countedTargetConfig{
			Listen:    "external",
			BaseURL:   strings.TrimRight(baseURL, "/"),
			BodyBytes: bodyBytes,
			RunID:     time.Now().UTC().Format("20060102T150405Z") + "-" + newID(),
		},
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

func (r *remoteCountedTarget) URLFor(endpoint, tier, method, profile string) string {
	urlValue, _ := r.URLAndPathFor(endpoint, tier, method, profile)
	return urlValue
}

func (r *remoteCountedTarget) URLAndPathFor(endpoint, tier, method, profile string) (string, string) {
	path := "/veriflier-url-once/" + r.cfg.RunID + "/" + safePathPart(endpoint) + "/" + safePathPart(tier) + "/" + safePathPart(method+"-"+profile)
	return r.cfg.BaseURL + path, path
}

func (r *remoteCountedTarget) Observation(path string, expected int) (*targetObservation, error) {
	resp, err := r.client.Get(r.cfg.BaseURL + "/_uptime_bench_counted_stats")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("stats status=%d", resp.StatusCode)
	}
	var stats map[string]targetCounterStats
	if err := json.NewDecoder(resp.Body).Decode(&stats); err != nil {
		return nil, err
	}
	return targetObservationFromStats(path, stats[path], expected), nil
}

func (r *remoteCountedTarget) Config() countedTargetConfig {
	return r.cfg
}

func (r *remoteCountedTarget) Close() {}

func targetObservationFromStats(path string, stats targetCounterStats, expected int) *targetObservation {
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

func runCountedTargetServer(listen, baseURL string, bodyBytes int) error {
	counter, err := startCountedTarget(listen, baseURL, bodyBytes)
	if err != nil {
		return err
	}
	defer counter.Close()
	log.Printf("counted target server listening on %s advertised as %s", counter.cfg.Listen, counter.cfg.BaseURL)
	log.Printf("stats endpoint: %s/_uptime_bench_counted_stats", counter.cfg.BaseURL)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	return nil
}

func loadAndSelectSites(path string, seed uint64, maxURLs int) (datasetSummary, []siteRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return datasetSummary{}, nil, err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return datasetSummary{}, nil, err
	}
	defer gz.Close()

	summary := datasetSummary{
		Path:               path,
		SchemeCounts:       map[string]int{},
		ActiveStatusCounts: map[string]int{},
		Seed:               seed,
	}
	var rows []siteRow
	reader := bufio.NewReaderSize(gz, 1024*1024)
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 0 && isMonitorInsertLine(line) {
			if err := parseInsertValuesLine(line, func(fields []string) error {
				summary.TotalRows++
				if len(fields) < 8 {
					return fmt.Errorf("expected at least 8 fields, got %d", len(fields))
				}
				active := strings.TrimSpace(fields[4]) == "1"
				if !active {
					return nil
				}
				summary.ActiveRows++
				status := strings.TrimSpace(fields[5])
				summary.ActiveStatusCounts[status]++
				rawURL := fields[3]
				parsed, err := url.Parse(rawURL)
				if err != nil || parsed.Hostname() == "" {
					summary.SkippedInvalidURL++
					return nil
				}
				scheme := strings.ToLower(parsed.Scheme)
				summary.SchemeCounts[scheme]++
				if scheme != "http" && scheme != "https" {
					return nil
				}
				siteID, _ := strconv.ParseInt(fields[0], 10, 64)
				blogID, _ := strconv.ParseInt(fields[1], 10, 64)
				statusInt, _ := strconv.Atoi(status)
				hash := stableURLHash(seed, blogID, rawURL)
				rows = append(rows, siteRow{
					SiteID:  siteID,
					BlogID:  blogID,
					URL:     rawURL,
					Scheme:  scheme,
					Host:    strings.ToLower(parsed.Hostname()),
					Status:  statusInt,
					Hash:    hash,
					URLHash: fmt.Sprintf("%016x", stableURLHash(0, blogID, rawURL)),
				})
				summary.EligibleRows++
				return nil
			}); err != nil {
				return datasetSummary{}, nil, err
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return datasetSummary{}, nil, readErr
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Hash == rows[j].Hash {
			return rows[i].URL < rows[j].URL
		}
		return rows[i].Hash < rows[j].Hash
	})
	if maxURLs > 0 && maxURLs < len(rows) {
		rows = rows[:maxURLs]
	}
	summary.SelectedRows = len(rows)
	return summary, rows, nil
}

func isMonitorInsertLine(line string) bool {
	if !strings.Contains(line, "INSERT INTO") || !strings.Contains(line, "jetpack_monitor_sites") {
		return false
	}
	return strings.Contains(line, "VALUES")
}

func parseInsertValuesLine(line string, cb func([]string) error) error {
	idx := strings.Index(line, "VALUES")
	if idx < 0 {
		return nil
	}
	values := line[idx+len("VALUES"):]
	var (
		fields  []string
		field   strings.Builder
		inRow   bool
		inQuote bool
		quoted  bool
	)
	flushField := func() {
		value := field.String()
		if !quoted {
			value = strings.TrimSpace(value)
		}
		fields = append(fields, value)
		field.Reset()
		quoted = false
	}
	flushRow := func() error {
		if len(fields) > 0 {
			if err := cb(fields); err != nil {
				return err
			}
		}
		fields = nil
		return nil
	}
	for i := 0; i < len(values); i++ {
		ch := values[i]
		if inQuote {
			switch ch {
			case '\\':
				if i+1 < len(values) {
					i++
					field.WriteByte(values[i])
				}
			case '\'':
				if i+1 < len(values) && values[i+1] == '\'' {
					i++
					field.WriteByte('\'')
					continue
				}
				inQuote = false
			default:
				field.WriteByte(ch)
			}
			continue
		}
		switch ch {
		case '(':
			if !inRow {
				inRow = true
				fields = nil
				field.Reset()
				quoted = false
				continue
			}
			field.WriteByte(ch)
		case '\'':
			inQuote = true
			quoted = true
		case ',':
			if inRow {
				flushField()
			}
		case ')':
			if inRow {
				flushField()
				if err := flushRow(); err != nil {
					return err
				}
				inRow = false
			}
		case ';', '\n', '\r':
		default:
			if inRow {
				field.WriteByte(ch)
			}
		}
	}
	if inQuote || inRow {
		return fmt.Errorf("unterminated SQL values row")
	}
	return nil
}

func stableURLHash(seed uint64, blogID int64, rawURL string) uint64 {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%d:%d:%s", seed, blogID, rawURL)
	return h.Sum64()
}

func buildDryRunPlan(urlCount int, modes []checkMode, perHostConcurrency, globalConcurrency, batchSize int, timeout time.Duration) *dryRunPlan {
	expectedByMode := map[string]int{}
	for _, mode := range modes {
		expectedByMode[mode.Name] = urlCount
	}
	return &dryRunPlan{
		URLCount:                urlCount,
		Modes:                   modes,
		ExpectedRequestsByMode:  expectedByMode,
		ExpectedTotalRequests:   urlCount * len(modes),
		PerHostConcurrencyLimit: perHostConcurrency,
		GlobalConcurrencyLimit:  globalConcurrency,
		BatchSize:               batchSize,
		RandomizationStrategy:   "stream active http/https rows from the SQL dump, hash blog_id+url with the configured seed, sort by hash, then take -max-urls if set",
		ResourceMeasurement:     "SSH /proc sampler on each Veriflier host captures process CPU jiffies, RSS, FD count, threads, /proc/<pid>/io read/write bytes, and host CPU jiffies",
		NetworkMeasurement:      "SSH /proc/net/dev deltas on non-loopback/non-container interfaces capture host RX/TX bytes for the Veriflier host over each mode window",
		TimeoutPolicy:           fmt.Sprintf("one Veriflier check attempt per URL per mode with %s per-check timeout; v1 callbacks drain for the configured drain timeout", timeout),
		RetryPolicy:             "zero retries; transport failures and missing callbacks are recorded as first-attempt outcomes",
		OutputSchema: []string{
			"synthetic_blog_id",
			"original_blog_id",
			"url_hash",
			"scheme",
			"host",
			"mode",
			"endpoint",
			"method",
			"detection_profile",
			"success",
			"outcome",
			"http_code",
			"error_code",
			"probe_rtt_ms",
			"end_to_end_ms",
			"transport_error",
			"missing",
			"overloaded",
		},
		SafetyNotes: []string{
			"No Monitor scheduler/event pipeline, WPCOM calls, alerting paths, live Jetmon DB writes, or cadence loop are used.",
			"Each selected real URL is contacted at most once per mode and at most four times total.",
			"The v1 request uses synthetic 32-bit blog IDs so legacy callback matching is not affected by large real blog IDs.",
			"Full real URL execution requires -phase=real -confirm-real-run.",
		},
		RealRunRequiresExplicitFlag: true,
	}
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

func writeReports(dir string, rep urlOnceReport) error {
	data, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(renderMarkdown(rep)), 0o644)
}

func renderMarkdown(rep urlOnceReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Jetmon Veriflier One-Shot URL Harness\n\n")
	fmt.Fprintf(&b, "- Phase: `%s`\n", rep.Phase)
	fmt.Fprintf(&b, "- Started: `%s`\n", rep.StartedAt.Format(time.RFC3339))
	if !rep.FinishedAt.IsZero() {
		fmt.Fprintf(&b, "- Finished: `%s`\n", rep.FinishedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(&b, "- Target locality: %s\n\n", rep.TargetLocality)
	if rep.CountedTarget != nil {
		fmt.Fprintf(&b, "- Counted fixture target: `%s` from `%s`, GET body `%d` bytes\n\n", rep.CountedTarget.BaseURL, rep.CountedTarget.Listen, rep.CountedTarget.BodyBytes)
	}

	fmt.Fprintf(&b, "## Safety Boundary\n\n")
	for _, note := range rep.Notes {
		fmt.Fprintf(&b, "- %s\n", note)
	}

	fmt.Fprintf(&b, "\n## Endpoints\n\n")
	fmt.Fprintf(&b, "| Endpoint | Protocol | Address | SSH host | Batch size |\n|---|---|---|---|---:|\n")
	for _, ep := range rep.Endpoints {
		fmt.Fprintf(&b, "| %s | %s | `%s` | `%s` | %d |\n", ep.Name, ep.Protocol, ep.Addr, ep.SSHHost, ep.BatchSize)
	}

	fmt.Fprintf(&b, "\n## Preflight\n\n")
	fmt.Fprintf(&b, "| Check | Status | Detail |\n|---|---:|---|\n")
	for _, pf := range rep.Preflight {
		fmt.Fprintf(&b, "| %s | `%s` | %s |\n", escapePipe(pf.Name), pf.Status, escapePipe(pf.Detail))
	}

	if len(rep.FixtureResults) > 0 {
		fmt.Fprintf(&b, "\n## Internal Fixture Results\n\n")
		fmt.Fprintf(&b, "| Mode | Endpoint | Method/Profile | URLs | Completed | Expected | Unexpected | Missing | Transport errors | Target requests | HEAD | GET | Request ratio | Target error | p95 e2e ms | p95 probe ms |\n|---|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---|---:|---:|\n")
		for _, r := range rep.FixtureResults {
			reqs, head, get, ratio := "-", "-", "-", "-"
			if r.TargetObservation != nil {
				reqs = strconv.FormatInt(r.TargetObservation.Requests, 10)
				head = strconv.FormatInt(r.TargetObservation.HEADRequests, 10)
				get = strconv.FormatInt(r.TargetObservation.GETRequests, 10)
				ratio = fmt.Sprintf("%.3f", r.TargetObservation.RequestRatio)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %d | %d | %d | %d | %d | %d | %s | %s | %s | %s | %s | %.0f | %.0f |\n",
				r.Mode, r.Endpoint, modeLabel(r.Method, r.DetectionProfile), r.URLCount, r.Completed, r.ExpectedMatches, r.Unexpected, r.Missing, r.TransportErrors, reqs, head, get, ratio, escapePipe(r.TargetObservationError), r.EndToEndLatencyMS.P95, r.ProbeRTTMS.P95)
		}

		fmt.Fprintf(&b, "\n## Fixture Resource Samples\n\n")
		fmt.Fprintf(&b, "| Mode | Endpoint | CPU avg/p95/max %%core | RSS avg/p95/max MiB | FDs avg/p95/max | Threads avg/p95/max | Net RX/TX avg KiB/s | I/O read/write avg KiB/s |\n|---|---|---:|---:|---:|---:|---:|---:|\n")
		for _, r := range rep.FixtureResults {
			rs := r.ResourceSummary
			fmt.Fprintf(&b, "| %s | %s | %.1f / %.1f / %.1f | %.1f / %.1f / %.1f | %.0f / %.0f / %.0f | %.0f / %.0f / %.0f | %.1f / %.1f | %.1f / %.1f |\n",
				r.Mode, r.Endpoint,
				rs.ProcessCPUPercentCore.Avg, rs.ProcessCPUPercentCore.P95, rs.ProcessCPUPercentCore.Max,
				bytesToMiB(rs.RSSBytes.Avg), bytesToMiB(rs.RSSBytes.P95), bytesToMiB(rs.RSSBytes.Max),
				rs.OpenFDs.Avg, rs.OpenFDs.P95, rs.OpenFDs.Max,
				rs.Threads.Avg, rs.Threads.P95, rs.Threads.Max,
				bytesToKiB(rs.HostNetRXBytesPerSecond.Avg), bytesToKiB(rs.HostNetTXBytesPerSecond.Avg),
				bytesToKiB(rs.ProcessReadBytesPerSecond.Avg), bytesToKiB(rs.ProcessWriteBytesPerSecond.Avg))
		}
	}

	if rep.DatasetSummary != nil {
		ds := rep.DatasetSummary
		fmt.Fprintf(&b, "\n## SQL Dataset Summary\n\n")
		fmt.Fprintf(&b, "- Source: `%s`\n", ds.Path)
		fmt.Fprintf(&b, "- Total rows parsed: `%d`\n", ds.TotalRows)
		fmt.Fprintf(&b, "- Active rows: `%d`\n", ds.ActiveRows)
		fmt.Fprintf(&b, "- Eligible active HTTP/HTTPS rows: `%d`\n", ds.EligibleRows)
		fmt.Fprintf(&b, "- Selected rows for this plan/run: `%d`\n", ds.SelectedRows)
		fmt.Fprintf(&b, "- Seed: `%d`\n", ds.Seed)
		fmt.Fprintf(&b, "- Active scheme counts: %s\n", renderIntMap(ds.SchemeCounts))
		fmt.Fprintf(&b, "- Active status counts: %s\n", renderIntMap(ds.ActiveStatusCounts))
		if ds.SkippedInvalidURL > 0 {
			fmt.Fprintf(&b, "- Skipped invalid active URLs: `%d`\n", ds.SkippedInvalidURL)
		}
	}

	if rep.DryRunPlan != nil {
		plan := rep.DryRunPlan
		fmt.Fprintf(&b, "\n## Real URL Dry-Run Plan\n\n")
		fmt.Fprintf(&b, "- URL count: `%d`\n", plan.URLCount)
		fmt.Fprintf(&b, "- Expected total real URL requests: `%d`\n", plan.ExpectedTotalRequests)
		fmt.Fprintf(&b, "- Per-host concurrency limit: `%d`\n", plan.PerHostConcurrencyLimit)
		fmt.Fprintf(&b, "- Global concurrency limit: `%d`\n", plan.GlobalConcurrencyLimit)
		fmt.Fprintf(&b, "- Batch size cap: `%d`\n", plan.BatchSize)
		fmt.Fprintf(&b, "- Randomization: %s\n", plan.RandomizationStrategy)
		fmt.Fprintf(&b, "- Timeout policy: %s\n", plan.TimeoutPolicy)
		fmt.Fprintf(&b, "- Retry policy: %s\n", plan.RetryPolicy)
		fmt.Fprintf(&b, "- Resource measurement: %s\n", plan.ResourceMeasurement)
		fmt.Fprintf(&b, "- Network measurement: %s\n", plan.NetworkMeasurement)
		fmt.Fprintf(&b, "\n| Mode | Endpoint | Method | Profile | Expected requests |\n|---|---|---|---|---:|\n")
		for _, mode := range plan.Modes {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %d |\n", mode.Name, mode.Endpoint, firstNonEmpty(mode.Method, "-"), firstNonEmpty(mode.DetectionProfile, "-"), plan.ExpectedRequestsByMode[mode.Name])
		}
		fmt.Fprintf(&b, "\nSafety notes:\n")
		for _, note := range plan.SafetyNotes {
			fmt.Fprintf(&b, "- %s\n", note)
		}
	}

	if len(rep.RealResults) > 0 {
		fmt.Fprintf(&b, "\n## Real URL Results\n\n")
		fmt.Fprintf(&b, "| Mode | Endpoint | URLs | Completed | Missing | Transport errors | Checks/sec | p95 e2e ms | p95 probe ms |\n|---|---|---:|---:|---:|---:|---:|---:|---:|\n")
		for _, r := range rep.RealResults {
			fmt.Fprintf(&b, "| %s | %s | %d | %d | %d | %d | %.2f | %.0f | %.0f |\n", r.Mode, r.Endpoint, r.URLCount, r.Completed, r.Missing, r.TransportErrors, r.ChecksPerSecond, r.EndToEndLatencyMS.P95, r.ProbeRTTMS.P95)
		}
	}
	return b.String()
}

func renderIntMap(m map[string]int) string {
	if len(m) == 0 {
		return "`{}`"
	}
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, m[key]))
	}
	return "`" + strings.Join(parts, ", ") + "`"
}

func scrubEndpoints(in []endpointConfig) []endpointConfig {
	out := append([]endpointConfig(nil), in...)
	for i := range out {
		out[i].Token = ""
		out[i].HTTPClient = nil
	}
	return out
}

func modeLabel(method, profile string) string {
	if method == "" && profile == "" {
		return "legacy"
	}
	return firstNonEmpty(method, "-") + "/" + firstNonEmpty(profile, "-")
}

func firstSyntheticBlogID(parts ...string) int64 {
	h := uint64(1469598103934665603)
	for _, part := range parts {
		for i := 0; i < len(part); i++ {
			h ^= uint64(part[i])
			h *= 1099511628211
		}
	}
	return int64(920000000 + h%900000000)
}

func hashString(value string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return fmt.Sprintf("%016x", h.Sum64())
}

func hostOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
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

func bytesToMiB(v float64) float64 {
	return v / 1024 / 1024
}

func bytesToKiB(v float64) float64 {
	return v / 1024
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
