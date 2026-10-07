package ports

import "context"

// ClearDevCandidateHandoffError is produced by the confined candidate collector,
// not by model text. BeforePublication is true only before a new freeze receipt
// or branch update could exist. An untyped error never establishes that fact.
type ClearDevCandidateHandoffError struct {
	Cause             error
	ProblemCode       string
	Path              string
	BeforePublication bool
}

func (e *ClearDevCandidateHandoffError) Error() string {
	return e.ProblemCode + ": " + e.Path
}
func (e *ClearDevCandidateHandoffError) Unwrap() error { return e.Cause }

// ClearDevProjectCandidateInspector uses the Node project's non-delivery
// runtime policy, while retaining every tracked change and every source path.
// Only the admitted project path may use it; ordinary source discovery keeps
// the strict InspectCandidate behavior.
type ClearDevProjectCandidateInspector interface {
	InspectProjectCandidate(context.Context, string, string) (ClearDevCandidateInspection, error)
}
