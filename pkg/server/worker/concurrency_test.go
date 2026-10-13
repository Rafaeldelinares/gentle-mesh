package worker_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/worker"
)

type controlledRunner struct {
	active      atomic.Int32
	maxActive   atomic.Int32
	totalCalls  atomic.Int32
	releaseChan chan struct{}
}

func newControlledRunner() *controlledRunner {
	return &controlledRunner{
		releaseChan: make(chan struct{}),
	}
}

func (r *controlledRunner) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	r.totalCalls.Add(1)
	cur := r.active.Add(1)
	defer r.active.Add(-1)

	for {
		old := r.maxActive.Load()
		if cur > old {
			if r.maxActive.CompareAndSwap(old, cur) {
				break
			}
		} else {
			break
		}
	}

	select {
	case <-r.releaseChan:
	case <-ctx.Done():
		return ctx.Err()
	}

	_, _ = sink.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{
		Result: "success",
	})
	return nil
}

// (a) max simultaneous executions = 1 with -concurrency 1
func TestWorkerServer_ConcurrencyLimit(t *testing.T) {
	cRunner := newControlledRunner()
	defer close(cRunner.releaseChan)

	srv := worker.NewServer(worker.ServerConfig{
		Addr:        "127.0.0.1:0",
		Runner:      cRunner,
		Concurrency: 1,
		QueueSize:   5,
	})
	if err := srv.Listen(); err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = srv.Start() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	var wg sync.WaitGroup
	client := &http.Client{Timeout: 5 * time.Second}
	taskBody, _ := json.Marshal(protocol.TaskRequest{Agent: "worker", Task: "task"})

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+srv.Addr()+"/v1/execute", bytes.NewReader(taskBody))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
	}

	// Poll until 1 task is running and 2 are in queue
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.ActiveTasks() == 1 && srv.QueuedTasks() == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if srv.ActiveTasks() != 1 {
		t.Fatalf("expected ActiveTasks() == 1, got %d", srv.ActiveTasks())
	}
	if srv.QueuedTasks() != 2 {
		t.Fatalf("expected QueuedTasks() == 2, got %d", srv.QueuedTasks())
	}
	if cRunner.maxActive.Load() > 1 {
		t.Fatalf("max concurrent executions exceeded limit 1: got %d", cRunner.maxActive.Load())
	}
}

// (b) with -queue-size 1, 2nd waits and 3rd receives 429 with Retry-After
func TestWorkerServer_QueueBoundedAnd429(t *testing.T) {
	cRunner := newControlledRunner()
	defer close(cRunner.releaseChan)

	srv := worker.NewServer(worker.ServerConfig{
		Addr:        "127.0.0.1:0",
		Runner:      cRunner,
		Concurrency: 1,
		QueueSize:   1,
	})
	if err := srv.Listen(); err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = srv.Start() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	taskBody, _ := json.Marshal(protocol.TaskRequest{Agent: "worker", Task: "task"})

	// Request 1: occupies the execution slot
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+srv.Addr()+"/v1/execute", bytes.NewReader(taskBody))
		req.Header.Set("Content-Type", "application/json")
		resp, _ := client.Do(req)
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	// Wait until Request 1 is active
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.ActiveTasks() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Request 2: occupies the single queue slot
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+srv.Addr()+"/v1/execute", bytes.NewReader(taskBody))
		req.Header.Set("Content-Type", "application/json")
		resp, _ := client.Do(req)
		if resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	// Wait until Request 2 is queued
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.QueuedTasks() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Request 3: execution and queue are full -> must receive HTTP 429 immediately
	req3, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+srv.Addr()+"/v1/execute", bytes.NewReader(taskBody))
	req3.Header.Set("Content-Type", "application/json")
	resp3, err := client.Do(req3)
	if err != nil {
		t.Fatalf("failed request 3: %v", err)
	}
	defer resp3.Body.Close()

	if resp3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected status 429 for request 3, got %d", resp3.StatusCode)
	}
	retryAfter := resp3.Header.Get("Retry-After")
	if retryAfter == "" {
		t.Errorf("expected Retry-After header on 429 response")
	}
	body3, _ := io.ReadAll(resp3.Body)
	if !strings.Contains(string(body3), "queue full") && !strings.Contains(string(body3), "concurrency") {
		t.Errorf("expected 429 error message, got %s", string(body3))
	}
}

