package di

import (
	"net/http"

	"github.com/samber/do/v2"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/proxy"
)

// ImagesHandlerService wires the Codex image gateway handler with the
// shared config and concurrency services.
type ImagesHandlerService struct {
	Handler     *proxy.ImagesHandler
	cfgSvc      *ConfigService
	concurrency *ConcurrencyService
}

// NewImagesHandler creates the Codex image gateway handler service.
func NewImagesHandler(i do.Injector) (*ImagesHandlerService, error) {
	cfgSvc := do.MustInvoke[*ConfigService](i)
	concurrencySvc := do.MustInvoke[*ConcurrencyService](i)

	handler, err := proxy.NewImagesHandler(&proxy.ImagesHandlerOptions{
		ConfigProvider: cfgSvc,
	})
	if err != nil {
		return nil, err
	}

	return &ImagesHandlerService{
		Handler:     handler,
		cfgSvc:      cfgSvc,
		concurrency: concurrencySvc,
	}, nil
}

// SetupImagesRoutes registers the image routes on the given mux.
func (s *ImagesHandlerService) SetupImagesRoutes(mux *http.ServeMux) error {
	return proxy.SetupImagesRoutes(mux, &proxy.ImagesRoutesOptions{
		ConfigProvider:     s.cfgSvc,
		Handler:            s.Handler,
		ConcurrencyLimiter: s.concurrency.Limiter,
	})
}

// Compile-time guard: ConfigService must expose hot-reloadable config to the
// image gateway handler.
var _ config.RuntimeConfigGetter = (*ConfigService)(nil)
