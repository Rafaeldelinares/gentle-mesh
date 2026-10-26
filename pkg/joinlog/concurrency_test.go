package joinlog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/joinlog"
)

// RFC-003 J2 (serialización sin colisión) requires that concurrent writers to
// the same log produce a single linear chain: no two events share the same
// predecessor, and every event references the hash of its immediate
// predecessor.
//
// This test is the RED proof of the risk identified in RFC-003 §7: with the
// unprotected append path (read the head, then insert), concurrent writers
// read the same head and append events that fork the chain.
//
// The test encodes the requirement, not the implementation: whatever
// serialization mechanism is chosen later, this test has to pass unchanged.
func TestJoinLog_ConcurrentAppendsProduceOneLinearChain(t *testing.T) {
	ctx := context.Background()
	log, err := joinlog.New(filepath.Join(t.TempDir(), "join.db"))
	if err != nil {
		t.Fatalf("open join log: %v", err)
	}
	defer log.Close()

	const writers = 16
	const appendsPerWriter = 8

	var wg sync.WaitGroup
	errs := make(chan error, writers*appendsPerWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(actor int) {
			defer wg.Done()
			for i := 0; i < appendsPerWriter; i++ {
				payload, err := json.Marshal(map[string]any{
					"task_id": fmt.Sprintf("task-%d-%d", actor, i),
				})
				if err != nil {
					errs <- err
					return
				}
				if err := log.Append(ctx, joinlog.Event{
					Type:    "task.created",
					Actor:   fmt.Sprintf("actor-%d", actor),
					Payload: payload,
				}); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("append failed: %v", err)
	}

	events, err := log.Events(ctx)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if got, want := len(events), writers*appendsPerWriter; got != want {
		t.Fatalf("expected %d events, got %d", want, got)
	}

	if err := joinlog.VerifyChain(events); err != nil {
		t.Fatalf("the join log chain must stay linear under concurrent appends: %v", err)
	}
}
