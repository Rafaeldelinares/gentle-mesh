package worker

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
)

// ServerConfig defines configuration parameters for the gentle-mesh worker HTTP daemon.
type ServerConfig struct {
	Addr        string
	Runner      runner.Runner
	BearerToken string
}

// Server executes delegated tasks locally on a mesh worker node and streams events via SSE.
type Server struct {
	config      ServerConfig
	httpServer  *http.Server
	runner      runner.Runner
	activeTasks atomic.Int32
	queriesMu   sync.Mutex
	queries     map[string]chan string
	listener    net.Listener
}

// NewServer initializes a new worker Server with route handlers and authentication.
func NewServer(cfg ServerConfig) *Server {
	if cfg.Runner == nil {
		cfg.Runner = runner.NewSimulatedRunner(runner.SimulatedOptions{})
	}

	s := &Server{
		config:  cfg,
		runner:  cfg.Runner,
		queries: make(map[string]chan string),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/execute", s.handleExecute)
	mux.HandleFunc("POST /v1/reply", s.handleReply)
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	var handler http.Handler = mux
	if cfg.BearerToken != "" {
		expectedAuth := "Bearer " + cfg.BearerToken
		handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/healthz" || r.URL.Path == "/healthz/" {
				mux.ServeHTTP(w, r)
				return
			}
			auth := r.Header.Get("Authorization")
			if subtle.ConstantTimeCompare([]byte(auth), []byte(expectedAuth)) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "unauthorized"})
				return
			}
			mux.ServeHTTP(w, r)
		})
	}

	s.httpServer = &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	return s
}

// Handler returns the root HTTP handler for the worker server.
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// Listen initializes the TCP listener on the configured address if not already listening.
func (s *Server) Listen() error {
	s.queriesMu.Lock()
	defer s.queriesMu.Unlock()
	if s.listener != nil {
		return nil
	}
	addr := s.config.Addr
	if addr == "" {
		addr = ":8081"
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.listener = ln
	return nil
}

// Start begins serving HTTP traffic on the listener.
func (s *Server) Start() error {
	if err := s.Listen(); err != nil {
		return err
	}
	if err := s.httpServer.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully terminates the HTTP server and underlying listener.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.httpServer.Shutdown(ctx)
	s.queriesMu.Lock()
	if s.listener != nil {
		_ = s.listener.Close()
	}
	s.queriesMu.Unlock()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// ActiveTasks returns the current number of concurrently running tasks.
func (s *Server) ActiveTasks() int {
	return int(s.activeTasks.Load())
}

// Addr returns the bound network address of the worker server.
func (s *Server) Addr() string {
	s.queriesMu.Lock()
	defer s.queriesMu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.config.Addr
}

func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req protocol.TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid task request: " + err.Error()})
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	s.activeTasks.Add(1)
	defer s.activeTasks.Add(-1)

	taskID := req.IdempotencyKey
	if taskID == "" {
		taskID = fmt.Sprintf("worker-task-%d", time.Now().UnixNano())
	}

	sink := newWorkerEventSink(r.Context(), taskID, s, w, flusher)
	defer sink.cleanup()

	log.Printf("[worker] executing task %q (agent: %q, tags: %v)", taskID, req.Agent, req.Tags)
	err := s.runner.Run(r.Context(), req, sink)
	if err != nil {
		log.Printf("[worker] task %q execution error: %v", taskID, err)
	} else {
		log.Printf("[worker] task %q completed", taskID)
	}
}

func (s *Server) handleReply(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req protocol.TaskReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid reply request: " + err.Error()})
		return
	}

	if req.QueryID == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "query_id is required"})
		return
	}

	s.queriesMu.Lock()
	ch, ok := s.queries[req.QueryID]
	if ok {
		delete(s.queries, req.QueryID)
	}
	s.queriesMu.Unlock()

	if !ok {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "query not found"})
		return
	}

	select {
	case ch <- req.Answer:
		log.Printf("[worker] query %q answered", req.QueryID)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "delivered"})
	default:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "query channel unavailable"})
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":       "ok",
		"active_tasks": s.ActiveTasks(),
	})
}

type workerEventSink struct {
	ctx       context.Context
	taskID    string
	server    *Server
	w         http.ResponseWriter
	flusher   http.Flusher
	writeMu   sync.Mutex
	eventSeq  atomic.Int64
	queriesMu sync.Mutex
	queries   []string
}

func newWorkerEventSink(ctx context.Context, taskID string, s *Server, w http.ResponseWriter, flusher http.Flusher) *workerEventSink {
	return &workerEventSink{
		ctx:     ctx,
		taskID:  taskID,
		server:  s,
		w:       w,
		flusher: flusher,
	}
}

func (sink *workerEventSink) EmitEvent(eventType protocol.EventType, payload any) (protocol.Event, error) {
	if err := sink.ctx.Err(); err != nil {
		return protocol.Event{}, err
	}

	seq := sink.eventSeq.Add(1)
	evt, err := protocol.NewEvent(seq, sink.taskID, eventType, payload)
	if err != nil {
		return protocol.Event{}, err
	}

	sseData := evt.FormatSSE()

	sink.writeMu.Lock()
	defer sink.writeMu.Unlock()

	if _, err := sink.w.Write(sseData); err != nil {
		return *evt, err
	}
	sink.flusher.Flush()
	return *evt, nil
}

func (sink *workerEventSink) Context() context.Context {
	return sink.ctx
}

func (sink *workerEventSink) RegisterQuery(queryID string) <-chan string {
	ch := make(chan string, 1)

	sink.queriesMu.Lock()
	sink.queries = append(sink.queries, queryID)
	sink.queriesMu.Unlock()

	sink.server.queriesMu.Lock()
	sink.server.queries[queryID] = ch
	sink.server.queriesMu.Unlock()

	return ch
}

func (sink *workerEventSink) cleanup() {
	sink.queriesMu.Lock()
	defer sink.queriesMu.Unlock()

	sink.server.queriesMu.Lock()
	defer sink.server.queriesMu.Unlock()

	for _, qID := range sink.queries {
		delete(sink.server.queries, qID)
	}
}
