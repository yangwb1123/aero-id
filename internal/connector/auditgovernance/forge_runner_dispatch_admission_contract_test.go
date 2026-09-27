package auditgovernance

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

// This receiver consumes only the bounded lease recheck projection. It does
// not issue a lease, authorize a command, contact a Runner, or publish Audit.
//
//go:embed testdata/forge-runner-dispatch-admission-v1.json
var forgeRunnerDispatchAdmissionFixture []byte

type forgeRunnerDispatchAdmissionOwner struct {
	Issuer   string `json:"issuer"`
	Subject  string `json:"subject"`
	TenantID string `json:"tenant_id"`
}
type forgeRunnerDispatchAdmissionAuthority struct {
	DeviceIdentityVerified bool `json:"device_identity_verified"`
	ReservationCreated     bool `json:"reservation_created"`
	ExecutionAuthorized    bool `json:"execution_authorized"`
	DispatchPerformed      bool `json:"dispatch_performed"`
	AuditPublished         bool `json:"audit_published"`
}
type forgeRunnerDispatchAdmissionObservation struct {
	SchemaVersion          string                                `json:"schema_version"`
	EvaluationMode         string                                `json:"evaluation_mode"`
	Owner                  forgeRunnerDispatchAdmissionOwner     `json:"owner"`
	ConversationID         string                                `json:"conversation_id"`
	RunID                  string                                `json:"run_id"`
	AttemptID              string                                `json:"attempt_id"`
	AttemptState           string                                `json:"attempt_state"`
	AttemptStateAdmissible bool                                  `json:"attempt_state_admissible"`
	CommandID              string                                `json:"command_id"`
	CommandSHA256          string                                `json:"command_sha256"`
	TargetID               string                                `json:"target_id"`
	LeaseEpoch             uint64                                `json:"lease_epoch"`
	LeaseIssuedAtMS        uint64                                `json:"lease_issued_at_ms"`
	LeaseExpiresAtMS       uint64                                `json:"lease_expires_at_ms"`
	EvaluatedAtMS          uint64                                `json:"evaluated_at_ms"`
	LeaseProofCurrent      bool                                  `json:"lease_proof_current"`
	LeaseActive            bool                                  `json:"lease_active"`
	CommandBindingValid    bool                                  `json:"command_binding_valid"`
	AdmissionReady         bool                                  `json:"admission_ready"`
	RejectionReasons       []string                              `json:"rejection_reasons"`
	PreviewOnly            bool                                  `json:"preview_only"`
	Authority              forgeRunnerDispatchAdmissionAuthority `json:"authority"`
}

func decodeForgeRunnerDispatchAdmission(raw []byte) (forgeRunnerDispatchAdmissionObservation, error) {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return forgeRunnerDispatchAdmissionObservation{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value forgeRunnerDispatchAdmissionObservation
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return value, err
	}
	if err := value.validate(); err != nil {
		return value, err
	}
	return value, nil
}

