package humanauthority

import (
	"bytes"
	"encoding/json"
	"net"
	"strings"
	"testing"
)

func TestReadBootstrapPlaintextIsBrowserOnly(t *testing.T) {
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadBootstrap(strings.NewReader(token + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got.BrowserRuntimeToken != token || got.HumanAuthorityEnabled {
		t.Fatalf("plaintext bootstrap = %+v", got)
	}
}

func TestReadBootstrapJSONEnablesHumanAuthority(t *testing.T) {
	browser, human := mustToken(t), mustToken(t)
	payload, err := json.Marshal(bootstrapJSON{
		SchemaVersion: 1, BrowserRuntimeToken: browser, HumanAuthorityToken: human,
		HumanAuthorityEndpoint: `\\.\pipe\ao-human-deskrun-1`, DesktopRunID: "deskrun-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadBootstrap(bytes.NewReader(append(payload, '\n')))
	if err != nil {
		t.Fatal(err)
	}
	if !got.HumanAuthorityEnabled || got.HumanAuthorityToken != human || got.DesktopRunID != "deskrun-1" {
		t.Fatalf("json bootstrap = %+v", got)
	}
}

func TestReadBootstrapRejectsExtraFieldsAndMatchingTokens(t *testing.T) {
	browser, human := mustToken(t), mustToken(t)
	valid := bootstrapJSON{
		SchemaVersion: 1, BrowserRuntimeToken: browser, HumanAuthorityToken: human,
		HumanAuthorityEndpoint: "/tmp/ao-hum-1-abcd.sock", DesktopRunID: "deskrun-1",
	}
	raw, _ := json.Marshal(valid)
	raw = []byte(strings.TrimSuffix(string(raw), "}") + `,"extra":true}`)
	if _, err := ReadBootstrap(bytes.NewReader(append(raw, '\n'))); err == nil {
		t.Fatal("extra bootstrap field was accepted")
	}
	valid.HumanAuthorityToken = browser
	raw, _ = json.Marshal(valid)
	if _, err := ReadBootstrap(bytes.NewReader(append(raw, '\n'))); err == nil {
		t.Fatal("matching tokens were accepted")
	}
}

func TestFramesRoundTripAndRejectOversize(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, map[string]any{"kind": "DESKTOP_HELLO", "n": 1}); err != nil {
		t.Fatal(err)
	}
	body, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte(`"DESKTOP_HELLO"`)) {
		t.Fatalf("frame body = %s", body)
	}
	if _, err := ReadFrame(bytes.NewReader(nil)); !isEOF(err) {
		t.Fatalf("empty reader err = %v", err)
	}
}

func TestHandshakeProofGoldenVector(t *testing.T) {
	tokenRaw := make([]byte, 32)
	desktopRaw := make([]byte, 32)
	daemonRaw := make([]byte, 32)
	for i := 0; i < 32; i++ {
		tokenRaw[i] = byte(i + 1)
		desktopRaw[i] = byte(i + 33)
		daemonRaw[i] = byte(i + 65)
	}
	token := EncodeToken(tokenRaw)
	desktop := EncodeToken(desktopRaw)
	daemon := EncodeToken(daemonRaw)
	proof, err := HandshakeProof(token, hmacDaemonRole, "deskrun-1", desktop, daemon)
	if err != nil {
		t.Fatal(err)
	}
	if proof != "sPxNNJbu5wgQtYCv54T_T3z2yt8fESdd2tZhR05B8J4" {
		t.Fatalf("daemon proof = %q", proof)
	}
	other, err := HandshakeProof(token, hmacDesktopRole, "deskrun-1", desktop, daemon)
	if err != nil {
		t.Fatal(err)
	}
	if other != "DlCYSK4gKczc1u1La3z5lWvvZvrV8SJPMJMp3khcxC4" {
		t.Fatalf("desktop proof = %q", other)
	}
}

func TestHandshakeAndProofMismatch(t *testing.T) {
	token, other := mustToken(t), mustToken(t)
	errCh := make(chan error, 2)
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	go func() { errCh <- PerformServerHandshake(server, token, "deskrun-1") }()
	go func() { errCh <- PerformClientHandshake(client, token, "deskrun-1") }()
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("matching handshake failed: %v", err)
		}
	}

	server2, client2 := net.Pipe()
	t.Cleanup(func() {
		_ = server2.Close()
		_ = client2.Close()
	})
	go func() { errCh <- PerformServerHandshake(server2, token, "deskrun-1") }()
	go func() { errCh <- PerformClientHandshake(client2, other, "deskrun-1") }()
	var sawError bool
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("mismatched HMAC was accepted")
	}
}

func mustToken(t *testing.T) string {
	t.Helper()
	token, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func isEOF(err error) bool { return err != nil }
