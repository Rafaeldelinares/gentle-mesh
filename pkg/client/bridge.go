// Package client provides the local side of the Gentle Mesh transport. The
// Bridge adapts the Pi subagent command protocol (newline-delimited JSON on
// stdin/stdout) onto the Gentle Mesh coordinator HTTP/SSE API, translating
// remote events (thought, tool_call, tool_result, completion, status) into the
// viewer event bus (message_start, message_update, tool_execution_start,
// tool_execution_end, message_end, agent_settled).
package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

const (
	defaultSessionID = "session-mesh-default"
	modelID          = "gentle-mesh"
	modelProvider    = "gentle-mesh"
)

// maxCommandLine bounds a single newline-delimited command read from the input
// stream so a corrupted local client cannot exhaust bridge memory.
const maxCommandLine = 8 << 20

// Config configures a Bridge.
type Config struct {
	// CoordinatorURL is the base URL of the Gentle Mesh coordinator.
	CoordinatorURL string
	// Token is an optional bearer token sent to the coordinator.
	Token string
	// Agent is the subagent role dispatched for each prompt command.
	Agent string
	// HTTPClient is an optional HTTP client. When nil, http.DefaultClient is
	// used. Tests inject one so the coordinator test server is trusted.
	HTTPClient *http.Client
	// CACertFile is the path to the mesh CA certificate for TLS verification.
	// If empty, uses system default CA pool.
	CACertFile string
	// InsecureSkipTLSVerify skips TLS verification (for development only).
	InsecureSkipTLSVerify bool
}

// Bridge adapts Pi subagent commands to the Gentle Mesh coordinator API.
type Bridge struct {
	config Config
}

// NewBridge builds a Bridge from cfg.
func NewBridge(cfg Config) *Bridge {
	return &Bridge{config: cfg}
}

// clientCommand is a single newline-delimited command received on stdin.
type clientCommand struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

// lineWriter serializes whole JSON lines written from concurrent prompt
// goroutines so a reader never observes interleaved frames.
type lineWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (lw *lineWriter) write(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	data = append(data, '\n')

	lw.mu.Lock()
	defer lw.mu.Unlock()
	_, _ = lw.w.Write(data)
}

// session holds the per-Serve mutable state. Keeping it local to Serve avoids
// sharing cancel state or output writers across concurrent Serve calls.
type session struct {
	bridge  *Bridge
	out     *lineWriter
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func (s *session) setCancel(id string, cancel context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancels[id] = cancel
}

func (s *session) clearCancel(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cancels, id)
}

func (s *session) abortAll() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.cancels))
	for _, cancel := range s.cancels {
		cancels = append(cancels, cancel)
	}
	s.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
}

func (b *Bridge) httpClient() *http.Client {
	if b.config.HTTPClient != nil {
		return b.config.HTTPClient
	}

	// Build custom client with TLS configuration if needed
	if b.config.CACertFile != "" || b.config.InsecureSkipTLSVerify {
		tlsConfig := &tls.Config{}

		if b.config.InsecureSkipTLSVerify {
			tlsConfig.InsecureSkipVerify = true
		} else if b.config.CACertFile != "" {
			// Load custom CA certificate
			caCert, err := os.ReadFile(b.config.CACertFile)
			if err != nil {
				// Fall back to default client if CA file can't be read
				return http.DefaultClient
			}
			caPool := x509.NewCertPool()
			if !caPool.AppendCertsFromPEM(caCert) {
				return http.DefaultClient
			}
			tlsConfig.RootCAs = caPool
		}

		return &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: tlsConfig,
			},
		}
	}

	return http.DefaultClient
}

// resolveURL joins a coordinator path or absolute events URL with the base.
func (b *Bridge) resolveURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	base := strings.TrimRight(b.config.CoordinatorURL, "/")
	if path == "" {
		return base
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

func (b *Bridge) authorize(req *http.Request) {
	if b.config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+b.config.Token)
	}
}

