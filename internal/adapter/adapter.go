package adapter

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// Adapter is implemented by each monitoring service under evaluation.
// All methods receive a context; adapters must respect cancellation.
type Adapter interface {
	// ServiceID returns the stable identifier for this service, matching
	// the IDs used in scenario TOML files (e.g. "pingdom", "datadog-synthetics").
	ServiceID() string

	// Capabilities returns what this service supports. Called by the harness
	// before provisioning to skip incompatible scenario/service pairs.
	Capabilities() Capabilities

	// Provision creates a monitor on the service for the given target.
	// Returns a MonitorHandle used in subsequent Retrieve and Deprovision calls.
	//
	// Returns *FrequencyError if the service cannot meet config.CheckFrequency.
	Provision(ctx context.Context, target Target, config ProvisionConfig) (MonitorHandle, error)

	// Retrieve fetches all incident events the service reported during the run window.
	// Blocks internally until the adapter considers data complete or ctx is cancelled.
	//
	// Returns RetrieveResult with Status == RetrieveUnknown — not a Go error — when
	// the service API is unreachable, rate-limited, or otherwise unavailable.
	//
	// Go errors indicate hard failures: misconfiguration, programming bugs, or
	// unrecoverable adapter state. They are not retried.
	Retrieve(ctx context.Context, handle MonitorHandle, window RunWindow) (RetrieveResult, error)

	// Deprovision removes the monitor from the service. Must be called even if
	// Provision only partially completed or the scenario aborted midway.
	Deprovision(ctx context.Context, handle MonitorHandle) error

	// Normalize maps a raw service-specific classification label to
	// uptime-bench's common vocabulary. Service-specific complexity
	// belongs in the adapter (see CLAUDE.md), so each adapter owns its
	// own mapping table.
	//
	// Normalized vocabulary:
	//
	//	"http_failure"    — non-2xx/3xx response or connection-level HTTP error
	//	"dns_failure"     — DNS resolution failure of any kind
	//	"tls_failure"     — TLS handshake or certificate error
	//	"tls_advisory"    — TLS concern where the HTTP request can still succeed
	//	"timeout"         — response timeout (any phase)
	//	"content_failure" — body content check failed
	//	"recovered"       — incident resolved
	//	"unknown"         — service could not determine state (monitor-side)
	//	"unrecognized"    — raw label unknown to this adapter; use UnrecognizedClassification
	Normalize(raw string) string
}

// StaleCleaner is implemented by adapters that can list and remove
// benchmark-owned resources that may have survived an interrupted run.
type StaleCleaner interface {
	CleanupStale(ctx context.Context, opts CleanupOptions) (CleanupResult, error)
}

// CleanupOptions controls provider-state cleanup. DryRun reports what would be
// removed without deleting anything. Scope lets adapters limit cleanup to the
// currently configured fleet.
type CleanupOptions struct {
	DryRun bool
	Scope  CleanupScope
}

// CleanupScope describes the target surface that is safe for preflight cleanup.
// An empty scope means "all benchmark-owned resources" for that provider.
type CleanupScope struct {
	TargetHosts []string
	TargetURLs  []string
}

func (s CleanupScope) Empty() bool {
	return len(s.TargetHosts) == 0 && len(s.TargetURLs) == 0
}

// MatchesURL returns whether raw belongs to the cleanup scope. Matching first
// tries full URL equivalence, then falls back to host matching so provider APIs
// that split host/path can still be scoped.
func (s CleanupScope) MatchesURL(raw string) bool {
	if s.Empty() {
		return true
	}
	rawNorm := normalizeCleanupURL(raw)
	if rawNorm != "" {
		for _, targetURL := range s.TargetURLs {
			if rawNorm == normalizeCleanupURL(targetURL) {
				return true
			}
		}
	}
	return s.MatchesHost(raw)
}

// MatchesHost returns whether raw's hostname belongs to the cleanup scope.
func (s CleanupScope) MatchesHost(raw string) bool {
	if s.Empty() {
		return true
	}
	rawHost := cleanupHostname(raw)
	if rawHost == "" {
		return false
	}
	for _, host := range s.TargetHosts {
		if rawHost == cleanupHostname(host) {
			return true
		}
	}
	return false
}

type CleanupResult struct {
	Actions []CleanupAction
}

type CleanupActionType string

