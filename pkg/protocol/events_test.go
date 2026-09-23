package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

func TestEventTypesConstants(t *testing.T) {
	tests := []struct {
		got  protocol.EventType
		want string
	}{
		{protocol.EventStatus, "status"},
		{protocol.EventThought, "thought"},
		{protocol.EventToolCall, "tool_call"},
		{protocol.EventToolResult, "tool_result"},
		{protocol.EventQuery, "query"},
		{protocol.EventCompletion, "completion"},
		{protocol.EventError, "error"},
	}

	for _, tc := range tests {
		if string(tc.got) != tc.want {
			t.Errorf("expected EventType %q, got %q", tc.want, tc.got)
		}
	}
}

func TestEventJSONSerialization(t *testing.T) {
	thoughtPayload := protocol.ThoughtPayload{
		Text: "Analyzing requirements for mesh protocol",
	}
	payloadBytes, err := json.Marshal(thoughtPayload)
	if err != nil {
		t.Fatalf("failed to marshal thought payload: %v", err)
	}

	event := protocol.Event{
		ID:        101,
		TaskID:    "task-mesh-01",
		Timestamp: 1727085600,
		Type:      protocol.EventThought,
		Payload:   payloadBytes,
	}

	data, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("failed to marshal event: %v", err)
	}

	var deserialized protocol.Event
	if err := json.Unmarshal(data, &deserialized); err != nil {
		t.Fatalf("failed to unmarshal event: %v", err)
	}

	if deserialized.ID != event.ID {
		t.Errorf("expected ID %d, got %d", event.ID, deserialized.ID)
	}
	if deserialized.TaskID != event.TaskID {
		t.Errorf("expected TaskID %q, got %q", event.TaskID, deserialized.TaskID)
	}
	if deserialized.Timestamp != event.Timestamp {
		t.Errorf("expected Timestamp %d, got %d", event.Timestamp, deserialized.Timestamp)
	}
	if deserialized.Type != event.Type {
		t.Errorf("expected Type %q, got %q", event.Type, deserialized.Type)
	}

	var parsedThought protocol.ThoughtPayload
	if err := json.Unmarshal(deserialized.Payload, &parsedThought); err != nil {
		t.Fatalf("failed to unmarshal thought from payload: %v", err)
	}
	if parsedThought.Text != thoughtPayload.Text {
		t.Errorf("expected thought text %q, got %q", thoughtPayload.Text, parsedThought.Text)
	}
}

func TestSpecializedPayloads(t *testing.T) {
	t.Run("StatusPayload", func(t *testing.T) {
		p := protocol.StatusPayload{
			Status:        protocol.TaskStatusRunning,
			Message:       "Working on task",
			QueuePosition: 2,
			ActiveSlots:   4,
		}
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		var decoded protocol.StatusPayload
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if decoded != p {
			t.Errorf("mismatch: got %+v, want %+v", decoded, p)
		}
	})

	t.Run("ToolCallPayload", func(t *testing.T) {
		p := protocol.ToolCallPayload{
			CallID: "call_abc123",
			Tool:   "execute_command",
			Args: map[string]any{
				"command": "go test ./...",
				"timeout": float64(60),
			},
		}
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		var decoded protocol.ToolCallPayload
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if decoded.CallID != p.CallID || decoded.Tool != p.Tool {
			t.Errorf("mismatch in CallID or Tool: %+v", decoded)
		}
		if decoded.Args["command"] != "go test ./..." {
			t.Errorf("expected command arg, got %v", decoded.Args["command"])
		}
	})

	t.Run("ToolResultPayload", func(t *testing.T) {
		p := protocol.ToolResultPayload{
			CallID:  "call_abc123",
			Output:  "PASS\nok 0.05s",
			IsError: false,
		}
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		var decoded protocol.ToolResultPayload
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if decoded != p {
			t.Errorf("mismatch: got %+v, want %+v", decoded, p)
		}
	})

	t.Run("QueryPayload", func(t *testing.T) {
		p := protocol.QueryPayload{
			QueryID: "q_10",
			Prompt:  "Should we proceed with schema rollback?",
			Choices: []string{"yes", "no", "abort"},
		}
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		var decoded protocol.QueryPayload
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if decoded.QueryID != p.QueryID || decoded.Prompt != p.Prompt || len(decoded.Choices) != 3 {
			t.Errorf("mismatch: got %+v, want %+v", decoded, p)
		}
	})

	t.Run("CompletionPayload", func(t *testing.T) {
		p := protocol.CompletionPayload{
			Result:       "All tasks completed cleanly",
			CommitHash:   "fedcba987654",
			Branch:       "feature/mesh-rfc",
			FilesChanged: []string{"file1.go", "file2.go"},
			DurationMs:   3500,
		}
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		var decoded protocol.CompletionPayload
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if decoded.Result != p.Result || decoded.CommitHash != p.CommitHash || decoded.DurationMs != p.DurationMs {
			t.Errorf("mismatch: got %+v, want %+v", decoded, p)
		}
		if len(decoded.FilesChanged) != 2 {
			t.Errorf("expected 2 files changed, got %d", len(decoded.FilesChanged))
		}
	})

	t.Run("ErrorPayload", func(t *testing.T) {
		p := protocol.ErrorPayload{
			Code:    "process_killed",
			Message: "Process killed by OOM killer",
			Fatal:   true,
		}
		data, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("marshal error: %v", err)
		}
		var decoded protocol.ErrorPayload
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal error: %v", err)
		}
		if decoded != p {
			t.Errorf("mismatch: got %+v, want %+v", decoded, p)
		}
	})
}

