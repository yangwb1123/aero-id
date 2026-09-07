package sourceauth

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// ParseEndpoint accepts only the source endpoint shape shared by config and
// runtime construction. Errors intentionally do not include the raw value.
func ParseEndpoint(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("endpoint is empty")
	}
	if strings.IndexAny(trimmed, "?#") >= 0 {
		return nil, errors.New("endpoint must be an absolute URL without userinfo, query, or fragment")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || !validEndpointShape(parsed) {
		return nil, errors.New("endpoint must be an absolute URL without userinfo, query, or fragment")
	}
	if !validEndpointScheme(parsed.Scheme) {
		return nil, errors.New("endpoint must use HTTP or HTTPS")
	}
	if parsed.Scheme != "https" && !IsExactLoopbackHost(parsed.Hostname()) {
		return nil, errors.New("endpoint must use HTTPS outside loopback")
	}
	return parsed, nil
}

func validEndpointShape(parsed *url.URL) bool {
	return validEndpointAuthority(parsed) && validEndpointPath(parsed.Path) && validEndpointPort(parsed)
}

func validEndpointAuthority(parsed *url.URL) bool {
	if parsed == nil || parsed.Hostname() == "" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" {
		return false
	}
	return parsed.RawQuery == "" && !parsed.ForceQuery && parsed.Fragment == "" && parsed.RawFragment == "" &&
		strings.IndexFunc(parsed.Path, unicode.IsControl) < 0
}

func validEndpointPath(path string) bool {
	if path == "" {
		return true
	}
	return strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") && validPathSegments(path)
}

func validEndpointPort(parsed *url.URL) bool {
	if parsed == nil || strings.HasSuffix(parsed.Host, ":") {
		return false
	}
	port := parsed.Port()
	if port == "" {
		return true
	}
	value, err := strconv.Atoi(port)
	return err == nil && value >= 1 && value <= 65535
}

func validEndpointScheme(scheme string) bool {
	return scheme == "http" || scheme == "https"
}

// ValidateOAuthTokenURLShape checks only URL syntax and network-address
// hazards. Authentication policy (approved hosts and loopback environment) is
// applied by ParseOAuthTokenEndpoint.
func ValidateOAuthTokenURLShape(raw string) error {
	_, err := parseOAuthTokenURL(raw)
	return err
}

func parseOAuthTokenURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.IndexAny(trimmed, "?#") >= 0 {
		return nil, errors.New("token URL is invalid")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || !validEndpointShape(parsed) || !validEndpointScheme(parsed.Scheme) || !validOAuthAuthority(parsed.Hostname()) {
		return nil, errors.New("token URL is invalid")
	}
	return parsed, nil
}

func validOAuthAuthority(host string) bool {
	return !forbiddenOAuthHost(host)
}

// ValidateOAuthAllowedHosts rejects malformed or obviously local entries in
// the explicit token-authority allow-list. An empty list is allowed here so
// the runtime can still accept an exact opted-in loopback token endpoint.
func ValidateOAuthAllowedHosts(hosts []string) error {
	for _, host := range hosts {
		if !validOAuthAllowedHost(host) {
			return errors.New("OAuth host allow-list is invalid")
		}
	}
	return nil
}

func validOAuthAllowedHost(value string) bool {
	if !validOAuthAllowedHostText(value) {
		return false
	}
	if strings.Contains(value, "://") {
		return validOAuthAllowedURL(value)
	}
	if strings.ContainsAny(value, "/?#@\\") {
		return false
	}
	parsed, err := url.Parse("https://" + value)
	return err == nil && validOAuthAllowedAuthority(parsed)
}

func validOAuthAllowedHostText(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && strings.IndexFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) < 0
}

func validOAuthAllowedURL(value string) bool {
	parsed, err := parseOAuthTokenURL(value)
	return err == nil && (parsed.Path == "" || parsed.Path == "/") && parsed.RawPath == ""
}

func validOAuthAllowedAuthority(parsed *url.URL) bool {
	if !validOAuthAllowedAuthorityShape(parsed) {
		return false
	}
	return validOAuthHostname(parsed.Hostname()) && !forbiddenOAuthHost(parsed.Hostname())
}

func validOAuthAllowedAuthorityShape(parsed *url.URL) bool {
	if !validOAuthAllowedAuthorityCore(parsed) {
		return false
	}
	return validEndpointPort(parsed)
}

func validOAuthAllowedAuthorityCore(parsed *url.URL) bool {
	if parsed == nil || parsed.Scheme != "https" {
		return false
	}
	if parsed.Host == "" || parsed.User != nil {
		return false
	}
	if !validOAuthAllowedAuthorityPath(parsed) {
		return false
	}
	return validOAuthAllowedAuthorityFragment(parsed)
}

