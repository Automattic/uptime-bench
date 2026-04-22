// Package jetmon implements the uptime-bench adapter for Jetmon.
//
// Jetmon is a WordPress uptime monitor. This adapter requires Jetmon's
// public REST API (see jetmon/ROADMAP.md). Until that API ships, Provision
// and Retrieve return errors directing the caller to the roadmap item.
package jetmon

import (
	"context"
	"fmt"
	"time"

	"github.com/Automattic/uptime-bench/internal/adapter"
)

const serviceID = "jetmon"

// Adapter implements adapter.Adapter for Jetmon.
type Adapter struct {
	// baseURL is the root of the Jetmon public API, e.g. "https://jetmon.example.com".
	baseURL string
	// apiKey is the bearer token for authenticating API requests.
	apiKey string
}

// New creates a Jetmon adapter.
func New(baseURL, apiKey string) *Adapter {
	return &Adapter{baseURL: baseURL, apiKey: apiKey}
}

func (a *Adapter) ServiceID() string { return serviceID }

func (a *Adapter) Capabilities() adapter.Capabilities {
	return adapter.Capabilities{
		MinCheckFrequency:     time.Minute,
		SupportsKeyword:       false,
		SupportsAgentChecks:   true,
		DefaultMaxCallsPerRun: 0, // self-hosted, no API cost
	}
}

func (a *Adapter) Provision(ctx context.Context, target adapter.Target, config adapter.ProvisionConfig) (adapter.MonitorHandle, error) {
	// TODO: POST /api/v1/sites/{blog_id}/checks via Jetmon public API.
	// Blocked on: jetmon/ROADMAP.md — Public REST API (Manage endpoint).
	return adapter.MonitorHandle{}, fmt.Errorf("jetmon: Provision not yet implemented — waiting on Jetmon public API")
}

func (a *Adapter) Retrieve(ctx context.Context, handle adapter.MonitorHandle, window adapter.RunWindow) (adapter.RetrieveResult, error) {
	// TODO: GET /api/v1/sites/{blog_id}/events via Jetmon public API.
	// Blocked on: jetmon/ROADMAP.md — Public REST API (Query endpoint).
	return adapter.RetrieveResult{
		Status: adapter.RetrieveUnknown,
		Reason: "jetmon: Retrieve not yet implemented — waiting on Jetmon public API",
	}, nil
}

func (a *Adapter) Deprovision(ctx context.Context, handle adapter.MonitorHandle) error {
	// TODO: DELETE /api/v1/sites/{blog_id}/checks/{check_id} via Jetmon public API.
	// Blocked on: jetmon/ROADMAP.md — Public REST API (Manage endpoint).
	return fmt.Errorf("jetmon: Deprovision not yet implemented — waiting on Jetmon public API")
}
