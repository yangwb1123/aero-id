package config

import (
	"fmt"
	"strings"
)

const (
	maxCallbackPreviousKeys   = 4
	minCallbackMasterKeyBytes = 32
	maxCallbackMasterKeyBytes = 4096
)

// ValidateCallbackConfig is shared by Load and the composition root so in-code
// configs cannot bypass the key, private-target or numeric safety checks. It
// performs no I/O and never includes configured secret material in errors.
func ValidateCallbackConfig(value CallbackConfig, environment string) error {
	return validateCallbackPolicy(value, environment)
}

func validateCallbackMasterKeys(value CallbackConfig, environment string) error {
	if value.SecretEnvelopeVersion < 0 || value.SecretEnvelopeVersion > 1 {
		return fmt.Errorf("callback.secret_envelope_version must be 0 (legacy-compatible) or 1 (versioned)")
	}
	production := productionLikeEnvironment(environment)
	if err := validateCallbackActiveKey(value, production); err != nil {
		return err
	}
	if len(value.PreviousMasterKeys) > maxCallbackPreviousKeys {
		return fmt.Errorf("callback.previous_master_keys must not contain more than %d keys", maxCallbackPreviousKeys)
	}
	if len(value.PreviousMasterKeys) > 0 && value.MasterKey == "" {
		return fmt.Errorf("callback.previous_master_keys requires an active callback.master_key")
	}
	return validateCallbackPreviousKeys(value, production)
}

func validateCallbackActiveKey(value CallbackConfig, production bool) error {
	if value.MasterKey == "" {
		if production && !value.DeliveryPaused {
			return fmt.Errorf("callback.master_key is required in production-like environments unless " +
				"callback.delivery_paused=true (AERO_ID_CALLBACK_MASTER_KEY)")
		}
		return nil
	}
	if production && isPlaceholderSecret(value.MasterKey) {
		return fmt.Errorf("callback.master_key must not be a placeholder in production-like environments")
	}
	if !validCallbackMasterKey(value.MasterKey) {
		return fmt.Errorf("callback.master_key must be canonical printable ASCII, without commas, and at least %d bytes (maximum %d)",
			minCallbackMasterKeyBytes, maxCallbackMasterKeyBytes)
	}
	return nil
}

func validateCallbackPreviousKeys(value CallbackConfig, production bool) error {
	seen := map[string]struct{}{}
	for _, key := range value.PreviousMasterKeys {
		if !validCallbackMasterKey(key) {
			return fmt.Errorf("callback.previous_master_keys entries must be canonical printable ASCII, without commas, and at least %d bytes (maximum %d)",
				minCallbackMasterKeyBytes, maxCallbackMasterKeyBytes)
		}
		if key == value.MasterKey {
			return fmt.Errorf("callback.previous_master_keys must not repeat the active master key")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("callback.previous_master_keys must not contain duplicate keys")
		}
		if production && isPlaceholderSecret(key) {
			return fmt.Errorf("callback.previous_master_keys must not contain placeholder keys in production-like environments")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validCallbackMasterKey(key string) bool {
	if key != strings.TrimSpace(key) || len(key) < minCallbackMasterKeyBytes || len(key) > maxCallbackMasterKeyBytes {
		return false
	}
	for _, value := range key {
		if value < 0x21 || value > 0x7e || value == ',' {
			return false
		}
	}
	return true
}
