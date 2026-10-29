package joinlog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// HTTPHandler exposes the RFC-003 join log HTTP endpoints:
// - GET /v1/mesh/snapshot (J3)
// - GET /v1/mesh/events (J4)
type HTTPHandler struct {
	log       *Log
	projector *Projector
}

// NewHTTPHandler creates a new HTTPHandler backed by log and projector.
func NewHTTPHandler(l *Log, p *Projector) *HTTPHandler {
	return &HTTPHandler{
		log:       l,
		projector: p,
	}
}

// RegisterRoutes registers the /v1/mesh/snapshot and /v1/mesh/events endpoints on mux.
func (h *HTTPHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/mesh/snapshot", h.HandleSnapshot)
	mux.HandleFunc("GET /v1/mesh/events", h.HandleEvents)
}

// HandleSnapshot serves the verifiable dual-digest snapshot (RFC-003 J3).
func (h *HTTPHandler) HandleSnapshot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap, err := h.projector.CreateSnapshot(r.Context(), h.log)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create snapshot: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(snap)
}

// HandleEvents serves the SSE event stream with resume capability (RFC-003 J4).
func (h *HTTPHandler) HandleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// 1. Parse cursor from Last-Event-ID header or query parameters
	var sinceSeq int64 = 0
	var sinceHash string = ""

	lastEventID := r.Header.Get("Last-Event-ID")
	if lastEventID != "" {
		if parts := strings.SplitN(lastEventID, ":", 2); len(parts) == 2 {
			if parsed, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
				sinceSeq = parsed
				sinceHash = parts[1]
			}
		} else {
			if parsed, err := strconv.ParseInt(lastEventID, 10, 64); err == nil {
				sinceSeq = parsed
			}
		}
	} else {
		if qSeq := r.URL.Query().Get("since_seq"); qSeq != "" {
			if parsed, err := strconv.ParseInt(qSeq, 10, 64); err == nil {
				sinceSeq = parsed
			}
		}
		if qHash := r.URL.Query().Get("since_hash"); qHash != "" {
			sinceHash = qHash
		}
	}

	// 2. Validate chain continuity anchor (RFC-003 §4.1.1)
	// If sinceHash is supplied, verify that the event at sinceSeq matches sinceHash.
	// If it doesn't match, return 409 Conflict (Chain Divergence Detected).
	if sinceSeq > 0 && sinceHash != "" {
		actualEvt, err := h.log.EventBySeq(r.Context(), sinceSeq)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to verify anchor: %v", err), http.StatusInternalServerError)
			return
		}
		if actualEvt != nil && actualEvt.Hash != sinceHash {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error": fmt.Sprintf("chain divergence detected at seq %d: expected hash %s, got %s",
					sinceSeq, sinceHash, actualEvt.Hash),
			})
			return
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// 3. Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	// 4. Subscribe to events > sinceSeq
	eventsChan, unsubscribe, err := h.log.Subscribe(r.Context(), sinceSeq)
	if err != nil {
		return
	}
	defer unsubscribe()

	// 5. Stream loop with SSE heartbeat ping every 15s
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case evt, ok := <-eventsChan:
			if !ok {
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "id: %d:%s\nevent: %s\ndata: %s\n\n",
				evt.Seq, evt.Hash, evt.Type, string(data))
			flusher.Flush()
		}
	}
}