// Serve reads newline-delimited JSON commands from in and writes
// newline-delimited JSON responses and viewer events to out. It returns when
// the input reaches EOF and every in-flight prompt has finished.
func (b *Bridge) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}

	s := &session{
		bridge:  b,
		out:     &lineWriter{w: out},
		cancels: make(map[string]context.CancelFunc),
	}

	var wg sync.WaitGroup
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 0, 64*1024), maxCommandLine)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var cmd clientCommand
		if err := json.Unmarshal(line, &cmd); err != nil {
			// Malformed lines are ignored so one bad frame does not abort the
			// whole bridge session.
			continue
		}

		switch cmd.Type {
		case "get_state":
			s.writeState(cmd.ID)
		case "get_messages":
			s.writeMessages(cmd.ID)
		case "new_session":
			s.writeNewSession(cmd.ID)
		case "abort":
			s.abortAll()
			s.out.write(map[string]any{
				"id":      cmd.ID,
				"type":    "response",
				"command": "abort",
				"success": true,
			})
		case "prompt":
			promptCtx, cancel := context.WithCancel(ctx)
			s.setCancel(cmd.ID, cancel)
			wg.Add(1)
			go func(c clientCommand, cctx context.Context, ccancel context.CancelFunc) {
				defer wg.Done()
				defer s.clearCancel(c.ID)
				defer ccancel()
				s.handlePrompt(cctx, c)
			}(cmd, promptCtx, cancel)
		default:
			// Unknown commands are ignored for forward compatibility.
		}
	}

	// Wait for in-flight prompts so no writer outlives Serve and every emitted
	// event reaches out before the caller closes it.
	wg.Wait()

	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func (s *session) writeState(id string) {
	s.out.write(map[string]any{
		"id":      id,
		"type":    "response",
		"command": "get_state",
		"success": true,
		"data": map[string]any{
			"model": map[string]any{
				"id":       modelID,
				"name":     "Gentle Mesh (" + s.bridge.config.CoordinatorURL + ")",
				"provider": modelProvider,
			},
			"sessionId":    defaultSessionID,
			"messageCount": 0,
		},
	})
}

func (s *session) writeMessages(id string) {
	s.out.write(map[string]any{
		"id":      id,
		"type":    "response",
		"command": "get_messages",
		"success": true,
		"data": map[string]any{
			"messages": []any{},
		},
	})
}

func (s *session) writeNewSession(id string) {
	s.out.write(map[string]any{
		"id":      id,
		"type":    "response",
		"command": "new_session",
		"success": true,
		"data": map[string]any{
			"sessionId":   fmt.Sprintf("session-mesh-%d", time.Now().UnixNano()),
			"sessionFile": nil,
		},
	})
}

// handlePrompt acknowledges the prompt, opens the coordinator task stream and
// translates remote events into viewer events. It always terminates the local
// message with agent_settled (once) and message_end.
func (s *session) handlePrompt(ctx context.Context, cmd clientCommand) {
	msgID := "msg-" + cmd.ID

	// Acknowledge immediately so the local client is not blocked while the
	// remote task is created and executed.
	s.out.write(map[string]any{
		"id":      cmd.ID,
		"type":    "response",
		"command": "prompt",
		"success": true,
	})
	s.out.write(map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":   msgID,
			"role": "assistant",
		},
	})

	var accumulated strings.Builder
	settled := false

	taskResp, err := s.dispatchTask(ctx, cmd.Message)
	if err == nil {
		eventsURL := taskResp.EventsURL
		if eventsURL == "" && taskResp.TaskID != "" {
			eventsURL = "/v1/tasks/" + taskResp.TaskID + "/events"
		}
		if eventsURL != "" {
			s.streamEvents(ctx, eventsURL, msgID, &accumulated, &settled)
		}
	}

	if !settled {
		s.emitSettled()
		settled = true
	}
	s.emitMessageEnd(msgID, accumulated.String())
}

// dispatchTask creates a remote task on the coordinator for the prompt message.
func (s *session) dispatchTask(ctx context.Context, message string) (*protocol.TaskResponse, error) {
	body, err := json.Marshal(protocol.TaskRequest{
		Prompt: message,
		Agent:  s.bridge.config.Agent,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.bridge.resolveURL("/v1/tasks"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	s.bridge.authorize(req)

	resp, err := s.bridge.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("coordinator returned status %d creating task", resp.StatusCode)
	}

	var taskResp protocol.TaskResponse
	if err := json.NewDecoder(resp.Body).Decode(&taskResp); err != nil {
		return nil, err
	}
	return &taskResp, nil
}

// streamEvents consumes a coordinator SSE stream and translates each frame into
// viewer events until the stream ends or ctx is cancelled.
func (s *session) streamEvents(ctx context.Context, eventsURL, msgID string, accumulated *strings.Builder, settled *bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.bridge.resolveURL(eventsURL), nil)
	if err != nil {
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	s.bridge.authorize(req)

	resp, err := s.bridge.httpClient().Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return
	}

	reader := bufio.NewReader(resp.Body)
	var (
		eventType string
		dataLines []string
	)

	flush := func() {
		if eventType == "" && len(dataLines) == 0 {
			return
		}
		s.handleEventFrame(eventType, strings.Join(dataLines, "\n"), msgID, accumulated, settled)
		eventType = ""
		dataLines = dataLines[:0]
	}

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case trimmed == "":
				flush()
			case strings.HasPrefix(trimmed, ":"):
				// SSE comment (e.g. heartbeat ping).
			default:
				field, value := splitSSEField(trimmed)
				switch field {
				case "event":
					eventType = value
				case "data":
					dataLines = append(dataLines, value)
				}
			}
		}
		if err != nil {
			// Flush a trailing frame that ended without a blank separator.
			flush()
			return
		}
		if ctx.Err() != nil {
			flush()
			return
		}
	}
}

