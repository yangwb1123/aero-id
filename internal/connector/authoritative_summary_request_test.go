package connector

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/aero/aero-id/internal/domain"
	"github.com/aero/aero-id/internal/pkg/metrics"
)

const (
	trustedBindingInvalid     = "trusted summary account binding is invalid"
	trustedScopeInvalid       = "trusted summary dataset scope is invalid"
	trustedBindingUnavailable = "trusted summary account binding is unavailable"
)

type contractAccountLoader struct {
	account *domain.Account
	err     error
	calls   atomic.Int32
	mu      sync.Mutex
	keys    [][2]any
}

func (l *contractAccountLoader) GetByID(_ context.Context, tenantID string, accountID domain.ID) (*domain.Account, error) {
	l.calls.Add(1)
	l.mu.Lock()
	l.keys = append(l.keys, [2]any{tenantID, accountID})
	l.mu.Unlock()
	return l.account, l.err
}

func contractAccount(id byte, tenant, uid, region string, status domain.AccountStatus) *domain.Account {
	return &domain.Account{ID: domain.ID{id}, TenantID: tenant, CanonicalUID: uid, HomeRegion: region, Status: status}
}

func TestTrustedSummaryRequestValidationMatrix(t *testing.T) {
	account := contractAccount(7, "tenant-a", "uid-a", "west-1", domain.AccountActive)
	validIM := []domain.AccountDataset{"aero-im.profile", "aero-im.workspaces"}
	validVault := []domain.AccountDataset{"aero-vault.usage", "aero-vault.tenants", "aero-vault.buckets"}
	cases := []struct {
		name, want string
		ctx        context.Context
		loader     AuthoritativeAccountLoader
		input      TrustedSummaryRequestInput
		result     *domain.Account
		err        error
		calls      int32
	}{
		{name: "valid IM", input: TrustedSummaryRequestInput{Source: SourceAeroIM, TenantID: "tenant-a", AccountID: account.ID,
			ActorUID: "actor-im", RequestID: "request-im", OperationID: "operation-im", Datasets: validIM}, result: account},
		{name: "valid Vault", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID,
			ActorUID: "actor-vault", RequestID: "request-vault", OperationID: "operation-vault", Reconcile: true, Datasets: validVault}, result: account},
		{name: "nil context", ctx: nil, want: trustedBindingInvalid},
		{name: "nil loader", loader: nil, want: trustedBindingInvalid},
		{name: "typed nil loader", want: trustedBindingInvalid},
		{name: "zero account ID", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", Datasets: validVault}, want: trustedBindingInvalid},
		{name: "invalid tenant", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: " tenant-a", AccountID: account.ID, Datasets: validVault}, want: trustedBindingInvalid},
		{name: "unknown source", input: TrustedSummaryRequestInput{Source: Source("other"), TenantID: "tenant-a", AccountID: account.ID, Datasets: validVault}, want: trustedBindingInvalid},
		{name: "empty stored scope", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID}, want: trustedScopeInvalid},
		{name: "duplicate scope", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: []domain.AccountDataset{"aero-vault.tenants", "aero-vault.tenants"}}, want: trustedScopeInvalid},
		{name: "Vault with IM scope", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: []domain.AccountDataset{"aero-im.profile"}}, want: trustedScopeInvalid},
		{name: "IM with Vault scope", input: TrustedSummaryRequestInput{Source: SourceAeroIM, TenantID: "tenant-a", AccountID: account.ID, Datasets: []domain.AccountDataset{"aero-vault.tenants"}}, want: trustedScopeInvalid},
		{name: "loader error", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: validVault}, err: errors.New("repository detail"), want: trustedBindingUnavailable, calls: 1},
		{name: "nil loader result", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: validVault}, want: trustedBindingUnavailable, calls: 1},
		{name: "inactive", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: validVault}, result: contractAccount(7, "tenant-a", "uid-a", "west-1", domain.AccountSuspended), want: trustedBindingUnavailable, calls: 1},
		{name: "nil account", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: validVault}, want: trustedBindingUnavailable, calls: 1},
		{name: "zero loaded ID", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: validVault}, result: contractAccount(0, "tenant-a", "uid-a", "west-1", domain.AccountActive), want: trustedBindingUnavailable, calls: 1},
		{name: "blank UID", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: validVault}, result: contractAccount(7, "tenant-a", "", "west-1", domain.AccountActive), want: trustedBindingUnavailable, calls: 1},
		{name: "blank tenant", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: validVault}, result: contractAccount(7, "", "uid-a", "west-1", domain.AccountActive), want: trustedBindingUnavailable, calls: 1},
		{name: "blank region", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: validVault}, result: contractAccount(7, "tenant-a", "uid-a", "", domain.AccountActive), want: trustedBindingUnavailable, calls: 1},
		{name: "ID mismatch", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: validVault}, result: contractAccount(8, "tenant-a", "uid-a", "west-1", domain.AccountActive), want: trustedBindingUnavailable, calls: 1},
		{name: "tenant mismatch", input: TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: validVault}, result: contractAccount(7, "tenant-b", "uid-a", "west-1", domain.AccountActive), want: trustedBindingUnavailable, calls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := tc.ctx
			if tc.name != "nil context" {
				ctx = context.Background()
			}
			result := tc.result
			if result != nil {
				copy := *result
				result = &copy
			}
			loader := &contractAccountLoader{account: result, err: tc.err}
			var authoritative AuthoritativeAccountLoader = loader
			if tc.name == "nil loader" {
				authoritative = nil
			}
			if tc.name == "typed nil loader" {
				var typedNil *contractAccountLoader
				authoritative = typedNil
			}
			input := tc.input
			input.Datasets = append([]domain.AccountDataset(nil), input.Datasets...)
			if input.AccountID == (domain.ID{}) && tc.name != "zero account ID" {
				input = TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, Datasets: validVault}
			}
			request, err := NewTrustedSummaryRequest(ctx, authoritative, input)
			if tc.want != "" {
				if err == nil || err.Error() != tc.want {
					t.Fatalf("error = %v, want %q", err, tc.want)
				}
				if loader != nil && loader.calls.Load() != tc.calls {
					t.Fatalf("loader calls = %d, want %d", loader.calls.Load(), tc.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if loader.calls.Load() != 1 || len(loader.keys) != 1 || loader.keys[0][0] != input.TenantID || loader.keys[0][1] != input.AccountID {
				t.Fatalf("loader lookup = %v/%v, want %q/%v", loader.calls.Load(), loader.keys, input.TenantID, input.AccountID)
			}
			if request.AccountID != account.ID || request.CanonicalUID != account.CanonicalUID || request.TenantID != account.TenantID || request.Region != account.HomeRegion {
				t.Fatalf("request identity = %#v", request)
			}
			if request.ActorUID != input.ActorUID || request.RequestID != input.RequestID || request.OperationID != input.OperationID || request.Reconcile != input.Reconcile {
				t.Fatalf("request metadata = %#v", request)
			}
			want := append([]domain.AccountDataset(nil), validIM...)
			if input.Source == SourceAeroVault {
				want = []domain.AccountDataset{"aero-vault.buckets", "aero-vault.tenants", "aero-vault.usage"}
			}
			if !reflect.DeepEqual(request.Datasets, want) {
				t.Fatalf("datasets = %v, want %v", request.Datasets, want)
			}
			input.Datasets[0] = "mutated"
			result.CanonicalUID = "mutated"
			if request.CanonicalUID != "uid-a" || !reflect.DeepEqual(request.Datasets, want) {
				t.Fatal("trusted request was not defensively copied")
			}
		})
	}
}

