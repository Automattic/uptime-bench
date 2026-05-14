package jetmoncapacity

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// RunConfig describes an end-to-end Jetmon v1/v2 capacity run.
type RunConfig struct {
	ID                 string                   `toml:"id"`
	PrometheusURL      string                   `toml:"prometheus_url"`
	Instances          []string                 `toml:"instances"`
	Window             WindowConfig             `toml:"window"`
	Targets            TargetConfig             `toml:"targets"`
	TargetPreflight    TargetPreflightConfig    `toml:"target_preflight"`
	TargetObserver     TargetObserverConfig     `toml:"target_observer"`
	CapacityReplay     CapacityReplayConfig     `toml:"capacity_replay"`
	ReplayDetection    ReplayDetectionConfig    `toml:"replay_detection"`
	NetworkBuckets     NetworkBucketsConfig     `toml:"network_buckets"`
	DiskIOAttribution  DiskIOAttributionConfig  `toml:"disk_io_attribution"`
	StreamingTelemetry StreamingTelemetryConfig `toml:"streaming_telemetry"`
	Checks             ChecksConfig             `toml:"checks"`
	Batches            BatchesConfig            `toml:"batches"`
	JetmonV1           ServiceConfig            `toml:"jetmon_v1"`
	JetmonV2           ServiceConfig            `toml:"jetmon_v2"`
	StopThreshold      StopThresholds           `toml:"stop_thresholds"`
}

// WindowConfig controls baseline and Prometheus capture windows.
type WindowConfig struct {
	BaselineDuration string `toml:"baseline_duration"`
	Step             string `toml:"step"`
	RateWindow       string `toml:"rate_window"`
}

// TargetConfig describes the generated synthetic target namespace.
type TargetConfig struct {
	Domain      string `toml:"domain"`
	HostPattern string `toml:"host_pattern"`
	URLPattern  string `toml:"url_pattern"`
	Count       int    `toml:"count"`
	URLStart    int64  `toml:"url_start"`
}

// TargetPreflightConfig controls exact activated-target validation before a
// live capacity window starts.
type TargetPreflightConfig struct {
	SkipHTTP       bool     `toml:"skip_http"`
	Timeout        string   `toml:"timeout"`
	ExpectedStatus int      `toml:"expected_status"`
	CheckSources   []string `toml:"check_sources"`
	DNSResolvers   []string `toml:"dns_resolvers"`
}

// TargetObserverConfig controls target-side black-box traffic observation for
// capacity windows.
type TargetObserverConfig struct {
	Enabled                 bool    `toml:"enabled"`
	TargetControlURL        string  `toml:"target_control_url"`
	TokenEnv                string  `toml:"token_env"`
	TokenFile               string  `toml:"token_file"`
	Timeout                 string  `toml:"timeout"`
	StaleAfter              string  `toml:"stale_after"`
	MaxNeverSeenSites       int     `toml:"max_never_seen_sites"`
	MaxStaleSites           int     `toml:"max_stale_sites"`
	MinExpectedRequestRatio float64 `toml:"min_expected_request_ratio"`
	MaxExpectedRequestRatio float64 `toml:"max_expected_request_ratio"`
}

// CapacityReplayConfig controls deterministic target failures during capacity
// windows. It is intentionally target-side only: the Jetmon services under test
// only observe normal downtime/recovery behavior.
type CapacityReplayConfig struct {
	Enabled          bool                  `toml:"enabled"`
	TargetControlURL string                `toml:"target_control_url"`
	TokenEnv         string                `toml:"token_env"`
	TokenFile        string                `toml:"token_file"`
	Timeout          string                `toml:"timeout"`
	Seed             int64                 `toml:"seed"`
	Events           []CapacityReplayEvent `toml:"events"`
}

// CapacityReplayEvent describes one deterministic failure window to apply to a
// generated host sample.
type CapacityReplayEvent struct {
	ID          string  `toml:"id"`
	Offset      string  `toml:"offset"`
	Duration    string  `toml:"duration"`
	Type        string  `toml:"type"`
	StatusCode  int     `toml:"status_code"`
	Rate        float64 `toml:"rate"`
	Path        string  `toml:"path"`
	Method      string  `toml:"method"`
	HostStart   int64   `toml:"host_start"`
	HostCount   int     `toml:"host_count"`
	SampleCount int     `toml:"sample_count"`
	Seed        int64   `toml:"seed"`
}

// ReplayDetectionConfig controls event-history correlation for capacity replay
// windows. It reads provider/service event history after the replay window and
// before benchmark cleanup changes service state.
type ReplayDetectionConfig struct {
	Enabled                     bool   `toml:"enabled"`
	Timeout                     string `toml:"timeout"`
	V1BridgeURL                 string `toml:"v1_bridge_url"`
	V1TokenEnv                  string `toml:"v1_token_env"`
	V1TokenFile                 string `toml:"v1_token_file"`
	WindowPadding               string `toml:"window_padding"`
	FailOnCheckIntervalMismatch bool   `toml:"fail_on_check_interval_mismatch"`
}

