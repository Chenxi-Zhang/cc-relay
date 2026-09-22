package proxy

import (
	"errors"
	"net/http"

	"github.com/omarluq/cc-relay/internal/config"
)

// ImagesRoutesOptions configures Codex image route setup.
type ImagesRoutesOptions struct {
	ConfigProvider     config.RuntimeConfigGetter
	Handler            *ImagesHandler
	ConcurrencyLimiter *ConcurrencyLimiter
}

const imagesRoutesRequiredMsg = "images routes options are required"

// SetupImagesRoutes registers the OpenAI Images API routes on the given mux.
// Routes:
//   - POST /v1/images/generations — text-to-image
//   - POST /v1/images/edits — image editing (multipart upload)
//
// Middleware order matches the OpenAI routes (no auth for local deployment):
// RequestID → Logging → MaxBodyBytes → Concurrency → Handler.
func SetupImagesRoutes(mux *http.ServeMux, opts *ImagesRoutesOptions) error {
	if opts == nil || opts.Handler == nil {
		return errors.New(imagesRoutesRequiredMsg)
	}

	mux.Handle("POST /v1/images/generations", wireImagesMiddleware(http.HandlerFunc(opts.Handler.HandleGenerations), opts))
	mux.Handle("POST /v1/images/edits", wireImagesMiddleware(http.HandlerFunc(opts.Handler.HandleEdits), opts))

	return nil
}

func wireImagesMiddleware(handler http.Handler, opts *ImagesRoutesOptions) http.Handler {
	var h http.Handler = handler

	h = MaxBodyBytesMiddleware(func() int64 {
		cfg := opts.ConfigProvider.Get()
		if cfg == nil {
			return 0
		}
		return cfg.Server.MaxBodyBytes
	})(h)

	if opts.ConcurrencyLimiter != nil {
		h = ConcurrencyMiddleware(opts.ConcurrencyLimiter)(h)
	}

	h = LoggingMiddlewareWithProvider(func() config.DebugOptions {
		cfg := opts.ConfigProvider.Get()
		if cfg == nil {
			return config.DebugOptions{}
		}
		return cfg.Logging.DebugOptions
	})(h)

	h = RequestIDMiddleware()(h)

	return h
}
