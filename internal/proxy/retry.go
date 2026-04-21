package proxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/router"
)

// ClientRetryAfterSec is the Retry-After value returned to clients when all
// retry attempts are exhausted. cc-relay has multiple keys and providers, so
// even if one request exhausted all options, the next request may find
// available capacity. A short Retry-After lets the client retry quickly while
// cc-relay handles key cooldown internally.
const ClientRetryAfterSec = 2

// overrideRetryAfter replaces the Retry-After header in hdr with a short value
// so the client retries quickly instead of waiting for a single key's cooldown.
func overrideRetryAfter(hdr http.Header) {
	hdr.Set("Retry-After", strconv.Itoa(ClientRetryAfterSec))
}

// retryContext tracks state across retry attempts within a single request.
type retryContext struct {
	excludedProviders map[string]bool
	excludedKeys      map[string]bool // key: "providerName:keyID"
	attemptCount      int
	lastRetryAfter    time.Duration
	lastZAIInfo       *providers.ZAIErrorInfo
	lastProviderName  string
	lastKeyID         string
	triedProviders    []string
	triedKeys         []string
}

func newRetryContext() *retryContext {
	return &retryContext{
		excludedProviders: make(map[string]bool),
		excludedKeys:      make(map[string]bool),
		triedProviders:    make([]string, 0),
		triedKeys:         make([]string, 0),
	}
}

func (rc *retryContext) canRetry(cfg config.RetryConfig) bool {
	return rc.attemptCount < cfg.GetMaxTotalAttempts()
}

func (rc *retryContext) recordAttempt(providerName, keyID string) {
	rc.attemptCount++
	rc.lastProviderName = providerName
	rc.lastKeyID = keyID

	found := false
	for _, p := range rc.triedProviders {
		if p == providerName {
			found = true
			break
		}
	}
	if !found {
		rc.triedProviders = append(rc.triedProviders, providerName)
	}
	rc.triedKeys = append(rc.triedKeys, providerName+":"+keyID)
}

func (rc *retryContext) excludeProvider(name string) {
	rc.excludedProviders[name] = true
}

func (rc *retryContext) excludeKey(providerName, keyID string) {
	rc.excludedKeys[providerName+":"+keyID] = true
}

func (rc *retryContext) isKeyExcluded(providerName, keyID string) bool {
	return rc.excludedKeys[providerName+":"+keyID]
}

// sameKeyBackoff returns min(configured backoff, Retry-After header, 5s cap).
func (rc *retryContext) sameKeyBackoff(cfg config.RetryConfig) time.Duration {
	backoff := cfg.GetSameKeyBackoff()
	const cap = 5 * time.Second
	if rc.lastRetryAfter > 0 && rc.lastRetryAfter < backoff {
		backoff = rc.lastRetryAfter
	}
	if backoff > cap {
		backoff = cap
	}
	return backoff
}

// isTransient429 checks whether the most recent 429 was a transient error
// eligible for same-key retry. For ZAI providers only Transient category
// (1302/1303/1305/1312) qualifies. For non-ZAI providers all 429s are
// treated as transient (same-key retry might help).
func (rc *retryContext) isTransient429(prov providers.Provider) bool {
	if rc.lastZAIInfo != nil {
		return rc.lastZAIInfo.Category == providers.ZAICatTransient
	}
	return true
}

// flushRecorder copies the buffered response from a httptest.ResponseRecorder
// to the real http.ResponseWriter.
func flushRecorder(recorder *httptest.ResponseRecorder, writer http.ResponseWriter) {
	dst := writer.Header()
	for k, vv := range recorder.Header() {
		dst[k] = vv
	}
	writer.WriteHeader(recorder.Code)
	recorder.Body.WriteTo(writer)
}

// isStreamingRequest detects SSE/streaming requests that cannot be buffered for retry.
func isStreamingRequest(r *http.Request) bool {
	if r.Header.Get("Accept") == "text/event-stream" {
		return true
	}
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			bodyStr := string(body)
			r.Body = io.NopCloser(strings.NewReader(bodyStr))
			if containsStreamTrue(bodyStr) {
				return true
			}
		}
	}
	return false
}