func (value forgeRunnerDispatchAdmissionObservation) validate() error {
	if value.SchemaVersion != "forge.runner-dispatch-admission/v1" || value.EvaluationMode != "durable_lease_bound_dispatch_admission_preview" ||
		!validForgeRunnerDispatchAdmissionOwner(value.Owner) || !validForgeRunnerDispatchAdmissionIdentifier(value.ConversationID) || !validForgeRunnerDispatchAdmissionIdentifier(value.RunID) || !validForgeRunnerDispatchAdmissionIdentifier(value.AttemptID) ||
		!validForgeRunnerDispatchAdmissionIdentifier(value.CommandID) || !validForgeRunnerDispatchAdmissionDigest(value.CommandSHA256) || !validForgeRunnerDispatchAdmissionIdentifier(value.TargetID) ||
		!validForgeRunnerDispatchAdmissionState(value.AttemptState) || value.AttemptStateAdmissible != validForgeRunnerDispatchAdmissionDispatchable(value.AttemptState) || value.LeaseEpoch == 0 || value.LeaseIssuedAtMS == 0 || value.LeaseExpiresAtMS <= value.LeaseIssuedAtMS || value.EvaluatedAtMS == 0 || value.EvaluatedAtMS > 9007199254740991 ||
		value.AdmissionReady != (value.CommandBindingValid && value.LeaseProofCurrent && value.LeaseActive && value.AttemptStateAdmissible) || !validForgeRunnerDispatchAdmissionReasons(value.RejectionReasons, value.CommandBindingValid, value.LeaseProofCurrent, value.LeaseActive, value.AttemptStateAdmissible) || !value.PreviewOnly || value.Authority != (forgeRunnerDispatchAdmissionAuthority{}) {
		return errors.New("invalid Runner dispatch admission")
	}
	return nil
}
func validForgeRunnerDispatchAdmissionOwner(value forgeRunnerDispatchAdmissionOwner) bool {
	return validForgeRunnerDispatchAdmissionOwnerPart(value.Issuer) && validForgeRunnerDispatchAdmissionOwnerPart(value.Subject) && validForgeRunnerDispatchAdmissionOwnerPart(value.TenantID)
}
func validForgeRunnerDispatchAdmissionOwnerPart(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n")
}
func validForgeRunnerDispatchAdmissionIdentifier(value string) bool {
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') {
			continue
		}
		if index == 0 || !strings.ContainsRune("._:+/-", char) {
			return false
		}
	}
	return value != ""
}
func validForgeRunnerDispatchAdmissionDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}
func validForgeRunnerDispatchAdmissionState(value string) bool {
	switch value {
	case "requested", "accepted", "starting", "running", "interrupted", "completed", "failed", "uncertain":
		return true
	}
	return false
}
func validForgeRunnerDispatchAdmissionDispatchable(value string) bool {
	return value == "accepted" || value == "starting" || value == "running"
}
func validForgeRunnerDispatchAdmissionReasons(values []string, command, current, active, state bool) bool {
	expected := []string{}
	if !command {
		expected = append(expected, "command_binding_invalid")
	}
	if !current {
		expected = append(expected, "lease_proof_not_current")
	}
	if !active {
		expected = append(expected, "lease_inactive_at_evaluated_time")
	}
	if !state {
		expected = append(expected, "attempt_state_not_dispatchable")
	}
	if len(values) != len(expected) {
		return false
	}
	for index, value := range values {
		if value != expected[index] {
			return false
		}
	}
	return true
}

func TestForgeRunnerDispatchAdmissionCanonicalObservation(t *testing.T) {
	value, err := decodeForgeRunnerDispatchAdmission(forgeRunnerDispatchAdmissionFixture)
	if err != nil {
		t.Fatal(err)
	}
	if value.Owner.Subject != "user-1" || value.ConversationID != "conversation-1" || value.TargetID != "runner-a" || !value.AdmissionReady || value.Authority != (forgeRunnerDispatchAdmissionAuthority{}) {
		t.Fatalf("unexpected admission observation: %#v", value)
	}
}
func TestForgeRunnerDispatchAdmissionRejectsUnknownDuplicateAndAuthority(t *testing.T) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(forgeRunnerDispatchAdmissionFixture, &root); err != nil {
		t.Fatal(err)
	}
	root["unexpected"] = json.RawMessage(`true`)
	unknown, _ := json.Marshal(root)
	if _, err := decodeForgeRunnerDispatchAdmission(unknown); err == nil {
		t.Fatal("unknown field accepted")
	}
	duplicate := strings.TrimSuffix(strings.TrimSpace(string(forgeRunnerDispatchAdmissionFixture)), "}") + `,"schema_version":"forge.runner-dispatch-admission/v1"}`
	if _, err := decodeForgeRunnerDispatchAdmission([]byte(duplicate)); err == nil {
		t.Fatal("duplicate field accepted")
	}
	authority := strings.Replace(string(forgeRunnerDispatchAdmissionFixture), `"execution_authorized": false`, `"execution_authorized": true`, 1)
	if _, err := decodeForgeRunnerDispatchAdmission([]byte(authority)); err == nil {
		t.Fatal("authority mutation accepted")
	}
}
