package cleardevdemo

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func browserImportScript() string {
	sample, err := json.Marshal(strings.Join(FrozenSample, "\n"))
	if err != nil {
		return ""
	}
	return `(async () => {
  const textarea = document.querySelector("#emails");
  const form = document.querySelector("#import-form");
  if (!(textarea instanceof HTMLTextAreaElement) || !form) {
    throw new Error("import form missing");
  }
  textarea.value = ` + string(sample) + `;
  const orig = window.fetch.bind(window);
  const api = await new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error("browser import did not call /api/import")), 15000);
    window.fetch = async (input, init) => {
      const res = await orig(input, init);
      const url = typeof input === "string" ? input : String(input && input.url);
      if (url.includes("/api/import")) {
        try {
          const body = await res.clone().json();
          clearTimeout(timer);
          resolve(body);
        } catch (err) {
          clearTimeout(timer);
          reject(err);
        }
      }
      return res;
    };
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
  });
  const deadline = Date.now() + 5000;
  let page = {};
  while (Date.now() < deadline) {
    page = {
      accepted: Number(document.querySelector('[data-testid="accepted-count"]')?.textContent),
      rejected: Number(document.querySelector('[data-testid="rejected-count"]')?.textContent),
      duplicates: Number(document.querySelector('[data-testid="duplicate-count"]')?.textContent)
    };
    if (page.accepted === api.accepted && page.rejected === api.rejected && page.duplicates === api.duplicates) {
      break;
    }
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  if (page.accepted !== api.accepted || page.rejected !== api.rejected || page.duplicates !== api.duplicates) {
    throw new Error("page counts did not match the import API result");
  }
  return JSON.stringify({ api, page });
})()`
}

type sampleBrowserOpen struct {
	ProjectID string
	SessionID string
	Preview   func(context.Context, string) error
}

func submitFrozenSampleInBrowser(ctx context.Context, debugPort, appPort int, open sampleBrowserOpen) (map[string]int, map[string]int, error) {
	appURL := fmt.Sprintf("http://127.0.0.1:%d/", appPort)
	if open.Preview != nil {
		if err := open.Preview(ctx, appURL); err != nil {
			return nil, nil, fmt.Errorf("set sample preview: %w", err)
		}
	}
	if open.ProjectID != "" && open.SessionID != "" {
		if err := showSessionBrowserTab(ctx, debugPort, open.ProjectID, open.SessionID); err != nil {
			return nil, nil, fmt.Errorf("open sample browser tab: %w", err)
		}
	}
	return submitSampleOnMatchingTarget(ctx, debugPort, appURL, 25*time.Second)
}

