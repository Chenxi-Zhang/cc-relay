package proxy

import (
	"fmt"
	"net/http"
	"time"

	"github.com/rs/zerolog"

	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/providers"
)

// UpdateKeyPoolFromResponse updates key pool state from an HTTP response.
// Handles 429 (rate limit), 403 (quota exhausted), and provider-specific errors.
// Returns true if the circuit breaker should skip counting this as a provider failure
// (e.g., permanent key errors that don't indicate provider health issues).
func UpdateKeyPoolFromResponse(
	resp *http.Response,
	pool *keypool.KeyPool,
	provider providers.Provider,
	cooldownStrategy string,
) bool {
	keyID, ok := resp.Request.Context().Value(keyIDContextKey).(string)
	if !ok || keyID == "" {
		return false
	}

	logger := zerolog.Ctx(resp.Request.Context())

	if owner := provider.Owner(); owner == providers.AnthropicOwner ||
		owner == providers.BedrockOwner ||
		owner == providers.VertexOwner {
		if err := pool.UpdateKeyFromHeaders(keyID, resp.Header); err != nil {
			logger.Debug().Err(err).Msg("failed to update key from headers")
		}
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		resolver := providers.NewCooldownResolver(cooldownStrategy)
		decision := resolver.Resolve(resp)
		return applyCooldownDecision(decision, pool, keyID, logger)
	}

	if resp.StatusCode == http.StatusForbidden {
		cooldown := 5 * time.Hour
		pool.MarkKeyExhausted(keyID, cooldown)
		logger.Warn().
			Str("key_id", keyID).
			Dur("cooldown", cooldown).
			Msg("key returned 403 Forbidden (quota exhausted), marking cooldown")
		return true
	}

	if resp.StatusCode == http.StatusBadRequest && cooldownStrategy == "zai" {
		resolver := providers.NewCooldownResolver(cooldownStrategy)
		decision := resolver.Resolve(resp)
		if decision.Cooldown > 0 {
			return applyCooldownDecision(decision, pool, keyID, logger)
		}
	}

	return false
}

func applyCooldownDecision(
	decision providers.CooldownDecision,
	pool *keypool.KeyPool,
	keyID string,
	logger *zerolog.Logger,
) bool {
	if decision.IsPermanent {
		pool.MarkKeyUnhealthy(keyID, fmt.Errorf("permanent key error, marking unhealthy"))
		logger.Warn().
			Str("key_id", keyID).
			Msg("permanent error, marking key unhealthy")
		return decision.SkipCB
	}

	if decision.Cooldown > 0 {
		pool.MarkKeyExhausted(keyID, decision.Cooldown)
		logger.Warn().
			Str("key_id", keyID).
			Dur("cooldown", decision.Cooldown).
			Msg("key hit rate limit, marking cooldown")
	}
	return decision.SkipCB
}
