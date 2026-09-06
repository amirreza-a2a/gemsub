package tester

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"gemsub/internal/store"
)

// maxInspectBytes bounds payload inspection to 1MB.
const maxInspectBytes = 1 << 20

var (
	// Flexible regex matching the ISO country code inside the vXmutd field of WIZ_global_data.
	countryCodeRegex = regexp.MustCompile(`"vXmutd"\s*:\s*"(?:\\.|[^"\\])*?\\?"([A-Z]{2})\\?"`)

	// Matches explicit Gemini restriction flags in client state.
	locationBlockRegex = regexp.MustCompile(`"LOCATION_REJECTED"|"no_access"|"GEO_RESTRICTED"`)

	// Matches document title case-insensitively.
	titleRegex = regexp.MustCompile(`(?i)<title[^>]*>([\s\S]*?)</title>`)
)

// List of ISO country codes restricted by Google AI services.
var restrictedCountries = map[string]bool{
	"IR": true, // Iran
	"RU": true, // Russia
	"CU": true, // Cuba
	"SY": true, // Syria
	"KP": true, // North Korea
	"BY": true, // Belarus
}

// NormalizeText unescapes HTML entities, normalizes quotes/apostrophes,
// collapses whitespace, and converts to lowercase using a streaming single-pass scanner.
func NormalizeText(s string) string {
	if len(s) == 0 {
		return ""
	}
	if len(s) > maxInspectBytes {
		s = s[:maxInspectBytes]
	}

	var b strings.Builder
	b.Grow(len(s))

	inWhitespace := false
	for i := 0; i < len(s); {
		// Skip <style>...</style> non-content segments
		if i+6 <= len(s) && (s[i] == '<' && (s[i+1] == 's' || s[i+1] == 'S') && (s[i+2] == 't' || s[i+2] == 'T') && (s[i+3] == 'y' || s[i+3] == 'Y') && (s[i+4] == 'l' || s[i+4] == 'L') && (s[i+5] == 'e' || s[i+5] == 'E')) {
			endIdx := indexCaseInsensitiveString(s[i+6:], "</style>")
			if endIdx >= 0 {
				i += 6 + endIdx + 8
				continue
			}
		}

		// Skip <!-- ... --> comments
		if i+4 <= len(s) && s[i] == '<' && s[i+1] == '!' && s[i+2] == '-' && s[i+3] == '-' {
			endIdx := strings.Index(s[i+4:], "-->")
			if endIdx >= 0 {
				i += 4 + endIdx + 3
				continue
			}
		}

		if s[i] == '&' {
			semi := strings.IndexByte(s[i:], ';')
			if semi > 1 && semi <= 32 && !strings.ContainsAny(s[i:i+semi], " \t\r\n<>") {
				entity := s[i : i+semi+1]
				if handled := handleEntity(&b, entity, &inWhitespace); handled {
					i += semi + 1
					continue
				}
				unescaped := html.UnescapeString(entity)
				if unescaped != entity {
					for _, ur := range unescaped {
						processRune(&b, ur, &inWhitespace)
					}
					i += semi + 1
					continue
				}
			}
		}

		var r rune
		var size int
		if s[i] < utf8.RuneSelf {
			r = rune(s[i])
			size = 1
		} else {
			r, size = utf8.DecodeRuneInString(s[i:])
		}
		i += size

		processRune(&b, r, &inWhitespace)
	}

	return b.String()
}

// NormalizeBytes normalizes byte slices directly to avoid heap string allocations,
// unescaping HTML entities, collapsing whitespace, converting to lowercase, and
// bounding inspection by skipping non-content segments.
func NormalizeBytes(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if len(b) > maxInspectBytes {
		b = b[:maxInspectBytes]
	}

	var builder strings.Builder
	builder.Grow(len(b))

	inWhitespace := false
	for i := 0; i < len(b); {
		// Skip <style>...</style> non-content segments
		if i+6 <= len(b) && (b[i] == '<' && (b[i+1] == 's' || b[i+1] == 'S') && (b[i+2] == 't' || b[i+2] == 'T') && (b[i+3] == 'y' || b[i+3] == 'Y') && (b[i+4] == 'l' || b[i+4] == 'L') && (b[i+5] == 'e' || b[i+5] == 'E')) {
			endIdx := indexCaseInsensitiveBytes(b[i+6:], []byte("</style>"))
			if endIdx >= 0 {
				i += 6 + endIdx + 8
				continue
			}
		}

		// Skip <!-- ... --> comments
		if i+4 <= len(b) && b[i] == '<' && b[i+1] == '!' && b[i+2] == '-' && b[i+3] == '-' {
			endIdx := bytes.Index(b[i+4:], []byte("-->"))
			if endIdx >= 0 {
				i += 4 + endIdx + 3
				continue
			}
		}

		if b[i] == '&' {
			semi := bytes.IndexByte(b[i:], ';')
			if semi > 1 && semi <= 32 && !bytes.ContainsAny(b[i:i+semi], " \t\r\n<>") {
				entity := string(b[i : i+semi+1])
				if handled := handleEntity(&builder, entity, &inWhitespace); handled {
					i += semi + 1
					continue
				}
				unescaped := html.UnescapeString(entity)
				if unescaped != entity {
					for _, ur := range unescaped {
						processRune(&builder, ur, &inWhitespace)
					}
					i += semi + 1
					continue
				}
			}
		}

		var r rune
		var size int
		if b[i] < utf8.RuneSelf {
			r = rune(b[i])
			size = 1
		} else {
			r, size = utf8.DecodeRune(b[i:])
		}
		i += size

		processRune(&builder, r, &inWhitespace)
	}

	return builder.String()
}

