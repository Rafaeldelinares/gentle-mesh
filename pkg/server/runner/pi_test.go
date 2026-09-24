package runner_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
)

// piHappyStream is a real-shaped Pi JSON event sequence captured from the
// documented `pi --print --mode json` wire format: session events are flat
// JSON objects, assistant deltas live under assistantMessageEvent, and tool
// activity arrives as tool_execution_start/tool_execution_end.
const piHappyStream = `{"type":"agent_start"}
{"type":"message_start","message":{"role":"assistant","content":[]}}
{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"planning steps"}}
{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"hello "}}
{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","contentIndex":1,"delta":"world"}}
{"type":"tool_execution_start","toolCallId":"call_1","toolName":"read","args":{"path":"pkg/server/runner/pi.go"}}
{"type":"tool_execution_end","toolCallId":"call_1","toolName":"read","result":{"content":"file body"},"isError":false}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"hello world"}]}}
{"type":"agent_settled"}
`

// writeFakePi writes an executable POSIX script standing in for the pi binary so
// the tests exercise the real os/exec wiring without a live model call.
func writeFakePi(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake pi harness requires a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "fake-pi")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("failed to write fake pi script: %v", err)
	}
	return path
}

// fakePiScript renders a script that records its argv and replays a canned
// newline-delimited JSON event stream before exiting with exitCode.
func fakePiScript(recordPath, stream string, exitCode int) string {
	var b strings.Builder
	if recordPath != "" {
		fmt.Fprintf(&b, "printf '%%s\\n' \"$@\" > %s\n", shellQuote(recordPath))
	}
	if stream != "" {
		b.WriteString("cat <<'PI_EOF'\n")
		b.WriteString(stream)
		if !strings.HasSuffix(stream, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("PI_EOF\n")
	}
	fmt.Fprintf(&b, "exit %d\n", exitCode)
	return b.String()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	trimmed := strings.TrimRight(string(data), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func eventTypes(events []protocol.Event) []protocol.EventType {
	types := make([]protocol.EventType, len(events))
	for i, evt := range events {
		types[i] = evt.Type
	}
	return types
}

// TestTokenEvent_UpdatesManagedTaskCurrentAction pins the ManagedTask event
// contract for the streaming token event type used by PiRunner.
func TestTokenEvent_UpdatesManagedTaskCurrentAction(t *testing.T) {
	mgr, err := task.NewTaskManager(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatalf("failed to create task manager: %v", err)
	}
	defer mgr.Close()

	mt, err := mgr.CreateTask(protocol.TaskRequest{Agent: "worker", Task: "stream tokens"})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	long := strings.Repeat("x", 150) + "\nignored second line"
	evt, err := mt.EmitEvent(protocol.EventToken, protocol.TokenPayload{Text: long})
	if err != nil {
		t.Fatalf("EmitEvent(token) failed: %v", err)
	}
	if evt.Type != protocol.EventToken {
		t.Fatalf("expected event type %q, got %q", protocol.EventToken, evt.Type)
	}

	want := strings.Repeat("x", 100)
	if mt.CurrentAction != want {
		t.Errorf("expected CurrentAction %q, got %q", want, mt.CurrentAction)
	}

	var payload protocol.TokenPayload
	if err := evt.UnmarshalPayload(&payload); err != nil {
		t.Fatalf("failed to unmarshal token payload: %v", err)
	}
	if payload.Text != long {
		t.Errorf("expected token payload text %q, got %q", long, payload.Text)
	}

	// An empty token must not clear the action already recorded.
	if _, err := mt.EmitEvent(protocol.EventToken, protocol.TokenPayload{}); err != nil {
		t.Fatalf("EmitEvent(empty token) failed: %v", err)
	}
	if mt.CurrentAction != want {
		t.Errorf("empty token changed CurrentAction to %q", mt.CurrentAction)
	}
}

func TestPiRunner_HappyPathTranslatesPiStream(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "argv.txt")
	script := writeFakePi(t, fakePiScript(argsPath, piHappyStream, 0))

	sink := newMockSink(context.Background())
	r := runner.NewPiRunner(runner.PiRunnerOptions{Binary: script})

	err := r.Run(context.Background(), protocol.TaskRequest{
		Agent:     "worker",
		Task:      "Summarize the repo",
		SessionID: "sess-42",
	}, sink)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	events := sink.Events()
	want := []protocol.EventType{
		protocol.EventThought,
		protocol.EventToken,
		protocol.EventToken,
		protocol.EventToolCall,
		protocol.EventToolResult,
		protocol.EventCompletion,
	}
	if got := eventTypes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("expected event sequence %v, got %v", want, got)
	}

	var thought protocol.ThoughtPayload
	if err := events[0].UnmarshalPayload(&thought); err != nil {
		t.Fatalf("failed to unmarshal thought payload: %v", err)
	}
	if thought.Text != "planning steps" {
		t.Errorf("expected thought text %q, got %q", "planning steps", thought.Text)
	}

	for i, expected := range []string{"hello ", "world"} {
		var token protocol.TokenPayload
		if err := events[i+1].UnmarshalPayload(&token); err != nil {
			t.Fatalf("failed to unmarshal token payload: %v", err)
		}
		if token.Text != expected {
			t.Errorf("token %d: expected %q, got %q", i, expected, token.Text)
		}
	}

	var toolCall protocol.ToolCallPayload
	if err := events[3].UnmarshalPayload(&toolCall); err != nil {
		t.Fatalf("failed to unmarshal tool call payload: %v", err)
	}
	if toolCall.CallID != "call_1" || toolCall.Tool != "read" {
		t.Errorf("unexpected tool call payload: %+v", toolCall)
	}
	if toolCall.Args["path"] != "pkg/server/runner/pi.go" {
		t.Errorf("unexpected tool call args: %+v", toolCall.Args)
	}

	var toolResult protocol.ToolResultPayload
	if err := events[4].UnmarshalPayload(&toolResult); err != nil {
		t.Fatalf("failed to unmarshal tool result payload: %v", err)
	}
	if toolResult.CallID != "call_1" {
		t.Errorf("expected tool result call id %q, got %q", "call_1", toolResult.CallID)
	}
	if toolResult.Output != `{"content":"file body"}` {
		t.Errorf("unexpected tool result output: %q", toolResult.Output)
	}
	if toolResult.IsError {
		t.Errorf("expected non-error tool result, got is_error=true")
	}

	var completion protocol.CompletionPayload
	if err := events[5].UnmarshalPayload(&completion); err != nil {
		t.Fatalf("failed to unmarshal completion payload: %v", err)
	}
	if completion.Result != "hello world" || completion.Text != "hello world" {
		t.Errorf("expected completion text %q, got result=%q text=%q", "hello world", completion.Result, completion.Text)
	}

	wantArgs := []string{"--print", "--mode", "json", "--session-id", "sess-42", "--", "Summarize the repo"}
	if gotArgs := readLines(t, argsPath); !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Errorf("expected pi argv %v, got %v", wantArgs, gotArgs)
	}
}

