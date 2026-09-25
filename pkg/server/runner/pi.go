package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

const (
	// maxPiEventLine bounds a single newline-delimited JSON event read from the
	// Pi process so a corrupted or hostile child cannot exhaust runner memory.
	maxPiEventLine = 8 << 20
	// maxPiStderr bounds the captured stderr kept for failure diagnostics.
	maxPiStderr = 64 << 10
)

// Pi JSON stream event names, as emitted by `pi --print --mode json`.
const (
	piEventMessageUpdate      = "message_update"
	piEventMessageEnd         = "message_end"
	piEventToolExecutionStart = "tool_execution_start"
	piEventToolExecutionEnd   = "tool_execution_end"

	piDeltaThinking = "thinking_delta"
	piDeltaText     = "text_delta"
)

// PiRunnerOptions configures a PiRunner.
type PiRunnerOptions struct {
	// Binary is the Pi CLI executable. When empty, "pi" is resolved from PATH.
	Binary string
	// WorkspaceRoot is the default working directory for the Pi process. A
	// non-empty TaskRequest.WorkspaceRoot takes precedence.
	WorkspaceRoot string
	// ExtraArgs are appended to the Pi command line before the prompt, for
	// example --model or --thinking overrides.
	ExtraArgs []string
}

// PiRunner executes tasks by spawning the local Pi CLI in non-interactive JSON
// mode and translating its event stream into gentle-mesh protocol events.
//
// The translation is the inverse of the Pi-viewer bridge in pkg/client:
// reasoning deltas become thought events, assistant text deltas become token
// events, tool execution becomes tool call and tool result events, and the
// accumulated answer becomes a single completion event.
type PiRunner struct {
	opts PiRunnerOptions
}

// NewPiRunner constructs a PiRunner, defaulting the Pi binary name to "pi".
func NewPiRunner(opts PiRunnerOptions) *PiRunner {
	if opts.Binary == "" {
		opts.Binary = "pi"
	}
	return &PiRunner{opts: opts}
}

// Options returns a copy of the runner's configured options.
func (r *PiRunner) Options() PiRunnerOptions {
	opts := r.opts
	opts.ExtraArgs = append([]string(nil), r.opts.ExtraArgs...)
	return opts
}

// Run spawns Pi for req and streams translated events into sink. It returns nil
// only when Pi exited successfully; a non-zero exit emits a fatal error event
// and returns the failure, and context cancellation returns the context error.
func (r *PiRunner) Run(ctx context.Context, req protocol.TaskRequest, sink EventSink) error {
	if sink == nil {
		return errors.New("event sink is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	prompt := req.Task
	if prompt == "" {
		prompt = req.Prompt
	}
	if strings.TrimSpace(prompt) == "" {
		return errors.New("task prompt is empty: set TaskRequest.Task or TaskRequest.Prompt")
	}

	// A run-scoped context lets the runner stop the child before waiting, so a
	// child blocked on a full stdout pipe can never deadlock Wait.
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	cmd := exec.CommandContext(runCtx, r.opts.Binary, r.args(req, prompt)...)
	cmd.Dir = r.workDir(req)

	var stderr cappedBuffer
	stderr.limit = maxPiStderr
	cmd.Stderr = &stderr

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return r.failRun(sink, "PI_START_ERROR", fmt.Errorf("failed to open pi stdout: %w", err))
	}

	if err := cmd.Start(); err != nil {
		return r.failRun(sink, "PI_START_ERROR", fmt.Errorf("failed to start pi (%s): %w", r.opts.Binary, err))
	}

	state := &piStream{sink: sink}
	readErr := state.consume(runCtx, stdout)
	if readErr != nil {
		cancelRun()
	}
	waitErr := cmd.Wait()

	// Caller cancellation wins over any incidental stream or exit error.
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Emit error event for timeout/cancellation so the client knows
		switch ctxErr {
		case context.DeadlineExceeded:
			r.failRun(sink, "TASK_TIMEOUT", fmt.Errorf("task exceeded timeout"))
		case context.Canceled:
			r.failRun(sink, "TASK_CANCELED", fmt.Errorf("task was canceled"))
		default:
			r.failRun(sink, "TASK_ERROR", ctxErr)
		}
		return ctxErr
	}
	if readErr != nil {
		return r.failRun(sink, "PI_STREAM_ERROR", readErr)
	}

	if waitErr != nil {
		exitErr := fmt.Errorf("pi exited with error: %w", waitErr)
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			exitErr = fmt.Errorf("%w: %s", exitErr, detail)
		}
		return r.failRun(sink, "PI_EXIT_ERROR", exitErr)
	}

	text := state.fullText.String()
	return state.emitEvent(protocol.EventCompletion, protocol.CompletionPayload{
		Result: text,
		Text:   text,
	})
}

// failRun records a fatal error event and returns the underlying failure. The
// scheduler dispatches runners without inspecting Run's return value, so an
// unemitted failure would leave the task running forever.
func (r *PiRunner) failRun(sink EventSink, code string, err error) error {
	if _, emitErr := sink.EmitEvent(protocol.EventError, protocol.ErrorPayload{
		Code:    code,
		Message: err.Error(),
		Fatal:   true,
	}); emitErr != nil {
		return fmt.Errorf("%w (failed to emit error event: %v)", err, emitErr)
	}
	return err
}

