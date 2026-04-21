// Package proxy implements the HTTP proxy server for cc-relay.
package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/tidwall/gjson"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/health"
	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/router"
)

// contextKey is used for storing values in request context.
type contextKey string

const (
	keyIDContextKey           contextKey = "keyID"
	providerNameContextKey    contextKey = "providerName"
	modelNameContextKey       contextKey = "modelName"
	thinkingContextContextKey contextKey = "thinkingContext"
	handlerOptionsRequiredMsg            = "handler options are required"
)

// ProviderInfoFunc is a function that returns current provider routing information.
// This enables hot-reload of provider inputs (enabled/disabled, weights, priorities)
// without recreating the handler.
type ProviderInfoFunc func() []router.ProviderInfo

// KeyPoolsFunc returns the current key pools map for hot-reload support.
type KeyPoolsFunc func() map[string]*keypool.KeyPool

// KeysFunc returns the current fallback keys map for hot-reload support.
type KeysFunc func() map[string]string

// HandlerOptions configures handler construction.
type HandlerOptions struct {
	ProviderRouter    router.ProviderRouter
	Provider          providers.Provider
	ProviderPools     map[string]*keypool.KeyPool
	ProviderInfosFunc ProviderInfoFunc
	Pool              *keypool.KeyPool
	ProviderKeys      map[string]string
	GetProviderPools  KeyPoolsFunc
	GetProviderKeys   KeysFunc
	RoutingConfig     *config.RoutingConfig
	HealthTracker     *health.Tracker
	SignatureCache    *SignatureCache
	APIKey            string `json:"-"`
	ProviderInfos     []router.ProviderInfo
	DebugOptions      config.DebugOptions
	RoutingDebug      bool
}

// Handler proxies requests to a backend provider.
type Handler struct {
	router           router.ProviderRouter
	runtimeCfg       config.RuntimeConfigGetter
	defaultProvider  providers.Provider
	routingConfig    *config.RoutingConfig
	healthTracker    *health.Tracker
	signatureCache   *SignatureCache
	providerProxies  map[string]*ProviderProxy
	providers        ProviderInfoFunc
	getProviderPools KeyPoolsFunc
	getProviderKeys  KeysFunc
	providerPools    map[string]*keypool.KeyPool
	providerKeys     map[string]string
	debugOpts        config.DebugOptions
	proxyMu          sync.RWMutex
	routingDebug     bool
}

// NewHandler creates a new proxy handler.
// If ProviderRouter is provided, it will be used for provider selection.
// If ProviderRouter is nil, Provider is used directly (single provider mode).
// ProviderPools maps provider names to their key pools (may be nil for providers without pooling).
// ProviderKeys maps provider names to their fallback API keys.
// RoutingConfig contains model-based routing configuration (may be nil).
// If HealthTracker is provided, success/failure will be reported to circuit breakers.
// If SignatureCache is provided, thinking signatures are cached for cross-provider reuse.
//
// For hot-reloadable provider inputs, set ProviderInfosFunc. Otherwise, ProviderInfos is used.
func NewHandler(opts *HandlerOptions) (*Handler, error) {
	return NewHandlerWithLiveProviders(opts)
}

// NewHandlerWithLiveProviders creates a new proxy handler with hot-reloadable provider info.
// ProviderInfosFunc is called per-request to get current provider routing information.
func NewHandlerWithLiveProviders(opts *HandlerOptions) (*Handler, error) {
	return NewHandlerWithLiveKeyPools(opts)
}

// NewHandlerWithLiveKeyPools creates a new proxy handler with hot-reloadable key pools.
// When providers are enabled via config reload, their keys and pools are available immediately.
func NewHandlerWithLiveKeyPools(opts *HandlerOptions) (*Handler, error) {
	if opts == nil {
		return nil, errors.New(handlerOptionsRequiredMsg)
	}
	return newHandlerWithOptions(opts)
}

func newHandlerWithOptions(opts *HandlerOptions) (*Handler, error) {
	providerInfosFunc := opts.ProviderInfosFunc
	if providerInfosFunc == nil {
		providerInfos := opts.ProviderInfos
		providerInfosFunc = func() []router.ProviderInfo { return providerInfos }
	}

	providerPools := opts.ProviderPools
	providerKeys := opts.ProviderKeys
	if opts.GetProviderPools != nil || opts.GetProviderKeys != nil {
		providerPools, providerKeys = resolveInitialMaps(opts.GetProviderPools, opts.GetProviderKeys)
	}

	initialInfos := providerInfosFunc()

	handler := &Handler{
		providerProxies:  make(map[string]*ProviderProxy),
		defaultProvider:  opts.Provider,
		providers:        providerInfosFunc,
		router:           opts.ProviderRouter,
		runtimeCfg:       nil,
		routingConfig:    opts.RoutingConfig,
		debugOpts:        opts.DebugOptions,
		routingDebug:     opts.RoutingDebug,
		healthTracker:    opts.HealthTracker,
		signatureCache:   opts.SignatureCache,
		getProviderPools: opts.GetProviderPools,
		getProviderKeys:  opts.GetProviderKeys,
		providerPools:    providerPools,
		providerKeys:     providerKeys,
		proxyMu:          sync.RWMutex{},
	}

	if err := initProviderProxies(handler, &providerProxyInit{
		provider:      opts.Provider,
		apiKey:        opts.APIKey,
		pool:          opts.Pool,
		providerPools: providerPools,
		providerKeys:  providerKeys,
		debugOpts:     opts.DebugOptions,
		initialInfos:  initialInfos,
	}); err != nil {
		return nil, err
	}

	return handler, nil
}

func resolveInitialMaps(
	getProviderPools KeyPoolsFunc,
	getProviderKeys KeysFunc,
) (providerPools map[string]*keypool.KeyPool, providerKeys map[string]string) {
	if getProviderPools != nil {
		providerPools = getProviderPools()
	}
	if getProviderKeys != nil {
		providerKeys = getProviderKeys()
	}
	return providerPools, providerKeys
}