const (
	CleanupActionWouldDelete CleanupActionType = "would_delete"
	CleanupActionDeleted     CleanupActionType = "deleted"
	CleanupActionSkipped     CleanupActionType = "skipped"
	CleanupActionError       CleanupActionType = "error"
)

type CleanupCandidate struct {
	ServiceID  string
	ResourceID string
	Kind       string
	Name       string
	URL        string
	Reason     string
	Ambiguous  bool
}

type CleanupAction struct {
	Candidate CleanupCandidate
	Action    CleanupActionType
	Error     string
}

// UnrecognizedClassification is the constant adapters return from Normalize
// when a raw label has no entry in their mapping table. The benchmark
// records the raw label alongside the normalized one so unrecognized
// values can be added later without losing audit data.
const UnrecognizedClassification = "unrecognized"

func normalizeCleanupURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String()
}

func cleanupHostname(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if u, err := url.Parse(raw); err == nil {
			return strings.ToLower(u.Hostname())
		}
	}
	host := raw
	if i := strings.Index(host, "/"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.Trim(host, "[]")
}

// Capabilities describes what a monitoring service supports.
type Capabilities struct {
	// MinCheckFrequency is the shortest check interval the service supports.
	MinCheckFrequency time.Duration

	// SupportsKeyword indicates whether the service can verify a keyword
	// in the response body. When true, the adapter must honour
	// ProvisionConfig.Keyword in present-mode (alert when keyword is
	// missing).
	SupportsKeyword bool

	// SupportsInvertedKeyword indicates whether the service can also
	// verify *absence* of a keyword (KeywordCheck = "absent": alert when
	// keyword is found). Distinct from SupportsKeyword because some
	// services (e.g. Better Uptime's `monitor_type = "keyword"`) only
	// expose the canary direction. When false, the runner gates
	// absent-mode scenarios as a capability_mismatch.
	SupportsInvertedKeyword bool

	// SupportsAgentChecks indicates whether the service has an on-site agent
	// capable of running reverse-check scenarios.
	SupportsAgentChecks bool

	// SupportsMaintenanceWindows indicates whether the adapter can configure
	// a vendor-side suppression window so the monitor still runs but does
	// not fire alerts during the declared interval. When false, the runner
	// gates scenarios with a [maintenance] block as capability_mismatch.
	// See docs/inter-run-state-design.md.
	SupportsMaintenanceWindows bool

	// SupportsCooldownReset indicates whether Deprovision (or a separate
	// reset path) can clear vendor-side alert cooldown so the next run's
	// first alert is not suppressed by the previous run. When false, the
	// measurement engine flags suspect rows as cooldown_uncertain.
	// See docs/inter-run-state-design.md.
	SupportsCooldownReset bool

	// DefaultMaxCallsPerRun is the adapter's default API call budget per run.
	// 0 means unlimited. The fleet.toml value takes precedence when present.
	DefaultMaxCallsPerRun int
}

// Target describes the endpoint to monitor.
type Target struct {
	ID  string
	URL string
}

// ProvisionConfig carries the parameters the adapter uses to configure the monitor.
type ProvisionConfig struct {
	CheckFrequency time.Duration

	// Keyword and KeywordCheck request body-content monitoring. Empty
	// Keyword disables keyword checking; the adapter falls back to a
	// status-only monitor.
	//
	// KeywordCheck is one of:
	//
	//   - KeywordCheckPresent — alert when Keyword is missing from the
	//     response body (the canary case).
	//   - KeywordCheckAbsent — alert when Keyword is found in the
	//     response body (the injected-bad-keyword case).
	//
	// The runner gates this against Capabilities.SupportsKeyword: if the
	// adapter does not support keyword monitoring, the runner skips
	// Provision and writes a capability_mismatch row instead of calling
	// the adapter.
	Keyword      string
	KeywordCheck string

	// MaintenanceWindow, when non-nil, requests a vendor-side maintenance
	// window covering [Start, End]. The adapter must configure the
	// service so alerts during this interval are suppressed without
	// pausing the underlying check. The runner gates this against
	// Capabilities.SupportsMaintenanceWindows: adapters where the flag
	// is false never see a non-nil MaintenanceWindow because the runner
	// has already skipped Provision and written a capability_mismatch
	// row. See docs/inter-run-state-design.md.
	MaintenanceWindow *MaintenanceWindow
}

// MaintenanceWindow describes a vendor-side alert-suppression window.
// Times are absolute (UTC); the runner converts the scenario's relative
// offsets to absolutes at provision time.
type MaintenanceWindow struct {
	Start time.Time
	End   time.Time
}

