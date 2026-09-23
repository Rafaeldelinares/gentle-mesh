package worker_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/worker"
)

func TestWorkerServer_ExecuteAndStreamSSE(t *testing.T) {
	// Configure simulated runner with slight token delay to verify active tasks during execution
	simRunner := runner.NewSimulatedRunner(runner.SimulatedOptions{
		Tokens:     []string{"Processing", " task", "..."},
		TokenDelay: 40 * time.Millisecond,
	})

	srv := worker.NewServer(worker.ServerConfig{
		Addr:   "127.0.0.1:0",
		Runner: simRunner,
	})

	if err := srv.Listen(); err != nil {
		t.Fatalf("failed to listen on random port: %v", err)
	}

	go func() {
		_ = srv.Start()
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	if srv.ActiveTasks() != 0 {
		t.Fatalf("expected initial active tasks 0, got %d", srv.ActiveTasks())
	}

	taskReq := protocol.TaskRequest{
		Agent: "worker",
		Task:  "run calculation",
	}
	body, err := json.Marshal(taskReq)
	if err != nil {
		t.Fatalf("failed to marshal task request: %v", err)
	}

	executeURL := "http://" + srv.Addr() + "/v1/execute"
	req, err := http.NewRequest(http.MethodPost, executeURL, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to create http request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to post execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected status 200, got %d: %s", resp.StatusCode, string(b))
	}

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected Content-Type text/event-stream, got %q", ct)
	}

	reader := bufio.NewReader(resp.Body)
	var events []*protocol.Event
	var blockLines []string
	sawActiveTask := false

	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			t.Fatalf("error reading stream: %v", readErr)
		}

		cleanLine := strings.TrimRight(line, "\r\n")
		if cleanLine == "" {
			if len(blockLines) > 0 {
				raw := []byte(strings.Join(blockLines, "\n"))
				evt, err := protocol.ParseSSE(raw)
				if err == nil {
					events = append(events, evt)
					// While processing streaming events, active tasks should be 1
					if srv.ActiveTasks() >= 1 {
						sawActiveTask = true
					}
				}
				blockLines = blockLines[:0]
			}
		} else {
			blockLines = append(blockLines, cleanLine)
		}

		if readErr == io.EOF {
			if len(blockLines) > 0 {
				raw := []byte(strings.Join(blockLines, "\n"))
				if evt, err := protocol.ParseSSE(raw); err == nil {
					events = append(events, evt)
				}
			}
			break
		}
	}

	if !sawActiveTask {
		t.Errorf("expected ActiveTasks to be at least 1 during execution")
	}

	// Verify ActiveTasks returned to 0 after stream ended
	for i := 0; i < 20; i++ {
		if srv.ActiveTasks() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if srv.ActiveTasks() != 0 {
		t.Errorf("expected ActiveTasks to return to 0 after execution, got %d", srv.ActiveTasks())
	}

	if len(events) == 0 {
		t.Fatalf("expected to receive events from execute stream, got 0")
	}

	var hasStatus, hasThought, hasCompletion bool
	for _, e := range events {
		switch e.Type {
		case protocol.EventStatus:
			hasStatus = true
		case protocol.EventThought:
			hasThought = true
		case protocol.EventCompletion:
			hasCompletion = true
		}
	}

	if !hasStatus {
		t.Errorf("expected status event in stream")
	}
	if !hasThought {
		t.Errorf("expected thought event in stream")
	}
	if !hasCompletion {
		t.Errorf("expected completion event in stream")
	}
}

func TestWorkerServer_Healthz(t *testing.T) {
	srv := worker.NewServer(worker.ServerConfig{
		Addr:        "127.0.0.1:0",
		BearerToken: "my-secret-token",
	})

	if err := srv.Listen(); err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	go func() {
		_ = srv.Start()
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	// Health check without bearer token should succeed (bypass auth)
	healthURL := "http://" + srv.Addr() + "/healthz"
	resp, err := http.Get(healthURL)
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	var data map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		t.Fatalf("failed to decode healthz json: %v", err)
	}

	if data["status"] != "ok" {
		t.Errorf("expected status ok, got %v", data["status"])
	}
	if act, ok := data["active_tasks"].(float64); !ok || int(act) != 0 {
		t.Errorf("expected active_tasks 0, got %v", data["active_tasks"])
	}
}

