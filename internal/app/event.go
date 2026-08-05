package app

import (
	"strings"

	"github.com/aero/aero-id/internal/config"
	"github.com/aero/aero-id/internal/event"
)

func buildEventSourceBindings(values map[string]config.EventSourceBindingConfig) event.SourceBindings {
	result := make(event.SourceBindings, len(values))
	for clientID, value := range values {
		result[strings.TrimSpace(clientID)] = event.SourceBinding{
			TenantID: strings.TrimSpace(value.TenantID), Source: strings.TrimSpace(value.Source),
			SourceRegion: strings.TrimSpace(value.SourceRegion),
		}
	}
	return result
}
