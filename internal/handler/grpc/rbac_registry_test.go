package grpc

// rbac_registry_test.go — drift guard for the gRPC method→permission registry
// (requiredPermForMethod in server.go) and the PermissionInterceptor behavior
// contract. The registry is the only transport-level RBAC gate on role-grant,
// audit-read, operation, sync, callback-management, and event-ingest RPCs; a
// dead key silently fails open and a typo'd permission silently breaks or
// over-grants. These tests pin both directions against the registered proto
// surface and the authoritative permission vocabulary. Test-only.

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	_ "github.com/aero/aero-id/internal/handler/grpc/aeropb" // registers aero_id.v1 file descriptors into protoregistry.GlobalFiles
	"github.com/aero/aero-id/internal/middleware"
	"github.com/aero/aero-id/internal/service"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// pinnedGatedMethods is the exact gRPC RBAC contract: every full method the
// registry must gate. Adding or removing a registry entry without updating this
// list (or vice versa) fails TestRequiredPermForMethod_KeysResolveToRealMethods.
var pinnedGatedMethods = []string{
	"/aero_id.v1.AdminService/ListAudit",
	"/aero_id.v1.AdminService/ListRoles",
	"/aero_id.v1.AdminService/ListUserRoles",
	"/aero_id.v1.AdminService/Ban",
	"/aero_id.v1.AdminService/Unban",
	"/aero_id.v1.AdminService/ListBans",
	"/aero_id.v1.AdminService/GrantRole",
	"/aero_id.v1.AdminService/RevokeRole",
	"/aero_id.v1.AuditLedgerService/ListAuditFacts",
	"/aero_id.v1.AccountService/GetAccount",
	"/aero_id.v1.AccountService/ListAccounts",
	"/aero_id.v1.AggregationService/GetAccountOverview",
	"/aero_id.v1.OperationService/RunOperation",
	"/aero_id.v1.OperationService/ReconcileOperation",
	"/aero_id.v1.SyncJobService/ReconcileSyncJob",
	"/aero_id.v1.SourceIngestService/IngestEvents",
	"/aero_id.v1.CallbackService/RegisterCallback",
	"/aero_id.v1.CallbackService/ListCallbacks",
	"/aero_id.v1.CallbackService/DeleteCallback",
	"/aero_id.v1.CallbackService/ReplayCallbackDelivery",
}

// permissionVocabulary is the authoritative permission vocabulary the registry
// may reference, built from the same constants the role/permission resolution
// speaks (internal/domain/admin.go + internal/service/account.go).
var permissionVocabulary = map[string]bool{
	domain.PermAuditRead:          true,
	domain.PermUserBan:            true,
	domain.PermOperationManage:    true,
	domain.PermSourceSync:         true,
	domain.PermSourceEventIngest:  true,
	domain.PermCallbackManage:     true,
	domain.PermSystemConfig:       true,
	service.AccountReadPermission: true,
}

// aeroIDV1Methods enumerates every real service/method pair declared by the
// registered aero_id.v1 protobuf files as canonical "/Service/Method" names.
func aeroIDV1Methods() map[string]bool {
	methods := make(map[string]bool)
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != protoreflect.FullName("aero_id.v1") {
			return true
		}
		services := fd.Services()
		for i := 0; i < services.Len(); i++ {
			sd := services.Get(i)
			ms := sd.Methods()
			for j := 0; j < ms.Len(); j++ {
				md := ms.Get(j)
				methods["/"+string(sd.FullName())+"/"+string(md.Name())] = true
			}
		}
		return true
	})
	return methods
}

// TestRequiredPermForMethod_KeysResolveToRealMethods pins REQ-1: every registry
// key must name a real aero_id.v1 method and be gated with a non-empty
// permission, and the gated set must equal the pinned contract exactly.
func TestRequiredPermForMethod_KeysResolveToRealMethods(t *testing.T) {
	realMethods := aeroIDV1Methods()
	pinned := make(map[string]bool, len(pinnedGatedMethods))
	for _, key := range pinnedGatedMethods {
		pinned[key] = true
		perm, ok := requiredPermForMethod(key)
		if !ok {
			t.Errorf("registry does not gate %q", key)
		}
		if perm == "" {
			t.Errorf("registry gates %q with an empty permission", key)
		}
		if !realMethods[key] {
			t.Errorf("registry key %q is not a real aero_id.v1 method", key)
		}
	}
	for method := range realMethods {
		if _, ok := requiredPermForMethod(method); !ok {
			continue
		}
		if !pinned[method] {
			t.Errorf("method %q is gated but missing from the pinned contract", method)
		}
	}
}

