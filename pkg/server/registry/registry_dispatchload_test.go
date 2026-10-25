package registry

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

// loadTestRegistry registers n online worker nodes, each with the given declared concurrency.
func loadTestRegistry(t *testing.T, n, capacity int) *Registry {
	t.Helper()

	reg := NewRegistry(30 * time.Second)
	for i := 1; i <= n; i++ {
		id := fmt.Sprintf("node-%d", i)
		if _, err := reg.RegisterNode(protocol.NodeJoinRequest{
			NodeID:         id,
			Endpoint:       fmt.Sprintf("http://10.0.0.%d:8080", i),
			Agents:         []protocol.AgentProfile{{Name: "worker"}},
			MaxConcurrency: capacity,
		}); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	return reg
}

// TestSelectAndReserveSpreadsConcurrentReservations is the core claim of the change: eight
// reservations made at the same time over four nodes of capacity two fill every node twice
// instead of piling up on the lowest NodeID.
func TestSelectAndReserveSpreadsConcurrentReservations(t *testing.T) {
	reg := loadTestRegistry(t, 4, 2)

	const reservations = 8
	start := make(chan struct{})
	got := make([]string, reservations)
	errs := make([]error, reservations)

	var wg sync.WaitGroup
	for i := 0; i < reservations; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			node, err := reg.SelectAndReserve("worker")
			if err != nil {
				errs[i] = err
				return
			}
			got[i] = node.NodeID
		}(i)
	}
	close(start)
	wg.Wait()

	counts := make(map[string]int, 4)
	for i, id := range got {
		if errs[i] != nil {
			t.Fatalf("reservation %d failed: %v", i, errs[i])
		}
		counts[id]++
	}
	for id, count := range counts {
		if count != 2 {
			t.Errorf("node %s got %d reservations, want 2 (distribution %v)", id, count, counts)
		}
	}
	if len(counts) != 4 {
		t.Errorf("expected all 4 nodes reserved, got %v", counts)
	}
	for id, inFlight := range reg.inFlight {
		if inFlight != 2 {
			t.Errorf("inFlight[%s]=%d after 8 reservations, want 2", id, inFlight)
		}
	}

	// A ninth reservation has no free slot anywhere: the soft cap still names a node.
	node, err := reg.SelectAndReserve("worker")
	if err != nil {
		t.Fatalf("soft cap must not fail, got: %v", err)
	}
	if node == nil {
		t.Fatal("soft cap must still return a node")
	}
}

// TestSelectAndReserveSoftCapLogsAndPicksLeastLoaded pins the soft-cap behaviour and its log line.
func TestSelectAndReserveSoftCapLogsAndPicksLeastLoaded(t *testing.T) {
	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)

	reg := loadTestRegistry(t, 2, 1)
	first, err := reg.SelectAndReserve("worker")
	if err != nil {
		t.Fatalf("first reservation: %v", err)
	}
	second, err := reg.SelectAndReserve("worker")
	if err != nil {
		t.Fatalf("second reservation: %v", err)
	}
	if first.NodeID == second.NodeID {
		t.Fatalf("a free node was ignored: both reservations went to %s", first.NodeID)
	}

	third, err := reg.SelectAndReserve("worker")
	if err != nil {
		t.Fatalf("soft cap reservation: %v", err)
	}
	if third == nil {
		t.Fatal("soft cap must still return a node")
	}
	if !strings.Contains(buf.String(), "[mesh-router] all nodes at capacity: picking least loaded") {
		t.Errorf("missing soft-cap log line, got %q", buf.String())
	}
}

// TestSoftCapLogLineIsRateLimited keeps the saturation line to one per second, with the first
// one always written.
func TestSoftCapLogLineIsRateLimited(t *testing.T) {
	const line = "[mesh-router] all nodes at capacity: picking least loaded"

	var buf bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(previous)

	reg := loadTestRegistry(t, 1, 1)
	if _, err := reg.SelectAndReserve("worker"); err != nil {
		t.Fatalf("fill the only slot: %v", err)
	}

	// The first saturated dispatch writes the line even though the registry is brand new.
	if _, err := reg.SelectAndReserve("worker"); err != nil {
		t.Fatalf("first saturated dispatch: %v", err)
	}
	if got := strings.Count(buf.String(), line); got != 1 {
		t.Fatalf("expected exactly 1 saturation line after the first saturated dispatch, got %d", got)
	}

	// A saturated dispatch right after it stays silent.
	if _, err := reg.SelectAndReserve("worker"); err != nil {
		t.Fatalf("second saturated dispatch: %v", err)
	}
	if got := strings.Count(buf.String(), line); got != 1 {
		t.Errorf("expected the saturation line to stay at 1 within the same second, got %d", got)
	}

	// Once the window has passed, it is written again.
	reg.lastCapacityLog = time.Now().Add(-2 * time.Second)
	if _, err := reg.SelectAndReserve("worker"); err != nil {
		t.Fatalf("third saturated dispatch: %v", err)
	}
	if got := strings.Count(buf.String(), line); got != 2 {
		t.Errorf("expected 2 saturation lines after the window passed, got %d", got)
	}
}

