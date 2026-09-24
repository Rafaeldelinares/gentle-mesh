package protocol_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

func TestTaskStatusConstants(t *testing.T) {
	tests := []struct {
		got  protocol.TaskStatus
		want string
	}{
		{protocol.TaskStatusQueued, "queued"},
		{protocol.TaskStatusPreparing, "preparing"},
		{protocol.TaskStatusRunning, "running"},
		{protocol.TaskStatusCompleted, "completed"},
		{protocol.TaskStatusFailed, "failed"},
		{protocol.TaskStatusCanceled, "canceled"},
	}

	for _, tc := range tests {
		if string(tc.got) != tc.want {
			t.Errorf("expected TaskStatus %q, got %q", tc.want, tc.got)
		}
	}
}

func TestTaskRequestJSONSerialization(t *testing.T) {
	req := protocol.TaskRequest{
		IdempotencyKey: "idem-key-999",
		Agent:          "worker",
		Task:           "Run schema migration and verify tests",
		Context:        "Postgres 16 connection details in env",
		WorkspaceRoot:  "/opt/gentle-mesh/workspaces/proj-1",
		GitRepo:        "git@github.com:example/repo.git",
		GitBranch:      "feature/mesh-migration",
		Patch:          "diff --git a/file.go b/file.go\n...",
		TimeoutSeconds: 300,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal TaskRequest: %v", err)
	}

	var parsed protocol.TaskRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal TaskRequest: %v", err)
	}

	if !reflect.DeepEqual(parsed, req) {
		t.Errorf("TaskRequest round-trip mismatch:\ngot  %+v\nwant %+v", parsed, req)
	}
}

func TestTaskRequestTagsJSONSerialization(t *testing.T) {
	req := protocol.TaskRequest{
		Agent: "worker",
		Task:  "Run GPU-accelerated model training",
		Tags:  []string{"gpu", "fast"},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal TaskRequest with Tags: %v", err)
	}

	var parsed protocol.TaskRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal TaskRequest with Tags: %v", err)
	}

	if !reflect.DeepEqual(parsed.Tags, req.Tags) {
		t.Errorf("expected Tags %v, got %v", req.Tags, parsed.Tags)
	}

	// Verify omitempty
	reqNoTags := protocol.TaskRequest{
		Agent: "worker",
		Task:  "Minimal task without tags",
	}
	dataNoTags, err := json.Marshal(reqNoTags)
	if err != nil {
		t.Fatalf("failed to marshal TaskRequest without Tags: %v", err)
	}
	if strings.Contains(string(dataNoTags), `"tags"`) {
		t.Errorf("expected tags to be omitted when empty, got: %s", string(dataNoTags))
	}
}

func TestTaskRequestOmitempty(t *testing.T) {
	req := protocol.TaskRequest{
		Agent: "worker",
		Task:  "Minimal task",
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("failed to marshal TaskRequest: %v", err)
	}

	s := string(data)
	if strings.Contains(s, "idempotency_key") {
		t.Errorf("expected idempotency_key to be omitted, got: %s", s)
	}
	if strings.Contains(s, "context") {
		t.Errorf("expected context to be omitted, got: %s", s)
	}
	if strings.Contains(s, "workspace_root") {
		t.Errorf("expected workspace_root to be omitted, got: %s", s)
	}
	if strings.Contains(s, "git_repo") {
		t.Errorf("expected git_repo to be omitted, got: %s", s)
	}
	if strings.Contains(s, "git_branch") {
		t.Errorf("expected git_branch to be omitted, got: %s", s)
	}
	if strings.Contains(s, "patch") {
		t.Errorf("expected patch to be omitted, got: %s", s)
	}
	if strings.Contains(s, "timeout_seconds") {
		t.Errorf("expected timeout_seconds to be omitted, got: %s", s)
	}
}

func TestTaskRequestIdempotencyKey(t *testing.T) {
	// With key
	reqWithKey := protocol.TaskRequest{
		IdempotencyKey: "unique-uuid-1234",
		Agent:          "worker",
		Task:           "Do work",
	}
	data, err := json.Marshal(reqWithKey)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}
	if !strings.Contains(string(data), `"idempotency_key":"unique-uuid-1234"`) {
		t.Errorf("expected idempotency_key in JSON, got: %s", string(data))
	}

	var parsed protocol.TaskRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if parsed.IdempotencyKey != "unique-uuid-1234" {
		t.Errorf("expected IdempotencyKey to be %q, got %q", "unique-uuid-1234", parsed.IdempotencyKey)
	}

	// Without key (from raw JSON)
	rawJSON := []byte(`{"agent":"worker","task":"Do work"}`)
	var parsedNoKey protocol.TaskRequest
	if err := json.Unmarshal(rawJSON, &parsedNoKey); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if parsedNoKey.IdempotencyKey != "" {
		t.Errorf("expected empty IdempotencyKey, got %q", parsedNoKey.IdempotencyKey)
	}
}

