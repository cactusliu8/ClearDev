package cleardev

import (
	"bytes"
	"encoding/json"
	"errors"
)

// boundAgentResultFields are identifiers and content hashes the control plane
// already holds for the current step. Agent JSON must not contain them
// (ADR-0011). Persistence structs still have these fields; parsers assign them
// from function arguments after a successful decode.
var boundAgentResultFields = []string{
	"compilationRequestId",
	"requirementVersionId",
	"compilationContextSha256",
	"clarificationRound",
	"planningRequestId",
	"requirementVersionSha256",
	"requirementSha256",
	"compilationSha256",
	"reviewRequestId",
	"planId",
	"planSha256",
	"requestId",
	"dispatchId",
	"taskId",
	"round",
	"executionRunId",
	"triggerReason",
	"triggerFactId",
	"taskSetVersion",
	"integrationCommitSha",
	"reviewAssignmentId",
	"candidateId",
	"candidateSha",
	"reviewPacketSha256",
	"developmentRequirementId",
	"phase",
	"attention",
	"directionRequestId",
	"currentRequirementVersionId",
	"currentRequirementSha256",
	"userMessageSha256",
	"factSummarySha256",
	"nextOwnerRole",
	"pendingDecision",
	"baseTaskSetVersion",
}

// MarshalAgentChosenResult encodes an Agent result without bound identifiers or
// content hashes. Tests and prompt examples use this so they do not ask Agents
// to copy control-plane facts. Key order from the original struct marshal is
// preserved so prompt extractors still find the root object.
func MarshalAgentChosenResult(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return omitBoundAgentResultFields(encoded)
}

func omitBoundAgentResultFields(raw []byte) ([]byte, error) {
	skip := make(map[string]struct{}, len(boundAgentResultFields))
	for _, field := range boundAgentResultFields {
		skip[field] = struct{}{}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("agent result must be a JSON object")
	}
	var out bytes.Buffer
	out.WriteByte('{')
	first := true
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("agent result has a non-string object key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if _, omitted := skip[key]; omitted {
			continue
		}
		if !first {
			out.WriteByte(',')
		}
		encodedKey, err := json.Marshal(key)
		if err != nil {
			return nil, err
		}
		out.Write(encodedKey)
		out.WriteByte(':')
		out.Write(value)
		first = false
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}
