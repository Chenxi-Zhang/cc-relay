package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/rs/zerolog"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/router"
)

// OpenAIHandlerOptions configures construction of an OpenAIHandler.
type OpenAIHandlerOptions struct {
	Router           router.ProviderRouter
	Providers        ProviderInfoFunc
	GetProviderPools KeyPoolsFunc
	GetProviderKeys  KeysFunc
	DebugOptions     config.DebugOptions
}

// OpenAIHandler handles OpenAI Chat Completions API requests.
// It is completely separate from the Anthropic Handler.
type OpenAIHandler struct {
	router           router.ProviderRouter
	providers        ProviderInfoFunc
	getProviderPools KeyPoolsFunc
	getProviderKeys  KeysFunc
	providerProxies  map[string]*ProviderProxy
	proxyMu          sync.RWMutex
	debugOpts        config.DebugOptions
}

// NewOpenAIHandler creates a new handler for OpenAI-format requests.
func NewOpenAIHandler(opts *OpenAIHandlerOptions) (*OpenAIHandler, error) {
	if opts == nil {
		return nil, errors.New("openai handler options are required")
	}
	if opts.Router == nil {
		return nil, errors.New("openai handler: router is required")
	}
	if opts.Providers == nil {
		return nil, errors.New("openai handler: providers function is required")
	}

	h := &OpenAIHandler{
		router:           opts.Router,
		providers:        opts.Providers,
		getProviderPools: opts.GetProviderPools,
		getProviderKeys:  opts.GetProviderKeys,
		providerProxies:  make(map[string]*ProviderProxy),
		debugOpts:        opts.DebugOptions,
	}

	// Pre-create proxies for initial provider set.
	if err := h.initProxies(); err != nil {
		return nil, err
	}

	return h, nil
}

// initProxies creates ProviderProxy instances for the initial provider set.
func (h *OpenAIHandler) initProxies() error {
	infos := h.providers()
	pools := h.resolvePools()
	keys := h.resolveKeys()

	for _, info := range infos {
		prov := info.Provider
		apiKey := ""
		if keys != nil {
			apiKey = keys[prov.Name()]
		}
		var pool *keypool.KeyPool
		if pools != nil {
			pool = pools[prov.Name()]
		}
		pp, err := NewProviderProxy(prov, apiKey, pool, h.debugOpts, h.modifyResponse)
		if err != nil {
			return fmt.Errorf("openai handler: create proxy for %s: %w", prov.Name(), err)
		}
		h.providerProxies[prov.Name()] = pp
	}
	return nil
}

func (h *OpenAIHandler) resolvePools() map[string]*keypool.KeyPool {
	if h.getProviderPools != nil {
		return h.getProviderPools()
	}
	return nil
}

func (h *OpenAIHandler) resolveKeys() map[string]string {
	if h.getProviderKeys != nil {
		return h.getProviderKeys()
	}
	return nil
}

