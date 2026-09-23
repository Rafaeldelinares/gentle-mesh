package http

import (
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"strconv"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
)

func (s *Server) registerRoutes(mux *stdhttp.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("POST /v1/mesh/join", s.handleMeshJoin)
	mux.HandleFunc("POST /v1/mesh/heartbeat", s.handleMeshHeartbeat)
	mux.HandleFunc("GET /v1/mesh/nodes", s.handleMeshNodes)
	mux.HandleFunc("POST /v1/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /v1/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("GET /v1/tasks/{id}/events", s.handleTaskEvents)
	mux.HandleFunc("POST /v1/tasks/{id}/reply", s.handleTaskReply)
	mux.HandleFunc("POST /v1/tasks/{id}/cancel", s.handleTaskCancel)
}

func writeJSON(w stdhttp.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if data != nil {
		_ = json.NewEncoder(w).Encode(data)
	}
}

func (s *Server) handleHealthz(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	uptime := int64(time.Since(s.startTime).Seconds())
	writeJSON(w, stdhttp.StatusOK, map[string]any{
		"status":         "ok",
		"uptime_seconds": uptime,
		"version":        "v1",
	})
}

func (s *Server) handleMeshJoin(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req protocol.NodeJoinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid request payload: %v", err)})
		return
	}

	nodeInfo, err := s.registry.RegisterNode(req)
	if err != nil {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, stdhttp.StatusOK, nodeInfo)
}

func (s *Server) handleMeshHeartbeat(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req protocol.NodeHeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid request payload: %v", err)})
		return
	}

	if err := s.registry.Heartbeat(req); err != nil {
		if errors.Is(err, registry.ErrNodeNotFound) {
			writeJSON(w, stdhttp.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, stdhttp.StatusOK, map[string]string{"status": "acknowledged"})
}

func (s *Server) handleMeshNodes(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	nodes := s.registry.ListNodes()
	if nodes == nil {
		nodes = []*protocol.NodeInfo{}
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"nodes": nodes})
}