func TestPiRunner_PromptAndSessionArguments(t *testing.T) {
	t.Run("prompt falls back to TaskRequest.Prompt without a session id", func(t *testing.T) {
		argsPath := filepath.Join(t.TempDir(), "argv.txt")
		script := writeFakePi(t, fakePiScript(argsPath, "", 0))

		sink := newMockSink(context.Background())
		r := runner.NewPiRunner(runner.PiRunnerOptions{Binary: script})

		if err := r.Run(context.Background(), protocol.TaskRequest{
			Agent:  "worker",
			Prompt: "Review the diff",
		}, sink); err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}

		wantArgs := []string{"--print", "--mode", "json", "--", "Review the diff"}
		if gotArgs := readLines(t, argsPath); !reflect.DeepEqual(gotArgs, wantArgs) {
			t.Errorf("expected pi argv %v, got %v", wantArgs, gotArgs)
		}
	})

	t.Run("extra args are passed before the prompt separator", func(t *testing.T) {
		argsPath := filepath.Join(t.TempDir(), "argv.txt")
		script := writeFakePi(t, fakePiScript(argsPath, "", 0))

		r := runner.NewPiRunner(runner.PiRunnerOptions{
			Binary:    script,
			ExtraArgs: []string{"--model", "provider/model"},
		})

		if err := r.Run(context.Background(), protocol.TaskRequest{
			Agent: "worker",
			Task:  "Ship it",
		}, newMockSink(context.Background())); err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}

		wantArgs := []string{"--print", "--mode", "json", "--model", "provider/model", "--", "Ship it"}
		if gotArgs := readLines(t, argsPath); !reflect.DeepEqual(gotArgs, wantArgs) {
			t.Errorf("expected pi argv %v, got %v", wantArgs, gotArgs)
		}
	})
}