// NetworkBucketsConfig controls per-host counter snapshots that split network
// traffic into coarse operational buckets such as target HTTP, MySQL, DNS, and
// monitoring scrape traffic.
type NetworkBucketsConfig struct {
	Enabled   bool                      `toml:"enabled"`
	SSHConfig string                    `toml:"ssh_config"`
	Table     string                    `toml:"table"`
	Timeout   string                    `toml:"timeout"`
	Hosts     []NetworkBucketHostConfig `toml:"hosts"`
}

// NetworkBucketHostConfig describes one service host where nftables counters
// should be installed and captured.
type NetworkBucketHostConfig struct {
	ID            string `toml:"id"`
	Instance      string `toml:"instance"`
	SSHHost       string `toml:"ssh_host"`
	TargetIP      string `toml:"target_ip"`
	MySQLIP       string `toml:"mysql_ip"`
	MySQLPort     int    `toml:"mysql_port"`
	MonitoringIP  string `toml:"monitoring_ip"`
	BridgeAPIPort int    `toml:"bridge_api_port"`
	APIPort       int    `toml:"api_port"`
	PeerPort      int    `toml:"peer_port"`
}

// DiskIOAttributionConfig controls read-only process, device, and mount
// attribution capture for Jetmon capacity windows.
type DiskIOAttributionConfig struct {
	Enabled         bool                          `toml:"enabled"`
	SSHConfig       string                        `toml:"ssh_config"`
	Timeout         string                        `toml:"timeout"`
	SampleInterval  string                        `toml:"sample_interval"`
	ProcessPatterns []string                      `toml:"process_patterns"`
	MountPaths      []string                      `toml:"mount_paths"`
	Hosts           []DiskIOAttributionHostConfig `toml:"hosts"`
}

// DiskIOAttributionHostConfig describes one service host where disk I/O
// attribution should be captured.
type DiskIOAttributionHostConfig struct {
	ID              string   `toml:"id"`
	Instance        string   `toml:"instance"`
	SSHHost         string   `toml:"ssh_host"`
	ProcessPatterns []string `toml:"process_patterns"`
	MountPaths      []string `toml:"mount_paths"`
}

// StreamingTelemetryConfig controls optional Jetmon v2 streaming-scheduler
// telemetry capture during capacity windows.
type StreamingTelemetryConfig struct {
	Enabled   bool                           `toml:"enabled"`
	SSHConfig string                         `toml:"ssh_config"`
	Timeout   string                         `toml:"timeout"`
	Unit      string                         `toml:"unit"`
	Hosts     []StreamingTelemetryHostConfig `toml:"hosts"`
}

// StreamingTelemetryHostConfig describes one Jetmon host whose streaming
// scheduler logs and dashboard state should be captured.
type StreamingTelemetryHostConfig struct {
	Service      string `toml:"service"`
	SSHHost      string `toml:"ssh_host"`
	Unit         string `toml:"unit"`
	DashboardURL string `toml:"dashboard_url"`
}

// ChecksConfig describes the monitor check cadence.
type ChecksConfig struct {
	Interval string `toml:"interval"`
}

// BatchesConfig describes the increasing active monitor windows.
type BatchesConfig struct {
	Sizes    []int  `toml:"sizes"`
	Duration string `toml:"duration"`
	Cooldown string `toml:"cooldown"`
}

// ServiceConfig describes one Jetmon service under capacity test.
type ServiceConfig struct {
	BridgeURL       string          `toml:"bridge_url"`
	APIURL          string          `toml:"api_url"`
	BulkLifecycle   string          `toml:"bulk_lifecycle"`
	SchedulerEngine string          `toml:"scheduler_engine"`
	Lifecycle       LifecycleConfig `toml:"lifecycle"`
	LifecycleTable  string          `toml:"-"`
}

// LifecycleConfig describes the benchmark-owned database range for one service.
type LifecycleConfig struct {
	Schema           string `toml:"schema"`
	BlogIDStart      int64  `toml:"blog_id_start"`
	Count            int    `toml:"count"`
	BucketMin        int    `toml:"bucket_min"`
	BucketMax        int    `toml:"bucket_max"`
	CheckInterval    string `toml:"check_interval"`
	BatchSize        int    `toml:"batch_size"`
	URLStart         int64  `toml:"url_start"`
	RequestMethod    string `toml:"request_method"`
	DetectionProfile string `toml:"detection_profile"`
	DSN              string `toml:"dsn"`
	DSNEnv           string `toml:"dsn_env"`
	DSNFile          string `toml:"dsn_file"`
}

// StopThresholds are documented in the config and interpreted by reporting.
type StopThresholds struct {
	HostCPUPercent              float64 `toml:"host_cpu_percent"`
	HostMemoryPercent           float64 `toml:"host_memory_percent"`
	RootDiskPercent             float64 `toml:"root_disk_percent"`
	MissedCheckPercent          float64 `toml:"missed_check_percent"`
	ScrapeUpMin                 float64 `toml:"scrape_up_min"`
	DockerstatsScrapeSuccessMin float64 `toml:"dockerstats_scrape_success_min"`
}

