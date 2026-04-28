// Package probeips parses public monitoring vendor probe IP feeds into the
// services.toml probe_ranges shape used by geographic failure scenarios.
package probeips

import (
	"encoding/json"
	"fmt"
	"html"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	ServicePingdom        = "pingdom"
	ServiceUptimeRobot    = "uptimerobot"
	ServiceDatadog        = "datadog-synthetics"
	ServiceBetterUptime   = "better-uptime"
	DefaultPingdomURL     = "https://my.pingdom.com/probes/ipv4"
	DefaultUptimeRobotURL = "https://uptimerobot.com/inc/files/ips/IPv4andIPv6.txt"
	DefaultDatadogURL     = "https://ip-ranges.datadoghq.com/"
	DefaultBetterStackURL = "https://betterstack.com/docs/uptime/frequently-asked-questions/"
)

// ServiceRanges is the normalized probe range output for one service.
type ServiceRanges struct {
	ServiceID   string
	ServiceType string
	SourceURL   string
	Regions     map[string][]string
	Warnings    []string
}

// RegionMap maps untagged probe IPs to operator-maintained regions.
type RegionMap struct {
	Prefixes []RegionPrefix `json:"prefixes"`
}

// RegionPrefix assigns CIDR prefixes to a probe_ranges region.
type RegionPrefix struct {
	Region string   `json:"region"`
	CIDRs  []string `json:"cidrs"`
}

// ParseRegionMap parses the operator-maintained UptimeRobot region map.
func ParseRegionMap(data []byte) (RegionMap, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return RegionMap{}, nil
	}
	var m RegionMap
	if err := json.Unmarshal(data, &m); err != nil {
		return RegionMap{}, fmt.Errorf("parse region map: %w", err)
	}
	return m, nil
}

// NormalizeCIDR accepts either a CIDR or a bare IP address. Bare IPv4
// addresses become /32 and bare IPv6 addresses become /128.
func NormalizeCIDR(raw string) (string, bool) {
	s := strings.TrimSpace(html.UnescapeString(raw))
	s = strings.Trim(s, "`'\"[]{}()<>,;")
	s = strings.TrimRight(s, ".,;")
	if s == "" {
		return "", false
	}
	if strings.Contains(s, "/") {
		prefix, err := netip.ParsePrefix(s)
		if err != nil {
			return "", false
		}
		return prefix.Masked().String(), true
	}
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return "", false
	}
	bits := 32
	if addr.Is6() {
		bits = 128
	}
	return netip.PrefixFrom(addr, bits).String(), true
}

// CanonicalRegion folds vendor-specific location names into the coarse region
// keys used by uptime-bench scenarios.
func CanonicalRegion(raw string) string {
	r := strings.ToLower(strings.TrimSpace(html.UnescapeString(raw)))
	r = strings.Trim(r, "`'\"[]{}")
	if r == "" {
		return ""
	}

	if strings.Contains(r, "north america") {
		return "us-east"
	}
	if strings.Contains(r, "south america") {
		return "sa-east"
	}
	if strings.Contains(r, "australia") || strings.Contains(r, "oceania") {
		return "ap-sea"
	}
	if strings.Contains(r, "asia") {
		return "ap-sea"
	}
	if strings.Contains(r, "europe") {
		return "eu-west"
	}
	if strings.Contains(r, "africa") {
		return "af-south"
	}

	if idx := strings.LastIndex(r, ":"); idx >= 0 && idx+1 < len(r) {
		r = r[idx+1:]
	}
	r = normalizeRegionLabel(r)

	switch {
	case strings.HasPrefix(r, "us-east"), strings.HasPrefix(r, "eastus"), strings.HasPrefix(r, "east-us"):
		return "us-east"
	case strings.HasPrefix(r, "us-west"), strings.HasPrefix(r, "westus"), strings.HasPrefix(r, "west-us"):
		return "us-west"
	case strings.HasPrefix(r, "eu-west"), strings.HasPrefix(r, "europe-west"):
		return "eu-west"
	case strings.HasPrefix(r, "eu-central"), strings.HasPrefix(r, "europe-central"):
		return "eu-central"
	case strings.HasPrefix(r, "eu-north"), strings.HasPrefix(r, "europe-north"):
		return "eu-north"
	case strings.HasPrefix(r, "eu-south"), strings.HasPrefix(r, "europe-south"):
		return "eu-south"
	case strings.HasPrefix(r, "ap-southeast"), strings.HasPrefix(r, "asia-southeast"):
		return "ap-sea"
	case strings.HasPrefix(r, "ap-northeast"), strings.HasPrefix(r, "asia-northeast"):
		return "ap-ne"
	case strings.HasPrefix(r, "ap-south"), strings.HasPrefix(r, "asia-south"):
		return "ap-south"
	case strings.HasPrefix(r, "ap-east"), strings.HasPrefix(r, "asia-east"):
		return "ap-east"
	case strings.HasPrefix(r, "ca-central"), strings.HasPrefix(r, "canada-central"):
		return "ca-central"
	case strings.HasPrefix(r, "sa-east"), strings.HasPrefix(r, "southamerica-east"):
		return "sa-east"
	case strings.HasPrefix(r, "af-south"):
		return "af-south"
	case strings.HasPrefix(r, "me-"):
		return "me"
	}
	return r
}

