package cleardev

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
)

// Service implements deterministic ClearDev control actions.
type Service struct {
	facts                       FactStore
	benchmarkFacts              BenchmarkFactStore
	standard                    StandardFactStore
	complex                     ComplexFactStore
	complexExecution            ComplexExecutionFactStore
	finalReviews                RequirementFinalReviewFactStore
	direction                   DirectionFactStore
	humanDecisions              HumanDecisionStore
	desktopRunID                string
	desktopChannelConnected     func() bool
	progress                    ProgressExplanationStore
	corrections                 ParseCorrectionStore
	attempts                    AgentAttemptStore
	preflights                  ControlledPreflightStore
	preflightChecker            ControlledPreflightChecker
	ao                          AOReader
	workspace                   ports.WorkspaceObserver
	human                       HumanDecisionAuthorizer
	sessions                    StandardSessionService
	recoverAgentSession         AgentSessionRecovery
	restoreOriginalAgentSession func(context.Context, domain.SessionID) (string, error)
	chat                        StandardChatService
	inspector                   ports.ClearDevCandidateInspector
	checks                      ports.ClearDevCheckRunner
	resultPreview               ResultPreviewManager
	benchmarkManifest           *core.BenchmarkBackendManifest
	backgroundContext           context.Context
	runBackground               func(func())
	autoAdvanceComplexPlans     bool
	plannerTaskContracts        bool
	automaticFailureRouting     bool
	plannerRuntimeCoordination  bool
	boundedMailAttempts         bool
	stepTimeout                 time.Duration
	pollInterval                time.Duration
	logger                      *slog.Logger
	newID                       func() string
	now                         func() time.Time
	standardMu                  sync.Mutex
	standardRunning             map[string]bool
	complexMu                   sync.Mutex
	complexRunning              map[string]bool
	complexWake                 map[string]bool
	directionMu                 sync.Mutex
	directionRunning            map[string]bool
	directionWake               map[string]bool
	complexExecutionMu          sync.Mutex
	complexExecutionRunning     map[string]bool
	complexExecutionWake        map[string]bool
	progressMu                  sync.Mutex
	progressRunning             map[string]bool
	afterComplexIdle            func(string)
	afterOccupy                 func()
	afterObserve                func()
	betweenTasks                func()
}