type providerProxyInit struct {
	provider      providers.Provider
	pool          *keypool.KeyPool
	providerPools map[string]*keypool.KeyPool
	providerKeys  map[string]string
	apiKey        string
	initialInfos  []router.ProviderInfo
	debugOpts     config.DebugOptions
}

func initProviderProxies(handler *Handler, init *providerProxyInit) error {
	if len(init.initialInfos) == 0 {
		providerProxy, err := NewProviderProxy(init.provider, init.apiKey,
			init.pool, init.debugOpts, handler.modifyResponse,
		)
		if err != nil {
			return err
		}
		handler.providerProxies[init.provider.Name()] = providerProxy
		return nil
	}

	for _, info := range init.initialInfos {
		prov := info.Provider
		key := ""
		if init.providerKeys != nil {
			key = init.providerKeys[prov.Name()]
		}
		var provPool *keypool.KeyPool
		if init.providerPools != nil {
			provPool = init.providerPools[prov.Name()]
		}

		providerProxy, err := NewProviderProxy(prov, key, provPool, init.debugOpts, handler.modifyResponse)
		if err != nil {
			return fmt.Errorf("failed to create proxy for %s: %w", prov.Name(), err)
		}
		handler.providerProxies[prov.Name()] = providerProxy
	}

	return nil
}

// SetRuntimeConfigGetter sets a live config provider for dynamic routing/debug settings.
// When set, the handler prefers this over static routingConfig/debugOpts/routingDebug.
func (h *Handler) SetRuntimeConfigGetter(cfg config.RuntimeConfigGetter) {
	h.runtimeCfg = cfg
}

func (h *Handler) getRuntimeConfigGetter() *config.Config {
	if h.runtimeCfg == nil {
		return nil
	}
	return h.runtimeCfg.Get()
}

func (h *Handler) getRoutingConfig() *config.RoutingConfig {
	if cfg := h.getRuntimeConfigGetter(); cfg != nil {
		return &cfg.Routing
	}
	return h.routingConfig
}

func (h *Handler) getDebugOptions() config.DebugOptions {
	if cfg := h.getRuntimeConfigGetter(); cfg != nil {
		return cfg.Logging.DebugOptions
	}
	return h.debugOpts
}

func (h *Handler) getRetryConfig() config.RetryConfig {
	if rc := h.getRoutingConfig(); rc != nil {
		return rc.Retry
	}
	return config.RetryConfig{}
}

func (h *Handler) isRoutingDebugEnabled() bool {
	if cfg := h.getRuntimeConfigGetter(); cfg != nil {
		return cfg.Routing.IsDebugEnabled()
	}
	return h.routingDebug
}

// modifyResponse handles key pool updates and circuit breaker reporting.
// SSE headers are handled by ProviderProxy.modifyResponse before this is called.
func (h *Handler) modifyResponse(resp *http.Response) error {
	// Get provider name from context to find the correct key pool
	providerName := ""
	if name, ok := resp.Request.Context().Value(providerNameContextKey).(string); ok {
		providerName = name
	}

	var skipCircuitBreaker bool

	if providerName != "" {
		// Thread-safe read of provider proxy
		h.proxyMu.RLock()
		pp, ok := h.providerProxies[providerName]
		h.proxyMu.RUnlock()
		if ok && pp.KeyPool != nil {
			skipCircuitBreaker = h.updateKeyPoolFromResponse(resp, pp.KeyPool, pp.Provider)
		}
	}

	// Report outcome to circuit breaker (skip for permanent key errors)
	h.reportOutcomeWithSkip(resp, skipCircuitBreaker)

	return nil
}