// ServiceLifecycle is a normalized lifecycle plan for one Jetmon service.
type ServiceLifecycle struct {
	ID              string
	Config          Config
	DSN             string
	DSNEnv          string
	DSNFile         string
	HasDSN          bool
	APIURL          string
	Bridge          string
	BulkVia         string
	SchedulerEngine string
}

// LoadRunConfig loads a capacity run config from TOML.
func LoadRunConfig(path string) (RunConfig, error) {
	var cfg RunConfig
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return RunConfig{}, err
	}
	return cfg.Normalize(), nil
}

// Normalize fills in conservative defaults.
func (c RunConfig) Normalize() RunConfig {
	if c.ID == "" {
		c.ID = "jetmon-v1-vs-v2-capacity"
	}
	c.Targets.Domain = strings.Trim(strings.TrimSpace(c.Targets.Domain), ".")
	c.Targets.HostPattern = strings.TrimSpace(c.Targets.HostPattern)
	c.Targets.URLPattern = strings.TrimSpace(c.Targets.URLPattern)
	if c.Targets.HostPattern == "" {
		if c.Targets.Domain != "" {
			c.Targets.HostPattern = "site-%07d." + c.Targets.Domain
		} else if c.Targets.URLPattern == "" {
			c.Targets.HostPattern = defaultHostPattern
		}
	}
	if c.Targets.URLPattern == "" {
		if c.Targets.HostPattern != "" {
			c.Targets.URLPattern = "http://" + c.Targets.HostPattern + "/"
		} else {
			c.Targets.URLPattern = defaultURLPattern
		}
	}
	if c.Targets.Count == 0 {
		c.Targets.Count = defaultCount
	}
	if c.Targets.URLStart == 0 {
		c.Targets.URLStart = defaultURLNumberStart
	}
	if c.TargetPreflight.Timeout == "" {
		c.TargetPreflight.Timeout = "5s"
	}
	if c.TargetPreflight.ExpectedStatus == 0 {
		c.TargetPreflight.ExpectedStatus = http.StatusOK
	}
	c.TargetPreflight.CheckSources = normalizeCheckSources(c.TargetPreflight.CheckSources)
	c.TargetPreflight.DNSResolvers = normalizeDNSResolvers(c.TargetPreflight.DNSResolvers)
	c.TargetObserver.TargetControlURL = strings.TrimRight(strings.TrimSpace(c.TargetObserver.TargetControlURL), "/")
	c.TargetObserver.TokenEnv = strings.TrimSpace(c.TargetObserver.TokenEnv)
	c.TargetObserver.TokenFile = strings.TrimSpace(c.TargetObserver.TokenFile)
	if c.TargetObserver.Timeout == "" {
		c.TargetObserver.Timeout = "5s"
	}
	c.CapacityReplay.TargetControlURL = strings.TrimRight(strings.TrimSpace(firstNonEmpty(c.CapacityReplay.TargetControlURL, c.TargetObserver.TargetControlURL)), "/")
	c.CapacityReplay.TokenEnv = strings.TrimSpace(firstNonEmpty(c.CapacityReplay.TokenEnv, c.TargetObserver.TokenEnv))
	c.CapacityReplay.TokenFile = strings.TrimSpace(firstNonEmpty(c.CapacityReplay.TokenFile, c.TargetObserver.TokenFile))
	if c.CapacityReplay.Timeout == "" {
		c.CapacityReplay.Timeout = firstNonEmpty(c.TargetObserver.Timeout, "5s")
	}
	if c.CapacityReplay.Seed == 0 {
		c.CapacityReplay.Seed = 8675309
	}
	for i := range c.CapacityReplay.Events {
		c.CapacityReplay.Events[i] = normalizeCapacityReplayEvent(c.CapacityReplay.Events[i], i)
	}
	c.ReplayDetection.V1BridgeURL = strings.TrimRight(strings.TrimSpace(firstNonEmpty(c.ReplayDetection.V1BridgeURL, c.JetmonV1.BridgeURL)), "/")
	c.ReplayDetection.V1TokenEnv = strings.TrimSpace(c.ReplayDetection.V1TokenEnv)
	c.ReplayDetection.V1TokenFile = strings.TrimSpace(c.ReplayDetection.V1TokenFile)
	if c.ReplayDetection.Timeout == "" {
		c.ReplayDetection.Timeout = "15s"
	}
	if c.ReplayDetection.WindowPadding == "" {
		c.ReplayDetection.WindowPadding = "30s"
	}
	c.NetworkBuckets.SSHConfig = strings.TrimSpace(c.NetworkBuckets.SSHConfig)
	c.NetworkBuckets.Table = strings.TrimSpace(c.NetworkBuckets.Table)
	if c.NetworkBuckets.Table == "" {
		c.NetworkBuckets.Table = "uptime_bench_net_buckets"
	}
	if c.NetworkBuckets.Timeout == "" {
		c.NetworkBuckets.Timeout = "10s"
	}
	for i := range c.NetworkBuckets.Hosts {
		c.NetworkBuckets.Hosts[i] = normalizeNetworkBucketHost(c.NetworkBuckets.Hosts[i])
	}
	c.DiskIOAttribution.SSHConfig = strings.TrimSpace(firstNonEmpty(c.DiskIOAttribution.SSHConfig, c.NetworkBuckets.SSHConfig))
	if c.DiskIOAttribution.Timeout == "" {
		c.DiskIOAttribution.Timeout = firstNonEmpty(c.NetworkBuckets.Timeout, "20s")
	}
	if c.DiskIOAttribution.SampleInterval == "" {
		c.DiskIOAttribution.SampleInterval = "5s"
	}
	c.DiskIOAttribution.ProcessPatterns = normalizeStringListWithDefault(c.DiskIOAttribution.ProcessPatterns, defaultDiskIOProcessPatterns())
	c.DiskIOAttribution.MountPaths = normalizeStringListWithDefault(c.DiskIOAttribution.MountPaths, defaultDiskIOMountPaths())
	for i := range c.DiskIOAttribution.Hosts {
		c.DiskIOAttribution.Hosts[i] = normalizeDiskIOAttributionHost(c.DiskIOAttribution, c.DiskIOAttribution.Hosts[i])
	}
	c.StreamingTelemetry.SSHConfig = strings.TrimSpace(firstNonEmpty(c.StreamingTelemetry.SSHConfig, c.NetworkBuckets.SSHConfig))
	c.StreamingTelemetry.Unit = strings.TrimSpace(c.StreamingTelemetry.Unit)
	if c.StreamingTelemetry.Unit == "" {
		c.StreamingTelemetry.Unit = "jetmon2"
	}
	if c.StreamingTelemetry.Timeout == "" {
		c.StreamingTelemetry.Timeout = firstNonEmpty(c.NetworkBuckets.Timeout, "10s")
	}
	for i := range c.StreamingTelemetry.Hosts {
		c.StreamingTelemetry.Hosts[i] = normalizeStreamingTelemetryHost(c.StreamingTelemetry, c.StreamingTelemetry.Hosts[i])
	}
	c.JetmonV1.SchedulerEngine = normalizeSchedulerEngine(c.JetmonV1.SchedulerEngine)
	c.JetmonV2.SchedulerEngine = normalizeSchedulerEngine(c.JetmonV2.SchedulerEngine)
	if c.Checks.Interval == "" {
		c.Checks.Interval = "1m"
	}
	if c.Window.Step == "" {
		c.Window.Step = "15s"
	}
	if c.Window.RateWindow == "" {
		c.Window.RateWindow = "2m"
	}
	if c.Window.BaselineDuration == "" {
		c.Window.BaselineDuration = "15m"
	}
	if c.Batches.Duration == "" {
		c.Batches.Duration = "30m"
	}
	if c.Batches.Cooldown == "" {
		c.Batches.Cooldown = "5m"
	}
	if len(c.Batches.Sizes) == 0 {
		c.Batches.Sizes = []int{10, 100, 1000}
	}
	if c.JetmonV1.Lifecycle.Schema == "" {
		c.JetmonV1.Lifecycle.Schema = SchemaV1
	}
	if c.JetmonV2.Lifecycle.Schema == "" {
		c.JetmonV2.Lifecycle.Schema = SchemaV2
	}
	return c
}

