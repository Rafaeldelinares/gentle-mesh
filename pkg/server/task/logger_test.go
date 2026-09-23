package task

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

func TestJSONLLogger_SequentialWriteRead(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "sub", "task-1.jsonl")

	logger, err := NewJSONLLogger(logPath)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	defer logger.Close()

	if logger.FilePath() != logPath {
		t.Fatalf("expected filepath %s, got %s", logPath, logger.FilePath())
	}

	for i := 1; i <= 5; i++ {
		evt, err := protocol.NewEvent(int64(i), "task-1", protocol.EventThought, protocol.ThoughtPayload{
			Text: fmt.Sprintf("thought %d", i),
		})
		if err != nil {
			t.Fatalf("failed to create event %d: %v", i, err)
		}
		if err := logger.WriteEvent(*evt); err != nil {
			t.Fatalf("failed to write event %d: %v", i, err)
		}
	}

	events, err := logger.ReadEvents(0)
	if err != nil {
		t.Fatalf("failed to read events: %v", err)
	}

	if len(events) != 5 {
		t.Fatalf("expected 5 events, got %d", len(events))
	}

	for i, evt := range events {
		expectedID := int64(i + 1)
		if evt.ID != expectedID {
			t.Errorf("event index %d: expected ID %d, got %d", i, expectedID, evt.ID)
		}
		if evt.TaskID != "task-1" {
			t.Errorf("event index %d: expected TaskID 'task-1', got %s", i, evt.TaskID)
		}
		if evt.Type != protocol.EventThought {
			t.Errorf("event index %d: expected type thought, got %s", i, evt.Type)
		}

		var payload protocol.ThoughtPayload
		if err := evt.UnmarshalPayload(&payload); err != nil {
			t.Errorf("event index %d: failed to unmarshal payload: %v", i, err)
		}
		expectedText := fmt.Sprintf("thought %d", i+1)
		if payload.Text != expectedText {
			t.Errorf("event index %d: expected text %q, got %q", i, expectedText, payload.Text)
		}
	}
}

func TestJSONLLogger_SinceIDFilter(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "task-filter.jsonl")

	logger, err := NewJSONLLogger(logPath)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	defer logger.Close()

	for i := 1; i <= 10; i++ {
		evt := protocol.Event{
			ID:        int64(i),
			TaskID:    "task-filter",
			Timestamp: time.Now().Unix(),
			Type:      protocol.EventThought,
			Payload:   json.RawMessage(fmt.Sprintf(`{"text":"msg-%d"}`, i)),
		}
		if err := logger.WriteEvent(evt); err != nil {
			t.Fatalf("failed to write event %d: %v", i, err)
		}
	}

	testCases := []struct {
		sinceID       int64
		expectedCount int
		expectedFirst int64
	}{
		{sinceID: 0, expectedCount: 10, expectedFirst: 1},
		{sinceID: 5, expectedCount: 5, expectedFirst: 6},
		{sinceID: 9, expectedCount: 1, expectedFirst: 10},
		{sinceID: 10, expectedCount: 0, expectedFirst: 0},
		{sinceID: 20, expectedCount: 0, expectedFirst: 0},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("sinceID_%d", tc.sinceID), func(t *testing.T) {
			events, err := logger.ReadEvents(tc.sinceID)
			if err != nil {
				t.Fatalf("ReadEvents failed: %v", err)
			}
			if len(events) != tc.expectedCount {
				t.Fatalf("expected %d events, got %d", tc.expectedCount, len(events))
			}
			if tc.expectedCount > 0 && events[0].ID != tc.expectedFirst {
				t.Errorf("expected first event ID %d, got %d", tc.expectedFirst, events[0].ID)
			}
		})
	}
}

func TestJSONLLogger_LargePayload1MB(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "task-large.jsonl")

	logger, err := NewJSONLLogger(logPath)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	defer logger.Close()

	// 700KB payload (fits within 1MB max capacity)
	largeString := strings.Repeat("A", 700*1024)
	evt, err := protocol.NewEvent(1, "task-large", protocol.EventThought, protocol.ThoughtPayload{
		Text: largeString,
	})
	if err != nil {
		t.Fatalf("failed to create large event: %v", err)
	}

	if err := logger.WriteEvent(*evt); err != nil {
		t.Fatalf("failed to write large event: %v", err)
	}

	events, err := logger.ReadEvents(0)
	if err != nil {
		t.Fatalf("failed to read large event: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	var payload protocol.ThoughtPayload
	if err := events[0].UnmarshalPayload(&payload); err != nil {
		t.Fatalf("failed to unmarshal large payload: %v", err)
	}

	if len(payload.Text) != len(largeString) {
		t.Fatalf("payload size mismatch: expected %d, got %d", len(largeString), len(payload.Text))
	}
}

func TestJSONLLogger_ConcurrentWrites(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "task-concurrent.jsonl")

	logger, err := NewJSONLLogger(logPath)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}
	defer logger.Close()

	const numGoroutines = 10
	const eventsPerGoroutine = 50
	var wg sync.WaitGroup

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				evt := protocol.Event{
					ID:        int64(goroutineID*1000 + i + 1),
					TaskID:    "task-concurrent",
					Timestamp: time.Now().Unix(),
					Type:      protocol.EventThought,
					Payload:   json.RawMessage(fmt.Sprintf(`{"g":%d,"i":%d}`, goroutineID, i)),
				}
				if err := logger.WriteEvent(evt); err != nil {
					t.Errorf("concurrent write failed: %v", err)
					return
				}
			}
		}(g)
	}

	wg.Wait()

	events, err := logger.ReadEvents(0)
	if err != nil {
		t.Fatalf("failed to read events: %v", err)
	}

	expectedTotal := numGoroutines * eventsPerGoroutine
	if len(events) != expectedTotal {
		t.Fatalf("expected %d total events, got %d", expectedTotal, len(events))
	}
}

func TestJSONLLogger_ClosedBehavior(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "task-closed.jsonl")

	logger, err := NewJSONLLogger(logPath)
	if err != nil {
		t.Fatalf("failed to create logger: %v", err)
	}

	if err := logger.Close(); err != nil {
		t.Fatalf("close failed: %v", err)
	}

	// Double close should succeed cleanly
	if err := logger.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}

	// Write after close should fail
	evt := protocol.Event{
		ID:     1,
		TaskID: "task-closed",
		Type:   protocol.EventThought,
	}
	if err := logger.WriteEvent(evt); err != ErrLoggerClosed {
		t.Fatalf("expected ErrLoggerClosed, got %v", err)
	}
}

func TestJSONLLogger_ReadNonexistent(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "nonexistent.jsonl")

	logger := &JSONLLogger{
		filePath: logPath,
	}

	events, err := logger.ReadEvents(0)
	if err != nil {
		t.Fatalf("expected nil error reading nonexistent file, got %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}
