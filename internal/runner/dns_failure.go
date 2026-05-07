package runner

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
	"github.com/Automattic/uptime-bench/internal/control"
	"github.com/Automattic/uptime-bench/internal/fleet"
	"github.com/Automattic/uptime-bench/internal/scenario"
)

type controlMember struct {
	ID          string
	Kind        string
	Address     string
	ControlPort int
	DNSPort     int
}

func controlMembersForFailure(fl *fleet.Config, target fleet.Target, f scenario.Failure, seed int64) ([]controlMember, error) {
	if isDNSFailureType(f.Type) {
		if len(fl.Nameservers) == 0 {
			return nil, fmt.Errorf("dns failure %s requires at least one configured nameserver", f.Type)
		}
		members := make([]controlMember, 0, len(fl.Nameservers))
		for _, ns := range fl.Nameservers {
			members = append(members, controlMember{
				ID:          ns.ID,
				Kind:        "dns",
				Address:     ns.Address,
				ControlPort: ns.ControlPort,
				DNSPort:     ns.DNSPort,
			})
		}
		if f.Type != "dns_ns_unavailable" {
			return members, nil
		}
		if len(members) < 2 {
			return nil, fmt.Errorf("dns_ns_unavailable requires at least two configured nameservers")
		}
		rate := f.Rate
		if rate <= 0 {
			rate = 1
		}
		if rate > 1 {
			rate = 1
		}
		affected := int(rate*float64(len(members)) + 0.999999)
		if affected < 1 {
			affected = 1
		}
		if affected > len(members) {
			affected = len(members)
		}
		sort.Slice(members, func(i, j int) bool {
			return controlMemberSelectionKey(seed, f.Type, members[i].ID) < controlMemberSelectionKey(seed, f.Type, members[j].ID)
		})
		return members[:affected], nil
	}
	return []controlMember{{
		ID:          target.ID,
		Kind:        "target",
		Address:     target.Address,
		ControlPort: target.ControlPort,
	}}, nil
}

func controlMemberSelectionKey(seed int64, failureType, memberID string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s", seed, failureType, memberID)))
	return hex.EncodeToString(sum[:])
}

func controlMemberIDs(members []controlMember) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.ID)
	}
	return out
}

func activateFailure(ctx context.Context, members []controlMember, token string, timeout time.Duration, req control.ActivateRequest) error {
	for _, m := range members {
		client := controlClientForMember(m, token, timeout)
		if err := client.Activate(ctx, req); err != nil {
			return fmt.Errorf("%s/%s: %w", m.Kind, m.ID, err)
		}
	}
	return nil
}

