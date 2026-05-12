package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/scenario"
	"github.com/Automattic/uptime-bench/internal/serviceconfig"
)

const (
	dnsBaselineEventType = "dns_baseline"
	dnsBaselineInterval  = time.Minute
	dnsBaselineTimeout   = 5 * time.Second
)

type dnsBaselinePlan struct {
	host      string
	resolvers []dnsBaselineResolver
	interval  time.Duration

	unstable       bool
	unstablePhases []string
	lastResult     dnsBaselineResult
	samples        int
}

type dnsBaselineResolver struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Address string `json:"address,omitempty"`
	SSHHost string `json:"ssh_host,omitempty"`
}

type dnsBaselineResult struct {
	Host     string             `json:"host"`
	Phase    string             `json:"phase"`
	Stable   bool               `json:"stable"`
	Reason   string             `json:"reason,omitempty"`
	Checks   []dnsBaselineCheck `json:"checks"`
	Sampled  time.Time          `json:"sampled_at"`
	Interval string             `json:"interval,omitempty"`
}

type dnsBaselineCheck struct {
	ResolverID   string    `json:"resolver_id"`
	ResolverKind string    `json:"resolver_kind"`
	Address      string    `json:"address,omitempty"`
	SSHHost      string    `json:"ssh_host,omitempty"`
	Status       string    `json:"status"`
	Tool         string    `json:"tool"`
	CheckedAt    time.Time `json:"checked_at"`
	ElapsedMS    int64     `json:"elapsed_ms"`
	Answers      []string  `json:"answers,omitempty"`
	Error        string    `json:"error,omitempty"`
}

func newDNSBaselinePlan(sc *scenario.Scenario, fl *fleet.Config, svcCfg *serviceconfig.Config, host string) *dnsBaselinePlan {
	if !scenarioIsTLSOnly(sc) || strings.TrimSpace(host) == "" {
		return nil
	}
	var resolvers []dnsBaselineResolver
	monitorHosts := configuredDNSBaselineMonitorHosts(sc, svcCfg)
	if len(monitorHosts) == 0 {
		resolvers = append(resolvers, dnsBaselineResolver{
			ID:   "harness-system",
			Kind: "harness_system",
		})
	} else {
		for _, monitorHost := range monitorHosts {
			resolvers = append(resolvers, dnsBaselineResolver{
				ID:      "monitor-system:" + monitorHost,
				Kind:    "monitor_system",
				SSHHost: monitorHost,
			})
		}
	}

	configured := configuredDNSBaselineResolvers(sc, svcCfg)
	for _, addr := range configured {
		if len(monitorHosts) == 0 {
			resolvers = append(resolvers, dnsBaselineResolver{
				ID:      "configured:" + addr,
				Kind:    "configured",
				Address: addr,
			})
		} else {
			for _, monitorHost := range monitorHosts {
				resolvers = append(resolvers, dnsBaselineResolver{
					ID:      "monitor-configured:" + monitorHost + ":" + addr,
					Kind:    "monitor_configured",
					Address: addr,
					SSHHost: monitorHost,
				})
			}
		}
	}

	if fl != nil {
		for _, ns := range fl.Nameservers {
			if strings.TrimSpace(ns.Address) == "" {
				continue
			}
			port := ns.DNSPort
			if port == 0 {
				port = 53
			}
			addr := net.JoinHostPort(ns.Address, fmt.Sprintf("%d", port))
			resolvers = append(resolvers, dnsBaselineResolver{
				ID:      "authoritative:" + ns.ID,
				Kind:    "authoritative",
				Address: addr,
			})
		}
	}

	resolvers = dedupeDNSBaselineResolvers(resolvers)
	if len(resolvers) == 0 {
		return nil
	}
	return &dnsBaselinePlan{
		host:      host,
		resolvers: resolvers,
		interval:  dnsBaselineInterval,
	}
}

func scenarioIsTLSOnly(sc *scenario.Scenario) bool {
	if sc == nil || len(sc.Failures) == 0 {
		return false
	}
	for _, f := range sc.Failures {
		if !strings.HasPrefix(f.Type, "tls_") {
			return false
		}
	}
	return true
}

func configuredDNSBaselineMonitorHosts(sc *scenario.Scenario, svcCfg *serviceconfig.Config) []string {
	return configuredDNSBaselineValues(sc, svcCfg, []string{
		"dns_baseline_ssh_host",
		"dns_baseline_monitor_host",
		"monitor_host",
	})
}

func configuredDNSBaselineResolvers(sc *scenario.Scenario, svcCfg *serviceconfig.Config) []string {
	values := configuredDNSBaselineValues(sc, svcCfg, []string{
		"dns_baseline_resolvers",
		"check_dns_resolvers",
		"dns_resolvers",
	})
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		addr := normalizeResolverAddress(value)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, addr)
	}
	return out
}

