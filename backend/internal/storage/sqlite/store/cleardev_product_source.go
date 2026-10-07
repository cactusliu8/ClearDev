package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

func sourcePreparationFromRow(row gen.CleardevProductSourcePreparation) (core.ProductSourcePreparation, error) {
	p := core.ProductSourcePreparation{ID: row.ID, ProductID: row.ProductID, PreviousID: row.PreviousID, InputJSON: row.InputJson, Branch: row.Branch, Status: row.Status, Failure: row.Failure}
	if err := json.Unmarshal([]byte(row.SelectionJson), &p.Selection); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(row.PreparedJson), &p.Prepared); err != nil {
		return p, err
	}
	var err error
	if p.CreatedAt, err = time.Parse(time.RFC3339Nano, row.CreatedAt); err != nil {
		return p, err
	}
	p.UpdatedAt, err = time.Parse(time.RFC3339Nano, row.UpdatedAt)
	return p, err
}

// GetClearDevProductSourcePreparation loads one immutable preparation request and its current result.
func (s *Store) GetClearDevProductSourcePreparation(ctx context.Context, id string) (*core.ProductSourcePreparation, error) {
	row, err := s.qr.GetClearDevProductSourcePreparation(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p, err := sourcePreparationFromRow(row)
	return &p, err
}

// GetLatestClearDevProductSourcePreparation loads the last registered preparation for a product.
func (s *Store) GetLatestClearDevProductSourcePreparation(ctx context.Context, id string) (*core.ProductSourcePreparation, error) {
	row, err := s.qr.GetLatestClearDevProductSourcePreparation(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p, err := sourcePreparationFromRow(row)
	return &p, err
}

// ListClearDevPendingProductSources returns products whose preparation needs reconciliation.
func (s *Store) ListClearDevPendingProductSources(ctx context.Context) ([]string, error) {
	return s.qr.ListClearDevPendingProductSources(ctx)
}

func checkSourcePreparationCurrent(ctx context.Context, q *gen.Queries, p core.ProductSourcePreparation) error {
	parent, err := q.GetClearDevRequirement(ctx, p.ProductID)
	if err != nil {
		return err
	}
	if parent.CancelledAt.Valid || parent.PausedFromState.Valid || parent.State == "PAUSED" {
		return productConflict("cancelled product cannot prepare source")
	}
	if err := checkProductRevision(ctx, q, p.ProductID); err != nil {
		return err
	}
	rounds, err := q.ListClearDevProductDiscussions(ctx, p.ProductID)
	if err != nil {
		return err
	}
	if len(rounds) == 0 || len(rounds) >= core.ProductMaxDiscussions {
		return productConflict("source preparation requires an available discussion")
	}
	previous := rounds[len(rounds)-1]
	if previous.ID != p.PreviousID || !previous.SettledAt.Valid {
		return productConflict("source preparation proposal changed")
	}
	proposal, err := q.GetClearDevProductDiscussion(ctx, p.Selection.SourceDiscussionID)
	if err != nil {
		return err
	}
	if proposal.ProductID != p.ProductID || !proposal.SettledAt.Valid || !requirementDigestMatches(proposal.ResultJson.String, proposal.ResultSha256.String) {
		return productConflict("source preparation has no exact proposal")
	}
	result, err := core.ParseProductDiscoveryResult([]byte(proposal.ResultJson.String))
	if err != nil {
		return err
	}
	for _, option := range result.Options {
		if sameSourceJSON(option, p.Selection.Option) && option.Origin == "DISCOVERED" {
			return nil
		}
	}
	return productConflict("source preparation choice does not match saved option")
}

// CheckClearDevProductSourcePreparation rechecks current discussion and cancellation without changing facts.
func (s *Store) CheckClearDevProductSourcePreparation(ctx context.Context, id string) error {
	return s.inTx(ctx, "check source preparation", func(q *gen.Queries) error {
		row, err := q.GetClearDevProductSourcePreparation(ctx, id)
		if err != nil {
			return err
		}
		p, err := sourcePreparationFromRow(row)
		if err != nil {
			return err
		}
		if p.Status != "PENDING" && p.Status != "READY" {
			return productConflict("source preparation is not active")
		}
		return checkSourcePreparationCurrent(ctx, q, p)
	})
}

// BeginClearDevProductSourcePreparation saves an exact source choice before any Git operation.
func (s *Store) BeginClearDevProductSourcePreparation(ctx context.Context, p core.ProductSourcePreparation) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "begin source preparation", func(q *gen.Queries) error {
		if old, err := q.GetClearDevProductSourcePreparation(ctx, p.ID); err == nil {
			if old.ProductID != p.ProductID || old.InputJson != p.InputJSON {
				return productConflict("source request belongs to different input")
			}
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := q.GetClearDevProductDiscussion(ctx, p.ID); err == nil {
			return productConflict("source request id already belongs to a discussion")
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var input struct {
			RequestID          string `json:"requestId"`
			ExpectedPreviousID string `json:"expectedPreviousId"`
			Message            string `json:"message"`
		}
		if json.Unmarshal([]byte(p.InputJSON), &input) != nil || input.RequestID != p.ID || input.ExpectedPreviousID != p.PreviousID || strings.TrimSpace(input.Message) == "" || utf8.RuneCountInString(input.Message) > 16000 || p.ID == "" || len(p.ID) > 160 || p.Selection.ChoiceDiscussionID != p.ID {
			return productConflict("invalid source preparation request")
		}
		if err := checkSourcePreparationCurrent(ctx, q, p); err != nil {
			return err
		}
		selection, err := json.Marshal(p.Selection)
		if err != nil {
			return err
		}
		return q.InsertClearDevProductSourcePreparation(ctx, gen.InsertClearDevProductSourcePreparationParams{ID: p.ID, ProductID: p.ProductID, PreviousID: p.PreviousID, InputJson: p.InputJSON, SelectionJson: string(selection), Branch: p.Branch, CreatedAt: p.CreatedAt.Format(time.RFC3339Nano), UpdatedAt: p.CreatedAt.Format(time.RFC3339Nano)})
	})
}

// SetClearDevProductSourcePreparation records a guarded result while retaining previous failures.
func (s *Store) SetClearDevProductSourcePreparation(ctx context.Context, id, expected, status, failure string, prepared *core.ProductSelection, now time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.inTx(ctx, "settle source preparation", func(q *gen.Queries) error {
		row, err := q.GetClearDevProductSourcePreparation(ctx, id)
		if err != nil {
			return err
		}
		if row.Status != expected {
			return productConflict("source preparation state changed")
		}
		legal := expected == "PENDING" && (status == "READY" || status == "FAILED") || expected == "READY" && (status == "APPLIED" || status == "FAILED") || expected == "FAILED" && status == "PENDING"
		if !legal {
			return productConflict("invalid source preparation transition")
		}
		p, err := sourcePreparationFromRow(row)
		if err != nil {
			return err
		}
		if status == "PENDING" {
			latest, err := q.GetLatestClearDevProductSourcePreparation(ctx, p.ProductID)
			if err != nil {
				return err
			}
			if latest.ID != p.ID {
				return productConflict("source preparation was superseded")
			}
		}
		if status == "PENDING" || status == "READY" {
			if err := checkSourcePreparationCurrent(ctx, q, p); err != nil {
				return err
			}
		}
		if status == "READY" {
			if prepared == nil {
				return productConflict("prepared source proof is missing")
			}
			fixed := *prepared
			fixed.BaseCommitSHA = p.Selection.BaseCommitSHA
			fixed.RepositoryURL = p.Selection.RepositoryURL
			if !sameSourceSelection(fixed, p.Selection) || (len(prepared.BaseCommitSHA) != 40 || strings.Trim(prepared.BaseCommitSHA, "0123456789abcdef") != "") {
				return productConflict("prepared source changed the selected project")
			}
		}
		if status == "APPLIED" {
			d, err := q.GetClearDevProductDiscussion(ctx, id)
			if err != nil {
				return err
			}
			_, selection, err := loadProductContext(ctx, q, id)
			if err != nil {
				return err
			}
			if d.ProductID != p.ProductID || selection == nil || p.Prepared == nil || !sameSourceSelection(*selection, *p.Prepared) {
				return productConflict("prepared discussion has not been applied")
			}
		}
		history := row.Failure
		if failure != "" {
			history += now.Format(time.RFC3339Nano) + " " + failure + "\n"
		}
		raw := row.PreparedJson
		if prepared != nil {
			data, err := json.Marshal(prepared)
			if err != nil {
				return err
			}
			raw = string(data)
		}
		changed, err := q.SetClearDevProductSourcePreparation(ctx, gen.SetClearDevProductSourcePreparationParams{ID: id, ExpectedStatus: expected, Status: status, Failure: history, PreparedJson: raw, UpdatedAt: now.Format(time.RFC3339Nano)})
		if err != nil {
			return err
		}
		if changed != 1 {
			return productConflict("source preparation state changed")
		}
		return nil
	})
}
func sameSourceSelection(a, b core.ProductSelection) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func sameSourceJSON(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func rejectActiveSourcePreparation(ctx context.Context, q *gen.Queries, productID string) error {
	p, err := q.GetLatestClearDevProductSourcePreparation(ctx, productID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if p.Status == "PENDING" || p.Status == "READY" {
		return productConflict("project source preparation is still active")
	}
	return nil
}
