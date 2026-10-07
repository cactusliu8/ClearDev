package humanauthority

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

const tokenBytes = 32

// NewToken returns a 32-byte secret encoded as unpadded base64url.
func NewToken() (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate human-authority token: %w", err)
	}
	return EncodeToken(raw), nil
}

func EncodeToken(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

func DecodeToken(value string) ([]byte, error) {
	if err := validateEncoded(value); err != nil {
		return nil, err
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("human-authority token: %w", err)
	}
	if len(decoded) != tokenBytes {
		return nil, fmt.Errorf("human-authority token must be %d bytes", tokenBytes)
	}
	return decoded, nil
}

func validateEncoded(value string) error {
	if value == "" {
		return fmt.Errorf("human-authority token is empty")
	}
	for _, character := range value {
		if character == '+' || character == '/' || character == '=' {
			return fmt.Errorf("human-authority token must be unpadded base64url")
		}
	}
	return nil
}
