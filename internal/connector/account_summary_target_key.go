package connector

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/aero/aero-id/internal/pkg/sourceauth"
)

func loadEd25519PrivateKey(path, envName string) (ed25519.PrivateKey, error) {
	path = strings.TrimSpace(path)
	envName = strings.TrimSpace(envName)
	if path != "" && envName != "" {
		return nil, errors.New("account summary target assertion key has multiple sources")
	}
	data, err := loadEd25519PrivateKeyData(path, envName)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxTargetAssertionKeyBytes {
		return nil, errors.New("account summary target assertion key is unavailable")
	}
	return parseEd25519PrivateKey(data)
}

func loadEd25519PrivateKeyData(path, envName string) ([]byte, error) {
	if path != "" {
		return readEd25519PrivateKeyFile(path)
	}
	if envName != "" {
		return readEd25519PrivateKeyEnvironment(envName)
	}
	return nil, errors.New("account summary target assertion private key is not configured")
}

func readEd25519PrivateKeyFile(path string) ([]byte, error) {
	if !validPrivateKeyPath(path) {
		return nil, errors.New("account summary target assertion key file is unavailable")
	}
	info, err := os.Lstat(path)
	if err != nil || !safePrivateKeyFile(path, info) {
		return nil, errors.New("account summary target assertion key file is unavailable")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errors.New("account summary target assertion key file is unavailable")
	}
	return readOpenedPrivateKeyFile(file, info, path)
}

func validPrivateKeyPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path &&
		!strings.ContainsAny(path, "\x00\r\n")
}

func safePrivateKeyFile(path string, info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink == 0 && safePrivateKeyFileInfo(info) &&
		safePrivateKeyDirectory(path)
}

func readOpenedPrivateKeyFile(file *os.File, info os.FileInfo, path string) ([]byte, error) {
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) || !safePrivateKeyFile(path, openedInfo) {
		_ = file.Close()
		return nil, errors.New("account summary target assertion key file is unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxTargetAssertionKeyBytes+1))
	_ = file.Close()
	if err != nil {
		return nil, errors.New("account summary target assertion key is unavailable")
	}
	return data, nil
}

func readEd25519PrivateKeyEnvironment(envName string) ([]byte, error) {
	if !validEnvironmentName(envName) {
		return nil, errors.New("account summary target assertion key environment name is invalid")
	}
	value, ok := os.LookupEnv(envName)
	if !ok || strings.TrimSpace(value) == "" {
		return nil, errors.New("account summary target assertion key environment value is unavailable")
	}
	return []byte(strings.ReplaceAll(value, `\n`, "\n")), nil
}

func parseEd25519PrivateKey(data []byte) (ed25519.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "PRIVATE KEY" || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, errors.New("account summary target assertion key is invalid")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("account summary target assertion key is invalid")
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("account summary target assertion key is invalid")
	}
	return append(ed25519.PrivateKey(nil), key...), nil
}

func safePrivateKeyFileInfo(info os.FileInfo) bool {
	return info != nil && info.Mode().IsRegular() && info.Mode().Perm()&0o077 == 0 &&
		info.Size() >= 0 && info.Size() <= maxTargetAssertionKeyBytes && safePrivateKeyOwner(info)
}

func safePrivateKeyDirectory(path string) bool {
	info, err := os.Lstat(filepath.Dir(path))
	return err == nil && info.Mode().IsDir() && info.Mode()&os.ModeSymlink == 0 &&
		info.Mode().Perm()&0o022 == 0 && safePrivateKeyOwner(info)
}

func safePrivateKeyOwner(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && (int(stat.Uid) == os.Getuid() || int(stat.Uid) == 0)
}

func validEnvironmentName(value string) bool {
	return sourceauth.ValidSecretEnvironmentName(value)
}
