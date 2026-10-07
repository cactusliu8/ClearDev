package cleardev

import (
	"errors"
	"strings"
)

var ErrInvalidCommitSHA = errors.New("cleardev: commit SHA must be 40 or 64 hexadecimal characters")

// NormalizeCommitSHA accepts only a complete SHA-1 or SHA-256 Git object ID
// and returns its canonical lowercase representation.
func NormalizeCommitSHA(value string) (string, error) {
	if len(value) != 40 && len(value) != 64 {
		return "", ErrInvalidCommitSHA
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') && !(r >= 'A' && r <= 'F') {
			return "", ErrInvalidCommitSHA
		}
	}
	return strings.ToLower(value), nil
}

// IsFullCommitSHA reports whether value is an accepted complete Git object ID.
func IsFullCommitSHA(value string) bool {
	_, err := NormalizeCommitSHA(value)
	return err == nil
}
