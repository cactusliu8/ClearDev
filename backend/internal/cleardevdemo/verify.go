package cleardevdemo

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

var (
	gitSHA1  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

const (
	// EvidenceSchemaV1 is the original demonstration pack format.
	EvidenceSchemaV1 = 1
	// EvidenceSchemaV2 binds requirement and plan bodies inside the pack.
	EvidenceSchemaV2 = 2
	// EvidencePackConsistencyScope is the only claim a successful verify makes.
	EvidencePackConsistencyScope = "只证明包内一致，不证明来源真伪"
	evidenceRequirementFile      = "requirement.json"
	evidencePlanFile             = "plan.json"
	evidenceReviewFile           = "review.json"
	evidenceStartupFile          = "desktop-startup.json"
	evidenceVisibilityFile       = "visibility.json"
	evidenceCleanupFile          = "cleanup.json"
)

type boundPlanIdentifiers struct {
	RequirementVersionID     string `json:"requirementVersionId"`
	RequirementVersionSHA256 string `json:"requirementVersionSha256"`
	CompilationSHA256        string `json:"compilationSha256"`
}

type boundReviewIdentifiers struct {
	ID         string `json:"id"`
	PlanID     string `json:"planId"`
	PlanSHA256 string `json:"planSha256"`
	Verdict    string `json:"verdict"`
}

// EvidenceVerifyResult is the offline check outcome. It never claims the pack
// came from a trusted source.
type EvidenceVerifyResult struct {
	SchemaVersion int
	Legacy        bool
}

// VerifyEvidenceDir checks a previously written demonstration pack without
// contacting the network or a running daemon.
func VerifyEvidenceDir(dir string) (EvidenceVerifyResult, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "evidence.json")) //nolint:gosec // evidence directory is a local demonstration output
	if err != nil {
		return EvidenceVerifyResult{}, fmt.Errorf("read evidence.json: %w", err)
	}
	var evidence Evidence
	if err := json.Unmarshal(raw, &evidence); err != nil {
		return EvidenceVerifyResult{}, fmt.Errorf("parse evidence.json: %w", err)
	}
	switch evidence.SchemaVersion {
	case EvidenceSchemaV1:
		if err := verifyEvidenceV1(dir, evidence); err != nil {
			return EvidenceVerifyResult{}, err
		}
		return EvidenceVerifyResult{SchemaVersion: EvidenceSchemaV1, Legacy: true}, nil
	case EvidenceSchemaV2:
		if err := verifyEvidenceV2(dir, evidence); err != nil {
			return EvidenceVerifyResult{}, err
		}
		return EvidenceVerifyResult{SchemaVersion: EvidenceSchemaV2}, nil
	default:
		return EvidenceVerifyResult{}, fmt.Errorf("unsupported evidence schema %d", evidence.SchemaVersion)
	}
}

func verifyEvidenceV1(dir string, evidence Evidence) error {
	return verifyEvidenceCommon(dir, evidence, false)
}

func verifyEvidenceV2(dir string, evidence Evidence) error {
	if err := verifyBoundBodies(dir, evidence); err != nil {
		return err
	}
	if evidence.TaskSetVersion != core.ComplexStandardTaskSetVersion {
		return fmt.Errorf("evidence taskSetVersion = %d, want %d", evidence.TaskSetVersion, core.ComplexStandardTaskSetVersion)
	}
	if !strings.EqualFold(evidence.PlanReviewVerdict, string(core.PlanReviewApproved)) {
		return fmt.Errorf("evidence plan review verdict = %s, want APPROVED", evidence.PlanReviewVerdict)
	}
	if err := verifyDesktopRuntimeEvidence(dir, evidence); err != nil {
		return err
	}
	return verifyEvidenceCommon(dir, evidence, true)
}

