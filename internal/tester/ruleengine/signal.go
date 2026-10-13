package ruleengine

import (
	"fmt"
	"strings"

	"gemsub/internal/tester/textutil"
)

// SignalKind represents the type of observable signal extracted from an EvaluationContext.
type SignalKind string

const (
	SignalHTTPStatus     SignalKind = "http_status"
	SignalHeader         SignalKind = "header"
	SignalCookieDomain   SignalKind = "cookie_domain"
	SignalTitle          SignalKind = "title"
	SignalNormalizedBody SignalKind = "normalized_body"
	SignalRawBody        SignalKind = "raw_body"
	SignalFinalURLPath   SignalKind = "final_url_path"
	SignalCanonicalLink  SignalKind = "canonical_link"
)

// Signal extracts an observable fact from an EvaluationContext.
type Signal interface {
	Kind() SignalKind
	Extract(ctx *EvaluationContext) any
}

// HTTPStatusSignal extracts the integer HTTP status code.
func HTTPStatusSignal() Signal {
	return httpStatusSignal{}
}

type httpStatusSignal struct{}

func (httpStatusSignal) Kind() SignalKind { return SignalHTTPStatus }
func (httpStatusSignal) Extract(ctx *EvaluationContext) any {
	if ctx == nil {
		return 0
	}
	return ctx.StatusCode
}

// HeaderSignal extracts a named HTTP header value, normalized with textutil.NormalizeText.
// Header name lookup is case-insensitive per HTTP specifications.
func HeaderSignal(name string) Signal {
	return headerSignal{name: name}
}

type headerSignal struct {
	name string
}

func (s headerSignal) Kind() SignalKind   { return SignalHeader }
func (s headerSignal) HeaderName() string { return s.name }
func (s headerSignal) Extract(ctx *EvaluationContext) any {
	if ctx == nil || ctx.Headers == nil {
		return ""
	}
	val := ctx.Headers.Get(s.name)
	if val == "" {
		return ""
	}
	return textutil.NormalizeText(val)
}

// CookieDomainSignal extracts cookie domains as a normalized lower-case string slice.
func CookieDomainSignal() Signal {
	return cookieDomainSignal{}
}

type cookieDomainSignal struct{}

func (cookieDomainSignal) Kind() SignalKind { return SignalCookieDomain }
func (cookieDomainSignal) Extract(ctx *EvaluationContext) any {
	if ctx == nil || len(ctx.Cookies) == 0 {
		return []string(nil)
	}
	domains := make([]string, 0, len(ctx.Cookies))
	for _, c := range ctx.Cookies {
		if c != nil && c.Domain != "" {
			domains = append(domains, strings.ToLower(c.Domain))
		}
	}
	return domains
}

// TitleSignal extracts the HTML document title, normalized to lower-case.
func TitleSignal() Signal {
	return titleSignal{}
}

type titleSignal struct{}

func (titleSignal) Kind() SignalKind { return SignalTitle }
func (titleSignal) Extract(ctx *EvaluationContext) any {
	if ctx == nil || ctx.Title == "" {
		return ""
	}
	return textutil.NormalizeText(ctx.Title)
}

// NormalizedBodySignal extracts the normalized body text (lower-cased, whitespace-collapsed).
func NormalizedBodySignal() Signal {
	return normalizedBodySignal{}
}

type normalizedBodySignal struct{}

func (normalizedBodySignal) Kind() SignalKind { return SignalNormalizedBody }
func (normalizedBodySignal) Extract(ctx *EvaluationContext) any {
	if ctx == nil {
		return ""
	}
	return ctx.NormalizedBody
}

// RawBodySignal extracts the raw response body bytes preserving original casing and bytes.
func RawBodySignal() Signal {
	return rawBodySignal{}
}

type rawBodySignal struct{}

func (rawBodySignal) Kind() SignalKind { return SignalRawBody }
func (rawBodySignal) Extract(ctx *EvaluationContext) any {
	if ctx == nil {
		return []byte(nil)
	}
	return ctx.RawBody
}

// FinalURLPathSignal extracts the URL path component (lower-cased) of the final redirected URL.
// It explicitly excludes scheme, host, and query string.
func FinalURLPathSignal() Signal {
	return finalURLPathSignal{}
}

type finalURLPathSignal struct{}

func (finalURLPathSignal) Kind() SignalKind { return SignalFinalURLPath }
func (finalURLPathSignal) Extract(ctx *EvaluationContext) any {
	if ctx == nil || ctx.FinalURL == nil {
		return ""
	}
	return strings.ToLower(ctx.FinalURL.Path)
}

// CanonicalLinkSignal extracts the canonical link href (lower-cased and normalized).
func CanonicalLinkSignal() Signal {
	return canonicalLinkSignal{}
}

type canonicalLinkSignal struct{}

func (canonicalLinkSignal) Kind() SignalKind { return SignalCanonicalLink }
func (canonicalLinkSignal) Extract(ctx *EvaluationContext) any {
	if ctx == nil || ctx.CanonicalLink == "" {
		return ""
	}
	return textutil.NormalizeText(ctx.CanonicalLink)
}

// ParseSignal constructs a Signal given its kind name and optional header parameter.
func ParseSignal(kind SignalKind, headerName string) (Signal, error) {
	switch kind {
	case SignalHTTPStatus:
		return HTTPStatusSignal(), nil
	case SignalHeader:
		if strings.TrimSpace(headerName) == "" {
			return nil, fmt.Errorf("signal %q requires non-empty header parameter", kind)
		}
		return HeaderSignal(headerName), nil
	case SignalCookieDomain:
		return CookieDomainSignal(), nil
	case SignalTitle:
		return TitleSignal(), nil
	case SignalNormalizedBody:
		return NormalizedBodySignal(), nil
	case SignalRawBody:
		return RawBodySignal(), nil
	case SignalFinalURLPath:
		return FinalURLPathSignal(), nil
	case SignalCanonicalLink:
		return CanonicalLinkSignal(), nil
	default:
		return nil, fmt.Errorf("unknown signal kind %q", kind)
	}
}
