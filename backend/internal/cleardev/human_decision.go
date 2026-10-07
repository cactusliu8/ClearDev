package cleardev

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Human-decision protocol constants, including the S05 direction-change kind.
const (
	HumanDecisionProtocolVersion            = 1
	HumanDecisionKindConfirmVersion         = "CONFIRM_REQUIREMENT_VERSION"
	HumanDecisionKindApproveDirectionChange = "APPROVE_DIRECTION_CHANGE"
	HumanDecisionOfferKind                  = "HUMAN_DECISION_OFFER"
	HumanDecisionResultKind                 = "HUMAN_DECISION_RESULT"
	ConfirmRequirementBindingVersion        = 1
	ApproveDirectionChangeBindingVersion    = 1
	HumanDecisionOfferTTL                   = 10 * time.Minute
	RejectedByDesktopHumanReason            = "REJECTED_BY_DESKTOP_HUMAN"
)

// HumanDecisionChoice is the only result Electron may return.
type HumanDecisionChoice string

const (
	HumanDecisionApprove HumanDecisionChoice = "APPROVE"
	HumanDecisionReject  HumanDecisionChoice = "REJECT"
	HumanDecisionLater   HumanDecisionChoice = "LATER"
)

// HumanDecisionRequestStatus is the durable lifecycle of one request.
type HumanDecisionRequestStatus string

const (
	HumanDecisionRequestPending  HumanDecisionRequestStatus = "PENDING"
	HumanDecisionRequestResolved HumanDecisionRequestStatus = "RESOLVED"
)

// HumanDecisionDispatchOutcome records how one short-lived offer ended.
type HumanDecisionDispatchOutcome string

const (
	HumanDecisionDispatchOpen         HumanDecisionDispatchOutcome = ""
	HumanDecisionDispatchApproved     HumanDecisionDispatchOutcome = "APPROVE"
	HumanDecisionDispatchRejected     HumanDecisionDispatchOutcome = "REJECT"
	HumanDecisionDispatchLater        HumanDecisionDispatchOutcome = "LATER"
	HumanDecisionDispatchExpired      HumanDecisionDispatchOutcome = "EXPIRED"
	HumanDecisionDispatchDisconnected HumanDecisionDispatchOutcome = "DISCONNECTED"
	HumanDecisionDispatchInvalid      HumanDecisionDispatchOutcome = "INVALID"
)

// HumanDecisionDisplay is the immutable read-only content shown on the desktop.
type HumanDecisionDisplay struct {
	Title         string `json:"title"`
	Summary       string `json:"summary"`
	FullContent   string `json:"fullContent"`
	ChangeSummary string `json:"changeSummary"`
}

// ConfirmRequirementBinding is the S03/S04/S05 version-confirmation binding.
type ConfirmRequirementBinding struct {
	DevelopmentRequirementID string `json:"developmentRequirementId"`
	RequirementVersionID     string `json:"requirementVersionId"`
	RequirementVersionSHA256 string `json:"requirementVersionSha256"`
	TaskSetVersion           int64  `json:"taskSetVersion"`
}

// ApproveDirectionChangeBinding is the S05 direction-approval binding. It is
// registered at compile time and reuses the S03 envelope.
// ApproveDirectionChangeBinding is the exact desktop binding for a direction decision.
type ApproveDirectionChangeBinding struct {
	DevelopmentRequirementID string `json:"developmentRequirementId"`
	DirectionRequestID       string `json:"directionRequestId"`
	RequirementVersionID     string `json:"requirementVersionId"`
	RequirementVersionSHA256 string `json:"requirementVersionSha256"`
	TaskSetVersion           int64  `json:"taskSetVersion"`
	SnapshotSHA256           string `json:"snapshotSha256"`
}

// HumanDecisionRequest is the immutable pending-or-resolved request fact.
type HumanDecisionRequest struct {
	ID                       string
	DevelopmentRequirementID string
	DecisionKind             string
	BindingSchemaVersion     int
	BindingJSON              string
	DisplayJSON              string
	ContentSHA256            string
	Status                   HumanDecisionRequestStatus
	Decision                 HumanDecisionChoice
	ResolvedAt               *time.Time
	CreatedAt                time.Time
}

