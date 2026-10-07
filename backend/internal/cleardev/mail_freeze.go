package cleardev

import (
	"encoding/json"
	"errors"
)

// MailFreezePolicyV1 moves candidate publication to the trusted executor, not
// to a model with write access to Git metadata or the control database.
const MailFreezePolicyV1 = "TRUSTED_MAIL_FREEZE_V1"

// BindMailFreezePolicy applies only to new, bounded mail execution envelopes.
func BindMailFreezePolicy(raw []byte) ([]byte, string, error) {
	var pkg ComplexExecutionRunPackage
	if err := json.Unmarshal(raw, &pkg); err != nil {
		return nil, "", err
	}
	if (pkg.DeliveryPolicy != MailDeliveryPolicyV1 && pkg.DeliveryPolicy != MailDeliveryPolicyV2) || pkg.AttemptPolicy != MailAttemptPolicyV1 {
		return nil, "", errors.New("trusted freeze requires supported bounded mail delivery")
	}
	pkg.FreezePolicy = MailFreezePolicyV1
	encoded, err := marshalCanonicalJSON(pkg)
	return encoded, sha256Hex(encoded), err
}

// TrustedMailFreeze is false for historical executions, including stopped demos.
func TrustedMailFreeze(run ComplexExecutionRun) bool {
	if !BoundedMailAttempts(run) {
		return false
	}
	var pkg ComplexExecutionRunPackage
	return json.Unmarshal([]byte(run.ExecutionPackageJSON), &pkg) == nil && pkg.FreezePolicy == MailFreezePolicyV1
}
