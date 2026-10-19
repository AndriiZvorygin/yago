package documentimport

import (
	"errors"
	"net/url"
	"strings"
	"unicode"

	"github.com/D4rk4/yago/yagocrawlcontract"
)

const (
	LegacyURLStatusMetadataKey           = "yacy.migration_url_status"
	LegacyURLStatusEncodedHostWhitespace = "percent_encoded_whitespace_in_host"
	LegacyURLStatusInvalidHostCharacter  = "invalid_host_character"
	LegacyURLStatusInvalidHostEscape     = "invalid_percent_escape_in_host"
	LegacyURLStatusInvalidEscape         = "invalid_percent_escape_in_url"
	LegacyURLStatusInvalidAuthority      = "invalid_source_authority"
	LegacyURLStatusExceedsCrawlLimit     = "exceeds_yago_crawl_url_limit"
)

// ValidateURLIdentity accepts ordinary crawlable URLs plus marked malformed
// YaCy host identities. A legacy form is preserved opaquely and must
// never be scheduled for recrawl.
func ValidateURLIdentity(rawURL, legacyStatus string) error {
	if len(rawURL) == 0 || len(rawURL) > MaximumMigrationURLBytes {
		return errors.New("URL identity is empty or exceeds the supported limit")
	}
	parsed, err := url.Parse(rawURL)
	if len(rawURL) <= yagocrawlcontract.MaximumCrawlURLBytes && err == nil &&
		parsed.Host != "" && parsed.User == nil && parsed.Fragment == "" &&
		isSupportedImportScheme(parsed.Scheme) {
		return nil
	}
	if legacyStatus != "" && legacyStatus == PreservableLegacyURLStatus(rawURL) {
		return nil
	}
	return errors.New("URL identity is not supported for document import")
}

// IsPreservableLegacyURL identifies malformed YaCy host identities that are
// safe to retain as opaque, non-fetchable document keys. It does not
// normalize or rewrite the source URL.
func IsPreservableLegacyURL(rawURL string) bool {
	return PreservableLegacyURLStatus(rawURL) != ""
}

// PreservableLegacyURLStatus returns a stable marker for an opaque legacy
// identity, or empty when the URL must be rejected. Malformed escapes and host
// characters are retained only for supported schemes without credentials,
// controls, or ambiguous authorities. Invalid hosts containing fragments or
// backslashes remain opaque and are never fetched.
func PreservableLegacyURLStatus(rawURL string) string {
	if len(rawURL) > yagocrawlcontract.MaximumCrawlURLBytes {
		return overlongURLIdentityStatus(rawURL)
	}
	if status := malformedQueryPercentEscapeStatus(rawURL); status != "" {
		return status
	}
	authority, parseErr, ok := legacyAuthorityError(rawURL)
	if !ok {
		return ""
	}
	urlErr, ok := parseErr.(*url.Error)
	if !ok {
		return ""
	}
	switch urlErr.Err.(type) {
	case url.EscapeError:
		if strings.ContainsAny(rawURL, "#\\") {
			return ""
		}
		if status := encodedHostWhitespaceStatus(
			authority,
			urlErr.Err.(url.EscapeError),
		); status != "" {
			return status
		}
		if status := invalidHostPercentEscapeStatus(rawURL, authority); status != "" {
			return status
		}
		if status := malformedPercentEscapeURLStatus(rawURL, authority); status != "" {
			return status
		}
		return malformedLegacyEscapeStatus(rawURL, authority)
	case url.InvalidHostError:
		if strings.IndexFunc(authority, unicode.IsControl) >= 0 ||
			strings.Contains(authority, "@") {
			return ""
		}
		// Some historical YaCy exports marked percent-encoded '@' host
		// identities as invalid host escapes. Keep that identity opaque for
		// migration; the importer prevents opaque identities from recrawling.
		if strings.Contains(strings.ToLower(authority), "%40") {
			return LegacyURLStatusInvalidHostEscape
		}
		return LegacyURLStatusInvalidHostCharacter
	default:
		return preservableInvalidAuthorityStatus(rawURL, authority)
	}
}

