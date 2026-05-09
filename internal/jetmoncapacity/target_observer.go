package jetmoncapacity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Automattic/uptime-bench/internal/targetserver"
)

// TargetObserverClient talks to the target's black-box capacity observer.
type TargetObserverClient interface {
	Reset(ctx context.Context, baseURL, token string, req targetserver.CapacityObserveResetRequest, timeout time.Duration) (targetserver.CapacityObserveSummary, error)
	Summary(ctx context.Context, baseURL, token string, timeout time.Duration) (targetserver.CapacityObserveSummary, error)
}

// DefaultTargetObserverClient is the live HTTP implementation.
type DefaultTargetObserverClient struct{}

func (DefaultTargetObserverClient) Reset(ctx context.Context, baseURL, token string, req targetserver.CapacityObserveResetRequest, timeout time.Duration) (targetserver.CapacityObserveSummary, error) {
	var summary targetserver.CapacityObserveSummary
	if err := postTargetObserverJSON(ctx, baseURL, token, "/capacity/observe/reset", req, timeout, &summary); err != nil {
		return targetserver.CapacityObserveSummary{}, err
	}
	return summary, nil
}

func (DefaultTargetObserverClient) Summary(ctx context.Context, baseURL, token string, timeout time.Duration) (targetserver.CapacityObserveSummary, error) {
	var summary targetserver.CapacityObserveSummary
	if err := getTargetObserverJSON(ctx, baseURL, token, "/capacity/observe/summary", timeout, &summary); err != nil {
		return targetserver.CapacityObserveSummary{}, err
	}
	return summary, nil
}

func postTargetObserverJSON(ctx context.Context, baseURL, token, path string, body any, timeout time.Duration, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, observerURL(baseURL, path), bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return doTargetObserverRequest(req, token, timeout, out)
}

func getTargetObserverJSON(ctx context.Context, baseURL, token, path string, timeout time.Duration, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, observerURL(baseURL, path), nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	return doTargetObserverRequest(req, token, timeout, out)
}

func doTargetObserverRequest(req *http.Request, token string, timeout time.Duration, out any) error {
	if strings.TrimSpace(token) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s returned HTTP %d: %s", req.URL.String(), resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func observerURL(baseURL, path string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + path
}

func resolveTargetObserverToken(cfg TargetObserverConfig) (string, error) {
	if cfg.TokenFile != "" {
		data, err := os.ReadFile(cfg.TokenFile)
		if err != nil {
			return "", fmt.Errorf("read target_observer.token_file %s: %w", cfg.TokenFile, err)
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return "", fmt.Errorf("target_observer.token_file %s is empty", cfg.TokenFile)
		}
		return token, nil
	}
	envName := cfg.TokenEnv
	if envName == "" {
		envName = "CONTROL_TOKEN"
	}
	token := strings.TrimSpace(os.Getenv(envName))
	if token == "" {
		return "", fmt.Errorf("target observer token is required: configure target_observer.token_file or set %s", envName)
	}
	return token, nil
}
