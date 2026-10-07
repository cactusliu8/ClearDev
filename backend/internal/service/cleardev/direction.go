package cleardev

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// ProposeDirectionIntent records an idempotent direction message and asks the
// original Project Steward to form a stop-and-revise request.
func (s *Service) ProposeDirectionIntent(ctx context.Context, requirementID string, input ProposeDirectionIntentInput) (RequirementView, error) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return RequirementView{}, err
	}

	if s.facts == nil || s.complex == nil || s.direction == nil || s.chat == nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_UNAVAILABLE", "ClearDev direction change is not fully configured")
	}
	requirementID = strings.TrimSpace(requirementID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.DevelopmentRequirementID = strings.TrimSpace(input.DevelopmentRequirementID)
	if requirementID == "" {
		return RequirementView{}, apierr.Invalid("CLEARDEV_REQUIREMENT_ID_REQUIRED", "ClearDev requirement id is required", nil)
	}
	if input.DevelopmentRequirementID == "" {
		return RequirementView{}, apierr.Invalid("CLEARDEV_REQUIREMENT_ID_REQUIRED", "developmentRequirementId is required", nil)
	}
	if input.DevelopmentRequirementID != requirementID {
		return RequirementView{}, apierr.Invalid("CLEARDEV_REQUIREMENT_ID_MISMATCH", "developmentRequirementId must match the path identifier", nil)
	}
	if input.RequestID == "" {
		return RequirementView{}, apierr.Invalid("DIRECTION_REQUEST_ID_REQUIRED", "requestId is required", nil)
	}
	message, err := core.ValidateDirectionMessage(input.Message)
	if err != nil {
		return RequirementView{}, apierr.Invalid("DIRECTION_MESSAGE_INVALID", err.Error(), nil)
	}
	snapshot, ok, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_REQUIREMENT_READ_FAILED", "Could not read the ClearDev requirement")
	}
	if !ok {
		return RequirementView{}, apierr.NotFound("CLEARDEV_REQUIREMENT_NOT_FOUND", "ClearDev requirement was not found")
	}
	if _, linked, stageErr := s.productStageSource(ctx, requirementID); stageErr != nil {
		return RequirementView{}, stageErr
	} else if linked {
		return RequirementView{}, apierr.Conflict("PRODUCT_STAGE_REVISION_UNSUPPORTED", "This stage is bound to a frozen product proposal; runtime stage revision is not supported in this version", nil)
	}
	planning, exists, err := s.complex.GetClearDevComplexPlanning(ctx, requirementID)
	if err != nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_COMPLEX_PLANNING_READ_FAILED", "Could not read the ClearDev complex planning facts")
	}
	if !exists {
		return RequirementView{}, apierr.Conflict(string(core.ReasonPreconditionNotMet), "Direction change requires a complex requirement", nil)
	}
	confirmed, ok := currentConfirmedVersion(snapshot)
	if !ok {
		return RequirementView{}, apierr.Conflict(string(core.ReasonPreconditionNotMet), "Direction change requires a current confirmed requirement version", nil)
	}
	steward, ok := core.ComplexRoleBindingByRole(planning, core.StandardRoleSteward)
	if !ok || steward.Status != core.RoleBindingStatusBound {
		return RequirementView{}, apierr.Conflict(string(core.ReasonPreconditionNotMet), "Direction change requires the original bound Steward", nil)
	}
	now := s.now().UTC()
	stepID := s.newID()
	directionRequestID := s.newID()
	prompt := directionChangePrompt(snapshot.Requirement, confirmed, message, directionRequestID)
	intent := core.DirectionIntent{
		RequestID: input.RequestID, DevelopmentRequirementID: requirementID, RequirementVersionID: confirmed.ID,
		RequirementSHA256: confirmed.SHA256, Message: message, MessageSHA256: core.DirectionMessageSHA256(message),
		StewardRoleBindingID: steward.ID, DirectionRequestID: directionRequestID, AgentStepID: stepID, CreatedAt: now,
	}
	step := core.AgentStep{
		ID: stepID, RoleBindingID: steward.ID, Kind: core.DirectionAgentStepChange, RequestID: directionRequestID,
		ClientMessageID: "cleardev-direction-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)),
		SendStatus: core.AgentStepSendStatusPending, RequestedAt: now,
	}
	if _, _, err := s.direction.CreateClearDevDirectionIntent(ctx, core.CreateDirectionIntentCommand{Intent: intent, Step: step}); err != nil {
		return RequirementView{}, mapStoreError(err, "CREATE_DIRECTION_INTENT_FAILED")
	}
	s.scheduleDirectionChange(requirementID)
	return s.GetRequirement(ctx, requirementID)
}