func validOAuthAllowedAuthorityPath(parsed *url.URL) bool {
	return parsed.Path == "" && parsed.RawPath == "" && parsed.RawQuery == "" && !parsed.ForceQuery
}

func validOAuthAllowedAuthorityFragment(parsed *url.URL) bool {
	return parsed.Fragment == "" && parsed.RawFragment == ""
}

// ValidAbsolutePath accepts a request path, not a URL. Query and fragment
// delimiters are rejected before parsing so bare delimiters cannot disappear
// into ForceQuery or an empty Fragment.
func ValidAbsolutePath(value string) bool {
	if value == "" {
		return true
	}
	if !validAbsolutePathPrefix(value) {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil && validAbsolutePathURL(parsed) && validPathSegments(parsed.Path)
}

func validAbsolutePathPrefix(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//") &&
		!strings.ContainsAny(value, "?#\\\x00\r\n")
}

func validAbsolutePathURL(parsed *url.URL) bool {
	if parsed == nil || parsed.Scheme != "" || parsed.Host != "" || parsed.Opaque != "" || parsed.User != nil {
		return false
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	return parsed.Path != ""
}

const maxPathUnescapeDepth = 8

func validPathSegments(path string) bool {
	for depth := 0; depth <= maxPathUnescapeDepth; depth++ {
		if !safePathStage(path) {
			return false
		}
		decoded, err := url.PathUnescape(path)
		if err != nil {
			return false
		}
		if decoded == path {
			return true
		}
		// Reject an encoding that still changes after the bounded number of
		// unescape passes. Otherwise a multiply encoded traversal/control
		// sequence could pass validation and be decoded by a downstream router.
		if depth == maxPathUnescapeDepth {
			return false
		}
		path = decoded
	}
	return false
}

func safePathStage(path string) bool {
	if strings.HasPrefix(path, "//") || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return false
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." || strings.Contains(segment, "\\") {
			return false
		}
	}
	return true
}

// ParseOAuthTokenEndpoint validates a credential-bearing token endpoint. The
// optional source endpoint lets callers require the same explicit loopback
// tuple for both the source and token authorities.
func ParseOAuthTokenEndpoint(raw, environment string, allowLoopback bool, allowedHosts []string,
	sourceEndpoints ...string) (*url.URL, error) {
	if err := ValidateOAuthAllowedHosts(allowedHosts); err != nil {
		return nil, err
	}
	parsed, err := parseOAuthTokenURL(raw)
	if err != nil {
		return nil, err
	}
	explicitLoopback := explicitOAuthLoopback(raw, environment, allowLoopback, sourceEndpoints)
	if err := validateOAuthTokenPolicy(parsed, explicitLoopback, allowedHosts); err != nil {
		return nil, err
	}
	return parsed, nil
}

func explicitOAuthLoopback(raw, environment string, allow bool, sourceEndpoints []string) bool {
	explicit := IsExplicitNonProductionLoopback(raw, environment, allow)
	if len(sourceEndpoints) == 0 || strings.TrimSpace(sourceEndpoints[0]) == "" {
		return explicit
	}
	if !explicit || !IsExplicitNonProductionLoopback(sourceEndpoints[0], environment, allow) {
		return false
	}
	tokenEndpoint, tokenErr := parseOAuthTokenURL(raw)
	sourceEndpoint, sourceErr := ParseEndpoint(sourceEndpoints[0])
	return tokenErr == nil && sourceErr == nil && sameOAuthOrigin(tokenEndpoint, sourceEndpoint)
}

func validateOAuthTokenPolicy(parsed *url.URL, explicitLoopback bool, allowedHosts []string) error {
	loopback := IsExactLoopbackHost(parsed.Hostname())
	if loopback && !explicitLoopback {
		return errors.New("token URL host is not approved")
	}
	if parsed.Scheme != "https" && !explicitLoopback {
		return errors.New("token URL must use HTTPS")
	}
	if !approvedOAuthOrigin(parsed, allowedHosts) && !(len(allowedHosts) == 0 && loopback && explicitLoopback) {
		return errors.New("token URL host is not approved")
	}
	return nil
}

// approvedOAuthOrigin compares the complete scheme, hostname, and effective
// port. A host-only allow-list entry is an HTTPS:443 shorthand; it never
// authorizes a different port or a clear-text token authority.
func approvedOAuthOrigin(parsed *url.URL, allowed []string) bool {
	if parsed == nil {
		return false
	}
	wantScheme := strings.ToLower(parsed.Scheme)
	wantHost := strings.ToLower(parsed.Hostname())
	wantPort := effectiveOAuthPort(parsed)
	for _, value := range allowed {
		entryScheme, entryHost, entryPort, ok := oauthAllowedOrigin(value)
		if ok && entryScheme == wantScheme && entryHost == wantHost && entryPort == wantPort {
			return true
		}
	}
	return false
}

