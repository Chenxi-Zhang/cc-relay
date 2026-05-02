package proxy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/providers"
)

// UpdateKeyPoolFromResponse updates key pool state from an HTTP response.
// Handles 429 (rate limit), 403 (quota exhausted), and ZAI-specific errors.
// Returns true if the circuit breaker should skip counting this as a provider failure
// (e.g., permanent key errors that don't indicate provider health issues).
func UpdateKeyPoolFromResponse(
	resp *http.Response,
	pool *keypool.KeyPool,
	provider providers.Provider,
) bool {
	keyID, ok := resp.Request.Context().Value(keyIDContextKey).(string)
	if !ok || keyID == "" {
		return false
	}

	logger := zerolog.Ctx(resp.Request.Context())

	// Only parse Anthropic rate limit headers for providers that emit them.
	// Calling UpdateFromHeaders on non-Anthropic providers would trigger
	// SetLimit which creates a new rate.Limiter, effectively resetting the
	// token bucket and allowing previously exhausted keys to be reused.
	if owner := provider.Owner(); owner == providers.AnthropicOwner ||
		owner == providers.BedrockOwner ||
		owner == providers.VertexOwner {
		if err := pool.UpdateKeyFromHeaders(keyID, resp.Header); err != nil {
			logger.Debug().Err(err).Msg("failed to update key from headers")
		}
	}

	// Handle 429 from backend
	if resp.StatusCode == http.StatusTooManyRequests {
		// Use ZAI-specific 429 handling for Zhipu providers
		if providers.IsZAIProvider(provider.Owner()) {
			return handleZAI429(resp, pool, keyID, logger)
		}

		// Generic 429 handling for other providers
		retryAfter := parseRetryAfter(resp.Header)
		pool.MarkKeyExhausted(keyID, retryAfter)
		logger.Warn().
			Str("key_id", keyID).
			Dur("cooldown", retryAfter).
			Msg("key hit rate limit, marking cooldown")
	}

	// Handle 403 from backend — quota exhausted or auth failure
	if resp.StatusCode == http.StatusForbidden {
		cooldown := 5 * time.Hour
		pool.MarkKeyExhausted(keyID, cooldown)
		logger.Warn().
			Str("key_id", keyID).
			Dur("cooldown", cooldown).
			Msg("key returned 403 Forbidden (quota exhausted), marking cooldown")
		return true // skip circuit breaker — key issue, not provider issue
	}

	// Handle 400 from backend for ZAI provider
	if resp.StatusCode == http.StatusBadRequest && providers.IsZAIProvider(provider.Owner()) {
		return handleZAI400(resp, pool, keyID, logger)
	}

	return false
}

// handleZAI400 handles ZAI-specific 400 error responses.
// ZAI returns business error codes in the response body, same format as 429:
//
//	{"error":{"code":"1234","message":"..."}}
//
// For code 1234, the key is marked exhausted with a short cooldown (retryable).
// For all other 400 error codes, the response is passed through unchanged.
// Returns true if the error is a retryable 400 (circuit breaker should skip).
func handleZAI400(
	resp *http.Response, pool *keypool.KeyPool, keyID string, logger *zerolog.Logger,
) bool {
	// Read the response body to parse the ZAI error code.
	info, bodyBytes, err := providers.ParseZAI429Error(resp.Body)
	if err != nil {
		logger.Warn().Err(err).Msg("failed to read ZAI 400 response body")
		return false
	}

	// Restore the body so the proxy can still send it to the client
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	resp.ContentLength = int64(len(bodyBytes))

	if info == nil || !providers.IsZAIRetryable400Code(info.Code) {
		// Not a retryable 400 — pass through unchanged
		return false
	}

	// Code 1234: retryable — mark key exhausted with short cooldown
	cooldown := 30 * time.Second
	pool.MarkKeyExhausted(keyID, cooldown)
	logger.Warn().
		Str("key_id", keyID).
		Str("zai_error_code", info.Code).
		Str("zai_error_msg", info.Message).
		Dur("cooldown", cooldown).
		Msg("ZAI retryable 400 error, marking key cooldown")

	return true // Skip circuit breaker — this is a transient issue
}

// handleZAI429 handles ZAI-specific 429 error responses with fine-grained error code parsing.
// Returns true if the error is a permanent key issue (circuit breaker should skip).
//
// ZAI returns business error codes in the response body:
//
//	{"error":{"code":"1302","message":"Rate limit reached for requests"}}
//
// Different codes require different handling:
//   - Transient (1302, 1303, 1305, 1312): short cooldown
//   - Quota exhausted (1304, 1308, 1310): longer cooldown based on reset time
//   - Permanent (1309, 1311, 1313, 1112, 1113, 1121): mark key unhealthy
func handleZAI429(
	resp *http.Response, pool *keypool.KeyPool, keyID string, logger *zerolog.Logger,
) bool {
	// Read the response body to parse the ZAI error code.
	// 429 responses are complete JSON, not streaming, so this is safe.
	info, bodyBytes, err := providers.ParseZAI429Error(resp.Body)
	if err != nil {
		logger.Warn().Err(err).Msg("failed to read ZAI 429 response body")
		retryAfter := parseRetryAfter(resp.Header)
		pool.MarkKeyExhausted(keyID, retryAfter)
		return false
	}

	// Restore the body so the proxy can still send it to the client
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	resp.ContentLength = int64(len(bodyBytes))

	if info == nil {
		// Body is not a recognizable ZAI error format — use generic cooldown
		retryAfter := parseRetryAfter(resp.Header)
		pool.MarkKeyExhausted(keyID, retryAfter)
		logger.Warn().
			Str("key_id", keyID).
			Dur("cooldown", retryAfter).
			Msg("ZAI 429 with unparseable body, using generic cooldown")
		return false
	}

	// Log the specific ZAI error with full details
	event := logger.Warn().
		Str("key_id", keyID).
		Str("zai_error_code", info.Code).
		Str("zai_error_msg", info.Message).
		Str("category", info.Category.String())

	switch info.Category {
	case providers.ZAICatTransient:
		pool.MarkKeyExhausted(keyID, info.Cooldown)
		event.
			Dur("cooldown", info.Cooldown).
			Msg("ZAI transient rate limit, marking key cooldown")

	case providers.ZAICatQuotaExhausted:
		pool.MarkKeyExhausted(keyID, info.Cooldown)
		event.
			Dur("cooldown", info.Cooldown).
			Msg("ZAI quota exhausted, marking key long cooldown")

	case providers.ZAICatPermanent:
		pool.MarkKeyUnhealthy(keyID, fmt.Errorf("ZAI permanent error [%s]: %s", info.Code, info.Message))
		event.Msg("ZAI permanent error, marking key unhealthy")
		return true // Skip circuit breaker — provider is fine, key is the problem
	}

	return false
}
