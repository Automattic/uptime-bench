package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type result struct {
	Latency    time.Duration
	DNSLatency time.Duration
	StatusCode int
	Error      string
}

type report struct {
	URLPattern     string         `json:"url_pattern"`
	Start          int            `json:"start"`
	Hosts          int            `json:"hosts"`
	Requests       int            `json:"requests"`
	Concurrency    int            `json:"concurrency"`
	DNSServer      string         `json:"dns_server,omitempty"`
	ConnectAddress string         `json:"connect_address,omitempty"`
	Duration       string         `json:"duration"`
	RPS            float64        `json:"rps"`
	Success        int            `json:"success"`
	Failure        int            `json:"failure"`
	StatusCodes    map[string]int `json:"status_codes"`
	Errors         map[string]int `json:"errors,omitempty"`
	LatencyMS      stats          `json:"latency_ms"`
	DNSLatencyMS   stats          `json:"dns_latency_ms,omitempty"`
}

type stats struct {
	Min  float64 `json:"min"`
	Avg  float64 `json:"avg"`
	P50  float64 `json:"p50"`
	P95  float64 `json:"p95"`
	Max  float64 `json:"max"`
	Last float64 `json:"last"`
}

func main() {
	urlPattern := flag.String("url-pattern", "", "URL pattern containing one fmt integer placeholder, e.g. http://site-%07d.load.example.com/")
	start := flag.Int("start", 1, "first generated host number")
	hosts := flag.Int("hosts", 1, "number of generated hosts to cycle through")
	requests := flag.Int("requests", 1000, "total requests to send")
	concurrency := flag.Int("concurrency", 50, "parallel workers")
	timeout := flag.Duration("timeout", 5*time.Second, "per-request timeout")
	dnsServer := flag.String("dns-server", "", "optional DNS server host:port to resolve every generated host before request")
	connectAddress := flag.String("connect-address", "", "optional host:port to connect to while preserving the generated Host header")
	tlsInsecure := flag.Bool("tls-insecure", false, "skip HTTPS certificate verification for target smoke tests")
	method := flag.String("method", http.MethodGet, "HTTP method")
	format := flag.String("format", "table", "output format: table or json")
	flag.Parse()

	if *urlPattern == "" {
		log.Fatal("targetload: set -url-pattern")
	}
	if err := validateURLPattern(*urlPattern, *start); err != nil {
		log.Fatalf("targetload: -url-pattern: %v", err)
	}
	if *hosts <= 0 {
		log.Fatal("targetload: -hosts must be positive")
	}
	if *requests <= 0 {
		log.Fatal("targetload: -requests must be positive")
	}
	if *concurrency <= 0 {
		log.Fatal("targetload: -concurrency must be positive")
	}
	if *dnsServer != "" && *connectAddress != "" {
		log.Fatal("targetload: use either -dns-server or -connect-address, not both")
	}

	client := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: *tlsInsecure},
		},
	}
	var resolver *net.Resolver
	if *dnsServer != "" {
		resolver = resolverFor(*dnsServer)
	}

	started := time.Now()
	results := run(context.Background(), client, resolver, options{
		URLPattern:     *urlPattern,
		Start:          *start,
		Hosts:          *hosts,
		Requests:       *requests,
		Concurrency:    *concurrency,
		Timeout:        *timeout,
		DNSServer:      *dnsServer,
		ConnectAddress: *connectAddress,
		Method:         *method,
	})
	duration := time.Since(started)
	rep := summarize(*urlPattern, *start, *hosts, *requests, *concurrency, *dnsServer, *connectAddress, duration, results)

	switch strings.ToLower(*format) {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			log.Fatalf("targetload: json: %v", err)
		}
	case "table":
		writeTable(os.Stdout, rep)
	default:
		log.Fatalf("targetload: unsupported -format %q", *format)
	}
}

