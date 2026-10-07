package cleardev

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// AgentResultValidationError identifies a protocol error without copying the
// provider's reply into the error. Character and byte limits are distinct.
// It is diagnostic metadata, not permission to correct or retry a result.
type AgentResultValidationError struct {
	Code   string
	Field  string
	Actual int
	Limit  int
	Offset int64
	Policy string
}

func (e *AgentResultValidationError) Error() string {
	if e.Policy != "" {
		plain := *e
		plain.Policy = ""
		return e.Policy + ": " + plain.Error()
	}
	switch e.Code {
	case "RESULT_EMPTY":
		return fmt.Sprintf("%s: agent result has no JSON object (%d bytes)", e.Code, e.Actual)
	case "RESULT_TOO_LARGE":
		return fmt.Sprintf("%s: agent result is %d bytes; maximum %d bytes for the complete reply", e.Code, e.Actual, e.Limit)
	case "RESULT_ENCODING_INVALID":
		return e.Code + ": agent result is not valid UTF-8"
	case "RESULT_TEXT_EMPTY":
		return fmt.Sprintf("%s: field %s must be non-empty (0 Unicode characters)", e.Code, e.Field)
	case "RESULT_TEXT_TOO_LONG":
		return fmt.Sprintf("%s: field %s has %d Unicode characters; maximum %d", e.Code, e.Field, e.Actual, e.Limit)
	case "RESULT_JSON_INCOMPLETE":
		return fmt.Sprintf("%s: JSON ended before the object was complete, near byte %d", e.Code, e.Offset)
	case "RESULT_JSON_INVALID":
		return fmt.Sprintf("%s: invalid JSON near byte %d", e.Code, e.Offset)
	default:
		return "agent result does not satisfy the frozen protocol"
	}
}

func validateResultText(field, value string, maximum int) error {
	actual := utf8.RuneCountInString(value)
	if actual == 0 {
		return &AgentResultValidationError{Code: "RESULT_TEXT_EMPTY", Field: field, Limit: maximum}
	}
	if actual > maximum {
		return &AgentResultValidationError{Code: "RESULT_TEXT_TOO_LONG", Field: field, Actual: actual, Limit: maximum}
	}
	return nil
}

// Preserve structural errors such as duplicate/null/unknown fields. Syntax
// offsets and incomplete input never include the raw reply or string values.
func describeResultJSONError(raw []byte, err error) error {
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		// Token's scanner can restart within a document. Validate the complete
		// bounded input to obtain an offset relative to its original bytes.
		var document json.RawMessage
		wholeErr := json.Unmarshal(raw, &document)
		if errors.As(wholeErr, &syntax) {
			return &AgentResultValidationError{Code: "RESULT_JSON_INVALID", Offset: syntax.Offset}
		}
		return err
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return &AgentResultValidationError{Code: "RESULT_JSON_INCOMPLETE", Offset: int64(len(raw)) + 1}
	}
	return err
}

func prefixResultField(field string, err error) error {
	if err == nil {
		return nil
	}
	var diagnostic *AgentResultValidationError
	if errors.As(err, &diagnostic) {
		copied := *diagnostic
		if strings.HasPrefix(copied.Field, "[") || copied.Field == "" {
			copied.Field = field + copied.Field
		} else {
			copied.Field = field + "." + copied.Field
		}
		return &copied
	}
	return fmt.Errorf("%s: %w", field, err)
}
