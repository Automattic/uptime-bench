package targetserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// CertLibraryClient sends cert-library configuration to a target's
// control plane. The harness uses one client per target to forward
// the certmint URL from fleet.toml's [certmint] section.
type CertLibraryClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewCertLibraryClient creates a client pointed at one target's
// control base URL (e.g. http://target-01:9000).
func NewCertLibraryClient(baseURL, token string, httpClient *http.Client) *CertLibraryClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &CertLibraryClient{baseURL: baseURL, token: token, http: httpClient}
}

// Configure sends PUT /config/cert-library to the target. The target's
// controller starts (or restarts) its polling goroutine; identical
// requests are deduplicated server-side so calling this on every
// harness invocation doesn't churn the poll loop.
func (c *CertLibraryClient) Configure(ctx context.Context, req CertLibraryConfigRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("cert-library client: marshal: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+"/config/cert-library", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("cert-library client: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("cert-library client: configure: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("cert-library client: configure: unexpected %d", resp.StatusCode)
	}
	return nil
}