func verifyDesktopRuntimeEvidence(dir string, evidence Evidence) error {
	buildSourcePath := filepath.Join(dir, buildSourceFileName)
	if err := verifyEvidenceFileHash(buildSourcePath, evidence.BuildSourceSHA256, "build source"); err != nil {
		return err
	}
	raw, err := os.ReadFile(buildSourcePath) //nolint:gosec // local evidence file
	if err != nil {
		return err
	}
	var buildSource BuildSource
	if err := json.Unmarshal(raw, &buildSource); err != nil {
		return fmt.Errorf("parse %s: %w", buildSourceFileName, err)
	}
	if buildSource.SchemaVersion != 1 || !buildSource.SourceClean || !evidence.BuildSourceClean {
		return fmt.Errorf("build source does not prove clean source inputs")
	}
	if !gitSHA1.MatchString(buildSource.CandidateCommit) || buildSource.CandidateCommit != evidence.BuildCandidateCommit {
		return fmt.Errorf("build source candidate does not match evidence")
	}
	if len(buildSource.SourceScope) == 0 {
		return fmt.Errorf("build source scope is empty")
	}
	for _, name := range []string{"electron", "daemon", "agent-browser"} {
		if !sha256RE.MatchString(evidence.RuntimeSHA256[name]) || isAllZeroHex(evidence.RuntimeSHA256[name]) {
			return fmt.Errorf("evidence runtime hash for %s is invalid", name)
		}
	}
	if err := verifyStartupEvidence(dir, evidence); err != nil {
		return err
	}
	if err := verifyVisibilityEvidence(dir, evidence); err != nil {
		return err
	}
	return verifyCleanupEvidence(dir, evidence)
}

func verifyStartupEvidence(dir string, evidence Evidence) error {
	path := filepath.Join(dir, evidenceStartupFile)
	if err := verifyEvidenceFileHash(path, evidence.StartupSHA256, "desktop startup"); err != nil {
		return err
	}
	raw, err := os.ReadFile(path) //nolint:gosec // local evidence file
	if err != nil {
		return err
	}
	var startup struct {
		SchemaVersion       int    `json:"schemaVersion"`
		Stage               string `json:"stage"`
		LastSuccessfulStage string `json:"lastSuccessfulStage"`
		Failure             any    `json:"failure"`
		Events              []struct {
			Stage     string `json:"stage"`
			Timestamp string `json:"timestamp"`
		} `json:"events"`
	}
	if err := json.Unmarshal(raw, &startup); err != nil {
		return fmt.Errorf("parse %s: %w", evidenceStartupFile, err)
	}
	if startup.SchemaVersion != 1 || startup.Stage != "DAEMON_READY" || startup.LastSuccessfulStage != "DAEMON_READY" || startup.Failure != nil {
		return fmt.Errorf("desktop startup evidence did not reach DAEMON_READY cleanly")
	}
	want := []string{"APP_READY", "WINDOW_STARTING", "WINDOW_READY", "DAEMON_STARTING", "DAEMON_STARTED", "DAEMON_READY"}
	position := 0
	var previous time.Time
	for _, event := range startup.Events {
		timestamp, err := time.Parse(time.RFC3339Nano, event.Timestamp)
		if err != nil {
			return fmt.Errorf("desktop startup event %s has invalid timestamp", event.Stage)
		}
		if !previous.IsZero() && timestamp.Before(previous) {
			return fmt.Errorf("desktop startup event %s is out of timestamp order", event.Stage)
		}
		previous = timestamp
		if position < len(want) && event.Stage == want[position] {
			position++
		}
	}
	if position != len(want) {
		return fmt.Errorf("desktop startup evidence is missing ordered stage %s", want[position])
	}
	return nil
}

