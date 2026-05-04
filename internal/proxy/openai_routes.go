package proxy

import (
	"errors"
	"net/http"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/keypool"
	"github.com/omarluq/cc-relay/internal/providers"
	"github.com/omarluq/cc-relay/internal/router"
)

// OpenAIRoutesOptions configures OpenAI route setup.
type OpenAIRoutesOptions struct {
	ProviderRouter     router.ProviderRouter
	ConfigProvider     config.RuntimeConfigGetter
	ProviderInfosFunc  ProviderInfoFunc
	ProviderPools      map[string]*keypool.KeyPool
	ProviderKeys       map[string]string
	GetProviderPools   KeyPoolsFunc
	GetProviderKeys    KeysFunc
	GetAllProviders    ProvidersGetter
	ConcurrencyLimiter *ConcurrencyLimiter
	DebugOptions       config.DebugOptions
}

const openAIRoutesRequiredMsg = "openai routes options are required"

// SetupOpenAIRoutes registers OpenAI API routes on the given mux.
// Routes:
//   - POST /openai/v1/chat/completions — OpenAI Chat Completions
//   - GET /openai/v1/models — OpenAI model listing
//   - GET /openai/v1/providers — OpenAI provider key status
//
// No auth middleware is applied (local deployment per user decision).
// Middleware order (outermost first):
//  1. RequestIDMiddleware — generates request ID
//  2. LoggingMiddleware — logs with request ID
//  3. MaxBodyBytesMiddleware — enforces max_body_bytes limit
//  4. ConcurrencyMiddleware — enforces max_concurrent limit
//  5. Handler
func SetupOpenAIRoutes(mux *http.ServeMux, opts *OpenAIRoutesOptions) error {
	if opts == nil {
		return errors.New(openAIRoutesRequiredMsg)
	}

	chatHandler, err := buildOpenAIChatHandler(opts)
	if err != nil {
		return err
	}
	mux.Handle("POST /openai/v1/chat/completions", chatHandler)

	providersGetter := openAILiveProvidersGetter(opts)
	mux.Handle("GET /openai/v1/models", NewOpenAIModelsHandlerWithProviderFunc(providersGetter))

	poolsGetter := func() map[string]*keypool.KeyPool {
		if opts.GetProviderPools != nil {
			return opts.GetProviderPools()
		}
		return opts.ProviderPools
	}
	mux.Handle("GET /openai/v1/providers", NewProvidersHandlerWithProviderFuncAndPools(providersGetter, poolsGetter))

	return nil
}

// buildOpenAIChatHandler wires the OpenAI handler with middleware stack.
// No auth middleware (local deployment).
func buildOpenAIChatHandler(opts *OpenAIRoutesOptions) (http.Handler, error) {
	handler, err := NewOpenAIHandler(&OpenAIHandlerOptions{
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

	var h http.Handler = handler

	// Apply max_body_bytes limit (hot-reloadable).
	h = MaxBodyBytesMiddleware(func() int64 {
		cfg := opts.ConfigProvider.Get()
		if cfg == nil {
			return 0
		}
		return cfg.Server.MaxBodyBytes
	})(h)

	// Apply concurrency limit if limiter provided.
	if opts.ConcurrencyLimiter != nil {
		h = ConcurrencyMiddleware(opts.ConcurrencyLimiter)(h)
	}

	// Logging with live debug options.
	h = LoggingMiddlewareWithProvider(func() config.DebugOptions {
		cfg := opts.ConfigProvider.Get()
		if cfg == nil {
			return config.DebugOptions{}
		}
		return cfg.Logging.DebugOptions
	})(h)

	// Request ID (outermost).
	h = RequestIDMiddleware()(h)

	return h, nil
}

// openAILiveProvidersGetter returns a function that resolves the current OpenAI provider list.
func openAILiveProvidersGetter(opts *OpenAIRoutesOptions) func() []providers.Provider {
	return func() []providers.Provider {
		if opts.GetAllProviders != nil {
			if list := opts.GetAllProviders(); list != nil {
				return list
			}
		}
		return nil
	}
}
