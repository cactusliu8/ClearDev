package humanauthority

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	core "github.com/aoagents/agent-orchestrator/backend/internal/cleardev"
)

const (
	ProtocolVersion  = 1
	KindDesktopHello = "DESKTOP_HELLO"
	KindDaemonHello  = "DAEMON_HELLO"
	KindDesktopProof = "DESKTOP_PROOF"
	handshakeTimeout = 5 * time.Second
	hmacDaemonRole   = "cleardev-human/daemon/v1"
	hmacDesktopRole  = "cleardev-human/desktop/v1"
)

type desktopHello struct {
	ProtocolVersion  int    `json:"protocolVersion"`
	Kind             string `json:"kind"`
	DesktopRunID     string `json:"desktopRunId"`
	DesktopChallenge string `json:"desktopChallenge"`
}

type daemonHello struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Kind            string `json:"kind"`
	DesktopRunID    string `json:"desktopRunId"`
	DaemonChallenge string `json:"daemonChallenge"`
	Proof           string `json:"proof"`
}

type desktopProof struct {
	ProtocolVersion int    `json:"protocolVersion"`
	Kind            string `json:"kind"`
	DesktopRunID    string `json:"desktopRunId"`
	Proof           string `json:"proof"`
}

func HandshakeProof(token, role, desktopRunID, desktopChallenge, daemonChallenge string) (string, error) {
	key, err := DecodeToken(token)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(role))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(desktopRunID))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(desktopChallenge))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(daemonChallenge))
	return EncodeToken(mac.Sum(nil)), nil
}

func PerformClientHandshake(rw io.ReadWriter, token, desktopRunID string) (err error) {
	clearDeadline := setHandshakeDeadline(rw)
	defer clearDeadline()
	defer func() { closeHandshakeOnError(rw, err) }()
	raw, err := ReadFrame(rw)
	if err != nil {
		return fmt.Errorf("read desktop hello: %w", err)
	}
	hello, err := parseDesktopHello(raw)
	if err != nil {
		return err
	}
	if hello.DesktopRunID != desktopRunID {
		return errors.New("desktop run id does not match the bootstrap payload")
	}
	daemonChallenge, err := NewToken()
	if err != nil {
		return err
	}
	proof, err := HandshakeProof(token, hmacDaemonRole, desktopRunID, hello.DesktopChallenge, daemonChallenge)
	if err != nil {
		return err
	}
	if err := WriteFrame(rw, daemonHello{
		ProtocolVersion: ProtocolVersion, Kind: KindDaemonHello, DesktopRunID: desktopRunID,
		DaemonChallenge: daemonChallenge, Proof: proof,
	}); err != nil {
		return err
	}
	proofRaw, err := ReadFrame(rw)
	if err != nil {
		return fmt.Errorf("read desktop proof: %w", err)
	}
	reply, err := parseDesktopProof(proofRaw)
	if err != nil {
		return err
	}
	if reply.DesktopRunID != desktopRunID {
		return errors.New("desktop proof run id does not match")
	}
	want, err := HandshakeProof(token, hmacDesktopRole, desktopRunID, hello.DesktopChallenge, daemonChallenge)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(reply.Proof), []byte(want)) {
		return errors.New("desktop proof is invalid")
	}
	return nil
}

func PerformServerHandshake(rw io.ReadWriter, token, desktopRunID string) (err error) {
	clearDeadline := setHandshakeDeadline(rw)
	defer clearDeadline()
	defer func() { closeHandshakeOnError(rw, err) }()
	desktopChallenge, err := NewToken()
	if err != nil {
		return err
	}
	if err := WriteFrame(rw, desktopHello{
		ProtocolVersion: ProtocolVersion, Kind: KindDesktopHello, DesktopRunID: desktopRunID,
		DesktopChallenge: desktopChallenge,
	}); err != nil {
		return err
	}
	raw, err := ReadFrame(rw)
	if err != nil {
		return fmt.Errorf("read daemon hello: %w", err)
	}
	hello, err := parseDaemonHello(raw)
	if err != nil {
		return err
	}
	if hello.DesktopRunID != desktopRunID {
		return errors.New("daemon hello run id does not match")
	}
	want, err := HandshakeProof(token, hmacDaemonRole, desktopRunID, desktopChallenge, hello.DaemonChallenge)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(hello.Proof), []byte(want)) {
		return errors.New("daemon proof is invalid")
	}
	proof, err := HandshakeProof(token, hmacDesktopRole, desktopRunID, desktopChallenge, hello.DaemonChallenge)
	if err != nil {
		return err
	}
	return WriteFrame(rw, desktopProof{
		ProtocolVersion: ProtocolVersion, Kind: KindDesktopProof, DesktopRunID: desktopRunID, Proof: proof,
	})
}

