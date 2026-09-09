// Package gemini implements the Gemini application compatibility probe.
// It encapsulates all Gemini-specific concerns: browser impersonation headers,
// response body download (1MB cap), WIZ_global_data country extraction,
// location restriction detection, block phrase evaluation, Google origin
// validation, and Gemini brand/DOM marker verification.
//
// This package has ZERO knowledge of transport health probing, sing-box Box
// lifecycle, retry orchestration, or candidate pool mechanics.
package gemini

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/textutil"
	"gemsub/internal/tester/transport"
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

// Config defines the parameters for the Gemini application compatibility probe.
type Config struct {
	URL          string        // Default: "https://gemini.google.com/"
	BlockPhrases []string      // Phrases indicating regional block
	Timeout      time.Duration // Gemini application timeout (bounds the entire request)
	DialTimeout  time.Duration // TLS handshake timeout (typically shorter than Timeout)
}

// Result contains the target compatibility outcome.
type Result struct {
	Status     store.Status
	Category   store.ErrorCategory
	StatusCode int
	Reason     string
	Latency    time.Duration
	Retryable  bool
	RetryAfter *time.Duration
	Err        error
}

// Probe executes the Gemini application check using an established transport dialer.
// It sends browser-impersonation headers, downloads up to 1MB of response body,
// and classifies the response for Gemini availability.
func Probe(ctx context.Context, dialFn transport.DialFunc, cfg Config) Result {
	geminiURL := cfg.URL
	if geminiURL == "" {
		geminiURL = "https://gemini.google.com/"
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tlsTimeout := cfg.DialTimeout
	if tlsTimeout <= 0 {
		tlsTimeout = timeout
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext:         dialFn,
			TLSHandshakeTimeout: tlsTimeout,
		},
	}

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, geminiURL, nil)
	if err != nil {
		return Result{
			Status:   store.StatusFailed,
			Category: store.ErrConfig,
			Reason:   fmt.Sprintf("build gemini request: %v", err),
			Err:      fmt.Errorf("build request: %w", err),
		}
	}

	// Browser impersonation headers for realistic Gemini probing.
	// These are Gemini-specific: a transport health probe uses minimal neutral headers.
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-CH-UA", `"Chromium";v="131", "Not_A Brand";v="24"`)

	start := time.Now()
	resp, err := client.Do(req)
	latency := time.Since(start)

	if err != nil {
		// Dial/TLS/transport errors during the Gemini request are returned
		// with Err populated so the caller can apply transport error classification.
		return Result{
			Status:   store.StatusFailed,
			Category: store.ErrProxyError,
			Reason:   err.Error(),
			Latency:  latency,
			Err:      err,
		}
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInspectBytes)) // 1MB cap
	if err != nil {
		return Result{
			Status:   store.StatusFailed,
			Category: store.ErrProxyError,
			Reason:   fmt.Sprintf("read gemini body: %v", err),
			Latency:  latency,
			Err:      fmt.Errorf("read body: %w", err),
		}
	}

	res := ClassifyResponse(resp, body, cfg.BlockPhrases)
	res.Latency = latency
	return res
}

// ParseRetryAfter parses an HTTP Retry-After header value into a time.Duration.
// It supports both delta-seconds (e.g. "120") and HTTP-date formats (RFC 1123, RFC 850, ANSI C).
// Returns nil if the header is empty or cannot be parsed.
func ParseRetryAfter(header string) *time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	if sec, err := strconv.Atoi(header); err == nil && sec >= 0 {
		d := time.Duration(sec) * time.Second
		return &d
	}
	if t, err := http.ParseTime(header); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return &d
	}
	return nil
}

// ClassifyResponse evaluates an HTTP response and response body for Gemini availability.
// This is the core Gemini classification logic, exported for direct unit testing
// with synthetic HTTP responses.
func ClassifyResponse(resp *http.Response, body []byte, blockPhrases []string) Result {
	res := classifyResponsePayload(resp, body, blockPhrases)
	if res.Retryable && resp != nil && resp.Header != nil {
		res.RetryAfter = ParseRetryAfter(resp.Header.Get("Retry-After"))
	}
	return res
}

func classifyResponsePayload(resp *http.Response, body []byte, blockPhrases []string) Result {
	if resp == nil {
		return Result{
			Status:   store.StatusFailed,
			Category: store.ErrTargetOther,
			Reason:   "nil response",
		}
	}

	statusCode := resp.StatusCode

	switch statusCode {
	case http.StatusTooManyRequests: // 429
		return Result{
			Status:     store.StatusInconclusive,
			Category:   store.ErrTargetRateLimited,
			StatusCode: 429,
			Reason:     "target HTTP 429 Too Many Requests",
			Retryable:  true,
		}

	case http.StatusServiceUnavailable: // 503
		return Result{
			Status:     store.StatusInconclusive,
			Category:   store.ErrTargetError,
			StatusCode: 503,
			Reason:     "target HTTP 503 Service Unavailable",
			Retryable:  true,
		}

	case http.StatusForbidden: // 403
		return Result{
			Status:     store.StatusFailed,
			Category:   store.ErrTargetDenied,
			StatusCode: 403,
			Reason:     "target HTTP 403 Forbidden",
			Retryable:  false,
		}

	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusGatewayTimeout:
		return Result{
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
				return Result{
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
			return Result{
				Status:     store.StatusFailed,
				Category:   store.ErrRegionBlocked,
				StatusCode: 200,
				Reason:     "gemini restriction flag detected (LOCATION_REJECTED / no_access)",
				Retryable:  false,
			}
		}

		normalized := textutil.NormalizeBytes(body)

		// 3. Negative check: Static block phrases
		for _, phrase := range blockPhrases {
			normPhrase := textutil.NormalizeText(phrase)
			if normPhrase != "" && strings.Contains(normalized, normPhrase) {
				return Result{
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
			return Result{
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
			return Result{
				Status:     store.StatusPassed,
				Category:   store.ErrNone,
				StatusCode: 200,
				Reason:     "ok",
				Retryable:  false,
			}
		}

		return Result{
			Status:     store.StatusFailed,
			Category:   store.ErrTargetOther,
			StatusCode: 200,
			Reason:     "target response missing verified Gemini origin and application markers (possible interception / false positive)",
			Retryable:  false,
		}

	default:
		return Result{
			Status:     store.StatusFailed,
			Category:   store.ErrTargetOther,
			StatusCode: statusCode,
			Reason:     fmt.Sprintf("unexpected HTTP status %d", statusCode),
			Retryable:  false,
		}
	}
}
