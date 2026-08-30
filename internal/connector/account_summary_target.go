package connector

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	"github.com/aero/aero-id/internal/domain"
)

const (
	accountSummaryTargetAssertionType   = "aero.account-target+jwt"
	accountSummaryTargetAssertionHeader = "X-Aero-Target-Assertion"
	accountSummaryCanonicalVersion      = "aero-account-summary-v1"
	accountSummaryCanonicalMethod       = "GET"
	accountSummaryCanonicalPath         = "/internal/account-summary"
	accountSummaryAssertionScope        = "aero.account.summary.read"
	accountSummaryTargetAudience        = "aero-im-account-summary"
	accountSummaryTargetSubject         = "aero-id-sync"
	accountSummaryOAuthResource         = "aero-im"
	defaultTargetAssertionLifetime      = time.Minute
	maxTargetAssertionLifetime          = time.Minute
	maxTargetAssertionTokenBytes        = 16 * 1024
	maxTargetAssertionKeyBytes          = 16 * 1024
)

// TargetAssertionKeyFile/Env are mutually exclusive secret references. The
// key itself is never kept in the YAML config or logged. The remaining fields
// identify the dedicated Aero IM target-assertion trust domain.
type targetAssertionSignerConfig struct {
	KeyFile    string
	KeyEnv     string
	KeyID      string
	Issuer     string
	Audience   string
	Subject    string
	ClientID   string
	Lifetime   time.Duration
	PrivateKey ed25519.PrivateKey // test-only injection; production uses KeyFile/KeyEnv
}

type accountSummaryTargetSigner struct {
	privateKey ed25519.PrivateKey
	keyID      string
	issuer     string
	audience   string
	subject    string
	clientID   string
	lifetime   time.Duration
	now        func() time.Time
	newJTI     func() (uuid.UUID, error)
}

type targetAssertionHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
	Type      string `json:"typ"`
}

type targetAssertionClaims struct {
	Issuer       string                  `json:"iss"`
	Audience     string                  `json:"aud"`
	Subject      string                  `json:"sub"`
	ClientID     string                  `json:"client_id"`
	AccountID    string                  `json:"account_id"`
	CanonicalUID string                  `json:"canonical_uid"`
	TenantID     string                  `json:"tenant_id"`
	Region       string                  `json:"region"`
	Datasets     []domain.AccountDataset `json:"datasets"`
	Method       string                  `json:"method"`
	Path         string                  `json:"path"`
	RequestHash  string                  `json:"request_hash"`
	Scope        string                  `json:"scope"`
	JTI          string                  `json:"jti"`
	IssuedAt     int64                   `json:"iat"`
	NotBefore    int64                   `json:"nbf"`
	ExpiresAt    int64                   `json:"exp"`
}

func configuredAccountSummaryTargetSigner(cfg HTTPConfig) (*accountSummaryTargetSigner, error) {
	if !targetAssertionConfigured(cfg) {
		return nil, nil
	}
	if err := validateConfiguredTargetAssertion(cfg); err != nil {
		return nil, err
	}
	clientID := strings.TrimSpace(cfg.TargetAssertionClientID)
	if err := validateConfiguredTargetAssertionClient(cfg, clientID); err != nil {
		return nil, err
	}
	return newAccountSummaryTargetSigner(targetAssertionSignerConfig{
		KeyFile: cfg.TargetAssertionKeyFile, KeyEnv: cfg.TargetAssertionKeyEnv,
		KeyID: cfg.TargetAssertionKeyID, Issuer: cfg.TargetAssertionIssuer,
		Audience: cfg.TargetAssertionAudience, Subject: cfg.TargetAssertionSubject,
		ClientID: clientID, Lifetime: cfg.TargetAssertionLifetime,
	})
}

