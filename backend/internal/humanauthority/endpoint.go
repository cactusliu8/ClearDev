package humanauthority

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"runtime"
	"strings"
)

const maxEndpointBytes = 256

func NewDesktopRunID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate desktop run id: %w", err)
	}
	return "deskrun-" + hex.EncodeToString(raw), nil
}

// NewEndpoint returns the platform private channel name. Unix paths stay short
// because macOS sockaddr_un is limited to 103 bytes. Windows uses a named pipe
// bound to the desktop run id.
func NewEndpoint(desktopRunID string) (string, error) {
	desktopRunID = strings.TrimSpace(desktopRunID)
	if desktopRunID == "" {
		return "", fmt.Errorf("desktop run id is required")
	}
	if runtime.GOOS == "windows" {
		return `\\.\pipe\ao-human-` + desktopRunID, nil
	}
	raw := make([]byte, 4)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate human-authority socket name: %w", err)
	}
	endpoint := fmt.Sprintf("/tmp/ao-hum-%d-%s.sock", os.Getpid(), hex.EncodeToString(raw))
	if len(endpoint) > maxEndpointBytes {
		return "", fmt.Errorf("human-authority endpoint is too long")
	}
	return endpoint, nil
}