func parseDesktopHello(raw []byte) (desktopHello, error) {
	var hello desktopHello
	if err := decodeHandshake(raw, &hello, "protocolVersion", "kind", "desktopRunId", "desktopChallenge"); err != nil {
		return hello, err
	}
	if hello.ProtocolVersion != ProtocolVersion || hello.Kind != KindDesktopHello {
		return hello, errors.New("desktop hello is not protocol v1")
	}
	if strings.TrimSpace(hello.DesktopRunID) == "" {
		return hello, errors.New("desktop hello is missing the run id")
	}
	if err := core.ValidateEncodedToken(hello.DesktopChallenge); err != nil {
		return hello, fmt.Errorf("desktop challenge: %w", err)
	}
	return hello, nil
}

func parseDaemonHello(raw []byte) (daemonHello, error) {
	var hello daemonHello
	if err := decodeHandshake(raw, &hello, "protocolVersion", "kind", "desktopRunId", "daemonChallenge", "proof"); err != nil {
		return hello, err
	}
	if hello.ProtocolVersion != ProtocolVersion || hello.Kind != KindDaemonHello {
		return hello, errors.New("daemon hello is not protocol v1")
	}
	if strings.TrimSpace(hello.DesktopRunID) == "" {
		return hello, errors.New("daemon hello is missing the run id")
	}
	if err := core.ValidateEncodedToken(hello.DaemonChallenge); err != nil {
		return hello, fmt.Errorf("daemon challenge: %w", err)
	}
	if err := core.ValidateEncodedToken(hello.Proof); err != nil {
		return hello, fmt.Errorf("daemon proof: %w", err)
	}
	return hello, nil
}

func parseDesktopProof(raw []byte) (desktopProof, error) {
	var proof desktopProof
	if err := decodeHandshake(raw, &proof, "protocolVersion", "kind", "desktopRunId", "proof"); err != nil {
		return proof, err
	}
	if proof.ProtocolVersion != ProtocolVersion || proof.Kind != KindDesktopProof {
		return proof, errors.New("desktop proof is not protocol v1")
	}
	if strings.TrimSpace(proof.DesktopRunID) == "" {
		return proof, errors.New("desktop proof is missing the run id")
	}
	if err := core.ValidateEncodedToken(proof.Proof); err != nil {
		return proof, fmt.Errorf("desktop proof: %w", err)
	}
	return proof, nil
}

func decodeHandshake(raw []byte, out any, fields ...string) error {
	if err := validateStrictObject(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("handshake frame must contain exactly one JSON object")
	}
	var mapped map[string]json.RawMessage
	if err := json.Unmarshal(raw, &mapped); err != nil {
		return err
	}
	if len(mapped) != len(fields) {
		return errors.New("handshake frame has missing or extra fields")
	}
	for _, field := range fields {
		if _, ok := mapped[field]; !ok {
			return fmt.Errorf("handshake frame is missing field %q", field)
		}
	}
	return nil
}

func setHandshakeDeadline(rw io.ReadWriter) func() {
	conn, ok := rw.(net.Conn)
	if !ok {
		return func() {}
	}
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	return func() { _ = conn.SetDeadline(time.Time{}) }
}

func closeHandshakeOnError(rw io.ReadWriter, err error) {
	if err == nil {
		return
	}
	if conn, ok := rw.(net.Conn); ok {
		_ = conn.Close()
	}
}