func indexCaseInsensitiveBytes(b []byte, target []byte) int {
	if len(target) == 0 {
		return 0
	}
	if len(b) < len(target) {
		return -1
	}
	max := len(b) - len(target)
	firstLower := target[0] | 0x20
	for i := 0; i <= max; i++ {
		if (b[i] | 0x20) == firstLower {
			match := true
			for j := 1; j < len(target); j++ {
				if (b[i+j] | 0x20) != (target[j] | 0x20) {
					match = false
					break
				}
			}
			if match {
				return i
			}
		}
	}
	return -1
}

func indexCaseInsensitiveString(s string, target string) int {
	if len(target) == 0 {
		return 0
	}
	if len(s) < len(target) {
		return -1
	}
	max := len(s) - len(target)
	firstLower := target[0] | 0x20
	for i := 0; i <= max; i++ {
		if (s[i] | 0x20) == firstLower {
			match := true
			for j := 1; j < len(target); j++ {
				if (s[i+j] | 0x20) != (target[j] | 0x20) {
					match = false
					break
				}
			}
			if match {
				return i
			}
		}
	}
	return -1
}

func processRune(b *strings.Builder, r rune, inWhitespace *bool) {
	switch r {
	case '’', '‘', 'ʼ', '`', '´':
		r = '\''
	case '“', '”':
		r = '"'
	case '\u00a0':
		r = ' '
	}

	if unicode.IsSpace(r) {
		if !*inWhitespace && b.Len() > 0 {
			*inWhitespace = true
		}
		return
	}

	if *inWhitespace {
		b.WriteByte(' ')
		*inWhitespace = false
	}

	if r < utf8.RuneSelf {
		if 'A' <= r && r <= 'Z' {
			b.WriteByte(byte(r + ('a' - 'A')))
		} else {
			b.WriteByte(byte(r))
		}
	} else {
		b.WriteRune(unicode.ToLower(r))
	}
}

func handleEntity(b *strings.Builder, entity string, inWhitespace *bool) bool {
	switch entity {
	case "&rsquo;", "&lsquo;", "&apos;":
		processRune(b, '\'', inWhitespace)
		return true
	case "&quot;", "&ldquo;", "&rdquo;":
		processRune(b, '"', inWhitespace)
		return true
	case "&nbsp;":
		processRune(b, ' ', inWhitespace)
		return true
	case "&amp;":
		processRune(b, '&', inWhitespace)
		return true
	case "&lt;":
		processRune(b, '<', inWhitespace)
		return true
	case "&gt;":
		processRune(b, '>', inWhitespace)
		return true
	}
	if len(entity) > 3 && entity[1] == '#' {
		var cp rune
		if entity[2] == 'x' || entity[2] == 'X' {
			for _, ch := range entity[3 : len(entity)-1] {
				if '0' <= ch && ch <= '9' {
					cp = cp*16 + rune(ch-'0')
				} else if 'a' <= ch && ch <= 'f' {
					cp = cp*16 + rune(ch-'a'+10)
				} else if 'A' <= ch && ch <= 'F' {
					cp = cp*16 + rune(ch-'A'+10)
				} else {
					return false
				}
			}
		} else {
			for _, ch := range entity[2 : len(entity)-1] {
				if '0' <= ch && ch <= '9' {
					cp = cp*10 + rune(ch-'0')
				} else {
					return false
				}
			}
		}
		if cp > 0 && utf8.ValidRune(cp) {
			processRune(b, cp, inWhitespace)
			return true
		}
	}
	return false
}

// ClassificationResult represents the structured outcome of evaluating a probe.
type ClassificationResult struct {
	Status     store.Status
	Category   store.ErrorCategory
	StatusCode int
	Reason     string
	Retryable  bool
}

