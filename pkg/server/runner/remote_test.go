package runner_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
)

func TestRemoteRunner_HappyPath(t *testing.T) {
	statusEvt, _ := protocol.NewEvent(1, "task-1", protocol.EventStatus, protocol.StatusPayload{
		Status: protocol.TaskStatusRunning,
	})
	thoughtEvt, _ := protocol.NewEvent(2, "task-1", protocol.EventThought, protocol.ThoughtPayload{
		Text: "Analyzing code...",
	})
	completionEvt, _ := protocol.NewEvent(3, "task-1", protocol.EventCompletion, protocol.CompletionPayload{
		Result: "Task finished successfully",
	})

	var authHeader string
	var contentTypeHeader string
	var receivedBody protocol.TaskRequest

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/execute" {
			http.NotFound(w, r)
			return
		}

		authHeader = r.Header.Get("Authorization")
		contentTypeHeader = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("ResponseWriter does not implement Flusher")
		}

		// Comment line (ping) should be ignored
		w.Write([]byte(": ping\n\n"))
		flusher.Flush()

		w.Write(statusEvt.FormatSSE())
		flusher.Flush()

		w.Write(thoughtEvt.FormatSSE())
		flusher.Flush()

		w.Write(completionEvt.FormatSSE())
		flusher.Flush()
	}))
	defer ts.Close()

	cfg := runner.RemoteRunnerConfig{
		Endpoint: ts.URL,
		Client:   ts.Client(),
		Token:    "secret-mesh-token",
	}
	r := runner.NewRemoteRunner(cfg)

	sink := newMockSink(context.Background())
	req := protocol.TaskRequest{
		Agent: "worker",
		Task:  "Execute remote work",
		Tags:  []string{"fast"},
	}

	err := r.Run(context.Background(), req, sink)
	if err != nil {
		t.Fatalf("unexpected error running RemoteRunner: %v", err)
	}

	if authHeader != "Bearer secret-mesh-token" {
		t.Errorf("expected Authorization header 'Bearer secret-mesh-token', got %q", authHeader)
	}
	if contentTypeHeader != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", contentTypeHeader)
	}
	if receivedBody.Agent != req.Agent || receivedBody.Task != req.Task {
		t.Errorf("received body mismatch: %+v vs %+v", receivedBody, req)
	}

	events := sink.Events()
	if len(events) != 3 {
		t.Fatalf("expected 3 events emitted to sink, got %d: %+v", len(events), events)
	}

	if events[0].Type != protocol.EventStatus {
		t.Errorf("event 0 type mismatch: got %s, want %s", events[0].Type, protocol.EventStatus)
	}
	if events[1].Type != protocol.EventThought {
		t.Errorf("event 1 type mismatch: got %s, want %s", events[1].Type, protocol.EventThought)
	}
	if events[2].Type != protocol.EventCompletion {
		t.Errorf("event 2 type mismatch: got %s, want %s", events[2].Type, protocol.EventCompletion)
	}
}

func TestRemoteRunner_WorkerErrors(t *testing.T) {
	t.Run("worker returns 500 internal server error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("out of capacity"))
		}))
		defer ts.Close()

		r := runner.NewRemoteRunner(runner.RemoteRunnerConfig{
			Endpoint: ts.URL,
			Client:   ts.Client(),
		})

		sink := newMockSink(context.Background())
		err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker"}, sink)
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		expectedSubstr := fmt.Sprintf("remote worker %s returned status 500: out of capacity", ts.URL)
		if !strings.Contains(err.Error(), expectedSubstr) {
			t.Errorf("expected error %q to contain %q", err.Error(), expectedSubstr)
		}
	})

	t.Run("worker returns 400 bad request", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("invalid agent"))
		}))
		defer ts.Close()

		r := runner.NewRemoteRunner(runner.RemoteRunnerConfig{
			Endpoint: ts.URL,
			Client:   ts.Client(),
		})

		sink := newMockSink(context.Background())
		err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker"}, sink)
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		expectedSubstr := fmt.Sprintf("remote worker %s returned status 400: invalid agent", ts.URL)
		if !strings.Contains(err.Error(), expectedSubstr) {
			t.Errorf("expected error %q to contain %q", err.Error(), expectedSubstr)
		}
	})

	t.Run("worker stream emits EventError", func(t *testing.T) {
		errEvt, _ := protocol.NewEvent(1, "task-1", protocol.EventError, protocol.ErrorPayload{
			Code:    "crash",
			Message: "process crashed",
			Fatal:   true,
		})

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			w.Write(errEvt.FormatSSE())
		}))
		defer ts.Close()

		r := runner.NewRemoteRunner(runner.RemoteRunnerConfig{
			Endpoint: ts.URL,
			Client:   ts.Client(),
		})

		sink := newMockSink(context.Background())
		err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker"}, sink)
		if err != nil {
			t.Fatalf("unexpected runner error: %v", err)
		}

		events := sink.Events()
		if len(events) != 1 || events[0].Type != protocol.EventError {
			t.Fatalf("expected 1 EventError, got %+v", events)
		}
	})
}