func TestWorkerServer_InteractiveReply(t *testing.T) {
	queryID := "query-test-123"
	simRunner := runner.NewSimulatedRunner(runner.SimulatedOptions{
		Query: &runner.SimulatedQuery{
			QueryID:  queryID,
			Question: "Should I proceed with the refactor?",
			Timeout:  3 * time.Second,
		},
	})

	srv := worker.NewServer(worker.ServerConfig{
		Addr:   "127.0.0.1:0",
		Runner: simRunner,
	})

	if err := srv.Listen(); err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	go func() {
		_ = srv.Start()
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	taskReq := protocol.TaskRequest{
		Agent: "worker",
		Task:  "interactive task",
	}
	body, _ := json.Marshal(taskReq)

	executeURL := "http://" + srv.Addr() + "/v1/execute"
	req, _ := http.NewRequest(http.MethodPost, executeURL, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("failed to start execute: %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	var blockLines []string
	var queryReceived bool
	var completionReceived bool

	replyURL := "http://" + srv.Addr() + "/v1/reply"

	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			t.Fatalf("read error: %v", readErr)
		}

		cleanLine := strings.TrimRight(line, "\r\n")
		if cleanLine == "" {
			if len(blockLines) > 0 {
				raw := []byte(strings.Join(blockLines, "\n"))
				evt, err := protocol.ParseSSE(raw)
				if err == nil {
					if evt.Type == protocol.EventQuery {
						queryReceived = true
						var qp protocol.QueryPayload
						_ = evt.UnmarshalPayload(&qp)
						if qp.QueryID != queryID {
							t.Errorf("expected query ID %q, got %q", queryID, qp.QueryID)
						}

						// Send reply
						replyPayload := protocol.TaskReplyRequest{
							QueryID: queryID,
							Answer:  "yes, proceed",
						}
						replyBytes, _ := json.Marshal(replyPayload)
						replyReq, _ := http.NewRequest(http.MethodPost, replyURL, bytes.NewReader(replyBytes))
						replyReq.Header.Set("Content-Type", "application/json")

						replyResp, err := http.DefaultClient.Do(replyReq)
						if err != nil {
							t.Fatalf("failed to send reply: %v", err)
						}
						if replyResp.StatusCode != http.StatusOK {
							b, _ := io.ReadAll(replyResp.Body)
							t.Fatalf("reply failed with status %d: %s", replyResp.StatusCode, string(b))
						}
						replyResp.Body.Close()
					}
					if evt.Type == protocol.EventCompletion {
						completionReceived = true
					}
				}
				blockLines = blockLines[:0]
			}
		} else {
			blockLines = append(blockLines, cleanLine)
		}

		if readErr == io.EOF {
			break
		}
	}

	if !queryReceived {
		t.Errorf("expected to receive query event")
	}
	if !completionReceived {
		t.Errorf("expected to receive completion event after reply")
	}

	// Verify replying to non-existent query returns 404
	badReply := protocol.TaskReplyRequest{
		QueryID: "non-existent-query",
		Answer:  "something",
	}
	badBytes, _ := json.Marshal(badReply)
	badReq, _ := http.NewRequest(http.MethodPost, replyURL, bytes.NewReader(badBytes))
	badReq.Header.Set("Content-Type", "application/json")

	badResp, err := http.DefaultClient.Do(badReq)
	if err != nil {
		t.Fatalf("failed to post bad reply: %v", err)
	}
	defer badResp.Body.Close()

	if badResp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for unknown query reply, got %d", badResp.StatusCode)
	}
}

func TestWorkerServer_AuthAndValidation(t *testing.T) {
	srv := worker.NewServer(worker.ServerConfig{
		Addr:        "127.0.0.1:0",
		BearerToken: "auth-secret",
	})

	if err := srv.Listen(); err != nil {
		t.Fatalf("failed to listen: %v", err)
	}

	go func() {
		_ = srv.Start()
	}()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	client := http.DefaultClient

	// 1. POST /v1/execute without auth -> 401
	resp, err := client.Post("http://"+srv.Addr()+"/v1/execute", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthorized execute, got %d", resp.StatusCode)
	}

	// 2. POST /v1/reply without auth -> 401
	resp, err = client.Post("http://"+srv.Addr()+"/v1/reply", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthorized reply, got %d", resp.StatusCode)
	}

	// 3. POST /v1/reply with invalid JSON -> 400
	req, _ := http.NewRequest(http.MethodPost, "http://"+srv.Addr()+"/v1/reply", strings.NewReader("not-json"))
	req.Header.Set("Authorization", "Bearer auth-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for bad JSON in reply, got %d", resp.StatusCode)
	}

	// 4. POST /v1/reply with empty query_id -> 400
	req, _ = http.NewRequest(http.MethodPost, "http://"+srv.Addr()+"/v1/reply", strings.NewReader(`{"query_id":""}`))
	req.Header.Set("Authorization", "Bearer auth-secret")
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for empty query_id in reply, got %d", resp.StatusCode)
	}
}
