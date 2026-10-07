package humanauthority

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

const (
	BootstrapSchemaVersion = 1
	maxBootstrapBytes      = 4096
)

// Bootstrap is the versioned stdin payload Electron hands to a spawned daemon.
type Bootstrap struct {
	BrowserRuntimeToken    string
	HumanAuthorityToken    string
	HumanAuthorityEndpoint string
	DesktopRunID           string
	HumanAuthorityEnabled  bool
}

type bootstrapJSON struct {
	SchemaVersion          int    `json:"schemaVersion"`
	BrowserRuntimeToken    string `json:"browserRuntimeToken"`
	HumanAuthorityToken    string `json:"humanAuthorityToken"`
	HumanAuthorityEndpoint string `json:"humanAuthorityEndpoint"`
	DesktopRunID           string `json:"desktopRunId"`
}

// ReadBootstrap reads one stdin line. A strict v1 JSON object enables desktop
// human authority. A legacy plaintext token only restores the browser runtime.
func ReadBootstrap(r io.Reader) (Bootstrap, error) {
	line, err := bufio.NewReader(io.LimitReader(r, maxBootstrapBytes+1)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return Bootstrap{}, fmt.Errorf("read desktop bootstrap: %w", err)
	}
	payload := strings.TrimSpace(line)
	if payload == "" {
		return Bootstrap{}, errors.New("desktop bootstrap was empty")
	}
	if len(payload) > maxBootstrapBytes {
		return Bootstrap{}, errors.New("desktop bootstrap was too long")
	}
	if strings.HasPrefix(payload, "{") {
		return parseBootstrapJSON([]byte(payload))
	}
	if len(payload) > 256 {
		return Bootstrap{}, errors.New("browser runtime token handoff was too long")
	}
	return Bootstrap{BrowserRuntimeToken: payload}, nil
}

func parseBootstrapJSON(raw []byte) (Bootstrap, error) {
	if !utf8.Valid(raw) {
		return Bootstrap{}, errors.New("desktop bootstrap is not UTF-8")
	}
	if err := validateStrictObject(raw); err != nil {
		return Bootstrap{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var parsed bootstrapJSON
	if err := decoder.Decode(&parsed); err != nil {
		return Bootstrap{}, fmt.Errorf("decode desktop bootstrap: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Bootstrap{}, errors.New("desktop bootstrap must contain exactly one JSON object")
	}
	if parsed.SchemaVersion != BootstrapSchemaVersion {
		return Bootstrap{}, fmt.Errorf("desktop bootstrap schemaVersion %d is not supported", parsed.SchemaVersion)
	}
	if err := core.ValidateEncodedToken(parsed.BrowserRuntimeToken); err != nil {
		return Bootstrap{}, fmt.Errorf("browserRuntimeToken: %w", err)
	}
	if err := core.ValidateEncodedToken(parsed.HumanAuthorityToken); err != nil {
		return Bootstrap{}, fmt.Errorf("humanAuthorityToken: %w", err)
	}
	if parsed.BrowserRuntimeToken == parsed.HumanAuthorityToken {
		return Bootstrap{}, errors.New("human authority token must differ from the browser runtime token")
	}
	if strings.TrimSpace(parsed.HumanAuthorityEndpoint) == "" || strings.TrimSpace(parsed.DesktopRunID) == "" {
		return Bootstrap{}, errors.New("desktop bootstrap is missing the human-authority endpoint or run id")
	}
	if len(parsed.HumanAuthorityEndpoint) > 256 || len(parsed.DesktopRunID) > 128 {
		return Bootstrap{}, errors.New("desktop bootstrap endpoint or run id is too long")
	}
	return Bootstrap{
		BrowserRuntimeToken:    parsed.BrowserRuntimeToken,
		HumanAuthorityToken:    parsed.HumanAuthorityToken,
		HumanAuthorityEndpoint: parsed.HumanAuthorityEndpoint,
		DesktopRunID:           parsed.DesktopRunID,
		HumanAuthorityEnabled:  true,
	}, nil
}
