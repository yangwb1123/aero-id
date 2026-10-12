package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

const (
	oldCallbackMaster = "old-master-key-for-callback-tests-0123456789"
	newCallbackMaster = "new-master-key-for-callback-tests-9876543210"
)

func TestCallbackSecretEnvelopeSupportsStagedMasterKeyRotation(t *testing.T) {
	plaintext := []byte("callback-signing-secret")
	oldService := NewCallbackService(nil, CallbackConfig{MasterKey: oldCallbackMaster, SecretEnvelopeVersion: 1}).(*callbackService)
	legacyCiphertext := legacyCallbackCiphertext(t, oldCallbackMaster, plaintext)
	versionedCiphertext, err := oldService.encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt old key: %v", err)
	}
	if !strings.HasPrefix(versionedCiphertext, "v1:") {
		t.Fatal("opted-in ciphertext has no version/key id")
	}

	rotatingService := NewCallbackService(nil, CallbackConfig{
		MasterKey: newCallbackMaster, PreviousMasterKeys: []string{oldCallbackMaster}, SecretEnvelopeVersion: 1,
	}).(*callbackService)
	for label, ciphertext := range map[string]string{
		"legacy": legacyCiphertext, "versioned": versionedCiphertext,
	} {
		got, err := rotatingService.decrypt(ciphertext)
		if err != nil || string(got) != string(plaintext) {
			t.Errorf("decrypt %s old-key ciphertext: %v", label, err)
		}
	}

	newCiphertext, err := rotatingService.encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt with active new key: %v", err)
	}
	_, expectedKeyID := deriveCallbackKey(newCallbackMaster)
	if strings.SplitN(newCiphertext, ":", 3)[1] != expectedKeyID {
		t.Fatal("new ciphertext does not select the active key")
	}
	if got, err := rotatingService.decrypt(newCiphertext); err != nil || string(got) != string(plaintext) {
		t.Fatalf("decrypt new ciphertext: %v", err)
	}
	if _, err := oldService.decrypt(newCiphertext); !errors.Is(err, errCallbackSecretUnavailable) {
		t.Fatalf("missing retained key error = %v", err)
	}
}

func TestCallbackSecretDefaultWritesRemainLegacyCompatible(t *testing.T) {
	svc := NewCallbackService(nil, CallbackConfig{MasterKey: oldCallbackMaster}).(*callbackService)
	ciphertext, err := svc.encrypt([]byte("callback-signing-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ciphertext, ":") {
		t.Fatal("default encryption wrote a versioned envelope before all readers upgraded")
	}
	data, err := base64.RawStdEncoding.DecodeString(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the pre-upgrade reader independently of the new keyring.
	key := sha256.Sum256([]byte(oldCallbackMaster))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil || string(plaintext) != "callback-signing-secret" {
		t.Fatalf("pre-upgrade reader cannot decrypt default writes: %v", err)
	}
}

func TestCallbackSecretKeyIDDoesNotExposeAESKey(t *testing.T) {
	key, keyID := deriveCallbackKey(oldCallbackMaster)
	publicID, err := hex.DecodeString(keyID)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(key) == keyID {
		t.Fatal("stored identifier is the AES key")
	}
	svc := NewCallbackService(nil, CallbackConfig{MasterKey: oldCallbackMaster, SecretEnvelopeVersion: 1}).(*callbackService)
	ciphertext, err := svc.encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.RawStdEncoding.DecodeString(strings.SplitN(ciphertext, ":", 3)[2])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openCallbackSecret(publicID, data, callbackSecretAAD(keyID)); err == nil {
		t.Fatal("public identifier decrypted the envelope")
	}
}

func TestCallbackSecretEnvelopeAuthenticatesIDAndCiphertext(t *testing.T) {
	svc := NewCallbackService(nil, CallbackConfig{MasterKey: newCallbackMaster,
		PreviousMasterKeys: []string{oldCallbackMaster}, SecretEnvelopeVersion: 1}).(*callbackService)
	ciphertext, err := svc.encrypt([]byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(ciphertext, ":", 3)
	_, oldID := deriveCallbackKey(oldCallbackMaster)
	if _, err := svc.decrypt("v1:" + oldID + ":" + parts[2]); !errors.Is(err, errCallbackSecretUnavailable) {
		t.Fatalf("substituted key id error = %v", err)
	}
	data, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if _, err := svc.decrypt("v1:" + parts[1] + ":" + base64.RawStdEncoding.EncodeToString(data)); !errors.Is(err, errCallbackSecretUnavailable) {
		t.Fatalf("tampered authentication tag error = %v", err)
	}
}

func TestCallbackSecretEnvelopeRejectsUnknownKeyAndMalformedVersion(t *testing.T) {
	svc := NewCallbackService(nil, CallbackConfig{MasterKey: newCallbackMaster}).(*callbackService)
	for _, value := range []string{"v1:unknown-key:not-base64", "v2:key-id:payload", "v1::payload", "", "AA", strings.Repeat("A", maxCallbackCipherBytes+1)} {
		if _, err := svc.decrypt(value); !errors.Is(err, errCallbackSecretUnavailable) {
			t.Errorf("malformed/unknown envelope error = %v, want secret unavailable", err)
		}
	}
}

func TestCallbackSecretEmptyKeyCannotEncryptOrDecrypt(t *testing.T) {
	svc := NewCallbackService(nil, CallbackConfig{}).(*callbackService)
	if _, err := svc.encrypt([]byte("secret")); !errors.Is(err, errCallbackSecretUnavailable) {
		t.Fatalf("empty key encryption error = %v", err)
	}
	if _, err := svc.decrypt(legacyCallbackCiphertext(t, oldCallbackMaster, []byte("secret"))); !errors.Is(err, errCallbackSecretUnavailable) {
		t.Fatalf("empty key decryption error = %v", err)
	}
}

func legacyCallbackCiphertext(t *testing.T, masterKey string, plaintext []byte) string {
	t.Helper()
	key := sha256.Sum256([]byte(masterKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	payload := append(nonce, gcm.Seal(nil, nonce, plaintext, nil)...)
	return base64.RawStdEncoding.EncodeToString(payload)
}
