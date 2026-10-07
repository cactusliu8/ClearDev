package cleardev_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/cleardevtest"
	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	cleardevsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/cleardev"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/sqlitetest"
)

func TestBenchmarkFacilityG3G4OfflineFixtureCompletesThroughHTTP(t *testing.T) {
	for _, tc := range []struct {
		group    string
		policy   core.BenchmarkModePolicy
		mode     core.WorkMode
		builders int
	}{
		{group: core.BenchmarkGroupG3, policy: core.BenchmarkModeStandardOnly, mode: core.WorkModeStandard, builders: 1},
		{group: core.BenchmarkGroupG4, policy: core.BenchmarkModeDynamic, mode: core.WorkModeParallel, builders: 2},
	} {
		t.Run(tc.group, func(t *testing.T) {
			store, harness, service, prep, dataDir := newBenchmarkParallelFixtureService(t, tc.group, tc.policy)
			server := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, httpd.APIDeps{ClearDev: service}, httpd.ControlDeps{}))
			defer server.Close()
			var view cleardevsvc.RequirementView
			var runID string
			requests := 1
			for ; requests <= 80; requests++ {
				request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/cleardev/requirements/"+prep.RequirementID+"/execution-runs", nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != http.StatusAccepted {
					raw, _ := io.ReadAll(response.Body)
					_ = response.Body.Close()
					t.Fatalf("execution status=%d body=%s", response.StatusCode, raw)
				}
				view = cleardevsvc.RequirementView{}
				if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
					_ = response.Body.Close()
					t.Fatal(err)
				}
				_ = response.Body.Close()
				if view.ComplexExecution == nil {
					t.Fatal("HTTP execution response lost complex execution")
				}
				if runID == "" {
					runID = view.ComplexExecution.Run.ID
				} else if view.ComplexExecution.Run.ID != runID {
					t.Fatalf("idempotent execution replay changed run %s -> %s", runID, view.ComplexExecution.Run.ID)
				}
				if view.ComplexExecution.Phase == core.ComplexExecutionCompleted {
					break
				}
			}
			if view.ComplexExecution == nil || view.ComplexExecution.Phase != core.ComplexExecutionCompleted {
				t.Fatalf("offline fixture did not complete after %d idempotent HTTP requests: %#v", requests-1, view.ComplexExecution)
			}
			if requests != 1 {
				t.Fatalf("offline fixture needed %d HTTP start requests, want one bounded production-service advance", requests)
			}
			if view.ComplexExecution.Run.Mode != tc.mode || view.ComplexExecution.Run.FixedBuilderCount != tc.builders {
				t.Fatalf("mode=%s builders=%d", view.ComplexExecution.Run.Mode, view.ComplexExecution.Run.FixedBuilderCount)
			}
			if len(view.ComplexExecution.Tasks) != 3 || view.ComplexExecution.Integration == nil {
				t.Fatalf("candidate/integration facts missing: tasks=%d integration=%#v", len(view.ComplexExecution.Tasks), view.ComplexExecution.Integration)
			}
			builders, reviewers := 0, 0
			for _, binding := range view.ComplexExecution.RoleBindings {
				switch binding.Role {
				case core.StandardRoleBuilder:
					builders++
				case core.StandardRoleReviewer:
					reviewers++
				}
			}
			if builders != tc.builders || reviewers != len(view.ComplexExecution.Tasks) {
				t.Fatalf("durable role counts builders=%d reviewers=%d", builders, reviewers)
			}
			for _, spec := range view.ComplexExecution.CheckSpecs {
				if spec.Kind == core.CandidateCheckScope {
					continue
				}
				if !strings.HasPrefix(spec.CheckID, "CHK-DEV-") {
					t.Fatalf("execution carried non-benchmark check %#v", spec)
				}
			}
			harness.mu.Lock()
			spawns := append([]portsSpawnRecord(nil), benchmarkSpawnRecords(harness.spawnConfigs)...)
			relays := len(harness.relays)
			harness.mu.Unlock()
			if len(spawns) == 0 || relays == 0 {
				t.Fatalf("offline fixture did not execute real service session/message actions: spawns=%d relays=%d", len(spawns), relays)
			}
			snapshot, found, err := store.GetClearDevComplexExecution(context.Background(), prep.RequirementID)
			if err != nil || !found || snapshot.Run.CompletedAt == nil || snapshot.Integration == nil {
				t.Fatalf("durable completion found=%v err=%v snapshot=%#v", found, err, snapshot)
			}
			assertBenchmarkSQLiteRejectsWrongCheckArgv(t, dataDir, snapshot)
		})
	}
}