func browserPreviewSessionID(sessions map[string]string) string {
	for _, key := range []string{"BUILDER-1", "BUILDER-2", "PLANNER", "SPECIALIST", "REVIEWER-1", "REVIEWER-2"} {
		if id := strings.TrimSpace(sessions[key]); id != "" {
			return id
		}
	}
	for key, id := range sessions {
		if key == "STEWARD" {
			continue
		}
		if strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return strings.TrimSpace(sessions["STEWARD"])
}

func showSessionBrowserTab(ctx context.Context, debugPort int, projectID, sessionID string) error {
	shell, err := dialCDP(ctx, debugPort, 45*time.Second)
	if err != nil {
		return err
	}
	defer shell.close()
	hash := fmt.Sprintf("#/projects/%s/sessions/%s", projectID, sessionID)
	if _, err := shell.evaluate(ctx, fmt.Sprintf("window.focus(); location.hash = %q; true", hash)); err != nil {
		return err
	}
	if err := shell.waitText(ctx, `document.querySelector('[data-testid="session-detail"]') ? "yes" : ""`, "yes", 20*time.Second); err != nil {
		return fmt.Errorf("session page: %w", err)
	}
	if _, err := shell.evaluate(ctx, `(function(){
  var toggle = document.querySelector('[data-testid="session-pinned-actions"] button[aria-pressed="false"]');
  if (toggle) toggle.click();
  var rail = document.querySelector('[data-testid="inspector-collapsed-rail"]');
  if (rail) rail.dispatchEvent(new PointerEvent("pointerdown", {bubbles: true}));
  return "ok";
})()`); err != nil {
		return err
	}
	deadline := time.Now().Add(12 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		clicked, err := shell.evaluate(ctx, `(function(){
  var inspector = document.querySelector('[data-testid="panel-inspector"]');
  if (!inspector || inspector.getAttribute("data-state") !== "expanded") {
    return "inspector:" + (inspector ? inspector.getAttribute("data-state") : "none");
  }
  var tabs = inspector.querySelectorAll('[role="tab"]');
  var labels = [];
  for (var i = 0; i < tabs.length; i++) {
    var label = ((tabs[i].getAttribute("aria-label") || tabs[i].getAttribute("title") || tabs[i].textContent || "") + "").trim();
    labels.push(label);
    var lower = label.toLowerCase();
    if (lower.indexOf("browser") !== -1 || lower.indexOf("浏览器") !== -1) {
      tabs[i].click();
      return "ok:" + label;
    }
  }
  return "missing-tabs:" + labels.join("|");
})()`)
		if err != nil {
			return err
		}
		last = clicked
		if strings.HasPrefix(clicked, "ok:") {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("browser inspector tab was not found (%s)", last)
}

func submitSampleOnMatchingTarget(ctx context.Context, debugPort int, appURL string, timeout time.Duration) (map[string]int, map[string]int, error) {
	page, err := dialMatchingCDP(ctx, debugPort, appURL, timeout)
	if err != nil {
		return nil, nil, err
	}
	defer page.close()
	_, _ = page.call(ctx, "Page.enable", map[string]any{})
	_, _ = page.call(ctx, "Runtime.enable", map[string]any{})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		ready, evalErr := page.evaluate(ctx, `document.readyState === "complete" && document.querySelector("#import-form") && document.querySelector("#emails") ? "yes" : ""`)
		if evalErr == nil && ready == "yes" {
			break
		}
		if time.Now().After(deadline.Add(-50 * time.Millisecond)) {
			if evalErr != nil {
				return nil, nil, fmt.Errorf("sample page form: %w", evalErr)
			}
			return nil, nil, fmt.Errorf("sample page did not render the import form")
		}
		time.Sleep(200 * time.Millisecond)
	}
	return evaluateBrowserImport(ctx, page)
}

func evaluateBrowserImport(ctx context.Context, client *cdpClient) (map[string]int, map[string]int, error) {
	raw, err := client.callOn(ctx, "", "Runtime.evaluate", map[string]any{
		"expression":    browserImportScript(),
		"returnByValue": true,
		"awaitPromise":  true,
	}, 30*time.Second)
	if err != nil {
		return nil, nil, fmt.Errorf("browser submit: %w", err)
	}
	encoded, err := parseEvaluateResult(raw, "browser import")
	if err != nil {
		return nil, nil, fmt.Errorf("browser submit: %w", err)
	}
	var got struct {
		API  map[string]int `json:"api"`
		Page map[string]int `json:"page"`
	}
	if err := json.Unmarshal([]byte(encoded), &got); err != nil {
		return nil, nil, fmt.Errorf("decode browser import result %q: %w", encoded, err)
	}
	if !sampleCountsMatch(got.API) {
		return nil, nil, fmt.Errorf("browser API = %#v, want %#v", got.API, FrozenSampleCounts)
	}
	if !sampleCountsMatch(got.Page) {
		return nil, nil, fmt.Errorf("browser page = %#v, want %#v", got.Page, FrozenSampleCounts)
	}
	return got.Page, got.API, nil
}

func sampleCountsMatch(got map[string]int) bool {
	return got["accepted"] == FrozenSampleCounts["accepted"] &&
		got["rejected"] == FrozenSampleCounts["rejected"] &&
		got["duplicates"] == FrozenSampleCounts["duplicates"]
}

func sampleURLPrefix(appURL string) string {
	return strings.TrimRight(appURL, "/")
}
