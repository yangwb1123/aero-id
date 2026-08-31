package app

import (
	"strings"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/event"
)

func buildEventSourceBindings(values map[string]config.EventSourceBindingConfig) event.SourceBindings {
	result := make(event.SourceBindings, len(values))
	for clientID, value := range values {
		allowed := make([]string, 0, len(value.AllowedTenants))
		for _, tenant := range value.AllowedTenants {
			if tenant = strings.TrimSpace(tenant); tenant != "" {
				allowed = append(allowed, tenant)
			}
		}
		result[strings.TrimSpace(clientID)] = event.SourceBinding{
			TenantID: strings.TrimSpace(value.TenantID), Source: strings.TrimSpace(value.Source),
			SourceRegion: strings.TrimSpace(value.SourceRegion), AllowedTenants: allowed,
		}
	}
	return result
}
