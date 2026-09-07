package connector

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

func (c *JSONSummaryConnector) decodeSummary(ctx context.Context, body io.Reader) (*Summary, error) {
	limited := io.LimitReader(body, c.cfg.MaxBodyBytes+1)
	var payload summaryPayload
	data, err := io.ReadAll(limited)
	if err != nil {
		kind := ErrorUnavailable
		if requestTimedOut(ctx, err) {
			kind = ErrorTimeout
		}
		return nil, &RemoteError{Source: c.source, Kind: kind,
			Code: "source.response_read_failed", Temporary: true, Cause: redactedRemoteCause(err)}
	}
	if int64(len(data)) > c.cfg.MaxBodyBytes {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.response_too_large"}
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, &RemoteError{Source: c.source, Kind: ErrorInvalid, Code: "source.response_invalid", Cause: redactedRemoteCause(err)}
	}
	return c.summaryFromPayload(&payload)
}

type summaryPayload struct {
	SourceRegion string                     `json:"source_region"`
	Version      int64                      `json:"version"`
	GeneratedAt  time.Time                  `json:"generated_at"`
	Complete     bool                       `json:"complete"`
	Coverage     *summaryCoveragePayload    `json:"coverage"`
	Datasets     map[string]json.RawMessage `json:"datasets"`
	Sources      []summarySourcePayload     `json:"sources"`
	Memberships  []summaryMembershipPayload `json:"memberships"`
}

type summaryCoveragePayload struct {
	Datasets    []string `json:"datasets"`
	Sources     bool     `json:"sources"`
	Memberships bool     `json:"memberships"`
}

type summarySourcePayload struct {
	SourceAccountID string         `json:"source_account_id"`
	ScopeType       string         `json:"scope_type"`
	ScopeID         string         `json:"scope_id"`
	Status          string         `json:"status"`
	Data            map[string]any `json:"data"`
}

type summaryMembershipPayload struct {
	ScopeType      string         `json:"scope_type"`
	ScopeID        string         `json:"scope_id"`
	SourceMemberID string         `json:"source_member_id"`
	Role           string         `json:"role"`
	Status         string         `json:"status"`
	Data           map[string]any `json:"data"`
}
