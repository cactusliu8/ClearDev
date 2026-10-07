package cleardev

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

var errComplexStopped = errors.New("ClearDev complex flow reached a durable stop")

func (s *Service) CreateComplexRequirement(ctx context.Context, input CreateComplexRequirementInput) (RequirementView, error) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return RequirementView{}, err
	}

	if s.facts == nil || s.complex == nil || s.ao == nil || s.sessions == nil || s.chat == nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_UNAVAILABLE", "ClearDev complex planning is not fully configured")
	}
	input.AOProjectID = strings.TrimSpace(input.AOProjectID)
	input.Name = strings.TrimSpace(input.Name)
	if input.AOProjectID == "" {
		return RequirementView{}, apierr.Invalid("AO_PROJECT_ID_REQUIRED", "aoProjectId is required", nil)
	}
	if input.Name == "" {
		return RequirementView{}, apierr.Invalid("NAME_REQUIRED", "name is required", nil)
	}
	prdText, err := core.ValidateComplexPRDText(input.PRDText)
	if err != nil {
		return RequirementView{}, apierr.Invalid("PRD_TEXT_INVALID", err.Error(), nil)
	}
	project, ok, err := s.ao.GetProject(ctx, input.AOProjectID)
	if err != nil {
		return RequirementView{}, apierr.Internal("AO_PROJECT_READ_FAILED", "Could not read the AO project")
	}
	if !ok || !project.ArchivedAt.IsZero() {
		return RequirementView{}, apierr.NotFound("AO_PROJECT_NOT_FOUND", "AO project was not found")
	}
	if project.Kind.WithDefault() != domain.ProjectKindSingleRepo {
		return RequirementView{}, apierr.Invalid("AO_PROJECT_KIND_UNSUPPORTED", "ClearDev S01 supports only single_repo AO projects", nil)
	}
	now := s.now().UTC()
	requirementID := s.newID()
	benchmarkBinding, bindingErr := s.benchmarkBindingForNewRequirement(project.Path, requirementID, input.AOProjectID, now)
	if bindingErr != nil {
		return RequirementView{}, bindingErr
	}
	targetVersionID := s.newID()
	bindingID := s.newID()
	digest := sha256.Sum256([]byte(prdText))
	command := core.CreateComplexRequirementCommand{
		Requirement: core.DevelopmentRequirement{
			ID: requirementID, AOProjectID: input.AOProjectID, Name: input.Name,
			CreatedAt: now, UpdatedAt: now,
		},
		OriginalPRDText:            prdText,
		OriginalPRDSHA256:          fmt.Sprintf("%x", digest),
		TargetRequirementVersionID: targetVersionID,
		StewardRoleBinding: core.ComplexRoleBinding{
			ID: bindingID, DevelopmentRequirementID: requirementID, Role: core.StandardRoleSteward,
			SessionCreationIdempotencyKey: complexSpawnKey(requirementID, core.StandardRoleSteward, bindingID),
			Status:                        core.RoleBindingStatusRequested, RequestedAt: now,
		},
		BenchmarkBinding: benchmarkBinding,
	}
	if err := s.complex.CreateClearDevComplexRequirement(ctx, command); err != nil {
		return RequirementView{}, mapStoreError(err, "CREATE_COMPLEX_REQUIREMENT_FAILED")
	}
	s.scheduleComplexFlow(requirementID)
	return s.GetRequirement(ctx, requirementID)
}

func (s *Service) SubmitComplexClarifications(ctx context.Context, requirementID string, input SubmitComplexClarificationsInput) (RequirementView, error) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return RequirementView{}, err
	}

	if s.complex == nil || s.facts == nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_UNAVAILABLE", "ClearDev complex planning is not configured")
	}
	requirementID = strings.TrimSpace(requirementID)
	if requirementID == "" {
		return RequirementView{}, apierr.Invalid("CLEARDEV_REQUIREMENT_ID_REQUIRED", "ClearDev requirement id is required", nil)
	}
	input.CompilationRequestID = strings.TrimSpace(input.CompilationRequestID)
	if input.CompilationRequestID == "" {
		return RequirementView{}, apierr.Invalid("COMPILATION_REQUEST_ID_REQUIRED", "compilationRequestId is required", nil)
	}
	answers := make([]core.ComplexClarificationAnswer, 0, len(input.Answers))
	now := s.now().UTC()
	for _, answer := range input.Answers {
		answers = append(answers, core.ComplexClarificationAnswer{
			CompilationRequestID: input.CompilationRequestID,
			QuestionKey:          strings.TrimSpace(answer.QuestionKey),
			Text:                 strings.TrimSpace(answer.Text),
			CreatedAt:            now,
		})
	}
	command := core.SubmitComplexClarificationCommand{
		DevelopmentRequirementID: requirementID, CompilationRequestID: input.CompilationRequestID,
		ClarificationRound: input.ClarificationRound, Answers: answers, At: now,
	}
	if s.direction != nil {
		if _, found, err := s.direction.GetClearDevDirectionCompilationRequest(ctx, input.CompilationRequestID); err != nil {
			return RequirementView{}, apierr.Internal("CLEARDEV_DIRECTION_READ_FAILED", "Could not read the ClearDev direction compilation request")
		} else if found {
			if err := s.direction.SubmitClearDevDirectionClarificationAnswers(ctx, command); err != nil {
				return RequirementView{}, mapStoreError(err, "SUBMIT_COMPLEX_CLARIFICATIONS_FAILED")
			}
			s.scheduleDirectionChange(requirementID)
			return s.GetRequirement(ctx, requirementID)
		}
	}
	if err := s.complex.SubmitClearDevComplexClarificationAnswers(ctx, command); err != nil {
		return RequirementView{}, mapStoreError(err, "SUBMIT_COMPLEX_CLARIFICATIONS_FAILED")
	}
	s.scheduleComplexFlow(requirementID)
	return s.GetRequirement(ctx, requirementID)
}

