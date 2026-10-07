package cleardevdemo

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type cdpClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
	next int64
}

type cdpTarget struct {
	Type                 string `json:"type"`
	URL                  string `json:"url"`
	Title                string `json:"title"`
	WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
}

func dialCDP(ctx context.Context, port int, timeout time.Duration) (*cdpClient, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		targets, err := listCDPTargets(ctx, port)
		if err != nil {
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		for _, target := range preferredCDPTargets(targets) {
			dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			conn, httpRes, dialErr := websocket.Dial(dialCtx, target.WebSocketDebuggerURL, nil)
			closeHTTPResponse(httpRes)
			cancel()
			if dialErr != nil {
				lastErr = dialErr
				continue
			}
			if conn == nil {
				lastErr = fmt.Errorf("cdp dial returned no connection")
				continue
			}
			client := &cdpClient{conn: conn}
			if _, evalErr := client.evaluate(ctx, "document.readyState"); evalErr == nil {
				_, _ = client.call(ctx, "Page.bringToFront", map[string]any{})
				return client, nil
			}
			_ = conn.Close(websocket.StatusNormalClosure, "")
		}
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no electron page target on port %d", port)
	}
	return nil, fmt.Errorf("electron CDP was not ready: %w", lastErr)
}

func listCDPTargets(ctx context.Context, port int) ([]cdpTarget, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/json/list", port), http.NoBody)
	if err != nil {
		return nil, err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cdp list status %d", res.StatusCode)
	}
	var targets []cdpTarget
	if err := json.NewDecoder(res.Body).Decode(&targets); err != nil {
		return nil, err
	}
	return targets, nil
}

func dialMatchingCDP(ctx context.Context, port int, appURL string, timeout time.Duration) (*cdpClient, error) {
	prefix := sampleURLPrefix(appURL)
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		targets, err := listCDPTargets(ctx, port)
		if err != nil {
			lastErr = err
			time.Sleep(300 * time.Millisecond)
			continue
		}
		for _, target := range targets {
			if target.WebSocketDebuggerURL == "" {
				continue
			}
			if target.Type != "page" && target.Type != "iframe" {
				continue
			}
			if !strings.HasPrefix(target.URL, prefix) {
				continue
			}
			dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			conn, httpRes, dialErr := websocket.Dial(dialCtx, target.WebSocketDebuggerURL, nil)
			closeHTTPResponse(httpRes)
			cancel()
			if dialErr != nil {
				lastErr = dialErr
				continue
			}
			if conn == nil {
				lastErr = fmt.Errorf("cdp dial returned no connection")
				continue
			}
			return &cdpClient{conn: conn}, nil
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no CDP target for %s", prefix)
		}
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no CDP target for %s", prefix)
	}
	return nil, fmt.Errorf("sample page CDP was not ready: %w", lastErr)
}

func preferredCDPTargets(targets []cdpTarget) []cdpTarget {
	var preferred, rest []cdpTarget
	for _, target := range targets {
		if target.WebSocketDebuggerURL == "" || target.Type != "page" {
			continue
		}
		if strings.HasPrefix(target.URL, "devtools://") || strings.HasPrefix(target.URL, "chrome-error://") {
			continue
		}
		if strings.Contains(target.URL, "index.html") || strings.HasPrefix(target.URL, "file:") || strings.HasPrefix(target.URL, "app:") {
			preferred = append(preferred, target)
			continue
		}
		if target.URL != "about:blank" {
			rest = append(rest, target)
		}
	}
	return append(preferred, rest...)
}

func closeHTTPResponse(res *http.Response) {
	if res == nil || res.Body == nil {
		return
	}
	_ = res.Body.Close()
}

func (c *cdpClient) close() {
	if c == nil || c.conn == nil {
		return
	}
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
}

func (c *cdpClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return c.callOn(ctx, "", method, params, 10*time.Second)
}

func (c *cdpClient) callOn(ctx context.Context, sessionID, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := c.next
	payload := map[string]any{"id": id, "method": method, "params": params}
	if sessionID != "" {
		payload["sessionId"] = sessionID
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := wsjson.Write(callCtx, c.conn, payload); err != nil {
		return nil, err
	}
	for {
		var msg struct {
			ID     int64           `json:"id"`
			Error  json.RawMessage `json:"error"`
			Result json.RawMessage `json:"result"`
		}
		if err := wsjson.Read(callCtx, c.conn, &msg); err != nil {
			return nil, err
		}
		if msg.ID != id {
			continue
		}
		if len(msg.Error) > 0 && string(msg.Error) != "null" {
			return nil, fmt.Errorf("cdp %s: %s", method, msg.Error)
		}
		return msg.Result, nil
	}
}

func (c *cdpClient) evaluate(ctx context.Context, expression string) (string, error) {
	raw, err := c.call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"returnByValue": true,
		"awaitPromise":  false,
	})
	if err != nil {
		return "", err
	}
	return parseEvaluateResult(raw, expression)
}

func parseEvaluateResult(raw json.RawMessage, expression string) (string, error) {
	var result struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
		ExceptionDetails json.RawMessage `json:"exceptionDetails"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	if len(result.ExceptionDetails) > 0 && string(result.ExceptionDetails) != "null" {
		return "", fmt.Errorf("evaluate %s: %s", expression, result.ExceptionDetails)
	}
	switch value := result.Result.Value.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return "", err
		}
		return string(encoded), nil
	}
}

func (c *cdpClient) waitText(ctx context.Context, expression, want string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last string
	var lastErr error
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		value, err := c.evaluate(ctx, expression)
		if err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}
		last = value
		if value == want {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	if lastErr != nil {
		return fmt.Errorf("cdp wait %s: last=%q err=%w", expression, last, lastErr)
	}
	return fmt.Errorf("cdp wait %s: last=%q want %q", expression, last, want)
}
