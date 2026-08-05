package app

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/connector/auditgovernance"
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
)

func wireAuditServices(callbackSvc service.CallbackService, auditLedger service.AuditLedgerService,
	auditLedgerRepo repository.AuditLedgerRepository, publishers ...service.AuditPublisher) {
	if setter, ok := callbackSvc.(interface {
		SetAuditLogger(service.AuditLedgerService)
	}); ok {
		setter.SetAuditLogger(auditLedger)
	}
	if setter, ok := callbackSvc.(interface {
		SetAuditPublicationRepository(repository.AuditPublicationRepository)
	}); ok {
		if publicationRepo, ok := auditLedgerRepo.(repository.AuditPublicationRepository); ok {
			setter.SetAuditPublicationRepository(publicationRepo)
		}
	}
	if setter, ok := callbackSvc.(interface {
		SetAuditPublishers(...service.AuditPublisher)
	}); ok {
		setter.SetAuditPublishers(publishers...)
	}
}

func buildAuditPublishers(cfg *config.Config, db *gorm.DB) ([]service.AuditPublisher, error) {
	if !cfg.AuditGovernance.Enabled {
		return nil, nil
	}
	settings := cfg.AuditGovernance
	bindings, err := auditOAuthBindings(settings.Bindings)
	if err != nil {
		return nil, err
	}
	if err := validateAuditBindingCoverage(db, settings.Bindings); err != nil {
		return nil, err
	}
	tokens, err := auditgovernance.NewOAuthTokenSource(auditgovernance.OAuthConfig{
		SourcePrefix: settings.SourcePrefix, Bindings: bindings,
		Timeout: settings.Timeout, RefreshSkew: settings.RefreshSkew,
		AllowInsecureLoopback: settings.AllowInsecureLoopback,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("audit governance oauth: %w", err)
	}
	publisher, err := auditgovernance.NewPublisher(auditgovernance.Config{
		Endpoint: settings.Endpoint, SourcePrefix: settings.SourcePrefix, ActorID: settings.ActorID,
		SchemaID: settings.SchemaID, SchemaVersion: settings.SchemaVersion,
		DataClassification: settings.DataClassification, RetentionClass: settings.RetentionClass,
		Timeout: settings.Timeout, AllowInsecureLoopback: settings.AllowInsecureLoopback,
	}, tokens, nil)
	if err != nil {
		return nil, fmt.Errorf("audit governance publisher: %w", err)
	}
	return []service.AuditPublisher{publisher}, nil
}

func auditOAuthBindings(values map[string]config.AuditGovernanceBindingConfig) (map[string]auditgovernance.OAuthBindingConfig, error) {
	result := make(map[string]auditgovernance.OAuthBindingConfig, len(values))
	for tenantID, value := range values {
		secret, err := config.ResolveClientSecretEnv(value.ClientSecretEnv)
		if err != nil {
			return nil, fmt.Errorf("audit governance credentials are unavailable")
		}
		result[tenantID] = auditgovernance.OAuthBindingConfig{
			TokenURL: value.TokenURL, ClientID: value.ClientID, ClientSecret: secret,
			Scope: value.Scope, Resources: value.Resources,
		}
	}
	return result, nil
}

func validateAuditBindingCoverage(db *gorm.DB, bindings map[string]config.AuditGovernanceBindingConfig) error {
	var tenants []string
	err := db.Raw(`
        SELECT tenant_id FROM accounts WHERE tenant_id <> '' AND tenant_id <> 'legacy'
        UNION
        SELECT ledger.tenant_id
          FROM audit_publications publication
          JOIN audit_ledger ledger ON ledger.id = publication.fact_id
         WHERE publication.status IN ('pending', 'delivering', 'dead_letter')
           AND ledger.tenant_id <> '' AND ledger.tenant_id <> 'legacy'`).Scan(&tenants).Error
	if err != nil {
		return fmt.Errorf("audit governance binding coverage check failed: %w", err)
	}
	for _, tenantID := range tenants {
		if _, found := bindings[tenantID]; !found {
			return fmt.Errorf("audit governance bindings do not cover durable tenant state")
		}
	}
	return nil
}
