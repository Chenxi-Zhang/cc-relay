package di

import (
	"context"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/samber/do/v2"

	"github.com/omarluq/cc-relay/internal/proxy"
)

// ServerService wraps the HTTP server.
type ServerService struct {
	Server *proxy.Server
}

// NewHTTPServer creates the HTTP server with both Anthropic and OpenAI routes.
func NewHTTPServer(i do.Injector) (*ServerService, error) {
	cfgSvc := do.MustInvoke[*ConfigService](i)
	handlerSvc := do.MustInvoke[*HandlerService](i)

	rootMux := http.NewServeMux()
	rootMux.Handle("/", handlerSvc.Handler)

	if len(cfgSvc.Config.OpenAIProviders) > 0 {
		openaiSvc := do.MustInvoke[*OpenAIHandlerService](i)
		rootMux.Handle("/openai/", openaiSvc.Handler)
		log.Info().Int("count", len(cfgSvc.Config.OpenAIProviders)).Msg("openai providers mounted")
	}

	enableHTTP2 := cfgSvc.Config.Server.EnableHTTP2

	server := proxy.NewServer(cfgSvc.Config.Server.Listen, rootMux, enableHTTP2)

	return &ServerService{Server: server}, nil
}

// Shutdown implements do.Shutdowner for graceful server shutdown.
func (s *ServerService) Shutdown() error {
	if s.Server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return s.Server.Shutdown(ctx)
	}
	return nil
}