// containsStreamTrue does a simple string scan for "stream":true in JSON body.
func containsStreamTrue(body string) bool {
	idx := strings.Index(body, `"stream"`)
	if idx < 0 {
		return false
	}
	rest := body[idx+8:]
	window := rest
	if len(window) > 20 {
		window = window[:20]
	}
	colonIdx := strings.Index(window, ":")
	if colonIdx < 0 {
		return false
	}
	afterColon := strings.TrimLeft(rest[colonIdx+1:], " \t\n\r")
	return strings.HasPrefix(afterColon, "true")
}

// selectProviderExcluding selects a provider, excluding previously-failed ones.
func (h *Handler) selectProviderExcluding(
	ctx context.Context, model string, hasThinking bool, rc *retryContext,
) (router.ProviderInfo, func(), error) {
	candidates, hasCandidates := h.providerCandidates()
	if !hasCandidates {
		return h.defaultProviderInfo(), nil, nil
	}

	candidates, hasCandidates = h.applyModelRouting(candidates, model)
	if !hasCandidates {
		return h.defaultProviderInfo(), nil, nil
	}

	candidates = h.applyThinkingAffinity(candidates, hasThinking)

	if len(rc.excludedProviders) > 0 {
		filtered := make([]router.ProviderInfo, 0, len(candidates))
		for _, c := range candidates {
			if !rc.excludedProviders[c.Provider.Name()] {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}

	if len(candidates) == 0 {
		return router.ProviderInfo{}, nil, fmt.Errorf("all providers excluded after retry")
	}

	selected, err := h.router.Select(ctx, candidates)
	if err != nil {
		return router.ProviderInfo{}, nil, err
	}

	if tracker, ok := h.router.(router.ProviderLoadTracker); ok {
		tracker.Acquire(selected.Provider)
		return selected, func() { tracker.Release(selected.Provider) }, nil
	}
	return selected, nil, nil
}

// selectKeyFromPoolExcluding selects a key from the pool, skipping excluded keys.
func (h *Handler) selectKeyFromPoolExcluding(
	request *http.Request, pool *keypool.KeyPool, providerName string, rc *retryContext,
) (keyID, apiKey string, updatedReq *http.Request, ok bool) {
	if pool == nil {
		return "", "", request, false
	}

	// MarkKeyExhausted (called by modifyResponse) makes GetKey skip exhausted keys.
	// We also check our explicit exclusion list for safety.
	for attempt := 0; attempt < 3; attempt++ {
		kid, key, err := pool.GetKey(request.Context())
		if err != nil {
			return "", "", request, false
		}
		if !rc.isKeyExcluded(providerName, kid) {
			updatedReq := request.WithContext(context.WithValue(request.Context(), keyIDContextKey, kid))
			return kid, key, updatedReq, true
		}
	}

	return "", "", request, false
}

func logRetryAttempt(
	logger *zerolog.Logger, attempt int,
	providerName, keyID string, statusCode int, level, reason string,
) {
	logger.Warn().
		Int("attempt", attempt).
		Str("provider", providerName).
		Str("key_id", keyID).
		Int("status_code", statusCode).
		Str("retry_level", level).
		Str("reason", reason).
		Msg("retry attempt")
}

func logRetryExhausted(
	logger *zerolog.Logger, totalAttempts int, providerNames, keyIDs []string,
) {
	logger.Warn().
		Int("total_attempts", totalAttempts).
		Strs("providers", providerNames).
		Strs("keys", keyIDs).
		Msg("retry exhausted, returning last 429 to client")
}

func logRetrySuccess(
	logger *zerolog.Logger, attempt int,
	providerName, keyID string, statusCode int,
) {
	logger.Info().
		Int("attempt", attempt).
		Str("provider", providerName).
		Str("key_id", keyID).
		Int("status_code", statusCode).
		Msg("retry succeeded")
}