func verifyVisibilityEvidence(dir string, evidence Evidence) error {
	path := filepath.Join(dir, evidenceVisibilityFile)
	if err := verifyEvidenceFileHash(path, evidence.VisibilitySHA256, "desktop visibility"); err != nil {
		return err
	}
	raw, err := os.ReadFile(path) //nolint:gosec // local evidence file
	if err != nil {
		return err
	}
	var visibility VisibilityEvidence
	if err := json.Unmarshal(raw, &visibility); err != nil {
		return fmt.Errorf("parse %s: %w", evidenceVisibilityFile, err)
	}
	host, rawPort, splitErr := net.SplitHostPort(visibility.VNCAddress)
	port, portErr := strconv.Atoi(rawPort)
	if visibility.SchemaVersion != 1 || !visibility.PasswordProtected || splitErr != nil || portErr != nil || host != "127.0.0.1" || port <= 0 || port > 65535 {
		return fmt.Errorf("visibility evidence is not password-protected loopback VNC")
	}
	if visibility.HostDisplay == "" || visibility.ViewerExecutable == "" || visibility.ViewerWindowWidth < 320 || visibility.ViewerWindowHeight < 240 {
		return fmt.Errorf("visibility evidence is missing a real desktop viewer window")
	}
	if visibility.ScreenshotSource != "vnc-streamed-xvfb-root" {
		return fmt.Errorf("visibility evidence has unexpected screenshot source %q", visibility.ScreenshotSource)
	}
	screenshotPath := filepath.Join(dir, "visible-desktop.xwd")
	if err := verifyEvidenceFileHash(screenshotPath, visibility.ScreenshotSHA256, "visible desktop screenshot"); err != nil {
		return err
	}
	return nil
}

func verifyCleanupEvidence(dir string, evidence Evidence) error {
	path := filepath.Join(dir, evidenceCleanupFile)
	if err := verifyEvidenceFileHash(path, evidence.CleanupSHA256, "cleanup"); err != nil {
		return err
	}
	raw, err := os.ReadFile(path) //nolint:gosec // local evidence file
	if err != nil {
		return err
	}
	var cleanup CleanupEvidence
	if err := json.Unmarshal(raw, &cleanup); err != nil {
		return fmt.Errorf("parse %s: %w", evidenceCleanupFile, err)
	}
	ports := map[int]bool{}
	for _, port := range cleanup.CheckedPorts {
		if port <= 0 || port > 65535 {
			return fmt.Errorf("cleanup evidence contains invalid port %d", port)
		}
		ports[port] = true
	}
	if cleanup.SchemaVersion != 1 || !cleanup.ProcessesStopped || !cleanup.PortsClosed || !cleanup.WorkDirectoriesGone || cleanup.FailureReason != "" || len(cleanup.CheckedPorts) != 4 || len(ports) != 4 {
		return fmt.Errorf("cleanup evidence is incomplete: %+v", cleanup)
	}
	return nil
}

func verifyEvidenceFileHash(path, expected, label string) error {
	if !sha256RE.MatchString(expected) || isAllZeroHex(expected) {
		return fmt.Errorf("evidence %s hash is invalid", label)
	}
	actual, err := sha256File(path)
	if err != nil {
		return fmt.Errorf("missing %s: %w", filepath.Base(path), err)
	}
	if actual != expected {
		return fmt.Errorf("evidence %s hash does not match %s", label, filepath.Base(path))
	}
	return nil
}

func verifyBoundBodies(dir string, evidence Evidence) error {
	reqHash, err := sha256File(filepath.Join(dir, evidenceRequirementFile))
	if err != nil {
		return fmt.Errorf("missing %s: %w", evidenceRequirementFile, err)
	}
	if evidence.RequirementSHA256 != reqHash {
		return fmt.Errorf("evidence requirement hash does not match %s", evidenceRequirementFile)
	}
	planHash, err := sha256File(filepath.Join(dir, evidencePlanFile))
	if err != nil {
		return fmt.Errorf("missing %s: %w", evidencePlanFile, err)
	}
	if evidence.PlanSHA256 != planHash {
		return fmt.Errorf("evidence plan hash does not match %s", evidencePlanFile)
	}
	if err := verifyPlanIdentifiers(dir, evidence); err != nil {
		return err
	}
	return verifyReviewIdentifiers(dir, evidence)
}

