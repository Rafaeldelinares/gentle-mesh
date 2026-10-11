package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
)

// scriptedRunner fails with the configured errors (repeating the last one) and counts its calls.
type scriptedRunner struct {
	mu     sync.Mutex
	errs   []error
	calls  int
	hold   chan struct{}
	onCall func(int)
}

func (r *scriptedRunner) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	r.mu.Lock()
	r.calls++
	n := r.calls
	r.mu.Unlock()
	if r.onCall != nil {
		r.onCall(n)
	}
	if r.hold != nil {
		<-r.hold
	}
	// A remote node emits a status event when it accepts the task: the task is running from then on.
	_, _ = sink.EmitEvent(protocol.EventStatus, protocol.StatusPayload{Status: protocol.TaskStatusRunning})
	if len(r.errs) == 0 {
		_, _ = sink.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "ok"})
		return nil
	}
	idx := n - 1
	if idx >= len(r.errs) {
		idx = len(r.errs) - 1
	}
	if err := r.errs[idx]; err != nil {
		return err
	}
	_, _ = sink.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{Result: "ok"})
	return nil
}

func (r *scriptedRunner) CallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func retryServer(t *testing.T, r runner.Runner) (*meshhttp.Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	srv, err := meshhttp.NewServer(meshhttp.ServerConfig{TasksDir: dir, HeartbeatTimeout: 5 * time.Second, TaskTTL: time.Hour, Runner: r})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); _ = srv.Shutdown(context.Background()) })
	return srv, ts
}

func retryTask(t *testing.T, ts *httptest.Server, body string) string {
	t.Helper()
	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/tasks: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, raw)
	}
	var tr protocol.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return tr.TaskID
}

func retryState(t *testing.T, ts *httptest.Server, id string) protocol.TaskState {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + "/v1/tasks/" + id)
	if err != nil {
		t.Fatalf("GET task: %v", err)
	}
	defer resp.Body.Close()
	var st protocol.TaskState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatalf("decode state: %v", err)
	}
	return st
}

// waitFor polls until the condition holds or the deadline expires (no fragile sleeps).
func waitRetryFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func waitRetryStatus(t *testing.T, ts *httptest.Server, id string, want ...protocol.TaskStatus) protocol.TaskState {
	t.Helper()
	var last protocol.TaskState
	waitRetryFor(t, 5*time.Second, "status "+string(want[0]), func() bool {
		last = retryState(t, ts, id)
		for _, w := range want {
			if last.Status == w {
				return true
			}
		}
		return false
	})
	return last
}

// A runner that always fails must not leave the task running forever: with max_retries=1 it is
// retried once and then finalized with the cause of the last error.
func TestRetryExhaustedFinalizesTheTask(t *testing.T) {
	r := &scriptedRunner{errs: []error{errors.New("dispatch failed: node is unreachable")}}
	_, ts := retryServer(t, r)

	id := retryTask(t, ts, `{"agent":"worker","task":"always fails","git_repo":"r","git_branch":"b","max_retries":1,"retry_delay_seconds":1}`)
	st := waitRetryStatus(t, ts, id, protocol.TaskStatusFailed)
	if st.Error == nil || st.Error.Code != "DISPATCH_FAILED" {
		t.Fatalf("state error = %+v, want DISPATCH_FAILED", st.Error)
	}
	if got := r.CallCount(); got != 2 {
		t.Fatalf("the runner must be called twice (initial + one retry), got %d", got)
	}
}

// A retry that succeeds completes the task exactly once, after two runner calls.
func TestRetryThatSucceedsCompletesOnce(t *testing.T) {
	r := &scriptedRunner{errs: []error{errors.New("dispatch failed: transient"), nil}}
	_, ts := retryServer(t, r)

	id := retryTask(t, ts, `{"agent":"worker","task":"succeeds on retry","max_retries":1,"retry_delay_seconds":1}`)
	waitRetryStatus(t, ts, id, protocol.TaskStatusCompleted)
	if got := r.CallCount(); got != 2 {
		t.Fatalf("the runner must be called twice, got %d", got)
	}
	time.Sleep(1500 * time.Millisecond)
	if again := retryState(t, ts, id); again.Status != protocol.TaskStatusCompleted {
		t.Fatalf("the task must stay completed, got %s", again.Status)
	}
}