// HumanDecisionDispatch is one issued offer. The raw nonce is never stored.
type HumanDecisionDispatch struct {
	ID           string
	RequestID    string
	DesktopRunID string
	NonceSHA256  string
	IssuedAt     time.Time
	ExpiresAt    time.Time
	ConsumedAt   *time.Time
	Outcome      HumanDecisionDispatchOutcome
}

// HumanDecisionEffect ties a resolved request to the domain event it caused.
type HumanDecisionEffect struct {
	ID            string
	RequestID     string
	Decision      HumanDecisionChoice
	EventSequence int64
	CreatedAt     time.Time
}

// HumanDecisionOffer is the frozen generic envelope sent over the private channel.
type HumanDecisionOffer struct {
	ProtocolVersion      int                  `json:"protocolVersion"`
	Kind                 string               `json:"kind"`
	DesktopRunID         string               `json:"desktopRunId"`
	RequestID            string               `json:"requestId"`
	DecisionKind         string               `json:"decisionKind"`
	BindingSchemaVersion int                  `json:"bindingSchemaVersion"`
	Binding              json.RawMessage      `json:"binding"`
	ContentSHA256        string               `json:"contentSha256"`
	Nonce                string               `json:"nonce"`
	ExpiresAt            string               `json:"expiresAt"`
	Display              HumanDecisionDisplay `json:"display"`
}

// HumanDecisionResult is the frozen generic envelope Electron returns.
type HumanDecisionResult struct {
	ProtocolVersion      int                 `json:"protocolVersion"`
	Kind                 string              `json:"kind"`
	DesktopRunID         string              `json:"desktopRunId"`
	RequestID            string              `json:"requestId"`
	DecisionKind         string              `json:"decisionKind"`
	BindingSchemaVersion int                 `json:"bindingSchemaVersion"`
	Binding              json.RawMessage     `json:"binding"`
	ContentSHA256        string              `json:"contentSha256"`
	Nonce                string              `json:"nonce"`
	Decision             HumanDecisionChoice `json:"decision"`
}

// DecisionKindSpec is one compile-time registration. HTTP, Agent, and Electron
// cannot add specs. Apply is optional and used by tests for extra kinds.
type DecisionKindSpec struct {
	Kind                 string
	BindingSchemaVersion int
	ParseBinding         func([]byte) ([]byte, error)
	Allowed              map[HumanDecisionChoice]bool
}

// DecisionRegistry is a process-local whitelist.
type DecisionRegistry struct {
	specs map[string]DecisionKindSpec
}

func NewDecisionRegistry() *DecisionRegistry {
	return &DecisionRegistry{specs: make(map[string]DecisionKindSpec)}
}

func (r *DecisionRegistry) Register(spec DecisionKindSpec) error {
	if r == nil {
		return errors.New("decision registry is nil")
	}
	if strings.TrimSpace(spec.Kind) == "" || spec.BindingSchemaVersion <= 0 || spec.ParseBinding == nil || len(spec.Allowed) == 0 {
		return errors.New("decision kind spec is incomplete")
	}
	if _, exists := r.specs[spec.Kind]; exists {
		return fmt.Errorf("decision kind %q is already registered", spec.Kind)
	}
	r.specs[spec.Kind] = spec
	return nil
}

func (r *DecisionRegistry) Lookup(kind string) (DecisionKindSpec, bool) {
	if r == nil {
		return DecisionKindSpec{}, false
	}
	spec, ok := r.specs[kind]
	return spec, ok
}

