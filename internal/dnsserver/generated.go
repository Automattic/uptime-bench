package dnsserver

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// GeneratedRecordRange maps a deterministic host pattern to one target IP.
// It lets capacity runs resolve large host ranges without expanding every
// hostname into the static zone map.
type GeneratedRecordRange struct {
	ID          string
	HostPattern string
	Prefix      string
	Suffix      string
	Width       int
	Start       int
	End         int
	IP          net.IP
	TTL         uint32
}

// NewGeneratedRecordRange parses a host pattern such as
// "site-%07d.load.example.com" and returns a matcher for start..start+count-1.
func NewGeneratedRecordRange(id, hostPattern string, start, count int, ip net.IP, ttl uint32) (GeneratedRecordRange, error) {
	if id == "" {
		return GeneratedRecordRange{}, fmt.Errorf("generated range id is required")
	}
	if hostPattern == "" {
		return GeneratedRecordRange{}, fmt.Errorf("generated range %q: host pattern is required", id)
	}
	if start < 0 {
		return GeneratedRecordRange{}, fmt.Errorf("generated range %q: start must be non-negative", id)
	}
	if count <= 0 {
		return GeneratedRecordRange{}, fmt.Errorf("generated range %q: count must be positive", id)
	}
	if ip == nil || ip.To4() == nil {
		return GeneratedRecordRange{}, fmt.Errorf("generated range %q: IPv4 address is required", id)
	}
	prefix, suffix, width, err := parseGeneratedHostPattern(hostPattern)
	if err != nil {
		return GeneratedRecordRange{}, fmt.Errorf("generated range %q: %w", id, err)
	}
	return GeneratedRecordRange{
		ID:          id,
		HostPattern: strings.ToLower(strings.TrimSuffix(hostPattern, ".")),
		Prefix:      prefix,
		Suffix:      suffix,
		Width:       width,
		Start:       start,
		End:         start + count - 1,
		IP:          ip.To4(),
		TTL:         ttl,
	}, nil
}

func parseGeneratedHostPattern(pattern string) (prefix, suffix string, width int, err error) {
	pattern = strings.ToLower(strings.TrimSuffix(pattern, "."))
	percent := strings.IndexByte(pattern, '%')
	if percent < 0 {
		return "", "", 0, fmt.Errorf("host_pattern must contain one %%d or %%0Nd integer placeholder")
	}
	if strings.Contains(pattern[percent+1:], "%") {
		return "", "", 0, fmt.Errorf("host_pattern must contain exactly one integer placeholder")
	}

	pos := percent + 1
	if pos >= len(pattern) {
		return "", "", 0, fmt.Errorf("host_pattern has incomplete placeholder")
	}
	if pattern[pos] == '0' {
		pos++
		startDigits := pos
		for pos < len(pattern) && pattern[pos] >= '0' && pattern[pos] <= '9' {
			pos++
		}
		if startDigits == pos {
			return "", "", 0, fmt.Errorf("zero-padded placeholder must include a width")
		}
		parsed, err := strconv.Atoi(pattern[startDigits:pos])
		if err != nil || parsed <= 0 {
			return "", "", 0, fmt.Errorf("invalid placeholder width")
		}
		width = parsed
	}
	if pos >= len(pattern) || pattern[pos] != 'd' {
		return "", "", 0, fmt.Errorf("host_pattern must use %%d or %%0Nd")
	}

	return pattern[:percent], pattern[pos+1:], width, nil
}

// Lookup returns an A record when host falls within the generated range.
func (r GeneratedRecordRange) Lookup(host string) (ZoneEntry, bool) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.HasPrefix(host, r.Prefix) || !strings.HasSuffix(host, r.Suffix) {
		return ZoneEntry{}, false
	}
	middle := host[len(r.Prefix) : len(host)-len(r.Suffix)]
	if middle == "" {
		return ZoneEntry{}, false
	}
	if r.Width > 0 && len(middle) < r.Width {
		return ZoneEntry{}, false
	}
	if r.Width == 0 && len(middle) > 1 && middle[0] == '0' {
		return ZoneEntry{}, false
	}
	for _, ch := range middle {
		if ch < '0' || ch > '9' {
			return ZoneEntry{}, false
		}
	}
	n64, err := strconv.ParseInt(middle, 10, 0)
	if err != nil {
		return ZoneEntry{}, false
	}
	n := int(n64)
	if n < r.Start || n > r.End {
		return ZoneEntry{}, false
	}
	return ZoneEntry{IP: r.IP, TTL: r.TTL}, true
}
