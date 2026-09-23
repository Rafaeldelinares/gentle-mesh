package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

// DefaultTokens provides a realistic subagent token streaming sequence.
var DefaultTokens = []string{
	"Analyzing",
	" task...",
	"\n\n",
	"Reading",
	" files...",
	"\n",
	"Done!",
}

// SimulatedToolCall defines a simulated tool invocation and result.
type SimulatedToolCall struct {
	ToolName string        `json:"tool_name"`
	Input    any           `json:"input"`
	Result   any           `json:"result"`
	Delay    time.Duration `json:"delay,omitempty"`
}

// SimulatedQuery defines an interactive query prompt requiring a reply.
type SimulatedQuery struct {
	QueryID  string        `json:"query_id"`
	Question string        `json:"question"`
	Timeout  time.Duration `json:"timeout,omitempty"`
}

// SimulatedOptions configures the behavior of a SimulatedRunner.
type SimulatedOptions struct {
	Tokens           []string            `json:"tokens,omitempty"`
	TokenDelay       time.Duration       `json:"token_delay,omitempty"`
	ToolCalls        []SimulatedToolCall `json:"tool_calls,omitempty"`
	Query            *SimulatedQuery     `json:"query,omitempty"`
	FailWithError    error               `json:"-"`
	CompletionResult string              `json:"completion_result,omitempty"`
	FilesChanged     []string            `json:"files_changed,omitempty"`
}

// SimulatedRunner executes tasks by emitting a configurable sequence of simulated events.
type SimulatedRunner struct {
	opts SimulatedOptions
}

// NewSimulatedRunner constructs a SimulatedRunner with the provided options.
// If Tokens is empty, it defaults to a copy of DefaultTokens.
func NewSimulatedRunner(opts SimulatedOptions) *SimulatedRunner {
	if len(opts.Tokens) == 0 {
		opts.Tokens = make([]string, len(DefaultTokens))
		copy(opts.Tokens, DefaultTokens)
	}
	if opts.CompletionResult == "" && opts.FailWithError == nil {
		opts.CompletionResult = "Task completed successfully"
	}
	return &SimulatedRunner{
		opts: opts,
	}
}

// Options returns a copy of the runner's configured options.
func (r *SimulatedRunner) Options() SimulatedOptions {
	return r.opts
}

// Run executes the task against sink by emitting simulated events according to opts.
func (r *SimulatedRunner) Run(ctx context.Context, req protocol.TaskRequest, sink EventSink) error {
	if sink == nil {
		return errors.New("event sink is nil")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	// 1. Emit status: running
	if _, err := sink.EmitEvent(protocol.EventStatus, protocol.StatusPayload{
		Status: protocol.TaskStatusRunning,
	}); err != nil {
		return err
	}

	// 2. Emit tool calls if configured
	for i, toolCall := range r.opts.ToolCalls {
		if err := ctx.Err(); err != nil {
			return err
		}

		if toolCall.Delay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(toolCall.Delay):
			}
		}

		callID := fmt.Sprintf("call_%d", i+1)
		var args map[string]any
		if toolCall.Input != nil {
			if m, ok := toolCall.Input.(map[string]any); ok {
				args = m
			} else if b, err := json.Marshal(toolCall.Input); err == nil {
				_ = json.Unmarshal(b, &args)
			}
		}
		if args == nil {
			args = make(map[string]any)
		}

		if _, err := sink.EmitEvent(protocol.EventToolCall, protocol.ToolCallPayload{
			CallID: callID,
			Tool:   toolCall.ToolName,
			Args:   args,
		}); err != nil {
			return err
		}

		var output string
		if toolCall.Result != nil {
			if s, ok := toolCall.Result.(string); ok {
				output = s
			} else if b, err := json.Marshal(toolCall.Result); err == nil {
				output = string(b)
			}
		}

		if _, err := sink.EmitEvent(protocol.EventToolResult, protocol.ToolResultPayload{
			CallID: callID,
			Output: output,
		}); err != nil {
			return err
		}
	}

	// 3. Emit interactive query if configured
	if r.opts.Query != nil {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Register query before emitting event so resolving cannot race
		replyChan := sink.RegisterQuery(r.opts.Query.QueryID)

		if _, err := sink.EmitEvent(protocol.EventQuery, protocol.QueryPayload{
			QueryID: r.opts.Query.QueryID,
			Prompt:  r.opts.Query.Question,
		}); err != nil {
			return err
		}

		var timeoutChan <-chan time.Time
		var timer *time.Timer
		if r.opts.Query.Timeout > 0 {
			timer = time.NewTimer(r.opts.Query.Timeout)
			timeoutChan = timer.C
		}

		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return ctx.Err()
		case <-timeoutChan:
			return errors.New("simulated query timed out")
		case answer, ok := <-replyChan:
			if timer != nil {
				timer.Stop()
			}
			if !ok {
				return errors.New("query channel closed")
			}
			_ = answer
		}
	}

	// 4. Stream tokens
	for _, token := range r.opts.Tokens {
		if err := ctx.Err(); err != nil {
			return err
		}

		if r.opts.TokenDelay > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(r.opts.TokenDelay):
			}
		}

		if _, err := sink.EmitEvent(EventToken, protocol.ThoughtPayload{
			Text: token,
		}); err != nil {
			return err
		}
	}

	// 5. Final completion or error
	if err := ctx.Err(); err != nil {
		return err
	}

	if r.opts.FailWithError != nil {
		if _, err := sink.EmitEvent(protocol.EventError, protocol.ErrorPayload{
			Code:    "SIMULATED_FAILURE",
			Message: r.opts.FailWithError.Error(),
			Fatal:   true,
		}); err != nil {
			return err
		}
		return r.opts.FailWithError
	}

	if _, err := sink.EmitEvent(protocol.EventCompletion, protocol.CompletionPayload{
		Result:       r.opts.CompletionResult,
		FilesChanged: r.opts.FilesChanged,
	}); err != nil {
		return err
	}

	return nil
}
