package jetmoncapacity

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TargetManifest records the generated capacity target namespace.
type TargetManifest struct {
	Domain                  string   `json:"domain,omitempty"`
	HostPattern             string   `json:"host_pattern,omitempty"`
	URLPattern              string   `json:"url_pattern,omitempty"`
	Count                   int      `json:"count,omitempty"`
	URLStart                int64    `json:"url_start,omitempty"`
	PreflightSkipHTTP       bool     `json:"preflight_skip_http,omitempty"`
	PreflightTimeout        string   `json:"preflight_timeout,omitempty"`
	PreflightExpectedStatus int      `json:"preflight_expected_status,omitempty"`
	PreflightCheckSources   []string `json:"preflight_check_sources,omitempty"`
}

// TargetPreflight records exact activated-URL validation for one service.
type TargetPreflight struct {
	Service        string            `json:"service"`
	Status         string            `json:"status"`
	Error          string            `json:"error,omitempty"`
	SkippedHTTP    bool              `json:"skipped_http,omitempty"`
	SampleCount    int               `json:"sample_count"`
	ExpectedStatus int               `json:"expected_status,omitempty"`
	CheckSources   []string          `json:"check_sources,omitempty"`
	Samples        []TargetURLSample `json:"samples,omitempty"`
}

// TargetURLSample records one activated monitor_url sampled from Jetmon DB.
type TargetURLSample struct {
	BlogID       int64            `json:"blog_id"`
	BucketNo     int              `json:"bucket_no"`
	URL          string           `json:"url"`
	ExpectedURL  string           `json:"expected_url"`
	Host         string           `json:"host"`
	ExpectedHost string           `json:"expected_host"`
	PatternMatch bool             `json:"pattern_match"`
	Checks       []TargetURLCheck `json:"checks,omitempty"`
}