func targetAssertionConfigured(cfg HTTPConfig) bool {
	return cfg.TargetAssertionKeyFile != "" || cfg.TargetAssertionKeyEnv != "" ||
		cfg.TargetAssertionKeyID != "" || cfg.TargetAssertionIssuer != "" ||
		cfg.TargetAssertionAudience != "" || cfg.TargetAssertionSubject != "" ||
		cfg.TargetAssertionClientID != "" || cfg.TargetAssertionLifetime != 0
}

func validateConfiguredTargetAssertion(cfg HTTPConfig) error {
	// A static bearer has no validated client_id claim. It is therefore not a
	// valid transport identity for a target assertion, even though it remains a
	// supported compatibility mode for non-bound source calls.
	if strings.TrimSpace(cfg.BearerToken) != "" {
		return errors.New("account summary target assertion requires OAuth client credentials")
	}
	if !targetAssertionOAuthConfigValid(cfg) {
		return errors.New("account summary target assertion requires the dedicated OAuth client configuration")
	}
	return nil
}

func targetAssertionOAuthConfigValid(cfg HTTPConfig) bool {
	return strings.TrimSpace(cfg.OAuth.TokenURL) != "" && strings.TrimSpace(cfg.OAuth.ClientID) != "" &&
		strings.TrimSpace(cfg.OAuth.ClientSecret) != "" &&
		strings.Join(strings.Fields(cfg.OAuth.Scope), " ") == accountSummaryAssertionScope &&
		strings.TrimSpace(cfg.OAuth.Resource) == accountSummaryOAuthResource
}

func validateConfiguredTargetAssertionClient(cfg HTTPConfig, clientID string) error {
	oauthClientID := strings.TrimSpace(cfg.OAuth.ClientID)
	if clientID == "" || clientID != oauthClientID {
		return errors.New("account summary target assertion client is inconsistent")
	}
	if cfg.SummaryPath != accountSummaryCanonicalPath {
		return errors.New("account summary target assertion requires the canonical summary path")
	}
	return nil
}

func newAccountSummaryTargetSigner(cfg targetAssertionSignerConfig) (*accountSummaryTargetSigner, error) {
	key, err := accountSummaryTargetSignerKey(cfg)
	if err != nil {
		return nil, err
	}
	lifetime, err := accountSummaryTargetSignerLifetime(cfg)
	if err != nil {
		return nil, err
	}
	return &accountSummaryTargetSigner{privateKey: key, keyID: cfg.KeyID, issuer: cfg.Issuer,
		audience: cfg.Audience, subject: cfg.Subject, clientID: cfg.ClientID, lifetime: lifetime,
		now: func() time.Time { return time.Now().UTC() }, newJTI: uuid.NewRandom}, nil
}

func accountSummaryTargetSignerKey(cfg targetAssertionSignerConfig) (ed25519.PrivateKey, error) {
	key := append(ed25519.PrivateKey(nil), cfg.PrivateKey...)
	if len(key) == 0 {
		return loadEd25519PrivateKey(cfg.KeyFile, cfg.KeyEnv)
	}
	if strings.TrimSpace(cfg.KeyFile) != "" || strings.TrimSpace(cfg.KeyEnv) != "" {
		return nil, errors.New("account summary target assertion key has multiple sources")
	}
	if len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("account summary target assertion private key is invalid")
	}
	return key, nil
}

func accountSummaryTargetSignerLifetime(cfg targetAssertionSignerConfig) (time.Duration, error) {
	lifetime := cfg.Lifetime
	if lifetime <= 0 {
		lifetime = defaultTargetAssertionLifetime
	}
	if !validAccountSummaryTargetSignerConfig(cfg, lifetime) {
		return 0, errors.New("account summary target assertion configuration is invalid")
	}
	return lifetime, nil
}

