package middleware

// These create routes already have authoritative, audited idempotency receipts.
// Replaying the transport cache would skip current service authorization and
// could return success for a different body under the same key. Match Gin's
// registered full path (including parameter names), not caller-controlled IDs.
func durableCommandEndpoint(path string) bool {
	switch path {
	case "/v1/operations", "/v1/audit/replays", "/v1/audit/export",
		"/v1/command-intents", "/v1/command-intents/:id/execute",
		"/v1/me/sync", "/v1/me/export", "/v1/me/erase",
		"/v1/admin/accounts/:id/sync", "/v1/admin/accounts/:id/export", "/v1/admin/accounts/:id/erase":
		return true
	default:
		return false
	}
}