func verifyPlanIdentifiers(dir string, evidence Evidence) error {
	raw, err := os.ReadFile(filepath.Join(dir, evidencePlanFile)) //nolint:gosec // evidence directory is a local demonstration output
	if err != nil {
		return fmt.Errorf("missing %s: %w", evidencePlanFile, err)
	}
	var plan boundPlanIdentifiers
	if err := json.Unmarshal(raw, &plan); err != nil {
		return fmt.Errorf("parse %s: %w", evidencePlanFile, err)
	}
	if plan.RequirementVersionID == "" || plan.RequirementVersionID != evidence.RequirementVersionID {
		return fmt.Errorf("plan.json requirementVersionId does not match evidence")
	}
	if plan.RequirementVersionSHA256 == "" || plan.RequirementVersionSHA256 != evidence.RequirementSHA256 {
		return fmt.Errorf("plan.json requirement hash does not match evidence")
	}
	if plan.CompilationSHA256 != "" && plan.CompilationSHA256 != evidence.RequirementSHA256 {
		return fmt.Errorf("plan.json compilation hash does not match evidence")
	}
	return nil
}

func verifyReviewIdentifiers(dir string, evidence Evidence) error {
	raw, err := os.ReadFile(filepath.Join(dir, evidenceReviewFile)) //nolint:gosec // evidence directory is a local demonstration output
	if err != nil {
		return fmt.Errorf("missing %s: %w", evidenceReviewFile, err)
	}
	var review boundReviewIdentifiers
	if err := json.Unmarshal(raw, &review); err != nil {
		return fmt.Errorf("parse %s: %w", evidenceReviewFile, err)
	}
	if review.ID == "" || review.ID != evidence.PlanReviewID {
		return fmt.Errorf("review.json id does not match evidence planReviewID")
	}
	if review.PlanID == "" || review.PlanID != evidence.PlanID {
		return fmt.Errorf("review.json planId does not match evidence")
	}
	if review.PlanSHA256 == "" || review.PlanSHA256 != evidence.PlanSHA256 {
		return fmt.Errorf("review.json plan hash does not match evidence")
	}
	if !strings.EqualFold(review.Verdict, evidence.PlanReviewVerdict) {
		return fmt.Errorf("review.json verdict does not match evidence")
	}
	return nil
}

func verifyEvidenceCommon(dir string, evidence Evidence, requireSettledSpecialist bool) error {
	if err := verifyFrozenPRD(dir, evidence); err != nil {
		return err
	}
	if err := verifyTemplateCommit(dir, evidence); err != nil {
		return err
	}
	if err := verifyCommit("combination commit", evidence.IntegrationSHA); err != nil {
		return err
	}
	if evidence.RequirementID == "" || evidence.RequirementVersionID == "" || evidence.PlanID == "" || evidence.PlanReviewID == "" {
		return fmt.Errorf("evidence is missing bound identifiers")
	}
	if !sha256RE.MatchString(evidence.RequirementSHA256) || !sha256RE.MatchString(evidence.PlanSHA256) {
		return fmt.Errorf("evidence requirement or plan hash is not a SHA-256")
	}
	if evidence.Mode != "PARALLEL" || evidence.FixedBuilderCount != 2 {
		return fmt.Errorf("evidence mode = %s builders=%d, want PARALLEL with two builders", evidence.Mode, evidence.FixedBuilderCount)
	}
	if !strings.EqualFold(evidence.ProgressPhase, "COMPLETED") {
		return fmt.Errorf("evidence phase = %s, want COMPLETED", evidence.ProgressPhase)
	}
	if !sha256RE.MatchString(evidence.ExplanationSHA256) || isAllZeroHex(evidence.ExplanationSHA256) {
		return fmt.Errorf("evidence is missing a progress explanation hash")
	}
	if err := verifySampleFile(dir, "sample-api.json"); err != nil {
		return err
	}
	if err := verifySampleFile(dir, "sample-page.json"); err != nil {
		return err
	}
	if evidence.Sample["accepted"] != FrozenSampleCounts["accepted"] ||
		evidence.Sample["rejected"] != FrozenSampleCounts["rejected"] ||
		evidence.Sample["duplicates"] != FrozenSampleCounts["duplicates"] {
		return fmt.Errorf("evidence sample = %#v, want %#v", evidence.Sample, FrozenSampleCounts)
	}
	if err := verifyTaskBindings(evidence); err != nil {
		return err
	}
	if err := verifySpecialist(evidence, requireSettledSpecialist); err != nil {
		return err
	}
	if err := verifyUsage(evidence); err != nil {
		return err
	}
	if strings.TrimSpace(evidence.RoleSessions["STEWARD"]) == "" {
		return fmt.Errorf("evidence is missing the Project Steward session")
	}
	return nil
}