// New constructs a ClearDev service. Missing optional authority/observer
// dependencies fail closed in the actions that need them.
func New(deps Deps) *Service {
	if deps.NewID == nil {
		deps.NewID = uuid.NewString
	}
	if deps.Clock == nil {
		deps.Clock = func() time.Time { return time.Now().UTC() }
	}
	if deps.BackgroundContext == nil {
		deps.BackgroundContext = context.Background()
	}
	if deps.RunBackground == nil {
		deps.RunBackground = func(run func()) { go run() }
	}
	if deps.StepTimeout <= 0 {
		deps.StepTimeout = 15 * time.Minute
	}
	if deps.PollInterval <= 0 {
		deps.PollInterval = 250 * time.Millisecond
	}
	if deps.Logger == nil {
		deps.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{
		desktopRunID: deps.DesktopRunID, desktopChannelConnected: deps.DesktopChannelConnected,
		facts: deps.Facts, benchmarkFacts: deps.BenchmarkFacts, standard: deps.StandardFacts, complex: deps.ComplexFacts, complexExecution: deps.ComplexExecutionFacts, direction: deps.DirectionFacts, humanDecisions: deps.HumanDecisions, progress: deps.ProgressExplanations, corrections: deps.ParseCorrections, attempts: deps.AgentAttempts, preflights: deps.ControlledPreflights, preflightChecker: deps.ControlledPreflightChecker, ao: deps.AO,
		workspace: deps.Workspace, human: deps.Human, sessions: deps.Sessions,
		finalReviews:                deps.RequirementFinalReviews,
		recoverAgentSession:         deps.RecoverAgentSession,
		restoreOriginalAgentSession: deps.RestoreOriginalAgentSession,
		chat:                        deps.Chat, inspector: deps.Inspector, checks: deps.Checks,
		benchmarkManifest: deps.BenchmarkManifest,
		resultPreview:     deps.ResultPreview,
		backgroundContext: deps.BackgroundContext, runBackground: deps.RunBackground,
		autoAdvanceComplexPlans:    deps.AutoAdvanceComplexPlans,
		plannerTaskContracts:       deps.PlannerTaskContracts,
		plannerRuntimeCoordination: deps.PlannerRuntimeCoordination,
		automaticFailureRouting:    deps.AutomaticFailureRouting,
		boundedMailAttempts:        deps.BoundedMailAttempts,
		stepTimeout:                deps.StepTimeout, pollInterval: deps.PollInterval,
		logger: deps.Logger,
		newID:  deps.NewID, now: deps.Clock, standardRunning: make(map[string]bool), complexRunning: make(map[string]bool), complexWake: make(map[string]bool),
		directionRunning: make(map[string]bool), directionWake: make(map[string]bool),
		complexExecutionRunning: make(map[string]bool), complexExecutionWake: make(map[string]bool),
		progressRunning: make(map[string]bool),
	}
}

// CreateRequirement validates the public JSON contract and atomically creates
// only the requirement container and its first DRAFT version.
func (s *Service) CreateRequirement(ctx context.Context, input CreateRequirementInput) (RequirementView, error) {
	if s.facts == nil || s.ao == nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_UNAVAILABLE", "ClearDev service is not fully configured")
	}
	input.AOProjectID = strings.TrimSpace(input.AOProjectID)
	input.Name = strings.TrimSpace(input.Name)
	if input.AOProjectID == "" {
		return RequirementView{}, apierr.Invalid("AO_PROJECT_ID_REQUIRED", "aoProjectId is required", nil)
	}
	if input.Name == "" {
		return RequirementView{}, apierr.Invalid("NAME_REQUIRED", "name is required", nil)
	}
	if strings.TrimSpace(input.RequirementText) == "" {
		return RequirementView{}, apierr.Invalid("REQUIREMENT_TEXT_REQUIRED", "requirementText is required", nil)
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
	digest := sha256.Sum256([]byte(input.RequirementText))
	initial := core.InitialRequirement{
		Requirement: core.DevelopmentRequirement{
			ID: requirementID, AOProjectID: input.AOProjectID, Name: input.Name,
			CreatedAt: now, UpdatedAt: now,
		},
		Version: core.RequirementVersion{
			ID: s.newID(), DevelopmentRequirementID: requirementID, Version: 1,
			RequirementText: input.RequirementText, SHA256: fmt.Sprintf("%x", digest),
			Status: core.RequirementVersionStatusDraft, CreatedAt: now,
		},
		BenchmarkBinding: benchmarkBinding,
	}
	if err := s.facts.CreateClearDevRequirement(ctx, initial); err != nil {
		return RequirementView{}, mapStoreError(err, "CREATE_REQUIREMENT_FAILED")
	}
	return s.GetRequirement(ctx, requirementID) //返回需求，就是RequirementView类
}

// GetRequirement returns durable facts plus recomputed evidence and progress.
func (s *Service) GetRequirement(ctx context.Context, id string) (RequirementView, error) {
	return s.getProgressRequirement(ctx, id, s.now().UTC(), true)
}

func (s *Service) readProgressRequirement(ctx context.Context, id string, now time.Time) (RequirementView, error) {
	if s.facts == nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_UNAVAILABLE", "ClearDev service is not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return RequirementView{}, apierr.Invalid("CLEARDEV_REQUIREMENT_ID_REQUIRED", "ClearDev requirement id is required", nil)
	}
	snapshot, ok, err := s.facts.GetClearDevRequirement(ctx, id)
	if err != nil {
		return RequirementView{}, apierr.Internal("CLEARDEV_REQUIREMENT_READ_FAILED", "Could not read the ClearDev requirement")
	}
	if !ok {
		return RequirementView{}, apierr.NotFound("CLEARDEV_REQUIREMENT_NOT_FOUND", "ClearDev requirement was not found")
	}
	view := buildRequirementView(snapshot, now)
	facts := core.TrustedProgressFacts{Snapshot: snapshot, Now: now}
	if s.standard != nil {
		flow, exists, flowErr := s.standard.GetClearDevStandardFlow(ctx, id)
		if flowErr != nil {
			return RequirementView{}, apierr.Internal("CLEARDEV_STANDARD_FLOW_READ_FAILED", "Could not read the ClearDev STANDARD flow")
		}
		if exists && len(flow.RoleBindings) != 0 {
			view.StandardFlow = &flow
			status := deriveStandardFlowStatus(snapshot, flow, view.OverallProgress)
			view.StandardFlowStatus = &status
			facts.StandardFlow = &flow
		}
	}
	if s.complex != nil {
		planning, exists, planningErr := s.complex.GetClearDevComplexPlanning(ctx, id)
		if planningErr != nil {
			return RequirementView{}, apierr.Internal("CLEARDEV_COMPLEX_PLANNING_READ_FAILED", "Could not read the ClearDev complex planning facts")
		}
		if exists {
			view.ComplexPlanning = buildComplexPlanningView(planning, snapshot.RequirementVersions)
			s.addPlannerClarificationView(ctx, &view, planning)
			facts.ComplexPlanning = &planning
		}
	}
	if s.direction != nil {
		change, exists, changeErr := s.direction.GetClearDevDirectionChange(ctx, id)
		if changeErr != nil {
			return RequirementView{}, apierr.Internal("CLEARDEV_DIRECTION_READ_FAILED", "Could not read the ClearDev direction-change facts")
		}
		if exists {
			view.DirectionChange = buildDirectionChangeView(change, snapshot.RequirementVersions, view.ComplexPlanning)
			facts.DirectionChange = &change
		}
	}
	if s.complexExecution != nil {
		execution, exists, executionErr := s.complexExecution.GetClearDevComplexExecution(ctx, id)
		if executionErr != nil {
			return RequirementView{}, apierr.Internal("CLEARDEV_COMPLEX_EXECUTION_READ_FAILED", "Could not read the ClearDev complex execution facts")
		}
		if exists {
			execution.Phase, execution.PhaseReason = core.DeriveComplexExecutionPhase(execution)
			execution.MissingEvidence = core.DeriveComplexExecutionMissingEvidence(execution)
			view.ComplexExecution = &execution
			facts.ComplexExecution = &execution
		}
		quick, quickExists, quickErr := s.complexExecution.GetClearDevComplexQuickExecution(ctx, id)
		if quickErr != nil {
			return RequirementView{}, apierr.Internal("CLEARDEV_COMPLEX_QUICK_READ_FAILED", "Could not read the ClearDev quick execution facts")
		}
		if quickExists {
			quick.Phase, quick.PhaseReason = core.DeriveComplexQuickPhase(quick)
			quick.MissingEvidence = core.DeriveComplexQuickMissingEvidence(quick)
			view.QuickExecution = &quick
			facts.QuickExecution = &quick
		}
	}
	if s.preflights != nil {
		record, found, preflightErr := s.preflights.GetLatestClearDevControlledPreflight(ctx, id)
		if preflightErr != nil {
			return RequirementView{}, apierr.Internal("CLEARDEV_PREFLIGHT_READ_FAILED", "Could not read the latest controlled preflight")
		}
		if found {
			pub := record.PublicView()
			view.LatestControlledPreflight = &pub
			facts.LatestControlledPreflight = &pub
		}
	}
	if s.attempts != nil {
		budget, budgetErr := s.attempts.GetClearDevMessageBudget(ctx, id)
		if budgetErr != nil {
			return RequirementView{}, budgetErr
		}
		view.MessageBudget = &budget
	}
	facts.Controlled, err = s.readControlledProgress(ctx, id, facts, view.MessageBudget)
	if err != nil {
		return RequirementView{}, err
	}
	selected := core.SelectTrustedProgressFacts(facts)
	if selected.ComplexExecution != nil {
		phase, reason, missing := core.CurrentExecutionProgress(*selected.ComplexExecution, facts.Controlled, now)
		view.ComplexExecution.Phase, view.ComplexExecution.PhaseReason, view.ComplexExecution.MissingEvidence = phase, reason, missing
	}
	if selected.QuickExecution != nil {
		view.QuickExecution.Phase, view.QuickExecution.PhaseReason, view.QuickExecution.MissingEvidence = core.CurrentQuickProgress(*selected.QuickExecution)
	}
	_, err = s.attachTrustedProgress(ctx, &view, facts)
	if err != nil {
		return RequirementView{}, err
	}
	return view, nil
}