func overlongURLIdentityStatus(rawURL string) string {
	if len(rawURL) > MaximumMigrationURLBytes ||
		strings.IndexFunc(rawURL, unicode.IsControl) >= 0 ||
		strings.ContainsAny(rawURL, "#\\") || hasMalformedPercentEscape(rawURL) {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		!isSupportedImportScheme(parsed.Scheme) ||
		strings.Contains(strings.ToLower(parsed.Host), "%40") {
		return ""
	}
	return LegacyURLStatusExceedsCrawlLimit
}

func preservableInvalidAuthorityStatus(rawURL, authority string) string {
	if authority == "" || strings.Contains(authority, "@") ||
		strings.Contains(strings.ToLower(authority), "%40") ||
		strings.IndexFunc(rawURL, unicode.IsControl) >= 0 ||
		strings.ContainsAny(rawURL, "#\\") {
		return ""
	}
	separator := strings.Index(rawURL, "://")
	if separator < 0 || !isSupportedImportScheme(rawURL[:separator]) {
		return ""
	}
	return LegacyURLStatusInvalidAuthority
}

func malformedLegacyEscapeStatus(rawURL, authority string) string {
	if authority == "" || strings.Contains(authority, "@") ||
		strings.Contains(strings.ToLower(authority), "%40") ||
		strings.IndexFunc(rawURL, unicode.IsControl) >= 0 ||
		strings.ContainsAny(rawURL, "#\\") || !hasMalformedPercentEscape(rawURL) {
		return ""
	}
	separator := strings.Index(rawURL, "://")
	if separator < 0 || !isSupportedImportScheme(rawURL[:separator]) {
		return ""
	}
	return LegacyURLStatusInvalidEscape
}

func malformedQueryPercentEscapeStatus(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" ||
		!isSupportedImportScheme(parsed.Scheme) || !hasMalformedPercentEscape(parsed.RawQuery) {
		return ""
	}
	return LegacyURLStatusInvalidEscape
}

func malformedPercentEscapeURLStatus(rawURL, authority string) string {
	if strings.IndexFunc(rawURL, unicode.IsControl) >= 0 ||
		!hasMalformedPercentEscape(rawURL) || strings.ContainsAny(rawURL, "#\\") {
		return ""
	}
	separator := strings.Index(rawURL, "://")
	if separator < 0 {
		return ""
	}
	authorityEnd := separator + 3 + len(authority)
	if authorityEnd > len(rawURL) || !hasMalformedPercentEscape(rawURL[authorityEnd:]) {
		return ""
	}
	base, err := url.Parse(rawURL[:separator] + "://" + authority + "/")
	if err != nil || base.Host == "" || base.User != nil ||
		!isSupportedImportScheme(base.Scheme) {
		return ""
	}
	return LegacyURLStatusInvalidEscape
}

func invalidHostPercentEscapeStatus(rawURL, authority string) string {
	if strings.IndexFunc(rawURL, unicode.IsControl) >= 0 ||
		!strings.Contains(authority, "%") {
		return ""
	}
	if strings.ContainsAny(rawURL, "#\\") {
		return ""
	}
	separator := strings.Index(rawURL, "://")
	if separator < 0 {
		return ""
	}
	scheme := strings.ToLower(rawURL[:separator])
	if !isSupportedImportScheme(scheme) {
		return ""
	}
	// url.Parse has already rejected this percent-bearing authority. Keep the
	// original as an opaque identity; the importer will never schedule it.
	return LegacyURLStatusInvalidHostEscape
}

func hasMalformedPercentEscape(value string) bool {
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			continue
		}
		if index+2 >= len(value) || !isHexByte(value[index+1]) || !isHexByte(value[index+2]) {
			return true
		}
		index += 2
	}
	return false
}

func isHexByte(value byte) bool {
	return (value >= '0' && value <= '9') ||
		(value >= 'a' && value <= 'f') || (value >= 'A' && value <= 'F')
}

func legacyAuthorityError(rawURL string) (string, error, bool) {
	if len(rawURL) == 0 || len(rawURL) > yagocrawlcontract.MaximumCrawlURLBytes ||
		strings.ContainsAny(rawURL, "\r\n\t") {
		return "", nil, false
	}
	separator := strings.Index(rawURL, "://")
	if separator < 0 {
		return "", nil, false
	}
	scheme := strings.ToLower(rawURL[:separator])
	if !isSupportedImportScheme(scheme) {
		return "", nil, false
	}
	authority := rawURL[separator+3:]
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	if authority == "" || strings.Contains(authority, "@") {
		return "", nil, false
	}
	_, parseErr := url.Parse(rawURL)
	if parseErr == nil {
		return "", nil, false
	}
	var urlErr *url.Error
	if !errors.As(parseErr, &urlErr) {
		return "", nil, false
	}
	return authority, urlErr, true
}

func encodedHostWhitespaceStatus(authority string, escapeErr url.EscapeError) string {
	if !strings.Contains(strings.ToLower(authority), "%20") ||
		!strings.Contains(string(escapeErr), "%20") {
		return ""
	}
	decodedAuthority := strings.ReplaceAll(strings.ToLower(authority), "%20", " ")
	if strings.Contains(decodedAuthority, "%") {
		return ""
	}
	hasWhitespace := false
	for _, r := range decodedAuthority {
		if unicode.IsSpace(r) {
			hasWhitespace = true
			continue
		}
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') &&
			r != '.' && r != '-' {
			return ""
		}
	}
	if !hasWhitespace {
		return ""
	}
	return LegacyURLStatusEncodedHostWhitespace
}

func isSupportedImportScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "http", "https", "ftp", "smb":
		return true
	default:
		return false
	}
}
