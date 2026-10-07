package cleardev

import (
	"context"
	"encoding/json"
	"errors"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
)

type finalReviewInputRejectionReader interface {
	FinalReviewInputRejected(context.Context, string) (bool, error)
}

func (s *Service) finalReviewInputRejected(ctx context.Context, review *core.RequirementFinalReview) (bool, error) {
	if review == nil || review.Status != "FAILED" || (review.ReasonCode != "REQUIREMENT_FINAL_REVIEWER_UNAVAILABLE" && review.ReasonCode != "REQUIREMENT_FINAL_REVIEW_RESULT_INVALID") {
		return false, nil
	}
	reader, ok := s.complexExecution.(finalReviewInputRejectionReader)
	if !ok {
		return false, nil
	}
	known, err := reader.FinalReviewInputRejected(ctx, review.ID)
	if err != nil || !known {
		return false, err
	}
	record, found, err := s.ao.GetSession(ctx, domain.SessionID(review.AOSessionID))
	if err != nil || !found {
		return false, err
	}
	if !s.controlledSessionMatches(ctx, record) || record.IsTerminated || record.Activity.State != domain.ActivityIdle || record.Metadata.WorkspacePath != review.WorkspacePath || !s.finalReviewWorkspaceMatches(ctx, review.WorkspacePath, review.CandidateCommitSHA) {
		return false, nil
	}
	return true, nil
}

func (s *Service) validateFinalReviewInputRecovery(ctx context.Context, review core.RequirementFinalReview) error {
	var packet core.RequirementFinalReviewPacket
	if err := json.Unmarshal([]byte(review.ReviewPacketJSON), &packet); err != nil {
		return err
	}
	if packet.Recovery == nil || packet.Recovery.StepID == "" || packet.Recovery.OriginalStatus != "FAILED" || (packet.Recovery.OriginalReason != "REQUIREMENT_FINAL_REVIEWER_UNAVAILABLE" && packet.Recovery.OriginalReason != "REQUIREMENT_FINAL_REVIEW_RESULT_INVALID") {
		return nil
	}
	reader, ok := s.complexExecution.(finalReviewInputRejectionReader)
	if !ok {
		return errors.New("final review recovery evidence unavailable")
	}
	known, err := reader.FinalReviewInputRejected(ctx, packet.Recovery.TargetID)
	if err != nil {
		return err
	}
	if known {
		return nil
	}
	return errors.New("final review input rejection evidence changed")
}