// (c) queued task emits status "queued" immediately for early worker attribution
func TestWorkerServer_QueuedTaskEarlyAttribution(t *testing.T) {
	cRunner := newControlledRunner()
	defer close(cRunner.releaseChan)

	srv := worker.NewServer(worker.ServerConfig{
		Addr:        "127.0.0.1:0",
		Runner:      cRunner,
		Concurrency: 1,
		QueueSize:   2,
	})
	if err := srv.Listen(); err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = srv.Start() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	taskBody, _ := json.Marshal(protocol.TaskRequest{Agent: "worker", Task: "task"})

	// Occupy execution slot
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+srv.Addr()+"/v1/execute", bytes.NewReader(taskBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.ActiveTasks() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Task 2 is queued: read its SSE stream immediately
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+srv.Addr()+"/v1/execute", bytes.NewReader(taskBody))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("failed to execute task 2: %v", err)
	}
	defer resp2.Body.Close()

	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 SSE stream for queued task, got %d", resp2.StatusCode)
	}

	reader := bufio.NewReader(resp2.Body)
	var rawBlock []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("error reading SSE: %v", err)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if len(rawBlock) > 0 {
				break
			}
		} else {
			rawBlock = append(rawBlock, trimmed)
		}
	}

	evt, err := protocol.ParseSSE([]byte(strings.Join(rawBlock, "\n")))
	if err != nil {
		t.Fatalf("failed to parse SSE event: %v", err)
	}

	if evt.Type != protocol.EventStatus {
		t.Fatalf("expected first event type to be 'status', got %q", evt.Type)
	}
	var sp protocol.StatusPayload
	if err := json.Unmarshal(evt.Payload, &sp); err != nil {
		t.Fatalf("failed to unmarshal status payload: %v", err)
	}
	if sp.Status != protocol.TaskStatusQueued {
		t.Fatalf("expected queued status payload, got %q", sp.Status)
	}
}