func (s *Server) handleCreateTask(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	var req protocol.TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid request payload: %v", err)})
		return
	}

	if req.Agent == "" || req.Task == "" {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": "agent and task must not be empty"})
		return
	}

	if req.IdempotencyKey != "" {
		if existingID, exists := s.registry.Idempotency().Get(req.IdempotencyKey); exists && existingID != "" {
			if existingTask, found := s.taskManager.GetTask(existingID); found {
				st := existingTask.Snapshot()
				writeJSON(w, stdhttp.StatusOK, protocol.TaskResponse{
					TaskID:    existingTask.TaskID,
					Status:    st.Status,
					EventsURL: "/v1/tasks/" + existingTask.TaskID + "/events",
					CreatedAt: st.CreatedAt,
				})
				return
			}
		}
	}

	t, err := s.taskManager.CreateTask(req)
	if err != nil {
		writeJSON(w, stdhttp.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if req.IdempotencyKey != "" {
		existingID, isDup := s.registry.Idempotency().RecordOrGet(req.IdempotencyKey, t.TaskID)
		if isDup && existingID != "" && existingID != t.TaskID {
			if existingTask, found := s.taskManager.GetTask(existingID); found {
				_ = s.taskManager.CancelTask(t.TaskID, "duplicate task by idempotency key")
				st := existingTask.Snapshot()
				writeJSON(w, stdhttp.StatusOK, protocol.TaskResponse{
					TaskID:    existingTask.TaskID,
					Status:    st.Status,
					EventsURL: "/v1/tasks/" + existingTask.TaskID + "/events",
					CreatedAt: st.CreatedAt,
				})
				return
			}
		}
	}

	if req.GitRepo != "" && req.GitBranch != "" {
		err := s.registry.Locks().ClaimLock(req.GitRepo, req.GitBranch, t.TaskID)
		if errors.Is(err, registry.ErrBranchLocked) {
			_ = s.taskManager.CancelTask(t.TaskID, "branch is locked by another task")
			writeJSON(w, stdhttp.StatusConflict, map[string]string{
				"error":   "branch is locked by another task",
				"task_id": t.TaskID,
			})
			return
		}
		if err != nil {
			_ = s.taskManager.CancelTask(t.TaskID, err.Error())
			writeJSON(w, stdhttp.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}

	go func(mt *task.ManagedTask, r runner.Runner) {
		defer func() {
			if mt.Request.GitRepo != "" && mt.Request.GitBranch != "" {
				_ = s.registry.Locks().ReleaseLock(mt.Request.GitRepo, mt.Request.GitBranch, mt.TaskID)
			}
		}()
		_ = r.Run(mt.Context(), mt.Request, mt)
	}(t, s.runner)

	writeJSON(w, stdhttp.StatusCreated, protocol.TaskResponse{
		TaskID:    t.TaskID,
		Status:    t.CurrentStatus(),
		EventsURL: "/v1/tasks/" + t.TaskID + "/events",
		CreatedAt: t.CreatedAt,
	})
}

func (s *Server) handleGetTask(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": "task id is required"})
		return
	}

	t, exists := s.taskManager.GetTask(id)
	if !exists {
		writeJSON(w, stdhttp.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}

	writeJSON(w, stdhttp.StatusOK, t.Snapshot())
}

func (s *Server) handleTaskEvents(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": "task id is required"})
		return
	}

	var sinceID int64
	if lastEventID := r.Header.Get("Last-Event-ID"); lastEventID != "" {
		if parsed, err := strconv.ParseInt(lastEventID, 10, 64); err == nil {
			sinceID = parsed
		}
	} else if sinceParam := r.URL.Query().Get("since_id"); sinceParam != "" {
		if parsed, err := strconv.ParseInt(sinceParam, 10, 64); err == nil {
			sinceID = parsed
		}
	}

	eventsChan, unsubscribe, err := s.taskManager.Subscribe(id, sinceID)
	if err != nil {
		if errors.Is(err, task.ErrTaskNotFound) {
			writeJSON(w, stdhttp.StatusNotFound, map[string]string{"error": "task not found"})
			return
		}
		writeJSON(w, stdhttp.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	defer unsubscribe()

	flusher, ok := w.(stdhttp.Flusher)
	if !ok {
		writeJSON(w, stdhttp.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(stdhttp.StatusOK)
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, ok := <-eventsChan:
			if !ok {
				return
			}
			sseBytes := evt.FormatSSE()
			if len(sseBytes) > 0 {
				_, _ = w.Write(sseBytes)
				flusher.Flush()
			}
		}
	}
}

func (s *Server) handleTaskReply(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": "task id is required"})
		return
	}

	var reply protocol.TaskReplyRequest
	if err := json.NewDecoder(r.Body).Decode(&reply); err != nil {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid request payload: %v", err)})
		return
	}

	if err := s.taskManager.SubmitReply(id, reply); err != nil {
		if errors.Is(err, task.ErrTaskNotFound) {
			writeJSON(w, stdhttp.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, stdhttp.StatusOK, map[string]string{"status": "accepted"})
}

type cancelPayload struct {
	Reason string `json:"reason,omitempty"`
}

func (s *Server) handleTaskCancel(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": "task id is required"})
		return
	}

	var req cancelPayload
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}

	reason := req.Reason
	if reason == "" {
		reason = "canceled by user"
	}

	t, exists := s.taskManager.GetTask(id)
	if !exists {
		writeJSON(w, stdhttp.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}

	if err := s.taskManager.CancelTask(id, reason); err != nil {
		if errors.Is(err, task.ErrTaskAlreadyFinished) {
			writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, stdhttp.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if t.Request.GitRepo != "" && t.Request.GitBranch != "" {
		_ = s.registry.Locks().ReleaseLock(t.Request.GitRepo, t.Request.GitBranch, id)
	}

	writeJSON(w, stdhttp.StatusOK, map[string]string{"status": "canceled"})
}