// portsSpawnRecord deliberately records only facts asserted by this test; it
// avoids coupling the benchmark check to unrelated SpawnConfig fields.
type portsSpawnRecord struct{ role string }

func benchmarkSpawnRecords(configs []ports.SpawnConfig) []portsSpawnRecord {
	out := make([]portsSpawnRecord, 0, len(configs))
	for _, cfg := range configs {
		out = append(out, portsSpawnRecord{role: cfg.DisplayName})
	}
	return out
}

func newBenchmarkParallelFixtureService(t *testing.T, group string, policy core.BenchmarkModePolicy) (*sqlite.Store, *parallelAgentHarness, *cleardevsvc.Service, cleardevtest.ComplexParallelPrep, string) {
	t.Helper()
	dataDir := t.TempDir()
	store := sqlitetest.MustOpenAt(t, dataDir)
	repo := initParallelPrepRepo(t)
	prep := cleardevtest.SeedComplexParallelV2(t, store, "ao-benchmark-"+strings.ToLower(group), repo, dataDir)
	manifest := benchmarkServiceManifestFixture(t, repo, group, policy)
	binding, err := core.NewBenchmarkBinding(manifest, prep.RequirementID, "ao-benchmark-"+strings.ToLower(group), time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	insertBenchmarkServiceBinding(t, dataDir, binding)
	installBenchmarkPlan(t, store, prep, group)
	harness := newParallelAgentHarness(store, false)
	ids := &parallelTestIDs{next: 4000}
	var clockMu sync.Mutex
	tick := 0
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		tick++
		return time.Date(2026, 9, 7, 12, 5, 0, 0, time.UTC).Add(time.Duration(tick) * time.Second)
	}
	service := cleardevsvc.New(cleardevsvc.Deps{
		Facts: store, BenchmarkFacts: store, BenchmarkManifest: &manifest,
		StandardFacts: store, ComplexFacts: store, ComplexExecutionFacts: store,
		ProgressExplanations: store, ParseCorrections: store, AgentAttempts: store, Workspace: &workspaceObserver{},
		RecoverAgentSession: func(context.Context, domain.SessionID) error { return nil },
		DirectionFacts:      store, HumanDecisions: store, AO: store,
		Sessions: harness, Chat: harness, Inspector: harness, Checks: harness,
		ControlledPreflights: store, ControlledPreflightChecker: passingProgressPreflight{},
		Human: allowParallelHuman{}, BackgroundContext: context.Background(), RunBackground: func(run func()) { run() },
		StepTimeout: time.Minute, PollInterval: time.Millisecond, NewID: ids.New, Clock: clock,
	})
	return store, harness, service, prep, dataDir
}

func assertBenchmarkSQLiteRejectsWrongCheckArgv(t *testing.T, dataDir string, snapshot core.ComplexExecutionSnapshot) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`INSERT INTO cleardev_complex_execution_check_specs (
		id, execution_run_id, task_mapping_id, required_check_id, check_kind, check_name,
		check_spec_sha256, argv_json, timeout_seconds, created_at
	) VALUES (?, ?, NULL, NULL, 'INTEGRATION', 'CHK-DEV-API', ?, ?, 60, ?)`,
		"benchmark-invalid-check-"+snapshot.Run.ID, snapshot.Run.ID,
		"82c13ff00269433076928c6cf744cec2cee363fc06763f0b45e74e3890a09586", `["node","--test"]`, time.Now().UTC())
	if err == nil {
		t.Fatal("SQLite accepted benchmark check id with non-frozen argv")
	}
}

