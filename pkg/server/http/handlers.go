package http

import (
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/pki"
	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/federation"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
)

func (s *Server) registerRoutes(mux *stdhttp.ServeMux) {
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /v1/mesh/ca", s.handleMeshCA) // Public: download mesh CA
	mux.HandleFunc("POST /v1/mesh/join", s.handleMeshJoin)
	mux.HandleFunc("POST /v1/mesh/heartbeat", s.handleMeshHeartbeat)
	mux.HandleFunc("GET /v1/mesh/nodes", s.handleMeshNodes)
	mux.HandleFunc("GET /v1/mesh/radar", s.handleMeshRadar)
	mux.HandleFunc("POST /v1/mesh/peers/register", s.handleMeshPeerRegister)
	mux.HandleFunc("POST /v1/mesh/peers", s.handleMeshPeerRegister)
	mux.HandleFunc("GET /v1/mesh/peers", s.handleMeshPeersList)
	mux.HandleFunc("DELETE /v1/mesh/peers/{id}", s.handleMeshPeerDelete)
	mux.HandleFunc("POST /v1/mesh/peers/{id}/sync", s.handleMeshPeerSync)
	mux.HandleFunc("GET /v1/mesh/territory", s.handleMeshTerritory)
	mux.HandleFunc("POST /v1/tasks", s.handleCreateTask)
	mux.HandleFunc("GET /v1/tasks/{id}", s.handleGetTask)
	mux.HandleFunc("GET /v1/tasks/{id}/events", s.handleTaskEvents)
	mux.HandleFunc("POST /v1/tasks/{id}/reply", s.handleTaskReply)
	mux.HandleFunc("POST /v1/tasks/{id}/cancel", s.handleTaskCancel)
	mux.HandleFunc("GET /v1/workspace/tree", s.handleWorkspaceTree)
	mux.HandleFunc("GET /v1/workspace/file", s.handleWorkspaceFile)
	mux.HandleFunc("POST /v1/certs/enroll", s.handleCertsEnroll)
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
	tlsStatus := "disabled"
	if s.TLSEnabled() {
		tlsStatus = "enabled"
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{
		"status":         "ok",
		"uptime_seconds": uptime,
		"version":        "v1",
		"tls":            tlsStatus,
	})
}

