// Package control defines the harness-to-fleet control plane protocol and
// provides both the server (used by cmd/target and cmd/dns) and the client
// (used by cmd/harness to send commands to fleet members).
//
// All control plane communication is authenticated HTTP/JSON on a dedicated
// port separate from the data-plane ports (80/443 for target, 53 for dns).
// The shared bearer token is configured in fleet.toml and distributed to
// fleet members via their EnvironmentFile.
package control

import "time"

// FailureSpec describes a failure to activate on a fleet member.
// It mirrors the Failure type in the scenario package but is expressed
// as a self-contained command rather than a parsed scenario field.
type FailureSpec struct {
	// Type is the failure type discriminator (e.g. "http_status", "dns_nxdomain").
	Type string `json:"type"`

	// Host is the virtual host this failure applies to (target VMs only).
	// Empty means the failure applies at the network level (e.g. TCP failures).
	Host string `json:"host,omitempty"`

	// Path is the specific path this failure applies to (target VMs only).
	// Empty means all paths on the host.
	Path string `json:"path,omitempty"`

	// Duration is how long the failure stays active. The fleet member clears
	// it automatically when the duration elapses, providing a safety net even
	// if the harness fails to send a deactivate command.
	Duration time.Duration `json:"duration"`

	// Rate is the fraction of requests that experience this failure, in (0.0, 1.0].
	// 1.0 means every request is affected. Applied using seeded randomness so the
	// statistical distribution is correct and reproducible per run.
	Rate float64 `json:"rate,omitempty"`

	// Params carries failure-type-specific parameters (status code, delay, etc.).
	Params map[string]any `json:"params,omitempty"`

	// SourceCIDRs, if non-empty, restricts the failure to connections whose
	// source IP falls within at least one of these CIDR ranges. Used for
	// geographic failure scenarios. The runner populates this by expanding
	// region names from the scenario file using probe IP ranges from services.toml.
	// Geo-restricted failures are applied at the TCP layer; the HTTP handler
	// never sees connections from matching source IPs.
	SourceCIDRs []string `json:"source_cidrs,omitempty"`
}

// ActivateRequest is sent by the harness to start a failure on a fleet member.
type ActivateRequest struct {
	RunID   string      `json:"run_id"`
	Seed    int64       `json:"seed"`
	Failure FailureSpec `json:"failure"`
}

// DeactivateRequest is sent by the harness to stop a failure early.
type DeactivateRequest struct {
	RunID       string `json:"run_id"`
	FailureType string `json:"failure_type"`
	Host        string `json:"host,omitempty"`
	Path        string `json:"path,omitempty"`
}

// StatusResponse is returned by the fleet member's status endpoint.
type StatusResponse struct {
	MemberID       string        `json:"member_id"`
	ActiveFailures []FailureSpec `json:"active_failures"`
}
