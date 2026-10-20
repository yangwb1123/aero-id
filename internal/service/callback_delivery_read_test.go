package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aero/aero-id/internal/domain"
	apperr "github.com/aero/aero-id/internal/pkg/errors"
	"github.com/aero/aero-id/internal/pkg/idgen"
	"github.com/aero/aero-id/internal/repository"
)

type callbackReadPermission struct {
	check func(context.Context, string, string) (bool, error)
}

func (p *callbackReadPermission) HasPermission(ctx context.Context, uid, permission string) (bool, error) {
	return p.check(ctx, uid, permission)
}

type callbackReadRepo struct {
	repository.CallbackRepository
	item   *domain.CallbackDeliveryMetadata
	calls  int
	owner  string
	tenant string
	filter domain.CallbackDeliveryFilter
	facts  []*domain.AuditFact
	fail   error
}

func (r *callbackReadRepo) ListDeliveryMetadataWithAudit(_ context.Context, owner, tenant string, _ domain.ID,
	filter domain.CallbackDeliveryFilter, fact *domain.AuditFact) ([]*domain.CallbackDeliveryMetadata, string, error) {
	r.calls++
	r.owner, r.tenant, r.filter = owner, tenant, filter
	r.facts = append(r.facts, fact)
	return []*domain.CallbackDeliveryMetadata{r.item}, "", r.fail
}

func (r *callbackReadRepo) GetDeliveryMetadataWithAudit(_ context.Context, owner, tenant string, _, _ domain.ID,
	fact *domain.AuditFact) (*domain.CallbackDeliveryMetadata, error) {
	r.calls++
	r.owner, r.tenant = owner, tenant
	r.facts = append(r.facts, fact)
	return r.item, r.fail
}

func callbackReadContext() context.Context {
	return WithIdentity(context.Background(), &domain.TokenIdentity{Verified: true, UID: "owner", Subject: "owner",
		Issuer: "https://snaplink.example", ClientID: "console", TenantID: "tenant-a"})
}

func callbackReadFixture() (*callbackReadRepo, *callbackReadPermission) {
	now := time.Now().UTC()
	return &callbackReadRepo{item: &domain.CallbackDeliveryMetadata{ID: idgen.NewID(), SubscriptionID: idgen.NewID(),
			EventID: idgen.NewID(), TenantID: "tenant-a", Status: "dead_letter", Action: "account.provisioned",
			CreatedAt: now, UpdatedAt: now, NextAttemptAt: now}},
		&callbackReadPermission{check: func(_ context.Context, uid, permission string) (bool, error) {
			return uid == "owner" && permission == domain.PermCallbackManage, nil
		}}
}

func TestCallbackDeliveryReadsRequireVerifiedCurrentAuthority(t *testing.T) {
	var typedNil *callbackReadPermission
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		checker PermissionChecker
		code    string
	}{
		{"missing checker", callbackReadContext(), nil, "callback.authorization_unavailable"},
		{"typed nil", callbackReadContext(), typedNil, "callback.authorization_unavailable"},
		{"denied", callbackReadContext(), &callbackReadPermission{check: func(context.Context, string, string) (bool, error) { return false, nil }}, "callback.authorization_denied"},
		{"outage", callbackReadContext(), &callbackReadPermission{check: func(context.Context, string, string) (bool, error) { return true, errors.New("authority secret") }}, "callback.authorization_unavailable"},
		{"UID only", WithActor(context.Background(), "owner"), nil, "auth.claims_required"},
		{"actor mismatch", WithActor(callbackReadContext(), "another-owner"), nil, "auth.claims_mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, _ := callbackReadFixture()
			svc := NewCallbackService(repo, CallbackConfig{}, WithCallbackDeliveryPermission(tc.checker))
			items, next, err := svc.ListDeliveries(tc.ctx, repo.item.SubscriptionID, domain.CallbackDeliveryFilter{})
			assertCallbackReadError(t, err, tc.code)
			if items != nil || next != "" || repo.calls != 0 {
				t.Fatal("unverified or unauthorized read reached persistence")
			}
			_, err = svc.GetDeliveryMetadata(tc.ctx, repo.item.SubscriptionID, repo.item.ID)
			assertCallbackReadError(t, err, tc.code)
			if repo.calls != 0 {
				t.Fatal("detail reached persistence without current grant")
			}
		})
	}
}