func installBenchmarkPlan(t *testing.T, store *sqlite.Store, prep cleardevtest.ComplexParallelPrep, group string) {
	t.Helper()
	ctx := context.Background()
	planning, found, err := store.GetClearDevComplexPlanning(ctx, prep.RequirementID)
	if err != nil || !found {
		t.Fatalf("read planning: found=%v err=%v", found, err)
	}
	var source core.ComplexEngineeringPlan
	var planner, steward core.ComplexRoleBinding
	maxVersion := int64(0)
	for _, plan := range planning.Plans {
		if plan.Version > maxVersion {
			maxVersion = plan.Version
		}
		if plan.ID == prep.V2PlanID {
			source = plan
		}
	}
	for _, binding := range planning.RoleBindings {
		if binding.Status != core.RoleBindingStatusBound {
			continue
		}
		switch binding.Role {
		case core.StandardRoleEngineeringPlanner:
			planner = binding
		case core.StandardRoleSteward:
			steward = binding
		}
	}
	if source.ID == "" || planner.ID == "" || steward.ID == "" {
		t.Fatalf("benchmark plan fixture source/planner/steward missing")
	}
	var parsed core.ComplexEngineeringPlanResult
	if err := json.Unmarshal([]byte(source.PlanJSON), &parsed); err != nil {
		t.Fatal(err)
	}
	for index := range parsed.Tasks {
		parsed.Tasks[index].RequiredCheckIDs = []string{"CHK-DEV-ALL"}
	}
	parsed.IntegrationCheckIDs = []string{"CHK-DEV-ALL"}
	planRaw, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	planSHA := digestBenchmarkBytes(planRaw)
	now := time.Date(2026, 9, 7, 12, 1, 0, 0, time.UTC)
	planStep := settledBenchmarkPlanningStep(t, store, planner, "benchmark-plan-step-"+group, core.ComplexAgentStepEngineeringPlan, "benchmark-plan-request-"+group, string(planRaw), now)
	plan := core.ComplexEngineeringPlan{
		ID: "benchmark-plan-" + group, PlanningRequestID: planStep.RequestID, DevelopmentRequirementID: prep.RequirementID,
		RequirementVersionID: source.RequirementVersionID, RequirementSHA256: source.RequirementSHA256, CompilationSHA256: source.CompilationSHA256,
		Version: maxVersion + 1, PlannerRoleBindingID: planner.ID, AgentStepID: planStep.ID, TurnID: planStep.TurnID, FinalMessageID: planStep.FinalMessageID,
		PlanJSON: string(planRaw), PlanSHA256: planSHA, CreatedAt: now,
	}
	if err := store.CreateClearDevComplexEngineeringPlan(ctx, core.CreateComplexPlanCommand{Plan: plan}); err != nil {
		t.Fatalf("save benchmark plan: %v", err)
	}
	reviewText := `{"schemaVersion":1,"kind":"COMPLEX_PLAN_REVIEW","verdict":"APPROVED","reasonCode":"PLAN_ACCEPTABLE","summary":"offline fixture approved","findings":[]}`
	reviewStep := settledBenchmarkPlanningStep(t, store, steward, "benchmark-review-step-"+group, core.ComplexAgentStepPlanReview, "benchmark-review-request-"+group, reviewText, now.Add(time.Second))
	review := core.ComplexPlanReview{
		ID: "benchmark-review-" + group, PlanID: plan.ID, StewardRoleBindingID: steward.ID, AgentStepID: reviewStep.ID,
		ReviewRequestID: reviewStep.RequestID, Verdict: core.PlanReviewApproved, ReasonCode: core.ReasonCode("PLAN_ACCEPTABLE"),
		Summary: "offline fixture approved", FindingsJSON: "[]", PlanSHA256: planSHA, TurnID: reviewStep.TurnID, FinalMessageID: reviewStep.FinalMessageID, CreatedAt: now.Add(time.Second),
	}
	if err := store.RecordClearDevComplexPlanReview(ctx, core.RecordComplexPlanReviewCommand{Review: review}); err != nil {
		t.Fatalf("save benchmark plan review: %v", err)
	}
}

