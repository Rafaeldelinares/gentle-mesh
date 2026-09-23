package protocol_test

import (
	"encoding/json"
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

	if parsed != req {
		t.Errorf("TaskRequest round-trip mismatch:\ngot  %+v\nwant %+v", parsed, req)
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