// (d) cancelling a queued task removes it without executing it
func TestWorkerServer_CanceledQueuedTask(t *testing.T) {
	cRunner := newControlledRunner()
	defer close(cRunner.releaseChan)

	srv := worker.NewServer(worker.ServerConfig{
		Addr:        "127.0.0.1:0",
		Runner:      cRunner,
		Concurrency: 1,
		QueueSize:   2,
	})
	if err := srv.Listen(); err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = srv.Start() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	taskBody, _ := json.Marshal(protocol.TaskRequest{Agent: "worker", Task: "task"})

	// Occupy execution slot
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+srv.Addr()+"/v1/execute", bytes.NewReader(taskBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.ActiveTasks() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Task 2 with cancellable context
	ctx2, cancel2 := context.WithCancel(context.Background())
	req2, _ := http.NewRequestWithContext(ctx2, http.MethodPost, "http://"+srv.Addr()+"/v1/execute", bytes.NewReader(taskBody))
	req2.Header.Set("Content-Type", "application/json")

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		resp, err := client.Do(req2)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	// Wait until Task 2 is queued
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.QueuedTasks() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Cancel Task 2
	cancel2()
	<-done2

	// Verify it left the queue
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.QueuedTasks() == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if srv.QueuedTasks() != 0 {
		t.Fatalf("expected QueuedTasks() == 0 after cancellation, got %d", srv.QueuedTasks())
	}
	if cRunner.totalCalls.Load() != 1 {
		t.Fatalf("runner should not have been called for canceled task: totalCalls = %d", cRunner.totalCalls.Load())
	}
}

// (e) -queue-timeout expires with QUEUE_TIMEOUT error cause
func TestWorkerServer_QueueTimeoutExpires(t *testing.T) {
	cRunner := newControlledRunner()
	defer close(cRunner.releaseChan)

	srv := worker.NewServer(worker.ServerConfig{
		Addr:         "127.0.0.1:0",
		Runner:       cRunner,
		Concurrency:  1,
		QueueSize:    2,
		QueueTimeout: 50 * time.Millisecond,
	})
	if err := srv.Listen(); err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = srv.Start() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	taskBody, _ := json.Marshal(protocol.TaskRequest{Agent: "worker", Task: "task"})

	// Occupy execution slot
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+srv.Addr()+"/v1/execute", bytes.NewReader(taskBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.ActiveTasks() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Task 2 will time out in queue
	req2, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+srv.Addr()+"/v1/execute", bytes.NewReader(taskBody))
	req2.Header.Set("Content-Type", "application/json")
	resp2, err := client.Do(req2)
	if err != nil {
		t.Fatalf("failed request 2: %v", err)
	}
	defer resp2.Body.Close()

	reader := bufio.NewReader(resp2.Body)
	var events []protocol.Event

	for {
		var rawBlock []string
		streamDone := false
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				streamDone = true
				break
			}
			trimmed := strings.TrimRight(line, "\r\n")
			if trimmed == "" {
				if len(rawBlock) > 0 {
					break
				}
			} else {
				rawBlock = append(rawBlock, trimmed)
			}
		}
		if len(rawBlock) > 0 {
			evt, err := protocol.ParseSSE([]byte(strings.Join(rawBlock, "\n")))
			if err == nil {
				events = append(events, *evt)
			}
		}
		if streamDone {
			break
		}
	}

	if len(events) < 2 {
		t.Fatalf("expected at least 2 events (status queued, error), got %d events", len(events))
	}

	lastEvt := events[len(events)-1]
	if lastEvt.Type != protocol.EventError {
		t.Fatalf("expected last event type 'error', got %q", lastEvt.Type)
	}
	var ep protocol.ErrorPayload
	if err := json.Unmarshal(lastEvt.Payload, &ep); err != nil {
		t.Fatalf("failed to unmarshal error payload: %v", err)
	}
	if ep.Code != "QUEUE_TIMEOUT" {
		t.Fatalf("expected error code QUEUE_TIMEOUT, got %q", ep.Code)
	}
}

// (h) active_tasks counts running tasks, not queued tasks
func TestWorkerServer_ActiveTasksCountsRunningOnly(t *testing.T) {
	cRunner := newControlledRunner()
	defer close(cRunner.releaseChan)

	srv := worker.NewServer(worker.ServerConfig{
		Addr:        "127.0.0.1:0",
		Runner:      cRunner,
		Concurrency: 1,
		QueueSize:   2,
	})
	if err := srv.Listen(); err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	go func() { _ = srv.Start() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	taskBody, _ := json.Marshal(protocol.TaskRequest{Agent: "worker", Task: "task"})

	// Occupy execution slot
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+srv.Addr()+"/v1/execute", bytes.NewReader(taskBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.ActiveTasks() == 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Send 2 queued tasks
	for i := 0; i < 2; i++ {
		go func() {
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://"+srv.Addr()+"/v1/execute", bytes.NewReader(taskBody))
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
			}
		}()
	}

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if srv.QueuedTasks() == 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// active_tasks must strictly be 1 while 2 are queued
	if srv.ActiveTasks() != 1 {
		t.Fatalf("expected ActiveTasks() == 1, got %d", srv.ActiveTasks())
	}
	if srv.QueuedTasks() != 2 {
		t.Fatalf("expected QueuedTasks() == 2, got %d", srv.QueuedTasks())
	}
}
