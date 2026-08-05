package service

import (
	"strings"

	"github.com/aero/aero-id/internal/domain"
)

func datasetsFromRequest(request map[string]any) []domain.AccountDataset {
	raw := request["datasets"]
	result := make([]domain.AccountDataset, 0)
	switch values := raw.(type) {
	case []any:
		for _, value := range values {
			if name, ok := value.(string); ok {
				result = append(result, domain.AccountDataset(name))
			}
		}
	case []string:
		for _, name := range values {
			result = append(result, domain.AccountDataset(name))
		}
	case []domain.AccountDataset:
		result = append(result, values...)
	}
	return result
}

func workerOrDefault(value string) string {
	if strings.TrimSpace(value) == "" {
		return "inline"
	}
	return value
}
