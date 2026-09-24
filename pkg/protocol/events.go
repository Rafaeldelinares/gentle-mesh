package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// EventType identifies the kind of SSE event streamed from the server.
type EventType string

const (
	EventStatus     EventType = "status"
	EventThought    EventType = "thought"
	EventToken      EventType = "token"
	EventToolCall   EventType = "tool_call"
	EventToolResult EventType = "tool_result"
	EventQuery      EventType = "query"
	EventCompletion EventType = "completion"
	EventError      EventType = "error"
)

// Event represents a single discrete protocol message streamed over SSE or stored in task logs.
type Event struct {
	ID        int64           `json:"id"`
	TaskID    string          `json:"task_id"`
	Timestamp int64           `json:"timestamp"`
	Type      EventType       `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

// StatusPayload carries task queue/execution state changes.
type StatusPayload struct {
	Status        TaskStatus `json:"status"`
	Message       string     `json:"message,omitempty"`
	QueuePosition int        `json:"queue_position,omitempty"`
	ActiveSlots   int        `json:"active_slots,omitempty"`
}

// ThoughtPayload carries internal agent reasoning or chain-of-thought tokens.
type ThoughtPayload struct {
	Text string `json:"text"`
}

// TokenPayload carries a streamed fragment of the subagent's final answer text.
// Tokens are incremental: a consumer must concatenate them in arrival order to
// reconstruct the completion message.
type TokenPayload struct {
	Text string `json:"text"`
}

// ToolCallPayload carries subagent tool invocation arguments.
type ToolCallPayload struct {
	CallID string         `json:"call_id"`
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
}

// ToolResultPayload carries the execution output of a tool.
type ToolResultPayload struct {
	CallID  string `json:"call_id"`
	Output  string `json:"output"`
	IsError bool   `json:"is_error,omitempty"`
}

// QueryPayload carries an interactive prompt requiring human or orchestrator response.
type QueryPayload struct {
	QueryID string   `json:"query_id"`
	Prompt  string   `json:"prompt"`
	Choices []string `json:"choices,omitempty"`
}

// CompletionPayload carries the terminal success state of a subagent run.
//
// Result and Text are interchangeable aliases for the completion message: the
// Gentle Mesh CLI reads "result" while Open Pi Viewer reads "text". Both fields
// are kept in sync by Normalize, MarshalJSON and UnmarshalJSON so either consumer
// observes the message content.
type CompletionPayload struct {
	Result       string   `json:"result"`
	Text         string   `json:"text,omitempty"`
	CommitHash   string   `json:"commit_hash,omitempty"`
	Branch       string   `json:"branch,omitempty"`
	FilesChanged []string `json:"files_changed,omitempty"`
	DurationMs   int64    `json:"duration_ms,omitempty"`
}

// completionPayloadAlias breaks the method set of CompletionPayload so the
// custom JSON codecs can delegate to the default struct encoding.
type completionPayloadAlias CompletionPayload

// Normalize mirrors Result and Text when only one of them is populated, keeping
// payloads created in Go and payloads decoded from JSON interchangeable across
// consumers that expect either field.
func (c *CompletionPayload) Normalize() {
	if c == nil {
		return
	}
	if c.Text == "" && c.Result != "" {
		c.Text = c.Result
	}
	if c.Result == "" && c.Text != "" {
		c.Result = c.Text
	}
}

// MarshalJSON normalizes the payload before encoding so both "result" and
// "text" are emitted for any populated completion message.
func (c CompletionPayload) MarshalJSON() ([]byte, error) {
	c.Normalize()
	return json.Marshal(completionPayloadAlias(c))
}

// UnmarshalJSON decodes a completion payload and normalizes it so a payload
// carrying only "text" also populates Result, and vice-versa.
func (c *CompletionPayload) UnmarshalJSON(data []byte) error {
	var decoded completionPayloadAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	payload := CompletionPayload(decoded)
	payload.Normalize()
	*c = payload
	return nil
}

// ErrorPayload carries terminal or non-fatal task error information.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fatal   bool   `json:"fatal,omitempty"`
}

// NewEvent constructs a new Event with serialized payload and current Unix timestamp if 0.
func NewEvent(id int64, taskID string, eventType EventType, payload any) (*Event, error) {
	var raw json.RawMessage
	if payload != nil {
		switch p := payload.(type) {
		case json.RawMessage:
			raw = p
		case []byte:
			raw = json.RawMessage(p)
		default:
			b, err := json.Marshal(p)
			if err != nil {
				return nil, fmt.Errorf("failed to marshal event payload: %w", err)
			}
			raw = b
		}
	}

	return &Event{
		ID:        id,
		TaskID:    taskID,
		Timestamp: time.Now().Unix(),
		Type:      eventType,
		Payload:   raw,
	}, nil
}

// UnmarshalPayload deserializes the Event's raw payload into the destination pointer.
func (e *Event) UnmarshalPayload(dest any) error {
	if e == nil || len(e.Payload) == 0 {
		return errors.New("event payload is empty")
	}
	return json.Unmarshal(e.Payload, dest)
}

// FormatSSE outputs the event in standard Server-Sent Events text format:
// id: <ID>\nevent: <Type>\ndata: <JSON-of-Event>\n\n
func (e *Event) FormatSSE() []byte {
	if e == nil {
		return nil
	}
	data, err := json.Marshal(e)
	if err != nil {
		return nil
	}
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "id: %d\n", e.ID)
	fmt.Fprintf(&buf, "event: %s\n", e.Type)
	fmt.Fprintf(&buf, "data: %s\n\n", data)
	return buf.Bytes()
}

// ParseSSE parses an SSE-formatted block back into an Event struct.
func ParseSSE(raw []byte) (*Event, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("empty sse payload")
	}

	var (
		idHeader    int64
		hasID       bool
		eventHeader string
		dataLines   []string
	)

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := scanner.Text()
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, ":") {
			// SSE comment line
			continue
		}
		if line == "" {
			continue
		}

		colonIdx := strings.Index(line, ":")
		if colonIdx == -1 {
			continue
		}
		field := line[:colonIdx]
		val := line[colonIdx+1:]
		if strings.HasPrefix(val, " ") {
			val = val[1:]
		}

		switch field {
		case "id":
			id, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid event id %q: %w", val, err)
			}
			idHeader = id
			hasID = true
		case "event":
			eventHeader = val
		case "data":
			dataLines = append(dataLines, val)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanner error while reading sse: %w", err)
	}

	if len(dataLines) == 0 {
		return nil, errors.New("missing data field in sse event")
	}

	dataStr := strings.Join(dataLines, "\n")
	var evt Event
	if err := json.Unmarshal([]byte(dataStr), &evt); err != nil {
		return nil, fmt.Errorf("failed to unmarshal sse data json: %w", err)
	}

	if evt.ID == 0 && hasID {
		evt.ID = idHeader
	}
	if evt.Type == "" && eventHeader != "" {
		evt.Type = EventType(eventHeader)
	}

	return &evt, nil
}

// ParseSSE method on *Event parses an SSE block and updates the receiver.
func (e *Event) ParseSSE(raw []byte) (*Event, error) {
	parsed, err := ParseSSE(raw)
	if err != nil {
		return nil, err
	}
	if e != nil {
		*e = *parsed
	}
	return parsed, nil
}
