package task

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

var (
	// ErrLoggerClosed is returned when attempting to write to a closed logger.
	ErrLoggerClosed = errors.New("logger is closed")
)

// JSONLLogger provides append-only, thread-safe persistence and replay for task events.
type JSONLLogger struct {
	mu       sync.Mutex
	filePath string
	file     *os.File
	closed   bool
}

// NewJSONLLogger creates or opens filePath in append mode, creating parent directories if needed.
func NewJSONLLogger(filePath string) (*JSONLLogger, error) {
	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory for logger: %w", err)
	}

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open event log file: %w", err)
	}

	return &JSONLLogger{
		filePath: filePath,
		file:     file,
	}, nil
}

// WriteEvent serializes event as a single-line JSON and appends it to disk.
// It syncs immediately only for critical events (status, completion, error).
func (l *JSONLLogger) WriteEvent(event protocol.Event) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return ErrLoggerClosed
	}

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal event: %w", err)
	}
	data = append(data, '\n')

	if _, err := l.file.Write(data); err != nil {
		return fmt.Errorf("failed to write event to disk: %w", err)
	}

	if event.Type == protocol.EventStatus || event.Type == protocol.EventCompletion || event.Type == protocol.EventError {
		if err := l.file.Sync(); err != nil {
			return fmt.Errorf("failed to sync event to disk: %w", err)
		}
	}

	return nil
}

// Sync flushes the underlying file to disk.
func (l *JSONLLogger) Sync() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrLoggerClosed
	}
	return l.file.Sync()
}

// ReadEvents reads the event log file from disk and returns all events with ID > sinceID.
// It supports lines up to 1MB and tolerates a corrupt/partial trailing line if a crash occurred mid-write at EOF.
func (l *JSONLLogger) ReadEvents(sinceID int64) ([]protocol.Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	file, err := os.Open(l.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return []protocol.Event{}, nil
		}
		return nil, fmt.Errorf("failed to open event log for reading: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	const maxCapacity = 1024 * 1024 // 1MB buffer
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, maxCapacity)

	var rawLines [][]byte
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		lineCopy := make([]byte, len(line))
		copy(lineCopy, line)
		rawLines = append(rawLines, lineCopy)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanner error while reading events: %w", err)
	}

	events := make([]protocol.Event, 0, len(rawLines))
	for i, line := range rawLines {
		var evt protocol.Event
		if err := json.Unmarshal(line, &evt); err != nil {
			if i == len(rawLines)-1 {
				// Corrupt/partial trailing line at EOF is skipped
				break
			}
			return nil, fmt.Errorf("failed to unmarshal event: %w", err)
		}

		if evt.ID > sinceID {
			events = append(events, evt)
		}
	}

	return events, nil
}

// Close syncs and closes the underlying file descriptor.
func (l *JSONLLogger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}
	l.closed = true

	if l.file == nil {
		return nil
	}

	syncErr := l.file.Sync()
	closeErr := l.file.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// FilePath returns the underlying file path.
func (l *JSONLLogger) FilePath() string {
	return l.filePath
}
