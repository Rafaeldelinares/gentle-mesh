package registry_test

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
)

func TestIdempotencyStore_DuplicateDetection(t *testing.T) {
	store := registry.NewIdempotencyStore(0)

	key := "idemp-key-1"
	task1 := "task-1"
	task2 := "task-2"

	// First registration should not be a duplicate
	gotTask, isDup := store.RecordOrGet(key, task1)
	if isDup {
		t.Fatal("expected first record to not be duplicate")
	}
	if gotTask != task1 {
		t.Fatalf("expected task %q, got %q", task1, gotTask)
	}

	// Second registration with same key should be detected as duplicate and return task1
	gotTask, isDup = store.RecordOrGet(key, task2)
	if !isDup {
		t.Fatal("expected second record with same key to be duplicate")
	}
	if gotTask != task1 {
		t.Fatalf("expected existing task %q, got %q", task1, gotTask)
	}

	// Verify Get returns task1
	val, exists := store.Get(key)
	if !exists || val != task1 {
		t.Fatalf("expected Get to return %q, got %q (exists=%v)", task1, val, exists)
	}
}

func TestIdempotencyStore_EmptyKeyBypass(t *testing.T) {
	store := registry.NewIdempotencyStore(0)

	// Empty key should never be treated as duplicate
	gotTask, isDup := store.RecordOrGet("", "task-1")
	if isDup {
		t.Error("expected empty key to not be duplicate")
	}
	if gotTask != "task-1" {
		t.Errorf("expected task-1, got %q", gotTask)
	}

	gotTask, isDup = store.RecordOrGet("", "task-2")
	if isDup {
		t.Error("expected second empty key call to not be duplicate")
	}
	if gotTask != "task-2" {
		t.Errorf("expected task-2, got %q", gotTask)
	}

	// Get on empty key should return false
	if _, exists := store.Get(""); exists {
		t.Error("expected Get on empty key to return false")
	}
}

func TestIdempotencyStore_TTLExpiration(t *testing.T) {
	ttl := 50 * time.Millisecond
	store := registry.NewIdempotencyStore(ttl)

	key := "temp-key"
	task1 := "task-old"
	task2 := "task-new"

	gotTask, isDup := store.RecordOrGet(key, task1)
	if isDup || gotTask != task1 {
		t.Fatalf("initial store failed: gotTask=%s, isDup=%v", gotTask, isDup)
	}

	// Immediate re-check should be duplicate
	gotTask, isDup = store.RecordOrGet(key, task2)
	if !isDup || gotTask != task1 {
		t.Fatalf("immediate check failed: gotTask=%s, isDup=%v", gotTask, isDup)
	}

	// Wait for TTL to pass
	time.Sleep(70 * time.Millisecond)

	// Get should now report false
	if _, exists := store.Get(key); exists {
		t.Fatal("expected key to have expired in Get")
	}

	// RecordOrGet should overwrite expired key and report not duplicate
	gotTask, isDup = store.RecordOrGet(key, task2)
	if isDup {
		t.Fatal("expected expired key to not be duplicate on re-record")
	}
	if gotTask != task2 {
		t.Fatalf("expected new task %q, got %q", task2, gotTask)
	}
}

func TestIdempotencyStore_CleanupExpired(t *testing.T) {
	ttl := 40 * time.Millisecond
	store := registry.NewIdempotencyStore(ttl)

	store.RecordOrGet("k1", "t1")
	store.RecordOrGet("k2", "t2")
	if store.Len() != 2 {
		t.Fatalf("expected 2 records, got %d", store.Len())
	}

	time.Sleep(60 * time.Millisecond)

	store.CleanupExpired()
	if store.Len() != 0 {
		t.Fatalf("expected 0 records after cleanup, got %d", store.Len())
	}

	// Cleanup on store with ttl <= 0 should do nothing
	noTTLStore := registry.NewIdempotencyStore(0)
	noTTLStore.RecordOrGet("k1", "t1")
	noTTLStore.CleanupExpired()
	if noTTLStore.Len() != 1 {
		t.Fatalf("expected 1 record on no-TTL store, got %d", noTTLStore.Len())
	}
}

func TestIdempotencyStore_ConcurrentAccess(t *testing.T) {
	store := registry.NewIdempotencyStore(1 * time.Second)
	key := "shared-key"

	const numGoroutines = 50
	var firstWins atomic.Int32
	var duplicates atomic.Int32
	var wg sync.WaitGroup

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			defer wg.Done()
			taskID := fmt.Sprintf("task-%d", id)
			_, isDup := store.RecordOrGet(key, taskID)
			if isDup {
				duplicates.Add(1)
			} else {
				firstWins.Add(1)
			}
		}(i)
	}

	wg.Wait()

	// Exactly 1 goroutine should have won the race
	if firstWins.Load() != 1 {
		t.Fatalf("expected exactly 1 winner, got %d", firstWins.Load())
	}
	if duplicates.Load() != int32(numGoroutines-1) {
		t.Fatalf("expected %d duplicates, got %d", numGoroutines-1, duplicates.Load())
	}
}