func TestNewEventAndUnmarshalPayload(t *testing.T) {
	t.Run("valid struct payload", func(t *testing.T) {
		query := protocol.QueryPayload{
			QueryID: "q_1",
			Prompt:  "Confirm deploy",
			Choices: []string{"yes", "no"},
		}
		evt, err := protocol.NewEvent(1, "task-1", protocol.EventQuery, query)
		if err != nil {
			t.Fatalf("NewEvent failed: %v", err)
		}
		if evt.ID != 1 || evt.TaskID != "task-1" || evt.Type != protocol.EventQuery {
			t.Errorf("unexpected event headers: %+v", evt)
		}
		if evt.Timestamp == 0 {
			t.Error("expected non-zero timestamp")
		}

		var extracted protocol.QueryPayload
		if err := evt.UnmarshalPayload(&extracted); err != nil {
			t.Fatalf("UnmarshalPayload failed: %v", err)
		}
		if extracted.QueryID != query.QueryID || extracted.Prompt != query.Prompt {
			t.Errorf("extracted query payload mismatch: %+v", extracted)
		}
	})

	t.Run("nil payload", func(t *testing.T) {
		evt, err := protocol.NewEvent(2, "task-2", protocol.EventStatus, nil)
		if err != nil {
			t.Fatalf("NewEvent failed: %v", err)
		}
		if len(evt.Payload) != 0 {
			t.Errorf("expected empty payload, got %s", evt.Payload)
		}
		var dummy protocol.StatusPayload
		if err := evt.UnmarshalPayload(&dummy); err == nil {
			t.Error("expected error when unmarshaling empty payload")
		}
	})

	t.Run("raw message payload", func(t *testing.T) {
		raw := json.RawMessage(`{"custom":"payload"}`)
		evt, err := protocol.NewEvent(3, "task-3", protocol.EventThought, raw)
		if err != nil {
			t.Fatalf("NewEvent failed: %v", err)
		}
		if string(evt.Payload) != string(raw) {
			t.Errorf("expected %s, got %s", raw, evt.Payload)
		}
	})

	t.Run("byte slice payload", func(t *testing.T) {
		b := []byte(`{"text":"hello"}`)
		evt, err := protocol.NewEvent(4, "task-4", protocol.EventThought, b)
		if err != nil {
			t.Fatalf("NewEvent failed: %v", err)
		}
		if string(evt.Payload) != string(b) {
			t.Errorf("expected %s, got %s", b, evt.Payload)
		}
	})
}