// ClassifyDialError determines whether an error encountered during dial or HTTP
// transport is a retryable tunnel/CDN error or a hard failure, and assigns a machine-readable category.
func ClassifyDialError(err error) ClassificationResult {
	if err == nil {
		return ClassificationResult{
			Status:   store.StatusPassed,
			Category: store.ErrNone,
			Reason:   "ok",
		}
	}

	errStr := err.Error()
	lower := strings.ToLower(errStr)

	switch {
	case strings.Contains(errStr, "unexpected HTTP response status: 429") || strings.Contains(errStr, "status: 429"):
		return ClassificationResult{
			Status:     store.StatusInconclusive,
			Category:   store.ErrProxyRateLimited,
			StatusCode: 429,
			Reason:     "proxy tunnel CDN returned HTTP 429",
			Retryable:  true,
		}
	case strings.Contains(errStr, "unexpected HTTP response status: 503") || strings.Contains(errStr, "status: 503"):
		return ClassificationResult{
			Status:     store.StatusInconclusive,
			Category:   store.ErrProxyError,
			StatusCode: 503,
			Reason:     "proxy tunnel CDN returned HTTP 503 (service unavailable)",
			Retryable:  true,
		}
	case strings.Contains(errStr, "unexpected HTTP response status: 403"):
		return ClassificationResult{
			Status:     store.StatusFailed,
			Category:   store.ErrProxyError,
			StatusCode: 403,
			Reason:     "proxy tunnel CDN returned HTTP 403 (forbidden)",
			Retryable:  false,
		}
	case strings.Contains(errStr, "unexpected HTTP response status:"):
		return ClassificationResult{
			Status:    store.StatusFailed,
			Category:  store.ErrProxyError,
			Reason:    errStr,
			Retryable: false,
		}
	case strings.Contains(lower, "connection refused"):
		return ClassificationResult{
			Status:    store.StatusFailed,
			Category:  store.ErrConnRefused,
			Reason:    "connection refused",
			Retryable: false,
		}
	case errors.Is(err, context.Canceled):
		return ClassificationResult{
			Status:    store.StatusInconclusive,
			Category:  store.ErrTimeout,
			Reason:    "context canceled",
			Retryable: false,
		}
	case strings.Contains(lower, "i/o timeout") || strings.Contains(lower, "context deadline exceeded") || strings.Contains(lower, "client.timeout exceeded"):
		return ClassificationResult{
			Status:    store.StatusFailed,
			Category:  store.ErrTimeout,
			Reason:    "timeout",
			Retryable: false,
		}
	case strings.Contains(lower, "reality verification failed"):
		return ClassificationResult{
			Status:    store.StatusFailed,
			Category:  store.ErrReality,
			Reason:    "reality verification failed",
			Retryable: false,
		}
	case strings.Contains(lower, "tls: handshake failure") || strings.Contains(lower, "remote error: tls") || strings.Contains(lower, "x509:"):
		return ClassificationResult{
			Status:    store.StatusFailed,
			Category:  store.ErrTLS,
			Reason:    errStr,
			Retryable: false,
		}
	case strings.Contains(lower, "eof") || strings.Contains(lower, "reset by peer") || strings.Contains(lower, "broken pipe"):
		return ClassificationResult{
			Status:    store.StatusFailed,
			Category:  store.ErrReset,
			Reason:    "connection reset / EOF",
			Retryable: false,
		}
	case strings.Contains(lower, "utls") || strings.Contains(lower, "fingerprint"):
		return ClassificationResult{
			Status:    store.StatusFailed,
			Category:  store.ErrConfig,
			Reason:    errStr,
			Retryable: false,
		}
	default:
		return ClassificationResult{
			Status:    store.StatusFailed,
			Category:  store.ErrProxyError,
			Reason:    errStr,
			Retryable: false,
		}
	}
}

