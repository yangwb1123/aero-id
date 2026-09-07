package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"
)

// VerifyFileChecksum validates the release checksum supplied by the Downward
// API. An empty expected value keeps local commands usable; a supplied value is
// always checked before the caller starts work. Errors deliberately contain no
// path or file contents.
func VerifyFileChecksum(path, expected string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" {
		return nil
	}
	if len(expected) != sha256.Size*2 || !lowerHex(expected) {
		return errors.New("config checksum is invalid")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return errors.New("config checksum could not read configuration")
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != expected {
		return errors.New("config checksum does not match configuration")
	}
	return nil
}

func lowerHex(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
