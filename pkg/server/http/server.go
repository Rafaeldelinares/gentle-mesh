package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
)

// ServerConfig defines configuration parameters for the mesh HTTP coordinator server.
type ServerConfig struct {
	Addr                 string
	TasksDir             string
	HeartbeatTimeout     time.Duration
	TaskTTL              time.Duration
	BearerToken          string
	Runner               runner.Runner
	Registry             *registry.Registry
	TaskManager          *task.TaskManager
	SSEHeartbeatInterval time.Duration
}

// Server provides the HTTP REST and SSE coordinator daemon for gentle-mesh.
type Server struct {
	config      ServerConfig
	httpServer  *stdhttp.Server
	registry    *registry.Registry
	taskManager *task.TaskManager
	runner      runner.Runner
	startTime   time.Time
}

// NewServer initializes a new Server with defaults for omitted configuration fields.
func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.HeartbeatTimeout <= 0 {
		cfg.HeartbeatTimeout = 30 * time.Second
	}
	if cfg.TaskTTL <= 0 {
		cfg.TaskTTL = 24 * time.Hour
	}
	if cfg.SSEHeartbeatInterval <= 0 {
		cfg.SSEHeartbeatInterval = 15 * time.Second
	}
	if cfg.Registry == nil {
		cfg.Registry = registry.NewRegistry(cfg.HeartbeatTimeout)
	}
	if cfg.TaskManager == nil {
		if cfg.TasksDir == "" {
			cfg.TasksDir = filepath.Join(os.TempDir(), fmt.Sprintf("gentle-mesh-tasks-%d", time.Now().UnixNano()))
		}
		tm, err := task.NewTaskManager(cfg.TasksDir, cfg.TaskTTL)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize task manager: %w", err)
		}
		cfg.TaskManager = tm
	}
	if cfg.Runner == nil {
		cfg.Runner = runner.NewSimulatedRunner(runner.SimulatedOptions{})
	}

	s := &Server{
		config:      cfg,
		registry:    cfg.Registry,
		taskManager: cfg.TaskManager,
		runner:      cfg.Runner,
		startTime:   time.Now(),
	}

	s.httpServer = &stdhttp.Server{
		Addr:              cfg.Addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	return s, nil
}

// Registry returns the attached node registry.
func (s *Server) Registry() *registry.Registry {
	return s.registry
}

// TaskManager returns the attached task lifecycle and log manager.
func (s *Server) TaskManager() *task.TaskManager {
	return s.taskManager
}

// Runner returns the configured task execution runner.
func (s *Server) Runner() runner.Runner {
	return s.runner
}

// Config returns a copy of the server configuration.
func (s *Server) Config() ServerConfig {
	return s.config
}

// Handler constructs and returns the HTTP handler with all endpoints and middleware applied.
func (s *Server) Handler() stdhttp.Handler {
	mux := stdhttp.NewServeMux()
	s.registerRoutes(mux)

	var h stdhttp.Handler = mux
	if s.config.BearerToken != "" {
		h = AuthMiddleware(s.config.BearerToken, h)
	}
	h = PanicRecoveryMiddleware(h)
	return h
}

// Start begins listening and serving HTTP requests on the configured address.
func (s *Server) Start() error {
	if s.httpServer == nil {
		s.httpServer = &stdhttp.Server{
			Addr:              s.config.Addr,
			Handler:           s.Handler(),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
	}
	if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully stops the HTTP server and releases task manager resources.
func (s *Server) Shutdown(ctx context.Context) error {
	var httpErr error
	if s.httpServer != nil {
		httpErr = s.httpServer.Shutdown(ctx)
	}
	tmErr := s.taskManager.Close()
	if httpErr != nil && !errors.Is(httpErr, stdhttp.ErrServerClosed) {
		return httpErr
	}
	return tmErr
}