func normalizeCheckSources(sources []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(sources))
	for _, source := range sources {
		source = strings.TrimSpace(source)
		if source == "" || seen[source] {
			continue
		}
		seen[source] = true
		out = append(out, source)
	}
	if len(out) == 0 {
		return []string{"runner"}
	}
	return out
}

func normalizeDNSResolvers(resolvers []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(resolvers))
	for _, resolver := range resolvers {
		resolver = strings.TrimSpace(resolver)
		if resolver == "" {
			continue
		}
		if !strings.Contains(resolver, ":") {
			resolver += ":53"
		}
		if seen[resolver] {
			continue
		}
		seen[resolver] = true
		out = append(out, resolver)
	}
	return out
}

func normalizeCapacityReplayEvent(event CapacityReplayEvent, index int) CapacityReplayEvent {
	event.ID = strings.TrimSpace(event.ID)
	if event.ID == "" {
		event.ID = fmt.Sprintf("event-%02d", index+1)
	}
	event.Offset = strings.TrimSpace(event.Offset)
	if event.Offset == "" {
		event.Offset = "2m"
	}
	event.Duration = strings.TrimSpace(event.Duration)
	if event.Duration == "" {
		event.Duration = "2m"
	}
	event.Type = strings.TrimSpace(event.Type)
	if event.Type == "" {
		event.Type = "http_status"
	}
	event.Path = strings.TrimSpace(event.Path)
	event.Method = strings.ToUpper(strings.TrimSpace(event.Method))
	if event.Rate == 0 {
		event.Rate = 1
	}
	if event.StatusCode == 0 && event.Type == "http_status" {
		event.StatusCode = http.StatusServiceUnavailable
	}
	return event
}

