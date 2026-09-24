package http

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/federation"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
)

// ServerConfig defines configuration parameters for the mesh HTTP coordinator server.
type ServerConfig struct {
	Addr                 string
	TasksDir             string
	DBPath               string
	Store                store.TaskStore
	HeartbeatTimeout     time.Duration
	TaskTTL              time.Duration
	BearerToken          string
	Runner               runner.Runner
	Registry             *registry.Registry
	TaskManager          *task.TaskManager
	SSEHeartbeatInterval time.Duration
	PeerID               string
	ClusterName          string
	TerritoryManager     *federation.TerritoryManager
	TerritoryMode        protocol.TerritoryMode
	WorkspaceRoot        string
}

// Server provides the HTTP REST and SSE coordinator daemon for gentle-mesh.
type Server struct {
	config           ServerConfig
	httpServer       *stdhttp.Server
	registry         *registry.Registry
	taskManager      *task.TaskManager
	runner           runner.Runner
	territoryManager *federation.TerritoryManager
	territoryMode    protocol.TerritoryMode
	scheduler        *TerritoryScheduler
	workspaceRoot    string
	startTime        time.Time
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
		var opts []task.TaskManagerOption
		var s store.TaskStore
		if cfg.Store != nil {
			opts = append(opts, task.WithStore(cfg.Store))
		} else if cfg.DBPath != "none" {
			dbPath := cfg.DBPath
			if dbPath == "" {
				dbPath = filepath.Join(cfg.TasksDir, "gentle-mesh.db")
			}
			var err error
			s, err = store.NewSQLiteStore(dbPath)
			if err != nil {
				return nil, fmt.Errorf("failed to initialize sqlite store: %w", err)
			}
			opts = append(opts, task.WithStore(s))
		}
		tm, err := task.NewTaskManager(cfg.TasksDir, cfg.TaskTTL, opts...)
		if err != nil {
			if s != nil {
				_ = s.Close()
			}
			return nil, fmt.Errorf("failed to initialize task manager: %w", err)
		}
		cfg.TaskManager = tm
	}
	if cfg.ClusterName == "" {
		cfg.ClusterName = "gentle-mesh"
	}
	if cfg.PeerID == "" {
		cfg.PeerID = cfg.ClusterName
	}
	if cfg.WorkspaceRoot == "" {
		cfg.WorkspaceRoot = "."
	}
	workspaceRoot, err := filepath.Abs(cfg.WorkspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve workspace root: %w", err)
	}
	if cfg.TerritoryManager == nil {
		cfg.TerritoryManager = federation.NewTerritoryManager(federation.ManagerConfig{
			PeerID:        cfg.PeerID,
			ClusterName:   cfg.ClusterName,
			LocalSource:   cfg.TaskManager.ActiveTerritories,
			RunningSource: cfg.TaskManager.RunningTerritories,
		})
	}
	if cfg.Runner == nil {
		localRunner := runner.NewSimulatedRunner(runner.SimulatedOptions{})
		cfg.Runner = runner.NewMeshRunner(runner.MeshRunnerOptions{
			Selector:       cfg.Registry,
			FallbackRunner: localRunner,
			Token:          cfg.BearerToken,
		})
	}

	mode := cfg.TerritoryMode
	if !mode.Valid() {
		mode = protocol.TerritoryModeQueue
	}
	scheduler := NewTerritoryScheduler(SchedulerConfig{
		Mode:             mode,
		TaskManager:      cfg.TaskManager,
		TerritoryManager: cfg.TerritoryManager,
		Registry:         cfg.Registry,
		Runner:           cfg.Runner,
	})

	s := &Server{
		config:           cfg,
		registry:         cfg.Registry,
		taskManager:      cfg.TaskManager,
		runner:           cfg.Runner,
		territoryManager: cfg.TerritoryManager,
		territoryMode:    mode,
		scheduler:        scheduler,
		workspaceRoot:    workspaceRoot,
		startTime:        time.Now(),
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

// TerritoryManager returns the attached territory federation manager.
func (s *Server) TerritoryManager() *federation.TerritoryManager {
	return s.territoryManager
}

// Runner returns the configured task execution runner.
func (s *Server) Runner() runner.Runner {
	return s.runner
}

// Scheduler returns the territory scheduler coordinating queueing and dispatch.
func (s *Server) Scheduler() *TerritoryScheduler {
	return s.scheduler
}

// TerritoryMode returns the normalized territory conflict policy in effect.
func (s *Server) TerritoryMode() protocol.TerritoryMode {
	return s.territoryMode
}

// WorkspaceRoot returns the absolute base directory exposed for remote workspace exploration.
func (s *Server) WorkspaceRoot() string {
	return s.workspaceRoot
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
	h = CORSMiddleware(h)
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
