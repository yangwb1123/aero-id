package repository

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/aero/aero-id/internal/pkg/idgen"
)

type auditCursor struct {
	OccurredAt time.Time
	ID         string
}

func encodeAuditCursor(fact *auditLedgerRow) string {
	if fact == nil {
		return ""
	}
	value := fact.OccurredAt.UTC().Format(time.RFC3339Nano) + "\x00" + fact.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func decodeAuditCursor(value string) (auditCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return auditCursor{}, err
	}
	parts := strings.Split(string(decoded), "\x00")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return auditCursor{}, fmt.Errorf("invalid audit cursor")
	}
	at, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return auditCursor{}, err
	}
	if _, err := idgen.Parse(parts[1]); err != nil {
		return auditCursor{}, err
	}
	return auditCursor{OccurredAt: at, ID: parts[1]}, nil
}