func (s *Service) ResumeComplexFlows(ctx context.Context) error {
	if err := s.ValidateControlledConfiguration(); err != nil {
		return err
	}

	if s.complex == nil {
		return nil
	}
	ids, err := s.complex.ListClearDevRunnableComplexFlows(ctx)
	if err != nil {
		return fmt.Errorf("list runnable ClearDev complex flows: %w", err)
	}
	if store, ok := s.complex.(productPlanStore); ok {
		more, e := store.ListClearDevAutomaticProducts(ctx)
		if e != nil {
			return e
		}
		ids = append(ids, more...)
	}
	if store, ok := s.complex.(productSourceStore); ok {
		more, err := store.ListClearDevPendingProductSources(ctx)
		if err != nil {
			return err
		}
		ids = append(ids, more...)
	}
	for _, id := range ids {
		s.scheduleComplexFlow(id)
	}
	return nil
}

func (s *Service) scheduleComplexFlow(requirementID string) {
	if err := s.ValidateControlledConfiguration(); err != nil {
		s.logger.Error("ClearDev controlled scheduling rejected", "err", err)
		return
	}

	if s.complex == nil || strings.TrimSpace(requirementID) == "" {
		return
	}
	s.complexMu.Lock()
	if s.complexRunning[requirementID] {
		s.complexWake[requirementID] = true
		s.complexMu.Unlock()
		return
	}
	s.complexRunning[requirementID] = true
	s.complexMu.Unlock()
	s.runBackground(func() {
		for {
			if err := s.runComplexFlow(s.backgroundContext, requirementID); err != nil {
				s.logger.Error("ClearDev complex flow stopped with an internal error", "requirementID", requirementID, "error", err)
				s.recordWorkflowOperationFailure(s.backgroundContext, requirementID, "planning")
			}
			s.complexMu.Lock()
			if s.complexWake[requirementID] {
				delete(s.complexWake, requirementID)
				s.complexMu.Unlock()
				continue
			}
			delete(s.complexRunning, requirementID)
			s.complexMu.Unlock()
			return
		}
	})
}

func (s *Service) runComplexFlow(ctx context.Context, requirementID string) error {
	blocked, err := s.recoverRequirementAgentAttempts(ctx, requirementID)
	if err != nil || blocked {
		return err
	}
	for iteration := 0; iteration < 64; iteration++ {
		progressed, done, err := s.advanceComplexFlow(ctx, requirementID)
		if errors.Is(err, errComplexStopped) {
			return nil
		}
		if err != nil || done {
			return err
		}
		if !progressed {
			if s.afterComplexIdle != nil {
				s.afterComplexIdle(requirementID)
			}
			return nil
		}
	}
	return errors.New("ClearDev complex flow exceeded its bounded advance count")
}

func (s *Service) advanceComplexFlow(ctx context.Context, requirementID string) (bool, bool, error) {
	snapshot, ok, err := s.facts.GetClearDevRequirement(ctx, requirementID)
	if err != nil {
		return false, false, err
	}
	if !ok {
		return false, false, errors.New("ClearDev requirement disappeared after complex start")
	}
	planning, ok, err := s.complex.GetClearDevComplexPlanning(ctx, requirementID)
	if err != nil {
		return false, false, err
	}
	if !ok {
		return false, true, nil
	}
	if snapshot.Requirement.CancelledAt != nil {
		return false, true, nil
	}
	if changed, err := s.advanceUnifiedFailureRecovery(ctx, requirementID, nil); err != nil || changed {
		return changed, false, err
	}
	if products, supported := s.complex.(ProductFactStore); supported {
		product, exists, productErr := products.GetClearDevProduct(ctx, requirementID)
		if productErr != nil {
			return false, false, productErr
		}
		if exists {
			if handled, err := s.advanceSourcePreparation(ctx, product); handled {
				if err != nil {
					return false, false, err
				}
				sourceStore, ok := s.complex.(productSourceStore)
				if !ok {
					return false, false, errors.New("source preparation storage unavailable")
				}
				p, err := sourceStore.GetLatestClearDevProductSourcePreparation(ctx, product.Goal.ID)
				if err != nil {
					return false, false, err
				}
				return p != nil && p.Status == "APPLIED", p == nil || p.Status != "APPLIED", nil
			}
			return s.advanceProductDiscovery(ctx, product, planning)
		}
	}
	projectState, projectErr := s.projectPlanningState(ctx, requirementID)
	if projectErr != nil {
		return false, false, projectErr
	}
	if projectState != nil && projectState.ReasonCode != core.ReasonNone {
		return false, true, nil
	}
	var change core.DirectionChangeSnapshot
	if s.direction != nil {
		loaded, exists, dirErr := s.direction.GetClearDevDirectionChange(ctx, requirementID)
		if dirErr != nil {
			return false, false, dirErr
		}
		if exists {
			change = loaded
		}
	}
	if confirmed := confirmedComplexRequirementVersion(snapshot.RequirementVersions); confirmed != nil && s.direction != nil {
		if stopped, stopErr := s.direction.HasActiveClearDevDirectionStop(ctx, confirmed.ID); stopErr != nil {
			return false, false, stopErr
		} else if stopped {
			return false, false, nil
		}
	}
	phase, _ := core.DeriveComplexPlanningPhase(planning, snapshot.RequirementVersions)
	switch phase {
	case core.ComplexPlanningAwaitingConfirmation:
		changed, e := s.confirmAutomaticStage(ctx, snapshot)
		return changed, false, e
	case core.ComplexPlanningAwaitingClarification:
		return false, false, nil
	case core.ComplexPlanningApproved, core.ComplexPlanningValidated:
		return s.advanceApprovedComplexPlan(ctx, snapshot, planning)
	case core.ComplexPlanningProjectPlanned:
		changed, e := s.startAutomaticStage(ctx, snapshot, planning)
		return changed, true, e
	case core.ComplexPlanningNeedsHuman, core.ComplexPlanningRejected:
		return false, true, nil
	}

	steward, ok := core.ComplexRoleBindingByRole(planning, core.StandardRoleSteward)
	if !ok {
		return false, false, errors.New("ClearDev complex requirement has no Steward binding")
	}
	stewardRecord, progressed, err := s.ensureComplexRoleSession(ctx, planning, steward, snapshot.Requirement.AOProjectID, domain.KindOrchestrator, "", core.ReasonCode("STEWARD_UNAVAILABLE"))
	if err != nil {
		return progressed, false, err
	}
	if progressed {
		return true, false, nil
	}
	if controlledPreflightIdle(steward.Status, stewardRecord.ID) {
		return false, false, nil
	}

	switch phase {
	case core.ComplexPlanningCompiling:
		return s.advanceComplexCompilation(ctx, snapshot, planning, steward)
	case core.ComplexPlanningPlanning:
		return s.advanceComplexPlanning(ctx, snapshot, planning, change, steward)
	case core.ComplexPlanningAwaitingPlanReview:
		return s.advanceComplexPlanReview(ctx, snapshot, planning, change, steward)
	default:
		return false, false, nil
	}
}