func validAccountSummaryTargetSignerConfig(cfg targetAssertionSignerConfig, lifetime time.Duration) bool {
	if lifetime <= 0 || lifetime > maxTargetAssertionLifetime || lifetime%time.Second != 0 {
		return false
	}
	return validTargetAssertionComponent(cfg.KeyID, 256) &&
		validTargetAssertionComponent(cfg.Issuer, 512) &&
		cfg.Audience == accountSummaryTargetAudience &&
		cfg.Subject == accountSummaryTargetSubject &&
		validTargetAssertionComponent(cfg.ClientID, 256)
}

func (s *accountSummaryTargetSigner) sign(target *trustedSummaryTarget) (string, error) {
	if s == nil || !trustedTargetFieldsValid(target) {
		return "", errors.New("trusted summary account binding is unavailable")
	}
	now := s.now().UTC()
	issuedAt := now.Unix()
	if issuedAt <= 0 {
		return "", errors.New("account summary target assertion clock is invalid")
	}
	expiresAt := issuedAt + int64(s.lifetime/time.Second)
	newJTI := s.newJTI
	if newJTI == nil {
		newJTI = uuid.NewRandom
	}
	jti, err := newJTI()
	if err != nil {
		return "", errors.New("account summary target assertion id is unavailable")
	}
	hash, err := canonicalAccountSummaryRequestHash(accountSummaryCanonicalMethod, accountSummaryCanonicalPath,
		target.accountID, target.region, target.datasets)
	if err != nil {
		return "", errors.New("account summary target assertion request is invalid")
	}
	datasets := append([]domain.AccountDataset(nil), target.datasets...)
	sort.Slice(datasets, func(left, right int) bool { return string(datasets[left]) < string(datasets[right]) })
	header := targetAssertionHeader{Algorithm: "EdDSA", KeyID: s.keyID, Type: accountSummaryTargetAssertionType}
	claims := targetAssertionClaims{Issuer: s.issuer, Audience: s.audience, Subject: s.subject,
		ClientID: s.clientID, AccountID: target.accountID, CanonicalUID: target.canonicalUID,
		TenantID: target.tenantID, Region: target.region, Datasets: datasets,
		Method: accountSummaryCanonicalMethod, Path: accountSummaryCanonicalPath,
		RequestHash: base64.RawURLEncoding.EncodeToString(hash[:]), Scope: accountSummaryAssertionScope,
		JTI: jti.String(), IssuedAt: issuedAt, NotBefore: issuedAt, ExpiresAt: expiresAt}
	encodedHeader, err := jsonMarshal(header)
	if err != nil {
		return "", errors.New("account summary target assertion could not be encoded")
	}
	encodedClaims, err := jsonMarshal(claims)
	if err != nil {
		return "", errors.New("account summary target assertion could not be encoded")
	}
	protected := base64.RawURLEncoding.EncodeToString(encodedHeader)
	payload := base64.RawURLEncoding.EncodeToString(encodedClaims)
	signed := protected + "." + payload
	signature := ed25519.Sign(s.privateKey, []byte(signed))
	return signed + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// jsonMarshal is a small indirection so tests can assert that only the
// allow-listed struct fields are signed without exposing any secret material.
func jsonMarshal(value any) ([]byte, error) { return json.Marshal(value) }

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
	if !filepath.IsAbs(path) || strings.ContainsAny(path, "\x00\r\n") {
		return nil, errors.New("account summary target assertion key file is unavailable")
	}
	info, statErr := os.Lstat(path)
	if statErr != nil || info.Mode()&os.ModeSymlink != 0 || !safePrivateKeyFileInfo(info) {
		return nil, errors.New("account summary target assertion key file is unavailable")
	}
	file, openErr := os.Open(path)
	if openErr != nil {
		return nil, errors.New("account summary target assertion key file is unavailable")
	}
	openedInfo, openedStatErr := file.Stat()
	if openedStatErr != nil || !safePrivateKeyFileInfo(openedInfo) {
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
		info.Size() >= 0 && info.Size() <= maxTargetAssertionKeyBytes
}

func validEnvironmentName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		first := index == 0
		letter := character >= 'A' && character <= 'Z'
		if !letter && character != '_' && (!(!first && character >= '0' && character <= '9')) {
			return false
		}
	}
	return true
}

