package sso

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxJWTBytes           = 64 << 10
	maxJWTPartBytes       = 32 << 10
	maxJWTHeaderPartBytes = 8 << 10
	maxIdentityClaimBytes = 256
	maxOAuthScopeBytes    = 4 << 10
	maxOAuthScopeCount    = 128
	maxUnknownKIDEntries  = 1024
)

func anyJWTPartTooLarge(parts []string) bool {
	for _, part := range parts {
		if len(part) == 0 || len(part) > maxJWTPartBytes {
			return true
		}
	}
	return false
}

func validJWTHeaderKid(value string) bool { return validBoundedString(value, true) == nil }
func looksLikeJWT(value string) bool {
	return len(value) <= maxJWTBytes && strings.Count(value, ".") == 2
}

func supportedAlgorithm(alg string) bool {
	switch alg {
	case "EdDSA", "RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512":
		return true
	default:
		return false
	}
}

// decodeJSONObject rejects duplicate members before decoding. Map decoding is
// otherwise last-wins, which is unsafe when duplicate security claims are used.
func decodeJSONObject(encoded string) (map[string]any, error) {
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(data) == 0 || len(data) > maxJWTPartBytes || !utf8.Valid(data) {
		return nil, errors.New("invalid json encoding")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("trailing json value")
	}
	decoder = json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("json value is not an object")
	}
	return value, nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	if delim == '{' {
		return scanJSONObject(decoder)
	}
	if delim == '[' {
		return scanJSONArray(decoder)
	}
	return errors.New("invalid json delimiter")
}

func scanJSONObject(decoder *json.Decoder) error {
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return errors.New("invalid or duplicate json member")
		}
		seen[key] = true
		if err := scanJSONValue(decoder); err != nil {
			return err
		}
	}
	return expectJSONDelimiter(decoder, '}')
}

func scanJSONArray(decoder *json.Decoder) error {
	for decoder.More() {
		if err := scanJSONValue(decoder); err != nil {
			return err
		}
	}
	return expectJSONDelimiter(decoder, ']')
}

func expectJSONDelimiter(decoder *json.Decoder, expected json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); !ok || delim != expected {
		return errors.New("invalid json structure")
	}
	return nil
}

func validateClaims(claims map[string]any, issuer string, audiences []string, now time.Time) error {
	if err := validateSubjectAndIssuer(claims, issuer); err != nil {
		return err
	}
	if raw, present := claims["aud"]; present && !validAudienceClaim(raw) {
		return errors.New("invalid audience claim")
	}
	if len(audiences) > 0 && !audienceMatches(claims["aud"], audiences) {
		return errors.New("audience mismatch")
	}
	exp, ok := numericClaim(claims["exp"])
	if !ok || float64(now.Unix()) >= exp {
		return errors.New("expired token")
	}
	return validateNotBefore(claims, now)
}

func validateSubjectAndIssuer(claims map[string]any, issuer string) error {
	if _, err := requiredClaimString(claims, "sub"); err != nil {
		return errors.New("missing or invalid subject")
	}
	if issuer == "" {
		if value, exists := claims["iss"]; exists {
			if _, err := claimString(value, false); err != nil {
				return errors.New("invalid issuer")
			}
		}
		return nil
	}
	claimIssuer, err := requiredClaimString(claims, "iss")
	if err != nil || claimIssuer != issuer {
		return errors.New("issuer mismatch")
	}
	return nil
}

func validateNotBefore(claims map[string]any, now time.Time) error {
	value, exists := claims["nbf"]
	if !exists {
		return nil
	}
	nbf, ok := numericClaim(value)
	if !ok {
		return errors.New("invalid not-before claim")
	}
	if float64(now.Unix()+60) < nbf {
		return errors.New("token is not active")
	}
	return nil
}

func requiredClaimString(claims map[string]any, name string) (string, error) {
	value, exists := claims[name]
	if !exists {
		return "", errors.New("missing claim")
	}
	return claimString(value, true)
}

func claimString(value any, required bool) (string, error) {
	text, ok := value.(string)
	if !ok || validBoundedString(text, required) != nil {
		return "", errors.New("invalid string claim")
	}
	return text, nil
}

func validBoundedString(value string, required bool) error {
	if !utf8.ValidString(value) || len(value) > maxIdentityClaimBytes || strings.TrimSpace(value) != value {
		return errors.New("invalid bounded string")
	}
	if required && value == "" {
		return errors.New("empty string")
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return errors.New("control character")
	}
	return nil
}

func audienceValues(raw any) ([]string, error) {
	switch value := raw.(type) {
	case string:
		text, err := claimString(value, true)
		if err != nil {
			return nil, err
		}
		return []string{text}, nil
	case []any:
		if len(value) == 0 || len(value) > 64 {
			return nil, errors.New("invalid audience array")
		}
		result := make([]string, 0, len(value))
		for _, item := range value {
			text, err := claimString(item, true)
			if err != nil {
				return nil, err
			}
			result = append(result, text)
		}
		return result, nil
	default:
		return nil, errors.New("audience is not a string or array")
	}
}

func validAudienceClaim(raw any) bool {
	_, err := audienceValues(raw)
	return err == nil
}

func audienceMatches(raw any, expected []string) bool {
	values, err := audienceValues(raw)
	if err != nil {
		return false
	}
	for _, want := range expected {
		for _, value := range values {
			if want == value {
				return true
			}
		}
	}
	return false
}

func splitAudience(value string) []string {
	result := make([]string, 0, 2)
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			result = append(result, item)
		}
	}
	return result
}

func claimAudience(raw any) []string { values, _ := audienceValues(raw); return values }

func parseOAuthScopes(raw any) []string {
	value, ok := raw.(string)
	if !ok || len(value) == 0 || len(value) > maxOAuthScopeBytes {
		return nil
	}
	scopes := strings.Split(value, " ")
	if len(scopes) > maxOAuthScopeCount {
		return nil
	}
	for _, scope := range scopes {
		if !validOAuthScopeToken(scope) {
			return nil
		}
	}
	return scopes
}

func validOAuthScopeToken(value string) bool {
	if len(value) == 0 || len(value) > maxIdentityClaimBytes {
		return false
	}
	for index := 0; index < len(value); index++ {
		char := value[index]
		if char != 0x21 && (char < 0x23 || char > 0x5b) && (char < 0x5d || char > 0x7e) {
			return false
		}
	}
	return true
}

func numericClaim(value any) (float64, bool) {
	var number float64
	switch value := value.(type) {
	case float64:
		number = value
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	return number, !math.IsNaN(number) && !math.IsInf(number, 0)
}