// splitSSEField splits an SSE line into its field name and value, honoring the
// optional single space after the colon.
func splitSSEField(line string) (string, string) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return line, ""
	}
	field := line[:idx]
	value := line[idx+1:]
	return field, strings.TrimPrefix(value, " ")
}

// resolveEventPayload determines the event type and payload bytes for a frame.
// It supports both a bare payload with the type carried by the SSE "event"
// field and the coordinator's canonical Event envelope, where the type lives in
// the JSON "type" field and the body in "payload".
func resolveEventPayload(eventType, rawData string) (string, json.RawMessage) {
	payload := json.RawMessage(rawData)

	var envelope struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(rawData), &envelope); err == nil {
		if envelope.Type != "" && len(envelope.Payload) > 0 {
			return envelope.Type, envelope.Payload
		}
		if eventType == "" {
			return envelope.Type, payload
		}
	}

	return eventType, payload
}

func (s *session) handleEventFrame(eventType, rawData, msgID string, accumulated *strings.Builder, settled *bool) {
	evtType, payload := resolveEventPayload(eventType, rawData)

	switch evtType {
	case string(protocol.EventThought):
		var p protocol.ThoughtPayload
		if err := json.Unmarshal(payload, &p); err == nil && p.Text != "" {
			s.emitMessageUpdate(msgID, "thinking_delta", p.Text)
		}

	case string(protocol.EventToolCall):
		var p protocol.ToolCallPayload
		if err := json.Unmarshal(payload, &p); err == nil {
			s.emitToolStart(p.CallID, p.Tool, p.Args)
		}

	case string(protocol.EventToolResult):
		var p protocol.ToolResultPayload
		if err := json.Unmarshal(payload, &p); err == nil {
			s.emitToolEnd(p.CallID, p.Output, p.IsError)
		}

	case string(protocol.EventCompletion):
		var p protocol.CompletionPayload
		if err := json.Unmarshal(payload, &p); err == nil {
			text := p.Result
			if text == "" {
				text = p.Text
			}
			if text != "" {
				accumulated.WriteString(text)
			}
			s.emitMessageUpdate(msgID, "text_delta", text)
		}

	case string(protocol.EventStatus):
		var p protocol.StatusPayload
		if err := json.Unmarshal(payload, &p); err == nil {
			switch string(p.Status) {
			case string(protocol.TaskStatusCompleted),
				string(protocol.TaskStatusFailed),
				string(protocol.TaskStatusCanceled),
				"cancelled":
				if !*settled {
					s.emitSettled()
					*settled = true
				}
			}
		}
	}
}

func (s *session) emitMessageUpdate(msgID, deltaType, delta string) {
	s.out.write(map[string]any{
		"type": "message_update",
		"message": map[string]any{
			"id":   msgID,
			"role": "assistant",
		},
		"assistantMessageEvent": map[string]any{
			"type":  deltaType,
			"delta": delta,
		},
	})
}

func (s *session) emitToolStart(callID, toolName string, args map[string]any) {
	if args == nil {
		args = map[string]any{}
	}
	s.out.write(map[string]any{
		"type":       "tool_execution_start",
		"toolCallId": callID,
		"toolName":   toolName,
		"args":       args,
	})
}

func (s *session) emitToolEnd(callID, result string, isError bool) {
	s.out.write(map[string]any{
		"type":       "tool_execution_end",
		"toolCallId": callID,
		"result":     result,
		"isError":    isError,
	})
}

func (s *session) emitSettled() {
	s.out.write(map[string]any{"type": "agent_settled"})
}

func (s *session) emitMessageEnd(msgID, text string) {
	s.out.write(map[string]any{
		"type": "message_end",
		"message": map[string]any{
			"id":   msgID,
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "text", "text": text},
			},
		},
	})
}