// ParseDatadog parses Datadog's ip-ranges endpoint and extracts only the
// synthetics public probe pools.
func ParseDatadog(data []byte) (map[string][]string, []string, error) {
	var root struct {
		Synthetics struct {
			PrefixesIPv4           []string            `json:"prefixes_ipv4"`
			PrefixesIPv6           []string            `json:"prefixes_ipv6"`
			PrefixesIPv4ByLocation map[string][]string `json:"prefixes_ipv4_by_location"`
			PrefixesIPv6ByLocation map[string][]string `json:"prefixes_ipv6_by_location"`
		} `json:"synthetics"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, nil, fmt.Errorf("parse datadog ip ranges: %w", err)
	}

	regions := make(map[string][]string)
	invalid := 0
	for location, cidrs := range root.Synthetics.PrefixesIPv4ByLocation {
		invalid += addMany(regions, location, cidrs)
	}
	for location, cidrs := range root.Synthetics.PrefixesIPv6ByLocation {
		invalid += addMany(regions, location, cidrs)
	}

	var warnings []string
	if len(regions) == 0 {
		invalid += addMany(regions, "global", root.Synthetics.PrefixesIPv4)
		invalid += addMany(regions, "global", root.Synthetics.PrefixesIPv6)
		if len(regions) > 0 {
			warnings = append(warnings, "Datadog synthetics feed did not include per-location maps; emitted flat prefixes under global")
		}
	} else {
		warnings = append(warnings, "Datadog provider locations were normalized to coarse uptime-bench regions; review before applying")
	}
	if invalid > 0 {
		warnings = append(warnings, fmt.Sprintf("ignored %d invalid Datadog prefix value(s)", invalid))
	}
	regions = normalizeRegions(regions)
	if len(regions) == 0 {
		return nil, warnings, fmt.Errorf("parse datadog ip ranges: no synthetics prefixes found")
	}
	return regions, warnings, nil
}

// ParsePingdom parses Pingdom's public probe IP endpoint. The endpoint has
// been observed as plain text even though older docs describe JSON, so both
// shapes are accepted.
func ParsePingdom(data []byte) (map[string][]string, []string, error) {
	regions, invalid, jsonErr := parseMaybeJSON(data)
	var warnings []string
	if jsonErr == nil && len(regions) > 0 {
		if hasOnlyRegion(regions, "global") {
			warnings = append(warnings, "Pingdom feed did not include region labels; emitted prefixes under global")
		}
		if invalid > 0 {
			warnings = append(warnings, fmt.Sprintf("ignored %d invalid Pingdom value(s)", invalid))
		}
		return normalizeRegions(regions), warnings, nil
	}

	regions, invalid = parseTextCIDRs(data, "global")
	if len(regions) == 0 {
		if jsonErr != nil {
			return nil, nil, fmt.Errorf("parse pingdom probe list: no IPs found; JSON parse error: %w", jsonErr)
		}
		return nil, nil, fmt.Errorf("parse pingdom probe list: no IPs found")
	}
	warnings = append(warnings, "Pingdom probe feed is untagged plain text; emitted prefixes under global")
	if invalid > 0 {
		warnings = append(warnings, fmt.Sprintf("ignored %d invalid Pingdom value(s)", invalid))
	}
	return normalizeRegions(regions), warnings, nil
}

// ParseUptimeRobot parses UptimeRobot's untagged IP list. When regionMap is
// empty, all prefixes are emitted under global. When it is populated, unknown
// prefixes are emitted under unmapped with a warning.
func ParseUptimeRobot(data []byte, regionMap RegionMap) (map[string][]string, []string, error) {
	source, invalid := parseTextCIDRs(data, "global")
	cidrs := source["global"]
	var warnings []string
	if invalid > 0 {
		warnings = append(warnings, fmt.Sprintf("ignored %d invalid UptimeRobot value(s)", invalid))
	}
	if len(cidrs) == 0 {
		return nil, warnings, fmt.Errorf("parse uptimerobot ip list: no IPs found")
	}
	if len(regionMap.Prefixes) == 0 {
		warnings = append(warnings, "UptimeRobot feed has no region tags and no region map entries; emitted prefixes under global")
		return normalizeRegions(source), warnings, nil
	}

	compiled, mapWarnings := compileRegionMap(regionMap)
	warnings = append(warnings, mapWarnings...)
	if len(compiled) == 0 {
		warnings = append(warnings, "UptimeRobot region map had no valid prefixes; emitted prefixes under global")
		return normalizeRegions(source), warnings, nil
	}

	regions := make(map[string][]string)
	unmapped := 0
	for _, cidr := range cidrs {
		prefix, ok := parsePrefix(cidr)
		if !ok {
			continue
		}
		region := ""
		for _, entry := range compiled {
			if prefixesOverlap(prefix, entry.prefix) {
				region = entry.region
				break
			}
		}
		if region == "" {
			region = "unmapped"
			unmapped++
		}
		addCIDR(regions, region, cidr)
	}
	if unmapped > 0 {
		warnings = append(warnings, fmt.Sprintf("%d UptimeRobot prefix(es) did not match the region map; emitted under unmapped", unmapped))
	}
	return normalizeRegions(regions), warnings, nil
}

// ParseBetterStack parses Better Stack's documentation page. The page is
// prose/HTML rather than a stable data feed, so the parser recognizes the
// current broad regional headings and emits a warning.
func ParseBetterStack(data []byte) (map[string][]string, []string, error) {
	text := focusBetterStackIPSection(html.UnescapeString(stripTags(string(data))))
	markers := betterRegionMarkers(text)
	var warnings []string
	regions := make(map[string][]string)
	invalid := 0
	if len(markers) == 0 {
		regions, invalid = parseTextCIDRs([]byte(text), "global")
		if len(regions) > 0 {
			warnings = append(warnings, "Better Stack IP documentation did not expose recognizable region headings; emitted prefixes under global")
		}
	} else {
		for i, marker := range markers {
			end := len(text)
			if i+1 < len(markers) {
				end = markers[i+1].start
			}
			section := text[marker.end:end]
			sectionRegions, sectionInvalid := parseTextCIDRs([]byte(section), marker.region)
			invalid += sectionInvalid
			mergeRegions(regions, sectionRegions)
		}
		if len(regions) > 0 {
			warnings = append(warnings, "Better Stack publishes broad prose regions; normalized them to coarse uptime-bench regions")
		}
	}
	if invalid > 0 {
		warnings = append(warnings, fmt.Sprintf("ignored %d invalid Better Stack value(s)", invalid))
	}
	regions = normalizeRegions(regions)
	if len(regions) == 0 {
		return nil, warnings, fmt.Errorf("parse better stack ip documentation: no IPs found")
	}
	return regions, warnings, nil
}

// FormatTOML emits a reviewable services.toml fragment. It deliberately emits
// complete [[services]] blocks so the fragment remains parseable on its own,
// but operators should merge only the [services.probe_ranges] updates.
func FormatTOML(items []ServiceRanges) string {
	cp := append([]ServiceRanges(nil), items...)
	sort.Slice(cp, func(i, j int) bool {
		return cp[i].ServiceID < cp[j].ServiceID
	})

	var b strings.Builder
	b.WriteString("# Generated by probe-ips-refresh.\n")
	b.WriteString("# Review and merge probe_ranges by hand; do not append this fragment wholesale.\n\n")

	for i, item := range cp {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "[[services]]\n")
		fmt.Fprintf(&b, "id = %s\n", strconv.Quote(item.ServiceID))
		fmt.Fprintf(&b, "type = %s\n", strconv.Quote(item.ServiceType))
		if item.SourceURL != "" {
			fmt.Fprintf(&b, "# source: %s\n", sanitizeComment(item.SourceURL))
		}
		for _, warning := range item.Warnings {
			fmt.Fprintf(&b, "# warning: %s\n", sanitizeComment(warning))
		}
		b.WriteString("\n[services.probe_ranges]\n")
		normalizedRegions := normalizeRegions(item.Regions)
		regions := sortedRegionKeys(normalizedRegions)
		for _, region := range regions {
			fmt.Fprintf(&b, "%s = [\n", strconv.Quote(region))
			cidrs := append([]string(nil), normalizedRegions[region]...)
			sort.Strings(cidrs)
			for j, cidr := range cidrs {
				suffix := ","
				if j == len(cidrs)-1 {
					suffix = ""
				}
				fmt.Fprintf(&b, "  %s%s\n", strconv.Quote(cidr), suffix)
			}
			b.WriteString("]\n")
		}
	}
	return b.String()
}

type compiledPrefix struct {
	region string
	prefix netip.Prefix
}

type betterMarker struct {
	start  int
	end    int
	region string
}

var (
	ipTokenRE = regexp.MustCompile(`(?i)(?:\d{1,3}\.){3}\d{1,3}(?:/\d{1,2})?|(?:[0-9a-f]{0,4}:){2,}[0-9a-f:.]{0,}(?:/\d{1,3})?`)
	tagRE     = regexp.MustCompile(`<[^>]+>`)
	spaceRE   = regexp.MustCompile(`\s+`)
	regionRE  = regexp.MustCompile(`(?i)\b(?:AS\s*\(Asia\)|AU\s*\(Australia\)|OC\s*\(Oceania\)|EU\s*\(Europe\)|NA\s*\(North America\)|SA\s*\(South America\)|AF\s*\(Africa\)|Asia|Australia|Oceania|Europe|North America|South America|Africa)\b`)
	labelRE   = regexp.MustCompile(`[^a-z0-9]+`)
)

func parseMaybeJSON(data []byte) (map[string][]string, int, error) {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
		return nil, 0, fmt.Errorf("not JSON")
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, 0, err
	}
	regions := make(map[string][]string)
	invalid := collectJSONIPs(v, "global", regions)
	return normalizeRegions(regions), invalid, nil
}

func collectJSONIPs(v any, region string, regions map[string][]string) int {
	invalid := 0
	switch x := v.(type) {
	case []any:
		for _, item := range x {
			invalid += collectJSONIPs(item, region, regions)
		}
	case map[string]any:
		if r := firstStringField(x, "region", "region_name", "location", "location_name", "country", "city"); r != "" {
			region = r
		}
		for _, key := range []string{"ip", "ipv4", "ipv6", "ip_address", "address", "cidr", "prefix"} {
			if val, ok := x[key]; ok {
				invalid += collectJSONIPs(val, region, regions)
			}
		}
		for key, val := range x {
			if isJSONIPField(key) {
				continue
			}
			childRegion := region
			if isRegionishKey(key) {
				childRegion = key
			}
			invalid += collectJSONIPs(val, childRegion, regions)
		}
	case string:
		if !addCIDR(regions, region, x) && looksLikeIPCandidate(x) {
			invalid++
		}
	}
	return invalid
}

func firstStringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if raw, ok := m[key]; ok {
			if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	}
	return ""
}

func isJSONIPField(key string) bool {
	switch strings.ToLower(key) {
	case "ip", "ipv4", "ipv6", "ip_address", "address", "cidr", "prefix",
		"region", "region_name", "location", "location_name", "country", "city":
		return true
	default:
		return false
	}
}

func isRegionishKey(key string) bool {
	k := strings.ToLower(key)
	if strings.Contains(k, ":") {
		return true
	}
	for _, part := range []string{"us-", "eu-", "ap-", "asia", "europe", "america", "africa", "australia"} {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

func parseTextCIDRs(data []byte, defaultRegion string) (map[string][]string, int) {
	regions := make(map[string][]string)
	invalid := 0
	for _, token := range ipTokenRE.FindAllString(string(data), -1) {
		if !addCIDR(regions, defaultRegion, token) {
			invalid++
		}
	}
	return normalizeRegions(regions), invalid
}

func addMany(regions map[string][]string, region string, values []string) int {
	invalid := 0
	for _, value := range values {
		if !addCIDR(regions, region, value) {
			invalid++
		}
	}
	return invalid
}

func addCIDR(regions map[string][]string, region, value string) bool {
	cidr, ok := NormalizeCIDR(value)
	if !ok {
		return false
	}
	region = CanonicalRegion(region)
	if region == "" {
		region = "global"
	}
	regions[region] = append(regions[region], cidr)
	return true
}

func normalizeRegions(in map[string][]string) map[string][]string {
	out := make(map[string][]string, len(in))
	for region, cidrs := range in {
		region = CanonicalRegion(region)
		if region == "" {
			region = "global"
		}
		seen := make(map[string]struct{}, len(cidrs))
		for _, cidr := range cidrs {
			normalized, ok := NormalizeCIDR(cidr)
			if !ok {
				continue
			}
			seen[normalized] = struct{}{}
		}
		if len(seen) == 0 {
			continue
		}
		out[region] = make([]string, 0, len(seen))
		for cidr := range seen {
			out[region] = append(out[region], cidr)
		}
		sort.Strings(out[region])
	}
	return out
}

func mergeRegions(dst, src map[string][]string) {
	for region, cidrs := range src {
		for _, cidr := range cidrs {
			addCIDR(dst, region, cidr)
		}
	}
}

func compileRegionMap(regionMap RegionMap) ([]compiledPrefix, []string) {
	var compiled []compiledPrefix
	var warnings []string
	for i, entry := range regionMap.Prefixes {
		region := CanonicalRegion(entry.Region)
		if region == "" {
			warnings = append(warnings, fmt.Sprintf("region map entry %d has empty region; ignored", i))
			continue
		}
		for _, cidr := range entry.CIDRs {
			prefix, ok := parsePrefix(cidr)
			if !ok {
				warnings = append(warnings, fmt.Sprintf("region map entry %d has invalid CIDR %q; ignored", i, cidr))
				continue
			}
			compiled = append(compiled, compiledPrefix{region: region, prefix: prefix})
		}
	}
	return compiled, warnings
}

func parsePrefix(cidr string) (netip.Prefix, bool) {
	normalized, ok := NormalizeCIDR(cidr)
	if !ok {
		return netip.Prefix{}, false
	}
	prefix, err := netip.ParsePrefix(normalized)
	if err != nil {
		return netip.Prefix{}, false
	}
	return prefix.Masked(), true
}

func prefixesOverlap(a, b netip.Prefix) bool {
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}

func hasOnlyRegion(regions map[string][]string, want string) bool {
	if len(regions) != 1 {
		return false
	}
	_, ok := regions[want]
	return ok
}

func stripTags(s string) string {
	s = tagRE.ReplaceAllString(s, "\n")
	return spaceRE.ReplaceAllString(s, " ")
}

func focusBetterStackIPSection(text string) string {
	lower := strings.ToLower(text)
	for _, marker := range []string{
		"what ips does better stack use",
		"uptime ip addresses",
		"ip addresses and user-agent",
	} {
		if idx := strings.Index(lower, marker); idx >= 0 {
			return text[idx:]
		}
	}
	return text
}

func betterRegionMarkers(text string) []betterMarker {
	matches := regionRE.FindAllStringIndex(text, -1)
	markers := make([]betterMarker, 0, len(matches))
	for _, match := range matches {
		label := text[match[0]:match[1]]
		region := CanonicalRegion(label)
		if region == "" {
			continue
		}
		if len(markers) > 0 && markers[len(markers)-1].region == region && markers[len(markers)-1].end == match[0] {
			continue
		}
		markers = append(markers, betterMarker{start: match[0], end: match[1], region: region})
	}
	return markers
}

func sortedRegionKeys(regions map[string][]string) []string {
	keys := make([]string, 0, len(regions))
	for region, cidrs := range regions {
		if len(cidrs) > 0 {
			keys = append(keys, region)
		}
	}
	sort.Strings(keys)
	return keys
}

func sanitizeComment(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

func normalizeRegionLabel(s string) string {
	s = labelRE.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	for strings.Contains(s, "--") {
		s = strings.ReplaceAll(s, "--", "-")
	}
	return s
}

func looksLikeIPCandidate(s string) bool {
	return ipTokenRE.MatchString(s)
}
