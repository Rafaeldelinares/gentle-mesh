package client_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/client"
)

func TestBridge_GetState(t *testing.T) {
	bridge := client.NewBridge(client.Config{
		CoordinatorURL: "http://coordinator.mesh.local:8080",
	})

	in := strings.NewReader(`{"id":"req-1","type":"get_state"}` + "\n")
	var out strings.Builder

	err := bridge.Serve(context.Background(), in, &out)
	if err != nil && err != io.EOF {
		t.Fatalf("unexpected Serve error: %v", err)
	}

	var resp struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Command string `json:"command"`
		Success bool   `json:"success"`
		Data    struct {
			Model struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Provider string `json:"provider"`
			} `json:"model"`
			SessionID string `json:"sessionId"`
		} `json:"data"`
	}

	line := strings.TrimSpace(out.String())
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v (raw: %q)", err, line)
	}

	if resp.ID != "req-1" {
		t.Errorf("expected ID 'req-1', got %q", resp.ID)
	}
	if resp.Type != "response" || resp.Command != "get_state" || !resp.Success {
		t.Errorf("expected successful get_state response, got %+v", resp)
	}
	if resp.Data.Model.Provider != "gentle-mesh" {
		t.Errorf("expected model provider 'gentle-mesh', got %q", resp.Data.Model.Provider)
	}
	if resp.Data.SessionID == "" {
		t.Errorf("expected non-empty sessionId")
	}
}

func TestBridge_GetMessages(t *testing.T) {
	bridge := client.NewBridge(client.Config{
		CoordinatorURL: "http://coordinator.mesh.local:8080",
	})

	in := strings.NewReader(`{"id":"req-msgs","type":"get_messages"}` + "\n")
	var out strings.Builder

	err := bridge.Serve(context.Background(), in, &out)
	if err != nil && err != io.EOF {
		t.Fatalf("unexpected Serve error: %v", err)
	}

	var resp struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Command string `json:"command"`
		Success bool   `json:"success"`
		Data    struct {
			Messages []interface{} `json:"messages"`
		} `json:"data"`
	}

	line := strings.TrimSpace(out.String())
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	if resp.ID != "req-msgs" || !resp.Success {
		t.Errorf("expected successful get_messages response, got %+v", resp)
	}
	if resp.Data.Messages == nil {
		t.Errorf("expected non-nil messages array")
	}
}

func TestBridge_Prompt_SSEStreaming(t *testing.T) {
	// Create mock coordinator HTTP server
	var taskCreated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/tasks" {
			taskCreated = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"task_id":    "task-test-456",
				"status":     "running",
				"events_url": "/v1/tasks/task-test-456/events",
			})
			return
		}

		if r.Method == http.MethodGet && r.URL.Path == "/v1/tasks/task-test-456/events" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatalf("expected http.Flusher")
			}

			sendEvent := func(eventType string, payload string) {
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, payload)
				flusher.Flush()
			}

			sendEvent("thought", `{"text":"Analizando archivo..."}`)
			sendEvent("tool_call", `{"call_id":"call-1","tool":"read_file","args":{"path":"main.go"}}`)
			sendEvent("tool_result", `{"call_id":"call-1","output":"package main\n"}`)
			sendEvent("completion", `{"text":"Tarea finalizada con exito","result":"Tarea finalizada con exito"}`)
			sendEvent("status", `{"status":"completed"}`)
			return
		}

		http.NotFound(w, r)
	}))
	defer server.Close()

	bridge := client.NewBridge(client.Config{
		CoordinatorURL: server.URL,
		HTTPClient:     server.Client(),
	})

	in := strings.NewReader(`{"id":"prompt-req-1","type":"prompt","message":"hola mundo"}` + "\n")
	r, w := io.Pipe()

	var lines []string
	var linesMu sync.Mutex
	done := make(chan struct{})

	go func() {
		defer close(done)
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			linesMu.Lock()
			lines = append(lines, scanner.Text())
			linesMu.Unlock()
		}
	}()

	err := bridge.Serve(context.Background(), in, w)
	_ = w.Close()
	if err != nil && err != io.EOF {
		t.Fatalf("Serve returned error: %v", err)
	}

	<-done

	if !taskCreated {
		t.Errorf("expected task to be created on coordinator")
	}

	linesMu.Lock()
	collected := make([]string, len(lines))
	copy(collected, lines)
	linesMu.Unlock()

	if len(collected) < 6 {
		t.Fatalf("expected at least 6 output lines, got %d: %v", len(collected), collected)
	}

	// 1. Prompt acknowledgment response
	var ack struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Command string `json:"command"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal([]byte(collected[0]), &ack); err != nil {
		t.Fatalf("failed to parse ack: %v", err)
	}
	if ack.ID != "prompt-req-1" || ack.Type != "response" || ack.Command != "prompt" || !ack.Success {
		t.Errorf("invalid prompt ack: %+v", ack)
	}

	// 2. message_start
	var msgStart struct {
		Type    string `json:"type"`
		Message struct {
			Role string `json:"role"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(collected[1]), &msgStart); err != nil {
		t.Fatalf("failed to parse msgStart: %v", err)
	}
	if msgStart.Type != "message_start" || msgStart.Message.Role != "assistant" {
		t.Errorf("invalid message_start event: %+v", msgStart)
	}

	// Verify thought delta
	var hasThought, hasToolCall, hasToolResult, hasCompletion, hasEnd, hasSettled bool
	for _, l := range collected {
		var evt map[string]interface{}
		_ = json.Unmarshal([]byte(l), &evt)
		evtType, _ := evt["type"].(string)

		switch evtType {
		case "message_update":
			if update, ok := evt["assistantMessageEvent"].(map[string]interface{}); ok {
				if update["type"] == "thinking_delta" {
					hasThought = true
				} else if update["type"] == "text_delta" {
					hasCompletion = true
				}
			}
		case "tool_execution_start":
			hasToolCall = true
		case "tool_execution_end":
			hasToolResult = true
		case "message_end":
			hasEnd = true
		case "agent_settled":
			hasSettled = true
		}
	}

	if !hasThought {
		t.Errorf("missing thinking_delta event in stream")
	}
	if !hasToolCall {
		t.Errorf("missing tool_execution_start event in stream")
	}
	if !hasToolResult {
		t.Errorf("missing tool_execution_end event in stream")
	}
	if !hasCompletion {
		t.Errorf("missing text_delta event in stream")
	}
	if !hasEnd {
		t.Errorf("missing message_end event in stream")
	}
	if !hasSettled {
		t.Errorf("missing agent_settled event in stream")
	}
}

