package cleardev

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestNextComplexQuickTaskSetVersionUsesItsSource(t *testing.T) {
	if got, err := NextComplexQuickTaskSetVersion(7); err != nil || got != 8 {
		t.Fatalf("next task set = %d, err=%v", got, err)
	}
	for _, source := range []int64{0, -1, math.MaxInt64} {
		if _, err := NextComplexQuickTaskSetVersion(source); err == nil {
			t.Fatalf("invalid source task set %d was accepted", source)
		}
	}
}

func TestSelectComplexQuickExecutionApprovesFollowUp(t *testing.T) {
	selection, err := SelectComplexQuickExecution(sampleQuickRequest(t, nil), sampleQuickSourceTask(), sampleQuickPlan(), sampleQuickVersion(), sampleQuickSHA(), true)
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != WorkModeQuick || selection.ReasonCode != ReasonQuickFollowUpApproved || selection.SourceTask.Key != "deduplicate-email" {
		t.Fatalf("approved selection = %#v", selection)
	}
}

func TestSelectComplexQuickExecutionRejectsUnsafeFollowUps(t *testing.T) {
	source := sampleQuickSourceTask()
	plan := sampleQuickPlan()
	version := sampleQuickVersion()
	sha := sampleQuickSHA()
	cases := []struct {
		name   string
		mutate func(*ComplexQuickTaskRequestResult)
		done   bool
		sha    string
		reason ReasonCode
	}{
		{"not completed", func(*ComplexQuickTaskRequestResult) {}, false, sha, ReasonQuickNotSingleCompleted},
		{"wrong source task", func(req *ComplexQuickTaskRequestResult) { req.SourceTaskKey = "build-summary" }, true, sha, ReasonQuickNotSingleCompleted},
		{"extra file", func(req *ComplexQuickTaskRequestResult) {
			req.WritePaths = []string{"src/deduplicate.js", "src/email.js"}
			req.TestPaths = []string{"test/deduplicate.test.js"}
		}, true, sha, ReasonQuickFileLimitExceeded},
		{"path outside task", func(req *ComplexQuickTaskRequestResult) { req.WritePaths = []string{"src/summary.js"} }, true, sha, ReasonQuickPathsNotSubset},
		{"glob path", func(req *ComplexQuickTaskRequestResult) { req.WritePaths = []string{"src/**"} }, true, sha, ReasonQuickPathsNotSubset},
		{"unknown requirement", func(req *ComplexQuickTaskRequestResult) { req.RequirementIDs = []string{"REQ-999"} }, true, sha, ReasonQuickUnknownIDs},
		{"shared path", func(req *ComplexQuickTaskRequestResult) { req.SharedPathsRequireApproval = []string{"src/shared.js"} }, true, sha, ReasonQuickSharedOrGenerated},
		{"sensitive lockfile", func(req *ComplexQuickTaskRequestResult) { req.WritePaths = []string{"package.json"} }, true, sha, ReasonQuickSensitivePath},
		{"sensitive vite config", func(req *ComplexQuickTaskRequestResult) { req.WritePaths = []string{"vite.config.ts"} }, true, sha, ReasonQuickSensitivePath},
		{"wrong checks", func(req *ComplexQuickTaskRequestResult) { req.RequiredCheckIDs = []string{"email-unit"} }, true, sha, ReasonQuickChecksNotFromPlan},
		{"stale integration", func(*ComplexQuickTaskRequestResult) {}, true, strings.Repeat("b", 40), ReasonQuickIntegrationMismatch},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			req := sampleQuickRequest(t, test.mutate)
			selection, err := SelectComplexQuickExecution(req, source, plan, version, test.sha, test.done)
			if err == nil || selection.ReasonCode != test.reason || selection.Mode != WorkModeQuick {
				t.Fatalf("got selection=%#v err=%v", selection, err)
			}
		})
	}
}

