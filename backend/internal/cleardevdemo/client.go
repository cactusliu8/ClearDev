package cleardevdemo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type apiClient struct {
	base   string
	client *http.Client
}

func newAPIClient(port int) *apiClient {
	return &apiClient{
		base:   fmt.Sprintf("http://127.0.0.1:%d/api/v1", port),
		client: &http.Client{Timeout: 45 * time.Second},
	}
}

func waitDaemonHTTP(ctx context.Context, port, pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/readyz", port), http.NoBody)
		if err != nil {
			return err
		}
		res, err := client.Do(req)
		if err == nil {
			if res.StatusCode == http.StatusOK {
				var probe struct {
					Status  string `json:"status"`
					Service string `json:"service"`
					PID     int    `json:"pid"`
				}
				decodeErr := json.NewDecoder(res.Body).Decode(&probe)
				_ = res.Body.Close()
				if decodeErr == nil && probe.Status == "ready" && probe.Service == "agent-orchestrator-daemon" && probe.PID == pid {
					return nil
				}
			} else {
				_ = res.Body.Close()
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("daemon pid %d on 127.0.0.1:%d did not pass /readyz", pid, port)
}

func (c *apiClient) get(ctx context.Context, path string, dest any) error {
	return c.expect(ctx, http.MethodGet, path, nil, dest, http.StatusOK)
}

func (c *apiClient) post(ctx context.Context, path string, body, dest any, want int) error {
	return c.expect(ctx, http.MethodPost, path, body, dest, want)
}

func (c *apiClient) put(ctx context.Context, path string, body, dest any) error {
	return c.expect(ctx, http.MethodPut, path, body, dest, http.StatusOK)
}

func (c *apiClient) expect(ctx context.Context, method, path string, body, dest any, want int) error {
	status, payload, err := c.call(ctx, method, path, body)
	if err != nil {
		return err
	}
	if status != want {
		return fmt.Errorf("%s %s: status %d, want %d: %s", method, path, status, want, truncate(payload, 512))
	}
	if dest != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, dest); err != nil {
			return fmt.Errorf("decode %s %s: %w: %s", method, path, err, truncate(payload, 512))
		}
	}
	return nil
}

func (c *apiClient) call(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return res.StatusCode, payload, err
	}
	return res.StatusCode, payload, nil
}

func truncate(raw []byte, n int) string {
	if len(raw) <= n {
		return string(raw)
	}
	return string(raw[:n]) + "..."
}