func TestRemoteRunner_InteractiveQuery(t *testing.T) {
	queryEvt, _ := protocol.NewEvent(1, "task-1", protocol.EventQuery, protocol.QueryPayload{
		QueryID: "query-42",
		Prompt:  "Confirm dangerous action?",
		Choices: []string{"yes", "no"},
	})
	completionEvt, _ := protocol.NewEvent(2, "task-1", protocol.EventCompletion, protocol.CompletionPayload{
		Result: "Done after confirmation",
	})

	var mu sync.Mutex
	var replyReceived protocol.TaskReplyRequest
	var replyAuthHeader string
	replyDone := make(chan struct{})

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/execute":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)

			flusher := w.(http.Flusher)
			w.Write(queryEvt.FormatSSE())
			flusher.Flush()

			// Wait for reply before sending completion
			select {
			case <-replyDone:
				w.Write(completionEvt.FormatSSE())
				flusher.Flush()
			case <-time.After(2 * time.Second):
				t.Errorf("timed out waiting for reply")
				return
			}

		case "/v1/reply":
			mu.Lock()
			replyAuthHeader = r.Header.Get("Authorization")
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &replyReceived)
			mu.Unlock()

			close(replyDone)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	sink := newMockSink(context.Background())
	sink.onEvent = func(evt protocol.Event) {
		if evt.Type == protocol.EventQuery {
			// Resolve the query asynchronously
			go func() {
				time.Sleep(10 * time.Millisecond)
				sink.ResolveQuery("query-42", "yes")
			}()
		}
	}

	r := runner.NewRemoteRunner(runner.RemoteRunnerConfig{
		Endpoint: ts.URL,
		Client:   ts.Client(),
		Token:    "query-auth-token",
	})

	err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker"}, sink)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if replyReceived.QueryID != "query-42" || replyReceived.Answer != "yes" {
		t.Errorf("unexpected reply received: %+v", replyReceived)
	}
	if replyAuthHeader != "Bearer query-auth-token" {
		t.Errorf("unexpected reply auth header: %q", replyAuthHeader)
	}

	events := sink.Events()
	if len(events) != 2 {
		t.Fatalf("expected 2 events (query, completion), got %d", len(events))
	}
}

func TestRemoteRunner_Triangulation(t *testing.T) {
	t.Run("nil sink returns error", func(t *testing.T) {
		r := runner.NewRemoteRunner(runner.RemoteRunnerConfig{
			Endpoint: "http://localhost:9999",
		})
		err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker"}, nil)
		if err == nil {
			t.Fatal("expected error with nil sink, got nil")
		}
	})

	t.Run("canceled context aborts request", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		r := runner.NewRemoteRunner(runner.RemoteRunnerConfig{
			Endpoint: "http://localhost:9999",
		})
		sink := newMockSink(ctx)
		err := r.Run(ctx, protocol.TaskRequest{Agent: "worker"}, sink)
		if err == nil {
			t.Fatal("expected error with canceled context, got nil")
		}
	})
}