func TestQuickPathIsSensitiveCoversFrozenCategories(t *testing.T) {
	allowed := []string{"src/deduplicate.js", "test/deduplicate.test.js", "src/email.js"}
	for _, item := range allowed {
		if quickPathIsSensitive(item) {
			t.Fatalf("ordinary follow-up path %q was treated as sensitive", item)
		}
	}
	cases := []struct {
		category string
		path     string
	}{
		{"database", "backend/migrations/0114_cleardev_quick_execution.sql"},
		{"database", "db/schema.sql"},
		{"dependency", "package.json"},
		{"dependency", "go.mod"},
		{"dependency", "requirements.txt"},
		{"config", "vite.config.ts"},
		{"config", "apps/web/vite.config.ts"},
		{"config", "webpack.config.js"},
		{"config", "tsconfig.json"},
		{"config", "eslint.config.mjs"},
		{"config", "config/app.yaml"},
		{"permission", "CODEOWNERS"},
		{"permission", "src/permissions.json"},
		{"auth", "src/oauth.js"},
		{"auth", "certs/server.pem"},
		{"network", "Dockerfile"},
		{"network", "nginx.conf"},
		{"network", "docker-compose.yml"},
		{"ci", ".github/workflows/test.yml"},
		{"ci", "Makefile"},
		{"ci", "Jenkinsfile"},
		{"repo", ".gitignore"},
		{"repo", ".gitattributes"},
		{"repo", "LICENSE"},
	}
	for _, test := range cases {
		t.Run(test.category+"/"+test.path, func(t *testing.T) {
			if !quickPathIsSensitive(test.path) {
				t.Fatalf("%s path %q was not rejected", test.category, test.path)
			}
		})
	}
}

func TestSelectComplexQuickExecutionRejectsSensitivePathsInsideTaskScope(t *testing.T) {
	paths := []string{
		"vite.config.ts",
		"package.json",
		"migrations/001_init.sql",
		"CODEOWNERS",
		"src/oauth.js",
		"Dockerfile",
		".github/workflows/ci.yml",
		".gitignore",
	}
	for _, item := range paths {
		t.Run(item, func(t *testing.T) {
			source := sampleQuickSourceTask()
			source.WritePaths = []string{item}
			req := sampleQuickRequest(t, func(req *ComplexQuickTaskRequestResult) {
				req.WritePaths = []string{item}
				req.TestPaths = []string{}
			})
			selection, err := SelectComplexQuickExecution(req, source, sampleQuickPlan(), sampleQuickVersion(), sampleQuickSHA(), true)
			if err == nil || selection.ReasonCode != ReasonQuickSensitivePath || selection.Mode != WorkModeQuick {
				t.Fatalf("in-scope %s: selection=%#v err=%v", item, selection, err)
			}
		})
	}
}

func TestSelectComplexQuickExecutionNeedsHumanDoesNotApprove(t *testing.T) {
	req := sampleQuickRequest(t, func(result *ComplexQuickTaskRequestResult) {
		result.Decision = string(ComplexExecutionDecisionNeedsHuman)
		result.ReasonCode = "HUMAN_DECISION_REQUIRED"
	})
	selection, err := SelectComplexQuickExecution(req, sampleQuickSourceTask(), sampleQuickPlan(), sampleQuickVersion(), sampleQuickSHA(), true)
	if err != nil {
		t.Fatal(err)
	}
	if selection.ReasonCode != ReasonQuickNeedsHuman || selection.Mode != WorkModeQuick {
		t.Fatalf("needs human = %#v", selection)
	}
}