// TestReleaseNeverGoesBelowZero covers the "never negative" requirement, including a release
// for a node that was never reserved.
func TestReleaseNeverGoesBelowZero(t *testing.T) {
	reg := loadTestRegistry(t, 1, 1)
	if _, err := reg.SelectAndReserve("worker"); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	reg.Release("node-1")
	reg.Release("node-1")
	reg.Release("node-that-does-not-exist")

	if got := reg.inFlight["node-1"]; got != 0 {
		t.Errorf("inFlight[node-1]=%d after one reserve and two releases, want 0", got)
	}
	if got := reg.inFlight["node-that-does-not-exist"]; got != 0 {
		t.Errorf("inFlight of an unknown node=%d, want 0", got)
	}
}

// TestSelectAndReserveRotatesEquallyLoadedNodes is the LRU tie-break: with every node equally
// loaded and no heartbeat in between, sequential reservations must rotate over the four nodes
// instead of always resolving to the lowest NodeID.
func TestSelectAndReserveRotatesEquallyLoadedNodes(t *testing.T) {
	reg := loadTestRegistry(t, 4, 2)

	var order []string
	for i := 0; i < 8; i++ {
		node, err := reg.SelectAndReserve("worker")
		if err != nil {
			t.Fatalf("reservation %d: %v", i, err)
		}
		order = append(order, node.NodeID)
		reg.Release(node.NodeID)
	}

	counts := make(map[string]int, 4)
	for _, id := range order {
		counts[id]++
	}
	if len(counts) != 4 {
		t.Fatalf("expected the 8 sequential reservations to use 4 nodes, got %v (order %v)", counts, order)
	}
	for id, count := range counts {
		if count != 2 {
			t.Errorf("node %s used %d times, want 2 (order %v)", id, count, order)
		}
	}
}

// TestHeartbeatDoesNotTouchInFlightAndEffectiveLoadIsMax pins both halves of the load model:
// the heartbeat only refreshes the reported count, and the load used for selection is the
// maximum of the reported count and the dispatches still in flight.
func TestHeartbeatDoesNotTouchInFlightAndEffectiveLoadIsMax(t *testing.T) {
	reg := loadTestRegistry(t, 1, 2)
	if _, err := reg.SelectAndReserve("worker"); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	if err := reg.Heartbeat(protocol.NodeHeartbeatRequest{NodeID: "node-1", ActiveTasks: 0}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if got := reg.inFlight["node-1"]; got != 1 {
		t.Fatalf("a heartbeat overwrote inFlight: got %d, want 1", got)
	}
	node, ok := reg.GetNode("node-1")
	if !ok {
		t.Fatal("node-1 not found")
	}
	if got := reg.effectiveLoad(node); got != 1 {
		t.Errorf("effectiveLoad=max(reported 0, inFlight 1)=%d, want 1", got)
	}

	if err := reg.Heartbeat(protocol.NodeHeartbeatRequest{NodeID: "node-1", ActiveTasks: 3}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	node, _ = reg.GetNode("node-1")
	if got := reg.effectiveLoad(node); got != 3 {
		t.Errorf("effectiveLoad=max(reported 3, inFlight 1)=%d, want 3", got)
	}
}

// TestSelectNodeForTaskDoesNotReserve keeps the old entry point side-effect free: it is the
// selection used by callers that manage capacity themselves.
func TestSelectNodeForTaskDoesNotReserve(t *testing.T) {
	reg := loadTestRegistry(t, 2, 1)
	if _, err := reg.SelectNodeForTask("worker"); err != nil {
		t.Fatalf("select: %v", err)
	}
	for id, inFlight := range reg.inFlight {
		if inFlight != 0 {
			t.Errorf("selection without reservation changed inFlight[%s] to %d", id, inFlight)
		}
	}
}

// TestSelectAndReserveWithoutEligibleNodeStillErrors keeps the "no eligible node at all" case
// separate from "all nodes full": only the first one is an error.
func TestSelectAndReserveWithoutEligibleNodeStillErrors(t *testing.T) {
	reg := loadTestRegistry(t, 1, 2)
	if _, err := reg.SelectAndReserve("designer"); !errors.Is(err, ErrAgentNotSupported) {
		t.Fatalf("expected ErrAgentNotSupported for an unsupported agent, got: %v", err)
	}
}