// TargetURLCheck records one DNS/HTTP validation from a configured source.
type TargetURLCheck struct {
	Source      string   `json:"source"`
	DNSOK       bool     `json:"dns_ok"`
	DNSResolver string   `json:"dns_resolver,omitempty"`
	Addresses   []string `json:"addresses,omitempty"`
	HTTPOK      bool     `json:"http_ok"`
	HTTPStatus  int      `json:"http_status,omitempty"`
	Attempts    int      `json:"attempts,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// TargetURLChecker checks exact target URLs before the capacity clock starts.
type TargetURLChecker interface {
	CheckURL(ctx context.Context, source string, rawURL string, timeout time.Duration, expectedStatus int) TargetURLCheck
}

type defaultTargetURLChecker struct{}

func (defaultTargetURLChecker) CheckURL(ctx context.Context, source string, rawURL string, timeout time.Duration, expectedStatus int) TargetURLCheck {
	source = strings.TrimSpace(source)
	if source == "" {
		source = "runner"
	}
	check := TargetURLCheck{Source: source}
	if source != "runner" {
		check.Error = fmt.Sprintf("source %q requires a source-aware TargetURLChecker", source)
		return check
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		check.Error = "parse URL: " + err.Error()
		return check
	}
	host := parsed.Hostname()
	if host == "" {
		check.Error = "URL missing hostname"
		return check
	}
	addrs, resolverName, err := lookupTargetHost(ctx, host, timeout)
	if err != nil {
		check.Error = "dns: " + err.Error()
		return check
	}
	check.DNSOK = true
	check.DNSResolver = resolverName
	check.Addresses = addrs

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		check.Error = "request: " + err.Error()
		return check
	}
	client := resolvedHTTPClient(parsed, addrs, timeout)
	resp, err := client.Do(req)
	if err != nil {
		check.Error = "http: " + err.Error()
		return check
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	check.HTTPStatus = resp.StatusCode
	check.HTTPOK = statusMatches(resp.StatusCode, expectedStatus)
	if !check.HTTPOK {
		check.Error = fmt.Sprintf("http status %d, want %s", resp.StatusCode, expectedStatusText(expectedStatus))
	}
	return check
}

var targetDNSFallbackServers = []string{
	"1.1.1.1:53",
	"8.8.8.8:53",
}

func lookupTargetHost(ctx context.Context, host string, timeout time.Duration) ([]string, string, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, timeout)
	addrs, err := net.DefaultResolver.LookupHost(lookupCtx, host)
	cancel()
	if err == nil {
		return addrs, "default", nil
	}
	var failures []string
	failures = append(failures, "default: "+err.Error())

	for _, server := range targetDNSFallbackServers {
		resolver := fallbackResolver(server, timeout)
		lookupCtx, cancel := context.WithTimeout(ctx, timeout)
		addrs, err := resolver.LookupHost(lookupCtx, host)
		cancel()
		if err == nil {
			return addrs, server, nil
		}
		failures = append(failures, server+": "+err.Error())
	}
	return nil, "", fmt.Errorf("%s", strings.Join(failures, "; "))
}

func fallbackResolver(server string, timeout time.Duration) *net.Resolver {
	dialer := &net.Dialer{Timeout: timeout}
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, server)
		},
	}
}

func resolvedHTTPClient(parsed *url.URL, addrs []string, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	targetHost := normalizeHostname(parsed.Hostname())
	targetPort := parsed.Port()
	if targetPort == "" {
		targetPort = defaultURLPort(parsed.Scheme)
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err == nil && normalizeHostname(host) == targetHost && port == targetPort && len(addrs) > 0 {
			return dialResolvedAddresses(ctx, dialer, network, port, addrs)
		}
		return dialer.DialContext(ctx, network, address)
	}
	return &http.Client{Timeout: timeout, Transport: transport}
}

func dialResolvedAddresses(ctx context.Context, dialer *net.Dialer, network, port string, addrs []string) (net.Conn, error) {
	var failures []string
	for _, addr := range addrs {
		conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr, port))
		if err == nil {
			return conn, nil
		}
		failures = append(failures, addr+": "+err.Error())
	}
	return nil, fmt.Errorf("dial resolved addresses: %s", strings.Join(failures, "; "))
}

func defaultURLPort(scheme string) string {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "https":
		return "443"
	default:
		return "80"
	}
}

func validateTargetPattern(target TargetConfig) error {
	target = RunConfig{Targets: target}.Normalize().Targets
	if strings.TrimSpace(target.HostPattern) == "" {
		return nil
	}
	for _, number := range targetSampleNumbers(target.URLStart, target.Count) {
		expectedHost, err := formatTargetHost(target.HostPattern, number)
		if err != nil {
			return fmt.Errorf("targets.host_pattern: %w", err)
		}
		rawURL, err := formatMonitorURL(target.URLPattern, number)
		if err != nil {
			return fmt.Errorf("targets.url_pattern: %w", err)
		}
		actualHost, err := hostnameFromURL(rawURL)
		if err != nil {
			return fmt.Errorf("targets.url_pattern: %w", err)
		}
		if normalizeHostname(actualHost) != normalizeHostname(expectedHost) {
			return fmt.Errorf("targets.url_pattern host %q does not match targets.host_pattern %q for generated number %d", actualHost, expectedHost, number)
		}
	}
	return nil
}

func targetManifest(cfg RunConfig) TargetManifest {
	cfg = cfg.Normalize()
	return TargetManifest{
		Domain:                  cfg.Targets.Domain,
		HostPattern:             cfg.Targets.HostPattern,
		URLPattern:              cfg.Targets.URLPattern,
		Count:                   cfg.Targets.Count,
		URLStart:                cfg.Targets.URLStart,
		PreflightSkipHTTP:       cfg.TargetPreflight.SkipHTTP,
		PreflightTimeout:        cfg.TargetPreflight.Timeout,
		PreflightExpectedStatus: cfg.TargetPreflight.ExpectedStatus,
		PreflightCheckSources:   append([]string(nil), cfg.TargetPreflight.CheckSources...),
	}
}

func targetSampleNumbers(start int64, count int) []int64 {
	if count <= 0 {
		return nil
	}
	last := start + int64(count) - 1
	values := []int64{start, start + int64(count/2), last}
	return uniqueInt64s(values)
}

func sampleOffsets(activeCount, bucketCount int) []int {
	if activeCount <= 0 {
		return nil
	}
	values := []int{0, activeCount / 2, activeCount - 1}
	for offset := 0; offset < activeCount && offset < bucketCount; offset++ {
		values = append(values, offset)
	}
	return uniqueInts(values)
}

func uniqueInts(values []int) []int {
	seen := make(map[int]bool, len(values))
	var out []int
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Ints(out)
	return out
}

func uniqueInt64s(values []int64) []int64 {
	seen := make(map[int64]bool, len(values))
	var out []int64
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func formatTargetHost(pattern string, number int64) (string, error) {
	host := fmt.Sprintf(pattern, number)
	if strings.Contains(host, "%!") {
		return "", fmt.Errorf("must contain exactly one fmt integer placeholder")
	}
	if strings.Contains(host, "://") || strings.ContainsAny(host, "/?#") {
		return "", fmt.Errorf("must be a hostname pattern, not a URL")
	}
	return host, nil
}

func hostnameFromURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	host := parsed.Hostname()
	if host == "" {
		return "", fmt.Errorf("URL missing hostname")
	}
	return host, nil
}

func normalizeHostname(host string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(host)), ".")
}

func statusMatches(got, expected int) bool {
	if expected == 0 {
		return got >= 200 && got < 300
	}
	return got == expected
}

func expectedStatusText(expected int) string {
	if expected == 0 {
		return "2xx"
	}
	return strconv.Itoa(expected)
}

const targetURLCheckAttempts = 3

var targetURLCheckRetryDelays = []time.Duration{
	250 * time.Millisecond,
	750 * time.Millisecond,
}

func (r Runner) preflightActivatedTargets(ctx context.Context, services []ServiceLifecycle, cfg RunConfig, activeCount int, m *RunManifest) error {
	timeout, err := cfg.TargetPreflightTimeout()
	if err != nil {
		return err
	}
	for _, service := range services {
		preflight, err := r.preflightServiceTargets(ctx, service, cfg, activeCount, timeout)
		m.TargetPreflights = append(m.TargetPreflights, preflight)
		if err != nil {
			return fmt.Errorf("%s target preflight: %w", service.ID, err)
		}
	}
	return nil
}

func (r Runner) preflightServiceTargets(ctx context.Context, service ServiceLifecycle, cfg RunConfig, activeCount int, timeout time.Duration) (TargetPreflight, error) {
	cfg = cfg.Normalize()
	preflight := TargetPreflight{
		Service:        service.ID,
		Status:         "running",
		SkippedHTTP:    cfg.TargetPreflight.SkipHTTP,
		ExpectedStatus: cfg.TargetPreflight.ExpectedStatus,
		CheckSources:   append([]string(nil), cfg.TargetPreflight.CheckSources...),
	}
	sqlText, err := RenderActiveURLSamplesSQL(service.Config, activeCount)
	if err != nil {
		preflight.Status = "fail"
		preflight.Error = err.Error()
		return preflight, err
	}
	result, err := r.execServiceSQL(ctx, service, sqlText)
	if err != nil {
		preflight.Status = "fail"
		preflight.Error = err.Error()
		return preflight, err
	}
	samples, err := targetSamplesFromResult(service.Config, result, activeCount)
	if err != nil {
		preflight.Status = "fail"
		preflight.Error = err.Error()
		preflight.Samples = samples
		preflight.SampleCount = len(samples)
		return preflight, err
	}
	for i := range samples {
		if !samples[i].PatternMatch {
			err = fmt.Errorf("activated URL %s for blog_id %d does not match expected %s", samples[i].URL, samples[i].BlogID, samples[i].ExpectedURL)
			preflight.Status = "fail"
			preflight.Error = err.Error()
			preflight.Samples = samples
			preflight.SampleCount = len(samples)
			return preflight, err
		}
		if !cfg.TargetPreflight.SkipHTTP {
			for _, source := range cfg.TargetPreflight.CheckSources {
				check := r.checkTargetURLWithRetry(ctx, source, samples[i].URL, timeout, cfg.TargetPreflight.ExpectedStatus)
				samples[i].Checks = append(samples[i].Checks, check)
				if !check.DNSOK || !check.HTTPOK {
					err = fmt.Errorf("activated URL %s failed %s DNS/HTTP check: %s", samples[i].URL, check.Source, check.Error)
					preflight.Status = "fail"
					preflight.Error = err.Error()
					preflight.Samples = samples
					preflight.SampleCount = len(samples)
					return preflight, err
				}
			}
		}
	}
	preflight.Status = "pass"
	preflight.Samples = samples
	preflight.SampleCount = len(samples)
	return preflight, nil
}

func (r Runner) checkTargetURLWithRetry(ctx context.Context, source string, rawURL string, timeout time.Duration, expectedStatus int) TargetURLCheck {
	if r.URLChecker == nil {
		r.URLChecker = defaultTargetURLChecker{}
	}
	source = strings.TrimSpace(source)
	if source == "" {
		source = "runner"
	}
	var last TargetURLCheck
	for attempt := 1; attempt <= targetURLCheckAttempts; attempt++ {
		last = r.URLChecker.CheckURL(ctx, source, rawURL, timeout, expectedStatus)
		if strings.TrimSpace(last.Source) == "" {
			last.Source = source
		}
		last.Attempts = attempt
		if last.DNSOK && last.HTTPOK {
			return last
		}
		if attempt == targetURLCheckAttempts {
			break
		}
		delay := targetURLCheckRetryDelay(attempt)
		if delay <= 0 || r.Sleeper == nil {
			continue
		}
		if err := r.Sleeper.Sleep(ctx, delay); err != nil {
			last.Error = appendTargetCheckError(last.Error, fmt.Sprintf("retry wait interrupted after %d/%d attempts: %v", attempt, targetURLCheckAttempts, err))
			return last
		}
	}
	if strings.TrimSpace(last.Error) == "" {
		last.Error = "check did not pass"
	}
	last.Error = appendTargetCheckError(last.Error, fmt.Sprintf("after %d attempts", targetURLCheckAttempts))
	last.Attempts = targetURLCheckAttempts
	return last
}

func targetURLCheckRetryDelay(attempt int) time.Duration {
	index := attempt - 1
	if index < 0 || index >= len(targetURLCheckRetryDelays) {
		return 0
	}
	return targetURLCheckRetryDelays[index]
}

func appendTargetCheckError(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	if base == "" {
		return extra
	}
	if extra == "" {
		return base
	}
	return base + " (" + extra + ")"
}

func targetSamplesFromResult(cfg Config, result SQLExecutionResult, activeCount int) ([]TargetURLSample, error) {
	cfg = cfg.Normalize()
	expectedOffsets := sampleOffsets(activeCount, cfg.BucketMax-cfg.BucketMin+1)
	expectedByBlogID := make(map[int64]bool, len(expectedOffsets))
	for _, offset := range expectedOffsets {
		expectedByBlogID[cfg.BlogIDStart+int64(offset)] = true
	}

	var samples []TargetURLSample
	for _, stmt := range result.Statements {
		blogIdx := columnIndex(stmt.Columns, "blog_id")
		bucketIdx := columnIndex(stmt.Columns, "bucket_no")
		urlIdx := columnIndex(stmt.Columns, "monitor_url")
		if blogIdx == -1 || bucketIdx == -1 || urlIdx == -1 {
			continue
		}
		for _, row := range stmt.Rows {
			if len(row) <= blogIdx || len(row) <= bucketIdx || len(row) <= urlIdx {
				continue
			}
			blogID, err := strconv.ParseInt(row[blogIdx], 10, 64)
			if err != nil {
				return samples, fmt.Errorf("parse sampled blog_id %q: %w", row[blogIdx], err)
			}
			bucket, err := strconv.Atoi(row[bucketIdx])
			if err != nil {
				return samples, fmt.Errorf("parse sampled bucket_no %q: %w", row[bucketIdx], err)
			}
			offset := blogID - cfg.BlogIDStart
			if offset < 0 || offset >= int64(cfg.Count) {
				return samples, fmt.Errorf("sampled blog_id %d outside reserved range", blogID)
			}
			expectedURL, err := formatMonitorURL(cfg.URLPattern, cfg.URLNumberStart+offset)
			if err != nil {
				return samples, err
			}
			host, err := hostnameFromURL(row[urlIdx])
			if err != nil {
				return samples, fmt.Errorf("sampled blog_id %d URL: %w", blogID, err)
			}
			expectedHost, err := hostnameFromURL(expectedURL)
			if err != nil {
				return samples, fmt.Errorf("expected URL for blog_id %d: %w", blogID, err)
			}
			samples = append(samples, TargetURLSample{
				BlogID:       blogID,
				BucketNo:     bucket,
				URL:          row[urlIdx],
				ExpectedURL:  expectedURL,
				Host:         host,
				ExpectedHost: expectedHost,
				PatternMatch: normalizeURLForCompare(row[urlIdx]) == normalizeURLForCompare(expectedURL),
			})
			delete(expectedByBlogID, blogID)
		}
	}
	if len(expectedByBlogID) > 0 {
		missing := make([]string, 0, len(expectedByBlogID))
		for blogID := range expectedByBlogID {
			missing = append(missing, strconv.FormatInt(blogID, 10))
		}
		sort.Strings(missing)
		return samples, fmt.Errorf("missing active URL sample rows for blog_id(s): %s", strings.Join(missing, ", "))
	}
	return samples, nil
}

func normalizeURLForCompare(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return strings.TrimSpace(rawURL)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = normalizeHostPort(parsed.Host)
	return parsed.String()
}

func normalizeHostPort(host string) string {
	if strings.TrimSpace(host) == "" {
		return ""
	}
	hostname := normalizeHostname(host)
	if strings.Contains(host, ":") {
		parsedHost, port, err := net.SplitHostPort(host)
		if err == nil {
			return net.JoinHostPort(normalizeHostname(parsedHost), port)
		}
	}
	return hostname
}