func normalizeNetworkBucketHost(host NetworkBucketHostConfig) NetworkBucketHostConfig {
	host.ID = strings.TrimSpace(host.ID)
	host.Instance = strings.TrimSpace(host.Instance)
	host.SSHHost = strings.TrimSpace(host.SSHHost)
	if host.SSHHost == "" {
		host.SSHHost = host.Instance
	}
	host.TargetIP = strings.TrimSpace(host.TargetIP)
	host.MySQLIP = strings.TrimSpace(host.MySQLIP)
	host.MonitoringIP = strings.TrimSpace(host.MonitoringIP)
	if host.MySQLPort == 0 {
		host.MySQLPort = 3306
	}
	return host
}

func normalizeDiskIOAttributionHost(cfg DiskIOAttributionConfig, host DiskIOAttributionHostConfig) DiskIOAttributionHostConfig {
	host.ID = strings.TrimSpace(host.ID)
	host.Instance = strings.TrimSpace(host.Instance)
	host.SSHHost = strings.TrimSpace(host.SSHHost)
	if host.SSHHost == "" {
		host.SSHHost = host.Instance
	}
	host.ProcessPatterns = normalizeStringListWithDefault(host.ProcessPatterns, cfg.ProcessPatterns)
	host.MountPaths = normalizeStringListWithDefault(host.MountPaths, cfg.MountPaths)
	return host
}

func normalizeStreamingTelemetryHost(cfg StreamingTelemetryConfig, host StreamingTelemetryHostConfig) StreamingTelemetryHostConfig {
	host.Service = strings.TrimSpace(host.Service)
	host.SSHHost = strings.TrimSpace(host.SSHHost)
	host.Unit = strings.TrimSpace(host.Unit)
	if host.Unit == "" {
		host.Unit = strings.TrimSpace(cfg.Unit)
	}
	host.DashboardURL = strings.TrimRight(strings.TrimSpace(host.DashboardURL), "/")
	return host
}

func normalizeSchedulerEngine(engine string) string {
	return strings.ToLower(strings.TrimSpace(engine))
}

func normalizeStringListWithDefault(values, defaults []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	if len(out) > 0 {
		return out
	}
	return append([]string(nil), defaults...)
}

