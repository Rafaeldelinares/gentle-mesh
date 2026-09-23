package registry

import (
	"sync"
	"time"
)

type idempotencyEntry struct {
	taskID    string
	createdAt time.Time
}

// IdempotencyStore tracks idempotency keys to prevent duplicate task dispatches.
type IdempotencyStore struct {
	mu      sync.RWMutex
	ttl     time.Duration
	records map[string]idempotencyEntry
}

// NewIdempotencyStore creates an IdempotencyStore with the given time-to-live.
// If ttl <= 0, entries do not expire automatically based on age.
func NewIdempotencyStore(ttl time.Duration) *IdempotencyStore {
	return &IdempotencyStore{
		ttl:     ttl,
		records: make(map[string]idempotencyEntry),
	}
}

// RecordOrGet records a taskID for the given key, or returns the existing taskID if already present and valid.
// If key is empty, it returns taskID and false (no deduplication).
// If key exists and is not expired, it returns existingTaskID and true (duplicate).
// If key does not exist or has expired, it stores taskID and returns taskID and false.
func (s *IdempotencyStore) RecordOrGet(key string, taskID string) (string, bool) {
	if key == "" {
		return taskID, false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if entry, exists := s.records[key]; exists {
		if s.ttl <= 0 || now.Sub(entry.createdAt) <= s.ttl {
			return entry.taskID, true
		}
	}

	s.records[key] = idempotencyEntry{
		taskID:    taskID,
		createdAt: now,
	}
	return taskID, false
}

// CleanupExpired removes entries that have exceeded the TTL.
// If ttl <= 0, this is a no-op.
func (s *IdempotencyStore) CleanupExpired() {
	if s.ttl <= 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for k, entry := range s.records {
		if now.Sub(entry.createdAt) > s.ttl {
			delete(s.records, k)
		}
	}
}

// Get returns the taskID for a key if it exists and has not expired.
func (s *IdempotencyStore) Get(key string) (string, bool) {
	if key == "" {
		return "", false
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	entry, exists := s.records[key]
	if !exists {
		return "", false
	}
	if s.ttl > 0 && time.Since(entry.createdAt) > s.ttl {
		return "", false
	}
	return entry.taskID, true
}

// Len returns the count of records currently in the store.
func (s *IdempotencyStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records)
}
