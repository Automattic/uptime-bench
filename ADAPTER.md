# uptime-bench Adapter Interface

This document defines the Go interface all monitor service adapters must implement, the supporting types, and the harness-side responsibilities that complement the interface.

## Design decisions

- **Normalization lives in the harness.** Adapters return the service's raw classification label unmodified. The harness maps raw labels to uptime-bench's common vocabulary via a per-service normalization table. This keeps service-specific vocabulary out of the interface and allows remapping without touching adapter code.
- **Retrieve returns a result struct, not a raw slice.** `RetrieveResult` carries an explicit `Status` (Known or Unknown), any reports retrieved, and a reason when Unknown. This handles partial retrieval (some data returned before the API became unavailable) and makes Unknown unambiguous — it is never inferred from an empty slice.
- **Adapters declare their capabilities.** The harness checks `Capabilities()` before provisioning and skips incompatible scenario/service pairs automatically. This prevents agent-only scenarios from running silently against probe-only services and producing misleading false-negative results.
- **Adapters block internally in Retrieve.** Each adapter polls its service API internally until data is complete or the context is cancelled. The context deadline is the harness's lever for controlling how long it will wait. This keeps the harness simple and lets each adapter encapsulate its own service's data availability patterns.

---

## The interface

```go
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
    // The harness inspects MinAchievable to decide whether to abort or proceed
    // with a degraded configuration.
    Provision(ctx context.Context, target Target, config ProvisionConfig) (MonitorHandle, error)

    // Retrieve fetches all incident events the service reported during the run window.
    // Blocks internally until the adapter considers data complete or ctx is cancelled.
    //
    // Returns RetrieveResult with Status == RetrieveUnknown — not a Go error — when
    // the service API is unreachable, rate-limited, or otherwise unavailable. The
    // harness treats Unknown as a distinct outcome, never as a false negative.
    //
    // Go errors from Retrieve indicate hard failures: misconfiguration, programming
    // bugs, or unrecoverable adapter state. They are not retried.
    Retrieve(ctx context.Context, handle MonitorHandle, window RunWindow) (RetrieveResult, error)

    // Deprovision removes the monitor from the service and cleans up any state
    // created during Provision. Must be called even if Provision only partially
    // completed or the scenario aborted midway.
    Deprovision(ctx context.Context, handle MonitorHandle) error
}
```

---

## Supporting types

### Capabilities

```go
// Capabilities describes what a monitoring service supports.
// The harness uses this to skip incompatible scenario/service combinations
// before provisioning, preventing silent wrong results.
type Capabilities struct {
    // MinCheckFrequency is the shortest check interval the service supports.
    // Provision returns *FrequencyError if the requested interval is shorter.
    MinCheckFrequency time.Duration

    // SupportsKeyword indicates whether the service can verify a keyword
    // in the response body. If false, the harness omits keyword-dependent
    // scenarios for this service. Provision ignores config.Keyword.
    SupportsKeyword bool

    // SupportsAgentChecks indicates whether the service has an on-site agent
    // capable of running reverse-check scenarios (heartbeat, wp-cron, etc.).
    // Probe-only services must set this to false.
    SupportsAgentChecks bool

    // DefaultMaxCallsPerRun is the adapter's own default API call budget per
    // run. The harness uses this when no per-adapter limit is set in fleet.toml.
    // 0 means unlimited — appropriate for self-hosted services with no API cost
    // (e.g. Jetmon). The fleet.toml value always takes precedence over this
    // default when present.
    DefaultMaxCallsPerRun int
}
```

### Target and ProvisionConfig

```go
// Target describes the endpoint to monitor.
type Target struct {
    ID  string // uptime-bench target ID, for logging and run records
    URL string // the URL the monitor will probe
}

// ProvisionConfig carries the parameters the adapter uses to configure the monitor.
type ProvisionConfig struct {
    // CheckFrequency is the desired probe interval. If the service cannot meet
    // this interval, Provision returns *FrequencyError.
    CheckFrequency time.Duration

    // Keyword is the string the monitor should verify in the response body.
    // Empty means no keyword check. Ignored if Capabilities.SupportsKeyword is false.
    Keyword string
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
```

### MonitorHandle

