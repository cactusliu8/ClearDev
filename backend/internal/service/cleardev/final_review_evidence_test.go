package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	local "github.com/aoagents/agent-orchestrator/backend/internal/adapters/cleardevlocal"
	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

type finalEvidenceInspector struct {
	*requirementFinalReviewHarness
	writer *local.Runner
	fail   bool
}

func (h *finalEvidenceInspector) FinalReviewEvidencePath(ctx context.Context, id string) (string, error) {
	return h.writer.FinalReviewEvidencePath(ctx, id)
}
func (h *finalEvidenceInspector) WriteFinalReviewEvidence(ctx context.Context, id, path, packet, digest string) error {
	if h.fail {
		return errors.New("evidence directory unavailable")
	}
	return h.writer.WriteFinalReviewEvidence(ctx, id, path, packet, digest)
}

func TestFinalReviewEvidenceFilePreparedBeforeSend(t *testing.T) {
	for _, fail := range []bool{false, true} {
		name := "available"
		if fail {
			name = "unavailable"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("AO_DATA_DIR", t.TempDir())
			f, h := newRequirementFinalReviewFixture(t)
			f.service.inspector = &finalEvidenceInspector{requirementFinalReviewHarness: h, writer: local.New(), fail: fail}
			f.confirm(t)
			if fail {
				assertMailNotCompleted(t, f)
			} else {
				f.completed(t)
			}
			e := finalReviewExecution(t, f)
			if e.FinalReview == nil {
				t.Fatal("no final review")
			}
			var packet core.RequirementFinalReviewPacket
			if err := json.Unmarshal([]byte(e.FinalReview.ReviewPacketJSON), &packet); err != nil {
				t.Fatal(err)
			}
			if packet.EvidenceFile == "" {
				t.Fatal("missing evidence reference")
			}
			if fail {
				if h.finalSends != 0 {
					t.Fatal("sent review without evidence file")
				}
				return
			}
			if h.finalSends != 1 {
				t.Fatalf("sends=%d", h.finalSends)
			}
			data, err := os.ReadFile(packet.EvidenceFile)
			if err != nil || string(data) != e.FinalReview.ReviewPacketJSON {
				t.Fatalf("incomplete frozen evidence: %v", err)
			}
			prompt := core.RequirementFinalReviewPrompt(*e.FinalReview)
			if strings.Contains(prompt, string(data)) {
				t.Fatal("history copied back into handoff")
			}
			before, err := os.Stat(packet.EvidenceFile)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.service.prepareFinalReviewEvidence(context.Background(), *e.FinalReview); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(packet.EvidenceFile)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("repeated send recreated history")
			}
		})
	}
}
