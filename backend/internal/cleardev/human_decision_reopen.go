package cleardev

import "time"

// HumanDecisionReopen is an immutable request to display one existing decision again.
// NextDispatchID is allocated before dispatch; it conveys no decision capability.
type HumanDecisionReopen struct {
	RequestID          string    `json:"requestId"`
	DecisionRequestID  string    `json:"decisionRequestId"`
	ContentSHA256      string    `json:"contentSha256"`
	PreviousDispatchID string    `json:"previousDispatchId"`
	DesktopRunID       string    `json:"-"`
	NextDispatchID     string    `json:"nextDispatchId"`
	CreatedAt          time.Time `json:"createdAt"`
}