func (s *Service) advanceComplexCompilation(ctx context.Context, snapshot core.RequirementSnapshot, planning core.ComplexPlanningSnapshot, steward core.ComplexRoleBinding) (bool, bool, error) {
	round := core.NextComplexClarificationRound(planning)
	requestID := nextComplexCompilationRequestID(planning, round, s.newID)
	contextModel := core.CompilationContextFromSnapshot(planning, planning.Requirement.TargetRequirementVersionID)
	contextSHA, _, err := core.HashCompilationContext(contextModel)
	if err != nil {
		return false, false, err
	}
	stage, stageLinked, stageErr := s.productStageSource(ctx, snapshot.Requirement.ID)
	if stageErr != nil {
		return false, false, stageErr
	}
	mailPolicy := false
	if stage.Definition.ExecutionBasis == nil {
		var policyErr error
		_, mailPolicy, policyErr = s.mailRequirementBaseline(ctx, snapshot.Requirement)
		if policyErr != nil {
			return false, false, policyErr
		}
	}
	prompt := complexCompilationPrompt(planning, requestID, contextSHA, round)
	if stageLinked {
		prompt = productStageCompilationPrompt(stage, planning, round)
		if plans, ok := s.complex.(productPlanStore); ok {
			a, e := plans.GetClearDevProductPlanAuthorization(ctx, stage.ProductID, stage.DiscussionID)
			if e != nil {
				return false, false, e
			}
			if a != nil && a.Status == "RESOLVED" && a.Decision == "APPROVE" {
				legacy := false
				for _, step := range planning.AgentSteps {
					if step.RequestID == requestID && step.Kind == core.ComplexAgentStepCompilation && step.PromptSHA256 == coreDigest([]byte(prompt)) {
						legacy = true
					}
				}
				if !legacy {
					prompt += "\n本阶段已有准确整体计划的真人授权，控制程序会在原范围内承接，不再逐阶段要求真人批准。为避免引入额外功能，READY 的 requirements.text 只能逐字使用本阶段 goal 或某条 acceptanceCriteria；覆盖全部原验收，不改非目标及执行依据。需要新增或改写目标、功能、验收时提出澄清，由用户决定。此消息本身不产生授权。"
				}
			}
		}
	}
	if mailPolicy {
		prompt += "\n" + core.MailDeliveryInstructionsV2 + "\nIdentify requirements outside this fixed scope before planning; explain the scope mismatch instead of proposing permissions or dependencies to bypass it."
	}
	var parsed core.RequirementCompilationResult
	message, changed, stepErr := s.runComplexAgentStep(ctx, planning, steward, requestID, core.ComplexAgentStepCompilation, prompt,
		standardStepReasons{Invalid: core.ReasonCode("STEWARD_RESULT_INVALID"), Timeout: core.ReasonCode("STEWARD_TIMEOUT"), Unavailable: core.ReasonCode("STEWARD_UNAVAILABLE"), ProjectID: snapshot.Requirement.AOProjectID},
		func(raw []byte) error {
			var parseErr error
			if stageLinked {
				parsed, parseErr = core.ParseProductStageCompilationResult(raw, requestID, planning.Requirement.TargetRequirementVersionID, contextSHA, round, stage.Definition)
			} else {
				parsed, parseErr = core.ParseRequirementCompilationResult(raw, requestID, planning.Requirement.TargetRequirementVersionID, contextSHA, round, false)
			}
			return parseErr
		})
	if stepErr != nil {
		return changed, false, stepErr
	}
	if changed && message.Text == "" {
		return true, false, nil
	}
	if compilationRequestExists(planning, requestID) {
		return changed, false, nil
	}
	if err := s.persistComplexCompilation(ctx, planning, parsed, message, requestID, contextSHA, round); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (s *Service) persistComplexCompilation(ctx context.Context, planning core.ComplexPlanningSnapshot, parsed core.RequirementCompilationResult, message standardMessage, requestID, contextSHA string, round int) error {
	now := s.now().UTC()
	request := core.ComplexCompilationRequest{
		ID: requestID, DevelopmentRequirementID: planning.Requirement.DevelopmentRequirementID,
		AgentStepID: message.StepID, ClarificationRound: round, CompilationContextSHA256: contextSHA,
		AdditionalRoundReason: parsed.AdditionalRoundReason, CreatedAt: now,
	}
	if parsed.Outcome == "CLARIFICATION_REQUIRED" {
		questions := make([]core.ComplexClarificationQuestion, 0, len(parsed.BlockingQuestions))
		for index, question := range parsed.BlockingQuestions {
			questions = append(questions, core.ComplexClarificationQuestion{
				CompilationRequestID: requestID, QuestionKey: question.Key, Text: question.Text,
				Reason: question.Reason, RequirementKeys: append([]string(nil), question.RequirementKeys...), Ordinal: index,
			})
		}
		return s.complex.RecordClearDevComplexClarification(ctx, core.RecordComplexClarificationCommand{Request: request, Questions: questions})
	}
	compilation := core.ComplexCompilation{
		ID: s.newID(), DevelopmentRequirementID: planning.Requirement.DevelopmentRequirementID,
		CompilationRequestID: requestID, AgentStepID: message.StepID, Outcome: parsed.Outcome, Summary: parsed.Summary,
		TurnID: message.TurnID, FinalMessageID: message.MessageID, RawMessageText: message.Text,
		RawMessageSHA256: coreDigest([]byte(message.Text)), CreatedAt: now,
	}
	command := core.SettleComplexCompilationCommand{Request: request, Compilation: compilation, At: now}
	if parsed.Outcome == "READY" {
		reqMaps, accMaps := core.AssignStableCompilationIDs(compilation.ID, parsed)
		_, encoded, digest, err := core.BuildNormalizedRequirementDocument(parsed, planning.Requirement.OriginalPRDSHA256, reqMaps, accMaps)
		if err != nil {
			return err
		}
		compilation.NormalizedRequirementJSON = string(encoded)
		compilation.CompilationSHA256 = digest
		command.Compilation = compilation
		command.IDMaps = append(reqMaps, accMaps...)
		command.Version = core.RequirementVersion{
			ID: planning.Requirement.TargetRequirementVersionID, DevelopmentRequirementID: planning.Requirement.DevelopmentRequirementID,
			Version: 1, RequirementText: string(encoded), SHA256: digest,
			Status: core.RequirementVersionStatusDraft, CreatedAt: now,
		}
	}
	return s.complex.SettleClearDevComplexCompilation(ctx, command)
}

func (s *Service) advanceComplexPlanning(ctx context.Context, snapshot core.RequirementSnapshot, planning core.ComplexPlanningSnapshot, change core.DirectionChangeSnapshot, steward core.ComplexRoleBinding) (bool, bool, error) {
	_ = steward
	confirmed := confirmedComplexRequirementVersion(snapshot.RequirementVersions)
	if confirmed == nil {
		return false, false, errors.New("complex planning requires a confirmed requirement version")
	}
	planner, exists := core.ComplexRoleBindingByRole(planning, core.StandardRoleEngineeringPlanner)
	if !exists {
		planner = core.ComplexRoleBinding{
			ID: s.newID(), DevelopmentRequirementID: snapshot.Requirement.ID, Role: core.StandardRoleEngineeringPlanner,
			Status: core.RoleBindingStatusRequested, RequestedAt: s.now().UTC(),
		}
		planner.SessionCreationIdempotencyKey = complexSpawnKey(confirmed.ID, planner.Role, planner.ID)
		if _, _, err := s.complex.CreateClearDevComplexRoleBinding(ctx, core.CreateComplexRoleBindingCommand{Binding: planner}); err != nil {
			return false, false, err
		}
		return true, false, nil
	}
	plannerBranch := complexBranch("planner", confirmed.ID)
	plannerRecord, progressed, err := s.ensureComplexRoleSession(ctx, planning, planner, snapshot.Requirement.AOProjectID, domain.KindWorker, plannerBranch, core.ReasonCode("PLANNER_UNAVAILABLE"))
	if err != nil {
		return progressed, false, err
	}
	if progressed {
		return true, false, nil
	}
	if controlledPreflightIdle(planner.Status, plannerRecord.ID) {
		return false, false, nil
	}
	ready, ok := core.ReadyCompilationForSHA(planning, change, confirmed.SHA256)
	if !ok {
		return false, false, errors.New("confirmed complex requirement is missing a READY compilation")
	}
	if core.ComplexReplanLimitReached(planning, confirmed.ID) {
		return false, true, nil
	}
	var doc core.NormalizedRequirementDocument
	if err := json.Unmarshal([]byte(ready.NormalizedRequirementJSON), &doc); err != nil {
		return false, false, err
	}
	coverage := core.CoverageFromNormalizedDocument(doc)
	stage, _, stageErr := s.productStageSource(ctx, snapshot.Requirement.ID)
	if stageErr != nil {
		return false, false, stageErr
	}
	projectPlan := stage.Definition.ExecutionBasis != nil
	var catalog []core.ComplexCheckSpec
	if projectPlan {
		catalog = stage.Definition.ExecutionBasis.CheckCatalog()
		if plannerRecord.Metadata.DiffBaseSHA != stage.BaseCommitSHA || !s.productWorkspaceUnchanged(ctx, plannerRecord) {
			_, err := s.complex.FailClearDevComplexRoleBinding(ctx, core.FailComplexRoleBindingCommand{RoleBindingID: planner.ID, ReasonCode: "PRODUCT_PLANNER_WORKSPACE_CHANGED", At: s.now().UTC()})
			return err == nil, true, err
		}
	} else {
		var catalogErr error
		catalog, catalogErr = s.complexCheckCatalogForRequirement(ctx, snapshot.Requirement)
		if catalogErr != nil {
			return false, false, catalogErr
		}
	}
	requestID := nextComplexPlanningRequestID(planning, confirmed.ID)
	var questionPlan core.ComplexEngineeringPlan
	var answer *core.PlannerClarificationAnswer
	for _, plan := range planning.Plans {
		if plan.RequirementVersionID == confirmed.ID && plan.Version > questionPlan.Version {
			questionPlan = plan
		}
	}
	if registered, ok := core.PlannerAnswerForPlan(planning, questionPlan); ok && registered.NextPlanningRequestID == requestID {
		answer = &registered
	}
	var feedback *core.ComplexPlanFeedback
	var feedbackErr error
	if answer == nil {
		feedback, feedbackErr = core.ComplexPlanFeedbackForNextPlanning(planning, confirmed.ID, confirmed.SHA256, ready.CompilationSHA256)
	}
	if feedbackErr != nil {
		return false, false, feedbackErr
	}
	mailPolicy := false
	var legacyPrompt, contractPrompt string
	if projectPlan {
		legacyPrompt = projectEngineeringPrompt(requestID, confirmed, ready, doc, planning.Requirement.OriginalPRDText, stage, feedback, plannerRecord.Harness)
		contractPrompt = legacyPrompt
	} else {
		var policyErr error
		_, mailPolicy, policyErr = s.mailRequirementBaseline(ctx, snapshot.Requirement)
		if policyErr != nil {
			return false, false, policyErr
		}
		legacyPrompt = complexEngineeringPlanPromptWithCatalog(requestID, confirmed, ready, doc, planning.Requirement.OriginalPRDText, catalog, feedback)
		contractPrompt = plannerTaskContractPrompt(requestID, confirmed, ready, doc, planning.Requirement.OriginalPRDText, catalog, feedback)
		if mailPolicy {
			legacyPrompt += "\n" + core.MailDeliveryInstructionsV2
			contractPrompt += "\n" + core.MailDeliveryInstructionsV2
		}
	}
	if answer != nil {
		// New messages recheck the original question/session in relayAgentTurn
		// and its reservation transaction. A sent successor can still be running
		// after restart; retain its normal delivery reconciliation and observation.
		extra := plannerAnswersPrompt(questionPlan, *answer)
		legacyPrompt += extra
		contractPrompt += extra
	}
	priorStep, _ := core.ComplexAgentStepByRequest(planning, core.ComplexAgentStepEngineeringPlan, requestID)
	prompt, contract, promptErr := chooseComplexPlannerPrompt(s.plannerTaskContracts && !projectPlan, priorStep, legacyPrompt, contractPrompt)
	if promptErr != nil {
		return false, true, promptErr
	}
	var parsed *core.ComplexEngineeringPlanResult
	var normalized []byte
	var planSHA string
	message, changed, stepErr := s.runComplexAgentStep(ctx, planning, planner, requestID, core.ComplexAgentStepEngineeringPlan, prompt,
		standardStepReasons{Invalid: core.ReasonCode("PLANNER_RESULT_INVALID"), Timeout: core.ReasonCode("PLANNER_TIMEOUT"), Unavailable: core.ReasonCode("PLANNER_UNAVAILABLE"), ProjectID: snapshot.Requirement.AOProjectID},
		func(raw []byte) error {
			var parseErr error
			if projectPlan {
				parsed, normalized, planSHA, parseErr = parseProjectPlannerOutcome(raw, requestID, confirmed.ID, confirmed.SHA256, ready.CompilationSHA256, coverage, *stage.Definition.ExecutionBasis)
			} else {
				parsed, normalized, planSHA, parseErr = parseComplexPlannerOutcome(raw, contract, requestID, confirmed.ID, confirmed.SHA256, ready.CompilationSHA256, coverage, catalog)
			}
			if parseErr == nil && mailPolicy && parsed != nil {
				parseErr = core.ValidateMailPlan(*parsed)
			}
			return parseErr
		})
	if stepErr != nil {
		return changed, false, stepErr
	}
	if projectPlan && !s.productWorkspaceUnchanged(ctx, plannerRecord) {
		_, err := s.complex.FailClearDevComplexRoleBinding(ctx, core.FailComplexRoleBindingCommand{RoleBindingID: planner.ID, ReasonCode: "PRODUCT_PLANNER_WORKSPACE_CHANGED", At: s.now().UTC()})
		return err == nil, true, err
	}
	if projectPlan && !s.selectedProjectCurrent(ctx, stage.Selection) {
		return false, true, nil
	}
	if planExistsForRequest(planning, requestID) {
		return changed, false, nil
	}
	plan := core.ComplexEngineeringPlan{
		ID: s.newID(), PlanningRequestID: requestID, DevelopmentRequirementID: snapshot.Requirement.ID,
		RequirementVersionID: confirmed.ID, RequirementSHA256: confirmed.SHA256, CompilationSHA256: ready.CompilationSHA256,
		Version: int64(len(planning.Plans) + 1), PlannerRoleBindingID: planner.ID, AgentStepID: message.StepID,
		TurnID: message.TurnID, FinalMessageID: message.MessageID, PlanJSON: string(normalized), PlanSHA256: planSHA,
		CreatedAt: s.now().UTC(),
	}
	command := core.CreateComplexPlanCommand{Plan: plan}
	if contract && parsed != nil {
		catalogSHA, err := core.ComplexCheckCatalogSHA256(catalog)
		if err != nil {
			return false, false, err
		}
		command.Validation = &core.ComplexPlanValidation{PlanID: plan.ID, PlanSHA256: plan.PlanSHA256, Policy: core.PlannerTaskContractPolicy, CheckCatalogSHA256: catalogSHA, CreatedAt: plan.CreatedAt}
	}
	if err := s.complex.CreateClearDevComplexEngineeringPlan(ctx, command); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (s *Service) advanceComplexPlanReview(ctx context.Context, snapshot core.RequirementSnapshot, planning core.ComplexPlanningSnapshot, change core.DirectionChangeSnapshot, steward core.ComplexRoleBinding) (bool, bool, error) {
	versionID := ""
	if confirmed := confirmedComplexRequirementVersion(snapshot.RequirementVersions); confirmed != nil {
		versionID = confirmed.ID
	}
	plan, ok := latestUnreviewedComplexPlan(planning, versionID)
	if !ok {
		return false, false, nil
	}
	var parsedPlan core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(plan.PlanJSON), &parsedPlan); err != nil {
		return false, false, err
	}
	ready, ok := core.ReadyCompilationForSHA(planning, change, plan.CompilationSHA256)
	if !ok {
		return false, false, errors.New("plan review is missing its READY compilation")
	}
	var doc core.NormalizedRequirementDocument
	if err := json.Unmarshal([]byte(ready.NormalizedRequirementJSON), &doc); err != nil {
		return false, false, err
	}
	coverage := core.CoverageFromNormalizedDocument(doc)
	taskKeys := make([]string, 0, len(parsedPlan.Tasks))
	for _, task := range parsedPlan.Tasks {
		taskKeys = append(taskKeys, task.Key)
	}
	catalog, catalogErr := s.complexCheckCatalogForRequirement(ctx, snapshot.Requirement)
	if catalogErr != nil {
		return false, false, catalogErr
	}
	requestID := nextComplexReviewRequestID(planning, plan.ID, s.newID)
	_, mailPolicy, policyErr := s.mailRequirementBaseline(ctx, snapshot.Requirement)
	if policyErr != nil {
		return false, false, policyErr
	}
	if mailPolicy {
		if err := core.ValidateMailPlan(parsedPlan); err != nil {
			return false, false, err
		}
	}
	prompt := complexPlanReviewPromptWithCatalog(requestID, plan, doc, catalog)
	if mailPolicy {
		prompt += "\n" + core.MailDeliveryInstructionsV2
	}
	var parsed core.ComplexPlanReviewResult
	message, changed, stepErr := s.runComplexAgentStep(ctx, planning, steward, requestID, core.ComplexAgentStepPlanReview, prompt,
		standardStepReasons{Invalid: core.ReasonCode("STEWARD_RESULT_INVALID"), Timeout: core.ReasonCode("STEWARD_TIMEOUT"), Unavailable: core.ReasonCode("STEWARD_UNAVAILABLE"), ProjectID: snapshot.Requirement.AOProjectID},
		func(raw []byte) error {
			var parseErr error
			parsed, _, parseErr = core.ParseComplexPlanReviewResult(raw, requestID, plan.ID, plan.PlanSHA256, coverage, taskKeys)
			return parseErr
		})
	if stepErr != nil {
		return changed, false, stepErr
	}
	if _, reviewed := reviewForComplexPlan(planning, plan.ID); reviewed {
		return changed, false, nil
	}
	findings := parsed.Findings
	if findings == nil {
		findings = []core.ComplexReviewFinding{}
	}
	encodedFindings, err := json.Marshal(findings)
	if err != nil {
		return false, false, err
	}
	review := core.ComplexPlanReview{
		ID: s.newID(), PlanID: plan.ID, StewardRoleBindingID: steward.ID, AgentStepID: message.StepID,
		ReviewRequestID: requestID, Verdict: core.PlanReviewVerdict(parsed.Verdict), ReasonCode: core.ReasonCode(parsed.ReasonCode),
		Summary: parsed.Summary, FindingsJSON: string(encodedFindings), PlanSHA256: plan.PlanSHA256,
		TurnID: message.TurnID, FinalMessageID: message.MessageID, CreatedAt: s.now().UTC(),
	}
	if err := s.complex.RecordClearDevComplexPlanReview(ctx, core.RecordComplexPlanReviewCommand{Review: review, At: review.CreatedAt}); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func (s *Service) ensureComplexRoleSession(
	ctx context.Context,
	planning core.ComplexPlanningSnapshot,
	binding core.ComplexRoleBinding,
	projectID string,
	kind domain.SessionKind,
	branch string,
	unavailableReason core.ReasonCode,
) (domain.SessionRecord, bool, error) {
	if binding.Status == core.RoleBindingStatusFailed || binding.Status == core.RoleBindingStatusEnded {
		return domain.SessionRecord{}, false, errComplexStopped
	}
	if binding.Status == core.RoleBindingStatusBound {
		record, found, err := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
		if err != nil {
			return domain.SessionRecord{}, false, err
		}
		if !found || record.IsTerminated || s.validateComplexSession(ctx, record, binding, projectID, kind, planning) != nil {
			return domain.SessionRecord{}, false, nil
		}
		return record, false, nil
	}
	if binding.Status != core.RoleBindingStatusRequested {
		return domain.SessionRecord{}, false, errComplexStopped
	}
	cfg := ports.SpawnConfig{
		ProjectID: domain.ProjectID(projectID), Kind: kind,
		Branch: branch, Prompt: "", RequestedMode: domain.SessionModeChat,
		AgentConfig:            ports.AgentConfig{Permissions: domain.PermissionModeAuto},
		DisplayName:            standardRoleDisplayName(binding.Role),
		CreationIdempotencyKey: binding.SessionCreationIdempotencyKey,
	}
	stage, linked, err := s.productStageSource(ctx, binding.DevelopmentRequirementID)
	if err != nil {
		return domain.SessionRecord{}, false, err
	}
	if linked && stage.Definition.ExecutionBasis != nil {
		cfg.WorkspaceBaseCommitSHA = stage.BaseCommitSHA
		if kind == domain.KindOrchestrator && cfg.Branch == "" {
			// A later stage can start while the previous stage's Steward is
			// still available for its original record. Its AO canonical branch
			// cannot be checked out by both controlled sessions.
			cfg.Branch = complexBranch("stage-steward", binding.ID)
		}
	}
	session, blocked, err := s.spawnControlledChatSession(ctx, binding.DevelopmentRequirementID, binding.ID, cfg)
	if blocked {
		return domain.SessionRecord{}, false, nil
	}
	if err != nil {
		s.logger.Error("ClearDev complex role session spawn failed", "role", binding.Role, "bindingID", binding.ID, "error", err)
		_, _ = s.complex.FailClearDevComplexRoleBinding(ctx, core.FailComplexRoleBindingCommand{RoleBindingID: binding.ID, ReasonCode: unavailableReason, At: s.now().UTC()})
		return domain.SessionRecord{}, true, errComplexStopped
	}
	if session.IsTerminated {
		_, _ = s.complex.FailClearDevComplexRoleBinding(ctx, core.FailComplexRoleBindingCommand{RoleBindingID: binding.ID, ReasonCode: unavailableReason, At: s.now().UTC()})
		return domain.SessionRecord{}, true, errComplexStopped
	}
	if validateErr := s.validateComplexSession(ctx, session.SessionRecord, binding, projectID, kind, planning); validateErr != nil {
		_, _ = s.complex.FailClearDevComplexRoleBinding(ctx, core.FailComplexRoleBindingCommand{RoleBindingID: binding.ID, ReasonCode: unavailableReason, At: s.now().UTC()})
		return domain.SessionRecord{}, true, errComplexStopped
	}
	if _, err = s.complex.BindClearDevComplexRoleBinding(ctx, binding.ID, string(session.ID), s.now().UTC()); err != nil {
		return domain.SessionRecord{}, false, err
	}
	return session.SessionRecord, true, nil
}

func validateComplexSession(record domain.SessionRecord, binding core.ComplexRoleBinding, projectID string, kind domain.SessionKind, planning core.ComplexPlanningSnapshot) error {
	if record.ID == "" || string(record.ProjectID) != projectID || record.Kind != kind || !supportedControlledHarness(record.Harness) ||
		domain.NormalizeSessionMode(record.Mode) != domain.SessionModeChat || record.PermissionMode != domain.PermissionModeAuto ||
		strings.TrimSpace(record.Metadata.WorkspacePath) == "" {
		return errors.New("session does not satisfy the bound controlled Chat role")
	}
	if binding.Role != core.StandardRoleSteward && record.CreationIdempotencyKey != binding.SessionCreationIdempotencyKey {
		return errors.New("worker session creation key does not match its role binding")
	}
	for _, other := range planning.RoleBindings {
		if other.ID != binding.ID && other.Status == core.RoleBindingStatusBound && other.AOSessionID == string(record.ID) {
			return errors.New("one AO session cannot hold two ClearDev complex roles")
		}
	}
	return nil
}

func (s *Service) runComplexAgentStep(
	ctx context.Context,
	planning core.ComplexPlanningSnapshot,
	binding core.ComplexRoleBinding,
	requestID string,
	kind core.AgentStepKind,
	prompt string,
	reasons standardStepReasons,
	validate func([]byte) error,
) (standardMessage, bool, error) {
	step, found := core.ComplexAgentStepByRequest(planning, kind, requestID)
	changed := false
	if !found {
		stepID := s.newID()
		step = core.AgentStep{
			ID: stepID, RoleBindingID: binding.ID, Kind: kind, RequestID: requestID,
			ClientMessageID: "cleardev-complex-step-" + stepID, PromptSHA256: coreDigest([]byte(prompt)),
			SendStatus: core.AgentStepSendStatusPending, RequestedAt: s.now().UTC(),
		}
		var err error
		step, _, err = s.complex.CreateClearDevComplexAgentStep(ctx, step)
		if err != nil {
			return standardMessage{}, changed, err
		}
		changed = true
	}
	if step.PromptSHA256 != coreDigest([]byte(prompt)) {
		return standardMessage{}, changed, errors.New("saved ClearDev Agent step prompt does not match immutable facts")
	}
	if step.SendStatus == core.AgentStepSendStatusPending && reasons.ProjectID != "" {
		sessionKind := domain.KindWorker
		if binding.Role == core.StandardRoleSteward {
			sessionKind = domain.KindOrchestrator
		}
		if reasons.SessionKind != "" {
			sessionKind = reasons.SessionKind
		}
		record, available, readErr := s.ao.GetSession(ctx, domain.SessionID(binding.AOSessionID))
		if readErr != nil {
			return standardMessage{}, changed, readErr
		}
		if !available || record.IsTerminated ||
			s.validateComplexSession(ctx, record, binding, reasons.ProjectID, sessionKind, planning) != nil {
			if err := s.failComplexAgentStep(ctx, step, reasons.Unavailable); err != nil {
				return standardMessage{}, changed, err
			}
			return standardMessage{}, true, errComplexStopped
		}
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
			return standardMessage{}, changed, errors.New("settled Agent step no longer passes its frozen protocol")
		}
		message.StepID = step.ID
		return message, changed, nil
	}
	if step.SendStatus == core.AgentStepSendStatusPending {
		if err := s.relayAgentTurn(ctx, planning.Requirement.DevelopmentRequirementID, core.AgentStepCategoryComplexPlanning, step, binding.AOSessionID, prompt, step.ClientMessageID, core.AgentAttemptSent, s.now().UTC()); err != nil {
			if isMessageBudgetError(err) {
				return standardMessage{}, changed, err
			}
			if ctx.Err() != nil {
				return standardMessage{}, changed, ctx.Err()
			}
			if isAgentRecoveryDeferred(err) {
				return standardMessage{}, true, errComplexStopped
			}
			if failure, ok := ports.ChatFailureFromError(err); ok && (failure.Category == domain.AgentFailureDeliveryUnknown || failure.Retryable) {
				return standardMessage{}, true, errComplexStopped
			}
			_ = s.failComplexAgentStep(ctx, step, reasons.Unavailable)
			return standardMessage{}, true, errComplexStopped
		}
		sentAt := s.now().UTC()
		if _, err := s.complex.MarkClearDevComplexAgentStepSent(ctx, step.ID, sentAt); err != nil {
			return standardMessage{}, changed, err
		}
		step.SendStatus = core.AgentStepSendStatusSent
		step.SentAt = &sentAt
		changed = true
	}
	fail := parseCorrectionFail(s.failComplexAgentStep)
	if reasons.OnFailed != nil {
		fail = func(ctx context.Context, step core.AgentStep, reason core.ReasonCode) error {
			if err := s.failComplexAgentStep(ctx, step, reason); err != nil {
				return err
			}
			return reasons.OnFailed(ctx, step, reason)
		}
	}
	message, stopped, err := s.awaitValidAgentJSON(ctx, planning.Requirement.DevelopmentRequirementID, core.AgentStepCategoryComplexPlanning, binding.AOSessionID, step, prompt, validate, reasons, fail, errComplexStopped)
	if err != nil || stopped {
		return standardMessage{}, changed || stopped, err
	}
	now := s.now().UTC()
	step.SendStatus = core.AgentStepSendStatusSettled
	step.TurnID, step.FinalMessageID, step.FinalMessageText = message.TurnID, message.MessageID, message.Text
	step.MessageSHA256 = coreDigest([]byte(message.Text))
	step.CompletedAt = &now
	if _, err := s.complex.SettleClearDevComplexAgentStep(ctx, step); err != nil {
		return standardMessage{}, changed, err
	}
	message.StepID = step.ID
	return message, true, nil
}

func (s *Service) failComplexAgentStep(ctx context.Context, step core.AgentStep, reason core.ReasonCode) error {
	now := s.now().UTC()
	step.SendStatus, step.FailedAt, step.ReasonCode = core.AgentStepSendStatusFailed, &now, reason
	_, err := s.complex.SettleClearDevComplexAgentStep(ctx, step)
	return err
}

func (s *Service) scheduleComplexAfterVersionConfirm(ctx context.Context, versionID string) {
	requirementID := s.complexRequirementIDForVersion(ctx, versionID)
	if requirementID == "" {
		return
	}
	s.scheduleComplexFlow(requirementID)
}

func (s *Service) complexRequirementIDForVersion(ctx context.Context, versionID string) string {
	if s.complex == nil {
		return ""
	}
	ids, err := s.complex.ListClearDevRunnableComplexFlows(ctx)
	if err != nil {
		return ""
	}
	for _, id := range ids {
		snapshot, ok, err := s.facts.GetClearDevRequirement(ctx, id)
		if err != nil || !ok {
			continue
		}
		for _, version := range snapshot.RequirementVersions {
			if version.ID == versionID {
				return id
			}
		}
	}
	return ""
}

func buildComplexPlanningView(snapshot core.ComplexPlanningSnapshot, versions []core.RequirementVersion) *ComplexPlanningView {
	phase, reason := core.DeriveComplexPlanningPhase(snapshot, versions)
	view := &ComplexPlanningView{
		Phase: phase, ReasonCode: reason, Requirement: snapshot.Requirement,
		RoleBindings: snapshot.RoleBindings, AgentSteps: snapshot.AgentSteps,
		CompilationRequests: snapshot.CompilationRequests, Questions: snapshot.Questions,
		Answers: snapshot.Answers, Compilations: snapshot.Compilations, IDMaps: snapshot.IDMaps,
		Plans: snapshot.Plans, Reviews: snapshot.Reviews, Validations: snapshot.Validations,
	}
	if version := confirmedComplexRequirementVersion(versions); version != nil {
		var latest core.ComplexEngineeringPlan
		for _, plan := range snapshot.Plans {
			if plan.RequirementVersionID == version.ID && plan.Version > latest.Version {
				latest = plan
			}
		}
		if clarification, ok := core.PlannerClarificationForPlan(latest); ok {
			view.ProductClarification = &clarification
		}
	}
	if view.RoleBindings == nil {
		view.RoleBindings = []core.ComplexRoleBinding{}
	}
	if view.AgentSteps == nil {
		view.AgentSteps = []core.AgentStep{}
	}
	if view.CompilationRequests == nil {
		view.CompilationRequests = []core.ComplexCompilationRequest{}
	}
	if view.Questions == nil {
		view.Questions = []core.ComplexClarificationQuestion{}
	}
	if view.Answers == nil {
		view.Answers = []core.ComplexClarificationAnswer{}
	}
	if view.Compilations == nil {
		view.Compilations = []core.ComplexCompilation{}
	}
	if view.IDMaps == nil {
		view.IDMaps = []core.ComplexIDMap{}
	}
	if view.Plans == nil {
		view.Plans = []core.ComplexEngineeringPlan{}
	}
	if view.Reviews == nil {
		view.Reviews = []core.ComplexPlanReview{}
	}
	if view.Validations == nil {
		view.Validations = []core.ComplexPlanValidation{}
	}
	return view
}

func confirmedComplexRequirementVersion(versions []core.RequirementVersion) *core.RequirementVersion {
	for i := range versions {
		if versions[i].Status == core.RequirementVersionStatusConfirmed {
			return &versions[i]
		}
	}
	return nil
}

func complexSpawnKey(scope string, role core.StandardRole, id string) string {
	return "cleardev-complex:" + strings.ToLower(string(role)) + ":" + scope + ":" + id
}

func complexBranch(role, id string) string {
	return standardBranch(role, id)
}

func nextComplexCompilationRequestID(snapshot core.ComplexPlanningSnapshot, round int, newID func() string) string {
	_ = round
	used := map[string]struct{}{}
	for _, request := range snapshot.CompilationRequests {
		used[request.ID] = struct{}{}
	}
	for _, step := range snapshot.AgentSteps {
		if step.Kind != core.ComplexAgentStepCompilation {
			continue
		}
		if _, exists := used[step.RequestID]; !exists {
			return step.RequestID
		}
	}
	return newID()
}

// nextComplexPlanningRequestID derives one deterministic identity per planning
// round of a confirmed requirement version. Restart, recovery or a duplicate
// advance computes the same request id, so the store's unique constraint keeps
// exactly one step and one actual send.
func nextComplexPlanningRequestID(snapshot core.ComplexPlanningSnapshot, versionID string) string {
	round := 1
	for _, plan := range snapshot.Plans {
		if plan.RequirementVersionID == versionID {
			round++
		}
	}
	return fmt.Sprintf("cleardev-complex-plan-%s-%d", versionID, round)
}

func nextComplexReviewRequestID(snapshot core.ComplexPlanningSnapshot, planID string, newID func() string) string {
	if review, ok := reviewForComplexPlan(snapshot, planID); ok {
		return review.ReviewRequestID
	}
	for _, step := range snapshot.AgentSteps {
		if step.Kind == core.ComplexAgentStepPlanReview && step.RequestID != "" {
			used := false
			for _, review := range snapshot.Reviews {
				if review.ReviewRequestID == step.RequestID {
					used = true
					break
				}
			}
			if !used {
				return step.RequestID
			}
		}
	}
	return newID()
}

func compilationRequestExists(snapshot core.ComplexPlanningSnapshot, requestID string) bool {
	for _, request := range snapshot.CompilationRequests {
		if request.ID == requestID {
			return true
		}
	}
	return false
}

func planExistsForRequest(snapshot core.ComplexPlanningSnapshot, requestID string) bool {
	for _, plan := range snapshot.Plans {
		if plan.PlanningRequestID == requestID {
			return true
		}
	}
	return false
}

func latestUnreviewedComplexPlan(snapshot core.ComplexPlanningSnapshot, versionID string) (core.ComplexEngineeringPlan, bool) {
	var latest core.ComplexEngineeringPlan
	found := false
	for _, plan := range snapshot.Plans {
		if versionID != "" && plan.RequirementVersionID != versionID {
			continue
		}
		if _, reviewed := reviewForComplexPlan(snapshot, plan.ID); reviewed {
			continue
		}
		if !found || plan.Version > latest.Version {
			latest = plan
			found = true
		}
	}
	return latest, found
}

func reviewForComplexPlan(snapshot core.ComplexPlanningSnapshot, planID string) (core.ComplexPlanReview, bool) {
	for _, review := range snapshot.Reviews {
		if review.PlanID == planID {
			return review, true
		}
	}
	return core.ComplexPlanReview{}, false
}