func validateURLPattern(pattern string, sampleNumber int) error {
	sample := fmt.Sprintf(pattern, sampleNumber)
	if strings.Contains(sample, "%!") {
		return fmt.Errorf("must contain exactly one fmt integer placeholder")
	}
	u, err := url.Parse(sample)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("sample URL scheme must be http or https")
	}
	if u.Host == "" {
		return fmt.Errorf("sample URL must include a host")
	}
	return nil
}

type options struct {
	URLPattern     string
	Start          int
	Hosts          int
	Requests       int
	Concurrency    int
	Timeout        time.Duration
	DNSServer      string
	ConnectAddress string
	Method         string
}

func run(ctx context.Context, client *http.Client, resolver *net.Resolver, opts options) []result {
	jobs := make(chan int)
	results := make([]result, opts.Requests)
	var wg sync.WaitGroup
	workers := opts.Concurrency
	if workers > opts.Requests {
		workers = opts.Requests
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				hostNumber := opts.Start + (idx % opts.Hosts)
				results[idx] = doRequest(ctx, client, resolver, opts, hostNumber)
			}
		}()
	}
	for i := 0; i < opts.Requests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return results
}

func doRequest(ctx context.Context, client *http.Client, resolver *net.Resolver, opts options, hostNumber int) result {
	reqCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	rawURL := fmt.Sprintf(opts.URLPattern, hostNumber)
	reqURL, err := url.Parse(rawURL)
	if err != nil {
		return result{Error: err.Error()}
	}
	if reqURL.Scheme != "http" && reqURL.Scheme != "https" {
		return result{Error: "url scheme must be http or https"}
	}

	originalHost := reqURL.Host
	var dnsLatency time.Duration
	if opts.ConnectAddress != "" {
		reqURL.Host = opts.ConnectAddress
	} else if resolver != nil {
		resolved, elapsed, err := resolveAddress(reqCtx, resolver, reqURL)
		if err != nil {
			return result{DNSLatency: elapsed, Error: "dns: " + err.Error()}
		}
		dnsLatency = elapsed
		reqURL.Host = resolved
	}

	req, err := http.NewRequestWithContext(reqCtx, opts.Method, reqURL.String(), nil)
	if err != nil {
		return result{DNSLatency: dnsLatency, Error: err.Error()}
	}
	if reqURL.Host != originalHost {
		req.Host = originalHost
	}

	started := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(started)
	if err != nil {
		return result{Latency: elapsed, DNSLatency: dnsLatency, Error: err.Error()}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return result{Latency: elapsed, DNSLatency: dnsLatency, StatusCode: resp.StatusCode}
}

func resolverFor(server string) *net.Resolver {
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, server)
		},
	}
}