// ClassifyResponse evaluates an HTTP response and response body for Gemini availability.
func ClassifyResponse(resp *http.Response, body []byte, blockPhrases []string) ClassificationResult {
	if resp == nil {
		return ClassificationResult{
			Status:   store.StatusFailed,
			Category: store.ErrTargetOther,
			Reason:   "nil response",
		}
	}

	statusCode := resp.StatusCode

	switch statusCode {
	case http.StatusTooManyRequests: // 429
		return ClassificationResult{
			Status:     store.StatusInconclusive,
			Category:   store.ErrTargetRateLimited,
			StatusCode: 429,
			Reason:     "target HTTP 429 Too Many Requests",
			Retryable:  true,
		}

	case http.StatusServiceUnavailable: // 503
		return ClassificationResult{
			Status:     store.StatusInconclusive,
			Category:   store.ErrTargetError,
			StatusCode: 503,
			Reason:     "target HTTP 503 Service Unavailable",
			Retryable:  true,
		}

	case http.StatusForbidden: // 403
		return ClassificationResult{
			Status:     store.StatusFailed,
			Category:   store.ErrTargetDenied,
			StatusCode: 403,
			Reason:     "target HTTP 403 Forbidden",
			Retryable:  false,
		}

	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout:
		return ClassificationResult{
			Status:     store.StatusInconclusive,
			Category:   store.ErrTargetError,
			StatusCode: statusCode,
			Reason:     fmt.Sprintf("target HTTP %d", statusCode),
			Retryable:  false,
		}

	case http.StatusOK:
		// Bound inspection to at most maxInspectBytes (1MB).
		if len(body) > maxInspectBytes {
			body = body[:maxInspectBytes]
		}

		// 1. Negative check: Country code verification from WIZ_global_data
		if matches := countryCodeRegex.FindSubmatch(body); len(matches) > 1 {
			detectedCountry := string(matches[1])
			if restrictedCountries[detectedCountry] {
				return ClassificationResult{
					Status:     store.StatusFailed,
					Category:   store.ErrRegionBlocked,
					StatusCode: 200,
					Reason:     fmt.Sprintf("restricted country detected in WIZ_global_data: %s", detectedCountry),
					Retryable:  false,
				}
			}
		}

		// 2. Negative check: Explicit restriction flags
		if locationBlockRegex.Match(body) {
			return ClassificationResult{
				Status:     store.StatusFailed,
				Category:   store.ErrRegionBlocked,
				StatusCode: 200,
				Reason:     "gemini restriction flag detected (LOCATION_REJECTED / no_access)",
				Retryable:  false,
			}
		}

		normalized := NormalizeBytes(body)

		// 3. Negative check: Static block phrases
		for _, phrase := range blockPhrases {
			normPhrase := NormalizeText(phrase)
			if normPhrase != "" && strings.Contains(normalized, normPhrase) {
				return ClassificationResult{
					Status:     store.StatusFailed,
					Category:   store.ErrRegionBlocked,
					StatusCode: 200,
					Reason:     "target returned regional restriction message",
					Retryable:  false,
				}
			}
		}

		// 4. Negative check: Interception / captive portal / CDN challenges
		if strings.Contains(normalized, "cf-chl-bypass") ||
			strings.Contains(normalized, "attention required! | cloudflare") ||
			strings.Contains(normalized, "just a moment...") ||
			strings.Contains(normalized, "routeros") ||
			strings.Contains(normalized, "mikrotik") {
			return ClassificationResult{
				Status:     store.StatusFailed,
				Category:   store.ErrTargetOther,
				StatusCode: 200,
				Reason:     "interception: CDN challenge or captive portal page",
				Retryable:  false,
			}
		}

		// 5. Positive confidence signals
		// Signal A: Google Origin Proof (Server header or Google domain cookies)
		serverHeader := resp.Header.Get("Server")
		isGoogleServer := strings.Contains(strings.ToUpper(serverHeader), "ESF") ||
			strings.Contains(strings.ToUpper(serverHeader), "GSE") ||
			strings.Contains(strings.ToUpper(serverHeader), "SFFE") ||
			strings.Contains(strings.ToUpper(serverHeader), "GWS")

		hasGoogleCookie := false
		for _, cookie := range resp.Cookies() {
			if strings.Contains(cookie.Domain, "google.com") || strings.Contains(cookie.Domain, "gemini.google.com") {
				hasGoogleCookie = true
				break
			}
		}
		originSignal := isGoogleServer || hasGoogleCookie

		// Signal B: Brand & Title signals
		titleSignal := false
		if matches := titleRegex.FindSubmatch(body); len(matches) > 1 {
			titleText := strings.ToLower(string(matches[1]))
			if strings.Contains(titleText, "gemini") {
				titleSignal = true
			}
		}

		brandSignal := strings.Contains(normalized, "gemini")

		// Signal C: Distinctive Gemini app / structural markers
		appMarkerSignal := strings.Contains(normalized, "bardchatui") ||
			strings.Contains(normalized, "wiz_global_data") ||
			strings.Contains(normalized, "google gemini")

		// Confidence Decision Rules:
		isVerified := false
		if originSignal && (titleSignal || brandSignal) {
			isVerified = true
		} else if titleSignal && appMarkerSignal {
			isVerified = true
		}

		if isVerified {
			return ClassificationResult{
				Status:     store.StatusPassed,
				Category:   store.ErrNone,
				StatusCode: 200,
				Reason:     "ok",
				Retryable:  false,
			}
		}

		return ClassificationResult{
			Status:     store.StatusFailed,
			Category:   store.ErrTargetOther,
			StatusCode: 200,
			Reason:     "target response missing verified Gemini origin and application markers (possible interception / false positive)",
			Retryable:  false,
		}

	default:
		return ClassificationResult{
			Status:     store.StatusFailed,
			Category:   store.ErrTargetOther,
			StatusCode: statusCode,
			Reason:     fmt.Sprintf("unexpected HTTP status %d", statusCode),
			Retryable:  false,
		}
	}
}
