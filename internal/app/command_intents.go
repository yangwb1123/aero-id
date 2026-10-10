package app

import (
	"github.com/aero/aero-id/internal/repository"
	"github.com/aero/aero-id/internal/service"
)

func buildCommandIntentService(deps serviceDependencies, accounts service.AccountService,
	operations service.OperationService, syncJobs service.SyncJobService) service.CommandIntentService {
	cfg := service.CommandIntentConfig{Region: deps.cfg.Account.Region,
		ExportEnabled: deps.store != nil && deps.cfg.Export.EncryptionKey != "", Sources: map[string]service.SourceCapabilityConfig{}}
	for _, client := range deps.sourceConnectors {
		if client != nil {
			cfg.Sources[string(client.Source())] = sourceCapabilityFlags(deps.cfg, client)
		}
	}
	return service.NewCommandIntentService(repository.NewCommandIntentRepository(deps.gdb, deps.auditLedgerRepo),
		accounts, deps.accountRepo, operations, syncJobs, deps.ssoClient, cfg)
}