func resolveAddress(ctx context.Context, resolver *net.Resolver, reqURL *url.URL) (string, time.Duration, error) {
	host := reqURL.Hostname()
	port := reqURL.Port()
	if port == "" {
		if reqURL.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	started := time.Now()
	addrs, err := resolver.LookupIPAddr(ctx, host)
	elapsed := time.Since(started)
	if err != nil {
		return "", elapsed, err
	}
	for _, addr := range addrs {
		if ip := addr.IP.To4(); ip != nil {
			return net.JoinHostPort(ip.String(), port), elapsed, nil
		}
	}
	if len(addrs) > 0 {
		return net.JoinHostPort(addrs[0].IP.String(), port), elapsed, nil
	}
	return "", elapsed, fmt.Errorf("no addresses")
}

func summarize(urlPattern string, start, hosts, requests, concurrency int, dnsServer, connectAddress string, duration time.Duration, results []result) report {
	statusCodes := make(map[string]int)
	errors := make(map[string]int)
	var latencies []float64
	var dnsLatencies []float64
	success := 0
	for _, r := range results {
		if r.StatusCode > 0 {
			statusCodes[strconv.Itoa(r.StatusCode)]++
			if r.StatusCode >= 200 && r.StatusCode < 400 {
				success++
			}
		}
		if r.Error != "" {
			statusCodes["error"]++
			errors[classifyError(r.Error)]++
		}
		if r.Latency > 0 {
			latencies = append(latencies, float64(r.Latency.Microseconds())/1000)
		}
		if r.DNSLatency > 0 {
			dnsLatencies = append(dnsLatencies, float64(r.DNSLatency.Microseconds())/1000)
		}
	}
	return report{
		URLPattern:     urlPattern,
		Start:          start,
		Hosts:          hosts,
		Requests:       requests,
		Concurrency:    concurrency,
		DNSServer:      dnsServer,
		ConnectAddress: connectAddress,
		Duration:       duration.String(),
		RPS:            float64(requests) / duration.Seconds(),
		Success:        success,
		Failure:        requests - success,
		StatusCodes:    statusCodes,
		Errors:         errors,
		LatencyMS:      summarizeValues(latencies),
		DNSLatencyMS:   summarizeValues(dnsLatencies),
	}
}

func classifyError(err string) string {
	lower := strings.ToLower(err)
	if strings.HasPrefix(lower, "dns: ") {
		switch {
		case strings.Contains(lower, "i/o timeout"), strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "timeout"):
			return "dns_timeout"
		case strings.Contains(lower, "no such host"):
			return "dns_no_such_host"
		case strings.Contains(lower, "connection refused"):
			return "dns_connection_refused"
		default:
			return "dns_error"
		}
	}
	switch {
	case strings.Contains(lower, "deadline exceeded"), strings.Contains(lower, "client.timeout"), strings.Contains(lower, "timeout"):
		return "request_timeout"
	case strings.Contains(lower, "connection refused"):
		return "connect_refused"
	case strings.Contains(lower, "connection reset"):
		return "connection_reset"
	case strings.Contains(lower, "eof"):
		return "eof"
	default:
		return "request_error"
	}
}

func summarizeValues(values []float64) stats {
	if len(values) == 0 {
		return stats{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return stats{
		Min:  sorted[0],
		Avg:  sum / float64(len(values)),
		P50:  percentile(sorted, 0.50),
		P95:  percentile(sorted, 0.95),
		Max:  sorted[len(sorted)-1],
		Last: values[len(values)-1],
	}
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

func writeTable(w io.Writer, rep report) {
	fmt.Fprintf(w, "URL Pattern: %s\n", rep.URLPattern)
	fmt.Fprintf(w, "Hosts:       %d from %d\n", rep.Hosts, rep.Start)
	fmt.Fprintf(w, "Requests:    %d\n", rep.Requests)
	fmt.Fprintf(w, "Concurrency: %d\n", rep.Concurrency)
	if rep.DNSServer != "" {
		fmt.Fprintf(w, "DNS Server:  %s\n", rep.DNSServer)
	}
	if rep.ConnectAddress != "" {
		fmt.Fprintf(w, "Connect:     %s\n", rep.ConnectAddress)
	}
	fmt.Fprintf(w, "Duration:    %s\n", rep.Duration)
	fmt.Fprintf(w, "RPS:         %.2f\n", rep.RPS)
	fmt.Fprintf(w, "Success:     %d\n", rep.Success)
	fmt.Fprintf(w, "Failure:     %d\n", rep.Failure)
	fmt.Fprintf(w, "Status:      %s\n", formatStatusCodes(rep.StatusCodes))
	if len(rep.Errors) > 0 {
		fmt.Fprintf(w, "Errors:      %s\n", formatStatusCodes(rep.Errors))
	}
	fmt.Fprintf(w, "Latency ms:  avg=%.2f p50=%.2f p95=%.2f max=%.2f\n",
		rep.LatencyMS.Avg, rep.LatencyMS.P50, rep.LatencyMS.P95, rep.LatencyMS.Max)
	if rep.DNSServer != "" {
		fmt.Fprintf(w, "DNS ms:      avg=%.2f p50=%.2f p95=%.2f max=%.2f\n",
			rep.DNSLatencyMS.Avg, rep.DNSLatencyMS.P50, rep.DNSLatencyMS.P95, rep.DNSLatencyMS.Max)
	}
}

func formatStatusCodes(codes map[string]int) string {
	keys := make([]string, 0, len(codes))
	for k := range codes {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.Itoa(codes[k]))
	}
	return strings.Join(parts, " ")
}