func deactivateFailure(ctx context.Context, members []controlMember, token string, timeout time.Duration, req control.DeactivateRequest) error {
	var errs []string
	for _, m := range members {
		client := controlClientForMember(m, token, timeout)
		if err := client.Deactivate(ctx, req); err != nil {
			errs = append(errs, fmt.Sprintf("%s/%s: %v", m.Kind, m.ID, err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

func controlClientForMember(m controlMember, token string, timeout time.Duration) *control.Client {
	return control.NewClient(
		fmt.Sprintf("http://%s:%d", m.Address, m.ControlPort),
		token,
		&http.Client{Timeout: timeout},
	)
}

func isDNSFailureType(failureType string) bool {
	switch failureType {
	case "dns_nxdomain", "dns_servfail", "dns_timeout", "dns_cname_nxdomain", "dns_latency", "dns_ns_unavailable":
		return true
	default:
		return false
	}
}

type dnsExposureResult struct {
	Host        string             `json:"host"`
	FailureType string             `json:"failure_type"`
	Observable  bool               `json:"observable"`
	Reason      string             `json:"reason,omitempty"`
	Checks      []dnsExposureCheck `json:"checks"`
}

type dnsExposureCheck struct {
	NameserverID string `json:"nameserver_id"`
	Address      string `json:"address"`
	Observable   bool   `json:"observable"`
	Error        string `json:"error,omitempty"`
	RCode        int    `json:"rcode,omitempty"`
	AnswerCount  int    `json:"answer_count,omitempty"`
	HasCNAME     bool   `json:"has_cname,omitempty"`
	ElapsedMS    int64  `json:"elapsed_ms"`
}

func checkDNSFailureExposure(ctx context.Context, f scenario.Failure, host string, members []controlMember, controlTimeout time.Duration) dnsExposureResult {
	result := dnsExposureResult{
		Host:        host,
		FailureType: f.Type,
		Observable:  true,
	}
	if strings.TrimSpace(host) == "" {
		result.Observable = false
		result.Reason = "empty DNS host"
		return result
	}
	if len(members) == 0 {
		result.Observable = false
		result.Reason = "no DNS control members selected"
		return result
	}
	timeout := dnsExposureProbeTimeout(f, controlTimeout)
	for _, m := range members {
		if m.Kind != "dns" {
			continue
		}
		check := probeAuthoritativeDNS(ctx, m, host, timeout)
		check.Observable = dnsProbeMatchesFailure(f, check)
		if !check.Observable {
			result.Observable = false
		}
		result.Checks = append(result.Checks, check)
	}
	if len(result.Checks) == 0 {
		result.Observable = false
		result.Reason = "no DNS members were probed"
		return result
	}
	if !result.Observable && result.Reason == "" {
		result.Reason = "authoritative DNS response did not match requested failure"
	}
	return result
}

func dnsExposureProbeTimeout(f scenario.Failure, controlTimeout time.Duration) time.Duration {
	timeout := 3 * time.Second
	if controlTimeout > 0 && controlTimeout < timeout {
		timeout = controlTimeout
	}
	if f.Type == "dns_latency" && f.AddedLatency > 0 && timeout <= f.AddedLatency {
		timeout = f.AddedLatency + time.Second
		if controlTimeout > 0 && timeout > controlTimeout {
			timeout = controlTimeout
		}
	}
	return timeout
}

func probeAuthoritativeDNS(ctx context.Context, m controlMember, host string, timeout time.Duration) dnsExposureCheck {
	check := dnsExposureCheck{
		NameserverID: m.ID,
		Address:      net.JoinHostPort(m.Address, fmt.Sprintf("%d", m.DNSPort)),
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var dialer net.Dialer
	conn, err := dialer.DialContext(probeCtx, "udp", check.Address)
	if err != nil {
		check.Error = err.Error()
		return check
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	query := buildDNSAQuery(host)
	start := time.Now()
	if _, err := conn.Write(query); err != nil {
		check.ElapsedMS = time.Since(start).Milliseconds()
		check.Error = err.Error()
		return check
	}
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	check.ElapsedMS = time.Since(start).Milliseconds()
	if err != nil {
		check.Error = err.Error()
		return check
	}
	check.RCode, check.AnswerCount, check.HasCNAME, err = parseDNSProbeResponse(buf[:n])
	if err != nil {
		check.Error = err.Error()
	}
	return check
}

func dnsProbeMatchesFailure(f scenario.Failure, check dnsExposureCheck) bool {
	switch f.Type {
	case "dns_nxdomain":
		return check.Error == "" && check.RCode == 3
	case "dns_servfail":
		return check.Error == "" && check.RCode == 2
	case "dns_timeout":
		return isTimeoutErrorText(check.Error)
	case "dns_cname_nxdomain":
		return check.Error == "" && check.RCode == 0 && check.HasCNAME
	case "dns_latency":
		if check.Error != "" || check.RCode != 0 {
			return false
		}
		minLatency := f.AddedLatency - 25*time.Millisecond
		if minLatency < 0 {
			minLatency = 0
		}
		return time.Duration(check.ElapsedMS)*time.Millisecond >= minLatency
	case "dns_ns_unavailable":
		if f.Mode == "servfail" {
			return check.Error == "" && check.RCode == 2
		}
		return isTimeoutErrorText(check.Error)
	default:
		return false
	}
}

func isTimeoutErrorText(s string) bool {
	s = strings.ToLower(s)
	return strings.Contains(s, "timeout") ||
		strings.Contains(s, "i/o timeout") ||
		strings.Contains(s, "deadline exceeded")
}

func buildDNSAQuery(host string) []byte {
	host = strings.TrimSuffix(strings.TrimSpace(host), ".")
	msg := make([]byte, 0, 512)
	msg = append(msg, 0x12, 0x34) // transaction ID
	msg = append(msg, 0x01, 0x00) // recursion desired; authoritative server may ignore it
	msg = append(msg, 0x00, 0x01) // QDCOUNT
	msg = append(msg, 0x00, 0x00) // ANCOUNT
	msg = append(msg, 0x00, 0x00) // NSCOUNT
	msg = append(msg, 0x00, 0x00) // ARCOUNT
	for _, label := range strings.Split(host, ".") {
		msg = append(msg, byte(len(label)))
		msg = append(msg, label...)
	}
	msg = append(msg, 0x00)       // root label
	msg = append(msg, 0x00, 0x01) // QTYPE A
	msg = append(msg, 0x00, 0x01) // QCLASS IN
	return msg
}

func parseDNSProbeResponse(msg []byte) (rcode, answerCount int, hasCNAME bool, err error) {
	if len(msg) < 12 {
		return 0, 0, false, fmt.Errorf("short DNS response")
	}
	rcode = int(msg[3] & 0x0F)
	qdcount := int(binary.BigEndian.Uint16(msg[4:6]))
	answerCount = int(binary.BigEndian.Uint16(msg[6:8]))
	pos := 12
	for i := 0; i < qdcount; i++ {
		var ok bool
		pos, ok = skipDNSName(msg, pos)
		if !ok || pos+4 > len(msg) {
			return rcode, answerCount, false, fmt.Errorf("malformed DNS question")
		}
		pos += 4
	}
	for i := 0; i < answerCount; i++ {
		var ok bool
		pos, ok = skipDNSName(msg, pos)
		if !ok || pos+10 > len(msg) {
			return rcode, answerCount, hasCNAME, fmt.Errorf("malformed DNS answer")
		}
		typ := binary.BigEndian.Uint16(msg[pos : pos+2])
		rdlen := int(binary.BigEndian.Uint16(msg[pos+8 : pos+10]))
		pos += 10
		if pos+rdlen > len(msg) {
			return rcode, answerCount, hasCNAME, fmt.Errorf("truncated DNS answer")
		}
		if typ == 5 {
			hasCNAME = true
		}
		pos += rdlen
	}
	return rcode, answerCount, hasCNAME, nil
}

func skipDNSName(msg []byte, pos int) (int, bool) {
	for {
		if pos >= len(msg) {
			return 0, false
		}
		l := int(msg[pos])
		pos++
		if l == 0 {
			return pos, true
		}
		if l&0xC0 == 0xC0 {
			if pos >= len(msg) {
				return 0, false
			}
			return pos + 1, true
		}
		if l&0xC0 != 0 {
			return 0, false
		}
		if pos+l > len(msg) {
			return 0, false
		}
		pos += l
	}
}

func logFailureNotObservable(ctx context.Context, database recorder, runID string, handles []provisioned, f scenario.Failure, exposure dnsExposureResult) {
	reason := fmt.Sprintf("%s failure was not observable from authoritative DNS controls", f.Type)
	for _, h := range handles {
		logMonitorReport(ctx, database, runID, h.a, adapter.RetrieveResult{
			Status:     adapter.RetrieveUnknown,
			Reason:     reason,
			ReasonCode: adapter.ReasonFailureNotObservable,
			Metadata: map[string]any{
				"failure_type": f.Type,
				"dns_exposure": exposure,
			},
		})
	}
}