func configuredDNSBaselineValues(sc *scenario.Scenario, svcCfg *serviceconfig.Config, keys []string) []string {
	if svcCfg == nil {
		return nil
	}
	monitorSet := map[string]bool{}
	if sc != nil {
		for _, id := range sc.Monitors {
			monitorSet[id] = true
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, svc := range svcCfg.Services {
		if !svc.Enabled || svc.Type != "jetmon-v2" {
			continue
		}
		if len(monitorSet) > 0 && !monitorSet[svc.ID] {
			continue
		}
		for _, key := range keys {
			for _, value := range parseDNSBaselineList(svc.Auth[key]) {
				value = strings.TrimSpace(value)
				if value == "" || seen[value] {
					continue
				}
				seen[value] = true
				out = append(out, value)
			}
		}
	}
	return out
}

func parseDNSBaselineList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var jsonValues []string
	if err := json.Unmarshal([]byte(raw), &jsonValues); err == nil {
		return jsonValues
	}
	if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = strings.TrimPrefix(strings.TrimSuffix(raw, "]"), "[")
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.Trim(strings.TrimSpace(field), `"'`)
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

func normalizeResolverAddress(raw string) string {
	raw = strings.Trim(strings.TrimSpace(raw), `"'`)
	if raw == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(raw)
	if err == nil {
		if strings.TrimSpace(host) == "" {
			return ""
		}
		if strings.TrimSpace(port) == "" {
			port = "53"
		}
		return net.JoinHostPort(host, port)
	}
	if strings.Count(raw, ":") == 1 {
		parts := strings.SplitN(raw, ":", 2)
		if parts[0] != "" && parts[1] != "" {
			return net.JoinHostPort(parts[0], parts[1])
		}
	}
	return net.JoinHostPort(strings.Trim(raw, "[]"), "53")
}

func dedupeDNSBaselineResolvers(in []dnsBaselineResolver) []dnsBaselineResolver {
	out := make([]dnsBaselineResolver, 0, len(in))
	seen := map[string]bool{}
	for _, r := range in {
		key := r.Kind + "\x00" + r.Address + "\x00" + r.SSHHost
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}

func (p *dnsBaselinePlan) record(ctx context.Context, database recorder, runID, targetID, phase string) error {
	if p == nil {
		return nil
	}
	result := p.run(ctx, phase)
	p.samples++
	p.lastResult = result
	if !result.Stable {
		p.unstable = true
		p.unstablePhases = append(p.unstablePhases, phase)
	}
	if err := logEvent(ctx, database, runID, targetID, dnsBaselineEventType, "tls_dns_baseline", result); err != nil {
		return err
	}
	return nil
}

func (p *dnsBaselinePlan) wait(ctx context.Context, database recorder, runID, targetID string, waitFor time.Duration) error {
	if p == nil || waitFor <= 0 {
		return nil
	}
	timer := time.NewTimer(waitFor)
	defer timer.Stop()

	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-timer.C:
			return nil
		case <-ticker.C:
			if err := p.record(ctx, database, runID, targetID, "active_periodic"); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (p *dnsBaselinePlan) run(ctx context.Context, phase string) dnsBaselineResult {
	result := dnsBaselineResult{
		Host:     p.host,
		Phase:    phase,
		Stable:   true,
		Sampled:  time.Now(),
		Interval: p.interval.String(),
	}
	var failed []string
	for _, resolver := range p.resolvers {
		check := resolver.check(ctx, p.host)
		result.Checks = append(result.Checks, check)
		if check.Status != "ok" {
			result.Stable = false
			failed = append(failed, check.ResolverID)
		}
	}
	if len(result.Checks) == 0 {
		result.Stable = false
		result.Reason = "no DNS baseline resolvers configured"
	} else if len(failed) > 0 {
		result.Reason = "failed checks: " + strings.Join(failed, ", ")
	}
	return result
}

func (r dnsBaselineResolver) check(ctx context.Context, host string) dnsBaselineCheck {
	check := dnsBaselineCheck{
		ResolverID:   r.ID,
		ResolverKind: r.Kind,
		Address:      r.Address,
		SSHHost:      r.SSHHost,
		Status:       "ok",
		CheckedAt:    time.Now(),
	}
	start := time.Now()
	var answers []string
	var err error
	switch r.Kind {
	case "harness_system":
		check.Tool = "net.DefaultResolver.LookupIPAddr"
		answers, err = lookupAWithResolver(ctx, net.DefaultResolver, host)
	case "configured", "authoritative":
		check.Tool = "net.Resolver.LookupIPAddr"
		answers, err = lookupAWithAddress(ctx, r.Address, host)
	case "monitor_system":
		check.Tool = "ssh getent ahostsv4"
		answers, err = remoteSystemLookup(ctx, r.SSHHost, host)
	case "monitor_configured":
		check.Tool = "ssh dig"
		answers, err = remoteResolverLookup(ctx, r.SSHHost, r.Address, host)
	default:
		err = fmt.Errorf("unsupported DNS baseline resolver kind %q", r.Kind)
	}
	check.ElapsedMS = time.Since(start).Milliseconds()
	if err != nil {
		check.Status = "error"
		check.Error = err.Error()
		return check
	}
	check.Answers = normalizeDNSAnswers(answers)
	if len(check.Answers) == 0 {
		check.Status = "error"
		check.Error = "no A answers"
	}
	return check
}

func lookupAWithAddress(ctx context.Context, resolverAddr, host string) ([]string, error) {
	resolverAddr = normalizeResolverAddress(resolverAddr)
	if resolverAddr == "" {
		return nil, fmt.Errorf("empty resolver address")
	}
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", resolverAddr)
		},
	}
	return lookupAWithResolver(ctx, r, host)
}

func lookupAWithResolver(ctx context.Context, resolver *net.Resolver, host string) ([]string, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, dnsBaselineTimeout)
	defer cancel()
	addrs, err := resolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		if ip := addr.IP.To4(); ip != nil {
			out = append(out, ip.String())
		}
	}
	return out, nil
}