// Validate checks the run config without requiring live DB credentials.
func (c RunConfig) Validate() error {
	c = c.Normalize()
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("id is required")
	}
	if c.Targets.Count <= 0 {
		return fmt.Errorf("targets.count must be positive")
	}
	if err := validateTargetPattern(c.Targets); err != nil {
		return err
	}
	if _, err := c.TargetPreflightTimeout(); err != nil {
		return err
	}
	if _, err := c.TargetObserverTimeout(); err != nil {
		return err
	}
	if _, err := c.TargetObserverStaleAfter(); err != nil {
		return err
	}
	if c.TargetObserver.Enabled {
		if strings.TrimSpace(c.TargetObserver.TargetControlURL) == "" {
			return fmt.Errorf("target_observer.target_control_url is required when target_observer.enabled=true")
		}
		if c.TargetObserver.MaxNeverSeenSites < -1 {
			return fmt.Errorf("target_observer.max_never_seen_sites must be -1 or greater")
		}
		if c.TargetObserver.MaxStaleSites < -1 {
			return fmt.Errorf("target_observer.max_stale_sites must be -1 or greater")
		}
		if c.TargetObserver.MinExpectedRequestRatio < 0 {
			return fmt.Errorf("target_observer.min_expected_request_ratio must be non-negative")
		}
		if c.TargetObserver.MaxExpectedRequestRatio < 0 {
			return fmt.Errorf("target_observer.max_expected_request_ratio must be non-negative")
		}
		if c.TargetObserver.MinExpectedRequestRatio > 0 &&
			c.TargetObserver.MaxExpectedRequestRatio > 0 &&
			c.TargetObserver.MaxExpectedRequestRatio < c.TargetObserver.MinExpectedRequestRatio {
			return fmt.Errorf("target_observer.max_expected_request_ratio must be greater than or equal to min_expected_request_ratio")
		}
	}
	if _, err := c.CapacityReplayTimeout(); err != nil {
		return err
	}
	if _, err := c.ReplayDetectionTimeout(); err != nil {
		return err
	}
	if _, err := c.ReplayDetectionWindowPadding(); err != nil {
		return err
	}
	if c.CapacityReplay.Enabled {
		if strings.TrimSpace(c.CapacityReplay.TargetControlURL) == "" {
			return fmt.Errorf("capacity_replay.target_control_url is required when capacity_replay.enabled=true")
		}
		if len(c.CapacityReplay.Events) == 0 {
			return fmt.Errorf("capacity_replay.events must contain at least one event when capacity_replay.enabled=true")
		}
		for i, event := range c.CapacityReplay.Events {
			if _, err := capacityReplayEventOffset(event, i); err != nil {
				return err
			}
			if _, err := capacityReplayEventDuration(event, i); err != nil {
				return err
			}
			if event.Rate <= 0 || event.Rate > 1 {
				return fmt.Errorf("capacity_replay.events[%d].rate must be in (0,1]", i)
			}
			if event.SampleCount < 0 {
				return fmt.Errorf("capacity_replay.events[%d].sample_count must be non-negative", i)
			}
			if event.HostCount < 0 {
				return fmt.Errorf("capacity_replay.events[%d].host_count must be non-negative", i)
			}
			if event.StatusCode < 0 || event.StatusCode > 999 {
				return fmt.Errorf("capacity_replay.events[%d].status_code must be between 0 and 999", i)
			}
		}
	}
	if c.ReplayDetection.Enabled {
		if !c.CapacityReplay.Enabled {
			return fmt.Errorf("replay_detection.enabled requires capacity_replay.enabled=true")
		}
	}
	if _, err := c.NetworkBucketsTimeout(); err != nil {
		return err
	}
	if _, err := c.DiskIOAttributionTimeout(); err != nil {
		return err
	}
	if _, err := c.DiskIOAttributionSampleInterval(); err != nil {
		return err
	}
	if _, err := c.StreamingTelemetryTimeout(); err != nil {
		return err
	}
	if err := validateSchedulerEngine("jetmon_v1.scheduler_engine", c.JetmonV1.SchedulerEngine); err != nil {
		return err
	}
	if err := validateSchedulerEngine("jetmon_v2.scheduler_engine", c.JetmonV2.SchedulerEngine); err != nil {
		return err
	}
	if c.NetworkBuckets.Enabled {
		if len(c.NetworkBuckets.Hosts) == 0 {
			return fmt.Errorf("network_buckets.hosts must contain at least one host when network_buckets.enabled=true")
		}
		for i, host := range c.NetworkBuckets.Hosts {
			if strings.TrimSpace(host.ID) == "" {
				return fmt.Errorf("network_buckets.hosts[%d].id is required", i)
			}
			if strings.TrimSpace(host.SSHHost) == "" {
				return fmt.Errorf("network_buckets.hosts[%d].ssh_host is required", i)
			}
		}
	}
	if c.DiskIOAttribution.Enabled {
		if len(c.DiskIOAttribution.Hosts) == 0 && len(c.NetworkBuckets.Hosts) == 0 {
			return fmt.Errorf("disk_io_attribution.hosts or network_buckets.hosts must contain at least one host when disk_io_attribution.enabled=true")
		}
		for i, host := range c.DiskIOAttribution.Hosts {
			if strings.TrimSpace(host.ID) == "" {
				return fmt.Errorf("disk_io_attribution.hosts[%d].id is required", i)
			}
			if strings.TrimSpace(host.SSHHost) == "" {
				return fmt.Errorf("disk_io_attribution.hosts[%d].ssh_host is required", i)
			}
		}
	}
	if c.StreamingTelemetry.Enabled {
		if len(c.StreamingTelemetry.Hosts) == 0 {
			return fmt.Errorf("streaming_telemetry.hosts must contain at least one host when streaming_telemetry.enabled=true")
		}
		for i, host := range c.StreamingTelemetry.Hosts {
			if strings.TrimSpace(host.Service) == "" {
				return fmt.Errorf("streaming_telemetry.hosts[%d].service is required", i)
			}
			if strings.TrimSpace(host.SSHHost) == "" {
				return fmt.Errorf("streaming_telemetry.hosts[%d].ssh_host is required", i)
			}
			if strings.TrimSpace(host.Unit) == "" {
				return fmt.Errorf("streaming_telemetry.hosts[%d].unit is required", i)
			}
		}
	}
	if _, err := c.BatchDuration(); err != nil {
		return err
	}
	if _, err := c.CooldownDuration(); err != nil {
		return err
	}
	if _, err := c.StepDuration(); err != nil {
		return err
	}
	if _, err := c.RateWindowDuration(); err != nil {
		return err
	}
	for i, size := range c.Batches.Sizes {
		if size <= 0 {
			return fmt.Errorf("batch sizes must be positive")
		}
		if i > 0 && size <= c.Batches.Sizes[i-1] {
			return fmt.Errorf("batch sizes must be strictly increasing")
		}
		if size > c.Targets.Count {
			return fmt.Errorf("batch size %d exceeds targets.count %d", size, c.Targets.Count)
		}
	}
	if _, err := c.ServiceLifecycles(nil); err != nil {
		return err
	}
	return nil
}

// BatchDuration returns the active measurement window duration.
func (c RunConfig) BatchDuration() (time.Duration, error) {
	return parseDuration("batches.duration", c.Normalize().Batches.Duration)
}

// CooldownDuration returns the inter-batch cooldown duration.
func (c RunConfig) CooldownDuration() (time.Duration, error) {
	return parseDuration("batches.cooldown", c.Normalize().Batches.Cooldown)
}