func verifyFrozenPRD(dir string, evidence Evidence) error {
	prdPath := filepath.Join(dir, "prd.md")
	fileHash, err := sha256File(prdPath)
	if err != nil {
		return fmt.Errorf("missing prd.md: %w", err)
	}
	if evidence.InputSHA256 == nil || evidence.InputSHA256["prd.md"] != fileHash {
		return fmt.Errorf("evidence prd.md hash does not match the file")
	}
	want, err := core.FrozenDemoPRD()
	if err != nil {
		return fmt.Errorf("read frozen PRD: %w", err)
	}
	got, err := os.ReadFile(prdPath) //nolint:gosec // evidence PRD is a local demonstration output
	if err != nil {
		return err
	}
	if strings.TrimRight(string(got), "\n") != strings.TrimRight(want, "\n") {
		return fmt.Errorf("evidence PRD is not the frozen demonstration PRD")
	}
	return nil
}

func verifyTemplateCommit(dir string, evidence Evidence) error {
	fileSHA := readTrimmed(dir, "template-commit.txt")
	if err := verifyCommit("template commit", fileSHA); err != nil {
		return err
	}
	if strings.TrimSpace(evidence.TemplateCommit) != fileSHA {
		return fmt.Errorf("template-commit.txt does not match evidence templateCommit")
	}
	return nil
}

func verifyCommit(label, sha string) error {
	sha = strings.TrimSpace(sha)
	if !gitSHA1.MatchString(sha) || isAllZeroHex(sha) {
		return fmt.Errorf("%s %q is not a real Git commit SHA", label, sha)
	}
	return nil
}

func verifySampleFile(dir, name string) error {
	raw, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // evidence sample files are local demonstration output
	if err != nil {
		return fmt.Errorf("missing %s: %w", name, err)
	}
	var got map[string]int
	if err := json.Unmarshal(raw, &got); err != nil {
		return fmt.Errorf("parse %s: %w", name, err)
	}
	if got["accepted"] != FrozenSampleCounts["accepted"] ||
		got["rejected"] != FrozenSampleCounts["rejected"] ||
		got["duplicates"] != FrozenSampleCounts["duplicates"] {
		return fmt.Errorf("%s = %#v, want %#v", name, got, FrozenSampleCounts)
	}
	return nil
}

func verifyTaskBindings(evidence Evidence) error {
	if len(evidence.Tasks) < 2 {
		return fmt.Errorf("evidence is missing tasks")
	}
	for _, task := range evidence.Tasks {
		taskID := mapString(task, "id")
		status := mapString(task, "status")
		sha := mapString(task, "currentCandidateCommitSha")
		if taskID == "" || !strings.EqualFold(status, "DONE") {
			return fmt.Errorf("evidence task is not complete: %+v", task)
		}
		if err := verifyCommit("task candidate", sha); err != nil {
			return fmt.Errorf("task %s: %w", taskID, err)
		}
		if !hasPassingCheck(evidence.CheckRuns, "SCOPE", taskID, sha, "") {
			return fmt.Errorf("task %s candidate %s is missing a passing SCOPE check", taskID, sha)
		}
		requiredIDs, err := uniqueNonEmptyIDs(mapStringSlice(task, "requiredCheckIds"), fmt.Sprintf("task %s requiredCheckIds", taskID))
		if err != nil {
			return err
		}
		for _, checkID := range requiredIDs {
			if !hasPassingCheck(evidence.CheckRuns, "REQUIRED_CHECK", taskID, sha, checkID) {
				return fmt.Errorf("task %s candidate %s is missing a passing REQUIRED_CHECK %s", taskID, sha, checkID)
			}
		}
		if !hasPassingReview(evidence.Reviews, taskID, sha) {
			return fmt.Errorf("task %s candidate %s is missing a passing independent review", taskID, sha)
		}
	}
	integrationIDs, err := uniqueNonEmptyIDs(evidence.IntegrationCheckIDs, "integrationCheckIds")
	if err != nil {
		return err
	}
	for _, checkID := range integrationIDs {
		if !hasPassingCheck(evidence.CheckRuns, "INTEGRATION", "", evidence.IntegrationSHA, checkID) {
			return fmt.Errorf("integration commit is not bound to a passing INTEGRATION check %s", checkID)
		}
	}
	return nil
}

