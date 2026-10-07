package cleardev

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"
)

// Project planning protocols and reasons are distinct from executable mail contracts.
const (
	ProjectDiscoveryVersion                = 2
	ProjectPlanningVersion                 = 3
	ReasonProjectPlanningOnly   ReasonCode = "PROJECT_EXECUTION_NOT_AVAILABLE"
	ReasonProductPlanSuperseded ReasonCode = "PRODUCT_PLAN_SUPERSEDED"
)

// ProductOption is a proposal, not a verified repository or an approved decision.
type ProductOption struct {
	Key           string   `json:"key"`
	Title         string   `json:"title"`
	Origin        string   `json:"origin"`
	RepositoryURL string   `json:"repositoryUrl,omitempty"`
	Description   string   `json:"description"`
	Tradeoffs     []string `json:"tradeoffs"`
}

// ProductEvidence preserves the distinction between observations and unknowns.
// Even OBSERVED is Steward-reported evidence; only repository bindings are
// independently observed by the control plane.
type ProductEvidence struct {
	Status string `json:"status"`
	Claim  string `json:"claim"`
	Source string `json:"source"`
}

// ProjectExecutionBasis records proposed engineering permissions and commands.
// A saved basis is planning input, never a runtime grant. Execution separately
// binds an explicit ProjectExecutionContract to the current confirmed sources.
type ProjectExecutionBasis struct {
	WritePaths      []string           `json:"writePaths"`
	DependencyNeeds []string           `json:"dependencyNeeds"`
	Checks          []ProjectCheckSpec `json:"checks"`
	Launch          ProjectLaunch      `json:"launch"`
	Runtime         *ProjectRuntime    `json:"runtime,omitempty"`
	Trial           *ProjectTrial      `json:"trial,omitempty"`
}

