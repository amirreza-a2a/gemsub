package tester

import (
	"fmt"
	"html"
	"net/http"
	"regexp"
	"strings"

	"gemsub/internal/store"
)

var (
	// Flexible regex matching the ISO country code inside the vXmutd field of WIZ_global_data.
	countryCodeRegex = regexp.MustCompile(`"vXmutd"\s*:\s*"(?:\\.|[^"\\])*?\\?"([A-Z]{2})\\?"`)

	// Matches explicit Gemini restriction flags in client state.
	locationBlockRegex = regexp.MustCompile(`"LOCATION_REJECTED"|"no_access"|"GEO_RESTRICTED"`)
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
// collapses whitespace, and converts to lowercase.
func NormalizeText(s string) string {
	// 1. Unescape HTML entities (&rsquo;, &#8217;, &#39;, &apos;, etc.)
	s = html.UnescapeString(s)

	// 2. Normalize curly quotes and apostrophes to standard ASCII
	// ’ (U+2019), ‘ (U+2018), ʼ (U+02BC), ` (U+0060), ´ (U+00B4) -> ' (ASCII 0x27)
	// “ (U+201C), ” (U+201D) -> " (ASCII 0x22)
	s = strings.ReplaceAll(s, "’", "'")
	s = strings.ReplaceAll(s, "‘", "'")
	s = strings.ReplaceAll(s, "ʼ", "'")
	s = strings.ReplaceAll(s, "`", "'")
	s = strings.ReplaceAll(s, "´", "'")
	s = strings.ReplaceAll(s, "“", "\"")
	s = strings.ReplaceAll(s, "”", "\"")

	// 3. Replace non-breaking spaces (\u00a0) with regular spaces
	s = strings.ReplaceAll(s, "\u00a0", " ")

	// 4. Collapse whitespace
	fields := strings.Fields(s)
	collapsed := strings.Join(fields, " ")

	// 5. Lowercase
	return strings.ToLower(collapsed)
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
		bodyStr := string(body)

		// 1. Negative check: Country code verification from WIZ_global_data
		if matches := countryCodeRegex.FindStringSubmatch(bodyStr); len(matches) > 1 {
			detectedCountry := matches[1]
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
		if locationBlockRegex.MatchString(bodyStr) {
			return ClassificationResult{
				Status:     store.StatusFailed,
				Category:   store.ErrRegionBlocked,
				StatusCode: 200,
				Reason:     "gemini restriction flag detected (LOCATION_REJECTED / no_access)",
				Retryable:  false,
			}
		}

		normalized := NormalizeText(bodyStr)

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
		lowerRaw := strings.ToLower(bodyStr)
		if strings.Contains(lowerRaw, "<title>") {
			start := strings.Index(lowerRaw, "<title>")
			end := strings.Index(lowerRaw[start:], "</title>")
			if end > 0 {
				titleText := lowerRaw[start+7 : start+end]
				if strings.Contains(titleText, "gemini") {
					titleSignal = true
				}
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