// ResumeDirectionChanges restarts unfinished direction-change work before HTTP listen.
func (s *Service) ResumeDirectionChanges(ctx context.Context) error {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return err
	}

	if s.direction == nil {
		return nil
	}
	ids, err := s.direction.ListClearDevRunnableDirectionRequirements(ctx)
	if err != nil {
		return fmt.Errorf("list runnable ClearDev direction changes: %w", err)
	}
	for _, id := range ids {
		s.scheduleDirectionChange(id)
	}
	return nil
}

//nolint:dupl // same running/wake latch as complex planning
func (s *Service) scheduleDirectionChange(requirementID string) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		s.logger.Error("ClearDev controlled scheduling rejected", "err", err)
		return
	}
	if s.direction == nil || strings.TrimSpace(requirementID) == "" {
		return
	}
	s.directionMu.Lock()
	if s.directionRunning[requirementID] {
		s.directionWake[requirementID] = true
		s.directionMu.Unlock()
		return
	}
	s.directionRunning[requirementID] = true
	s.directionMu.Unlock()
	s.runBackground(func() {
		for {
			if err := s.runDirectionChange(s.backgroundContext, requirementID); err != nil {
				s.logger.Error("ClearDev direction change stopped with an internal error", "requirementID", requirementID, "error", err)
			}
			s.directionMu.Lock()
			if s.directionWake[requirementID] {
				delete(s.directionWake, requirementID)
				s.directionMu.Unlock()
				continue
			}
			delete(s.directionRunning, requirementID)
			s.directionMu.Unlock()
			return
		}
	})
}

func (s *Service) runDirectionChange(ctx context.Context, requirementID string) error {
	blocked, err := s.recoverRequirementAgentAttempts(ctx, requirementID)
	if err != nil || blocked {
		return err
	}
	for iteration := 0; iteration < 128; iteration++ {
		progressed, done, err := s.advanceDirectionChange(ctx, requirementID)
		if err != nil || done {
			return err
		}
		if !progressed {
			return nil
		}
	}
	return errors.New("ClearDev direction change exceeded its bounded advance count")
}

func (s *Service) advanceDirectionChange(ctx context.Context, requirementID string) (bool, bool, error) {
	snapshot, ok, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil {
		return false, false, err
	}
	if !ok || snapshot.Requirement.CancelledAt != nil {
		return false, true, nil
	}
	change, exists, err := s.direction.GetClearDevDirectionChange(ctx, requirementID)
	if err != nil {
		return false, false, err
	}
	if !exists {
		return false, true, nil
	}
	planning, _, err := s.complex.GetClearDevComplexPlanning(ctx, requirementID)
	if err != nil {
		return false, false, err
	}
	phase := core.DeriveDirectionChangePhase(change, snapshot.RequirementVersions, planning.Plans, planning.Reviews)
	switch phase {
	case core.DirectionChangeAwaitingDecision, core.DirectionChangeAwaitingClarification, core.DirectionChangeAwaitingConfirmation:
		return false, false, nil
	case core.DirectionChangeApproved, core.DirectionChangeRejected, core.DirectionChangeNeedsHuman, core.DirectionChangeNone:
		return false, true, nil
	case core.DirectionChangeAwaitingSteward:
		return s.advanceDirectionStewardRequest(ctx, snapshot, planning, change)
	case core.DirectionChangeProcessing:
		return s.advanceDirectionProcessing(ctx, snapshot, change)
	case core.DirectionChangeCompiling:
		return s.advanceDirectionCompilation(ctx, snapshot, planning, change)
	case core.DirectionChangePlanning, core.DirectionChangeAwaitingPlanReview:
		s.scheduleComplexFlow(requirementID)
		return false, false, nil
	default:
		return false, false, nil
	}
}

