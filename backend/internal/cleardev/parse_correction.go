package cleardev

import "time"

// ParseCorrection records that one Agent step already sent its single
// mechanical JSON correction. The original invalid text is never rewritten.
type ParseCorrection struct {
	StepID          string
	AttemptNumber   int64
	ClientMessageID string
	PromptText      string
	PromptSHA256    string
	SentAt          time.Time
}
