package proxy

import (
	"errors"
	"net/http"

	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/providers"
)

// SetupResponsesRoutes registers Responses API routes on the given mux.
// Routes (mounted under the OpenAI mux):
//   - POST /openai/v1/responses — Responses API endpoint (Codex-compatible)
//   - GET /openai/v1/responses/models — Responses API model listing
//   - GET /openai/v1/responses/providers — Responses API provider status
//
// No auth middleware is applied (local deployment per user decision).
// Middleware order (outermost first):
//  1. RequestIDMiddleware — generates request ID
//  2. LoggingMiddleware — logs with request ID
//  3. MaxBodyBytesMiddleware — enforces max_body_bytes limit
//  4. ConcurrencyMiddleware — enforces max_concurrent limit
//  5. Handler
func SetupResponsesRoutes(mux *http.ServeMux, opts *OpenAIRoutesOptions) error {
	if opts == nil {
		return errors.New("responses routes options are required")
	}

	chatHandler, err := buildResponsesHandler(opts)
	if err != nil {
		return err
	}
	mux.Handle("POST /openai/v1/responses", chatHandler)

	// Reuse existing OpenAI models endpoint for Responses API
	providersGetter := responsesLiveProvidersGetter(opts)
	mux.Handle("GET /openai/v1/responses/models", NewOpenAIModelsHandlerWithProviderFunc(providersGetter))

	poolsGetter := func() map[string]*keypool.KeyPool {
		if opts.GetProviderPools != nil {
			return opts.GetProviderPools()
		}
		return opts.ProviderPools
	}
	mux.Handle("GET /openai/v1/responses/providers", NewProvidersHandlerWithProviderFuncAndPools(providersGetter, poolsGetter))

	return nil
}

// buildResponsesHandler wires the Responses handler with middleware stack.
// No auth middleware (local deployment).
func buildResponsesHandler(opts *OpenAIRoutesOptions) (http.Handler, error) {
	handler, err := NewResponsesHandler(&OpenAIHandlerOptions{
		Router:           opts.ProviderRouter,
		Providers:        opts.ProviderInfosFunc,
		GetProviderPools: opts.GetProviderPools,
		GetProviderKeys:  opts.GetProviderKeys,
		DebugOptions:     opts.DebugOptions,
		ConfigProvider:   opts.ConfigProvider,
	})
	if err != nil {
		return nil, err
	}

	return wireOpenAICompatMiddleware(handler, opts), nil
}

// responsesLiveProvidersGetter adapts the provider info for Responses API
func responsesLiveProvidersGetter(opts *OpenAIRoutesOptions) ProvidersGetter {
	return func() []providers.Provider {
		infos := opts.ProviderInfosFunc()
		providers := make([]providers.Provider, len(infos))
		for i, info := range infos {
			providers[i] = info.Provider
		}
		return providers
	}
}
