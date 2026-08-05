package service

import "testing"

func TestValidateAccountPreferencesRejectsCredentialFields(t *testing.T) {
	if err := validateAccountPreferences(map[string]any{"theme": "dark", "access_token": "secret"}); err == nil {
		t.Fatal("credential preference was accepted")
	}
}

func TestValidateAccountPreferencesAcceptsPresentationValues(t *testing.T) {
	if err := validateAccountPreferences(map[string]any{"theme": "dark", "sidebar": map[string]any{"collapsed": true}}); err != nil {
		t.Fatal(err)
	}
}