// updateKeyPoolFromResponse updates key pool state from response headers.
// Returns true if the circuit breaker should skip counting this as a provider failure
// (e.g., permanent key errors that don't indicate provider health issues).
func (h *Handler) updateKeyPoolFromResponse(
	resp *http.Response, pool *keypool.KeyPool, prov providers.Provider,
) bool {
	keyID, ok := resp.Request.Context().Value(keyIDContextKey).(string)
	if !ok || keyID == "" {
		return false
	}

	logger := zerolog.Ctx(resp.Request.Context())

	// Update key state from response headers
	if err := pool.UpdateKeyFromHeaders(keyID, resp.Header); err != nil {
		logger.Debug().Err(err).Msg("failed to update key from headers")
	}

	// Handle 429 from backend
	if resp.StatusCode == http.StatusTooManyRequests {
		// Use ZAI-specific 429 handling for Zhipu providers
		if providers.IsZAIProvider(prov.Owner()) {
			return h.handleZAI429(resp, pool, keyID, logger)
		}

		// Generic 429 handling for other providers
		retryAfter := parseRetryAfter(resp.Header)
		pool.MarkKeyExhausted(keyID, retryAfter)
		logger.Warn().
			Str("key_id", keyID).
			Dur("cooldown", retryAfter).
			Msg("key hit rate limit, marking cooldown")
	}

	// Handle 400 from backend for ZAI provider
	if resp.StatusCode == http.StatusBadRequest && providers.IsZAIProvider(prov.Owner()) {
		return h.handleZAI400(resp, pool, keyID, logger)
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
func (h *Handler) handleZAI400(
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
func (h *Handler) handleZAI429(
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

// reportOutcomeWithSkip records success or failure to the circuit breaker.
// When skip is true (e.g., permanent key errors), the outcome is not reported,
// preventing key-specific issues from affecting the provider's circuit breaker state.
func (h *Handler) reportOutcomeWithSkip(resp *http.Response, skip bool) {
	if h.healthTracker == nil {
		return // No health tracking configured
	}

	// Get provider name from context
	providerName, ok := resp.Request.Context().Value(providerNameContextKey).(string)
	if !ok || providerName == "" {
		return // No provider name in context (single provider mode without routing)
	}

	// Skip reporting for permanent key errors — they don't indicate provider health issues
	if skip {
		return
	}

	if health.ShouldCountAsFailure(resp.StatusCode, nil) {
		h.healthTracker.RecordFailure(providerName, fmt.Errorf("HTTP %d", resp.StatusCode))
	} else {
		h.healthTracker.RecordSuccess(providerName)
	}
}

// getOrCreateProxy returns the proxy for the given provider, creating it lazily if needed.
// This allows newly enabled providers (after config reload) to become routable without
// handler recreation. Uses double-checked locking for efficiency.
// Uses live accessor functions (getProviderPools, getProviderKeys) when available for
// hot-reload support, falling back to static maps for backward compatibility.
func (h *Handler) getOrCreateProxy(prov providers.Provider) (*ProviderProxy, error) {
	provName := prov.Name()

	key, pool, keys, pools := h.currentKeyPool(provName)

	// Fast path: read lock to check if proxy exists and matches
	h.proxyMu.RLock()
	providerProxy, exists := h.providerProxies[provName]
	h.proxyMu.RUnlock()

	if exists && h.proxyMatches(providerProxy, prov, keys, pools, key, pool) {
		return providerProxy, nil
	}

	// Slow path: write lock to create proxy
	h.proxyMu.Lock()
	defer h.proxyMu.Unlock()

	// Double-check after acquiring write lock
	providerProxy, exists = h.providerProxies[provName]
	if exists && h.proxyMatches(providerProxy, prov, keys, pools, key, pool) {
		return providerProxy, nil
	}

	// In single-provider mode (maps==nil), preserve existing proxy's values.
	// This ensures we don't lose values set during handler construction.
	key, pool = h.preserveProxyAuthInputs(providerProxy, key, pool, keys, pools)

	// (Re)create proxy if provider instance or auth inputs changed
	providerProxy, err := NewProviderProxy(prov, key, pool, h.getDebugOptions(), h.modifyResponse)
	if err != nil {
		return nil, fmt.Errorf("failed to create proxy for %s: %w", provName, err)
	}

	h.providerProxies[provName] = providerProxy
	return providerProxy, nil
}

func (h *Handler) currentKeyPool(
	provName string,
) (
	key string,
	pool *keypool.KeyPool,
	keys map[string]string,
	pools map[string]*keypool.KeyPool,
) {
	keys = h.providerKeys
	pools = h.providerPools
	if h.getProviderKeys != nil {
		keys = h.getProviderKeys()
	}
	if h.getProviderPools != nil {
		pools = h.getProviderPools()
	}

	key = ""
	if keys != nil {
		key = keys[provName]
	}
	pool = nil
	if pools != nil {
		pool = pools[provName]
	}

	return key, pool, keys, pools
}

func (h *Handler) preserveProxyAuthInputs(
	providerProxy *ProviderProxy,
	key string,
	pool *keypool.KeyPool,
	keys map[string]string,
	pools map[string]*keypool.KeyPool,
) (string, *keypool.KeyPool) {
	if keys == nil && providerProxy != nil && providerProxy.APIKey != "" {
		key = providerProxy.APIKey
	}
	if pools == nil && providerProxy != nil && providerProxy.KeyPool != nil {
		pool = providerProxy.KeyPool
	}
	return key, pool
}

// proxyMatches checks if an existing proxy matches the given provider and auth inputs.
// In single-provider mode (maps are nil), keys/pools always match since we preserve
// values set during handler construction. In multi-provider mode, compares values
// to detect hot-reload changes.
func (h *Handler) proxyMatches(
	providerProxy *ProviderProxy,
	prov providers.Provider,
	keys map[string]string,
	pools map[string]*keypool.KeyPool,
	key string,
	pool *keypool.KeyPool,
) bool {
	if providerProxy == nil {
		return false
	}

	// Provider instance must match; rebuild if provider was recreated on reload.
	if providerProxy.Provider != prov {
		return false
	}

	// Provider must match name and base URL
	if providerProxy.Provider.Name() != prov.Name() || providerProxy.Provider.BaseURL() != prov.BaseURL() {
		return false
	}

	// In single-provider mode (nil maps), keys/pools always match.
	// This preserves values set during handler construction via NewHandler.
	keysMatch := keys == nil || providerProxy.APIKey == key
	poolsMatch := pools == nil || providerProxy.KeyPool == pool

	return keysMatch && poolsMatch
}

// selectProvider chooses a provider using the router or returns the static provider.
// In single provider mode (router is nil or no providers), returns the static provider.
// If model is provided and model-based routing is enabled, filters providers first.
// If hasThinkingAffinity is true, uses deterministic selection (first healthy provider)
// to ensure thinking signature validation works across conversation turns.
func (h *Handler) selectProvider(
	ctx context.Context, model string, hasThinkingAffinity bool,
) (router.ProviderInfo, error) {
	start := time.Now()
	if timings := getRequestTimings(ctx); timings != nil {
		defer func() {
			timings.Routing = time.Since(start)
		}()
	}

	candidates, hasCandidates := h.providerCandidates()
	if !hasCandidates {
		return h.defaultProviderInfo(), nil
	}

	// Filter providers if model-based routing is enabled
	candidates, hasCandidates = h.applyModelRouting(candidates, model)
	if !hasCandidates {
		return h.defaultProviderInfo(), nil
	}

	// If thinking affinity is required, use deterministic selection.
	// This ensures that thinking-enabled conversations always route to the same
	// provider (the first healthy one), preventing signature validation failures.
	candidates = h.applyThinkingAffinity(candidates, hasThinkingAffinity)

	return h.router.Select(ctx, candidates)
}

func (h *Handler) providerCandidates() ([]router.ProviderInfo, bool) {
	if h.router == nil || h.providers == nil {
		return nil, false
	}
	candidates := h.providers()
	if len(candidates) == 0 {
		return nil, false
	}
	return candidates, true
}

func (h *Handler) defaultProviderInfo() router.ProviderInfo {
	return router.ProviderInfo{
		Provider:  h.defaultProvider,
		IsHealthy: func() bool { return true },
		Weight:    0,
		Priority:  0,
	}
}

func (h *Handler) applyModelRouting(
	candidates []router.ProviderInfo, model string,
) ([]router.ProviderInfo, bool) {
	if !h.isModelBasedRouting() || model == "" {
		return candidates, true
	}
	routingConfig := h.getRoutingConfig()
	if routingConfig == nil {
		return nil, false
	}
	return FilterProvidersByModel(
		model,
		candidates,
		routingConfig.ModelMapping,
		routingConfig.DefaultProvider,
	), true
}

func (h *Handler) applyThinkingAffinity(
	candidates []router.ProviderInfo, hasThinkingAffinity bool,
) []router.ProviderInfo {
	if !hasThinkingAffinity || len(candidates) <= 1 {
		return candidates
	}
	healthy := router.FilterHealthy(candidates)
	if len(healthy) > 0 {
		// Force single candidate - first healthy provider (deterministic)
		return healthy[:1]
	}
	return candidates
}

// isModelBasedRouting returns true if model-based routing is configured.
func (h *Handler) isModelBasedRouting() bool {
	routingConfig := h.getRoutingConfig()
	return routingConfig != nil &&
		routingConfig.Strategy == router.StrategyModelBased &&
		len(routingConfig.ModelMapping) > 0
}

// selectKeyFromPool handles key selection from the pool.
// Returns keyID, key, updated request, and success.
// If success is false, an error response has been written and caller should return.
func (h *Handler) selectKeyFromPool(
	writer http.ResponseWriter, request *http.Request, logger *zerolog.Logger,
	pool *keypool.KeyPool,
) (keyID, selectedKey string, updatedReq *http.Request, ok bool) {
	var err error
	keyID, selectedKey, err = pool.GetKey(request.Context())
	if errors.Is(err, keypool.ErrAllKeysExhausted) {
		retryAfter := pool.GetEarliestResetTime()
		WriteRateLimitError(writer, retryAfter)
		logger.Warn().
			Dur("retry_after", retryAfter).
			Msg("all keys exhausted, returning 429")
		return "", "", request, false
	}
	if err != nil {
		WriteError(writer, http.StatusInternalServerError, "internal_error",
			fmt.Sprintf("failed to select API key: %v", err))
		logger.Error().Err(err).Msg("failed to select API key")
		return "", "", request, false
	}

	// Add relay headers to response
	writer.Header().Set(HeaderRelayKeyID, keyID)
	stats := pool.GetStats()
	writer.Header().Set(HeaderRelayKeysTotal, strconv.Itoa(stats.TotalKeys))
	writer.Header().Set(HeaderRelayKeysAvail, strconv.Itoa(stats.AvailableKeys))

	// Store keyID in context for ModifyResponse
	updatedReq = request.WithContext(context.WithValue(request.Context(), keyIDContextKey, keyID))

	return keyID, selectedKey, updatedReq, true
}

// ServeHTTP handles the proxy request.
// When retry is enabled and the request is non-streaming, delegates to serveWithRetry
// for three-level 429 retry (same-key → different-key → different-provider).
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	start := time.Now()

	prep, requestOK := h.prepareRequest(writer, request)
	if !requestOK {
		return
	}
	request = prep.request

	retryCfg := h.getRetryConfig()

	// Use retry path when enabled. Streaming requests use serveStreamingWithRetry
	// which buffers only 429 responses; non-streaming uses serveWithRetry.
	if retryCfg.Enabled {
		if isStreamingRequest(request) {
			h.serveStreamingWithRetry(writer, request, prep, retryCfg, start)
		} else {
			h.serveWithRetry(writer, request, prep, retryCfg, start)
		}
		return
	}

	h.serveDirect(writer, request, prep, start)
}

// serveDirect is the original non-retry proxy path.
func (h *Handler) serveDirect(
	writer http.ResponseWriter, request *http.Request,
	prep requestPrep, start time.Time,
) {
	selected, release, err := h.selectProviderWithTracking(request.Context(), prep.model, prep.hasThinking)
	if err != nil {
		WriteError(writer, http.StatusServiceUnavailable, "api_error",
			fmt.Sprintf("failed to select provider: %v", err))
		return
	}
	if release != nil {
		defer release()
	}

	SetProviderNameOnWriter(writer, selected.Provider.Name())

	proxyCtx, requestOK := h.prepareProxyRequest(writer, request, selected.Provider)
	if !requestOK {
		return
	}

	backendStart := time.Now()
	serveReverseProxy(proxyCtx.proxy.Proxy, writer, proxyCtx.request)
	backendTime := time.Since(backendStart)

	h.logMetricsIfEnabled(proxyCtx.request, &proxyCtx.logger, start, backendTime, proxyCtx.getTLSMetrics)
}

// serveWithRetry implements three-level 429 retry.
//
//	Level 1: same key retry (transient 429 only)
//	Level 2: same provider, different key
//	Level 3: different provider
//
// Uses httptest.ResponseRecorder to buffer each attempt so that only the final
// response is flushed to the real writer.
func (h *Handler) serveWithRetry(
	writer http.ResponseWriter, request *http.Request,
	prep requestPrep, retryCfg config.RetryConfig, start time.Time,
) {
	rc := newRetryContext()
	var lastRecorder *httptest.ResponseRecorder

	// Buffer request body for retries.
	// httputil.ReverseProxy consumes the Body on each attempt, and
	// request.WithContext shares the Body field. Without buffering,
	// retry attempts send an empty body (422 "Field required").
	var bodyBytes []byte
	if request.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(request.Body)
		request.Body.Close()
		if err != nil {
			WriteError(writer, http.StatusInternalServerError, "internal_error",
				"failed to buffer request body for retry")
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		request.ContentLength = int64(len(bodyBytes))
	}

	for providerAttempt := 0; providerAttempt <= retryCfg.GetProviderRetries(); providerAttempt++ {
		// --- Provider selection (excluding previously failed) ---
		selected, release, err := h.selectProviderExcluding(
			request.Context(), prep.model, prep.hasThinking, rc,
		)
		if err != nil {
			if lastRecorder != nil {
				overrideRetryAfter(lastRecorder.Header())
		flushRecorder(lastRecorder, writer)
				return
			}
			WriteError(writer, http.StatusServiceUnavailable, "api_error",
				fmt.Sprintf("failed to select provider: %v", err))
			return
		}
		if release != nil {
			defer release()
		}

		providerName := selected.Provider.Name()

		// Get provider proxy for key pool access
		providerProxy, proxyErr := h.getOrCreateProxy(selected.Provider)
		if proxyErr != nil {
			if lastRecorder != nil {
				overrideRetryAfter(lastRecorder.Header())
		flushRecorder(lastRecorder, writer)
				return
			}
			WriteError(writer, http.StatusInternalServerError, "internal_error",
				fmt.Sprintf("failed to get proxy for provider %s: %v", providerName, proxyErr))
			return
		}

		// --- Key retry loop ---
		for keyAttempt := 0; keyAttempt <= retryCfg.GetKeyRetries(); keyAttempt++ {
			var keyID, selectedKey string
			var keyReq *http.Request
			var keyOK bool

			if providerProxy.KeyPool != nil {
				keyID, selectedKey, keyReq, keyOK = h.selectKeyFromPoolExcluding(
					request, providerProxy.KeyPool, providerName, rc,
				)
				if !keyOK {
					// No available keys in this provider — try next provider
					break
				}
				keyReq.Header.Set("X-Selected-Key", selectedKey)
			} else {
				// Single key mode — use provider's API key
				keyID = "default"
				selectedKey = providerProxy.APIKey
				keyReq = request.WithContext(context.WithValue(request.Context(), keyIDContextKey, keyID))
				keyReq.Header.Set("X-Selected-Key", selectedKey)
				keyOK = true
			}

			// --- Same-key retry loop ---
			for sameKeyAttempt := 0; sameKeyAttempt <= retryCfg.GetSameKeyRetries(); sameKeyAttempt++ {
				if !rc.canRetry(retryCfg) {
					break
				}

				// Backoff before same-key retry (not on first attempt)
				if sameKeyAttempt > 0 {
					backoff := rc.sameKeyBackoff(retryCfg)
					time.Sleep(backoff)
				}

				// Build per-attempt context with provider name and key ID
				attemptReq := keyReq.WithContext(context.WithValue(
					keyReq.Context(), providerNameContextKey, providerName,
				))

				// Create logger for this attempt
				logger := h.createProviderLoggerWithProvider(attemptReq, selected.Provider)
				attemptReq = attemptReq.WithContext(logger.WithContext(attemptReq.Context()))
				h.logAndSetDebugHeaders(writer, attemptReq, &logger, selected.Provider)
				h.rewriteModelIfNeeded(attemptReq, &logger, selected.Provider)
				attemptReq, getTLSMetrics := h.attachTLSTraceIfEnabled(attemptReq)

				// Buffer response via ResponseRecorder
				recorder := httptest.NewRecorder()
				SetProviderNameOnWriter(recorder, providerName)
				rc.recordAttempt(providerName, keyID)

				// Reset body for this attempt (ReverseProxy consumes Body)
				if bodyBytes != nil {
					attemptReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
					attemptReq.ContentLength = int64(len(bodyBytes))
				}

				backendStart := time.Now()
				serveReverseProxy(providerProxy.Proxy, recorder, attemptReq)
				backendTime := time.Since(backendStart)
				lastRecorder = recorder

				h.logMetricsIfEnabled(attemptReq, &logger, start, backendTime, getTLSMetrics)

				// Non-429 → success or non-retryable error, flush immediately
				if recorder.Code != http.StatusTooManyRequests {
					flushRecorder(recorder, writer)
					return
				}

				// --- 429 received ---
				// modifyResponse already ran during serveReverseProxy, which
				// called updateKeyPoolFromResponse → MarkKeyExhausted / MarkKeyUnhealthy.
				// Now decide whether to retry at Level 1 (same key) or escalate.

				// Parse Retry-After from the buffered response for backoff calculation
				rc.lastRetryAfter = parseRetryAfter(recorder.Header())

				// Check if 429 is transient → eligible for same-key retry
				if !rc.isTransient429(selected.Provider) {
					// QuotaExhausted or Permanent — same-key retry won't help
					logRetryAttempt(&logger, rc.attemptCount, providerName, keyID,
						recorder.Code, "L1_skip", "non-transient 429, escalating to next key")
					break // exit same-key loop → try different key
				}

				logRetryAttempt(&logger, rc.attemptCount, providerName, keyID,
					recorder.Code, "L1", "transient 429, same-key retry")
			}

			// Same-key retries exhausted for this key — exclude it
			rc.excludeKey(providerName, keyID)

			if !rc.canRetry(retryCfg) {
				break
			}
		}

		// Key retries exhausted for this provider — exclude it
		rc.excludeProvider(providerName)

		if !rc.canRetry(retryCfg) {
			break
		}

		// Backoff before switching provider
		if providerAttempt < retryCfg.GetProviderRetries() {
			time.Sleep(retryCfg.GetProviderBackoff())
		}
	}

	// All retry levels exhausted — return last 429 to client
	if lastRecorder != nil {
		logger := zerolog.Ctx(request.Context())
		logRetryExhausted(logger, rc.attemptCount, rc.triedProviders, rc.triedKeys)
		overrideRetryAfter(lastRecorder.Header())
		flushRecorder(lastRecorder, writer)
		return
	}

	// Should not reach here normally — no attempt was made
	WriteError(writer, http.StatusServiceUnavailable, "api_error", "no providers available for retry")
}

// serveStreamingWithRetry implements three-level 429 retry for streaming requests.
//
// Uses peekWriter instead of httptest.ResponseRecorder: 429 responses are buffered
// (small JSON errors), while 200 streaming responses are committed immediately to the
// real writer and stream directly with no added latency.
//
// Once a 200 response is committed (SSE streaming starts), the method returns
// immediately — no further retries are possible.
func (h *Handler) serveStreamingWithRetry(
	writer http.ResponseWriter, request *http.Request,
	prep requestPrep, retryCfg config.RetryConfig, start time.Time,
) {
	rc := newRetryContext()
	var lastPeek *peekWriter

	// Buffer request body for retries.
	var bodyBytes []byte
	if request.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(request.Body)
		request.Body.Close()
		if err != nil {
			WriteError(writer, http.StatusInternalServerError, "internal_error",
				"failed to buffer request body for streaming retry")
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		request.ContentLength = int64(len(bodyBytes))
	}

	for providerAttempt := 0; providerAttempt <= retryCfg.GetProviderRetries(); providerAttempt++ {
		selected, release, err := h.selectProviderExcluding(
			request.Context(), prep.model, prep.hasThinking, rc,
		)
		if err != nil {
			if lastPeek != nil {
				overrideRetryAfter(lastPeek.Header())
				lastPeek.FlushBuffered429()
				return
			}
			WriteError(writer, http.StatusServiceUnavailable, "api_error",
				fmt.Sprintf("failed to select provider: %v", err))
			return
		}
		if release != nil {
			defer release()
		}

		providerName := selected.Provider.Name()

		providerProxy, proxyErr := h.getOrCreateProxy(selected.Provider)
		if proxyErr != nil {
			if lastPeek != nil {
				overrideRetryAfter(lastPeek.Header())
				lastPeek.FlushBuffered429()
				return
			}
			WriteError(writer, http.StatusInternalServerError, "internal_error",
				fmt.Sprintf("failed to get proxy for provider %s: %v", providerName, proxyErr))
			return
		}

		// Set provider name once on the real writer for logging middleware.
		SetProviderNameOnWriter(writer, providerName)

		for keyAttempt := 0; keyAttempt <= retryCfg.GetKeyRetries(); keyAttempt++ {
			var keyID, selectedKey string
			var keyReq *http.Request
			var keyOK bool

			if providerProxy.KeyPool != nil {
				keyID, selectedKey, keyReq, keyOK = h.selectKeyFromPoolExcluding(
					request, providerProxy.KeyPool, providerName, rc,
				)
				if !keyOK {
					break
				}
				keyReq.Header.Set("X-Selected-Key", selectedKey)
			} else {
				keyID = "default"
				selectedKey = providerProxy.APIKey
				keyReq = request.WithContext(context.WithValue(request.Context(), keyIDContextKey, keyID))
				keyReq.Header.Set("X-Selected-Key", selectedKey)
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

				attemptReq := keyReq.WithContext(context.WithValue(
					keyReq.Context(), providerNameContextKey, providerName,
				))

				logger := h.createProviderLoggerWithProvider(attemptReq, selected.Provider)
				attemptReq = attemptReq.WithContext(logger.WithContext(attemptReq.Context()))
				h.logAndSetDebugHeaders(writer, attemptReq, &logger, selected.Provider)
				h.rewriteModelIfNeeded(attemptReq, &logger, selected.Provider)
				attemptReq, getTLSMetrics := h.attachTLSTraceIfEnabled(attemptReq)

				// Reset body for this attempt (ReverseProxy consumes Body)
				if bodyBytes != nil {
					attemptReq.Body = io.NopCloser(bytes.NewReader(bodyBytes))
					attemptReq.ContentLength = int64(len(bodyBytes))
				}

				pw := newPeekWriter(writer)
				rc.recordAttempt(providerName, keyID)

				backendStart := time.Now()
				serveReverseProxy(providerProxy.Proxy, pw, attemptReq)
				backendTime := time.Since(backendStart)

				h.logMetricsIfEnabled(attemptReq, &logger, start, backendTime, getTLSMetrics)

				// 200 streaming response already committed to real writer — return immediately.
				if pw.IsCommitted() {
					return
				}

				// Non-429, non-streaming (e.g., 400, 500) — commit buffered response.
				if !pw.Is429() {
					pw.FlushBuffered429()
					return
				}

				// --- 429 received (still buffered) ---
				lastPeek = pw

				rc.lastRetryAfter = parseRetryAfter(pw.Header())

				if !rc.isTransient429(selected.Provider) {
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

	// All retries exhausted — return last 429 to client
	if lastPeek != nil {
		logger := zerolog.Ctx(request.Context())
		logRetryExhausted(logger, rc.attemptCount, rc.triedProviders, rc.triedKeys)
		overrideRetryAfter(lastPeek.Header())
		lastPeek.FlushBuffered429()
		return
	}

	WriteError(writer, http.StatusServiceUnavailable, "api_error", "no providers available for streaming retry")
}

// serveReverseProxy forwards the request via the pre-configured reverse proxy.
// The proxy target URL was validated at construction time in NewProviderProxy.
func serveReverseProxy(proxy *httputil.ReverseProxy, w http.ResponseWriter, r *http.Request) {
	type httpHandler interface {
		ServeHTTP(http.ResponseWriter, *http.Request)
	}
	httpHandler(proxy).ServeHTTP(w, r)
}

type requestPrep struct {
	request     *http.Request
	model       string
	hasThinking bool
}

func (h *Handler) prepareRequest(writer http.ResponseWriter, request *http.Request) (requestPrep, bool) {
	modelOpt, bodyTooLarge := ExtractModelWithBodyCheck(request)
	if bodyTooLarge {
		WriteBodyTooLargeError(writer)
		return requestPrep{request: nil, model: "", hasThinking: false}, false
	}

	model := modelOpt.OrEmpty()
	if modelOpt.IsPresent() {
		request = request.WithContext(CacheModelInContext(request.Context(), model))
	}
	if model != "" {
		request = request.WithContext(context.WithValue(request.Context(), modelNameContextKey, model))
	}

	request = h.processThinkingSignatures(request, model)

	hasThinking := h.detectThinkingAffinity(&request)
	return requestPrep{request: request, model: model, hasThinking: hasThinking}, true
}

func (h *Handler) detectThinkingAffinity(request **http.Request) bool {
	if h.router == nil || h.providers == nil {
		return false
	}
	if len(h.providers()) <= 1 {
		return false
	}
	if !HasThinkingSignature(*request) {
		return false
	}
	*request = (*request).WithContext(CacheThinkingAffinityInContext((*request).Context(), true))
	return true
}

func (h *Handler) selectProviderWithTracking(
	ctx context.Context, model string, hasThinking bool,
) (router.ProviderInfo, func(), error) {
	selected, err := h.selectProvider(ctx, model, hasThinking)
	if err != nil {
		return router.ProviderInfo{}, nil, err
	}
	if tracker, ok := h.router.(router.ProviderLoadTracker); ok {
		tracker.Acquire(selected.Provider)
		return selected, func() { tracker.Release(selected.Provider) }, nil
	}
	return selected, nil, nil
}

type proxyContext struct {
	request       *http.Request
	proxy         *ProviderProxy
	logger        zerolog.Logger
	getTLSMetrics func() TLSMetrics
}

func (h *Handler) prepareProxyRequest(
	writer http.ResponseWriter, request *http.Request, selectedProvider providers.Provider,
) (proxyContext, bool) {
	providerProxy, err := h.getOrCreateProxy(selectedProvider)
	if err != nil {
		WriteError(writer, http.StatusInternalServerError, "internal_error",
			fmt.Sprintf("failed to get proxy for provider %s: %v", selectedProvider.Name(), err))
		return proxyContext{request: nil, proxy: nil, logger: zerolog.Logger{}, getTLSMetrics: nil}, false
	}

	logger := h.createProviderLoggerWithProvider(request, selectedProvider)
	request = request.WithContext(logger.WithContext(request.Context()))
	request = request.WithContext(context.WithValue(request.Context(), providerNameContextKey, selectedProvider.Name()))

	h.logAndSetDebugHeaders(writer, request, &logger, selectedProvider)

	var authOK bool
	request, authOK = h.handleAuthAndKeySelection(writer, request, &logger, providerProxy)
	if !authOK {
		return proxyContext{request: nil, proxy: nil, logger: zerolog.Logger{}, getTLSMetrics: nil}, false
	}

	h.rewriteModelIfNeeded(request, &logger, selectedProvider)

	request, getTLSMetrics := h.attachTLSTraceIfEnabled(request)
	logger.Debug().Msg("proxying request to backend")

	return proxyContext{
		request:       request,
		proxy:         providerProxy,
		logger:        logger,
		getTLSMetrics: getTLSMetrics,
	}, true
}

// logAndSetDebugHeaders logs routing strategy and sets debug headers if enabled.
func (h *Handler) logAndSetDebugHeaders(
	writer http.ResponseWriter, request *http.Request, logger *zerolog.Logger, selectedProvider providers.Provider,
) {
	hasThinking := GetThinkingAffinityFromContext(request.Context())

	// Log routing strategy if router is available
	if h.router != nil {
		event := logger.Debug().
			Str("strategy", h.router.Name()).
			Str("selected_provider", selectedProvider.Name())
		if hasThinking {
			event.Bool("thinking_affinity", true)
		}
		event.Msg("provider selected by router")
	}

	if !h.isRoutingDebugEnabled() {
		return
	}

	// Add debug headers if routing debug is enabled
	if h.router != nil {
		writer.Header().Set("X-CC-Relay-Strategy", h.router.Name())
		writer.Header().Set("X-CC-Relay-Provider", selectedProvider.Name())
		if hasThinking {
			writer.Header().Set("X-CC-Relay-Thinking-Affinity", "true")
		}
	}

	// Add health debug header
	if h.healthTracker != nil {
		state := h.healthTracker.GetState(selectedProvider.Name())
		writer.Header().Set("X-CC-Relay-Health", state.String())
	}
}

// rewriteModelIfNeeded rewrites model name if provider has model mapping configured.
func (h *Handler) rewriteModelIfNeeded(
	request *http.Request, logger *zerolog.Logger, selectedProvider providers.Provider,
) {
	if mapping := selectedProvider.GetModelMapping(); len(mapping) > 0 {
		rewriter := NewModelRewriter(mapping)
		if err := rewriter.RewriteRequest(request, logger); err != nil {
			logger.Warn().Err(err).Msg("failed to rewrite model in request body")
			// Continue with original request - don't fail on rewrite errors
		}
	}
}

// createProviderLoggerWithProvider creates a logger with the given provider context.
func (h *Handler) createProviderLoggerWithProvider(r *http.Request, p providers.Provider) zerolog.Logger {
	return zerolog.Ctx(r.Context()).With().
		Str("provider", p.Name()).
		Str("backend_url", p.BaseURL()).
		Logger()
}

// handleAuthAndKeySelection handles transparent auth mode detection and key selection.
// Returns the updated request and success status.
// Uses the provider from providerProxy for transparent auth checks and key selection.
func (h *Handler) handleAuthAndKeySelection(
	writer http.ResponseWriter, request *http.Request, logger *zerolog.Logger,
	providerProxy *ProviderProxy,
) (*http.Request, bool) {
	clientAuth := request.Header.Get("Authorization")
	clientAPIKey := request.Header.Get("x-api-key")
	hasClientAuth := clientAuth != "" || clientAPIKey != ""
	useTransparentAuth := hasClientAuth && providerProxy.Provider.SupportsTransparentAuth()

	if useTransparentAuth {
		logger.Debug().
			Bool("has_authorization", clientAuth != "").
			Bool("has_x_api_key", clientAPIKey != "").
			Msg("transparent mode: forwarding client auth")
		return request, true
	}

	if providerProxy.KeyPool != nil {
		return h.handleKeyPoolSelection(writer, request, logger, providerProxy, hasClientAuth, clientAuth, clientAPIKey)
	}

	// Single key mode - set header directly
	request.Header.Set("X-Selected-Key", providerProxy.APIKey)
	return request, true
}

// handleKeyPoolSelection handles key selection from the pool.
func (h *Handler) handleKeyPoolSelection(
	writer http.ResponseWriter, request *http.Request, logger *zerolog.Logger,
	providerProxy *ProviderProxy, hasClientAuth bool, clientAuth, clientAPIKey string,
) (*http.Request, bool) {
	if hasClientAuth {
		logger.Debug().
			Bool("has_authorization", clientAuth != "").
			Bool("has_x_api_key", clientAPIKey != "").
			Str("provider", providerProxy.Provider.Name()).
			Msg("provider does not support transparent auth, using configured keys")
	}

	_, selectedKey, updatedReq, selectionOK := h.selectKeyFromPool(writer, request, logger, providerProxy.KeyPool)
	if !selectionOK {
		return request, false
	}

	updatedReq.Header.Set("X-Selected-Key", selectedKey)
	// Note: defer cleanup not needed since header is only used within this request
	return updatedReq, true
}

// attachTLSTraceIfEnabled attaches TLS trace if debug metrics are enabled.
func (h *Handler) attachTLSTraceIfEnabled(request *http.Request) (req *http.Request, getMetrics func() TLSMetrics) {
	debugOpts := h.getDebugOptions()
	if !debugOpts.LogTLSMetrics {
		return request, nil
	}
	newCtx, metricsFunc := AttachTLSTrace(request.Context(), request)
	return request.WithContext(newCtx), metricsFunc
}

// logMetricsIfEnabled logs TLS and proxy metrics if debug mode is enabled.
func (h *Handler) logMetricsIfEnabled(
	request *http.Request, logger *zerolog.Logger, start time.Time,
	backendTime time.Duration, getTLSMetrics func() TLSMetrics,
) {
	debugOpts := h.getDebugOptions()
	if getTLSMetrics != nil {
		tlsMetrics := getTLSMetrics()
		LogTLSMetrics(request.Context(), tlsMetrics, debugOpts)
	}

	if debugOpts.IsEnabled() || logger.GetLevel() <= zerolog.DebugLevel {
		proxyMetrics := Metrics{
			BackendTime:     backendTime,
			TotalTime:       time.Since(start),
			BytesSent:       0,
			BytesReceived:   0,
			StreamingEvents: 0,
		}
		LogProxyMetrics(request.Context(), proxyMetrics, debugOpts)
	}
}

// parseRetryAfter parses the Retry-After header from an HTTP response.
// Returns the duration to wait before retrying. Defaults to 60 seconds if parsing fails.
func parseRetryAfter(headers http.Header) time.Duration {
	val := headers.Get("Retry-After")
	if val == "" {
		return 60 * time.Second // Default 1 minute
	}

	// Try seconds format (integer)
	if seconds, err := strconv.Atoi(val); err == nil {
		return time.Duration(seconds) * time.Second
	}

	// Try HTTP-date format
	if t, err := http.ParseTime(val); err == nil {
		duration := time.Until(t)
		if duration > 0 {
			return duration
		}
	}

	return 60 * time.Second // Default if parsing failed
}

// processThinkingSignatures processes thinking block signatures in the request.
// Looks up cached signatures and replaces/drops blocks as needed.
// Returns the potentially modified request.
func (h *Handler) processThinkingSignatures(request *http.Request, model string) *http.Request {
	// Skip if no signature cache configured
	if h.signatureCache == nil {
		return request
	}

	// Read body for thinking detection
	if request.Body == nil {
		return request
	}
	body, err := io.ReadAll(request.Body)
	if closeErr := request.Body.Close(); closeErr != nil {
		zerolog.Ctx(request.Context()).Error().Err(closeErr).Msg("failed to close request body")
	}
	// Always restore the body (and ContentLength) using the bytes read,
	// even if io.ReadAll returned an error, so upstream handlers see
	// the same body that was available to us.
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	if err != nil {
		return request
	}

	// Fast path: check if request has thinking blocks
	if !HasThinkingBlocks(body) {
		return request
	}

	// Extract model from body if not already known
	modelName := model
	if modelName == "" {
		modelName = gjson.GetBytes(body, "model").String()
	}

	// Process thinking blocks
	logger := zerolog.Ctx(request.Context())
	modifiedBody, thinkingCtx, err := ProcessRequestThinking(
		request.Context(), body, modelName, h.signatureCache,
	)
	if err != nil {
		logger.Warn().Err(err).Msg("failed to process thinking signatures")
		return request
	}

	// Log processing results
	if thinkingCtx.DroppedBlocks > 0 {
		logger.Debug().
			Int("dropped_blocks", thinkingCtx.DroppedBlocks).
			Msg("dropped unsigned thinking blocks")
	}
	if thinkingCtx.ReorderedBlocks {
		logger.Debug().Msg("reordered content blocks (thinking first)")
	}

	// Update request body
	request.Body = io.NopCloser(bytes.NewReader(modifiedBody))
	request.ContentLength = int64(len(modifiedBody))

	// Store thinking context for response processing
	request = request.WithContext(context.WithValue(request.Context(), thinkingContextContextKey, thinkingCtx))

	return request
}

// GetModelNameFromContext retrieves the model name from context.
func GetModelNameFromContext(ctx context.Context) string {
	if model, ok := ctx.Value(modelNameContextKey).(string); ok {
		return model
	}
	return ""
}

// GetThinkingContextFromContext retrieves the thinking context from context.
func GetThinkingContextFromContext(ctx context.Context) *ThinkingContext {
	if thinkingCtx, ok := ctx.Value(thinkingContextContextKey).(*ThinkingContext); ok {
		return thinkingCtx
	}
	return nil
}