// ServeHTTP implements http.Handler for OpenAI Chat Completions.
//
// Flow:
//  1. Read request body
//  2. Parse model from body
//  3. Detect streaming — set SSE headers if streaming
//  4. Select provider via router
//  5. Get key from keypool (or fallback key)
//  6. Apply model mapping
//  7. Proxy request via ProviderProxy (SSE passthrough for streaming)
//  8. Return response (passthrough)
func (h *OpenAIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logger := zerolog.Ctx(r.Context())

	// Step 1: Read request body.
	bodyBytes, err := io.ReadAll(r.Body)
	if closeErr := r.Body.Close(); closeErr != nil {
		logger.Warn().Err(closeErr).Msg("failed to close request body")
	}
	if err != nil {
		writeOpenAIError(w, http.StatusBadRequest,
			fmt.Sprintf("failed to read request body: %v", err),
			"invalid_request_error", "")
		return
	}

	// Step 2: Parse model from body.
	model := extractOpenAIModel(bodyBytes)

	// Step 3: Detect streaming.
	// SSE headers are NOT set here — the ProviderProxy's modifyResponse
	// handles them based on the backend response's Content-Type.
	// This avoids setting text/event-stream when the backend returns an error (e.g. 500 JSON).
	// No retry for streaming (v1 simplification).
	_ = OpenAIGetIsStreaming(bodyBytes)

	// Step 4: Select provider.
	infos := h.providers()
	if len(infos) == 0 {
		writeOpenAIError(w, http.StatusServiceUnavailable,
			"no openai providers available",
			"server_error", "no_providers")
		return
	}

	selected, err := h.router.Select(r.Context(), infos)
	if err != nil {
		writeOpenAIError(w, http.StatusServiceUnavailable,
			fmt.Sprintf("failed to select provider: %v", err),
			"server_error", "no_providers")
		return
	}

	prov := selected.Provider

	// Step 5: Get or create proxy for selected provider.
	pp, err := h.getOrCreateOpenAIProxy(prov)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError,
			fmt.Sprintf("failed to get proxy: %v", err),
			"internal_error", "")
		return
	}

	// Step 6: Select key.
	var keyID, selectedKey string
	if pp.KeyPool != nil {
		keyID, selectedKey, err = pp.KeyPool.GetKey(r.Context())
		if err != nil {
			if errors.Is(err, keypool.ErrAllKeysExhausted) {
				writeOpenAIError(w, http.StatusTooManyRequests,
					"all keys exhausted for provider",
					"rate_limit_error", "keys_exhausted")
				return
			}
			writeOpenAIError(w, http.StatusInternalServerError,
				fmt.Sprintf("failed to select key: %v", err),
				"internal_error", "")
			return
		}

		// Set relay metadata headers.
		w.Header().Set(HeaderRelayKeyID, keyID)
		stats := pp.KeyPool.GetStats()
		w.Header().Set(HeaderRelayKeysTotal, fmt.Sprintf("%d", stats.TotalKeys))
		w.Header().Set(HeaderRelayKeysAvail, fmt.Sprintf("%d", stats.AvailableKeys))
	} else {
		keyID = "default"
		selectedKey = pp.APIKey
	}

	// Restore body for proxy (it was consumed above).
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	r.ContentLength = int64(len(bodyBytes))

	// Strip the /openai/v1 route prefix so only the API endpoint path
	// (e.g. /chat/completions, /models) is joined with the provider base_url.
	// joinURLPath(base_url_path, incoming_path) concatenates both,
	// so /openai/v1 must be removed to avoid /api/paas/v4/openai/v1/chat/completions.
	r.URL.Path = strings.TrimPrefix(r.URL.Path, "/openai/v1")
	if r.URL.Path == "" {
		r.URL.Path = "/"
	}

	// Set the selected key so ProviderProxy.setAuth can pick it up.
	r.Header.Set("X-Selected-Key", selectedKey)

	// Store key ID in context for response modification.
	ctx := context.WithValue(r.Context(), keyIDContextKey, keyID)
	ctx = context.WithValue(ctx, providerNameContextKey, prov.Name())
	r = r.WithContext(ctx)

	// Step 7: Apply model mapping via ModelRewriter.
	if mapping := prov.GetModelMapping(); len(mapping) > 0 {
		rewriter := NewModelRewriter(mapping)
		if rewriteErr := rewriter.RewriteRequest(r, nil); rewriteErr != nil {
			logger.Warn().Err(rewriteErr).Msg("failed to rewrite model")
		}
	}

	logger.Debug().
		Str("provider", prov.Name()).
		Str("model", model).
		Str("key_id", keyID).
		Msg("openai handler: proxying request")

	// Step 8: Proxy request.
	pp.Proxy.ServeHTTP(w, r)
}

// modifyResponse is the response hook for OpenAI proxied requests.
// When debug logging is enabled, it peeks into the response body to extract
// the actual backend model name and logs it, matching the pattern used by
// the Anthropic handler's updateKeyPoolFromResponse.
func (h *OpenAIHandler) modifyResponse(resp *http.Response) error {
	logger := zerolog.Ctx(resp.Request.Context())
	peekAndLogOpenAIModel(resp, logger)
	return nil
}

// getOrCreateOpenAIProxy returns the proxy for a provider, creating it lazily if needed.
func (h *OpenAIHandler) getOrCreateOpenAIProxy(prov providers.Provider) (*ProviderProxy, error) {
	name := prov.Name()

	h.proxyMu.RLock()
	pp, exists := h.providerProxies[name]
	h.proxyMu.RUnlock()

	if exists && pp.Provider.Name() == name && pp.Provider.BaseURL() == prov.BaseURL() {
		return pp, nil
	}

	h.proxyMu.Lock()
	defer h.proxyMu.Unlock()

	// Double-check after acquiring write lock.
	pp, exists = h.providerProxies[name]
	if exists && pp.Provider.Name() == name && pp.Provider.BaseURL() == prov.BaseURL() {
		return pp, nil
	}

	// Resolve key and pool for this provider.
	pools := h.resolvePools()
	keys := h.resolveKeys()

	apiKey := ""
	if keys != nil {
		apiKey = keys[name]
	}
	var pool *keypool.KeyPool
	if pools != nil {
		pool = pools[name]
	}

	newPP, err := NewProviderProxy(prov, apiKey, pool, h.debugOpts, h.modifyResponse)
	if err != nil {
		return nil, fmt.Errorf("create proxy for %s: %w", name, err)
	}

	h.providerProxies[name] = newPP
	return newPP, nil
}

// extractOpenAIModel extracts the model field from a JSON body.
// Returns empty string if model is not found or not a string.
func extractOpenAIModel(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	return req.Model
}

// writeOpenAIError writes an OpenAI-format error response.
func writeOpenAIError(w http.ResponseWriter, status int, msg, errType, code string) {
	resp := OpenAIErrorResponse{
		Error: OpenAIErrorDetail{
			Message: msg,
			Type:    errType,
			Code:    code,
		},
	}
	writeJSON(w, status, resp)
}