// StepDuration returns the Prometheus query_range step.
func (c RunConfig) StepDuration() (time.Duration, error) {
	return parseDuration("window.step", c.Normalize().Window.Step)
}

// RateWindowDuration returns the PromQL rate() window.
func (c RunConfig) RateWindowDuration() (time.Duration, error) {
	return parseDuration("window.rate_window", c.Normalize().Window.RateWindow)
}

// BaselineDuration returns the pre-run baseline capture duration.
func (c RunConfig) BaselineDuration() (time.Duration, error) {
	return parseDuration("window.baseline_duration", c.Normalize().Window.BaselineDuration)
}

// TargetPreflightTimeout returns the per-URL timeout for activated-target checks.
func (c RunConfig) TargetPreflightTimeout() (time.Duration, error) {
	return parseDuration("target_preflight.timeout", c.Normalize().TargetPreflight.Timeout)
}

// CheckIntervalDuration returns the configured monitor check interval.
func (c RunConfig) CheckIntervalDuration() (time.Duration, error) {
	return parseDuration("checks.interval", c.Normalize().Checks.Interval)
}

// TargetObserverTimeout returns the HTTP timeout for target-observer control
// calls.
func (c RunConfig) TargetObserverTimeout() (time.Duration, error) {
	return parseDuration("target_observer.timeout", c.Normalize().TargetObserver.Timeout)
}

// TargetObserverStaleAfter returns the observer staleness threshold. A zero
// duration means the target should use its default of two check intervals.
func (c RunConfig) TargetObserverStaleAfter() (time.Duration, error) {
	raw := strings.TrimSpace(c.Normalize().TargetObserver.StaleAfter)
	if raw == "" {
		return 0, nil
	}
	return parseDuration("target_observer.stale_after", raw)
}

// CapacityReplayTimeout returns the HTTP timeout for target control calls.
func (c RunConfig) CapacityReplayTimeout() (time.Duration, error) {
	return parseDuration("capacity_replay.timeout", c.Normalize().CapacityReplay.Timeout)
}

// ReplayDetectionTimeout returns the timeout for event-history retrieval.
func (c RunConfig) ReplayDetectionTimeout() (time.Duration, error) {
	return parseDuration("replay_detection.timeout", c.Normalize().ReplayDetection.Timeout)
}

// ReplayDetectionWindowPadding returns the extra time included around replay
// event query windows.
func (c RunConfig) ReplayDetectionWindowPadding() (time.Duration, error) {
	return parseDuration("replay_detection.window_padding", c.Normalize().ReplayDetection.WindowPadding)
}

// NetworkBucketsTimeout returns the per-host SSH/nft command timeout.
func (c RunConfig) NetworkBucketsTimeout() (time.Duration, error) {
	return parseDuration("network_buckets.timeout", c.Normalize().NetworkBuckets.Timeout)
}

// DiskIOAttributionTimeout returns the per-host setup and final-snapshot
// timeout for disk I/O attribution.
func (c RunConfig) DiskIOAttributionTimeout() (time.Duration, error) {
	return parseDuration("disk_io_attribution.timeout", c.Normalize().DiskIOAttribution.Timeout)
}

// DiskIOAttributionSampleInterval returns the sampling interval for pidstat
// and iostat captures during the capacity window.
func (c RunConfig) DiskIOAttributionSampleInterval() (time.Duration, error) {
	return parseDuration("disk_io_attribution.sample_interval", c.Normalize().DiskIOAttribution.SampleInterval)
}

// StreamingTelemetryTimeout returns the per-host telemetry capture timeout.
func (c RunConfig) StreamingTelemetryTimeout() (time.Duration, error) {
	return parseDuration("streaming_telemetry.timeout", c.Normalize().StreamingTelemetry.Timeout)
}

func capacityReplayEventOffset(event CapacityReplayEvent, index int) (time.Duration, error) {
	return parseDuration(fmt.Sprintf("capacity_replay.events[%d].offset", index), event.Offset)
}

func capacityReplayEventDuration(event CapacityReplayEvent, index int) (time.Duration, error) {
	return parseDuration(fmt.Sprintf("capacity_replay.events[%d].duration", index), event.Duration)
}

// ServiceLifecycles returns normalized lifecycle plans for selected services.
func (c RunConfig) ServiceLifecycles(selected []string) ([]ServiceLifecycle, error) {
	c = c.Normalize()
	allowed, err := selectedSet(selected)
	if err != nil {
		return nil, err
	}
	services := []struct {
		id  string
		cfg ServiceConfig
	}{
		{id: "jetmon-v1", cfg: c.JetmonV1},
		{id: "jetmon-v2", cfg: c.JetmonV2},
	}

	var out []ServiceLifecycle
	for _, svc := range services {
		if len(allowed) > 0 && !allowed[svc.id] {
			continue
		}
		lifecycle, err := serviceLifecycle(svc.id, svc.cfg, c.Targets, c.Checks)
		if err != nil {
			return nil, err
		}
		out = append(out, lifecycle)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no services selected")
	}
	return out, nil
}