// max_retries=0 keeps the current behaviour: one call and failed.
func TestNoRetriesKeepsCurrentBehaviour(t *testing.T) {
	r := &scriptedRunner{errs: []error{errors.New("dispatch failed: no retries")}}
	_, ts := retryServer(t, r)

	id := retryTask(t, ts, `{"agent":"worker","task":"no retries","max_retries":0}`)
	st := waitRetryStatus(t, ts, id, protocol.TaskStatusFailed)
	if st.Error == nil || st.Error.Code != "DISPATCH_FAILED" {
		t.Fatalf("state error = %+v, want DISPATCH_FAILED", st.Error)
	}
	if got := r.CallCount(); got != 1 {
		t.Fatalf("without retries the runner must be called once, got %d", got)
	}
}

// NODE_LOST and TASK_CANCELED are terminal: with max_retries>0 they are not retried.
func TestTerminalCausesAreNotRetried(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		code  string
		setup func(t *testing.T, ts *httptest.Server, id string)
	}{
		{name: "node lost", err: runner.ErrNodeLost, code: "NODE_LOST"},
		{name: "canceled", err: context.Canceled, code: "TASK_CANCELED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &scriptedRunner{errs: []error{tc.err}}
			_, ts := retryServer(t, r)
			id := retryTask(t, ts, `{"agent":"worker","task":"terminal","max_retries":3,"retry_delay_seconds":1}`)
			st := waitRetryStatus(t, ts, id, protocol.TaskStatusFailed)
			if st.Error == nil || st.Error.Code != tc.code {
				t.Fatalf("state error = %+v, want %s", st.Error, tc.code)
			}
			time.Sleep(1200 * time.Millisecond)
			if got := r.CallCount(); got != 1 {
				t.Fatalf("a terminal cause must not be retried, got %d calls", got)
			}
			if again := retryState(t, ts, id); again.Status != protocol.TaskStatusFailed {
				t.Fatalf("the task must stay failed, got %s", again.Status)
			}
		})
	}
}

// The branch lock is kept during the retry and released once, when the task really finishes.
func TestLockIsKeptDuringRetryAndReleasedOnce(t *testing.T) {
	release := make(chan struct{})
	r := &scriptedRunner{errs: []error{errors.New("dispatch failed: first"), errors.New("dispatch failed: second")}, hold: release}
	srv, ts := retryServer(t, r)

	id := retryTask(t, ts, `{"agent":"worker","task":"holds the lock","git_repo":"acme/mesh","git_branch":"feature/x","max_retries":1,"retry_delay_seconds":1}`)
	waitRetryFor(t, 5*time.Second, "the task to start running", func() bool {
		st := retryState(t, ts, id)
		return st.Status == protocol.TaskStatusRunning || st.Status == protocol.TaskStatusQueued
	})
	// While the retry waits, the lock must still be held.
	if _, _, locked := srv.Registry().Locks().GetLock("acme/mesh", "feature/x"); !locked {
		t.Fatal("the branch lock must be held while the task is between retries")
	}
	close(release)
	st := waitRetryStatus(t, ts, id, protocol.TaskStatusFailed)
	if st.Error == nil || st.Error.Code != "DISPATCH_FAILED" {
		t.Fatalf("state error = %+v, want DISPATCH_FAILED", st.Error)
	}
	// complete() releases the lock in its deferred cleanup, which runs just after the task becomes
	// terminal: poll for the release instead of asserting immediately (same class of race as #92).
	waitRetryFor(t, 3*time.Second, "the branch lock to be released", func() bool {
		_, _, locked := srv.Registry().Locks().GetLock("acme/mesh", "feature/x")
		return !locked
	})
	// And it stays released: no re-acquisition, so the release happened exactly once.
	time.Sleep(500 * time.Millisecond)
	if _, _, locked := srv.Registry().Locks().GetLock("acme/mesh", "feature/x"); locked {
		t.Fatal("the branch lock must not be re-acquired after the task finished")
	}
	waitRetryFor(t, 2*time.Second, "the territory to be free", func() bool {
		return len(srv.TaskManager().RunningTerritories()) == 0
	})
}

