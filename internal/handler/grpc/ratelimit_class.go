package grpc

import "strings"

// grpcRateLimitSensitiveMethods mirrors the HTTP P6-07 failure policy for
// control-plane RPCs whose work is security-, cost-, or replay-sensitive.
var grpcRateLimitSensitiveMethods = []string{
	"/aero_id.v1.AdminService/ListAudit",
	"/aero_id.v1.AuditLedgerService/ListAuditFacts",
	"/aero_id.v1.OperationService/CreateOperation",
	"/aero_id.v1.OperationService/RunOperation",
	"/aero_id.v1.OperationService/ReconcileOperation",
	"/aero_id.v1.CallbackService/ReplayCallbackDelivery",
	"/aero_id.v1.SyncJobService/CreateSyncJob",
	"/aero_id.v1.SyncJobService/ReconcileSyncJob",
	"/aero_id.v1.SourceIngestService/IngestEvents",
}

func isGRPCRateLimitSensitive(method string) bool {
	for _, prefix := range grpcRateLimitSensitiveMethods {
		if method == prefix || strings.HasPrefix(method, prefix+"/") {
			return true
		}
	}
	return false
}
