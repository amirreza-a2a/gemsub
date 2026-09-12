package tester

import (
	"context"
	"errors"
	"strings"
	"time"

	"gemsub/internal/store"
	"gemsub/internal/tester/textutil"
	"gemsub/internal/tester/transport"
)

// NormalizeText delegates to textutil.NormalizeText for backward compatibility.
// This wrapper allows existing callers in the tester package and external test
// code to continue using tester.NormalizeText without modification.
func NormalizeText(s string) string { return textutil.NormalizeText(s) }

// NormalizeBytes delegates to textutil.NormalizeBytes for backward compatibility.
func NormalizeBytes(b []byte) string { return textutil.NormalizeBytes(b) }

// ClassificationResult represents the structured outcome of evaluating a probe.
type ClassificationResult struct {
	Status                 store.Status
	Category               store.ErrorCategory
	StatusCode             int
	Reason                 string
	Retryable              bool
	TransportOK            bool
	TransportLatency       time.Duration
	TransportEvidenceKnown bool
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
	case transport.IsConnectionRefused(err):
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