func hasPassingCheck(runs []map[string]any, kind, taskID, sha, checkID string) bool {
	for _, run := range runs {
		if !strings.EqualFold(mapString(run, "kind"), kind) {
			continue
		}
		if mapString(run, "candidateCommitSha") != sha {
			continue
		}
		if taskID != "" && mapString(run, "complexExecutionTaskId") != taskID {
			continue
		}
		if checkID != "" && mapString(run, "checkId") != checkID {
			continue
		}
		if !strings.EqualFold(mapString(run, "status"), "SETTLED") || !strings.EqualFold(mapString(run, "result"), "PASS") {
			continue
		}
		if err := verifyCommit("check candidate", sha); err != nil {
			continue
		}
		return true
	}
	return false
}

func hasPassingReview(reviews []map[string]any, taskID, sha string) bool {
	for _, review := range reviews {
		if mapString(review, "complexExecutionTaskId") != taskID {
			continue
		}
		if mapString(review, "candidateCommitSha") != sha {
			continue
		}
		if !strings.EqualFold(mapString(review, "status"), "SETTLED") || !strings.EqualFold(mapString(review, "verdict"), "PASS") {
			continue
		}
		if err := verifyCommit("review candidate", sha); err != nil {
			continue
		}
		return true
	}
	return false
}

func mapString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func mapStringSlice(values map[string]any, key string) []string {
	if values == nil {
		return nil
	}
	return stringSlice(values[key])
}

func stringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, len(typed))
		copy(out, typed)
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, strings.TrimSpace(fmt.Sprint(item)))
		}
		return out
	default:
		return nil
	}
}

func uniqueNonEmptyIDs(ids []string, label string) ([]string, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("%s is empty", label)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("%s contains an empty identifier", label)
		}
		if seen[id] {
			return nil, fmt.Errorf("%s repeats %s", label, id)
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, nil
}

func verifySpecialist(evidence Evidence, requireSettled bool) error {
	for _, check := range evidence.SpecialistChecks {
		id := mapString(check, "checkId")
		result := mapString(check, "result")
		status := mapString(check, "status")
		if id == "sqlite-migration-specialist" && strings.EqualFold(result, "PASS") {
			if requireSettled {
				if strings.EqualFold(status, "SETTLED") {
					return nil
				}
				continue
			}
			if status == "" || strings.EqualFold(status, "SETTLED") {
				return nil
			}
		}
	}
	return fmt.Errorf("evidence is missing a passing sqlite-migration-specialist check")
}

func verifyUsage(evidence Evidence) error {
	if evidence.Usage == nil {
		return fmt.Errorf("evidence is missing usage")
	}
	steps := jsonNumber(evidence.Usage["agentSteps"])
	checks := jsonNumber(evidence.Usage["checkRuns"])
	reviews := jsonNumber(evidence.Usage["reviews"])
	if steps < 1 || checks < 1 || reviews < 1 {
		return fmt.Errorf("evidence usage is incomplete: %+v", evidence.Usage)
	}
	if int(checks) != len(evidence.CheckRuns) || int(reviews) != len(evidence.Reviews) {
		return fmt.Errorf("evidence usage does not match stored checks and reviews")
	}
	return nil
}

func jsonNumber(value any) float64 {
	switch n := value.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		v, _ := n.Float64()
		return v
	default:
		return 0
	}
}

func readTrimmed(dir, name string) string {
	raw, err := os.ReadFile(filepath.Join(dir, name)) //nolint:gosec // evidence files are local demonstration output
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func isAllZeroHex(s string) bool {
	if s == "" {
		return true
	}
	for _, c := range s {
		if c != '0' {
			return false
		}
	}
	return true
}