func ProductionDecisionRegistry() *DecisionRegistry {
	registry := NewDecisionRegistry()
	if err := registry.Register(ProductPlanDecisionSpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(ConfirmRequirementVersionSpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(ApproveDirectionChangeSpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(ExtraMailAttemptSpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(FinalReviewRecheckSpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(PlanningRecoverySpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(ExtraReviewBudgetSpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(ExtraBuilderTurnSpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(StoppedCheckRecoverySpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(ExtraCoordinationSpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(CoordinationRepairSpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(ControlledEngineChangeSpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(ExtraPlanningAttemptSpec()); err != nil {
		panic(err)
	}
	if err := registry.Register(BuilderReplacementSpec()); err != nil {
		panic(err)
	}
	return registry
}

func ConfirmRequirementVersionSpec() DecisionKindSpec {
	return DecisionKindSpec{
		Kind:                 HumanDecisionKindConfirmVersion,
		BindingSchemaVersion: ConfirmRequirementBindingVersion,
		ParseBinding:         parseConfirmRequirementBindingJSON,
		Allowed: map[HumanDecisionChoice]bool{
			HumanDecisionApprove: true,
			HumanDecisionReject:  true,
			HumanDecisionLater:   true,
		},
	}
}

func parseConfirmRequirementBindingJSON(raw []byte) ([]byte, error) {
	binding, err := ParseConfirmRequirementBinding(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(binding)
}

func ParseConfirmRequirementBinding(raw []byte) (ConfirmRequirementBinding, error) {
	var binding ConfirmRequirementBinding
	if err := decodeStrictAgentResult(raw, &binding); err != nil {
		return binding, err
	}
	if _, err := requireJSONObjectFields(raw, "developmentRequirementId", "requirementVersionId", "requirementVersionSha256", "taskSetVersion"); err != nil {
		return binding, err
	}
	binding.DevelopmentRequirementID = strings.TrimSpace(binding.DevelopmentRequirementID)
	binding.RequirementVersionID = strings.TrimSpace(binding.RequirementVersionID)
	binding.RequirementVersionSHA256 = strings.TrimSpace(binding.RequirementVersionSHA256)
	if binding.DevelopmentRequirementID == "" || binding.RequirementVersionID == "" || !validProtocolSHA256(binding.RequirementVersionSHA256) {
		return binding, errors.New("confirm-requirement binding is incomplete")
	}
	if binding.TaskSetVersion < 0 {
		return binding, errors.New("task set version cannot be negative")
	}
	return binding, nil
}

func ParseHumanDecisionDisplay(raw []byte) (HumanDecisionDisplay, error) {
	var display HumanDecisionDisplay
	if err := decodeStrictAgentResult(raw, &display); err != nil {
		return display, err
	}
	if _, err := requireJSONObjectFields(raw, "title", "summary", "fullContent", "changeSummary"); err != nil {
		return display, err
	}
	display.Title = strings.TrimSpace(display.Title)
	display.Summary = strings.TrimSpace(display.Summary)
	display.FullContent = strings.TrimSpace(display.FullContent)
	display.ChangeSummary = strings.TrimSpace(display.ChangeSummary)
	if !validText(display.Title, 200) || !validText(display.Summary, 10000) ||
		!validText(display.FullContent, 32*1024) || !validText(display.ChangeSummary, 10000) {
		return display, errors.New("human decision display is incomplete")
	}
	return display, nil
}

func ConfirmRequirementDisplay(requirement DevelopmentRequirement, version RequirementVersion) HumanDecisionDisplay {
	return HumanDecisionDisplay{
		Title:         fmt.Sprintf("Confirm requirement version v%d", version.Version),
		Summary:       fmt.Sprintf("%s version %d is waiting for confirmation.", strings.TrimSpace(requirement.Name), version.Version),
		FullContent:   version.RequirementText,
		ChangeSummary: fmt.Sprintf("Submitted draft version %d for confirmation. Task-set version %d. SHA-256 %s.", version.Version, version.TaskSetVersion, version.SHA256),
	}
}

// ApproveDirectionChangeSpec registers the S05 desktop decision kind.
func ApproveDirectionChangeSpec() DecisionKindSpec {
	return DecisionKindSpec{
		Kind:                 HumanDecisionKindApproveDirectionChange,
		BindingSchemaVersion: ApproveDirectionChangeBindingVersion,
		ParseBinding:         parseApproveDirectionChangeBindingJSON,
		Allowed: map[HumanDecisionChoice]bool{
			HumanDecisionApprove: true,
			HumanDecisionReject:  true,
			HumanDecisionLater:   true,
		},
	}
}

func parseApproveDirectionChangeBindingJSON(raw []byte) ([]byte, error) {
	binding, err := ParseApproveDirectionChangeBinding(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(binding)
}

// ParseApproveDirectionChangeBinding accepts only the frozen S05 decision binding.
func ParseApproveDirectionChangeBinding(raw []byte) (ApproveDirectionChangeBinding, error) {
	var binding ApproveDirectionChangeBinding
	if err := decodeStrictAgentResult(raw, &binding); err != nil {
		return binding, err
	}
	if _, err := requireJSONObjectFields(raw,
		"developmentRequirementId", "directionRequestId", "requirementVersionId",
		"requirementVersionSha256", "taskSetVersion", "snapshotSha256"); err != nil {
		return binding, err
	}
	binding.DevelopmentRequirementID = strings.TrimSpace(binding.DevelopmentRequirementID)
	binding.DirectionRequestID = strings.TrimSpace(binding.DirectionRequestID)
	binding.RequirementVersionID = strings.TrimSpace(binding.RequirementVersionID)
	binding.RequirementVersionSHA256 = strings.TrimSpace(binding.RequirementVersionSHA256)
	binding.SnapshotSHA256 = strings.TrimSpace(binding.SnapshotSHA256)
	if binding.DevelopmentRequirementID == "" || binding.DirectionRequestID == "" ||
		binding.RequirementVersionID == "" || !validProtocolSHA256(binding.RequirementVersionSHA256) ||
		!validProtocolSHA256(binding.SnapshotSHA256) {
		return binding, errors.New("approve-direction-change binding is incomplete")
	}
	if binding.TaskSetVersion < 0 {
		return binding, errors.New("task set version cannot be negative")
	}
	return binding, nil
}

// ApproveDirectionChangeDisplay is the native dialog text for a direction decision.
func ApproveDirectionChangeDisplay(
	requirement DevelopmentRequirement,
	userMessage, stewardSummary, versionSHA, snapshotSHA string,
	taskCount int,
) HumanDecisionDisplay {
	return HumanDecisionDisplay{
		Title:       "Approve direction change",
		Summary:     fmt.Sprintf("%s asked to change direction for the current confirmed version.", strings.TrimSpace(requirement.Name)),
		FullContent: userMessage,
		ChangeSummary: fmt.Sprintf("%s Task count %d. v1 SHA-256 %s. Snapshot SHA-256 %s.",
			strings.TrimSpace(stewardSummary), taskCount, versionSHA, snapshotSHA),
	}
}

func HumanDecisionContentSHA256(decisionKind string, binding []byte, display HumanDecisionDisplay) (string, error) {
	displayJSON, err := json.Marshal(display)
	if err != nil {
		return "", err
	}
	material := struct {
		Binding      json.RawMessage `json:"binding"`
		DecisionKind string          `json:"decisionKind"`
		Display      json.RawMessage `json:"display"`
	}{Binding: append(json.RawMessage(nil), binding...), DecisionKind: decisionKind, Display: displayJSON}
	raw, err := json.Marshal(material)
	if err != nil {
		return "", err
	}
	return sha256Hex(raw), nil
}

func ParseHumanDecisionOffer(raw []byte, registry *DecisionRegistry) (HumanDecisionOffer, error) {
	var offer HumanDecisionOffer
	if err := decodeStrictAgentResult(raw, &offer); err != nil {
		return offer, err
	}
	fields, err := requireJSONObjectFields(raw,
		"protocolVersion", "kind", "desktopRunId", "requestId", "decisionKind",
		"bindingSchemaVersion", "binding", "contentSha256", "nonce", "expiresAt", "display")
	if err != nil {
		return offer, err
	}
	if _, err := requireJSONObjectFields(fields["display"], "title", "summary", "fullContent", "changeSummary"); err != nil {
		return offer, fmt.Errorf("human decision display: %w", err)
	}
	if err := validateHumanDecisionEnvelope(registry, offer.ProtocolVersion, offer.Kind, HumanDecisionOfferKind,
		offer.DesktopRunID, offer.RequestID, offer.DecisionKind, offer.BindingSchemaVersion, offer.Binding, offer.ContentSHA256, offer.Nonce); err != nil {
		return offer, err
	}
	display, err := ParseHumanDecisionDisplay(fields["display"])
	if err != nil {
		return offer, err
	}
	offer.Display = display
	spec, _ := registry.Lookup(offer.DecisionKind)
	normalized, err := spec.ParseBinding(offer.Binding)
	if err != nil {
		return offer, err
	}
	offer.Binding = normalized
	digest, err := HumanDecisionContentSHA256(offer.DecisionKind, offer.Binding, offer.Display)
	if err != nil {
		return offer, err
	}
	if digest != offer.ContentSHA256 {
		return offer, errors.New("human decision content hash does not match the display and binding")
	}
	if _, err := time.Parse(time.RFC3339, offer.ExpiresAt); err != nil {
		return offer, fmt.Errorf("human decision expiry is not RFC 3339: %w", err)
	}
	return offer, nil
}

func ParseHumanDecisionResult(raw []byte, registry *DecisionRegistry) (HumanDecisionResult, error) {
	var result HumanDecisionResult
	if err := decodeStrictAgentResult(raw, &result); err != nil {
		return result, err
	}
	if _, err := requireJSONObjectFields(raw,
		"protocolVersion", "kind", "desktopRunId", "requestId", "decisionKind",
		"bindingSchemaVersion", "binding", "contentSha256", "nonce", "decision"); err != nil {
		return result, err
	}
	if err := validateHumanDecisionEnvelope(registry, result.ProtocolVersion, result.Kind, HumanDecisionResultKind,
		result.DesktopRunID, result.RequestID, result.DecisionKind, result.BindingSchemaVersion, result.Binding, result.ContentSHA256, result.Nonce); err != nil {
		return result, err
	}
	spec, _ := registry.Lookup(result.DecisionKind)
	if !spec.Allowed[result.Decision] {
		return result, fmt.Errorf("decision %q is not allowed for kind %q", result.Decision, result.DecisionKind)
	}
	normalized, err := spec.ParseBinding(result.Binding)
	if err != nil {
		return result, err
	}
	result.Binding = normalized
	return result, nil
}

func validateHumanDecisionEnvelope(
	registry *DecisionRegistry,
	protocolVersion int, kind, wantKind, desktopRunID, requestID, decisionKind string,
	bindingSchemaVersion int, binding json.RawMessage, contentSHA, nonce string,
) error {
	if protocolVersion != HumanDecisionProtocolVersion || kind != wantKind {
		return errors.New("human decision envelope version or kind is not supported")
	}
	if strings.TrimSpace(desktopRunID) == "" || strings.TrimSpace(requestID) == "" {
		return errors.New("human decision envelope is missing identifiers")
	}
	spec, ok := registry.Lookup(decisionKind)
	if !ok {
		return fmt.Errorf("human decision kind %q is not registered", decisionKind)
	}
	if bindingSchemaVersion != spec.BindingSchemaVersion {
		return fmt.Errorf("human decision binding schema version %d does not match kind %q", bindingSchemaVersion, decisionKind)
	}
	if !validProtocolSHA256(contentSHA) {
		return errors.New("human decision content hash is invalid")
	}
	if err := ValidateEncodedToken(nonce); err != nil {
		return fmt.Errorf("human decision nonce: %w", err)
	}
	if len(binding) == 0 {
		return errors.New("human decision binding is required")
	}
	return nil
}

// ValidateEncodedToken accepts a 32-byte value encoded with unpadded Base64 URL.
func ValidateEncodedToken(value string) error {
	if strings.ContainsAny(value, "+/=") {
		return errors.New("token is not unpadded base64url")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return fmt.Errorf("token is not unpadded base64url: %w", err)
	}
	if len(decoded) != 32 {
		return errors.New("token must be 32 bytes")
	}
	return nil
}

func ResultMatchesOffer(result HumanDecisionResult, offer HumanDecisionOffer) error {
	if result.ProtocolVersion != offer.ProtocolVersion || result.DesktopRunID != offer.DesktopRunID ||
		result.RequestID != offer.RequestID || result.DecisionKind != offer.DecisionKind ||
		result.BindingSchemaVersion != offer.BindingSchemaVersion || result.ContentSHA256 != offer.ContentSHA256 ||
		result.Nonce != offer.Nonce || string(result.Binding) != string(offer.Binding) {
		return errors.New("human decision result does not echo the issued offer")
	}
	return nil
}

func DecodeEncodedToken(value string) ([]byte, error) {
	if err := ValidateEncodedToken(value); err != nil {
		return nil, err
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

func HumanDecisionNonceSHA256(nonce string) (string, error) {
	raw, err := DecodeEncodedToken(nonce)
	if err != nil {
		return "", fmt.Errorf("human decision nonce: %w", err)
	}
	return sha256Hex(raw), nil
}

// IssueHumanDecisionDispatchCommand issues one short-lived offer for a pending request.
type IssueHumanDecisionDispatchCommand struct {
	ReopenRequestID string
	InitialOnly     bool
	RequestID       string
	DesktopRunID    string
	Nonce           string
	IssuedAt        time.Time
	ExpiresAt       time.Time
}

// DismissHumanDecisionDispatchCommand records how one offer ended without settling.
type DismissHumanDecisionDispatchCommand struct {
	Nonce        string
	DesktopRunID string
	Outcome      HumanDecisionDispatchOutcome
	At           time.Time
}
