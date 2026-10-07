package ports

import "context"

// ClearDevTrialReceiptReader reads a settled action without starting or retrying
// any process. Absence/unknown is not successful execution.
type ClearDevTrialReceiptReader interface {
	ReadCandidateCheck(context.Context, ClearDevCheckRequest) (ClearDevCheckResult, bool, error)
}
