package repository

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aero/aero-id/internal/domain"
)

func TestCommandIntentCodecPreservesExactIntentNumbersAndRejectsOverflow(t *testing.T) {
	intent := &domain.CommandIntent{ID: domain.ID{1}, AccountID: domain.ID{2}, Version: 1, Status: "prepared",
		Principal: domain.CommandPrincipal{ActorUID: "actor", TenantID: "tenant", Issuer: "https://snaplink.example", Subject: "opaque", ClientID: "console"},
		Plan:      domain.CommandPlan{Kind: "operation", RequestPayload: map[string]any{"version": json.Number("9007199254740993")}}}
	row, err := commandIntentToRow(intent)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := commandIntentFromRow(row)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Principal != intent.Principal || decoded.Plan.RequestPayload["version"] != json.Number("9007199254740993") {
		t.Fatal("codec lost principal or version precision")
	}
	intent.Plan.RequestPayload["note"] = strings.Repeat("x", 256*1024)
	if _, err := commandIntentToRow(intent); !errors.Is(err, ErrCommandIntentTooLarge) {
		t.Fatal("oversized durable metadata accepted", err)
	}
}