func settledBenchmarkPlanningStep(t *testing.T, store *sqlite.Store, binding core.ComplexRoleBinding, id string, kind core.AgentStepKind, requestID, finalText string, at time.Time) core.AgentStep {
	t.Helper()
	ctx := context.Background()
	step := core.AgentStep{ID: id, RoleBindingID: binding.ID, Kind: kind, RequestID: requestID, ClientMessageID: id + "-client", PromptSHA256: strings.Repeat("7", 64), SendStatus: core.AgentStepSendStatusPending, RequestedAt: at}
	created, ok, err := store.CreateClearDevComplexAgentStep(ctx, step)
	if err != nil || !ok {
		t.Fatalf("create benchmark planning step: created=%v err=%v", ok, err)
	}
	if changed, err := store.MarkClearDevComplexAgentStepSent(ctx, created.ID, at.Add(time.Millisecond)); err != nil || !changed {
		t.Fatalf("mark benchmark planning step sent: changed=%v err=%v", changed, err)
	}
	completed := at.Add(2 * time.Millisecond)
	created.SendStatus = core.AgentStepSendStatusSettled
	created.TurnID = id + "-turn"
	created.FinalMessageID = id + "-message"
	created.FinalMessageText = finalText
	created.MessageSHA256 = digestBenchmarkBytes([]byte(finalText))
	created.CompletedAt = &completed
	if changed, err := store.SettleClearDevComplexAgentStep(ctx, created); err != nil || !changed {
		t.Fatalf("settle benchmark planning step: changed=%v err=%v", changed, err)
	}
	return created
}

func benchmarkServiceManifestFixture(t *testing.T, projectRoot, group string, policy core.BenchmarkModePolicy) core.BenchmarkBackendManifest {
	t.Helper()
	isolation := filepath.Dir(projectRoot)
	return core.BenchmarkBackendManifest{
		SchemaVersion: 1, Kind: core.BenchmarkBackendManifestKind, Purpose: core.BenchmarkPurposeOffline, FacilityVersion: core.BenchmarkFacilityVersionV1,
		RepositoryCommit: strings.Repeat("a", 40), FacilityCommit: strings.Repeat("b", 40), ProtocolCommit: strings.Repeat("c", 40), MaterialCommit: strings.Repeat("d", 40),
		MaterialSHA256: strings.Repeat("e", 64), PublicInputSHA256: strings.Repeat("f", 64), PlanUnitID: "DEV-R1-T3-" + group, Scene: 1,
		Group: group, Policy: policy, CheckProfile: core.BenchmarkCheckProfileNodeTS, CheckPrefix: "CHK-DEV", ProjectRoot: projectRoot, IsolationRoot: isolation,
		Path: filepath.Join(isolation, "private", "scene-"+group+".json"), ManifestSHA256: strings.Repeat("1", 64),
	}
}

func insertBenchmarkServiceBinding(t *testing.T, dataDir string, binding core.BenchmarkBinding) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dataDir, "ao.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	_, err = db.Exec(`INSERT INTO cleardev_benchmark_bindings (
		development_requirement_id, ao_project_id, project_root, manifest_path, manifest_sha256,
		facility_version, purpose, plan_unit_id, scene, group_id, mode_policy, check_profile,
		check_prefix, check_profile_sha256, public_input_sha256, material_sha256, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		binding.DevelopmentRequirementID, binding.AOProjectID, binding.ProjectRoot, binding.ManifestPath, binding.ManifestSHA256,
		binding.FacilityVersion, binding.Purpose, binding.PlanUnitID, binding.Scene, binding.Group, string(binding.Policy), binding.CheckProfile,
		binding.CheckPrefix, binding.CheckProfileSHA256, binding.PublicInputSHA256, binding.MaterialSHA256, binding.CreatedAt)
	if err != nil {
		t.Fatalf("insert benchmark binding: %v", err)
	}
}

func digestBenchmarkBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
