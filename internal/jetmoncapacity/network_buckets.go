package jetmoncapacity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// NetworkBucketCollector resets and captures per-host traffic bucket counters.
type NetworkBucketCollector interface {
	Reset(ctx context.Context, cfg NetworkBucketsConfig, services []ServiceLifecycle) ([]NetworkBucketHostSnapshot, error)
	Snapshot(ctx context.Context, cfg NetworkBucketsConfig, services []ServiceLifecycle) ([]NetworkBucketHostSnapshot, error)
}

// DefaultNetworkBucketCollector installs counter-only nftables rules through
// SSH. The table is scoped to uptime-bench and does not change packet verdicts.
type DefaultNetworkBucketCollector struct{}

// NetworkBucketHostSnapshot records one host's counter snapshot.
type NetworkBucketHostSnapshot struct {
	ID         string                 `json:"id"`
	Instance   string                 `json:"instance,omitempty"`
	SSHHost    string                 `json:"ssh_host"`
	Status     string                 `json:"status"`
	Error      string                 `json:"error,omitempty"`
	CapturedAt time.Time              `json:"captured_at"`
	Counters   []NetworkBucketCounter `json:"counters,omitempty"`
}

// NetworkBucketCounter is one named packet/byte counter.
type NetworkBucketCounter struct {
	Name      string `json:"name"`
	Bucket    string `json:"bucket"`
	Direction string `json:"direction"`
	Packets   uint64 `json:"packets"`
	Bytes     uint64 `json:"bytes"`
}

var nftTableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (DefaultNetworkBucketCollector) Reset(ctx context.Context, cfg NetworkBucketsConfig, services []ServiceLifecycle) ([]NetworkBucketHostSnapshot, error) {
	cfg = normalizeNetworkBucketsConfig(cfg)
	if !cfg.Enabled {
		return nil, nil
	}
	timeout, err := parseDuration("network_buckets.timeout", cfg.Timeout)
	if err != nil {
		return nil, err
	}
	hosts := selectedNetworkBucketHosts(cfg, services)
	snapshots := make([]NetworkBucketHostSnapshot, 0, len(hosts))
	var errs []error
	for _, host := range hosts {
		snapshot := NetworkBucketHostSnapshot{
			ID:         host.ID,
			Instance:   host.Instance,
			SSHHost:    host.SSHHost,
			CapturedAt: time.Now().UTC(),
			Status:     "pass",
		}
		script, err := nftBucketScript(cfg.Table, host)
		if err != nil {
			snapshot.Status = "fail"
			snapshot.Error = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", host.ID, err))
			snapshots = append(snapshots, snapshot)
			continue
		}
		if _, err := runNetworkBucketSSH(ctx, cfg, host.SSHHost, timeout, []string{"sudo", "nft", "-f", "-"}, script); err != nil {
			snapshot.Status = "fail"
			snapshot.Error = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", host.ID, err))
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, joinErrors(errs)
}

func (DefaultNetworkBucketCollector) Snapshot(ctx context.Context, cfg NetworkBucketsConfig, services []ServiceLifecycle) ([]NetworkBucketHostSnapshot, error) {
	cfg = normalizeNetworkBucketsConfig(cfg)
	if !cfg.Enabled {
		return nil, nil
	}
	timeout, err := parseDuration("network_buckets.timeout", cfg.Timeout)
	if err != nil {
		return nil, err
	}
	hosts := selectedNetworkBucketHosts(cfg, services)
	snapshots := make([]NetworkBucketHostSnapshot, 0, len(hosts))
	var errs []error
	for _, host := range hosts {
		snapshot := NetworkBucketHostSnapshot{
			ID:         host.ID,
			Instance:   host.Instance,
			SSHHost:    host.SSHHost,
			CapturedAt: time.Now().UTC(),
			Status:     "pass",
		}
		out, err := runNetworkBucketSSH(ctx, cfg, host.SSHHost, timeout, []string{"sudo", "nft", "-j", "list", "table", "inet", cfg.Table}, "")
		if err != nil {
			snapshot.Status = "fail"
			snapshot.Error = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", host.ID, err))
			snapshots = append(snapshots, snapshot)
			continue
		}
		counters, err := parseNFTCounters(out)
		if err != nil {
			snapshot.Status = "fail"
			snapshot.Error = err.Error()
			errs = append(errs, fmt.Errorf("%s: %w", host.ID, err))
		} else {
			snapshot.Counters = append(counters, derivedOtherNetworkCounters(counters)...)
			sortNetworkCounters(snapshot.Counters)
		}
		snapshots = append(snapshots, snapshot)
	}
	return snapshots, joinErrors(errs)
}

func normalizeNetworkBucketsConfig(cfg NetworkBucketsConfig) NetworkBucketsConfig {
	cfg.Table = strings.TrimSpace(cfg.Table)
	if cfg.Table == "" {
		cfg.Table = "uptime_bench_net_buckets"
	}
	if cfg.Timeout == "" {
		cfg.Timeout = "10s"
	}
	cfg.SSHConfig = strings.TrimSpace(cfg.SSHConfig)
	for i := range cfg.Hosts {
		cfg.Hosts[i] = normalizeNetworkBucketHost(cfg.Hosts[i])
	}
	return cfg
}

func selectedNetworkBucketHosts(cfg NetworkBucketsConfig, services []ServiceLifecycle) []NetworkBucketHostConfig {
	if len(services) == 0 {
		return append([]NetworkBucketHostConfig(nil), cfg.Hosts...)
	}
	selected := map[string]bool{}
	for _, service := range services {
		selected[service.ID] = true
	}
	var hosts []NetworkBucketHostConfig
	for _, host := range cfg.Hosts {
		if selected[host.ID] {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func nftBucketScript(table string, host NetworkBucketHostConfig) (string, error) {
	if !nftTableNamePattern.MatchString(table) {
		return "", fmt.Errorf("network_buckets.table %q is not a safe nftables identifier", table)
	}
	if err := validateNetworkBucketHostIPs(host); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "destroy table inet %s\n", table)
	fmt.Fprintf(&b, "table inet %s {\n", table)
	for _, name := range networkCounterNames(host) {
		fmt.Fprintf(&b, "\tcounter %s {}\n", name)
	}
	fmt.Fprintln(&b, "\tchain output {")
	fmt.Fprintln(&b, "\t\ttype filter hook output priority filter; policy accept;")
	fmt.Fprintln(&b, "\t\tcounter name total_tx")
	if host.TargetIP != "" {
		fmt.Fprintf(&b, "\t\tip daddr %s tcp dport { 80, 443 } counter name target_http_tx\n", host.TargetIP)
	}
	if host.MySQLIP != "" {
		fmt.Fprintf(&b, "\t\tip daddr %s tcp dport %d counter name mysql_tx\n", host.MySQLIP, host.MySQLPort)
	}
	if host.StatsDPort > 0 {
		fmt.Fprintf(&b, "\t\tudp dport %d counter name statsd_tx\n", host.StatsDPort)
	}
	if host.WPCOMHTTPSPort > 0 {
		if host.TargetIP != "" {
			fmt.Fprintf(&b, "\t\tip daddr != %s tcp dport %d counter name wpcom_https_tx\n", host.TargetIP, host.WPCOMHTTPSPort)
		} else {
			fmt.Fprintf(&b, "\t\ttcp dport %d counter name wpcom_https_tx\n", host.WPCOMHTTPSPort)
		}
	}
	fmt.Fprintln(&b, "\t\tudp dport 53 counter name dns_tx")
	fmt.Fprintln(&b, "\t\ttcp dport 53 counter name dns_tx")
	if host.MonitoringIP != "" {
		fmt.Fprintf(&b, "\t\tip daddr %s counter name monitoring_tx\n", host.MonitoringIP)
	}
	if host.BridgeAPIPort > 0 {
		fmt.Fprintf(&b, "\t\ttcp sport %d counter name bridge_api_tx\n", host.BridgeAPIPort)
	}
	if host.APIPort > 0 {
		fmt.Fprintf(&b, "\t\ttcp sport %d counter name api_tx\n", host.APIPort)
	}
	if host.PeerPort > 0 {
		fmt.Fprintf(&b, "\t\ttcp sport %d counter name jetmon_peer_tx\n", host.PeerPort)
	}
	fmt.Fprintln(&b, "\t\ttcp sport 22 counter name ssh_tx")
	fmt.Fprintln(&b, "\t}")
	fmt.Fprintln(&b, "\tchain input {")
	fmt.Fprintln(&b, "\t\ttype filter hook input priority filter; policy accept;")
	fmt.Fprintln(&b, "\t\tcounter name total_rx")
	if host.TargetIP != "" {
		fmt.Fprintf(&b, "\t\tip saddr %s tcp sport { 80, 443 } counter name target_http_rx\n", host.TargetIP)
	}
	if host.MySQLIP != "" {
		fmt.Fprintf(&b, "\t\tip saddr %s tcp sport %d counter name mysql_rx\n", host.MySQLIP, host.MySQLPort)
	}
	if host.StatsDPort > 0 {
		fmt.Fprintf(&b, "\t\tudp sport %d counter name statsd_rx\n", host.StatsDPort)
	}
	if host.WPCOMHTTPSPort > 0 {
		if host.TargetIP != "" {
			fmt.Fprintf(&b, "\t\tip saddr != %s tcp sport %d counter name wpcom_https_rx\n", host.TargetIP, host.WPCOMHTTPSPort)
		} else {
			fmt.Fprintf(&b, "\t\ttcp sport %d counter name wpcom_https_rx\n", host.WPCOMHTTPSPort)
		}
	}
	fmt.Fprintln(&b, "\t\tudp sport 53 counter name dns_rx")
	fmt.Fprintln(&b, "\t\ttcp sport 53 counter name dns_rx")
	if host.MonitoringIP != "" {
		fmt.Fprintf(&b, "\t\tip saddr %s counter name monitoring_rx\n", host.MonitoringIP)
	}
	if host.BridgeAPIPort > 0 {
		fmt.Fprintf(&b, "\t\ttcp dport %d counter name bridge_api_rx\n", host.BridgeAPIPort)
	}
	if host.APIPort > 0 {
		fmt.Fprintf(&b, "\t\ttcp dport %d counter name api_rx\n", host.APIPort)
	}
	if host.PeerPort > 0 {
		fmt.Fprintf(&b, "\t\ttcp dport %d counter name jetmon_peer_rx\n", host.PeerPort)
	}
	fmt.Fprintln(&b, "\t\ttcp dport 22 counter name ssh_rx")
	fmt.Fprintln(&b, "\t}")
	fmt.Fprintln(&b, "}")
	return b.String(), nil
}

func networkCounterNames(host NetworkBucketHostConfig) []string {
	names := []string{"total_tx", "total_rx", "dns_tx", "dns_rx", "ssh_tx", "ssh_rx"}
	if host.TargetIP != "" {
		names = append(names, "target_http_tx", "target_http_rx")
	}
	if host.MySQLIP != "" {
		names = append(names, "mysql_tx", "mysql_rx")
	}
	if host.StatsDPort > 0 {
		names = append(names, "statsd_tx", "statsd_rx")
	}
	if host.WPCOMHTTPSPort > 0 {
		names = append(names, "wpcom_https_tx", "wpcom_https_rx")
	}
	if host.MonitoringIP != "" {
		names = append(names, "monitoring_tx", "monitoring_rx")
	}
	if host.BridgeAPIPort > 0 {
		names = append(names, "bridge_api_tx", "bridge_api_rx")
	}
	if host.APIPort > 0 {
		names = append(names, "api_tx", "api_rx")
	}
	if host.PeerPort > 0 {
		names = append(names, "jetmon_peer_tx", "jetmon_peer_rx")
	}
	sort.Strings(names)
	return names
}

func runNetworkBucketSSH(ctx context.Context, cfg NetworkBucketsConfig, sshHost string, timeout time.Duration, remoteArgs []string, stdin string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{}
	if cfg.SSHConfig != "" {
		args = append(args, "-F", cfg.SSHConfig)
	}
	args = append(args, sshHost)
	args = append(args, remoteArgs...)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	if err != nil {
		return out, fmt.Errorf("%v: %w: %s", append([]string{"ssh"}, args...), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func parseNFTCounters(data []byte) ([]NetworkBucketCounter, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse nft json: %w", err)
	}
	byName := map[string]NetworkBucketCounter{}
	walkNFTCounters(root, byName)
	counters := make([]NetworkBucketCounter, 0, len(byName))
	for _, counter := range byName {
		counters = append(counters, counter)
	}
	sortNetworkCounters(counters)
	return counters, nil
}

func walkNFTCounters(v any, out map[string]NetworkBucketCounter) {
	switch val := v.(type) {
	case []any:
		for _, item := range val {
			walkNFTCounters(item, out)
		}
	case map[string]any:
		if raw, ok := val["counter"]; ok {
			if counter, ok := raw.(map[string]any); ok {
				name, _ := counter["name"].(string)
				if name != "" && (counter["bytes"] != nil || counter["packets"] != nil) {
					out[name] = NetworkBucketCounter{
						Name:      name,
						Bucket:    networkBucketFromCounterName(name),
						Direction: networkDirectionFromCounterName(name),
						Packets:   jsonNumberUint64(counter["packets"]),
						Bytes:     jsonNumberUint64(counter["bytes"]),
					}
				}
			}
		}
		for _, item := range val {
			walkNFTCounters(item, out)
		}
	}
}

func jsonNumberUint64(v any) uint64 {
	switch val := v.(type) {
	case json.Number:
		n, _ := strconv.ParseUint(val.String(), 10, 64)
		return n
	case float64:
		if val <= 0 {
			return 0
		}
		return uint64(val)
	case int:
		if val <= 0 {
			return 0
		}
		return uint64(val)
	case uint64:
		return val
	default:
		return 0
	}
}

func derivedOtherNetworkCounters(counters []NetworkBucketCounter) []NetworkBucketCounter {
	var totalTx, totalRx uint64
	var knownTx, knownRx uint64
	for _, counter := range counters {
		switch counter.Name {
		case "total_tx":
			totalTx = counter.Bytes
		case "total_rx":
			totalRx = counter.Bytes
		default:
			if counter.Direction == "tx" {
				knownTx += counter.Bytes
			} else if counter.Direction == "rx" {
				knownRx += counter.Bytes
			}
		}
	}
	return []NetworkBucketCounter{
		{Name: "other_tx", Bucket: "other", Direction: "tx", Bytes: saturatingSubtract(totalTx, knownTx)},
		{Name: "other_rx", Bucket: "other", Direction: "rx", Bytes: saturatingSubtract(totalRx, knownRx)},
	}
}

func saturatingSubtract(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}

func networkBucketFromCounterName(name string) string {
	name = strings.TrimSuffix(strings.TrimSuffix(name, "_tx"), "_rx")
	return name
}

func networkDirectionFromCounterName(name string) string {
	switch {
	case strings.HasSuffix(name, "_tx"):
		return "tx"
	case strings.HasSuffix(name, "_rx"):
		return "rx"
	default:
		return ""
	}
}

func sortNetworkCounters(counters []NetworkBucketCounter) {
	sort.Slice(counters, func(i, j int) bool {
		if counters[i].Bucket != counters[j].Bucket {
			return counters[i].Bucket < counters[j].Bucket
		}
		if counters[i].Direction != counters[j].Direction {
			return counters[i].Direction < counters[j].Direction
		}
		return counters[i].Name < counters[j].Name
	})
}

func writeNetworkBucketArtifact(dir, name string, snapshots []NetworkBucketHostSnapshot, m *RunManifest) error {
	data, err := json.MarshalIndent(snapshots, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal network bucket snapshots: %w", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	recordArtifactOnce(m, Artifact{Action: strings.TrimSuffix(name, ".json"), Path: path})
	return nil
}

func validateNetworkBucketHostIPs(host NetworkBucketHostConfig) error {
	for label, ip := range map[string]string{
		"target_ip":     host.TargetIP,
		"mysql_ip":      host.MySQLIP,
		"monitoring_ip": host.MonitoringIP,
	} {
		if ip == "" {
			continue
		}
		if net.ParseIP(ip) == nil {
			return fmt.Errorf("%s=%q is not an IP address", label, ip)
		}
	}
	return nil
}

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	return errors.New(strings.Join(parts, "; "))
}
