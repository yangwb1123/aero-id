package service

import (
	"context"
	"sync"

	"github.com/aero/aero-id/internal/domain"
)

type capabilityFeature struct {
	name       string
	permission string
	enabled    bool
	write      bool
}

func (s *capabilityService) features() []capabilityFeature {
	return []capabilityFeature{
		{name: "account.self.read", enabled: true},
		{name: "account.self.update", enabled: true, write: true},
		{name: "account.self.sync", enabled: s.anySourceSummaryEnabled(), write: true},
		{name: "account.self.export", enabled: s.config.ExportEnabled, write: true},
		{name: "account.self.erase", enabled: s.anySourceEraseEnabled(), write: true},
		{name: "account.admin.read", permission: AccountReadPermission, enabled: true},
		{name: "account.admin.manage", permission: AccountManagePermission, enabled: true, write: true},
		{name: "operation.read", permission: domain.PermOperationRead, enabled: true},
		{name: "operation.manage", permission: domain.PermOperationManage, enabled: true, write: true},
		{name: "source.sync", permission: domain.PermSourceSync, enabled: s.anySourceSummaryEnabled(), write: true},
		{name: "account.export", permission: domain.PermAccountExport, enabled: s.config.ExportEnabled, write: true},
		{name: "account.erase", permission: domain.PermAccountErase, enabled: s.anySourceEraseEnabled(), write: true},
		{name: "audit.read", permission: domain.PermAuditRead, enabled: true},
		{name: "audit.export", permission: domain.PermAuditExport, enabled: s.config.ExportEnabled, write: true},
		{name: "audit.replay", permission: domain.PermAuditReplay, enabled: true, write: true},
		{name: "callback.read", permission: domain.PermCallbackManage, enabled: true},
		{name: "callback.manage", permission: domain.PermCallbackManage, enabled: s.config.CallbacksEnabled, write: true},
		{name: "system.config", permission: domain.PermSystemConfig, enabled: true, write: true},
		{name: "user.ban", permission: domain.PermUserBan, enabled: true, write: true},
	}
}

func (s *capabilityService) anySourceSummaryEnabled() bool {
	for _, flags := range s.config.Sources {
		if flags.SummaryEnabled {
			return true
		}
	}
	return false
}

func (s *capabilityService) anySourceEraseEnabled() bool {
	for _, flags := range s.config.Sources {
		if flags.EraseEnabled {
			return true
		}
	}
	return false
}

// Fixed allow-list, one shared deadline and four workers. This is not source
// enrichment/fan-out; only Snaplink's published Check RPC decides global access.
// No error text or policy internals is included in the resulting report.
func (s *capabilityService) authorizationDecisions(ctx context.Context, actor string) map[string]domain.AccessState {
	ctx, cancel := context.WithTimeout(ctx, s.config.AuthorizationTimeout)
	defer cancel()
	decisions := map[string]domain.AccessState{}
	queue := make(chan string, len(s.features()))
	for _, feature := range s.features() {
		if feature.permission != "" {
			if _, exists := decisions[feature.permission]; exists {
				continue
			}
			decisions[feature.permission] = domain.AccessUnknown
			queue <- feature.permission
		}
	}
	close(queue)
	var mu sync.Mutex
	var workers sync.WaitGroup
	for range 4 {
		workers.Go(func() {
			for permission := range queue {
				decision := capabilityPermissionDecision(ctx, s.permission, actor, permission)
				mu.Lock()
				decisions[permission] = decision
				mu.Unlock()
			}
		})
	}
	workers.Wait()
	return decisions
}

func capabilityPermissionDecision(ctx context.Context, checker PermissionChecker, actor, permission string) domain.AccessState {
	if checker == nil || ctx.Err() != nil {
		return domain.AccessUnknown
	}
	allowed, err := checker.HasPermission(ctx, actor, permission)
	if err != nil || ctx.Err() != nil {
		return domain.AccessUnknown
	}
	if allowed {
		return domain.AccessAllowed
	}
	return domain.AccessDenied
}

func (s *capabilityService) featureCapabilities(account *domain.Account, decisions map[string]domain.AccessState) []domain.Capability {
	features := s.features()
	result := make([]domain.Capability, 0, len(features))
	for _, feature := range features {
		item := domain.Capability{Name: feature.name, Registered: true, Enabled: feature.enabled,
			Authorization: domain.AccessAllowed, AuthorizationScope: "self"}
		if feature.permission != "" {
			item.Authorization = decisions[feature.permission]
			item.AuthorizationScope = "global"
		}
		item.ReasonCode = s.featureReason(account, feature, item.Authorization)
		item.Available = item.ReasonCode == ""
		result = append(result, item)
	}
	return result
}

func (s *capabilityService) featureReason(account *domain.Account, feature capabilityFeature, access domain.AccessState) string {
	if !feature.enabled {
		return "capability.not_configured"
	}
	if access == domain.AccessUnknown {
		return "capability.authorization_unknown"
	}
	if access != domain.AccessAllowed {
		return "capability.denied"
	}
	if account.Status == domain.AccountErased {
		return "account.erased"
	}
	if feature.permission == "" && feature.write && account.HomeRegion != s.config.LocalRegion {
		return "account.home_region_required"
	}
	return ""
}