func TestParseComplexQuickTaskRequestResultRejectsUnknownAndStaleBindings(t *testing.T) {
	raw := mustQuickJSON(t, sampleQuickRequest(t, nil))
	got, err := ParseComplexQuickTaskRequestResult(raw, "quick-1", "v2", strings.Repeat("c", 64), "deduplicate-email", sampleQuickSHA(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Decision != ComplexQuickDecisionSubmit || got.SourceTaskKey != "deduplicate-email" ||
		got.RequirementSHA256 != strings.Repeat("c", 64) || got.IntegrationCommitSHA != sampleQuickSHA() {
		t.Fatalf("parsed = %#v", got)
	}
	if _, err := ParseComplexQuickTaskRequestResult(append(raw[:len(raw)-1], []byte(`,"extra":true}`)...), "quick-1", "v2", strings.Repeat("c", 64), "deduplicate-email", sampleQuickSHA(), 1); err == nil {
		t.Fatal("unknown field was accepted")
	}
	echoed := sampleQuickRequest(t, nil)
	stale, err := json.Marshal(echoed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseComplexQuickTaskRequestResult(stale, "quick-1", "v2", strings.Repeat("c", 64), "deduplicate-email", sampleQuickSHA(), 1); err == nil {
		t.Fatal("echoed quick-request bindings were accepted")
	}
}

func TestBuildComplexQuickExecutionPackageUsesQuickMode(t *testing.T) {
	input := ComplexStandardExecutionPackageInput{
		ExecutionRunID: "quick-run", RequirementVersionID: "v2", RequirementSHA256: strings.Repeat("c", 64),
		RequirementText: "follow up", PlanID: "plan-1", PlanSHA256: strings.Repeat("d", 64),
		TaskSetVersion: 8, TaskID: "quick-task",
		Task: sampleQuickSourceTask(),
	}
	check, _ := FrozenComplexCheckByID("deduplicate-unit")
	pkg, raw, digest, err := BuildComplexQuickExecutionPackage(input, []string{"src/deduplicate.js", "test/deduplicate.test.js"}, []ComplexExecutionCheckSpec{{ID: check.ID, Argv: check.Argv, TimeoutSeconds: check.TimeoutSeconds}})
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Mode != string(WorkModeQuick) || pkg.TaskSetVersion != 8 || digest == "" || len(raw) == 0 {
		t.Fatalf("package = %#v digest=%s", pkg, digest)
	}
	parsed, err := ParseComplexQuickExecutionPackage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Mode != string(WorkModeQuick) || strings.Join(parsed.WritePaths, ",") != "src/deduplicate.js,test/deduplicate.test.js" {
		t.Fatalf("parsed package = %#v", parsed)
	}
}

func sampleQuickSourceTask() ComplexPlanTask {
	return ComplexPlanTask{
		Key: "deduplicate-email", Title: "去重邮箱", Objective: "实现邮箱去重。",
		RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"},
		WritePaths: []string{"src/deduplicate.js"}, GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{},
		ForbiddenPaths: []string{".git/**"}, RequiredCheckIDs: []string{"deduplicate-unit"},
	}
}

func sampleQuickPlan() ComplexEngineeringPlanResult {
	return ComplexEngineeringPlanResult{
		Tasks:               []ComplexPlanTask{sampleQuickSourceTask()},
		IntegrationCheckIDs: []string{"all-tests"},
	}
}

func sampleQuickVersion() RequirementVersion {
	return RequirementVersion{ID: "v2", SHA256: strings.Repeat("c", 64), TaskSetVersion: 1}
}

func sampleQuickSHA() string { return strings.Repeat("a", 40) }

func sampleQuickRequest(t *testing.T, mutate func(*ComplexQuickTaskRequestResult)) ComplexQuickTaskRequestResult {
	t.Helper()
	req := ComplexQuickTaskRequestResult{
		SchemaVersion: 1, Kind: ComplexQuickTaskRequestKind, RequestID: "quick-1",
		RequirementVersionID: "v2", RequirementSHA256: strings.Repeat("c", 64), TaskSetVersion: 1,
		IntegrationCommitSHA: sampleQuickSHA(), SourceTaskKey: "deduplicate-email",
		Decision: ComplexQuickDecisionSubmit, ReasonCode: ReasonFollowUpReady,
		Summary: "收紧无效邮箱过滤。", Objective: "收紧无效邮箱过滤。",
		RequirementIDs: []string{"REQ-001"}, AcceptanceIDs: []string{"ACC-001"},
		WritePaths: []string{"src/deduplicate.js"}, TestPaths: []string{"test/deduplicate.test.js"},
		GeneratedPaths: []string{}, SharedPathsRequireApproval: []string{}, ForbiddenPaths: []string{".git/**"},
		RequiredCheckIDs: []string{"deduplicate-unit"}, IntegrationCheckIDs: []string{"all-tests"},
	}
	if mutate != nil {
		mutate(&req)
	}
	return req
}

func mustQuickJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := MarshalAgentChosenResult(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
