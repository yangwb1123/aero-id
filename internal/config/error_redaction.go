package config

import (
	"errors"
	"strings"
)

// redactedUnmarshalError keeps the useful unknown-field name emitted by
// mapstructure while discarding decoder text that may contain a secret value,
// file path, or URL. Validation errors below Load provide stable field-level
// messages for supported configuration.
func redactedUnmarshalError(err error) error {
	if err == nil {
		return nil
	}
	const marker = "invalid keys:"
	message := err.Error()
	index := strings.Index(message, marker)
	if index >= 0 {
		key := strings.TrimSpace(message[index+len(marker):])
		key = strings.Trim(key, "[],'\"")
		if validConfigKeyName(key) {
			return errors.New("unsupported configuration field: " + key)
		}
	}
	return errors.New("configuration contains an invalid or unsupported field")
}

func validConfigKeyName(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if !validConfigKeyCharacter(character) {
			return false
		}
	}
	return true
}

func validConfigKeyCharacter(character rune) bool {
	if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' {
		return true
	}
	if character >= '0' && character <= '9' {
		return true
	}
	return character == '_' || character == '-' || character == '.'
}