func TestCallbackDeliveryReadsUseBoundedMetadataAndIndependentFacts(t *testing.T) {
	repo, checker := callbackReadFixture()
	svc := NewCallbackService(repo, CallbackConfig{DeliveryPaused: true}, WithCallbackDeliveryPermission(checker))
	ctx := WithRequestID(callbackReadContext(), "request-a")
	for range 2 {
		items, _, err := svc.ListDeliveries(ctx, repo.item.SubscriptionID,
			domain.CallbackDeliveryFilter{Status: " dead_letter ", Page: domain.Page{Limit: 200, Cursor: repo.item.ID.String()}})
		if err != nil || len(items) != 1 {
			t.Fatalf("list = %v / %v", items, err)
		}
	}
	if repo.owner != "owner" || repo.tenant != "tenant-a" || repo.filter.Page.Limit != 100 || repo.filter.Status != "dead_letter" {
		t.Fatalf("scope/filter = %s/%s/%+v", repo.owner, repo.tenant, repo.filter)
	}
	for _, fact := range repo.facts {
		if fact.EventID != fact.ID || fact.ActorUID != "owner" || fact.TenantID != "tenant-a" || fact.RequestID != "request-a" ||
			fact.ResourceID != repo.item.SubscriptionID.String() || fact.Action != "callback.deliveries.read" {
			t.Fatalf("read fact = %+v", fact)
		}
	}
	if repo.facts[0].EventID == repo.facts[1].EventID {
		t.Fatal("distinct reads folded on a resource anchor")
	}
	if _, err := svc.GetDeliveryMetadata(ctx, repo.item.SubscriptionID, repo.item.ID); err != nil {
		t.Fatal(err)
	}
	if repo.facts[2].Action != "callback.delivery.read" || repo.facts[2].ResourceID != repo.item.ID.String() {
		t.Fatal("detail fact differs")
	}
}

func TestCallbackDeliveryReadsRejectInvalidFiltersWithoutSQL(t *testing.T) {
	for _, filter := range []domain.CallbackDeliveryFilter{
		{Status: "arbitrary"}, {Page: domain.Page{Cursor: "SQL or secret"}}, {Page: domain.Page{Cursor: (domain.ID{}).String()}},
	} {
		repo, checker := callbackReadFixture()
		svc := NewCallbackService(repo, CallbackConfig{}, WithCallbackDeliveryPermission(checker))
		items, _, err := svc.ListDeliveries(callbackReadContext(), repo.item.SubscriptionID, filter)
		if err == nil || items != nil || repo.calls != 0 {
			t.Fatal("invalid filter reached SQL")
		}
	}
}

func TestCallbackDeliveryReadFailureReturnsNoContentAndSanitizesHistory(t *testing.T) {
	repo, checker := callbackReadFixture()
	svc := NewCallbackService(repo, CallbackConfig{}, WithCallbackDeliveryPermission(checker))
	repo.item.Action, repo.item.ErrorCode, repo.item.Status = "https://secret.example/token", "password=secret", "invented"
	item, err := svc.GetDeliveryMetadata(callbackReadContext(), repo.item.SubscriptionID, repo.item.ID)
	if err != nil || item.Action != "unknown" || item.ErrorCode != "callback.error_unknown" || item.Status != "unknown" {
		t.Fatalf("sanitized item = %+v, %v", item, err)
	}
	if repo.item.ErrorCode != "password=secret" {
		t.Fatal("read mutated authoritative historical metadata")
	}
	for _, failure := range []error{repository.ErrNotFound, errors.New("SQL password=secret audit failure")} {
		repo.fail = failure
		items, next, err := svc.ListDeliveries(callbackReadContext(), repo.item.SubscriptionID, domain.CallbackDeliveryFilter{})
		if err == nil || items != nil || next != "" {
			t.Fatal("failed read exposed metadata")
		}
		var app *apperr.AppError
		if !errors.As(err, &app) || strings.Contains(app.Message, "secret") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	metadata := reflect.TypeFor[domain.CallbackDeliveryMetadata]()
	for _, forbidden := range []string{"Payload", "ResponseBody", "SecretCiphertext", "Endpoint", "LeaseOwner"} {
		if _, ok := metadata.FieldByName(forbidden); ok {
			t.Fatalf("read model contains %s", forbidden)
		}
	}
}

func TestCallbackDeliveryReadLateGrantAndMissingReaderFailClosed(t *testing.T) {
	repo, _ := callbackReadFixture()
	checker := &callbackReadPermission{check: func(ctx context.Context, _, _ string) (bool, error) { <-ctx.Done(); return true, nil }}
	svc := NewCallbackService(repo, CallbackConfig{}, WithCallbackDeliveryPermission(checker))
	ctx, cancel := context.WithTimeout(callbackReadContext(), 10*time.Millisecond)
	defer cancel()
	_, _, err := svc.ListDeliveries(ctx, repo.item.SubscriptionID, domain.CallbackDeliveryFilter{})
	assertCallbackReadError(t, err, "callback.authorization_unavailable")
	if repo.calls != 0 {
		t.Fatal("late grant reached SQL")
	}
	_, checker = callbackReadFixture()
	svc = NewCallbackService(newCallbackFakeRepo(), CallbackConfig{}, WithCallbackDeliveryPermission(checker))
	_, _, err = svc.ListDeliveries(callbackReadContext(), repo.item.SubscriptionID, domain.CallbackDeliveryFilter{})
	assertCallbackReadError(t, err, "callback.delivery_read_unavailable")
}

func assertCallbackReadError(t *testing.T, err error, code string) {
	t.Helper()
	var app *apperr.AppError
	if !errors.As(err, &app) || app.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
}