func TestTaskResponseJSONSerialization(t *testing.T) {
	resp := protocol.TaskResponse{
		TaskID:    "task-99",
		Status:    protocol.TaskStatusQueued,
		EventsURL: "/v1/tasks/task-99/events",
		CreatedAt: 1727085600,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal TaskResponse: %v", err)
	}

	var parsed protocol.TaskResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal TaskResponse: %v", err)
	}

	if parsed != resp {
		t.Errorf("TaskResponse round-trip mismatch:\ngot  %+v\nwant %+v", parsed, resp)
	}
}

func TestTaskStateJSONSerialization(t *testing.T) {
	completion := &protocol.CompletionPayload{
		Result:       "Migration completed successfully",
		CommitHash:   "abc1234def",
		Branch:       "feature/mesh-migration",
		FilesChanged: []string{"migrations/001_init.sql"},
		DurationMs:   1250,
	}

	state := protocol.TaskState{
		TaskID: "task-100",
		Request: protocol.TaskRequest{
			Agent: "worker",
			Task:  "Run migrations",
		},
		Status:     protocol.TaskStatusCompleted,
		CreatedAt:  1727085600,
		StartedAt:  1727085605,
		FinishedAt: 1727085620,
		Completion: completion,
		Error:      nil,
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("failed to marshal TaskState: %v", err)
	}

	var parsed protocol.TaskState
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal TaskState: %v", err)
	}

	if parsed.TaskID != state.TaskID {
		t.Errorf("expected TaskID %q, got %q", state.TaskID, parsed.TaskID)
	}
	if parsed.Status != state.Status {
		t.Errorf("expected Status %q, got %q", state.Status, parsed.Status)
	}
	if parsed.Completion == nil {
		t.Fatal("expected Completion not to be nil")
	}
	if parsed.Completion.CommitHash != completion.CommitHash {
		t.Errorf("expected CommitHash %q, got %q", completion.CommitHash, parsed.Completion.CommitHash)
	}
	if parsed.Error != nil {
		t.Errorf("expected nil Error, got %+v", parsed.Error)
	}
}

func TestTaskRequestOpenPiViewerFieldsJSONSerialization(t *testing.T) {
	t.Run("session_id and prompt round-trip", func(t *testing.T) {
		req := protocol.TaskRequest{
			SessionID: "viewer-session-42",
			Prompt:    "Summarize the failing test output",
		}

		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("failed to marshal TaskRequest: %v", err)
		}

		s := string(data)
		if !strings.Contains(s, `"session_id":"viewer-session-42"`) {
			t.Errorf("expected session_id in JSON, got: %s", s)
		}
		if !strings.Contains(s, `"prompt":"Summarize the failing test output"`) {
			t.Errorf("expected prompt in JSON, got: %s", s)
		}

		var parsed protocol.TaskRequest
		if err := json.Unmarshal(data, &parsed); err != nil {
			t.Fatalf("failed to unmarshal TaskRequest: %v", err)
		}
		if parsed.SessionID != req.SessionID {
			t.Errorf("expected SessionID %q, got %q", req.SessionID, parsed.SessionID)
		}
		if parsed.Prompt != req.Prompt {
			t.Errorf("expected Prompt %q, got %q", req.Prompt, parsed.Prompt)
		}
	})

	t.Run("unmarshal from raw viewer payload", func(t *testing.T) {
		raw := []byte(`{"session_id":"sess-7","prompt":"do the thing"}`)
		var parsed protocol.TaskRequest
		if err := json.Unmarshal(raw, &parsed); err != nil {
			t.Fatalf("failed to unmarshal viewer payload: %v", err)
		}
		if parsed.SessionID != "sess-7" || parsed.Prompt != "do the thing" {
			t.Errorf("unexpected decoded request: %+v", parsed)
		}
	})

	t.Run("omitempty when unset", func(t *testing.T) {
		req := protocol.TaskRequest{Agent: "worker", Task: "Minimal task"}
		data, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("failed to marshal TaskRequest: %v", err)
		}
		s := string(data)
		if strings.Contains(s, "session_id") {
			t.Errorf("expected session_id to be omitted, got: %s", s)
		}
		if strings.Contains(s, "prompt") {
			t.Errorf("expected prompt to be omitted, got: %s", s)
		}
	})
}