// handleMeshCA serves the mesh CA certificate for nodes to download and trust.
// This endpoint is public (no auth required) because the CA is meant to be shared.
func (s *Server) handleMeshCA(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	caFile := s.MeshCAPemFile()
	if caFile == "" {
		writeJSON(w, stdhttp.StatusServiceUnavailable, map[string]string{
			"error": "TLS not configured, no CA available",
		})
		return
	}

	caData, err := os.ReadFile(caFile)
	if err != nil {
		writeJSON(w, stdhttp.StatusInternalServerError, map[string]string{
			"error": "failed to read CA certificate",
		})
		return
	}

	// Set headers for download
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="gentle-mesh-ca.pem"`))
	w.Header().Set("Cache-Control", "public, max-age=86400") // Cache for 24 hours
	w.WriteHeader(stdhttp.StatusOK)
	w.Write(caData)
}

// EnrollmentRequest represents a certificate enrollment request.
type EnrollmentRequest struct {
	Token  string `json:"token"`
	CSR    string `json:"csr"`     // PEM-encoded CSR
	NodeID string `json:"node_id"` // Requested node identifier
}

// EnrollmentResponse represents a certificate enrollment response.
type EnrollmentResponse struct {
	CertPEM string `json:"cert_pem"` // PEM-encoded signed certificate
	NodeID  string `json:"node_id"`
}

// handleCertsEnroll handles certificate enrollment via CSR and token.
// This endpoint allows automatic certificate issuance without admin intervention.
func (s *Server) handleCertsEnroll(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if !s.TLSEnabled() {
		writeJSON(w, stdhttp.StatusServiceUnavailable, map[string]string{
			"error": "TLS is not enabled on this coordinator",
		})
		return
	}

	if s.tokenStore == nil || s.meshCA == nil {
		writeJSON(w, stdhttp.StatusServiceUnavailable, map[string]string{
			"error": "Enrollment is not configured on this coordinator",
		})
		return
	}

	r.Body = stdhttp.MaxBytesReader(w, r.Body, 64<<10)

	var req EnrollmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid request: %v", err)})
		return
	}

	if req.Token == "" {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": "token is required"})
		return
	}

	if req.CSR == "" {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": "csr is required"})
		return
	}

	// Validate token
	_, err := s.tokenStore.UseToken(r.Context(), req.Token)
	if err != nil {
		if err.Error() == "token not found" {
			writeJSON(w, stdhttp.StatusUnauthorized, map[string]string{"error": "invalid token"})
			return
		}
		if err.Error() == "token expired" {
			writeJSON(w, stdhttp.StatusUnauthorized, map[string]string{"error": "token expired"})
			return
		}
		if err.Error() == "token max uses exceeded" {
			writeJSON(w, stdhttp.StatusUnauthorized, map[string]string{"error": "token already used"})
			return
		}
		writeJSON(w, stdhttp.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("token validation failed: %v", err)})
		return
	}

	// Use provided NodeID or extract from CSR
	nodeID := req.NodeID
	if nodeID == "" {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": "node_id is required"})
		return
	}

	// Sign CSR with CA
	cert, err := s.meshCA.SignCSR(req.CSR, nodeID, 365*24*time.Hour)
	if err != nil {
		writeJSON(w, stdhttp.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to sign certificate: %v", err)})
		return
	}

	// Convert to PEM
	certPEM, err := pki.CertificateToPEM(cert)
	if err != nil {
		writeJSON(w, stdhttp.StatusInternalServerError, map[string]string{"error": fmt.Sprintf("failed to encode certificate: %v", err)})
		return
	}

	writeJSON(w, stdhttp.StatusOK, EnrollmentResponse{
		CertPEM: certPEM,
		NodeID:  nodeID,
	})
}

func (s *Server) handleMeshJoin(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	r.Body = stdhttp.MaxBytesReader(w, r.Body, 64<<10)
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
	r.Body = stdhttp.MaxBytesReader(w, r.Body, 64<<10)
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

func (s *Server) handleMeshRadar(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	territories := s.taskManager.ActiveTerritories()
	if territories == nil {
		territories = []protocol.ActiveTerritory{}
	}
	report := protocol.RadarReport{
		ClusterName:  "gentle-mesh",
		Timestamp:    time.Now().Unix(),
		ActiveAgents: territories,
	}
	writeJSON(w, stdhttp.StatusOK, report)
}

func (s *Server) handleMeshPeerRegister(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	r.Body = stdhttp.MaxBytesReader(w, r.Body, 64<<10)
	var req protocol.PeerRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid request payload: %v", err)})
		return
	}

	peerInfo, err := s.territoryManager.RegisterPeer(req)
	if err != nil {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, stdhttp.StatusOK, peerInfo)
}

func (s *Server) handleMeshPeersList(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	peers := s.territoryManager.ListPeers()
	if peers == nil {
		peers = []protocol.PeerInfo{}
	}
	writeJSON(w, stdhttp.StatusOK, map[string]any{"peers": peers})
}

func (s *Server) handleMeshPeerDelete(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	if err := s.territoryManager.DeregisterPeer(id); err != nil {
		if errors.Is(err, federation.ErrPeerNotFound) {
			writeJSON(w, stdhttp.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, stdhttp.StatusOK, map[string]string{"status": "deregistered"})
}

func (s *Server) handleMeshPeerSync(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	id := r.PathValue("id")
	manifest, err := s.territoryManager.SyncPeer(r.Context(), id)
	if err != nil {
		if errors.Is(err, federation.ErrPeerNotFound) {
			writeJSON(w, stdhttp.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, stdhttp.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, stdhttp.StatusOK, manifest)
}

func (s *Server) handleMeshTerritory(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	writeJSON(w, stdhttp.StatusOK, s.territoryManager.LocalManifest())
}

func (s *Server) handleCreateTask(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	r.Body = stdhttp.MaxBytesReader(w, r.Body, 1<<20)
	var req protocol.TaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid request payload: %v", err)})
		return
	}

	// Open Pi Viewer submits a session-scoped prompt without an explicit agent or
	// task field. Alias "prompt" into "task" and default the agent so those
	// clients can dispatch work with the same contract as Gentle Mesh clients.
	if req.Task == "" && req.Prompt != "" {
		req.Task = req.Prompt
	}
	if req.Agent == "" && req.Task != "" {
		req.Agent = "worker"
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
					SessionID: existingTask.Request.SessionID,
					Status:    st.Status,
					EventsURL: "/v1/tasks/" + existingTask.TaskID + "/events",
					CreatedAt: st.CreatedAt,
				})
				return
			}
		}
	}

	if s.territoryMode == protocol.TerritoryModeStrict && req.GitRepo != "" {
		targetTerritory := protocol.ActiveTerritory{
			Repo:         req.GitRepo,
			Branch:       req.GitBranch,
			Domain:       req.Domain,
			EditSurfaces: req.EditSurfaces,
			BlastRadius:  req.BlastRadius,
			TaskSummary:  req.Task,
		}
		if conflict := s.territoryManager.FindConflict(targetTerritory); conflict != nil {
			errStr := conflict.Message
			if conflict.ConflictType == protocol.ConflictBranchLocked && !strings.Contains(errStr, "branch is locked") {
				errStr = "branch is locked by another task: " + errStr
			}
			writeJSON(w, stdhttp.StatusConflict, map[string]any{
				"error":    errStr,
				"conflict": conflict,
				"task_id":  conflict.ExistingTerritory.TaskID,
			})
			return
		}
		if req.GitBranch != "" {
			if _, _, locked := s.registry.Locks().GetLock(req.GitRepo, req.GitBranch); locked {
				writeJSON(w, stdhttp.StatusConflict, map[string]string{
					"error": "branch is locked by another task",
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
					SessionID: existingTask.Request.SessionID,
					Status:    st.Status,
					EventsURL: "/v1/tasks/" + existingTask.TaskID + "/events",
					CreatedAt: st.CreatedAt,
				})
				return
			}
		}
	}

	// The scheduler owns dispatch, exclusive branch locking, runner tracking, panic
	// recovery, and FIFO queue draining. In queue mode a territory conflict enqueues
	// the task; in strict mode it is rejected here.
	if _, err := s.scheduler.Schedule(t); err != nil {
		_ = s.taskManager.CancelTask(t.TaskID, err.Error())
		if errors.Is(err, registry.ErrBranchLocked) {
			writeJSON(w, stdhttp.StatusConflict, map[string]string{
				"error":   "branch is locked by another task",
				"task_id": t.TaskID,
			})
			return
		}
		writeJSON(w, stdhttp.StatusConflict, map[string]any{
			"error":   err.Error(),
			"task_id": t.TaskID,
		})
		return
	}

	writeJSON(w, stdhttp.StatusCreated, protocol.TaskResponse{
		TaskID:    t.TaskID,
		SessionID: t.Request.SessionID,
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

	heartbeatInterval := s.config.SSEHeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = 15 * time.Second
	}
	heartbeatTicker := time.NewTicker(heartbeatInterval)
	defer heartbeatTicker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeatTicker.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
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
	r.Body = stdhttp.MaxBytesReader(w, r.Body, 64<<10)
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

	if s.scheduler.Cancel(id, reason) {
		writeJSON(w, stdhttp.StatusOK, map[string]string{
			"task_id": id,
			"status":  string(protocol.TaskStatusCanceled),
		})
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
	s.scheduler.Drain()

	writeJSON(w, stdhttp.StatusOK, map[string]string{
		"task_id": id,
		"status":  string(protocol.TaskStatusCanceled),
	})
}

// workspaceMaxFileSize bounds the payload returned by the workspace file endpoint.
const workspaceMaxFileSize = 5 * 1024 * 1024

// WorkspaceFileEntry describes a single directory entry relative to the workspace root.
type WorkspaceFileEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"`
}

