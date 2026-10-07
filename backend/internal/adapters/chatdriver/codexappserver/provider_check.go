package codexappserver

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/adapters/chatdriver/codexappserver/codexproto"
)

// ProviderCheckEvidence is an in-memory observation. Frames can contain account
// information; the trusted runner must redact them before persistence.
// Complete describes the protocol only, never process or network completion.
type ProviderCheckEvidence struct {
	Frames          []LocalCheckFrame `json:"frames"`
	Complete        bool              `json:"complete"`
	Outcome         string            `json:"outcome"`
	Error           string            `json:"error,omitempty"`
	Pages           int               `json:"pages"`
	CatalogComplete bool              `json:"catalogComplete"`
	TargetListed    bool              `json:"targetListed"`
	HighListed      bool              `json:"highListed"`
}

// CheckProvider reads a fixed, bounded sequence. It cannot start a thread, send
// a turn, authenticate, refresh credentials or answer a server request. Callers
// close both transport ends on cancellation. No product driver behavior changes.
func CheckProvider(ctx context.Context, input io.Reader, output io.Writer) (e ProviderCheckEvidence, returned error) {
	e.Frames = []LocalCheckFrame{}
	e.Outcome = "INCOMPLETE"
	defer func() {
		if returned != nil {
			e.Error = returned.Error()
		}
	}()
	reader := bufio.NewReaderSize(io.LimitReader(input, 8<<20), 64<<10)
	send := func(v any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err = output.Write(append(raw, '\n')); err != nil {
			return errors.New("transport write failed")
		}
		e.Frames = append(e.Frames, LocalCheckFrame{Direction: "sent", Raw: string(raw)})
		return nil
	}
	id := int64(0)
	request := func(method string, params any) (json.RawMessage, bool, error) {
		id++
		if err := send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return nil, false, err
		}
		for {
			if err := ctx.Err(); err != nil {
				return nil, false, err
			}
			raw, err := readFrameBounded(reader, 1<<20)
			if err != nil {
				return nil, false, errors.New("transport frame incomplete")
			}
			e.Frames = append(e.Frames, LocalCheckFrame{Direction: "received", Raw: string(raw)})
			var f frame
			if json.Unmarshal(raw, &f) != nil {
				return nil, false, errors.New("malformed frame")
			}
			if f.Method != "" {
				if f.ID != nil {
					return nil, false, errors.New("server request forbidden")
				}
				continue
			}
			var got int64
			if responseID(f, &got) != nil || got != id {
				return nil, false, errors.New("response ID mismatch")
			}
			if f.Error != nil {
				return nil, true, errors.New("provider rejected read-only request")
			}
			var object map[string]json.RawMessage
			if json.Unmarshal(f.Result, &object) != nil || object == nil {
				return nil, false, errors.New("response object missing")
			}
			return f.Result, false, nil
		}
	}
	unavailable := func() { e.Complete = true; e.Outcome = "UNAVAILABLE" }
	if _, failed, err := request("initialize", initializeParams()); failed {
		unavailable()
		return e, nil
	} else if err != nil {
		return e, err
	}
	if err := send(map[string]any{"method": "initialized"}); err != nil {
		return e, err
	}
	refresh := false
	raw, failed, err := request("account/read", codexproto.GetAccountParams{RefreshToken: &refresh})
	if failed {
		unavailable()
		return e, nil
	}
	if err != nil {
		return e, err
	}
	var account codexproto.GetAccountResponse
	if json.Unmarshal(raw, &account) != nil {
		return e, errors.New("account schema invalid")
	}
	if account.Account == nil || (account.Account.Type != codexproto.AccountTypeChatgpt && account.Account.Type != codexproto.AccountTypeApiKey) {
		unavailable()
		return e, nil
	}
	var cursor *string
	for page := 0; page < 3; page++ {
		raw, failed, err = request("model/list", codexproto.ModelListParams{Cursor: cursor})
		if failed {
			unavailable()
			return e, nil
		}
		if err != nil {
			return e, err
		}
		var models codexproto.ModelListResponse
		if json.Unmarshal(raw, &models) != nil || models.Data == nil {
			return e, errors.New("catalog schema invalid")
		}
		e.Pages++
		for _, m := range models.Data {
			if m.Model == "gpt-5.6-terra" || m.ID == "gpt-5.6-terra" {
				e.TargetListed = true
				for _, effort := range m.SupportedReasoningEfforts {
					if effort.ReasoningEffort == "high" {
						e.HighListed = true
					}
				}
			}
		}
		cursor = models.NextCursor
		if cursor == nil || *cursor == "" {
			e.CatalogComplete = true
			break
		}
	}
	raw, failed, err = request("account/rateLimits/read", map[string]any{})
	if failed {
		unavailable()
		return e, nil
	}
	if err != nil {
		return e, err
	}
	var limits rateLimitsEnvelope
	if json.Unmarshal(raw, &limits) != nil {
		return e, errors.New("quota schema invalid")
	}
	quota := rateLimitsFrom(limits, time.Now())
	e.Complete = true
	e.Outcome = "UNAVAILABLE"
	// Unknown quota is never an unlimited account. The existing parser supplies
	// the same rate-limit semantics as the product; no production state is written.
	if e.CatalogComplete && e.TargetListed && e.HighListed && quota.PrimaryUsedPercent >= 0 && quota.PrimaryUsedPercent < 100 && quota.SecondaryUsedPercent < 100 && !quota.RateLimited && !quota.QuotaExhausted {
		e.Outcome = "READ_SUCCESS"
	}
	return e, nil
}