func TestFormatSSE(t *testing.T) {
	t.Run("valid event", func(t *testing.T) {
		thoughtPayload := protocol.ThoughtPayload{Text: "Thinking..."}
		payloadBytes, err := json.Marshal(thoughtPayload)
		if err != nil {
			t.Fatalf("failed to marshal thought payload: %v", err)
		}

		evt := &protocol.Event{
			ID:        42,
			TaskID:    "task-42",
			Timestamp: 1727085600,
			Type:      protocol.EventThought,
			Payload:   payloadBytes,
		}

		formatted := evt.FormatSSE()
		s := string(formatted)

		if !strings.HasPrefix(s, "id: 42\n") {
			t.Errorf("expected prefix 'id: 42\\n', got: %q", s)
		}
		if !strings.Contains(s, "event: thought\n") {
			t.Errorf("expected 'event: thought\\n', got: %q", s)
		}
		if !strings.Contains(s, "data: {") {
			t.Errorf("expected data field with JSON, got: %q", s)
		}
		if !strings.HasSuffix(s, "\n\n") {
			t.Errorf("expected double newline suffix, got: %q", s)
		}
	})

	t.Run("nil receiver", func(t *testing.T) {
		var evt *protocol.Event
		if res := evt.FormatSSE(); res != nil {
			t.Errorf("expected nil for nil receiver, got: %v", res)
		}
	})
}

func TestParseSSERoundTrip(t *testing.T) {
	origPayload := protocol.StatusPayload{
		Status:        protocol.TaskStatusRunning,
		Message:       "Processing step 1",
		QueuePosition: 0,
		ActiveSlots:   1,
	}
	payloadBytes, err := json.Marshal(origPayload)
	if err != nil {
		t.Fatalf("failed to marshal status payload: %v", err)
	}

	orig := &protocol.Event{
		ID:        7,
		TaskID:    "task-7",
		Timestamp: 1727085600,
		Type:      protocol.EventStatus,
		Payload:   payloadBytes,
	}

	sseBytes := orig.FormatSSE()
	parsed, err := protocol.ParseSSE(sseBytes)
	if err != nil {
		t.Fatalf("ParseSSE failed: %v", err)
	}

	if parsed.ID != orig.ID {
		t.Errorf("expected ID %d, got %d", orig.ID, parsed.ID)
	}
	if parsed.TaskID != orig.TaskID {
		t.Errorf("expected TaskID %q, got %q", orig.TaskID, parsed.TaskID)
	}
	if parsed.Timestamp != orig.Timestamp {
		t.Errorf("expected Timestamp %d, got %d", orig.Timestamp, parsed.Timestamp)
	}
	if parsed.Type != orig.Type {
		t.Errorf("expected Type %q, got %q", orig.Type, parsed.Type)
	}

	var statusPayload protocol.StatusPayload
	if err := json.Unmarshal(parsed.Payload, &statusPayload); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if statusPayload.Status != origPayload.Status {
		t.Errorf("expected Status %q, got %q", origPayload.Status, statusPayload.Status)
	}
	if statusPayload.Message != origPayload.Message {
		t.Errorf("expected Message %q, got %q", origPayload.Message, statusPayload.Message)
	}
}

