// Package ccrelay provides the public API for embedding cc-relay in other applications.
// It exposes the server lifecycle (create, start, stop, reload) without requiring
// direct access to internal packages.
package ccrelay

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/omarluq/cc-relay/internal/di"
	"github.com/omarluq/cc-relay/internal/proxy"
)

// ServerStatus represents the current state of the relay server.
type ServerStatus string

const (
	StatusStopped ServerStatus = "stopped"
	StatusRunning ServerStatus = "running"
	StatusError   ServerStatus = "error"
)

// Server wraps the cc-relay proxy server lifecycle for embedded use.
// It manages the DI container, HTTP server, and graceful shutdown.
type Server struct {
	container   *di.Container
	server      *proxy.Server
	listenAddr  string
	cancelWatch context.CancelFunc
	cancelStop  context.CancelFunc

	mu     sync.RWMutex
	status ServerStatus
	err    error
}

// NewServer creates a new server from the given config file path.
// The server is initialized but not started. Call Start() to begin serving.
func NewServer(configPath string) (*Server, error) {
	container, err := di.NewContainer(configPath)
	if err != nil {
		return nil, err
	}

	cfgSvc := di.MustInvoke[*di.ConfigService](container)
	cfg := cfgSvc.Get()

	// Setup logging from config
	logger, err := proxy.NewLogger(cfg.Logging)
	if err != nil {
		return nil, err
	}
	log.Logger = logger
	zerolog.DefaultContextLogger = &logger

	serverSvc, err := di.Invoke[*di.ServerService](container)
	if err != nil {
		return nil, err
	}

	return &Server{
		container:  container,
		server:     serverSvc.Server,
		listenAddr: cfg.Server.Listen,
		status:     StatusStopped,
	}, nil
}

// Start begins serving requests in a background goroutine (non-blocking).
// Returns an error if the server is already running.
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.status == StatusRunning {
		return errors.New("server is already running")
	}

	// Start health checker
	checkerSvc := di.MustInvoke[*di.CheckerService](s.container)
	checkerSvc.Start()

	// Start config file watcher
	watchCtx, watchCancel := context.WithCancel(context.Background())
	s.cancelWatch = watchCancel

	cfgSvc := di.MustInvoke[*di.ConfigService](s.container)
	cfgSvc.StartWatching(watchCtx)

	s.status = StatusRunning

	go func() {
		log.Info().Str("listen", s.listenAddr).Msg("starting cc-relay")

		if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			s.status = StatusError
			s.err = err
			s.mu.Unlock()
			log.Error().Err(err).Msg("server error")
		}

		s.mu.Lock()
		if s.status == StatusRunning {
			s.status = StatusStopped
		}
		s.mu.Unlock()
	}()

	return nil
}

// Stop gracefully shuts down the server.
// It waits up to 30 seconds for connections to drain.
func (s *Server) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.status != StatusRunning {
		return errors.New("server is not running")
	}

	// Cancel config watcher
	if s.cancelWatch != nil {
		s.cancelWatch()
	}

	// Shutdown HTTP server
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := s.server.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("server shutdown error")
	}

	// Shutdown DI container services
	if err := s.container.ShutdownWithContext(ctx); err != nil {
		log.Error().Err(err).Msg("service shutdown error")
	}

	s.status = StatusStopped
	log.Info().Msg("server stopped")

	return nil
}

// Status returns the current server status.
func (s *Server) Status() ServerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// ListenAddr returns the address the server is configured to listen on.
func (s *Server) ListenAddr() string {
	return s.listenAddr
}

// Error returns the last error if the server status is StatusError.
func (s *Server) Error() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}
