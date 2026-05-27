package di

import (
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/samber/do/v2"

	"github.com/omarluq/cc-relay/internal/config"
	"github.com/omarluq/cc-relay/internal/proxy"
	"github.com/omarluq/cc-relay/internal/router"
)

// OpenAIProviderInfoService holds live OpenAI provider routing information.
type OpenAIProviderInfoService struct {
	infos       atomic.Pointer[[]router.ProviderInfo]
	cfgSvc      *ConfigService
	providerSvc *OpenAIProviderMapService
	trackerSvc  *HealthTrackerService
}

// Get returns the current OpenAI provider info slice (lock-free read).
func (s *OpenAIProviderInfoService) Get() []router.ProviderInfo {
	ptr := s.infos.Load()
	if ptr == nil {
		return nil
	}
	return append([]router.ProviderInfo(nil), (*ptr)...)
}

// RebuildFrom rebuilds the OpenAI provider info slice from the given config.
func (s *OpenAIProviderInfoService) RebuildFrom(cfg *config.Config) {
	infos := rebuildProviderInfos(cfg.OpenAIProviders, s.providerSvc.GetProviders(), s.trackerSvc)
	s.infos.Store(&infos)
}

// StartWatching registers a callback to rebuild OpenAI provider info on config reload.
func (s *OpenAIProviderInfoService) StartWatching() {
	if s.cfgSvc.watcher == nil {
		return
	}

	s.cfgSvc.watcher.OnReload(func(newCfg *config.Config) error {
		s.RebuildFrom(newCfg)
		log.Info().Msg("openai provider info rebuilt after config reload")
		return nil
	})
}

// NewOpenAIProviderInfo creates the OpenAI provider info service.
func NewOpenAIProviderInfo(i do.Injector) (*OpenAIProviderInfoService, error) {
	cfgSvc := do.MustInvoke[*ConfigService](i)
	providerSvc := do.MustInvoke[*OpenAIProviderMapService](i)
	trackerSvc := do.MustInvoke[*HealthTrackerService](i)

	svc := &OpenAIProviderInfoService{
		infos:       atomic.Pointer[[]router.ProviderInfo]{},
		cfgSvc:      cfgSvc,
		providerSvc: providerSvc,
		trackerSvc:  trackerSvc,
	}

	svc.RebuildFrom(cfgSvc.Get())
	svc.StartWatching()

	return svc, nil
}

// OpenAIHandlerService wraps the OpenAI HTTP handler.
type OpenAIHandlerService struct {
	Handler http.Handler
}

// NewOpenAIHandler creates the OpenAI HTTP handler with all middleware.
func NewOpenAIHandler(i do.Injector) (*OpenAIHandlerService, error) {
	cfgSvc := do.MustInvoke[*ConfigService](i)
	providerSvc := do.MustInvoke[*OpenAIProviderMapService](i)
	poolMapSvc := do.MustInvoke[*OpenAIKeyPoolMapService](i)
	routerSvc := do.MustInvoke[*RouterService](i)
	providerInfoSvc := do.MustInvoke[*OpenAIProviderInfoService](i)
	concurrencySvc := do.MustInvoke[*ConcurrencyService](i)

	cfg := cfgSvc.Get()
	if cfg == nil {
		return nil, fmt.Errorf("openai handler: config is nil")
	}

	liveRouter := router.NewLiveRouter(routerSvc.GetRouterAsFunc())

	mux := http.NewServeMux()
	openaiOpts := &proxy.OpenAIRoutesOptions{
		ProviderRouter:     liveRouter,
		ConfigProvider:     cfgSvc,
		ProviderInfosFunc:  providerInfoSvc.Get,
		GetProviderPools:   poolMapSvc.GetPools,
		GetProviderKeys:    poolMapSvc.GetKeys,
		GetAllProviders:    providerSvc.GetAllProviders,
		ConcurrencyLimiter: concurrencySvc.Limiter,
		DebugOptions:       cfg.Logging.DebugOptions,
	}
	err := proxy.SetupOpenAIRoutes(mux, openaiOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to setup openai handler: %w", err)
	}

	responsesOpts := &proxy.OpenAIRoutesOptions{
		ProviderRouter:     liveRouter,
		ConfigProvider:     cfgSvc,
		ProviderInfosFunc:  providerInfoSvc.Get,
		GetProviderPools:   poolMapSvc.GetPools,
		GetProviderKeys:    poolMapSvc.GetKeys,
		GetAllProviders:    providerSvc.GetAllProviders,
		ConcurrencyLimiter: concurrencySvc.Limiter,
		DebugOptions:       cfg.Logging.DebugOptions,
	}
	if err := proxy.SetupResponsesRoutes(mux, responsesOpts); err != nil {
		return nil, fmt.Errorf("failed to setup responses handler: %w", err)
	}

	return &OpenAIHandlerService{Handler: mux}, nil
}