func serviceLifecycle(id string, svc ServiceConfig, target TargetConfig, checks ChecksConfig) (ServiceLifecycle, error) {
	lc := svc.Lifecycle
	if strings.TrimSpace(lc.DSN) != "" {
		return ServiceLifecycle{}, fmt.Errorf("%s lifecycle: inline dsn is not supported; use dsn_env or dsn_file", id)
	}
	if lc.Count == 0 {
		lc.Count = target.Count
	}
	if lc.URLStart == 0 {
		lc.URLStart = target.URLStart
	}
	checkInterval := lc.CheckInterval
	if checkInterval == "" {
		checkInterval = checks.Interval
	}
	checkMinutes, err := durationWholeMinutes(id+".lifecycle.check_interval", checkInterval)
	if err != nil {
		return ServiceLifecycle{}, err
	}
	plan := Config{
		Schema:               lc.Schema,
		BlogIDStart:          lc.BlogIDStart,
		Count:                lc.Count,
		URLPattern:           target.URLPattern,
		URLNumberStart:       lc.URLStart,
		BucketMin:            lc.BucketMin,
		BucketMax:            lc.BucketMax,
		CheckIntervalMinutes: checkMinutes,
		BatchSize:            lc.BatchSize,
		RequestMethod:        lc.RequestMethod,
		DetectionProfile:     lc.DetectionProfile,
	}.Normalize()
	if err := plan.Validate(); err != nil {
		return ServiceLifecycle{}, fmt.Errorf("%s lifecycle: %w", id, err)
	}
	dsn := strings.TrimSpace(lc.DSN)
	if dsn == "" && strings.TrimSpace(lc.DSNEnv) != "" {
		dsn = strings.TrimSpace(os.Getenv(strings.TrimSpace(lc.DSNEnv)))
	}
	if dsn == "" && strings.TrimSpace(lc.DSNFile) != "" {
		path := strings.TrimSpace(lc.DSNFile)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return ServiceLifecycle{
					ID:              id,
					Config:          plan,
					DSNEnv:          strings.TrimSpace(lc.DSNEnv),
					DSNFile:         path,
					APIURL:          strings.TrimSpace(svc.APIURL),
					Bridge:          strings.TrimSpace(svc.BridgeURL),
					BulkVia:         strings.TrimSpace(svc.BulkLifecycle),
					SchedulerEngine: normalizeSchedulerEngine(svc.SchedulerEngine),
				}, nil
			}
			return ServiceLifecycle{}, fmt.Errorf("%s lifecycle: stat dsn_file: %w", id, err)
		}
		if info.IsDir() {
			return ServiceLifecycle{}, fmt.Errorf("%s lifecycle: dsn_file must be a file", id)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return ServiceLifecycle{}, fmt.Errorf("%s lifecycle: dsn_file permissions must not allow group/other access", id)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return ServiceLifecycle{}, fmt.Errorf("%s lifecycle: read dsn_file: %w", id, err)
		}
		dsn = strings.TrimSpace(string(data))
	}
	return ServiceLifecycle{
		ID:              id,
		Config:          plan,
		DSN:             dsn,
		DSNEnv:          strings.TrimSpace(lc.DSNEnv),
		DSNFile:         strings.TrimSpace(lc.DSNFile),
		HasDSN:          dsn != "",
		APIURL:          strings.TrimSpace(svc.APIURL),
		Bridge:          strings.TrimSpace(svc.BridgeURL),
		BulkVia:         strings.TrimSpace(svc.BulkLifecycle),
		SchedulerEngine: normalizeSchedulerEngine(svc.SchedulerEngine),
	}, nil
}

func validateSchedulerEngine(name, engine string) error {
	switch normalizeSchedulerEngine(engine) {
	case "", "legacy", "streaming":
		return nil
	default:
		return fmt.Errorf("%s must be empty, %q, or %q", name, "legacy", "streaming")
	}
}

func parseDuration(name, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return d, nil
}

func durationWholeMinutes(name, raw string) (int, error) {
	d, err := parseDuration(name, raw)
	if err != nil {
		return 0, err
	}
	if d%time.Minute != 0 {
		return 0, fmt.Errorf("%s must be a whole number of minutes", name)
	}
	minutes := int(d / time.Minute)
	if minutes <= 0 || minutes > 65535 {
		return 0, fmt.Errorf("%s must be between 1m and 65535m", name)
	}
	return minutes, nil
}

func selectedSet(selected []string) (map[string]bool, error) {
	out := make(map[string]bool)
	for _, raw := range selected {
		id := strings.ToLower(strings.TrimSpace(raw))
		switch id {
		case "", "all":
			continue
		case "v1":
			id = "jetmon-v1"
		case "v2":
			id = "jetmon-v2"
		case "jetmon-v1", "jetmon-v2":
		default:
			return nil, fmt.Errorf("unknown service %q", raw)
		}
		out[id] = true
	}
	return out, nil
}