func TestPiRunner_MessageEndSuppliesCompletionText(t *testing.T) {
	stream := `{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"thinking_delta","contentIndex":0,"delta":"reasoning"}}
{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"final answer"},{"type":"toolCall","id":"call_9","name":"read","arguments":{}}]}}
{"type":"agent_settled"}
`
	script := writeFakePi(t, fakePiScript("", stream, 0))
	sink := newMockSink(context.Background())
	r := runner.NewPiRunner(runner.PiRunnerOptions{Binary: script})

	if err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "answer"}, sink); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	events := sink.Events()
	want := []protocol.EventType{protocol.EventThought, protocol.EventCompletion}
	if got := eventTypes(events); !reflect.DeepEqual(got, want) {
		t.Fatalf("expected event sequence %v, got %v", want, got)
	}

	var completion protocol.CompletionPayload
	if err := events[1].UnmarshalPayload(&completion); err != nil {
		t.Fatalf("failed to unmarshal completion payload: %v", err)
	}
	if completion.Result != "final answer" {
		t.Errorf("expected completion result %q, got %q", "final answer", completion.Result)
	}
}

func TestPiRunner_SkipsNonJSONAndUnknownEvents(t *testing.T) {
	stream := `not json at all
{"type":"agent_start"}
{"type":"some_future_event","payload":{"a":1}}
{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"ok"}}
{"type":"agent_settled"}
`
	script := writeFakePi(t, fakePiScript("", stream, 0))
	sink := newMockSink(context.Background())
	r := runner.NewPiRunner(runner.PiRunnerOptions{Binary: script})

	if err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "robust"}, sink); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	want := []protocol.EventType{protocol.EventToken, protocol.EventCompletion}
	if got := eventTypes(sink.Events()); !reflect.DeepEqual(got, want) {
		t.Fatalf("expected event sequence %v, got %v", want, got)
	}
}

func TestPiRunner_FailureReportsFatalErrorEvent(t *testing.T) {
	script := writeFakePi(t, "echo 'provider unreachable' >&2\n"+fakePiScript("", `{"type":"agent_start"}`+"\n", 3))

	sink := newMockSink(context.Background())
	r := runner.NewPiRunner(runner.PiRunnerOptions{Binary: script})

	err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "fail"}, sink)
	if err == nil {
		t.Fatal("expected an error for a non-zero pi exit, got nil")
	}
	if !strings.Contains(err.Error(), "provider unreachable") {
		t.Errorf("expected stderr detail in error, got: %v", err)
	}

	events := sink.Events()
	if len(events) != 1 || events[0].Type != protocol.EventError {
		t.Fatalf("expected a single error event, got %v", eventTypes(events))
	}
	var errorPayload protocol.ErrorPayload
	if err := events[0].UnmarshalPayload(&errorPayload); err != nil {
		t.Fatalf("failed to unmarshal error payload: %v", err)
	}
	if !errorPayload.Fatal {
		t.Errorf("expected fatal error payload, got %+v", errorPayload)
	}
}

func TestPiRunner_MissingBinaryReportsFatalErrorEvent(t *testing.T) {
	sink := newMockSink(context.Background())
	r := runner.NewPiRunner(runner.PiRunnerOptions{Binary: filepath.Join(t.TempDir(), "does-not-exist")})

	if err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "run"}, sink); err == nil {
		t.Fatal("expected an error when the pi binary cannot be started, got nil")
	}

	want := []protocol.EventType{protocol.EventError}
	if got := eventTypes(sink.Events()); !reflect.DeepEqual(got, want) {
		t.Fatalf("expected a single fatal error event, got %v", got)
	}

	var errorPayload protocol.ErrorPayload
	if err := sink.Events()[0].UnmarshalPayload(&errorPayload); err != nil {
		t.Fatalf("failed to unmarshal error payload: %v", err)
	}
	if !errorPayload.Fatal || errorPayload.Code != "PI_START_ERROR" {
		t.Errorf("unexpected error payload: %+v", errorPayload)
	}
}

func TestPiRunner_EmitsNoCompletionOnNonZeroExit(t *testing.T) {
	stream := `{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"partial"}}
`
	script := writeFakePi(t, fakePiScript("", stream, 1))
	sink := newMockSink(context.Background())
	r := runner.NewPiRunner(runner.PiRunnerOptions{Binary: script})

	if err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "partial"}, sink); err == nil {
		t.Fatal("expected an error, got nil")
	}

	want := []protocol.EventType{protocol.EventToken, protocol.EventError}
	if got := eventTypes(sink.Events()); !reflect.DeepEqual(got, want) {
		t.Fatalf("expected event sequence %v, got %v", want, got)
	}
}