func buildRequirementView(snapshot core.RequirementSnapshot, now time.Time) RequirementView {
	view := RequirementView{
		MessageBudget: &core.MessageBudgetView{Steps: []core.MessageBudgetUsage{}, Roles: []core.MessageBudgetUsage{}, UnknownReason: "MESSAGE_BUDGET_STORE_UNAVAILABLE"},
		Requirement:   snapshot.Requirement, RequirementVersions: snapshot.RequirementVersions,
		IntegrationCandidates: snapshot.IntegrationCandidates, Events: snapshot.Events,
		OverallProgress: core.DeriveOverallProgress(snapshot, now),
	}
	for _, record := range snapshot.Evidence {
		if record.SubjectType == core.SubjectDevelopmentRequirement {
			view.RequirementEvidence = append(view.RequirementEvidence, record)
		}
	}
	for _, task := range snapshot.DevelopmentTasks {
		taskView := DevelopmentTaskView{DevelopmentTask: task}
		for _, permission := range snapshot.PermissionVersions {
			if permission.DevelopmentTaskID == task.ID {
				taskView.PermissionVersions = append(taskView.PermissionVersions, permission)
			}
		}
		for _, check := range snapshot.RequiredChecks {
			if check.DevelopmentTaskID == task.ID {
				taskView.RequiredChecks = append(taskView.RequiredChecks, check)
			}
		}
		for _, candidate := range snapshot.Candidates {
			if candidate.DevelopmentTaskID != task.ID {
				continue
			}
			taskView.Candidates = append(taskView.Candidates, candidate)
			if taskView.CurrentCandidate == nil || candidate.Sequence > taskView.CurrentCandidate.Sequence {
				copy := candidate
				taskView.CurrentCandidate = &copy
			}
		}
		if taskView.CurrentCandidate != nil && !candidateIsCurrentRound(snapshot.Events, task.ID, taskView.CurrentCandidate.ID) {
			taskView.CurrentCandidate = nil
		}
		for _, record := range snapshot.Evidence {
			if record.SubjectType == core.SubjectDevelopmentTask && record.SubjectID == task.ID {
				taskView.Evidence = append(taskView.Evidence, record)
			}
		}
		taskView.EffectiveEvidence, taskView.MissingEvidence = effectiveDevelopmentTaskEvidence(taskView, now)
		normalizeDevelopmentTaskView(&taskView)
		view.DevelopmentTasks = append(view.DevelopmentTasks, taskView)
	}
	normalizeRequirementView(&view)
	return view
}

