package grpc

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/middleware"
)

// controlPlaneRPC identifies the account, aggregate, and source-ingest gRPC
// surface. These methods must not be downgraded to the legacy UID-only seam.
func controlPlaneRPC(method string) bool {
	return strings.HasPrefix(method, "/aero_id.v1.AccountService/") ||
		strings.HasPrefix(method, "/aero_id.v1.AggregationService/") ||
		strings.HasPrefix(method, "/aero_id.v1.SourceIngestService/")
}

func validateControlPlaneRPC(ctx context.Context, validator TokenValidator, token string) (*domain.TokenIdentity, error) {
	candidate, ok := validator.(middleware.ControlPlaneTokenValidator)
	if !ok || isNilInterface(candidate) {
		return nil, errors.New("control-plane validator unavailable")
	}
	identity, err := candidate.ValidateControlPlaneIdentity(ctx, token)
	if err != nil {
		return nil, err
	}
	if !completeControlPlaneIdentity(identity) {
		return nil, errors.New("incomplete control-plane identity")
	}
	return identity, nil
}

func completeControlPlaneIdentity(identity *domain.TokenIdentity) bool {
	return identity != nil && validControlPlaneField(identity.UID) &&
		validControlPlaneField(identity.Issuer) && validControlPlaneField(identity.Subject) &&
		validControlPlaneField(identity.TenantID) && validControlPlaneField(identity.ClientID)
}

func validControlPlaneField(value string) bool {
	return utf8.ValidString(value) && value != "" && len(value) <= 256 &&
		value == strings.TrimSpace(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
