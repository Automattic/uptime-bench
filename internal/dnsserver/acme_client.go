package dnsserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// ACMEClient sends DNS-01 challenge mutations to one DNS member's
// control API. Certmint's certbot manual hooks do not need this — they
// shell out via curl per the example scripts — but Go-side integration
// tests and harness fanout code use it for compactness.
type ACMEClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewACMEClient creates an ACMEClient pointed at one DNS member's
// control base URL (e.g. "http://dns-01.bench.local:9100").
func NewACMEClient(baseURL, token string, httpClient *http.Client) *ACMEClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &ACMEClient{baseURL: baseURL, token: token, http: httpClient}
}

// Put installs a TXT challenge value on this DNS member.
func (c *ACMEClient) Put(ctx context.Context, req ACMETXTPutRequest) error {
	return c.send(ctx, http.MethodPut, "/acme/txt", req)
}

// Delete removes one TXT challenge value from this DNS member,
// preserving any sibling values at the same name.
func (c *ACMEClient) Delete(ctx context.Context, req ACMETXTDeleteRequest) error {
	return c.send(ctx, http.MethodDelete, "/acme/txt", req)
}

func (c *ACMEClient) send(ctx context.Context, method, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("acme client: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("acme client: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("acme client: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("acme client: %s %s: unexpected %d", method, path, resp.StatusCode)
	}
	return nil
}