func oauthAllowedOrigin(value string) (scheme, host, port string, ok bool) {
	if value == "" || value != strings.TrimSpace(value) {
		return "", "", "", false
	}
	if strings.Contains(value, "://") {
		parsed, err := parseOAuthTokenURL(value)
		if err != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawPath != "" {
			return "", "", "", false
		}
		return strings.ToLower(parsed.Scheme), strings.ToLower(parsed.Hostname()), effectiveOAuthPort(parsed), true
	}
	parsed, err := url.Parse("https://" + value)
	if err != nil || !validOAuthAllowedAuthority(parsed) {
		return "", "", "", false
	}
	return "https", strings.ToLower(parsed.Hostname()), effectiveOAuthPort(parsed), true
}

func sameOAuthOrigin(left, right *url.URL) bool {
	return left != nil && right != nil && strings.EqualFold(left.Scheme, right.Scheme) &&
		strings.EqualFold(left.Hostname(), right.Hostname()) && effectiveOAuthPort(left) == effectiveOAuthPort(right)
}

func effectiveOAuthPort(value *url.URL) string {
	if value != nil && value.Port() != "" {
		// Compare ports as numbers. URL parsers preserve spellings such as
		// :0443, but those spellings name the same TCP origin as :443.
		if port, err := strconv.Atoi(value.Port()); err == nil {
			return strconv.Itoa(port)
		}
		return value.Port()
	}
	if value != nil {
		return defaultOAuthPort(value.Scheme)
	}
	return "443"
}

// OAuthDestinationAllowed is the final IP-level guard for an OAuth token
// authority. An exact host allow-list is not sufficient: an attacker who can
// influence DNS could make an approved name resolve to a loopback, private,
// link-local, multicast, or otherwise unspecified address. Loopback is
// allowed only for the explicitly approved development tuple.
func OAuthDestinationAllowed(host string, ip net.IP, allowLoopback bool) bool {
	if ip == nil {
		return false
	}
	if allowLoopback && IsExactLoopbackHost(host) {
		return ip.IsLoopback()
	}
	return !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() &&
		!ip.IsUnspecified() && !ip.IsMulticast()
}

func defaultOAuthPort(scheme string) string {
	if strings.EqualFold(scheme, "http") {
		return "80"
	}
	return "443"
}

func forbiddenOAuthHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if !validOAuthHostname(host) || forbiddenOAuthName(host) || strings.HasSuffix(host, ".") || strings.Contains(host, "%") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return forbiddenOAuthIP(host, ip)
	}
	return numericLikeOAuthHost(host)
}

func validOAuthHostname(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" || strings.IndexFunc(host, func(character rune) bool {
		return unicode.IsSpace(character) || unicode.IsControl(character)
	}) >= 0 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !validOAuthLabel(label) {
			return false
		}
	}
	return true
}

func validOAuthLabel(label string) bool {
	if !validOAuthLabelShape(label) {
		return false
	}
	return strings.IndexFunc(label, func(character rune) bool {
		return !validOAuthLabelCharacter(character)
	}) < 0
}

func validOAuthLabelShape(label string) bool {
	return label != "" && len(label) <= 63 && label[0] != '-' && label[len(label)-1] != '-'
}

func validOAuthLabelCharacter(character rune) bool {
	switch {
	case character >= 'a' && character <= 'z':
		return true
	case character >= '0' && character <= '9':
		return true
	case character == '-':
		return true
	default:
		return false
	}
}

var forbiddenOAuthHostNames = map[string]struct{}{
	"metadata": {}, "metadata.google.internal": {}, "instance-data": {},
	"localhost.localdomain": {}, "ip6-localhost": {}, "ip6-loopback": {},
	"host.docker.internal": {}, "kubernetes.default": {},
	"kubernetes.default.svc": {}, "metadata.google.internal.": {},
}

func forbiddenOAuthName(host string) bool {
	if _, forbidden := forbiddenOAuthHostNames[host]; forbidden {
		return true
	}
	return strings.HasSuffix(host, ".localhost")
}

func forbiddenOAuthIP(host string, ip net.IP) bool {
	if IsExactLoopbackHost(host) {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast()
}

func numericLikeOAuthHost(host string) bool {
	if strings.HasPrefix(host, "0x") || strings.HasPrefix(host, "0X") {
		return true
	}
	for _, character := range host {
		if (character < '0' || character > '9') && character != '.' {
			return false
		}
	}
	return host != ""
}
