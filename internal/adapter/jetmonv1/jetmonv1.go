// Package jetmonv1 implements the uptime-bench adapter for Jetmon 1.
//
// Jetmon 1 has no public API; this adapter talks to jetmon-bridge
// (github.com/Automattic/jetmon-bridge), which fronts Jetmon 1's MySQL
// database with a small HTTP API.
//
// In read-only mode (default), Provision looks up a pre-seeded monitor by URL.
// In write mode, Provision calls POST /monitors to create or reactivate the monitor,
// and Deprovision calls DELETE /monitors to deactivate it when the run ends.
//
// Required services.toml fields:
//
//	url        — root URL of the Jetmon bridge API (no public endpoint; must be configured)
//	auth.token — bearer token for authentication
//
// Optional services.toml fields:
//
//	auth.write_mode — "true" to enable monitor create/delete (default "false")
package jetmonv1

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

// statusConfirmedDown is Jetmon's site_status value for a confirmed outage.
const statusConfirmedDown = 2

// classification maps Jetmon 1's raw site_status / report labels to
// uptime-bench's normalized vocabulary. Lives next to the adapter so
// service-specific knowledge stays out of the core (CLAUDE.md).
var classification = map[string]string{
	"down":       "http_failure",
	"seems_down": "http_failure",
	"degraded":   "http_failure",
	"up":         "recovered",
	"unknown":    "unknown",
}

// Adapter implements adapter.Adapter for Jetmon 1.
type Adapter struct {
	id        string
	apiURL    string
	token     string
	writeMode bool
	client    *http.Client
}

// New creates a Jetmon adapter. id is the configured service instance ID,
// apiURL is the root URL of the Jetmon bridge, token is the bearer token,
// and writeMode controls whether Provision/Deprovision make write calls.
func New(id, apiURL, token string, writeMode bool) *Adapter {
	return &Adapter{
		id:        id,
		apiURL:    apiURL,
		token:     token,
		writeMode: writeMode,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (a *Adapter) ServiceID() string { return a.id }

// Normalize implements adapter.Adapter.Normalize.
func (a *Adapter) Normalize(raw string) string {
	if v, ok := classification[raw]; ok {
		return v
	}
	return adapter.UnrecognizedClassification
}

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

// Provision looks up or creates a monitor for target.URL.
// In write mode it calls POST /monitors; in read-only mode it calls GET /monitors.
func (a *Adapter) Provision(ctx context.Context, target adapter.Target, config adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	if a.apiURL == "" {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v1: url is not configured")
	}
	if a.writeMode {
		return a.provisionWrite(ctx, target)
	}
	return a.provisionRead(ctx, target)
}

func (a *Adapter) provisionRead(ctx context.Context, target adapter.Target) (adapter.MonitorHandle, error) {
	endpoint := a.apiURL + "/monitors?url=" + url.QueryEscape(target.URL)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+a.token)

	resp, err := a.client.Do(req)
	if err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v1: /monitors: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v1: no monitor pre-seeded for %s — add it to jetpack_monitor_sites", target.URL)
	}
	if resp.StatusCode != http.StatusOK {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v1: /monitors: status %d", resp.StatusCode)
	}

	var m monitorResponse
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v1: /monitors: decode: %w", err)
	}
	return monitorHandle(a.id, m), nil
}

func (a *Adapter) provisionWrite(ctx context.Context, target adapter.Target) (adapter.MonitorHandle, error) {
	type createReq struct {
		URL string `json:"url"`
	}
	bodyBytes, _ := json.Marshal(createReq{URL: target.URL})

	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.apiURL+"/monitors", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token)

	resp, err := a.client.Do(req)
	if err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v1: POST /monitors: %w", err)
	}
	defer resp.Body.Close()

	// Bridge not in write mode — fall back to read-only lookup.
	if resp.StatusCode == http.StatusMethodNotAllowed {
		return a.provisionRead(ctx, target)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v1: POST /monitors: status %d", resp.StatusCode)
	}

	var m monitorResponse
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return adapter.MonitorHandle{}, fmt.Errorf("jetmon-v1: POST /monitors: decode: %w", err)
	}
	return monitorHandle(a.id, m), nil
}

// Retrieve fetches status_transition events from the Jetmon bridge for the run window.
func (a *Adapter) Retrieve(ctx context.Context, handle adapter.MonitorHandle, window adapter.RunWindow) (adapter.RetrieveResult, error) {
	if a.apiURL == "" {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: "jetmon-v1: url is not configured",
		}, nil
	}

	blogID := handle.Fields["blog_id"]
	if blogID == "" {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: "jetmon-v1: handle missing blog_id",
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
			Reason: fmt.Sprintf("jetmon-v1: /events unreachable: %v", err),
		}, nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("jetmon-v1: /events: status %d", resp.StatusCode),
		}, nil
	}

	var events []eventResponse
	if err := json.NewDecoder(resp.Body).Decode(&events); err != nil {
		return adapter.RetrieveResult{
			Status: adapter.RetrieveUnknown,
			Reason: fmt.Sprintf("jetmon-v1: /events: decode: %v", err),
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

// Deprovision deactivates the monitor when write mode is enabled.
// In read-only mode it is a no-op: monitors are managed outside the benchmark tool.
func (a *Adapter) Deprovision(ctx context.Context, handle adapter.MonitorHandle) error {
	if !a.writeMode {
		return nil
	}
	monitorURL := handle.Fields["monitor_url"]
	if monitorURL == "" {
		return nil
	}

	endpoint := a.apiURL + "/monitors?url=" + url.QueryEscape(monitorURL)
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	req.Header.Set("Authorization", "Bearer "+a.token)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("jetmon-v1: DELETE /monitors: %w", err)
	}
	defer resp.Body.Close()

	// 204 No Content and 404 Not Found are both acceptable — idempotent.
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("jetmon-v1: DELETE /monitors: status %d", resp.StatusCode)
}

func monitorHandle(serviceID string, m monitorResponse) adapter.MonitorHandle {
	return adapter.MonitorHandle{
		ServiceID: serviceID,
		MonitorID: strconv.FormatInt(m.BlogID, 10),
		Fields: map[string]string{
			"blog_id":     strconv.FormatInt(m.BlogID, 10),
			"monitor_url": m.MonitorURL,
		},
	}
}