func remoteSystemLookup(ctx context.Context, sshHost, host string) ([]string, error) {
	out, err := runRemoteDNSCommand(ctx, sshHost, "getent", "ahostsv4", host)
	if err != nil {
		return nil, err
	}
	return parseIPLines(out), nil
}

func remoteResolverLookup(ctx context.Context, sshHost, resolverAddr, host string) ([]string, error) {
	resolverHost, resolverPort := splitResolverForDig(resolverAddr)
	args := []string{"dig", "+time=3", "+tries=1", "+short", "@" + resolverHost}
	if resolverPort != "" && resolverPort != "53" {
		args = append(args, "-p", resolverPort)
	}
	args = append(args, host, "A")
	out, err := runRemoteDNSCommand(ctx, sshHost, args...)
	if err != nil {
		return nil, err
	}
	return parseIPLines(out), nil
}

func runRemoteDNSCommand(ctx context.Context, sshHost string, args ...string) (string, error) {
	sshHost = strings.TrimSpace(sshHost)
	if sshHost == "" {
		return "", fmt.Errorf("empty SSH host")
	}
	cmdArgs := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=5", sshHost}
	cmdArgs = append(cmdArgs, args...)
	cmdCtx, cancel := context.WithTimeout(ctx, dnsBaselineTimeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "ssh", cmdArgs...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, text)
	}
	return text, nil
}

func splitResolverForDig(raw string) (host, port string) {
	addr := normalizeResolverAddress(raw)
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return strings.Trim(raw, "[]"), "53"
	}
	return host, port
}

func parseIPLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		for _, field := range strings.Fields(line) {
			ip := net.ParseIP(strings.TrimSpace(field))
			if ip == nil || ip.To4() == nil {
				continue
			}
			out = append(out, ip.To4().String())
			break
		}
	}
	return out
}

func normalizeDNSAnswers(answers []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(answers))
	for _, answer := range answers {
		ip := net.ParseIP(strings.TrimSpace(answer))
		if ip == nil || ip.To4() == nil {
			continue
		}
		text := ip.To4().String()
		if seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
	}
	sort.Strings(out)
	return out
}

func (p *dnsBaselinePlan) unstableSummary() map[string]any {
	if p == nil {
		return nil
	}
	phases := make([]string, 0, len(p.unstablePhases))
	seen := map[string]bool{}
	for _, phase := range p.unstablePhases {
		if seen[phase] {
			continue
		}
		seen[phase] = true
		phases = append(phases, phase)
	}
	return map[string]any{
		"host":            p.host,
		"samples":         p.samples,
		"unstable_phases": phases,
		"last_result":     p.lastResult,
	}
}

func logDNSBaselineUnstable(ctx context.Context, database recorder, runID string, handles []provisioned, baseline *dnsBaselinePlan) {
	reason := "DNS baseline was unstable during TLS-only scenario"
	for _, h := range handles {
		logMonitorReport(ctx, database, runID, h.a, adapter.RetrieveResult{
			Status:     adapter.RetrieveUnknown,
			Reason:     reason,
			ReasonCode: adapter.ReasonSetupEnvironmentDNSUnstable,
			Metadata: map[string]any{
				"dns_baseline": baseline.unstableSummary(),
			},
		})
	}
}
