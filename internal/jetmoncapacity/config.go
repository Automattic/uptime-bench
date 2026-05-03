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
	ID              string                `toml:"id"`
	PrometheusURL   string                `toml:"prometheus_url"`
	Instances       []string              `toml:"instances"`
	Window          WindowConfig          `toml:"window"`
	Targets         TargetConfig          `toml:"targets"`
	TargetPreflight TargetPreflightConfig `toml:"target_preflight"`
	Checks          ChecksConfig          `toml:"checks"`
	Batches         BatchesConfig         `toml:"batches"`
	JetmonV1        ServiceConfig         `toml:"jetmon_v1"`
	JetmonV2        ServiceConfig         `toml:"jetmon_v2"`
	StopThreshold   StopThresholds        `toml:"stop_thresholds"`
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
	BridgeURL      string          `toml:"bridge_url"`
	APIURL         string          `toml:"api_url"`
	BulkLifecycle  string          `toml:"bulk_lifecycle"`
	Lifecycle      LifecycleConfig `toml:"lifecycle"`
	LifecycleTable string          `toml:"-"`
}

// LifecycleConfig describes the benchmark-owned database range for one service.
type LifecycleConfig struct {
	Schema        string `toml:"schema"`
	BlogIDStart   int64  `toml:"blog_id_start"`
	Count         int    `toml:"count"`
	BucketMin     int    `toml:"bucket_min"`
	BucketMax     int    `toml:"bucket_max"`
	CheckInterval string `toml:"check_interval"`
	BatchSize     int    `toml:"batch_size"`
	URLStart      int64  `toml:"url_start"`
	DSN           string `toml:"dsn"`
	DSNEnv        string `toml:"dsn_env"`
	DSNFile       string `toml:"dsn_file"`
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
	ID      string
	Config  Config
	DSN     string
	DSNEnv  string
	DSNFile string
	HasDSN  bool
	APIURL  string
	Bridge  string
	BulkVia string
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
					ID:      id,
					Config:  plan,
					DSNEnv:  strings.TrimSpace(lc.DSNEnv),
					DSNFile: path,
					APIURL:  strings.TrimSpace(svc.APIURL),
					Bridge:  strings.TrimSpace(svc.BridgeURL),
					BulkVia: strings.TrimSpace(svc.BulkLifecycle),
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
		ID:      id,
		Config:  plan,
		DSN:     dsn,
		DSNEnv:  strings.TrimSpace(lc.DSNEnv),
		DSNFile: strings.TrimSpace(lc.DSNFile),
		HasDSN:  dsn != "",
		APIURL:  strings.TrimSpace(svc.APIURL),
		Bridge:  strings.TrimSpace(svc.BridgeURL),
		BulkVia: strings.TrimSpace(svc.BulkLifecycle),
	}, nil
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
