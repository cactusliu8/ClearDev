package cleardev

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"
)

// ReviewCheckRequestKind identifies a bounded request, never a verdict.
const ReviewCheckRequestKind = "REVIEW_CHECK_REQUEST"

// ReviewCheckRequestResult contains only model-selected identifiers. Bindings and limits are owned
// by the control plane. This is not a review verdict or a parse correction.
type ReviewCheckRequestResult struct {
	SchemaVersion int      `json:"schemaVersion"`
	Kind          string   `json:"kind"`
	CheckIDs      []string `json:"checkIds"`
	Summary       string   `json:"summary"`
}

// ReviewCheckRequest freezes one request against an original candidate review.
type ReviewCheckRequest struct {
	// These optional identities are filled only from an authorized replacement's
	// immutable raw reply. They are never accepted from the model wire protocol.
	ReplacementRecoveryID string    `json:"replacementRecoveryId,omitempty"`
	RequestAttemptID      string    `json:"requestAttemptId,omitempty"`
	RequestResultID       string    `json:"requestResultId,omitempty"`
	ReviewID              string    `json:"reviewId"`
	CandidateID           string    `json:"candidateId"`
	CandidateSHA          string    `json:"candidateSha"`
	PacketSHA256          string    `json:"packetSha256"`
	RequestStepID         string    `json:"requestStepId"`
	CheckIDs              []string  `json:"checkIds"`
	RequestedAt           time.Time `json:"requestedAt"`
	// Only the backend may attach the admitted project contract. Its absence
	// preserves the historical mail-only request and evidence semantics.
	ProjectExecution *ProjectExecutionContract `json:"projectExecution,omitempty"`
}

// ReviewCheckEvidence records one trusted execution, including failures.
type ReviewCheckEvidence struct {
	ReviewID   string         `json:"reviewId"`
	CheckID    string         `json:"checkId"`
	Proof      MailCheckProof `json:"proof"`
	Outcome    string         `json:"outcome"`
	Output     string         `json:"output"`
	RecordedAt time.Time      `json:"recordedAt"`
	// Generic checks retain the same candidate-bound runtime receipt used by
	// task/integration checks, in addition to the historical summary fields.
	ProjectReceipt *ProjectCheckReceipt `json:"projectReceipt,omitempty"`
}

// ApprovedReviewCheck resolves only the four frozen mail check identifiers.
func ApprovedReviewCheck(id string) (ComplexCheckSpec, bool) {
	if !slices.Contains([]string{"demo-backend", "demo-api", "demo-frontend", "demo-integration"}, id) {
		return ComplexCheckSpec{}, false
	}
	return ComplexCheckByID(FrozenComplexCheckCatalog(), id)
}

// ParseReviewCheckRequest rejects commands, bindings and repeated check IDs.
func ParseReviewCheckRequest(raw []byte) (ReviewCheckRequestResult, error) {
	return parseReviewCheckRequest(raw, 1, ApprovedReviewCheck)
}

func parseReviewCheckRequest(raw []byte, version int, approved func(string) (ComplexCheckSpec, bool)) (ReviewCheckRequestResult, error) {
	var result ReviewCheckRequestResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw, "schemaVersion", "kind", "checkIds", "summary"); err != nil {
		return result, err
	}
	if result.SchemaVersion != version || result.Kind != ReviewCheckRequestKind || !validText(strings.TrimSpace(result.Summary), 10000) || len(result.CheckIDs) < 1 || len(result.CheckIDs) > 4 {
		return result, errors.New("invalid bounded Reviewer check request")
	}
	seen := map[string]bool{}
	for _, id := range result.CheckIDs {
		if _, ok := approved(id); !ok || seen[id] {
			return result, errors.New("reviewer check ID is not unique and preapproved")
		}
		seen[id] = true
	}
	sort.Strings(result.CheckIDs)
	return result, nil
}

// ReviewCheckRunID is stable across service and executor restarts.
func ReviewCheckRunID(reviewID, checkID string) string {
	return reviewID + ":requested-check:" + checkID
}

// ReviewCheckFollowupID identifies the separately budgeted final response step.
func ReviewCheckFollowupID(reviewID string) string { return reviewID + ":check-results" }

// ValidateReviewCheckEvidence rejects stale identities and false PASS receipts.
func ValidateReviewCheckEvidence(request ReviewCheckRequest, result ReviewCheckEvidence) error {
	spec, ok := ReviewCheckSpec(request, result.CheckID)
	p := result.Proof
	if !ok || !slices.Contains(request.CheckIDs, result.CheckID) || result.ReviewID != request.ReviewID || p.RunID != ReviewCheckRunID(request.ReviewID, result.CheckID) || p.CandidateSHA != request.CandidateSHA || !slices.Equal(p.Argv, spec.Argv) || result.RecordedAt.IsZero() || len(result.Output) > 65536 {
		return errors.New("REVIEW_CHECK_EVIDENCE_BINDING_INVALID")
	}
	if !slices.Contains([]string{"PASS", "FAIL", "TIMED_OUT", "INFRA_ERROR"}, result.Outcome) || p.Passed != (result.Outcome == "PASS") {
		return errors.New("REVIEW_CHECK_EVIDENCE_OUTCOME_INVALID")
	}
	if request.ProjectExecution != nil {
		return validateProjectReviewCheckEvidence(request, result)
	}
	if result.ProjectReceipt != nil {
		return errors.New("a historical Reviewer check cannot acquire a project receipt")
	}
	if p.Passed && (p.ExitCode != 0 || p.TimedOut || p.Truncated || !validProtocolSHA256(p.SourceManifestID) || !mailSHA1(p.SourceTreeOID) || p.ImageID == "" || !validProtocolSHA256(p.EnvironmentID) || !validProtocolSHA256(p.OutputSHA256)) {
		return errors.New("REVIEW_CHECK_EVIDENCE_FALSE_PASS")
	}
	return nil
}

// ReviewCheckReport serializes data, never shell or new instructions. Failed checks
// stay visible and cannot be converted to PASS by the final model response.
func ReviewCheckReport(request ReviewCheckRequest, results []ReviewCheckEvidence) (string, bool, error) {
	if len(results) != len(request.CheckIDs) || len(results) == 0 {
		return "", false, errors.New("REVIEW_CHECK_RESULTS_INCOMPLETE")
	}
	ordered := make([]ReviewCheckEvidence, 0, len(results))
	passed := true
	for _, id := range request.CheckIDs {
		found := false
		for _, result := range results {
			if result.CheckID != id {
				continue
			}
			if found {
				return "", false, errors.New("REVIEW_CHECK_RESULTS_DUPLICATED")
			}
			if err := ValidateReviewCheckEvidence(request, result); err != nil {
				return "", false, err
			}
			found = true
			passed = passed && result.Proof.Passed
			ordered = append(ordered, result)
		}
		if !found {
			return "", false, errors.New("REVIEW_CHECK_RESULTS_INCOMPLETE")
		}
	}
	raw, err := json.Marshal(struct {
		Request ReviewCheckRequest    `json:"request"`
		Results []ReviewCheckEvidence `json:"results"`
	}{request, ordered})
	return string(raw), passed, err
}