func validTargetAssertionComponent(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && value == strings.TrimSpace(value) &&
		strings.IndexFunc(value, func(character rune) bool {
			return unicode.IsControl(character) || unicode.IsSpace(character)
		}) == -1
}

func trustedTargetFieldsValid(target *trustedSummaryTarget) bool {
	if !trustedTargetIdentityFieldsValid(target) {
		return false
	}
	if len(target.datasets) == 0 {
		return false
	}
	return trustedTargetDatasetsValid(target)
}

func trustedTargetIdentityFieldsValid(target *trustedSummaryTarget) bool {
	if target == nil || !validTrustedSummarySource(target.source) {
		return false
	}
	return target.accountID != "" && target.canonicalUID != "" && target.tenantID != "" &&
		target.region != "" && validTargetAssertionComponent(target.accountID, 512) &&
		validTargetAssertionComponent(target.canonicalUID, 512) &&
		validTargetAssertionComponent(target.tenantID, 512) &&
		validTargetAssertionComponent(target.region, 128)
}

func trustedTargetDatasetsValid(target *trustedSummaryTarget) bool {
	for index, dataset := range target.datasets {
		descriptor, ok := domain.DatasetDescriptorOf(dataset)
		if !ok || descriptor.Source != string(target.source) ||
			(index > 0 && string(target.datasets[index-1]) >= string(dataset)) {
			return false
		}
	}
	return true
}

func trustedTargetMatchesRequest(target *trustedSummaryTarget, request SummaryRequest) bool {
	if !trustedTargetFieldsValid(target) || target.accountID != request.AccountID.String() ||
		target.canonicalUID != request.CanonicalUID || target.tenantID != request.TenantID ||
		target.region != request.Region || len(target.datasets) != len(request.Datasets) {
		return false
	}
	for index, dataset := range request.Datasets {
		if target.datasets[index] != dataset {
			return false
		}
	}
	return true
}

func canonicalAccountSummaryRequestHash(method, path, accountID, region string,
	datasets []domain.AccountDataset) ([32]byte, error) {
	bytes, err := canonicalAccountSummaryRequest(method, path, accountID, region, datasets)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(bytes), nil
}

// canonicalAccountSummaryRequest is the cross-language protocol encoding. Each
// field is uint32 big-endian length followed by its exact UTF-8 bytes. The
// version, method, path, account ID, region, and byte-sorted datasets are the
// complete request-hash input; no URL escaping or identifier normalization is
// performed.
func canonicalAccountSummaryRequest(method, path, accountID, region string,
	datasets []domain.AccountDataset) ([]byte, error) {
	values := []string{accountSummaryCanonicalVersion, method, path, accountID, region}
	ordered := make([]string, len(datasets))
	for index, dataset := range datasets {
		ordered[index] = string(dataset)
	}
	sort.Strings(ordered)
	for index, dataset := range ordered {
		if dataset == "" || (index > 0 && ordered[index-1] == dataset) {
			return nil, errors.New("duplicate or empty account summary dataset")
		}
	}
	values = append(values, ordered...)
	var encoded []byte
	for _, value := range values {
		fieldLength, err := canonicalAccountSummaryFieldLength(value)
		if err != nil {
			return nil, err
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], fieldLength)
		encoded = append(encoded, length[:]...)
		encoded = append(encoded, []byte(value)...)
	}
	return encoded, nil
}

func canonicalAccountSummaryFieldLength(value string) (uint32, error) {
	length := uint64(len(value))
	// len is non-negative; this upper bound proves the narrowing is lossless.
	if length > uint64(^uint32(0)) {
		return 0, errors.New("account summary canonical field is too large")
	}
	return uint32(length), nil
}
