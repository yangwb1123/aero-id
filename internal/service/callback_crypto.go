package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

// Local key/envelope failures must not consume the remote delivery retry budget.
// The worker parks the durable delivery until an operator repairs configuration.
var errCallbackSecretUnavailable = errors.New("callback secret is unavailable")

const (
	maxCallbackPreviousKeys = 4
	minCallbackKeyBytes     = 32
	maxCallbackCipherBytes  = 1024
)

func callbackKeys(cfg CallbackConfig) ([]byte, string, map[string][]byte) {
	if len(cfg.MasterKey) < minCallbackKeyBytes || len(cfg.PreviousMasterKeys) > maxCallbackPreviousKeys ||
		cfg.SecretEnvelopeVersion < 0 || cfg.SecretEnvelopeVersion > 1 {
		return nil, "", nil
	}
	key, keyID := deriveCallbackKey(cfg.MasterKey)
	keys := map[string][]byte{keyID: key}
	for _, previous := range cfg.PreviousMasterKeys {
		if len(previous) < minCallbackKeyBytes {
			return nil, "", nil
		}
		previousKey, previousID := deriveCallbackKey(previous)
		keys[previousID] = previousKey
	}
	return key, keyID, keys
}

func deriveCallbackKey(masterKey string) ([]byte, string) {
	// Keep the legacy AES derivation byte-compatible. The identifier must NOT
	// equal those AES bytes: it is stored with the ciphertext, not kept secret.
	key := sha256.Sum256([]byte(masterKey))
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte("aero-id/callback-secret/key-id/v1"))
	return key[:], hex.EncodeToString(mac.Sum(nil))
}

func callbackSecretAAD(keyID string) []byte {
	return []byte("aero-id/callback-secret/v1/" + keyID)
}

func (s *callbackService) encrypt(value []byte) (string, error) {
	if s.activeKeyID == "" || len(s.key) == 0 {
		return "", errCallbackSecretUnavailable
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return "", errCallbackSecretUnavailable
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", errCallbackSecretUnavailable
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	var aad []byte
	if s.cfg.SecretEnvelopeVersion == 1 {
		aad = callbackSecretAAD(s.activeKeyID)
	}
	data := base64.RawStdEncoding.EncodeToString(append(nonce, gcm.Seal(nil, nonce, value, aad)...))
	if s.cfg.SecretEnvelopeVersion == 1 {
		return "v1:" + s.activeKeyID + ":" + data, nil
	}
	return data, nil
}

func (s *callbackService) decrypt(value string) ([]byte, error) {
	if len(value) == 0 || len(value) > maxCallbackCipherBytes {
		return nil, errCallbackSecretUnavailable
	}
	if strings.Contains(value, ":") {
		return s.decryptVersioned(value)
	}
	// Legacy ciphertext has no key ID or AAD. The ring is capped at five keys;
	// trying retained keys preserves old subscriptions during staged rotation.
	data, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return nil, errCallbackSecretUnavailable
	}
	for _, key := range s.keyring {
		if plaintext, err := openCallbackSecret(key, data, nil); err == nil {
			return plaintext, nil
		}
	}
	return nil, errCallbackSecretUnavailable
}

func (s *callbackService) decryptVersioned(value string) ([]byte, error) {
	parts := strings.SplitN(value, ":", 3)
	if len(parts) != 3 || parts[0] != "v1" || parts[1] == "" {
		return nil, errCallbackSecretUnavailable
	}
	key, ok := s.keyring[parts[1]]
	if !ok {
		return nil, errCallbackSecretUnavailable
	}
	data, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errCallbackSecretUnavailable
	}
	return openCallbackSecret(key, data, callbackSecretAAD(parts[1]))
}

func openCallbackSecret(key, data, aad []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errCallbackSecretUnavailable
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(data) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errCallbackSecretUnavailable
	}
	plaintext, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], aad)
	if err != nil {
		return nil, errCallbackSecretUnavailable
	}
	return plaintext, nil
}

func randomSecret() ([]byte, error) {
	value := make([]byte, 32)
	_, err := rand.Read(value)
	return value, err
}