func TestAeroVaultSummaryAuthorizationContract(t *testing.T) {
	account := contractAccount(11, "tenant-a", "uid-a", "west-2", domain.AccountActive)
	datasets := []domain.AccountDataset{"aero-vault.usage", "aero-vault.tenants", "aero-vault.buckets"}
	registry := metrics.NewRegistry()
	var seen atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if r.URL.Path != "/v1/internal/account-summary" || r.URL.Query().Get("account_id") != account.ID.String() || r.URL.Query().Get("region") != "west-2" || r.URL.Query().Get("reconcile") != "true" {
			t.Errorf("unexpected Vault request: %s", r.URL.String())
		}
		if got := r.URL.Query()["dataset"]; !reflect.DeepEqual(got, []string{"aero-vault.buckets", "aero-vault.tenants", "aero-vault.usage"}) {
			t.Errorf("datasets = %v", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer vault-contract-token" {
			t.Errorf("Authorization = %q, want configured Vault bearer", got)
		}
		for name, want := range map[string]string{
			"X-Aero-Account-ID": account.ID.String(), "X-Aero-Canonical-UID": "uid-a", "X-Aero-Actor-UID": "actor-a",
			"X-Aero-Tenant-ID": "tenant-a", "X-Aero-Region": "west-2", "X-Request-ID": "request-a", "X-Aero-Operation-ID": "operation-a",
		} {
			if got := r.Header.Get(name); got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		if got := r.Header.Get(accountSummaryTargetAssertionHeader); got != "" {
			t.Errorf("Vault target assertion = %q, want absent", got)
		}
		_, _ = io.WriteString(w, `{"source_region":"west-2","version":1,"generated_at":"2026-08-20T00:00:00Z","complete":true,"datasets":{"aero-vault.tenants":{},"aero-vault.buckets":{},"aero-vault.usage":{}}}`)
	}))
	defer server.Close()
	request, err := NewTrustedSummaryRequest(t.Context(), &contractAccountLoader{account: account}, TrustedSummaryRequestInput{
		Source: SourceAeroVault, TenantID: "tenant-a", AccountID: account.ID, ActorUID: "actor-a", RequestID: "request-a", OperationID: "operation-a", Datasets: datasets, Reconcile: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewJSONSummaryConnector(SourceAeroVault, HTTPConfig{Endpoint: server.URL,
		SummaryPath: "/v1/internal/account-summary", AllowInsecureLoopback: true, Environment: "test",
		BearerToken: "vault-contract-token", Client: server.Client(), MaxAttempts: 1, Metrics: registry})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Summary(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if seen.Load() != 1 {
		t.Fatalf("Vault calls = %d, want 1", seen.Load())
	}
	var exposition strings.Builder
	registry.WriteText(&exposition)
	text := exposition.String()
	if !strings.Contains(text, `aeroid_source_calls_total{source="aero-vault",operation="summary"} 1`) || !strings.Contains(text, `aeroid_source_errors_total{source="aero-vault",operation="summary"} 0`) {
		t.Fatalf("source metrics = %s", text)
	}
	if strings.Contains(text, account.ID.String()) || strings.Contains(text, "tenant-a") || strings.Contains(text, "uid-a") {
		t.Fatalf("identity leaked into source metrics: %s", text)
	}
}

func TestTrustedSummaryRequestRejectsBeforeSourceMetrics(t *testing.T) {
	registry := metrics.NewRegistry()
	loader := &contractAccountLoader{account: contractAccount(12, "tenant-a", "uid-a", "west-1", domain.AccountActive)}
	_, err := NewTrustedSummaryRequest(t.Context(), loader, TrustedSummaryRequestInput{Source: SourceAeroVault, TenantID: "tenant-a", AccountID: loader.account.ID, Datasets: []domain.AccountDataset{"aero-im.profile"}})
	if err == nil || err.Error() != trustedScopeInvalid {
		t.Fatalf("error = %v, want %q", err, trustedScopeInvalid)
	}
	var exposition strings.Builder
	registry.WriteText(&exposition)
	if strings.Contains(exposition.String(), "aeroid_source_") {
		t.Fatalf("local rejection recorded source metrics: %s", exposition.String())
	}
}

type concurrentSummaryLoader struct {
	mu       sync.Mutex
	accounts map[domain.ID]*domain.Account
	calls    atomic.Int32
}

func (l *concurrentSummaryLoader) GetByID(_ context.Context, tenant string, id domain.ID) (*domain.Account, error) {
	l.calls.Add(1)
	l.mu.Lock()
	defer l.mu.Unlock()
	account := l.accounts[id]
	if account == nil || account.TenantID != tenant {
		return nil, errors.New("not found")
	}
	copy := *account
	return &copy, nil
}

type concurrentWireCapture struct {
	region   string
	uid      string
	datasets string
}

func TestTrustedSummaryRequestConcurrentIsolation(t *testing.T) {
	loader := &concurrentSummaryLoader{accounts: make(map[domain.ID]*domain.Account)}
	var mu sync.Mutex
	seen := make(map[string]concurrentWireCapture)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accountID := r.URL.Query().Get("account_id")
		region := r.URL.Query().Get("region")
		mu.Lock()
		seen[accountID] = concurrentWireCapture{region: region, uid: r.Header.Get("X-Aero-Canonical-UID"),
			datasets: strings.Join(r.URL.Query()["dataset"], ",")}
		mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"source_region":%q,"version":1,"generated_at":"2026-08-30T00:00:00Z","complete":true,"datasets":{"aero-vault.tenants":{},"aero-vault.buckets":{},"aero-vault.usage":{}}}`, region)
	}))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceAeroVault, HTTPConfig{Endpoint: server.URL,
		AllowInsecureLoopback: true, Environment: "test", Client: server.Client(), MaxAttempts: 1, Metrics: metrics.NewRegistry()})
	if err != nil {
		t.Fatal(err)
	}
	const total = 32
	for i := byte(1); i <= total; i++ {
		id := domain.ID{i}
		loader.accounts[id] = contractAccount(i, "tenant-concurrent", fmt.Sprintf("uid-%d", i), fmt.Sprintf("region-%d", i), domain.AccountActive)
	}
	var wait sync.WaitGroup
	errorsCh := make(chan error, total)
	for i := byte(1); i <= total; i++ {
		i := i
		wait.Add(1)
		go func() {
			defer wait.Done()
			id := domain.ID{i}
			datasets := []domain.AccountDataset{"aero-vault.tenants"}
			if i%2 == 0 {
				datasets = []domain.AccountDataset{"aero-vault.usage", "aero-vault.tenants"}
			}
			request, requestErr := NewTrustedSummaryRequest(t.Context(), loader, TrustedSummaryRequestInput{
				Source: SourceAeroVault, TenantID: "tenant-concurrent", AccountID: id, Datasets: datasets,
			})
			if requestErr != nil {
				errorsCh <- fmt.Errorf("id %d: %w", i, requestErr)
				return
			}
			if _, requestErr = client.Summary(t.Context(), request); requestErr != nil {
				errorsCh <- fmt.Errorf("id %d: %w", i, requestErr)
			}
		}()
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	if loader.calls.Load() != total || len(seen) != total {
		t.Fatalf("loader/source calls = %d/%d, want %d/%d", loader.calls.Load(), len(seen), total, total)
	}
	for i := byte(1); i <= total; i++ {
		id := domain.ID{i}
		got := seen[id.String()]
		wantDatasets := "aero-vault.tenants"
		if i%2 == 0 {
			wantDatasets = "aero-vault.tenants,aero-vault.usage"
		}
		if got.region != loader.accounts[id].HomeRegion || got.uid != loader.accounts[id].CanonicalUID || got.datasets != wantDatasets {
			t.Fatalf("account %s wire capture = %#v, want region=%q uid=%q datasets=%q", id, got,
				loader.accounts[id].HomeRegion, loader.accounts[id].CanonicalUID, wantDatasets)
		}
	}
}

func TestTrustedSummaryRequestConcurrentBindingFailuresHaveNoSourceObservations(t *testing.T) {
	const total = 32
	loader := &contractAccountLoader{account: contractAccount(99, "tenant-concurrent", "uid", "region", domain.AccountActive)}
	registry := metrics.NewRegistry()
	var wait sync.WaitGroup
	var failures atomic.Int32
	for i := byte(1); i <= total; i++ {
		wait.Add(1)
		go func(i byte) {
			defer wait.Done()
			_, err := NewTrustedSummaryRequest(t.Context(), loader, TrustedSummaryRequestInput{
				Source: SourceAeroVault, TenantID: "tenant-concurrent", AccountID: domain.ID{i},
				Datasets: []domain.AccountDataset{"aero-vault.tenants"},
			})
			if err != nil && err.Error() == trustedBindingUnavailable {
				failures.Add(1)
			}
		}(i)
	}
	wait.Wait()
	if failures.Load() != total || loader.calls.Load() != total {
		t.Fatalf("binding failures/loader calls = %d/%d, want %d/%d", failures.Load(), loader.calls.Load(), total, total)
	}
	var exposition strings.Builder
	registry.WriteText(&exposition)
	if strings.Contains(exposition.String(), "aeroid_source_") {
		t.Fatalf("binding failures recorded source metrics: %s", exposition.String())
	}
}

func TestAeroVaultSummaryRemoteRejectionIsObservable(t *testing.T) {
	registry := metrics.NewRegistry()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "rejected", http.StatusForbidden) }))
	defer server.Close()
	client, err := NewJSONSummaryConnector(SourceAeroVault, HTTPConfig{Endpoint: server.URL, AllowInsecureLoopback: true, Environment: "test", Client: server.Client(), MaxAttempts: 1, Metrics: registry})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Summary(t.Context(), SummaryRequest{AccountID: domain.ID{13}, Datasets: []domain.AccountDataset{"aero-vault.tenants"}})
	if err == nil || !strings.Contains(err.Error(), "source.http_403") {
		t.Fatalf("error = %v, want source rejection", err)
	}
	var exposition strings.Builder
	registry.WriteText(&exposition)
	text := exposition.String()
	if !strings.Contains(text, `aeroid_source_calls_total{source="aero-vault",operation="summary"} 1`) || !strings.Contains(text, `aeroid_source_errors_total{source="aero-vault",operation="summary"} 1`) {
		t.Fatalf("remote rejection metrics = %s", text)
	}
}