// TestRequiredPermForMethod_PermissionsInKnownVocabulary pins REQ-2: every
// permission string the registry references must be a member of the known
// vocabulary, and each vocabulary constant must still hold its pinned literal
// value (guards against a coordinated constant-value drift).
func TestRequiredPermForMethod_PermissionsInKnownVocabulary(t *testing.T) {
	for _, key := range pinnedGatedMethods {
		perm, ok := requiredPermForMethod(key)
		if !ok {
			continue // gating itself is REQ-1's concern
		}
		if !permissionVocabulary[perm] {
			t.Errorf("registry maps %q to %q, outside the permission vocabulary", key, perm)
		}
	}
	literalPin := map[string]string{
		"audit.read":          domain.PermAuditRead,
		"user.ban":            domain.PermUserBan,
		"operation.manage":    domain.PermOperationManage,
		"source.sync":         domain.PermSourceSync,
		"source.event.ingest": domain.PermSourceEventIngest,
		"callback.manage":     domain.PermCallbackManage,
		"system.config":       domain.PermSystemConfig,
		"account.read":        service.AccountReadPermission,
	}
	for literal, constant := range literalPin {
		if constant != literal {
			t.Errorf("permission constant drifted: want %q, got %q", literal, constant)
		}
	}
}

// stubPermissionChecker records every consultation so tests can prove the
// interceptor bypasses the checker on infra/unlisted/nil-info paths.
type stubPermissionChecker struct {
	allowed bool
	err     error
	calls   int
}

func (s *stubPermissionChecker) HasPermission(context.Context, string, string) (bool, error) {
	s.calls++
	return s.allowed, s.err
}

var _ middleware.PermissionChecker = (*stubPermissionChecker)(nil)

// TestPermissionInterceptor_BehaviorMatrix pins REQ-3: the interceptor's
// fail-closed behavior matrix. Bypass rows must prove the checker is never
// consulted, not merely that the handler runs.
func TestPermissionInterceptor_BehaviorMatrix(t *testing.T) {
	const gated = "/aero_id.v1.AdminService/ListAudit"
	sentinel := "ok"
	boom := stderrors.New("checker boom")
	rows := []struct {
		name       string
		method     string
		actor      string
		allowed    bool
		checkerErr error
		wantCode   codes.Code
		wantMsg    string
		wantCalled bool
		wantChecks int
	}{
		{"allowed", gated, "u1", true, nil, codes.OK, "", true, 1},
		{"denied", gated, "u1", false, nil, codes.PermissionDenied, "", false, 1},
		{"checker error", gated, "u1", false, boom, codes.Internal, "permission check failed", false, 1},
		{"missing actor", gated, "", false, nil, codes.Unauthenticated, "missing authenticated actor", false, 0},
		{"health bypass", "/grpc.health.v1.Health/Check", "", false, nil, codes.OK, "", true, 0},
		{"reflection bypass", "/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo", "", false, nil, codes.OK, "", true, 0},
		{"unlisted method", "/aero_id.v1.MessageService/SendMessage", "u1", false, nil, codes.OK, "", true, 0},
		{"nil info", "", "", false, nil, codes.OK, "", true, 0},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			checker := &stubPermissionChecker{allowed: tc.allowed, err: tc.checkerErr}
			ctx := context.Background()
			if tc.actor != "" {
				ctx = service.WithActor(ctx, tc.actor)
			}
			var info *grpc.UnaryServerInfo
			if tc.method != "" {
				info = &grpc.UnaryServerInfo{FullMethod: tc.method}
			}
			var called bool
			intc := PermissionInterceptor(checker)
			resp, err := intc(ctx, nil, info, func(context.Context, any) (any, error) {
				called = true
				return sentinel, nil
			})
			if status.Code(err) != tc.wantCode {
				t.Fatalf("code = %v, want %v", status.Code(err), tc.wantCode)
			}
			if tc.wantMsg != "" && status.Convert(err).Message() != tc.wantMsg {
				t.Fatalf("message = %q, want %q", status.Convert(err).Message(), tc.wantMsg)
			}
			if called != tc.wantCalled {
				t.Fatalf("handler called = %v, want %v", called, tc.wantCalled)
			}
			if checker.calls != tc.wantChecks {
				t.Fatalf("checker consulted %d times, want %d", checker.calls, tc.wantChecks)
			}
			if tc.wantCode == codes.OK && resp != sentinel {
				t.Fatalf("response = %v, want %v", resp, sentinel)
			}
		})
	}
}