// ProjectCheckSpec is a proposed project operation, unlike the internal frozen
// legacy check catalog. Its wire format must not change that legacy catalog.
type ProjectCheckSpec struct {
	ID             string   `json:"id"`
	Argv           []string `json:"argv"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
	MainPaths      []string `json:"mainPaths"`
}

// CheckCatalog adapts a saved basis for validation only, not runtime execution.
func (basis ProjectExecutionBasis) CheckCatalog() []ComplexCheckSpec {
	catalog := make([]ComplexCheckSpec, 0, len(basis.Checks))
	for _, check := range basis.Checks {
		catalog = append(catalog, ComplexCheckSpec{
			ID: check.ID, Argv: append([]string(nil), check.Argv...),
			TimeoutSeconds: check.TimeoutSeconds, MainPaths: append([]string(nil), check.MainPaths...),
		})
	}
	return catalog
}

// ProjectLaunch describes a proposed startup or usage operation, not a runtime grant.
type ProjectLaunch struct {
	Argv             []string `json:"argv"`
	WorkingDirectory string   `json:"workingDirectory"`
	Description      string   `json:"description"`
}

// ProductSelection is a user choice bound to an actually inspected, registered
// repository. It is copied into each discussion context, never mutated in place.
type ProductSelection struct {
	PlanAuthorizationID string                   `json:"planAuthorizationId,omitempty"`
	SourceDiscussionID  string                   `json:"sourceDiscussionId"`
	ChoiceDiscussionID  string                   `json:"choiceDiscussionId,omitempty"`
	Option              ProductOption            `json:"option"`
	Reason              string                   `json:"reason"`
	AOProjectID         string                   `json:"aoProjectId"`
	RepositoryPath      string                   `json:"repositoryPath"`
	RepositoryURL       string                   `json:"repositoryUrl"`
	BaseCommitSHA       string                   `json:"baseCommitSha"`
	CreatedAt           time.Time                `json:"createdAt"`
	Delivery            *ProductDeliveryBaseline `json:"delivery,omitempty"`
}

// ProductDeliveryBaseline is selected explicitly by the user, then resolved
// against the backend's completed integration and independent final review.
// It does not repoint main or reinterpret the original EMPTY/EXISTING choice.
type ProductDeliveryBaseline struct {
	RequirementID          string `json:"requirementId"`
	ExecutionRunID         string `json:"executionRunId"`
	ResultID               string `json:"resultId"`
	IntegrationCandidateID string `json:"integrationCandidateId"`
	CandidateSHA           string `json:"candidateSha"`
}

// ValidateProductDeliveryBaseline rejects incomplete or inconsistent delivery provenance.
func ValidateProductDeliveryBaseline(selection ProductSelection) error {
	if selection.PlanAuthorizationID != "" && (selection.ChoiceDiscussionID != "" || selection.Delivery == nil) {
		return errors.New("automatic delivery selection has conflicting authority")
	}
	if selection.Delivery == nil {
		return nil
	}
	binding := selection.Delivery
	if !validExecutionIdentifier(binding.RequirementID) || !validExecutionIdentifier(binding.ExecutionRunID) ||
		!validExecutionIdentifier(binding.ResultID) || !validExecutionIdentifier(binding.IntegrationCandidateID) ||
		!mailSHA1(binding.CandidateSHA) || binding.CandidateSHA != selection.BaseCommitSHA || (selection.ChoiceDiscussionID == "" && selection.PlanAuthorizationID == "") {
		return errors.New("a continued project must bind an explicit exact completed delivery")
	}
	return nil
}

// ProjectPath is a bounded repository-relative planning boundary. Generic
// planning permits arbitrary project roots, but never traversal or Git internals.
func ProjectPath(value string, allowGit bool) bool {
	if value == "" || len(value) > 500 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\\\x00\r\n") || strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return false
	}
	base := strings.TrimSuffix(value, "/**")
	if base == "." || base == "" || path.Clean(base) != base || strings.ContainsAny(base, "*?[]") {
		return false
	}
	for _, part := range strings.Split(base, "/") {
		if part == ".." || part == "." || (!allowGit && part == ".git") {
			return false
		}
	}
	return true
}

// ValidateProjectExecutionBasis checks bounded planning inputs without executing them.
func ValidateProjectExecutionBasis(basis ProjectExecutionBasis) error {
	if err := ValidateProjectTrial(basis.Trial); err != nil {
		return err
	}
	if err := productTextList(basis.WritePaths, 1, 20); err != nil {
		return prefixResultField("writePaths", err)
	}
	for _, p := range basis.WritePaths {
		if !ProjectPath(p, false) {
			return errors.New("project write boundary must be a safe repository-relative path")
		}
	}
	if err := productTextList(basis.DependencyNeeds, 0, 20); err != nil {
		return prefixResultField("dependencyNeeds", err)
	}
	if len(basis.Checks) < 1 || len(basis.Checks) > 10 {
		return errors.New("project basis needs 1-10 proposed checks")
	}
	seen := map[string]bool{}
	checkPaths := []string{}
	for i, check := range basis.Checks {
		if !productKey(check.ID) || seen[check.ID] || check.TimeoutSeconds < 1 || check.TimeoutSeconds > 3600 || len(check.Argv) < 1 || len(check.Argv) > 40 {
			return errors.New("invalid or duplicate project check")
		}
		seen[check.ID] = true
		for j, arg := range check.Argv {
			if err := validateResultText(fmt.Sprintf("checks[%d].argv[%d]", i, j), arg, 2000); err != nil {
				return err
			}
			if strings.ContainsRune(arg, '\x00') {
				return errors.New("invalid proposed check argument")
			}
		}
		if err := productTextList(check.MainPaths, 1, 20); err != nil {
			return prefixResultField(fmt.Sprintf("checks[%d].mainPaths", i), err)
		}
		for _, p := range check.MainPaths {
			if !ProjectPath(p, false) {
				return errors.New("invalid project check path")
			}
			checkPaths = append(checkPaths, p)
		}
	}
	for _, p := range basis.WritePaths {
		if !matchesAny(checkPaths, p) {
			return fmt.Errorf("project write boundary %q is not covered by any proposed check", p)
		}
	}
	if err := validateResultText("launch.description", basis.Launch.Description, 10000); err != nil {
		return err
	}
	if basis.Launch.Argv == nil || len(basis.Launch.Argv) > 40 || (basis.Launch.WorkingDirectory != "." && !ProjectPath(basis.Launch.WorkingDirectory, false)) {
		return errors.New("project basis needs an explicit launch description and working directory")
	}
	for i, arg := range basis.Launch.Argv {
		if err := validateResultText(fmt.Sprintf("launch.argv[%d]", i), arg, 2000); err != nil {
			return err
		}
		if strings.ContainsRune(arg, '\x00') {
			return errors.New("invalid launch argument")
		}
	}
	if basis.Runtime != nil {
		if err := ValidateProjectRuntime(*basis.Runtime); err != nil {
			return err
		}
	}
	return nil
}

func validateProductOptions(r ProductDiscoveryResult) error {
	if len(r.Options) < 1 || len(r.Options) > 6 || r.Evidence == nil || len(r.Evidence) > 30 {
		return errors.New("project proposal needs bounded options and evidence")
	}
	seen := map[string]bool{}
	for i, option := range r.Options {
		if err := validateResultText(fmt.Sprintf("options[%d].title", i), option.Title, 200); err != nil {
			return err
		}
		if err := validateResultText(fmt.Sprintf("options[%d].description", i), option.Description, 10000); err != nil {
			return err
		}
		if !productKey(option.Key) || seen[option.Key] {
			return errors.New("invalid or duplicate project option")
		}
		seen[option.Key] = true
		if option.Origin != "EMPTY" && option.Origin != "EXISTING" && option.Origin != "DISCOVERED" {
			return errors.New("unknown project origin")
		}
		if option.Origin == "DISCOVERED" && !PublicRepositoryURL(option.RepositoryURL) {
			return errors.New("discovered option needs a public repository URL without credentials")
		}
		if option.RepositoryURL != "" && !PublicRepositoryURL(option.RepositoryURL) {
			return errors.New("invalid repository URL")
		}
		if err := productTextList(option.Tradeoffs, 1, 8); err != nil {
			return prefixResultField(fmt.Sprintf("options[%d].tradeoffs", i), err)
		}
	}
	if r.SelectedOptionKey != "" && !seen[r.SelectedOptionKey] {
		return errors.New("selected option is not in proposal")
	}
	if r.Outcome == "READY" && r.SelectedOptionKey == "" {
		return errors.New("READY needs the selected project option")
	}
	for i, e := range r.Evidence {
		if err := validateResultText(fmt.Sprintf("evidence[%d].claim", i), e.Claim, 10000); err != nil {
			return err
		}
		if err := validateResultText(fmt.Sprintf("evidence[%d].source", i), e.Source, 10000); err != nil {
			return err
		}
		if e.Status != "OBSERVED" && e.Status != "ASSUMPTION" && e.Status != "UNVERIFIED" {
			return errors.New("evidence must distinguish observations, assumptions and unverified information with a source")
		}
	}
	for i, stage := range r.Stages {
		if stage.ExecutionBasis == nil {
			return errors.New("project stage needs its own proposed execution basis")
		}
		if err := ValidateProjectExecutionBasis(*stage.ExecutionBasis); err != nil {
			return prefixResultField(fmt.Sprintf("stages[%d].executionBasis", i), err)
		}
	}
	return nil
}

// ProductOptionsEqual compares the complete saved proposal identity. Reusing
// an option key cannot silently change its source, description or tradeoffs.
func ProductOptionsEqual(left, right ProductOption) bool {
	return left.Key == right.Key && left.Title == right.Title && left.Origin == right.Origin &&
		left.RepositoryURL == right.RepositoryURL && left.Description == right.Description && slices.Equal(left.Tradeoffs, right.Tradeoffs)
}

// ValidateProductDiscussionSelection pins the protocol and the complete chosen
// option. A model cannot invent a user decision or alter it while keeping its key.
func ValidateProductDiscussionSelection(result ProductDiscoveryResult, protocol int, selection *ProductSelection) error {
	if protocol == 0 {
		protocol = ProductDiscoveryVersion
	}
	if result.SchemaVersion != protocol {
		return errors.New("discussion result does not match its immutable protocol")
	}
	if protocol == ProductDiscoveryVersion {
		if selection != nil {
			return errors.New("legacy discussion cannot acquire a project choice")
		}
		return nil
	}
	if selection == nil {
		if result.Outcome == "READY" || result.SelectedOptionKey != "" {
			return errors.New("an unselected proposal must await the user's project choice")
		}
		return nil
	}
	if err := ValidateProductDeliveryBaseline(*selection); err != nil {
		return err
	}
	if result.SelectedOptionKey != selection.Option.Key {
		return errors.New("preserve the saved user choice; do not silently choose another option")
	}
	for _, option := range result.Options {
		if ProductOptionsEqual(option, selection.Option) {
			return nil
		}
	}
	return errors.New("the selected option must remain identical to the saved user choice")
}

// ProductChoiceProposal returns only a genuinely saved model proposal. An
// interrupted selected discussion keeps its original proposal for a new user
// discussion. Workspace drift still requires a fresh explicit choice; a rejected
// protocol reply may retain a source that the Service revalidates. Neither path
// fabricates a result or replays the failed round.
func ProductChoiceProposal(discussions []ProductDiscussion) *ProductDiscussion {
	if len(discussions) == 0 {
		return nil
	}
	latest := &discussions[len(discussions)-1]
	if latest.Result != nil {
		return latest
	}
	if latest.ProtocolVersion != ProjectDiscoveryVersion || latest.Selection == nil || latest.SettledAt == nil ||
		(latest.FailureReason != "PRODUCT_STEWARD_WORKSPACE_CHANGED" && latest.FailureReason != "PRODUCT_DISCOVERY_INVALID") {
		return nil
	}
	for i := range discussions[:len(discussions)-1] {
		proposal := &discussions[i]
		if proposal.ID == latest.Selection.SourceDiscussionID && proposal.ProtocolVersion == ProjectDiscoveryVersion && proposal.Result != nil {
			return proposal
		}
	}
	return nil
}

// ProductDiscussionHasChoice distinguishes explicit decisions from inheritance,
// including recovery whose proposal predates the immediately preceding failure.
// Rows saved before choiceDiscussionId retain their original replay semantics.
func ProductDiscussionHasChoice(selection *ProductSelection, discussionID, previousID string) bool {
	if selection == nil {
		return false
	}
	if selection.ChoiceDiscussionID != "" {
		return selection.ChoiceDiscussionID == discussionID
	}
	return selection.SourceDiscussionID == previousID
}

// PublicRepositoryURL accepts credential-free HTTP repository references.
func PublicRepositoryURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.Path != ""
}

// ProjectStageBasisConstraint is carried verbatim into the confirmed Stage.
// ProductBasisConstraintPreserved reports whether a compiled constraint keeps
// the stage's execution basis. Byte equality is the primary form; a copy that
// differs only in JSON escaping of the same object is accepted, because Go's
// HTML escaping (\u003c for "<") is a serialization choice, not basis content.
// Anything that changes, drops or adds a field is rejected.
func ProductBasisConstraintPreserved(constraint, basis string) bool {
	if constraint == basis {
		return true
	}
	_, constraintJSON, constraintOK := strings.Cut(constraint, "：")
	_, basisJSON, basisOK := strings.Cut(basis, "：")
	if !constraintOK || !basisOK {
		return false
	}
	var got, want any
	if json.Unmarshal([]byte(constraintJSON), &got) != nil || json.Unmarshal([]byte(basisJSON), &want) != nil {
		return false
	}
	canonicalGot, errGot := json.Marshal(got)
	canonicalWant, errWant := json.Marshal(want)
	return errGot == nil && errWant == nil && bytes.Equal(canonicalGot, canonicalWant)
}

func ProjectStageBasisConstraint(stage ProductStageDefinition) string {
	if stage.ExecutionBasis == nil {
		return ""
	}
	data, _ := json.Marshal(stage.ExecutionBasis)
	return "项目执行依据（仅规划，不授权运行命令）：" + string(data)
}
