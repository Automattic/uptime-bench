package adapter

import (
	"context"
	"fmt"
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
}

// Capabilities describes what a monitoring service supports.
type Capabilities struct {
	// MinCheckFrequency is the shortest check interval the service supports.
	MinCheckFrequency time.Duration

	// SupportsKeyword indicates whether the service can verify a keyword
	// in the response body.
	SupportsKeyword bool

	// SupportsAgentChecks indicates whether the service has an on-site agent
	// capable of running reverse-check scenarios.
	SupportsAgentChecks bool

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
	Keyword        string
}

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
	Status  RetrieveStatus
	Reports []MonitorReport
	Reason  string // populated when Status == RetrieveUnknown
}

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

// NormalizedClassification maps each service's raw incident labels to
// uptime-bench's common vocabulary. "unrecognized" is stored when no
// mapping exists — never silently dropped.
//
// Normalized vocabulary:
//
//	"http_failure"    — non-2xx/3xx response or connection-level HTTP error
//	"dns_failure"     — DNS resolution failure of any kind
//	"tls_failure"     — TLS handshake or certificate error
//	"timeout"         — response timeout (any phase)
//	"content_failure" — body content check failed
//	"recovered"       — incident resolved
//	"unknown"         — service could not determine state (monitor-side)
//	"unrecognized"    — raw label not in this table
var NormalizedClassification = map[string]map[string]string{
	"jetmon-v1": {
		"down":       "http_failure",
		"seems_down": "http_failure",
		"degraded":   "http_failure",
		"up":         "recovered",
		"unknown":    "unknown",
	},
	// "jetmon-v2" intentionally omitted until the Jetmon 2 public API lands
	// and a real adapter replaces the stub in internal/adapter/jetmonv2.
	"uptimerobot": {
		"down":       "http_failure",
		"up":         "recovered",
		"seems_down": "http_failure",
	},
	"pingdom": {
		"down":        "http_failure",
		"up":          "recovered",
		"unconfirmed": "unknown",
	},
	"datadog-synthetics": {
		"Alert":     "http_failure",
		"No Data":   "unknown",
		"Recovered": "recovered",
	},
	"better-uptime": {
		"down":   "http_failure",
		"up":     "recovered",
		"paused": "unknown",
	},
}

// Normalize maps a raw service classification label to uptime-bench's vocabulary.
func Normalize(serviceID, raw string) string {
	if table, ok := NormalizedClassification[serviceID]; ok {
		if normalized, ok := table[raw]; ok {
			return normalized
		}
	}
	return "unrecognized"
}