func TestTaskResponseSessionIDJSONSerialization(t *testing.T) {
	resp := protocol.TaskResponse{
		TaskID:    "task-viewer-1",
		SessionID: "viewer-session-42",
		Status:    protocol.TaskStatusQueued,
		EventsURL: "/v1/tasks/task-viewer-1/events",
		CreatedAt: 1727085600,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal TaskResponse: %v", err)
	}
	if !strings.Contains(string(data), `"session_id":"viewer-session-42"`) {
		t.Errorf("expected session_id in JSON, got: %s", string(data))
	}

	var parsed protocol.TaskResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal TaskResponse: %v", err)
	}
	if parsed != resp {
		t.Errorf("TaskResponse round-trip mismatch:\ngot  %+v\nwant %+v", parsed, resp)
	}

	// omitempty: a response without a session must not emit the field.
	withoutSession := protocol.TaskResponse{TaskID: "task-1", Status: protocol.TaskStatusQueued, EventsURL: "/v1/tasks/task-1/events"}
	dataNoSession, err := json.Marshal(withoutSession)
	if err != nil {
		t.Fatalf("failed to marshal TaskResponse without session: %v", err)
	}
	if strings.Contains(string(dataNoSession), "session_id") {
		t.Errorf("expected session_id to be omitted, got: %s", string(dataNoSession))
	}
}

func TestTaskStateSessionIDJSONSerialization(t *testing.T) {
	state := protocol.TaskState{
		TaskID:    "task-viewer-1",
		SessionID: "viewer-session-42",
		Request: protocol.TaskRequest{
			SessionID: "viewer-session-42",
			Prompt:    "do the thing",
			Agent:     "worker",
			Task:      "do the thing",
		},
		Status:    protocol.TaskStatusRunning,
		CreatedAt: 1727085600,
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("failed to marshal TaskState: %v", err)
	}
	if !strings.Contains(string(data), `"session_id":"viewer-session-42"`) {
		t.Errorf("expected session_id in JSON, got: %s", string(data))
	}

	var parsed protocol.TaskState
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal TaskState: %v", err)
	}
	if parsed.SessionID != state.SessionID {
		t.Errorf("expected SessionID %q, got %q", state.SessionID, parsed.SessionID)
	}
	if parsed.Request.SessionID != state.Request.SessionID {
		t.Errorf("expected Request.SessionID %q, got %q", state.Request.SessionID, parsed.Request.SessionID)
	}
	if parsed.Request.Prompt != state.Request.Prompt {
		t.Errorf("expected Request.Prompt %q, got %q", state.Request.Prompt, parsed.Request.Prompt)
	}
}

func TestTaskStateWithError(t *testing.T) {
	errPayload := &protocol.ErrorPayload{
		Code:    "timeout",
		Message: "Execution exceeded 300s limit",
		Fatal:   true,
	}

	state := protocol.TaskState{
		TaskID: "task-101",
		Request: protocol.TaskRequest{
			Agent: "worker",
			Task:  "Long running test",
		},
		Status:     protocol.TaskStatusFailed,
		CreatedAt:  1727085600,
		StartedAt:  1727085605,
		FinishedAt: 1727085905,
		Completion: nil,
		Error:      errPayload,
	}

	data, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("failed to marshal TaskState: %v", err)
	}

	var parsed protocol.TaskState
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal TaskState: %v", err)
	}

	if parsed.Status != protocol.TaskStatusFailed {
		t.Errorf("expected Status %q, got %q", protocol.TaskStatusFailed, parsed.Status)
	}
	if parsed.Error == nil {
		t.Fatal("expected Error not to be nil")
	}
	if parsed.Error.Code != errPayload.Code {
		t.Errorf("expected error Code %q, got %q", errPayload.Code, parsed.Error.Code)
	}
	if !parsed.Error.Fatal {
		t.Error("expected Fatal to be true")
	}
}

func TestTaskReplyRequestJSONSerialization(t *testing.T) {
	reply := protocol.TaskReplyRequest{
		QueryID: "q_42",
		Answer:  "Confirm migration execution",
	}

	data, err := json.Marshal(reply)
	if err != nil {
		t.Fatalf("failed to marshal TaskReplyRequest: %v", err)
	}

	var parsed protocol.TaskReplyRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal TaskReplyRequest: %v", err)
	}

	if parsed != reply {
		t.Errorf("TaskReplyRequest round-trip mismatch:\ngot  %+v\nwant %+v", parsed, reply)
	}
}

func TestInvalidJSONErrors(t *testing.T) {
	invalidJSON := []byte(`{invalid`)

	var req protocol.TaskRequest
	if err := json.Unmarshal(invalidJSON, &req); err == nil {
		t.Error("expected error unmarshaling invalid JSON into TaskRequest")
	}

	var resp protocol.TaskResponse
	if err := json.Unmarshal(invalidJSON, &resp); err == nil {
		t.Error("expected error unmarshaling invalid JSON into TaskResponse")
	}

	var state protocol.TaskState
	if err := json.Unmarshal(invalidJSON, &state); err == nil {
		t.Error("expected error unmarshaling invalid JSON into TaskState")
	}

	var reply protocol.TaskReplyRequest
	if err := json.Unmarshal(invalidJSON, &reply); err == nil {
		t.Error("expected error unmarshaling invalid JSON into TaskReplyRequest")
	}
}
