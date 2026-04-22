// Package jetmon implements the uptime-bench adapter for Jetmon 1.
//
// Jetmon monitors are always-on and must be pre-seeded before a run.
// Provision looks up the existing monitor by URL; it does not create one.
// Deprovision is a no-op.
//
// Required services.toml fields:
//
//	url  — root URL of the Jetmon API (no public endpoint; must be configured)
//	auth = { token = "..." }
package jetmon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

// adapterType is the key used in adapter.NormalizedClassification.
const adapterType = "jetmon"

// statusConfirmedDown is Jetmon's site_status value for a confirmed outage.
const statusConfirmedDown = 2

// Adapter implements adapter.Adapter for Jetmon 1.
type Adapter struct {
	id     string
	apiURL string
	token  string
	client *http.Client
}

// New creates a Jetmon adapter. id is the configured service instance ID,
// apiURL is the root URL of the Jetmon API, and token is the bearer token.
func New(id, apiURL, token string) *Adapter {
	return &Adapter{
		id:     id,
		apiURL: apiURL,
		token:  token,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (a *Adapter) ServiceID() string { return a.id }

func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		MinCheckFrequency:     time.Minute,
		SupportsKeyword:       false,
		SupportsAgentChecks:   true,
		DefaultMaxCallsPerRun: 0, // self-hosted, no API cost limit
	}
}

type monitorResponse struct {
	BlogID        int64  `json:"blog_id"`
	MonitorURL    string `json:"monitor_url"`
	MonitorActive bool   `json:"monitor_active"`
	SiteStatus    int    `json:"site_status"`
}

type eventResponse struct {
	ID        int64   `json:"id"`
	BlogID    int64   `json:"blog_id"`
	EventType string  `json:"event_type"`
	Source    string  `json:"source"`
	HTTPCode  *int    `json:"http_code"`
	OldStatus *int    `json:"old_status"`
	NewStatus *int    `json:"new_status"`
	Detail    *string `json:"detail"`
	CreatedAt string  `json:"created_at"`
}

// Provision looks up the pre-seeded monitor for target.URL.
// Returns an error if the API is unreachable or no monitor is registered
// for this URL — pre-seeding is required for the always-on Jetmon model.
func (a *Adapter) Provision(ctx context.Context, target adapter.Target, config adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	if a.apiURL == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon: url is not configured")
	}

	endpoint := a.apiURL + "/monitors?url=" + url.QueryEscape(target.URL)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+a.token)

	resp, err := a.client.Do(req)
	if err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon: /monitors: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon: no monitor pre-seeded for %s — add it to jetpack_monitor_sites", target.URL)
	}
	if resp.StatusCode != http.StatusOK {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon: /monitors: status %d", resp.StatusCode)
	}

	var m monitorResponse
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon: /monitors: decode: %w", err)
	}

	return adapter.MonitorHandle{
		ServiceID: a.id,
		MonitorID: strconv.FormatInt(m.BlogID, 10),
		Fields: map[string]string{
			"service_type": adapterType,
			"blog_id":      strconv.FormatInt(m.BlogID, 10),
			"monitor_url":  m.MonitorURL,
		},
	}, nil
}

// Retrieve fetches status_transition events from the Jetmon API for the run window.
// It maps Jetmon's site_status values to normalized MonitorReport events.
func (a *Adapter) Retrieve(ctx context.Context, handle adapter.MonitorHandle, window adapter.RunWindow) (adapter.RetrieveResult, error) {
	if a.apiURL == "" {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: "jetmon: url is not configured",
		}, nil
	}

	blogID := handle.Fields["blog_id"]
	if blogID == "" {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: "jetmon: handle missing blog_id",
		}, nil
	}

	since := window.FailureStarted.UTC().Format(time.RFC3339)
	until := window.GracePeriodEnd.UTC().Format(time.RFC3339)

	endpoint := fmt.Sprintf("%s/events?blog_id=%s&since=%s&until=%s",
		a.apiURL, url.QueryEscape(blogID),
		url.QueryEscape(since), url.QueryEscape(until),
	)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+a.token)

	resp, err := a.client.Do(req)
	if err != nil {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("jetmon: /events unreachable: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("jetmon: /events: status %d", resp.StatusCode),
		}, nil
	}

	var events []eventResponse
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("jetmon: /events: decode: %v", err),
		}, nil
	}

	now := time.Now()
	var reports []adapter.MonitorReport
	for _, e := range events {
		if e.EventType != "status_transition" {
			continue
		}
		if e.NewStatus == nil {
			continue
		}

		reportedAt, err := time.Parse(time.RFC3339, e.CreatedAt)
		if err != nil {
			reportedAt = now
		}

		var eventType adapter.ReportEventType
		var rawClass string
		if *e.NewStatus == statusConfirmedDown {
			eventType = adapter.EventAlertFired
			rawClass = "down"
		} else {
			// Any transition away from down (new_status = 1 = UP) is a recovery.
			eventType = adapter.EventAlertResolved
			rawClass = "up"
		}

		meta := map[string]any{
			"old_status": e.OldStatus,
			"new_status": *e.NewStatus,
			"source":     e.Source,
		}
		if e.HTTPCode != nil {
			meta["http_code"] = *e.HTTPCode
		}
		if e.Detail != nil {
			meta["detail"] = *e.Detail
		}

		reports = append(reports, adapter.MonitorReport{
			EventType:         eventType,
			RawClassification: rawClass,
			ReportedAt:        reportedAt,
			RetrievedAt:       now,
			Metadata:          meta,
		})
	}

	return adapter.RetrieveResult{
		Status:  adapter.RetrieveKnown,
		Reports: reports,
	}, nil
}

// Deprovision is a no-op: Jetmon monitors are always-on and managed outside
// the benchmark tool. Pre-seeded monitors are not deleted between runs.
func (a *Adapter) Deprovision(ctx context.Context, handle adapter.MonitorHandle) error {
	return nil
}
