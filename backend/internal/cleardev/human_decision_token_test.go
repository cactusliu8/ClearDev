package cleardev

import "encoding/base64"

func encodeTestToken(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}