func TestParseSSEEdgeCases(t *testing.T) {
	t.Run("CRLF line endings and comments", func(t *testing.T) {
		raw := ": keepalive comment\r\nid: 99\r\nevent: error\r\ndata: {\"id\":99,\"task_id\":\"t1\",\"timestamp\":1727085600,\"type\":\"error\",\"payload\":{\"code\":\"e1\",\"message\":\"fail\"}}\r\n\r\n"
		evt, err := protocol.ParseSSE([]byte(raw))
		if err != nil {
			t.Fatalf("ParseSSE failed: %v", err)
		}
		if evt.ID != 99 || evt.Type != protocol.EventError || evt.TaskID != "t1" {
			t.Errorf("unexpected event: %+v", evt)
		}
	})

	t.Run("without space after colon", func(t *testing.T) {
		raw := "id:55\nevent:thought\ndata:{\"id\":55,\"task_id\":\"t2\",\"timestamp\":1727085600,\"type\":\"thought\",\"payload\":{\"text\":\"hi\"}}\n\n"
		evt, err := protocol.ParseSSE([]byte(raw))
		if err != nil {
			t.Fatalf("ParseSSE failed: %v", err)
		}
		if evt.ID != 55 || evt.Type != protocol.EventThought {
			t.Errorf("unexpected event: %+v", evt)
		}
	})

	t.Run("multiline data field", func(t *testing.T) {
		raw := "id: 12\nevent: thought\ndata: {\"id\":12,\"task_id\":\"t3\",\ndata: \"timestamp\":1727085600,\"type\":\"thought\",\ndata: \"payload\":{\"text\":\"multi\"}}\n\n"
		evt, err := protocol.ParseSSE([]byte(raw))
		if err != nil {
			t.Fatalf("ParseSSE with multiline data failed: %v", err)
		}
		if evt.ID != 12 || evt.TaskID != "t3" {
			t.Errorf("unexpected event: %+v", evt)
		}
	})

	t.Run("missing ID in JSON header populated from id field", func(t *testing.T) {
		raw := "id: 88\nevent: completion\ndata: {\"task_id\":\"t4\",\"timestamp\":1727085600,\"type\":\"completion\",\"payload\":{\"result\":\"ok\"}}\n\n"
		evt, err := protocol.ParseSSE([]byte(raw))
		if err != nil {
			t.Fatalf("ParseSSE failed: %v", err)
		}
		if evt.ID != 88 {
			t.Errorf("expected ID 88, got %d", evt.ID)
		}
	})

	t.Run("missing Type in JSON header populated from event field", func(t *testing.T) {
		raw := "id: 89\nevent: tool_call\ndata: {\"id\":89,\"task_id\":\"t5\",\"timestamp\":1727085600,\"payload\":{\"call_id\":\"c1\",\"tool\":\"sh\"}}\n\n"
		evt, err := protocol.ParseSSE([]byte(raw))
		if err != nil {
			t.Fatalf("ParseSSE failed: %v", err)
		}
		if evt.Type != protocol.EventToolCall {
			t.Errorf("expected Type %q, got %q", protocol.EventToolCall, evt.Type)
		}
	})

	t.Run("method receiver ParseSSE", func(t *testing.T) {
		raw := "id: 200\nevent: thought\ndata: {\"id\":200,\"task_id\":\"t200\",\"timestamp\":1727085600,\"type\":\"thought\",\"payload\":{\"text\":\"method\"}}\n\n"
		evt := &protocol.Event{}
		res, err := evt.ParseSSE([]byte(raw))
		if err != nil {
			t.Fatalf("evt.ParseSSE failed: %v", err)
		}
		if res.ID != 200 || evt.ID != 200 {
			t.Errorf("expected ID 200 in both result and receiver: res=%d, evt=%d", res.ID, evt.ID)
		}
	})

	t.Run("empty payload error", func(t *testing.T) {
		_, err := protocol.ParseSSE([]byte(""))
		if err == nil {
			t.Error("expected error for empty payload, got nil")
		}
	})

	t.Run("whitespace only payload error", func(t *testing.T) {
		_, err := protocol.ParseSSE([]byte("   \n\n  \t "))
		if err == nil {
			t.Error("expected error for whitespace only payload, got nil")
		}
	})

	t.Run("missing data field error", func(t *testing.T) {
		raw := "id: 1\nevent: thought\n\n"
		_, err := protocol.ParseSSE([]byte(raw))
		if err == nil {
			t.Error("expected error for missing data field, got nil")
		}
	})

	t.Run("invalid ID header error", func(t *testing.T) {
		raw := "id: not_a_number\nevent: thought\ndata: {}\n\n"
		_, err := protocol.ParseSSE([]byte(raw))
		if err == nil {
			t.Error("expected error for invalid ID header, got nil")
		}
	})

	t.Run("malformed data JSON error", func(t *testing.T) {
		raw := "id: 1\nevent: thought\ndata: {this is not json}\n\n"
		_, err := protocol.ParseSSE([]byte(raw))
		if err == nil {
			t.Error("expected error for malformed json, got nil")
		}
	})
}
