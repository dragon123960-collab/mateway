package llm

import (
	"net/http"
	"strings"
)

func LooksLikeQuotaExceeded(err error) bool {
	return hasAnyMarker(err, []string{
		"usage allocated quota exceeded",
		"quota exceeded",
		"insufficient_quota",
		"quota has been exhausted",
	})
}

func LooksLikeProviderRateLimited(err error) bool {
	return hasAnyMarker(err, []string{
		"429 too many requests",
		"rate limit",
		"rate_limit",
		"request limit",
		"throttl",
	})
}

func LooksLikeLocalCooldown(err error) bool {
	return hasAnyMarker(err, []string{
		"llm cooling down until",
	})
}

func LooksLikeRetryableProviderError(err error) bool {
	return hasAnyMarker(err, []string{
		" llm http 502",
		" llm http 503",
		" llm http 504",
		" llm http 408",
		"context deadline exceeded",
		"i/o timeout",
		"connection reset by peer",
		"temporary failure",
	})
}

func ShouldFallbackModel(err error) bool {
	return LooksLikeQuotaExceeded(err) ||
		LooksLikeProviderRateLimited(err) ||
		LooksLikeLocalCooldown(err) ||
		LooksLikeRetryableProviderError(err)
}

func IsRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		http.StatusRequestTimeout:
		return true
	default:
		return false
	}
}

func hasAnyMarker(err error, markers []string) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, marker := range markers {
		if strings.Contains(msg, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}