func candidateIsCurrentRound(events []core.RequirementEvent, taskID, candidateID string) bool {
	var candidateSequence, restartSequence int64
	for _, event := range events {
		if event.Outcome != core.EventAccepted {
			continue
		}
		switch {
		case event.Action == core.ActionRegisterCandidate && event.SubjectType == core.SubjectCandidate && event.SubjectID == candidateID:
			if event.Sequence > candidateSequence {
				candidateSequence = event.Sequence
			}
		case event.Action == core.ActionRestartDevelopmentTask && event.SubjectType == core.SubjectDevelopmentTask && event.SubjectID == taskID:
			if event.Sequence > restartSequence {
				restartSequence = event.Sequence
			}
		}
	}
	return candidateSequence > restartSequence
}

func effectiveDevelopmentTaskEvidence(view DevelopmentTaskView, now time.Time) ([]core.EvidenceRecord, []string) {
	if view.CurrentCandidate == nil {
		return []core.EvidenceRecord{}, append([]string{"candidate"}, evidenceRequirementNames(view.DevelopmentTask.Mode, view.RequiredChecks)...)
	}
	latest := make(map[string]core.EvidenceRecord)
	for _, record := range view.Evidence {
		if record.CandidateCommitID != view.CurrentCandidate.ID || record.CommitSHA != view.CurrentCandidate.CommitSHA {
			continue
		}
		key := string(record.Kind) + "\x00" + record.Key
		current, exists := latest[key]
		if !exists || record.Sequence > current.Sequence {
			latest[key] = record
		}
	}
	keys := make([]string, 0, len(latest))
	for key := range latest {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	effective := make([]core.EvidenceRecord, 0, len(keys))
	for _, key := range keys {
		record := latest[key]
		if record.ExpiresAt == nil || now.Before(*record.ExpiresAt) {
			effective = append(effective, record)
		}
	}
	missing := make([]string, 0)
	for _, requirement := range evidenceRequirementNames(view.DevelopmentTask.Mode, view.RequiredChecks) {
		kind, key := requirementEvidenceKey(requirement)
		record, ok := latest[kind+"\x00"+key]
		if !ok || (record.ExpiresAt != nil && !now.Before(*record.ExpiresAt)) || record.Result != core.EvidenceResultPass {
			missing = append(missing, requirement)
		}
	}
	return effective, missing
}

func evidenceRequirementNames(mode core.WorkMode, checks []core.RequiredCheck) []string {
	requirements := []string{"scope"}
	for _, check := range checks {
		requirements = append(requirements, "required_check:"+check.Name)
	}
	requirements = append(requirements, "integration")
	if mode == core.WorkModeStandard {
		requirements = append(requirements, "review")
	}
	return requirements
}

func requirementEvidenceKey(requirement string) (string, string) {
	if strings.HasPrefix(requirement, "required_check:") {
		return string(core.EvidenceKindRequiredCheck), strings.TrimPrefix(requirement, "required_check:")
	}
	switch requirement {
	case "scope":
		return string(core.EvidenceKindScope), ""
	case "integration":
		return string(core.EvidenceKindIntegration), ""
	case "review":
		return string(core.EvidenceKindReview), ""
	default:
		return "", ""
	}
}

func normalizeRequirementView(view *RequirementView) {
	if view.RequirementVersions == nil {
		view.RequirementVersions = []core.RequirementVersion{}
	}
	if view.DevelopmentTasks == nil {
		view.DevelopmentTasks = []DevelopmentTaskView{}
	}
	if view.IntegrationCandidates == nil {
		view.IntegrationCandidates = []core.IntegrationCandidate{}
	}
	if view.RequirementEvidence == nil {
		view.RequirementEvidence = []core.EvidenceRecord{}
	}
	if view.OverallProgress.MissingEvidence == nil {
		view.OverallProgress.MissingEvidence = []core.MissingEvidence{}
	}
	if view.Events == nil {
		view.Events = []core.RequirementEvent{}
	}
	if view.AgentStepAttempts == nil {
		view.AgentStepAttempts = []core.AgentStepAttemptView{}
	}
}

func normalizeDevelopmentTaskView(view *DevelopmentTaskView) {
	if view.PermissionVersions == nil {
		view.PermissionVersions = []core.PermissionVersion{}
	}
	if view.RequiredChecks == nil {
		view.RequiredChecks = []core.RequiredCheck{}
	}
	if view.Candidates == nil {
		view.Candidates = []core.CandidateCommit{}
	}
	if view.Evidence == nil {
		view.Evidence = []core.EvidenceRecord{}
	}
	if view.EffectiveEvidence == nil {
		view.EffectiveEvidence = []core.EvidenceRecord{}
	}
	if view.MissingEvidence == nil {
		view.MissingEvidence = []string{}
	}
}

func cloneStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string(nil), values...)
}

func mapStoreError(err error, fallbackCode string) error {
	var rule *core.RuleError
	if errors.As(err, &rule) {
		return apierr.Conflict(string(rule.Code), rule.Error(), nil)
	}
	return apierr.Internal(fallbackCode, "ClearDev storage operation failed")
}
