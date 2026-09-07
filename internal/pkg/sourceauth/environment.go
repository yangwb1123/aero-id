package sourceauth

import (
	"strings"
	"unicode"
)

// IsExactLoopbackHost is deliberately narrower than private-network checks:
// only these three spellings receive the unauthenticated compatibility mode.
func IsExactLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

// ValidSecretEnvironmentName accepts only process-environment references in
// aero-id's namespace. Callers may add a narrower feature prefix when a
// secret must be dedicated to one integration.
func ValidSecretEnvironmentName(value string) bool {
	if !validSecretEnvironmentPrefix(value) {
		return false
	}
	for index, character := range value {
		if !validSecretEnvironmentCharacter(index, character) {
			return false
		}
	}
	return true
}

func validSecretEnvironmentPrefix(value string) bool {
	return strings.HasPrefix(value, "AERO_ID_") && value != "AERO_ID_" && len(value) <= 128
}

func validSecretEnvironmentCharacter(index int, character rune) bool {
	letter := character >= 'A' && character <= 'Z'
	digit := character >= '0' && character <= '9'
	return letter || character == '_' || (digit && index > 0)
}

// ApprovedNonProductionEnvironment is the allow-list for environments in
// which the unauthenticated loopback compatibility mode may ever be used.
// Unknown names intentionally do not qualify: an environment typo must not
// silently turn a production endpoint into a development exception.
func ApprovedNonProductionEnvironment(value string) bool {
	if value == "" || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "local", "dev", "development", "test", "testing", "ci":
		return true
	default:
		return false
	}
}

// ProductionLikeEnvironment is the single environment definition used by the
// loopback compatibility policy and configuration validation. Empty is kept
// neutral for legacy configurations; it is still ineligible for loopback
// compatibility because that policy requires a non-empty approved name.
func ProductionLikeEnvironment(value string) bool {
	// Empty is kept neutral for legacy configurations. It is still ineligible
	// for loopback compatibility because IsExplicitNonProductionLoopback also
	// requires an approved, non-empty environment. Any non-empty value outside
	// the allow-list is production-like, including unknown future labels.
	return strings.TrimSpace(value) != "" && !ApprovedNonProductionEnvironment(value)
}

// IsExplicitNonProductionLoopback reports whether an endpoint is eligible for
// an explicitly opted-in, non-production loopback compatibility exception.
// Keep this tuple-based check separate from Decision.Compatibility: callers
// use it for requirements that are independent of the selected auth mode.
func IsExplicitNonProductionLoopback(endpoint, environment string, allow bool) bool {
	if !allow || !ApprovedNonProductionEnvironment(environment) {
		return false
	}
	parsed, err := ParseEndpoint(endpoint)
	return err == nil && IsExactLoopbackHost(parsed.Hostname())
}