// WorkspaceTreeResponse is the payload returned by GET /v1/workspace/tree.
type WorkspaceTreeResponse struct {
	Root    string               `json:"root"`
	Path    string               `json:"path"`
	Entries []WorkspaceFileEntry `json:"entries"`
}

// WorkspaceFileResponse is the payload returned by GET /v1/workspace/file.
type WorkspaceFileResponse struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"`
	Content string `json:"content"`
}

// resolveWorkspacePath resolves a client-supplied path inside the workspace root.
// It returns ok=false when the path escapes the root so callers can answer 403.
// The returned rel is the cleaned path relative to the workspace root.
func (s *Server) resolveWorkspacePath(subPath string) (fullPath, rel string, ok bool) {
	cleaned := filepath.Clean(subPath)
	if cleaned == "." {
		return s.workspaceRoot, ".", true
	}
	// Reject explicit traversal before normalization so requests such as
	// "../../etc" can never be silently folded back into the workspace root.
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", "", false
	}

	fullPath = filepath.Join(s.workspaceRoot, filepath.Clean("/"+subPath))
	rel, err := filepath.Rel(s.workspaceRoot, fullPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", false
	}
	return fullPath, rel, true
}

func (s *Server) handleWorkspaceTree(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	fullPath, relDir, ok := s.resolveWorkspacePath(r.URL.Query().Get("path"))
	if !ok {
		writeJSON(w, stdhttp.StatusForbidden, map[string]string{"error": "access denied: path outside workspace root"})
		return
	}

	fi, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, stdhttp.StatusNotFound, map[string]string{"error": "path not found"})
			return
		}
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !fi.IsDir() {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": "path is not a directory"})
		return
	}

	dirEntries, err := os.ReadDir(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, stdhttp.StatusNotFound, map[string]string{"error": "path not found"})
			return
		}
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	entries := make([]WorkspaceFileEntry, 0, len(dirEntries))
	for _, de := range dirEntries {
		info, err := de.Info()
		if err != nil {
			continue
		}
		entries = append(entries, WorkspaceFileEntry{
			Name:    de.Name(),
			Path:    filepath.Join(relDir, de.Name()),
			IsDir:   de.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime().Unix(),
		})
	}

	// Directories first (alphabetical), then files (alphabetical).
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return entries[i].Name < entries[j].Name
	})

	writeJSON(w, stdhttp.StatusOK, WorkspaceTreeResponse{
		Root:    s.workspaceRoot,
		Path:    relDir,
		Entries: entries,
	})
}

func (s *Server) handleWorkspaceFile(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	subPath := r.URL.Query().Get("path")
	if subPath == "" {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": "path query parameter is required"})
		return
	}

	fullPath, relPath, ok := s.resolveWorkspacePath(subPath)
	if !ok {
		writeJSON(w, stdhttp.StatusForbidden, map[string]string{"error": "access denied: path outside workspace root"})
		return
	}

	fi, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSON(w, stdhttp.StatusNotFound, map[string]string{"error": "file not found"})
			return
		}
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if fi.IsDir() {
		writeJSON(w, stdhttp.StatusBadRequest, map[string]string{"error": "cannot read directory as file"})
		return
	}
	if fi.Size() > workspaceMaxFileSize {
		writeJSON(w, stdhttp.StatusRequestEntityTooLarge, map[string]string{"error": "file exceeds 5MB maximum size"})
		return
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		writeJSON(w, stdhttp.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, stdhttp.StatusOK, WorkspaceFileResponse{
		Path:    relPath,
		Size:    fi.Size(),
		ModTime: fi.ModTime().Unix(),
		Content: string(content),
	})
}
