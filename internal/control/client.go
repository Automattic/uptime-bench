package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Client sends control commands to a fleet member's control API.
type Client struct {
	baseURL string // e.g. "http://192.0.2.10:9000"
	token   string
	http    *http.Client
}

// NewClient creates a control Client pointed at the given base URL.
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{baseURL: baseURL, token: token, http: httpClient}
}

// Activate sends an activate command to the fleet member.
func (c *Client) Activate(ctx context.Context, req ActivateRequest) error {
	return c.post(ctx, "/activate", req)
}

// Deactivate sends a deactivate command to the fleet member.
func (c *Client) Deactivate(ctx context.Context, req DeactivateRequest) error {
	return c.post(ctx, "/deactivate", req)
}

// Status returns the fleet member's current active failures.
func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/status", nil)
	if err != nil {
		return nil, fmt.Errorf("control: client: %w", err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("control: client: status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("control: client: status: unexpected %d", resp.StatusCode)
	}
	var out StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("control: client: status: decode: %w", err)
	}
	return &out, nil
}

func (c *Client) post(ctx context.Context, path string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("control: client: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("control: client: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("control: client: %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("control: client: %s: unexpected %d", path, resp.StatusCode)
	}
	return nil
}