// args builds the Pi command line. The prompt is passed after "--" so a prompt
// starting with a dash is never parsed as a flag.
func (r *PiRunner) args(req protocol.TaskRequest, prompt string) []string {
	args := []string{"--print", "--mode", "json"}
	if req.SessionID != "" {
		args = append(args, "--session-id", req.SessionID)
	}
	args = append(args, r.opts.ExtraArgs...)
	return append(args, "--", prompt)
}

// workDir resolves the Pi working directory, preferring the per-request
// workspace root over the runner default.
func (r *PiRunner) workDir(req protocol.TaskRequest) string {
	if req.WorkspaceRoot != "" {
		return req.WorkspaceRoot
	}
	return r.opts.WorkspaceRoot
}

// piStream translates Pi JSON events into gentle-mesh events while accumulating
// the assistant's answer text.
type piStream struct {
	sink     EventSink
	fullText strings.Builder
}

// consume reads newline-delimited JSON events until the stream ends.
func (s *piStream) consume(ctx context.Context, out io.Reader) error {
	scanner := bufio.NewScanner(out)
	scanner.Buffer(make([]byte, 0, 64*1024), maxPiEventLine)

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}

		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		// Non-JSON diagnostics on stdout are ignored so one stray line cannot
		// abort an otherwise healthy run.
		var evt piEvent
		if err := json.Unmarshal(line, &evt); err != nil {
			continue
		}

		if err := s.handle(evt); err != nil {
			return err
		}
	}

	return scanner.Err()
}

func (s *piStream) handle(evt piEvent) error {
	switch evt.Type {
	case piEventMessageUpdate:
		return s.handleMessageUpdate(evt)
	case piEventMessageEnd:
		s.handleMessageEnd(evt)
		return nil
	case piEventToolExecutionStart:
		args := evt.Args
		if args == nil {
			args = map[string]any{}
		}
		return s.emitEvent(protocol.EventToolCall, protocol.ToolCallPayload{
			CallID: evt.ToolCallID,
			Tool:   evt.ToolName,
			Args:   args,
		})
	case piEventToolExecutionEnd:
		return s.emitEvent(protocol.EventToolResult, protocol.ToolResultPayload{
			CallID:  evt.ToolCallID,
			Output:  toolResultText(evt.Result),
			IsError: evt.IsError,
		})
	default:
		// agent_start, agent_settled, turn boundaries and future event types
		// carry no translation for the mesh protocol.
		return nil
	}
}

func (s *piStream) handleMessageUpdate(evt piEvent) error {
	update := evt.AssistantMessageEvent
	if update == nil || update.Delta == "" {
		return nil
	}

	switch update.Type {
	case piDeltaThinking:
		return s.emitEvent(protocol.EventThought, protocol.ThoughtPayload{Text: update.Delta})
	case piDeltaText:
		s.fullText.WriteString(update.Delta)
		return s.emitEvent(protocol.EventToken, protocol.TokenPayload{Text: update.Delta})
	default:
		// text_start/text_end/thinking_start/thinking_end/toolcall_* and usage
		// events carry no standalone content.
		return nil
	}
}

// handleMessageEnd supplies the completion text when the stream carried no
// assistant text deltas, so a non-streaming answer is still reported.
func (s *piStream) handleMessageEnd(evt piEvent) {
	if s.fullText.Len() > 0 || evt.Message == nil || evt.Message.Role != "assistant" {
		return
	}
	s.fullText.WriteString(messageText(evt.Message))
}

func (s *piStream) emitEvent(eventType protocol.EventType, payload any) error {
	_, err := s.sink.EmitEvent(eventType, payload)
	return err
}

// messageText concatenates the text parts of an assistant message, ignoring
// thinking and tool-call parts.
func messageText(msg *piAssistantMessage) string {
	var b strings.Builder
	for _, part := range msg.Content {
		if part.Type == "text" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// toolResultText renders a Pi tool result for the mesh tool-result payload.
// String results are unwrapped; structured results keep their compact JSON.
func toolResultText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}

	var asString string
	if err := json.Unmarshal(trimmed, &asString); err == nil {
		return asString
	}
	return string(trimmed)
}

// piEvent is the subset of the Pi JSON event stream translated by PiRunner.
type piEvent struct {
	Type                  string              `json:"type"`
	AssistantMessageEvent *piAssistantEvent   `json:"assistantMessageEvent,omitempty"`
	ToolCallID            string              `json:"toolCallId,omitempty"`
	ToolName              string              `json:"toolName,omitempty"`
	Args                  map[string]any      `json:"args,omitempty"`
	Result                json.RawMessage     `json:"result,omitempty"`
	IsError               bool                `json:"isError,omitempty"`
	Message               *piAssistantMessage `json:"message,omitempty"`
}

// piAssistantEvent is an incremental assistant content delta.
type piAssistantEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta,omitempty"`
}

// piAssistantMessage is the assistant message snapshot carried by message_end.
type piAssistantMessage struct {
	Role    string          `json:"role,omitempty"`
	Content []piContentPart `json:"content,omitempty"`
}

// piContentPart is a single assistant message content block.
type piContentPart struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

// cappedBuffer keeps at most limit bytes and silently discards the remainder,
// so a chatty child cannot grow the runner's memory without bound.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if remaining := c.limit - c.buf.Len(); remaining > 0 {
		if len(p) > remaining {
			c.buf.Write(p[:remaining])
		} else {
			c.buf.Write(p)
		}
	}
	return len(p), nil
}

func (c *cappedBuffer) String() string {
	return c.buf.String()
}