func TestPiRunner_ContextCancellationStopsStreaming(t *testing.T) {
	stream := `{"type":"message_update","message":{"role":"assistant"},"assistantMessageEvent":{"type":"text_delta","contentIndex":0,"delta":"start"}}
`
	script := writeFakePi(t, fakePiScript("", stream, 0)+"sleep 30\n")

	sink := newMockSink(context.Background())
	firstEvent := make(chan struct{})
	sink.onEvent = func(evt protocol.Event) {
		if evt.Type == protocol.EventToken {
			close(firstEvent)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := runner.NewPiRunner(runner.PiRunnerOptions{Binary: script})
	errCh := make(chan error, 1)
	go func() {
		errCh <- r.Run(ctx, protocol.TaskRequest{Agent: "worker", Task: "long running"}, sink)
	}()

	select {
	case <-firstEvent:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the first streamed token")
	}

	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner did not stop after context cancellation")
	}

	if got := eventTypes(sink.Events()); !reflect.DeepEqual(got, []protocol.EventType{protocol.EventToken}) {
		t.Errorf("expected only the pre-cancellation token event, got %v", got)
	}
}

func TestPiRunner_UsesWorkspaceRoot(t *testing.T) {
	optsRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("failed to resolve temp dir: %v", err)
	}
	reqRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("failed to resolve temp dir: %v", err)
	}

	optsPwdPath := filepath.Join(t.TempDir(), "pwd-opts.txt")
	optsScript := writeFakePi(t, fmt.Sprintf("pwd -P > %s\n", shellQuote(optsPwdPath))+fakePiScript("", "", 0))

	r := runner.NewPiRunner(runner.PiRunnerOptions{Binary: optsScript, WorkspaceRoot: optsRoot})
	if err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "cwd"}, newMockSink(context.Background())); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if gotPwd := readLines(t, optsPwdPath); len(gotPwd) != 1 || gotPwd[0] != optsRoot {
		t.Errorf("expected pi cwd %q, got %v", optsRoot, gotPwd)
	}

	reqPwdPath := filepath.Join(t.TempDir(), "pwd-req.txt")
	reqScript := writeFakePi(t, fmt.Sprintf("pwd -P > %s\n", shellQuote(reqPwdPath))+fakePiScript("", "", 0))

	reqRunner := runner.NewPiRunner(runner.PiRunnerOptions{Binary: reqScript, WorkspaceRoot: optsRoot})
	if err := reqRunner.Run(context.Background(), protocol.TaskRequest{
		Agent:         "worker",
		Task:          "cwd override",
		WorkspaceRoot: reqRoot,
	}, newMockSink(context.Background())); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if gotPwd := readLines(t, reqPwdPath); len(gotPwd) != 1 || gotPwd[0] != reqRoot {
		t.Errorf("expected per-request cwd %q to win, got %v", reqRoot, gotPwd)
	}
}

func TestPiRunner_RejectsInvalidInputs(t *testing.T) {
	t.Run("nil sink", func(t *testing.T) {
		r := runner.NewPiRunner(runner.PiRunnerOptions{Binary: "pi"})
		if err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "x"}, nil); err == nil {
			t.Fatal("expected an error for a nil sink, got nil")
		}
	})

	t.Run("empty prompt never spawns pi", func(t *testing.T) {
		sink := newMockSink(context.Background())
		r := runner.NewPiRunner(runner.PiRunnerOptions{Binary: filepath.Join(t.TempDir(), "missing-pi")})

		err := r.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "   "}, sink)
		if err == nil {
			t.Fatal("expected an error for an empty prompt, got nil")
		}
		if !strings.Contains(err.Error(), "prompt") {
			t.Errorf("expected a prompt validation error, got: %v", err)
		}
		if len(sink.Events()) != 0 {
			t.Errorf("expected no events for an invalid request, got %v", eventTypes(sink.Events()))
		}
	})

	t.Run("already cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		sink := newMockSink(ctx)
		r := runner.NewPiRunner(runner.PiRunnerOptions{Binary: filepath.Join(t.TempDir(), "missing-pi")})

		if err := r.Run(ctx, protocol.TaskRequest{Agent: "worker", Task: "x"}, sink); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", err)
		}
		if len(sink.Events()) != 0 {
			t.Errorf("expected no events for a cancelled run, got %v", eventTypes(sink.Events()))
		}
	})
}