func TestBridge_Abort(t *testing.T) {
	bridge := client.NewBridge(client.Config{
		CoordinatorURL: "http://coordinator.mesh.local:8080",
	})

	in := strings.NewReader(`{"id":"abort-req-1","type":"abort"}` + "\n")
	var out strings.Builder

	err := bridge.Serve(context.Background(), in, &out)
	if err != nil && err != io.EOF {
		t.Fatalf("unexpected Serve error: %v", err)
	}

	var resp struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Command string `json:"command"`
		Success bool   `json:"success"`
	}

	line := strings.TrimSpace(out.String())
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	if resp.ID != "abort-req-1" || resp.Command != "abort" || !resp.Success {
		t.Errorf("expected successful abort response, got %+v", resp)
	}
}

func TestBridge_NewSession(t *testing.T) {
	bridge := client.NewBridge(client.Config{
		CoordinatorURL: "http://coordinator.mesh.local:8080",
	})

	in := strings.NewReader(`{"id":"new-sess-1","type":"new_session"}` + "\n")
	var out strings.Builder

	err := bridge.Serve(context.Background(), in, &out)
	if err != nil && err != io.EOF {
		t.Fatalf("unexpected Serve error: %v", err)
	}

	var resp struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Command string `json:"command"`
		Success bool   `json:"success"`
		Data    struct {
			SessionID string `json:"sessionId"`
		} `json:"data"`
	}

	line := strings.TrimSpace(out.String())
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v", err)
	}

	if resp.ID != "new-sess-1" || resp.Command != "new_session" || !resp.Success {
		t.Errorf("expected successful new_session response, got %+v", resp)
	}
	if resp.Data.SessionID == "" {
		t.Errorf("expected non-empty sessionId")
	}
}

func TestBridge_MalformedInput(t *testing.T) {
	bridge := client.NewBridge(client.Config{
		CoordinatorURL: "http://coordinator.mesh.local:8080",
	})

	// Malformed input followed by valid get_state
	in := strings.NewReader("this is not json\n" + `{"id":"req-ok","type":"get_state"}` + "\n")
	var out strings.Builder

	err := bridge.Serve(context.Background(), in, &out)
	if err != nil && err != io.EOF {
		t.Fatalf("unexpected Serve error: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) == 0 {
		t.Fatalf("expected at least one response line")
	}

	// Last line should be the get_state response
	var resp struct {
		ID      string `json:"id"`
		Success bool   `json:"success"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &resp); err != nil {
		t.Fatalf("failed to parse last line JSON: %v", err)
	}
	if resp.ID != "req-ok" || !resp.Success {
		t.Errorf("expected valid response to req-ok despite prior malformed line")
	}
}