// Cancelling while the task waits for its retry must not dispatch a second run.
func TestCancelDuringRetryDelayDoesNotRunAgain(t *testing.T) {
	r := &scriptedRunner{errs: []error{errors.New("dispatch failed: first")}}
	_, ts := retryServer(t, r)

	id := retryTask(t, ts, `{"agent":"worker","task":"cancelled while waiting","max_retries":3,"retry_delay_seconds":2}`)
	waitRetryFor(t, 5*time.Second, "the first runner call", func() bool { return r.CallCount() == 1 })
	resp, err := ts.Client().Post(ts.URL+"/v1/tasks/"+id+"/cancel", "application/json", nil)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	resp.Body.Close()
	waitRetryStatus(t, ts, id, protocol.TaskStatusCanceled)
	time.Sleep(2500 * time.Millisecond)
	if got := r.CallCount(); got != 1 {
		t.Fatalf("a cancelled task must not be dispatched again, got %d calls", got)
	}
	if again := retryState(t, ts, id); again.Status != protocol.TaskStatusCanceled {
		t.Fatalf("the task must stay canceled, got %s (error %+v)", again.Status, again.Error)
	}
}

// A coordinator shutdown during the retry delay must not call the runner again.
func TestShutdownDuringRetryDelayDoesNotRunAgain(t *testing.T) {
	r := &scriptedRunner{errs: []error{errors.New("dispatch failed: first")}}
	srv, ts := retryServer(t, r)

	id := retryTask(t, ts, `{"agent":"worker","task":"shutdown while waiting","max_retries":3,"retry_delay_seconds":2}`)
	waitRetryFor(t, 5*time.Second, "the first runner call", func() bool { return r.CallCount() == 1 })
	done := make(chan struct{})
	go func() { _ = srv.Shutdown(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown did not return within 5s")
	}
	time.Sleep(2500 * time.Millisecond)
	if got := r.CallCount(); got != 1 {
		t.Fatalf("after a shutdown the retry must not call the runner again, got %d calls", got)
	}
	_ = id
}

// The task timeout is a deadline on the task context: if it fires while the task waits for its
// retry, the task must be finalized with TASK_TIMEOUT (and not left running), releasing the lock and
// the territory exactly once, and without calling the runner again.
func TestTimeoutDuringRetryDelayFinalizesWithTaskTimeout(t *testing.T) {
	r := &scriptedRunner{errs: []error{errors.New("dispatch failed: always")}}
	srv, ts := retryServer(t, r)

	id := retryTask(t, ts, `{"agent":"worker","task":"times out waiting","git_repo":"acme/mesh","git_branch":"feature/t","max_retries":5,"retry_delay_seconds":3,"timeout_seconds":1}`)
	st := waitRetryStatus(t, ts, id, protocol.TaskStatusFailed)
	if st.Error == nil || st.Error.Code != "TASK_TIMEOUT" {
		t.Fatalf("state error = %+v, want TASK_TIMEOUT", st.Error)
	}
	waitRetryFor(t, 3*time.Second, "the lock to be released", func() bool {
		_, _, locked := srv.Registry().Locks().GetLock("acme/mesh", "feature/t")
		return !locked
	})
	waitRetryFor(t, 2*time.Second, "the territory to be free", func() bool {
		return len(srv.TaskManager().RunningTerritories()) == 0
	})
	time.Sleep(1500 * time.Millisecond)
	if got := r.CallCount(); got != 1 {
		t.Fatalf("after a timeout the retry must not call the runner again, got %d calls", got)
	}
}

// Exactly one release of the branch lock in each of the three endings: success, exhausted and
// cancelled, and zero while a retry is pending.
// The branch lock is taken before the runner is called, kept while the task runs (retries
// included) and released exactly once when the task ends: deterministically, waiting for the first
// runner call instead of polling for the lock.
func TestLockIsReleasedExactlyOnceInEveryEnding(t *testing.T) {
	cases := []struct {
		name      string
		errs      []error
		body      string
		cancel    bool
		want      protocol.TaskStatus
		wantCalls int
	}{
		{name: "success", errs: []error{errors.New("dispatch failed: once"), nil}, body: `"max_retries":1,"retry_delay_seconds":1`, want: protocol.TaskStatusCompleted, wantCalls: 2},
		{name: "exhausted", errs: []error{errors.New("dispatch failed: always")}, body: `"max_retries":1,"retry_delay_seconds":1`, want: protocol.TaskStatusFailed, wantCalls: 2},
		{name: "cancelled", errs: []error{errors.New("dispatch failed: always")}, body: `"max_retries":5,"retry_delay_seconds":30`, cancel: true, want: protocol.TaskStatusCanceled, wantCalls: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := make(chan struct{})
			var once sync.Once
			r := &scriptedRunner{errs: tc.errs, onCall: func(n int) {
				if n == 1 {
					once.Do(func() { close(first) })
				}
			}}
			srv, ts := retryServer(t, r)

			id := retryTask(t, ts, `{"agent":"worker","task":"`+tc.name+`","git_repo":"acme/mesh","git_branch":"feature/lock",`+tc.body+`}`)
			// (1) the lock is claimed before the runner is called, and it belongs to the task.
			select {
			case <-first:
			case <-time.After(5 * time.Second):
				t.Fatal("the runner was never called")
			}
			owner, _, locked := srv.Registry().Locks().GetLock("acme/mesh", "feature/lock")
			if !locked || owner != id {
				t.Fatalf("the lock must be held by the task while it runs: locked=%v owner=%q (task %q)", locked, owner, id)
			}
			// (2) provoke the ending.
			if tc.cancel {
				resp, err := ts.Client().Post(ts.URL+"/v1/tasks/"+id+"/cancel", "application/json", nil)
				if err != nil {
					t.Fatalf("cancel: %v", err)
				}
				resp.Body.Close()
			}
			waitRetryStatus(t, ts, id, tc.want)
			// (3) the lock is released, once.
			waitRetryFor(t, 3*time.Second, "the branch lock to be released", func() bool {
				_, _, stillLocked := srv.Registry().Locks().GetLock("acme/mesh", "feature/lock")
				return !stillLocked
			})
			// (4) nobody takes it again and the runner is not called again.
			time.Sleep(300 * time.Millisecond)
			if owner, _, locked := srv.Registry().Locks().GetLock("acme/mesh", "feature/lock"); locked {
				t.Fatalf("the lock must not be re-acquired (owner %q)", owner)
			}
			if got := r.CallCount(); got != tc.wantCalls {
				t.Fatalf("runner calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}

// K2: the branch lock is held, and owned by the task, at the very moment the runner is called.
func TestLockIsHeldOnTheFirstRunnerCall(t *testing.T) {
	for i := 0; i < 50; i++ {
		first := make(chan struct{})
		var once sync.Once
		r := &scriptedRunner{errs: []error{errors.New("dispatch failed: always")}, onCall: func(n int) {
			if n == 1 {
				once.Do(func() { close(first) })
			}
		}}
		srv, ts := retryServer(t, r)
		id := retryTask(t, ts, `{"agent":"worker","task":"k2","git_repo":"acme/mesh","git_branch":"feature/k2","max_retries":1,"retry_delay_seconds":30}`)
		select {
		case <-first:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: the runner was never called", i)
		}
		owner, _, locked := srv.Registry().Locks().GetLock("acme/mesh", "feature/k2")
		if !locked || owner != id {
			t.Fatalf("iteration %d: the lock must be held by the task on the first runner call: locked=%v owner=%q task=%q", i, locked, owner, id)
		}
		ts.Close()
	}
}

// A shutdown while a task waits for its retry must return quickly (the retry runs in the runner
// goroutine, which is tracked by the manager WaitGroup) and must not call the runner again.
func TestShutdownDuringLongRetryDelayReturnsFast(t *testing.T) {
	r := &scriptedRunner{errs: []error{errors.New("dispatch failed: always")}}
	srv, ts := retryServer(t, r)

	_ = retryTask(t, ts, `{"agent":"worker","task":"long retry wait","max_retries":5,"retry_delay_seconds":30}`)
	waitRetryFor(t, 5*time.Second, "the first runner call", func() bool { return r.CallCount() == 1 })
	start := time.Now()
	done := make(chan struct{})
	go func() { _ = srv.Shutdown(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Shutdown did not return within 5s (retry_delay was 30s)")
	}
	elapsed := time.Since(start)
	t.Logf("shutdown with a 30s retry pending returned in %v", elapsed)
	if elapsed > 2*time.Second {
		t.Fatalf("shutdown must cancel the wait and return in under 2s, took %v", elapsed)
	}
	time.Sleep(500 * time.Millisecond)
	if got := r.CallCount(); got != 1 {
		t.Fatalf("after a shutdown the retry must not call the runner, got %d", got)
	}
}

// Closing the manager repeatedly while retries are pending must not race nor misuse the WaitGroup.
func TestCloseWithPendingRetriesDoesNotRace(t *testing.T) {
	for i := 0; i < 200; i++ {
		r := &scriptedRunner{errs: []error{errors.New("dispatch failed: always")}}
		srv, ts := retryServer(t, r)
		_ = retryTask(t, ts, `{"agent":"worker","task":"close while retrying","max_retries":3,"retry_delay_seconds":30}`)
		waitRetryFor(t, 5*time.Second, "the first runner call", func() bool { return r.CallCount() == 1 })
		done := make(chan struct{})
		go func() { _ = srv.Shutdown(context.Background()); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: Shutdown did not return", i)
		}
		ts.Close()
	}
}

// errorEmittingRunner emits an error event (which finalizes the task) and then returns a dispatch
// error, exactly like a runner that notices a failure the task manager already recorded.
type errorEmittingRunner struct {
	mu    sync.Mutex
	calls int
}

func (r *errorEmittingRunner) Run(ctx context.Context, req protocol.TaskRequest, sink runner.EventSink) error {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	_, _ = sink.EmitEvent(protocol.EventError, protocol.ErrorPayload{Code: "NODE_FAILED", Message: "the node said no", Fatal: true})
	return errors.New("dispatch failed: error event already finalized the task")
}

func (r *errorEmittingRunner) CallCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// A task that is already terminal when the runner returns must not be retried: the error event that
// finalized it wins as the cause and the runner is not called again.
func TestTerminalTaskIsNotRetriedAfterAnErrorEvent(t *testing.T) {
	r := &errorEmittingRunner{}
	_, ts := retryServer(t, r)

	id := retryTask(t, ts, `{"agent":"worker","task":"error event then dispatch error","max_retries":2,"retry_delay_seconds":1}`)
	st := waitRetryStatus(t, ts, id, protocol.TaskStatusFailed)
	if st.Error == nil || st.Error.Code != "NODE_FAILED" {
		t.Fatalf("the cause of the error event must win, got %+v", st.Error)
	}
	time.Sleep(1500 * time.Millisecond)
	if got := r.CallCount(); got != 1 {
		t.Fatalf("a terminal task must not be retried, got %d runner calls", got)
	}
	if again := retryState(t, ts, id); again.Error == nil || again.Error.Code != "NODE_FAILED" {
		t.Fatalf("the first cause must stay, got %+v", again.Error)
	}
}
