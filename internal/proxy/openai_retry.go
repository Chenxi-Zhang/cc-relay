package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/router"
)

// selectProviderExcludingOpenAI selects a provider for OpenAI retry,
// excluding previously-failed providers. Mirrors the Anthropic version
// without thinking affinity (not applicable to OpenAI format).
func (h *OpenAIHandler) selectProviderExcludingOpenAI(
	ctx context.Context, model string, rc *retryContext,
) (router.ProviderInfo, func(), error) {
	candidates := h.providers()
	if len(candidates) == 0 {
		return router.ProviderInfo{}, nil, fmt.Errorf("no openai providers available")
	}

	// Filter out providers whose keypool has no available keys.
	filtered := make([]router.ProviderInfo, 0, len(candidates))
	for _, c := range candidates {
		if h.hasAvailableKeys(c.Provider.Name()) {
			filtered = append(filtered, c)
		}
	}
	candidates = filtered

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

// selectKeyFromPoolExcludingOpenAI selects a key from the pool, skipping excluded keys.
func (h *OpenAIHandler) selectKeyFromPoolExcludingOpenAI(
	request *http.Request, pool *keypool.KeyPool, providerName string, rc *retryContext,
) (keyID, apiKey string, updatedReq *http.Request, ok bool) {
	if pool == nil {
		return "", "", request, false
	}

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

// prepareOpenAIAttempt builds a request for one retry attempt: resets the body,
// strips the route prefix, sets auth headers, applies model mapping, and
// attaches provider/key context values.
func (h *OpenAIHandler) prepareOpenAIAttempt(
	request *http.Request,
	bodyBytes []byte,
	prov router.ProviderInfo,
	keyID, selectedKey string,
	responsesURL *url.URL,
) *http.Request {
	// Restore body for this attempt (ReverseProxy consumes it).
	r := request.Clone(request.Context())
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	r.ContentLength = int64(len(bodyBytes))

	// Strip the /openai/v1 route prefix.
	r.URL.Path = trimOpenAIPrefix(r.URL.Path)

	// Set auth key for ProviderProxy.setAuth.
	r.Header.Set("X-Selected-Key", selectedKey)

	// Context values for modifyResponse.
	ctx := context.WithValue(r.Context(), keyIDContextKey, keyID)
	ctx = context.WithValue(ctx, providerNameContextKey, prov.Provider.Name())
	if responsesURL != nil {
		ctx = context.WithValue(ctx, responsesURLContextKey{}, responsesURL)
	}
	r = r.WithContext(ctx)

	// Apply model mapping for this provider.
	if mapping := prov.Provider.GetModelMapping(); len(mapping) > 0 {
		rewriter := NewModelRewriter(mapping)
		_ = rewriter.RewriteRequest(r, nil)
	}

	return r
}

func trimOpenAIPrefix(path string) string {
	p := strings.TrimPrefix(path, "/openai/v1")
	if p == "" {
		return "/"
	}
	return p
}

// resolveOpenAIResponsesURL looks up the responses_url for the selected provider.
func (h *OpenAIHandler) resolveOpenAIResponsesURL(providerName string) *url.URL {
	if !h.responsesAPI || h.configProvider == nil {
		return nil
	}
	cfg := h.configProvider.Get()
	if cfg == nil {
		return nil
	}
	for _, pc := range cfg.OpenAIProviders {
		if pc.Name == providerName && pc.ResponsesURL != "" {
			u, err := url.Parse(pc.ResponsesURL)
			if err == nil && u.Host != "" && (u.Scheme == "http" || u.Scheme == "https") {
				return u
			}
		}
	}
	return nil
}

// serveOpenAIWithRetry implements three-level 429 retry for non-streaming
// OpenAI requests (same-key → different-key → different-provider).
// Uses httptest.ResponseRecorder to buffer each attempt so that only the
// final response is flushed to the real writer.
func (h *OpenAIHandler) serveOpenAIWithRetry(
	w http.ResponseWriter, r *http.Request,
	bodyBytes []byte, model string, retryCfg config.RetryConfig,
) {
	rc := newRetryContext()
	var lastRecorder *httptest.ResponseRecorder

	for providerAttempt := 0; providerAttempt <= retryCfg.GetProviderRetries(); providerAttempt++ {
		selected, release, err := h.selectProviderExcludingOpenAI(
			r.Context(), model, rc,
		)
		if err != nil {
			if lastRecorder != nil {
				overrideRetryAfter(lastRecorder.Header())
				flushRecorder(lastRecorder, w)
				return
			}
			writeOpenAIError(w, http.StatusServiceUnavailable,
				fmt.Sprintf("failed to select provider: %v", err),
				"server_error", "no_providers")
			return
		}
		if release != nil {
			defer release()
		}

		prov := selected.Provider
		providerName := prov.Name()

		pp, proxyErr := h.getOrCreateOpenAIProxy(prov)
		if proxyErr != nil {
			if lastRecorder != nil {
				overrideRetryAfter(lastRecorder.Header())
				flushRecorder(lastRecorder, w)
				return
			}
			writeOpenAIError(w, http.StatusInternalServerError,
				fmt.Sprintf("failed to get proxy: %v", proxyErr),
				"internal_error", "")
			return
		}

		responsesURL := h.resolveOpenAIResponsesURL(providerName)

		// --- Key retry loop ---
		for keyAttempt := 0; keyAttempt <= retryCfg.GetKeyRetries(); keyAttempt++ {
			var keyID, selectedKey string
			var keyOK bool

			if pp.KeyPool != nil {
				keyID, selectedKey, _, keyOK = h.selectKeyFromPoolExcludingOpenAI(
					r, pp.KeyPool, providerName, rc,
				)
				if !keyOK {
					break
				}
			} else {
				keyID = "default"
				selectedKey = pp.APIKey
				keyOK = true
			}

			// --- Same-key retry loop ---
			for sameKeyAttempt := 0; sameKeyAttempt <= retryCfg.GetSameKeyRetries(); sameKeyAttempt++ {
				if !rc.canRetry(retryCfg) {
					break
				}

				if sameKeyAttempt > 0 {
					backoff := rc.sameKeyBackoff(retryCfg)
					time.Sleep(backoff)
				}

				logger := zerolog.Ctx(r.Context()).With().
					Str("provider", providerName).
					Str("key_id", keyID).
					Logger()
				attemptReq := h.prepareOpenAIAttempt(
					r, bodyBytes, selected, keyID, selectedKey, responsesURL,
				)
				attemptReq = attemptReq.WithContext(logger.WithContext(attemptReq.Context()))

				recorder := httptest.NewRecorder()
				SetProviderNameOnWriter(recorder, providerName)
				setOpenAIRelayHeaders(recorder, pp.KeyPool, keyID)
				rc.recordAttempt(providerName, keyID)

				pp.Proxy.ServeHTTP(recorder, attemptReq)
				lastRecorder = recorder

				// Non-retryable status → success or non-retryable error, flush immediately.
				if !isRetryableStatusCode(recorder.Code) {
					flushRecorder(recorder, w)
					return
				}

				// --- Retryable status received (429/403) ---
				// modifyResponse already ran during ServeHTTP, which called
				// UpdateKeyPoolFromResponse → MarkKeyExhausted with cooldown.
				rc.lastRetryAfter = parseRetryAfter(recorder.Header())

				// 403 never benefits from same-key retry — quota is exhausted.
				if recorder.Code == http.StatusForbidden {
					logRetryAttempt(&logger, rc.attemptCount, providerName, keyID,
						recorder.Code, "L1_skip", "403 Forbidden, escalating to next key")
					break
				}

				if !rc.isTransient429(prov) {
					logRetryAttempt(&logger, rc.attemptCount, providerName, keyID,
						recorder.Code, "L1_skip", "non-transient 429, escalating to next key")
					break
				}

				logRetryAttempt(&logger, rc.attemptCount, providerName, keyID,
					recorder.Code, "L1", "transient 429, same-key retry")
			}

			rc.excludeKey(providerName, keyID)

			if !rc.canRetry(retryCfg) {
				break
			}
		}

		rc.excludeProvider(providerName)

		if !rc.canRetry(retryCfg) {
			break
		}

		if providerAttempt < retryCfg.GetProviderRetries() {
			time.Sleep(retryCfg.GetProviderBackoff())
		}
	}

	// All retries exhausted — return last error to client.
	if lastRecorder != nil {
		logger := zerolog.Ctx(r.Context())
		logRetryExhausted(logger, rc.attemptCount, rc.triedProviders, rc.triedKeys)
		overrideRetryAfter(lastRecorder.Header())
		flushRecorder(lastRecorder, w)
		return
	}

	writeOpenAIError(w, http.StatusServiceUnavailable,
		"no providers available for retry", "server_error", "no_providers")
}

// serveOpenAIStreamingWithRetry implements three-level 429 retry for
// streaming OpenAI requests. Uses peekWriter to buffer only 429 responses;
// successful 200 SSE responses are committed immediately to the real writer.
func (h *OpenAIHandler) serveOpenAIStreamingWithRetry(
	w http.ResponseWriter, r *http.Request,
	bodyBytes []byte, model string, retryCfg config.RetryConfig,
) {
	rc := newRetryContext()
	var lastPeek *peekWriter

	for providerAttempt := 0; providerAttempt <= retryCfg.GetProviderRetries(); providerAttempt++ {
		selected, release, err := h.selectProviderExcludingOpenAI(
			r.Context(), model, rc,
		)
		if err != nil {
			if lastPeek != nil {
				overrideRetryAfter(lastPeek.Header())
				lastPeek.FlushBuffered429()
				return
			}
			writeOpenAIError(w, http.StatusServiceUnavailable,
				fmt.Sprintf("failed to select provider: %v", err),
				"server_error", "no_providers")
			return
		}
		if release != nil {
			defer release()
		}

		prov := selected.Provider
		providerName := prov.Name()

		pp, proxyErr := h.getOrCreateOpenAIProxy(prov)
		if proxyErr != nil {
			if lastPeek != nil {
				overrideRetryAfter(lastPeek.Header())
				lastPeek.FlushBuffered429()
				return
			}
			writeOpenAIError(w, http.StatusInternalServerError,
				fmt.Sprintf("failed to get proxy: %v", proxyErr),
				"internal_error", "")
			return
		}

		responsesURL := h.resolveOpenAIResponsesURL(providerName)

		SetProviderNameOnWriter(w, providerName)

		for keyAttempt := 0; keyAttempt <= retryCfg.GetKeyRetries(); keyAttempt++ {
			var keyID, selectedKey string
			var keyOK bool

			if pp.KeyPool != nil {
				keyID, selectedKey, _, keyOK = h.selectKeyFromPoolExcludingOpenAI(
					r, pp.KeyPool, providerName, rc,
				)
				if !keyOK {
					break
				}
			} else {
				keyID = "default"
				selectedKey = pp.APIKey
				keyOK = true
			}

			for sameKeyAttempt := 0; sameKeyAttempt <= retryCfg.GetSameKeyRetries(); sameKeyAttempt++ {
				if !rc.canRetry(retryCfg) {
					break
				}

				if sameKeyAttempt > 0 {
					backoff := rc.sameKeyBackoff(retryCfg)
					time.Sleep(backoff)
				}

				logger := zerolog.Ctx(r.Context()).With().
					Str("provider", providerName).
					Str("key_id", keyID).
					Logger()
				attemptReq := h.prepareOpenAIAttempt(
					r, bodyBytes, selected, keyID, selectedKey, responsesURL,
				)
				attemptReq = attemptReq.WithContext(logger.WithContext(attemptReq.Context()))

				pw := newPeekWriter(w)
				rc.recordAttempt(providerName, keyID)

				pp.Proxy.ServeHTTP(pw, attemptReq)

				// 200 streaming response already committed to real writer — return immediately.
				if pw.IsCommitted() {
					return
				}

				// Non-retryable, non-streaming (e.g., 400, 500) — commit buffered response.
				if !pw.Is429() {
					pw.FlushBuffered429()
					return
				}

				// --- Retryable status received (429/403, still buffered) ---
				lastPeek = pw
				rc.lastRetryAfter = parseRetryAfter(pw.Header())

				if pw.Status() == http.StatusForbidden {
					logRetryAttempt(&logger, rc.attemptCount, providerName, keyID,
						pw.Status(), "L1_skip", "403 Forbidden, escalating to next key")
					break
				}

				if !rc.isTransient429(prov) {
					logRetryAttempt(&logger, rc.attemptCount, providerName, keyID,
						pw.Status(), "L1_skip", "non-transient 429, escalating to next key")
					break
				}

				logRetryAttempt(&logger, rc.attemptCount, providerName, keyID,
					pw.Status(), "L1", "transient 429, streaming same-key retry")
			}

			rc.excludeKey(providerName, keyID)

			if !rc.canRetry(retryCfg) {
				break
			}
		}

		rc.excludeProvider(providerName)

		if !rc.canRetry(retryCfg) {
			break
		}

		if providerAttempt < retryCfg.GetProviderRetries() {
			time.Sleep(retryCfg.GetProviderBackoff())
		}
	}

	// All retries exhausted — return last error to client.
	if lastPeek != nil {
		logger := zerolog.Ctx(r.Context())
		logRetryExhausted(logger, rc.attemptCount, rc.triedProviders, rc.triedKeys)
		overrideRetryAfter(lastPeek.Header())
		lastPeek.FlushBuffered429()
		return
	}

	writeOpenAIError(w, http.StatusServiceUnavailable,
		"no providers available for retry", "server_error", "no_providers")
}

// setOpenAIRelayHeaders sets relay metadata headers on the response writer.
func setOpenAIRelayHeaders(w http.ResponseWriter, pool *keypool.KeyPool, keyID string) {
	w.Header().Set(HeaderRelayKeyID, keyID)
	if pool != nil {
		stats := pool.GetStats()
		w.Header().Set(HeaderRelayKeysTotal, fmt.Sprintf("%d", stats.TotalKeys))
		w.Header().Set(HeaderRelayKeysAvail, fmt.Sprintf("%d", stats.AvailableKeys))
	}
}