func (s *Service) advanceDirectionStewardRequest(ctx context.Context, snapshot core.RequirementSnapshot, planning core.ComplexPlanningSnapshot, change core.DirectionChangeSnapshot) (bool, bool, error) {
	if change.Intent == nil {
		return false, true, nil
	}
	steward, ok := core.ComplexRoleBindingByRole(planning, core.StandardRoleSteward)
	if !ok {
		return false, false, errors.New("direction change has no Steward binding")
	}
	confirmed, ok := currentConfirmedVersion(snapshot)
	if !ok {
		return false, true, nil
	}
	prompt := directionChangePrompt(snapshot.Requirement, confirmed, change.Intent.Message, change.Intent.DirectionRequestID)
	var parsed core.DirectionChangeRequestResult
	message, changed, err := s.runDirectionAgentStep(ctx, planning, change, steward, change.Intent.DirectionRequestID, core.DirectionAgentStepChange, prompt,
		standardStepReasons{Invalid: core.ReasonCode("STEWARD_RESULT_INVALID"), Timeout: core.ReasonCode("STEWARD_TIMEOUT"), Unavailable: core.ReasonCode("STEWARD_UNAVAILABLE"), ProjectID: snapshot.Requirement.AOProjectID},
		func(raw []byte) error {
			var parseErr error
			parsed, parseErr = core.ParseDirectionChangeRequest(raw, change.Intent.DirectionRequestID, snapshot.Requirement.ID, confirmed.ID, confirmed.SHA256, change.Intent.MessageSHA256)
			return parseErr
		})
	if err != nil {
		return changed, false, err
	}
	if changed && message.Text == "" {
		return true, false, nil
	}
	digest, err := core.HashDirectionChangeRequest(parsed)
	if err != nil {
		return false, false, err
	}
	now := s.now().UTC()
	step, _ := core.DirectionAgentStepByRequest(change, core.DirectionAgentStepChange, change.Intent.DirectionRequestID)
	if err := s.direction.AcceptClearDevDirectionChange(ctx, core.AcceptDirectionChangeCommand{
		Intent: *change.Intent,
		Request: core.DirectionRequest{
			ID: change.Intent.DirectionRequestID, IntentRequestID: change.Intent.RequestID,
			DevelopmentRequirementID: snapshot.Requirement.ID, RequirementVersionID: confirmed.ID,
			Summary: parsed.Summary, AffectedRequirementIDs: parsed.AffectedRequirementIDs, ResultSHA256: digest,
			AgentStepID: change.Intent.AgentStepID, CreatedAt: now,
		},
		Gate:      core.DirectionStopGate{ID: s.newID(), CreatedAt: now},
		AgentStep: core.AgentStep{ID: step.ID, SendStatus: core.AgentStepSendStatusSettled, TurnID: message.TurnID, FinalMessageID: message.MessageID, FinalMessageText: message.Text, MessageSHA256: coreDigest([]byte(message.Text)), CompletedAt: &now},
		At:        now,
	}); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (s *Service) advanceDirectionProcessing(ctx context.Context, snapshot core.RequirementSnapshot, change core.DirectionChangeSnapshot) (bool, bool, error) {
	if change.Request == nil {
		return false, false, nil
	}
	if core.AllDirectionProcessingsFinal(change) {
		if change.Revision != nil {
			return true, false, nil
		}
		confirmed, ok := currentConfirmedVersion(snapshot)
		if !ok {
			return false, true, nil
		}
		if err := s.direction.CreateClearDevDirectionRevision(ctx, core.CreateDirectionRevisionCommand{
			Revision: core.DirectionRevision{
				DirectionRequestID: change.Request.ID, PreviousRequirementVersionID: confirmed.ID,
				TargetRequirementVersionID: s.newID(), CreatedAt: s.now().UTC(),
			},
		}); err != nil {
			return false, false, err
		}
		return true, false, nil
	}
	for _, processing := range change.Processings {
		if processing.Occupancy == core.DirectionOccupancyFinal {
			continue
		}
		progressed, err := s.finalizeOneDirectionTask(ctx, snapshot, change, processing)
		if err != nil {
			return false, false, err
		}
		if s.betweenTasks != nil {
			s.betweenTasks()
		}
		return progressed, false, nil
	}
	return false, false, nil
}

func (s *Service) finalizeOneDirectionTask(ctx context.Context, _ core.RequirementSnapshot, _ core.DirectionChangeSnapshot, processing core.DirectionTaskProcessing) (bool, error) {
	now := s.now().UTC()
	if processing.Occupancy == core.DirectionOccupancyPending {
		if _, err := s.direction.OccupyClearDevDirectionTask(ctx, core.OccupyDirectionTaskCommand{ProcessingID: processing.ID, At: now}); err != nil {
			return false, err
		}
		processing.Occupancy = core.DirectionOccupancyOccupied
		occupied := now
		processing.OccupiedAt = &occupied
		if s.afterOccupy != nil {
			s.afterOccupy()
		}
	} else if processing.Occupancy == core.DirectionOccupancyOccupied && processing.InterruptResult == "" {
		if err := s.direction.MarkClearDevDirectionInterruptUnknown(ctx, processing.ID, string(core.ReasonDirectionUnknownAfterRestart)); err != nil {
			return false, err
		}
		processing.InterruptResult = string(core.ReasonDirectionUnknownAfterRestart)
	}
	task, _, ok, err := s.facts.GetClearDevTaskContext(ctx, processing.TaskID)
	if err != nil {
		return false, err
	}
	command := core.FinalizeDirectionTaskCommand{Processing: processing, At: now}
	// Frozen v3 forbids CANCEL from BLOCKED or NEEDS_HUMAN. Those tasks are
	// already stopped, so direction change checkpoints them in place.
	if !ok || task.Status == core.DevelopmentTaskStatusDone || task.Status == core.DevelopmentTaskStatusCancelled ||
		task.Status == core.DevelopmentTaskStatusBlocked || task.Status == core.DevelopmentTaskStatusNeedsHuman {
		command.Processing.CancelResult = core.DirectionCancelUnchangedTerminal
		command.Processing.ReasonCode = core.ReasonDirectionTerminalUnchanged
		command.Checkpoint = &core.DirectionCheckpoint{
			ID: s.newID(), ProcessingID: processing.ID, TaskID: processing.TaskID,
			Kind: core.DirectionCheckpointTerminal, ChangeSummaryJSON: "[]", CreatedAt: now,
		}
		return true, s.direction.FinalizeClearDevDirectionTask(ctx, command)
	}
	if task.Status == core.DevelopmentTaskStatusPlanned {
		command.CancelTask = true
		command.Processing.CancelResult = core.DirectionCancelCancelled
		command.Processing.ReasonCode = core.ReasonDirectionPlannedUnstarted
		command.Checkpoint = &core.DirectionCheckpoint{
			ID: s.newID(), ProcessingID: processing.ID, TaskID: processing.TaskID,
			Kind: core.DirectionCheckpointUnstarted, ChangeSummaryJSON: "[]", CreatedAt: now,
		}
		return true, s.direction.FinalizeClearDevDirectionTask(ctx, command)
	}
	dispatchID, baseSHA, sessionID, err := s.direction.GetClearDevTaskDispatchFacts(ctx, processing.TaskID)
	if err != nil {
		return false, err
	}
	interruptResult := processing.InterruptResult
	if sessionID != "" && interruptResult == "" && s.chat != nil {
		if err := s.chat.Interrupt(ctx, domain.SessionID(sessionID)); err != nil {
			interruptResult = string(core.ReasonDirectionInterruptFailed)
		} else {
			interruptResult = "INTERRUPTED"
		}
	} else if sessionID == "" {
		command.Processing.ReasonCode = core.ReasonDirectionNoSession
	}
	command.Processing.InterruptResult = interruptResult
	checkpoint := core.DirectionCheckpoint{
		ID: s.newID(), ProcessingID: processing.ID, TaskID: processing.TaskID,
		SessionID: sessionID, BaselineSHA: baseSHA, ChangeSummaryJSON: "[]", CreatedAt: now,
		Kind: core.DirectionCheckpointNoWorktree,
	}
	if sessionID == "" {
		checkpoint.Kind = core.DirectionCheckpointNoSession
		command.Processing.ReasonCode = core.ReasonDirectionNoSession
	} else if strings.TrimSpace(baseSHA) == "" || strings.TrimSpace(dispatchID) == "" {
		checkpoint.Kind = core.DirectionCheckpointNoWorktree
		command.Processing.ReasonCode = core.ReasonDirectionNoBaseline
	} else if s.ao != nil && s.workspace != nil {
		session, found, sessionErr := s.ao.GetSession(ctx, domain.SessionID(sessionID))
		if sessionErr != nil {
			return false, sessionErr
		}
		if !found || strings.TrimSpace(session.Metadata.WorkspacePath) == "" {
			checkpoint.Kind = core.DirectionCheckpointNoWorktree
			command.Processing.ReasonCode = core.ReasonDirectionNoWorktree
		} else {
			observation, observeErr := s.workspace.ObserveWorkspace(ctx, ports.WorkspaceInfo{
				Path: session.Metadata.WorkspacePath, Branch: session.Metadata.Branch,
				BaseRef: session.Metadata.DiffBaseRef, RepoPath: session.Metadata.WorkspaceRepoPath,
				SessionID: session.ID, ProjectID: session.ProjectID,
			})
			if s.afterObserve != nil {
				s.afterObserve()
			}
			if observeErr != nil {
				checkpoint.Kind = core.DirectionCheckpointFailedObserve
				command.Processing.ReasonCode = core.ReasonDirectionObserveFailed
			} else {
				checkpoint.WorktreePath = observation.Path
				checkpoint.HeadSHA = observation.HeadSHA
				checkpoint.Dirty = observation.Dirty
				checkpoint.Staged = observation.Staged
				checkpoint.Untracked = observation.Untracked
				changes := make([]map[string]string, 0, len(observation.Changes))
				for _, change := range observation.Changes {
					changes = append(changes, map[string]string{"path": change.Path, "status": change.Status})
				}
				summary, boundErr := core.BoundChangeSummary(changes)
				if boundErr != nil {
					return false, boundErr
				}
				checkpoint.ChangeSummaryJSON = summary
				if observation.Dirty || observation.Staged || observation.Untracked || len(observation.Changes) != 0 {
					checkpoint.Kind = core.DirectionCheckpointDirtyPreserved
					command.Processing.ReasonCode = core.ReasonDirectionDirtyWorktree
				} else if s.inspector != nil {
					inspection, inspectErr := s.inspector.InspectCandidate(ctx, observation.Path, baseSHA)
					if inspectErr != nil || inspection.CandidateSHA == "" {
						checkpoint.Kind = core.DirectionCheckpointFailedObserve
						command.Processing.ReasonCode = core.ReasonDirectionCandidateInvalid
					} else {
						checkpoint.Kind = core.DirectionCheckpointCleanCandidate
						checkpoint.HeadSHA = inspection.CandidateSHA
						command.Candidate = &core.CandidateCommit{
							ID: s.newID(), DevelopmentTaskID: processing.TaskID, AOSessionID: sessionID,
							CommitSHA: inspection.CandidateSHA, CreatedAt: now,
						}
					}
				}
			}
		}
	}
	if command.Processing.ReasonCode == core.ReasonNone && checkpoint.Kind == core.DirectionCheckpointNoWorktree {
		command.Processing.ReasonCode = core.ReasonDirectionNoWorktree
	}
	command.CancelTask = true
	command.Processing.CancelResult = core.DirectionCancelCancelled
	command.Checkpoint = &checkpoint
	return true, s.direction.FinalizeClearDevDirectionTask(ctx, command)
}

func (s *Service) advanceDirectionCompilation(ctx context.Context, snapshot core.RequirementSnapshot, planning core.ComplexPlanningSnapshot, change core.DirectionChangeSnapshot) (bool, bool, error) {
	if change.Revision == nil || change.Intent == nil || change.Request == nil {
		return false, false, errors.New("direction compilation is missing revision facts")
	}
	steward, ok := core.ComplexRoleBindingByRole(planning, core.StandardRoleSteward)
	if !ok {
		return false, false, errors.New("direction compilation has no Steward binding")
	}
	previous := versionByIDPtr(snapshot.RequirementVersions, change.Revision.PreviousRequirementVersionID)
	if previous == nil {
		return false, false, errors.New("direction compilation is missing the previous requirement version")
	}
	var previousDoc core.NormalizedRequirementDocument
	if err := json.Unmarshal([]byte(previous.RequirementText), &previousDoc); err != nil {
		return false, false, err
	}
	reqIDs, accIDs := core.StableIDsFromDocument(previousDoc)
	round := core.NextDirectionClarificationRound(change)
	requestID := s.newID()
	for _, existing := range change.CompilationRequests {
		if existing.ClarificationRound == round && existing.CompilationContextSHA256 != "" {
			requestID = existing.ID
			break
		}
	}
	contextModel := core.DirectionCompilationContext(
		snapshot.Requirement.ID, change.Revision.TargetRequirementVersionID, previous.ID, previous.SHA256,
		planning.Requirement.OriginalPRDSHA256, *change.Intent, *change.Request,
		change.DecisionRequestID, change.DecisionContentSHA256, reqIDs, accIDs, core.DirectionCompilationRounds(change),
	)
	contextSHA, _, err := core.HashCompilationContext(contextModel)
	if err != nil {
		return false, false, err
	}
	prompt := directionCompilationPrompt(planning, previous, change, requestID, contextSHA, round)
	var parsed core.RequirementCompilationResult
	message, changed, err := s.runDirectionAgentStep(ctx, planning, change, steward, requestID, core.ComplexAgentStepCompilation, prompt,
		standardStepReasons{Invalid: core.ReasonCode("STEWARD_RESULT_INVALID"), Timeout: core.ReasonCode("STEWARD_TIMEOUT"), Unavailable: core.ReasonCode("STEWARD_UNAVAILABLE"), ProjectID: snapshot.Requirement.AOProjectID},
		func(raw []byte) error {
			var parseErr error
			parsed, parseErr = core.ParseRequirementCompilationResult(raw, requestID, change.Revision.TargetRequirementVersionID, contextSHA, round, true)
			return parseErr
		})
	if err != nil {
		return changed, false, err
	}
	if changed && message.Text == "" {
		return true, false, nil
	}
	now := s.now().UTC()
	request := core.ComplexCompilationRequest{
		ID: requestID, DevelopmentRequirementID: snapshot.Requirement.ID, AgentStepID: message.StepID,
		ClarificationRound: round, CompilationContextSHA256: contextSHA, AdditionalRoundReason: parsed.AdditionalRoundReason, CreatedAt: now,
	}
	if parsed.Outcome == "CLARIFICATION_REQUIRED" {
		questions := make([]core.ComplexClarificationQuestion, 0, len(parsed.BlockingQuestions))
		for index, question := range parsed.BlockingQuestions {
			questions = append(questions, core.ComplexClarificationQuestion{
				CompilationRequestID: requestID, QuestionKey: question.Key, Text: question.Text,
				Reason: question.Reason, RequirementKeys: append([]string(nil), question.RequirementKeys...), Ordinal: index,
			})
		}
		if err := s.direction.RecordClearDevDirectionClarification(ctx, core.RecordDirectionClarificationCommand{
			DirectionRequestID: change.Request.ID, Request: request, Questions: questions,
		}); err != nil {
			return false, false, err
		}
		return true, false, nil
	}
	compilation := core.ComplexCompilation{
		ID: s.newID(), DevelopmentRequirementID: snapshot.Requirement.ID, CompilationRequestID: requestID,
		AgentStepID: message.StepID, Outcome: parsed.Outcome, Summary: parsed.Summary,
		TurnID: message.TurnID, FinalMessageID: message.MessageID, RawMessageText: message.Text,
		RawMessageSHA256: coreDigest([]byte(message.Text)), CreatedAt: now,
	}
	command := core.SettleDirectionCompilationCommand{DirectionRequestID: change.Request.ID, Request: request, Compilation: compilation, At: now}
	if parsed.Outcome == "READY" {
		reqMaps, accMaps, assignErr := core.AssignStableCompilationIDsFromPrevious(compilation.ID, parsed, reqIDs, accIDs)
		if assignErr != nil {
			return false, false, assignErr
		}
		_, encoded, digest, buildErr := core.BuildNormalizedRequirementDocument(parsed, planning.Requirement.OriginalPRDSHA256, reqMaps, accMaps)
		if buildErr != nil {
			return false, false, buildErr
		}
		compilation.NormalizedRequirementJSON = string(encoded)
		compilation.CompilationSHA256 = digest
		command.Compilation = compilation
		command.IDMaps = append(append([]core.ComplexIDMap{}, reqMaps...), accMaps...)
		command.Version = core.RequirementVersion{
			ID: change.Revision.TargetRequirementVersionID, DevelopmentRequirementID: snapshot.Requirement.ID,
			Version: previous.Version + 1, RequirementText: string(encoded), SHA256: digest,
			Status: core.RequirementVersionStatusDraft, CreatedAt: now,
		}
	}
	if err := s.direction.SettleClearDevDirectionCompilation(ctx, command); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (s *Service) runDirectionAgentStep(
	ctx context.Context,
	_ core.ComplexPlanningSnapshot,
	change core.DirectionChangeSnapshot,
	binding core.ComplexRoleBinding,
	requestID string,
	kind core.AgentStepKind,
	prompt string,
	reasons standardStepReasons,
	validate func([]byte) error,
) (standardMessage, bool, error) {
	step, found := core.DirectionAgentStepByRequest(change, kind, requestID)
	changed := false
	if !found {
		stepID := s.newID()
		step = core.AgentStep{
			ID: stepID, RoleBindingID: binding.ID, Kind: kind, RequestID: requestID,
			ClientMessageID: "cleardev-direction-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)),
			SendStatus: core.AgentStepSendStatusPending, RequestedAt: s.now().UTC(),
		}
		var err error
		step, _, err = s.direction.CreateClearDevDirectionAgentStep(ctx, step)
		if err != nil {
			return standardMessage{}, changed, err
		}
		changed = true
	}
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return standardMessage{}, changed, errors.New("saved ClearDev direction Agent step prompt does not match immutable facts")
	}
	if step.SendStatus == core.AgentStepSendStatusFailed {
		return standardMessage{}, changed, errComplexStopped
	}
	if step.SendStatus == core.AgentStepSendStatusSettled {
		message, err := s.readSettledStep(ctx, core.RoleSessionBinding{AOSessionID: binding.AOSessionID}, step, prompt)
		if err != nil {
			return standardMessage{}, changed, errComplexStopped
		}
		if err := validate([]byte(message.Text)); err != nil {
			return standardMessage{}, changed, errors.New("settled direction Agent step no longer passes its frozen protocol")
		}
		message.StepID = step.ID
		return message, changed, nil
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if err := s.relayAgentTurn(ctx, binding.DevelopmentRequirementID, core.AgentStepCategoryDirection, step, binding.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			if isMessageBudgetError(err) {
				return standardMessage{}, changed, err
			}
			if isAgentRecoveryDeferred(err) {
				return standardMessage{}, true, errComplexStopped
			}
			if failure, ok := ports.ChatFailureFromError(err); ok && (failure.Category == domain.AgentFailureDeliveryUnknown || failure.Retryable) {
				return standardMessage{}, true, errComplexStopped
			}
			now := s.now().UTC()
			step.SendStatus, step.FailedAt, step.ReasonCode = core.AgentStepSendStatusFailed, &now, reasons.Unavailable
			_, _ = s.direction.SettleClearDevDirectionAgentStep(ctx, step)
			return standardMessage{}, true, errComplexStopped
		}
		sentAt := s.now().UTC()
		if _, err := s.direction.MarkClearDevDirectionAgentStepSent(ctx, step.ID, sentAt); err != nil {
			return standardMessage{}, changed, err
		}
		step.SendStatus = core.AgentStepSendStatusSent
		step.SentAt = &sentAt
		changed = true
	}
	message, stopped, err := s.awaitValidAgentJSON(ctx, binding.DevelopmentRequirementID, core.AgentStepCategoryDirection, binding.AOSessionID, step, prompt, validate, reasons, s.failDirectionAgentStep, errComplexStopped)
	if err != nil || stopped {
		return standardMessage{}, changed || stopped, err
	}
	now := s.now().UTC()
	step.SendStatus = core.AgentStepSendStatusSettled
	step.TurnID, step.FinalMessageID, step.FinalMessageText = message.TurnID, message.MessageID, message.Text
	step.MessageSHA256 = coreDigest([]byte(message.Text))
	step.CompletedAt = &now
	if _, err := s.direction.SettleClearDevDirectionAgentStep(ctx, step); err != nil {
		return standardMessage{}, changed, err
	}
	message.StepID = step.ID
	return message, true, nil
}

func (s *Service) failDirectionAgentStep(ctx context.Context, step core.AgentStep, reason core.ReasonCode) error {
	now := s.now().UTC()
	step.SendStatus, step.FailedAt, step.ReasonCode = core.AgentStepSendStatusFailed, &now, reason
	_, err := s.direction.SettleClearDevDirectionAgentStep(ctx, step)
	return err
}

func buildDirectionChangeView(snapshot core.DirectionChangeSnapshot, versions []core.RequirementVersion, planning *ComplexPlanningView) *DirectionChangeView {
	var plans []core.ComplexEngineeringPlan
	var reviews []core.ComplexPlanReview
	if planning != nil {
		plans = planning.Plans
		reviews = planning.Reviews
	}
	view := &DirectionChangeView{
		Phase:  core.DeriveDirectionChangePhase(snapshot, versions, plans, reviews),
		Intent: snapshot.Intent, Request: snapshot.Request, Gate: snapshot.Gate,
		Snapshots: snapshot.Snapshots, Processings: snapshot.Processings, Checkpoints: snapshot.Checkpoints,
		Revision: snapshot.Revision, DecisionRequestID: snapshot.DecisionRequestID,
		CompilationRequests: snapshot.CompilationRequests, AgentSteps: snapshot.AgentSteps,
		Questions: snapshot.Questions, Answers: snapshot.Answers,
	}
	if snapshot.Revision != nil {
		if target := versionByIDPtr(versions, snapshot.Revision.TargetRequirementVersionID); target != nil {
			view.TargetVersion = target
		}
	}
	return view
}

func versionByIDPtr(versions []core.RequirementVersion, id string) *core.RequirementVersion {
	for i := range versions {
		if versions[i].ID == id {
			return &versions[i]
		}
	}
	return nil
}