```go
// MonitorHandle identifies a provisioned monitor on a specific service.
// Returned by Provision and passed to Retrieve and Deprovision.
//
// Adapters may store service-specific state in Fields to avoid re-fetching
// it on every call (e.g. check group IDs, region identifiers, webhook IDs).
type MonitorHandle struct {
    ServiceID string
    MonitorID string            // the service-assigned identifier for this monitor
    Fields    map[string]string // adapter-specific state; may be nil
}
```

### RunWindow

```go
// RunWindow describes the time range the harness wants incident data for.
// Passed to Retrieve after the grace period ends.
type RunWindow struct {
    RunID           string
    FailureStarted  time.Time // when failure injection began (ground truth)
    FailureEnded    time.Time // when failure injection stopped (ground truth)
    GracePeriodEnds time.Time // end of the window the harness will wait for resolution
}
```

### RetrieveResult and MonitorReport

```go
// RetrieveResult is returned by Retrieve.
type RetrieveResult struct {
    // Status indicates whether the adapter was able to determine the service's state.
    Status RetrieveStatus

    // Reports contains the incident events the service reported during the window.
    // May be non-empty even when Status is RetrieveUnknown — if the adapter
    // retrieved partial data before the service became unavailable.
    Reports []MonitorReport

    // Reason explains why Status is RetrieveUnknown. Empty when Status is RetrieveKnown.
    Reason string
}

// RetrieveStatus indicates whether the adapter could determine the service's state.
type RetrieveStatus string

const (
    // RetrieveKnown means the adapter successfully retrieved complete incident data.
    RetrieveKnown RetrieveStatus = "known"

    // RetrieveUnknown means the adapter could not determine the service's state.
    // Causes: API unavailable, rate limited, authentication failure, context cancelled
    // before data was complete. Must not be counted as a false negative.
    RetrieveUnknown RetrieveStatus = "unknown"
)

// MonitorReport is one incident event reported by the monitoring service.
type MonitorReport struct {
    EventType ReportEventType

    // RawClassification is the service's own label, unmodified.
    // The harness maps this to a normalized vocabulary via the normalization table.
    // Preserved in the run output for audit and re-normalization.
    RawClassification string

    ReportedAt  time.Time      // when the service recorded this event (service clock)
    RetrievedAt time.Time      // when the adapter fetched this event
    Metadata    map[string]any // adapter-specific fields: HTTP code, probe region, etc.
}

// ReportEventType classifies the kind of event a monitoring service reported.
type ReportEventType string

const (
    EventAlertFired    ReportEventType = "alert_fired"
    EventAlertResolved ReportEventType = "alert_resolved"
    EventStatusChange  ReportEventType = "status_change"
)
```

---

## Harness responsibilities

### Capabilities check

Before provisioning any adapter for a scenario run, the harness calls `Capabilities()` and skips incompatible pairs:

```go
func (h *Harness) compatible(a Adapter, s Scenario) error {
    caps := a.Capabilities()

    if s.RequiresAgentChecks && !caps.SupportsAgentChecks {
        return fmt.Errorf("%s: does not support agent-based scenarios", a.ServiceID())
    }
    if s.CheckFrequency < caps.MinCheckFrequency {
        return &FrequencyError{
            Requested:     s.CheckFrequency,
            MinAchievable: caps.MinCheckFrequency,
        }
    }
    if s.RequiresKeyword && !caps.SupportsKeyword {
        return fmt.Errorf("%s: does not support keyword checks", a.ServiceID())
    }
    return nil
}
```

Skipped pairs are recorded in the run output with reason `"capability_mismatch"` — they do not appear as Unknown or false negatives.

### Normalization

After Retrieve, the harness maps each `RawClassification` to uptime-bench's common vocabulary. The normalization table is the single place that encodes each service's vocabulary. Both the raw and normalized labels are stored in the run output.