// KeywordCheck values for ProvisionConfig.KeywordCheck.
const (
	KeywordCheckPresent = "present"
	KeywordCheckAbsent  = "absent"
)

// FrequencyError is returned by Provision when the service cannot meet the
// requested check frequency.
type FrequencyError struct {
	Requested     time.Duration
	MinAchievable time.Duration
}

func (e *FrequencyError) Error() string {
	return fmt.Sprintf(
		"adapter: requested check frequency %v not supported; minimum achievable is %v",
		e.Requested, e.MinAchievable,
	)
}

// MonitorHandle identifies a provisioned monitor on a specific service.
type MonitorHandle struct {
	ServiceID string
	MonitorID string
	Fields    map[string]string
}

// RunWindow describes the time range the harness wants incident data for.
type RunWindow struct {
	RunID          string
	FailureStarted time.Time
	FailureEnded   time.Time
	GracePeriodEnd time.Time
}

// RetrieveResult is returned by Retrieve.
type RetrieveResult struct {
	Status     RetrieveStatus
	Reports    []MonitorReport
	Reason     string // free-form human-readable detail (any Status)
	ReasonCode string // structured categorisation; see Reason*Code constants

	// Metadata describes retrieve-level context that is not tied to a
	// specific alert event. It is especially important when Status is
	// known and Reports is empty: the runner still writes a no-event
	// monitor_reports row so measurement can derive false_negative,
	// maintenance_suppressed, cooldown_suppressed, or cooldown_uncertain.
	Metadata map[string]any
}

// Reason codes that may appear on RetrieveResult.ReasonCode and on the
// monitor_reports.reason_code column. Empty means "not categorised"
// (typically a Known result, or an Unknown without a code attached).
//
// See docs/events.md for the reporting rules and
// docs/inter-run-state-design.md for the maintenance/cooldown codes.
const (
	// ReasonAdapterError: the adapter failed before it could produce a
	// usable monitor result, usually during provision or retrieval. The
	// row is operationally invalid for service-behavior scoring but is
	// still part of provider/API reliability data.
	ReasonAdapterError = "adapter_error"

	// ReasonCapabilityMismatch: the harness skipped Provision because
	// the scenario required a capability the adapter doesn't support.
	// Never counted as a false negative; queryable as the support matrix.
	ReasonCapabilityMismatch = "capability_mismatch"

	// ReasonMaintenanceSuppressed: failure was active and the monitor
	// returned no alerts, but a maintenance window covered the failure
	// period. Correct behaviour, not a false negative. Written by the
	// measurement engine, not the runner.
	ReasonMaintenanceSuppressed = "maintenance_suppressed"

	// ReasonCooldownSuppressed: failure was active, the monitor returned
	// no alerts, no maintenance window applied, and a recent prior run
	// on the same monitor had alerted. Probably the cooldown working;
	// uninformative for benchmark purposes. Written by the measurement
	// engine.
	ReasonCooldownSuppressed = "cooldown_suppressed"

	// ReasonCooldownUncertain: same shape as cooldown_suppressed, but
	// the adapter can only say cooldown may have suppressed the alert
	// because the prior cleanup/reset state was uncertain.
	ReasonCooldownUncertain = "cooldown_uncertain"

	// ReasonCooldownResetFailed: Deprovision attempted to reset the
	// vendor-side alert cooldown and got a non-fatal error. Recorded so
	// the next run's data can be flagged as potentially-cooldown-affected.
	// Written by the runner.
	ReasonCooldownResetFailed = "cooldown_reset_failed"
)

// RetrieveStatus indicates whether the adapter could determine the service's state.
type RetrieveStatus string

const (
	RetrieveKnown   RetrieveStatus = "known"
	RetrieveUnknown RetrieveStatus = "unknown"
)

// MonitorReport is one incident event reported by a monitoring service.
type MonitorReport struct {
	EventType         ReportEventType
	RawClassification string
	ReportedAt        time.Time
	RetrievedAt       time.Time
	Metadata          map[string]any
}

// ReportEventType classifies the kind of event a monitoring service reported.
type ReportEventType string

const (
	EventAlertFired    ReportEventType = "alert_fired"
	EventAlertResolved ReportEventType = "alert_resolved"
	EventStatusChange  ReportEventType = "status_change"
)