```go
// NormalizedClassification maps raw service labels to uptime-bench's vocabulary.
// "unrecognized" is stored when no mapping exists — never silently dropped.
//
// Normalized vocabulary:
//   "http_failure"   — non-2xx/3xx response or connection-level HTTP error
//   "dns_failure"    — DNS resolution failure of any kind
//   "tls_failure"    — TLS handshake or certificate error
//   "timeout"        — response timeout (any phase)
//   "content_failure"— body content check failed (keyword, empty body, error page)
//   "recovered"      — incident resolved
//   "unknown"        — service could not determine state (monitor-side)
//   "unrecognized"   — raw label not in this table; add an entry when seen
var NormalizedClassification = map[string]map[string]string{
    "pingdom": {
        "down":         "http_failure",
        "up":           "recovered",
        "unconfirmed":  "unknown",
    },
    "uptimerobot": {
        "down":         "http_failure",
        "up":           "recovered",
        "seems_down":   "http_failure",
    },
    "datadog-synthetics": {
        "Alert":        "http_failure",
        "No Data":      "unknown",
        "Recovered":    "recovered",
    },
    "better-uptime": {
        "down":         "http_failure",
        "up":           "recovered",
        "paused":       "unknown",
    },
    "jetmon": {
        "down":         "http_failure",
        "seems_down":   "http_failure",
        "degraded":     "http_failure",
        "up":           "recovered",
        "unknown":      "unknown",
    },
}

func Normalize(serviceID, raw string) string {
    if table, ok := NormalizedClassification[serviceID]; ok {
        if normalized, ok := table[raw]; ok {
            return normalized
        }
    }
    return "unrecognized"
}
```

### Unknown vs. false negative

The harness must distinguish these outcomes and never conflate them:

| Outcome | Condition | Accuracy metric impact |
|---|---|---|
| True positive | `alert_fired` during active failure window | Counts for the service |
| False negative | `RetrieveKnown`, no `alert_fired` during failure | Counts against the service |
| Unknown | `RetrieveUnknown` for any reason | Excluded from accuracy metrics; recorded separately |
| Capability skip | Pair skipped before provisioning | Excluded from all metrics; recorded as skipped |

---

## What adapters must guarantee

- `Deprovision` must be safe to call even if `Provision` only partially completed. The harness calls it unconditionally on run end, including aborts.
- `Retrieve` must return `RetrieveUnknown` — not a Go error — when the service API is unavailable. Reserve Go errors for adapter bugs and misconfiguration.
- `Retrieve` must respect context cancellation promptly. When `ctx` is cancelled mid-poll, return whatever has been retrieved so far with `Status: RetrieveUnknown` and `Reason: ctx.Err().Error()`.
- `MonitorHandle.Fields` values must be safe to serialize to strings. The harness persists handles between Provision and Retrieve; complex types do not survive.
- `ServiceID()` must return the same value on every call and must match the IDs used in scenario TOML files exactly.

---

## Implementation notes for common service behaviors

### Alert cooldown / suppression windows

Many monitoring services suppress repeated alerts for the same monitor within a cooldown window (commonly 15–60 minutes). If uptime-bench runs consecutive scenarios against the same provisioned monitor, the second run's alert may be suppressed by the first run's cooldown — producing a result that appears to be a missed detection but is the service working as designed.

**`Deprovision` should clear cooldown state.** If the service API supports resetting alert state (e.g., acknowledging an incident, toggling the monitor off and on), do so in `Deprovision`. If the API does not support this, delete and recreate the monitor — the cost of reprovisioning is acceptable to ensure clean state between runs.

**If neither is possible:** record the monitor's current alert state at the start of `Retrieve`. If the service reports that an alert is currently suppressed, include `"alert_suppressed": true` in `MonitorReport.Metadata`. The measurement engine will distinguish this from a genuine missed detection.

### Per-component timing breakdown

Some services record per-component timings: DNS resolution time, TCP connection time, TLS handshake time, time to first byte. When available, populate the following keys in `MonitorReport.Metadata`:

```
"dns_ms":  float64  // DNS resolution duration in milliseconds
"tcp_ms":  float64  // TCP connection duration
"tls_ms":  float64  // TLS handshake duration
"ttfb_ms": float64  // Time to first response byte
"rtt_ms":  float64  // Total round-trip time
```

This data enables layer-level attribution verification: a `dns_latency` scenario should appear as increased `dns_ms`, not `ttfb_ms`. Adapters that can retrieve this data should always do so.

### Maintenance window provisioning

If the service supports scheduled maintenance windows, expose this via a `ProvisionConfig` extension (a service-specific option passed through `MonitorHandle.Fields`). Do not build maintenance window support into the core `ProvisionConfig` struct — it is a service capability, not a universal one. See ROADMAP.md for the planned maintenance window scenario type.
