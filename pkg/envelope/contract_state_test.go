package envelope

import (
	"errors"
	"sync"
	"testing"
)

// TestR5_ContractStatus_ResolvedIsTerminal tests RFC-002 R5:
// All terminal states, including the resolution of disputed contracts,
// must be recognized as terminal.
func TestR5_ContractStatus_ResolvedIsTerminal(t *testing.T) {
	if !ContractStatusResolved.IsTerminal() {
		t.Errorf("R5 VIOLATION: ContractStatusResolved.IsTerminal() = false, want true")
	}
}

// TestR5_ContractTransitionValidation tests that all valid forward transitions
// are permitted, and all illegal transitions (backwards, cross-branch, or from terminal states)
// are rejected.
func TestR5_ContractTransitionValidation(t *testing.T) {
	// Valid transitions
	validPairs := []struct {
		from ContractStatus
		to   ContractStatus
	}{
		// PROPOSED transitions
		{ContractStatusProposed, ContractStatusAccepted},
		{ContractStatusProposed, ContractStatusRejected},

		// ACCEPTED transitions
		{ContractStatusAccepted, ContractStatusExecuting},
		{ContractStatusAccepted, ContractStatusExpired},
		{ContractStatusAccepted, ContractStatusAbandoned},

		// EXECUTING transitions
		{ContractStatusExecuting, ContractStatusSettling},
		{ContractStatusExecuting, ContractStatusAbandoned},
		{ContractStatusExecuting, ContractStatusSettlementTimeout},

		// SETTLING transitions
		{ContractStatusSettling, ContractStatusSettled},
		{ContractStatusSettling, ContractStatusRemediating},
		{ContractStatusSettling, ContractStatusSettlementFailed},
		{ContractStatusSettling, ContractStatusSettlementTimeout},

		// REMEDIATING transitions
		{ContractStatusRemediating, ContractStatusSettling},
		{ContractStatusRemediating, ContractStatusSettlementFailed},

		// SETTLED transitions
		{ContractStatusSettled, ContractStatusAcceptedFinal},
		{ContractStatusSettled, ContractStatusDisputed},

		// DISPUTED transitions
		{ContractStatusDisputed, ContractStatusResolved},
		{ContractStatusDisputed, ContractStatusAbandoned},
	}

	for _, p := range validPairs {
		if err := ValidateContractTransition(p.from, p.to); err != nil {
			t.Errorf("expected transition %s -> %s to be valid, got: %v", p.from, p.to, err)
		}
	}

	// Invalid transitions: illegal jumps, backwards moves, or moves out of terminal absorbing states
	invalidPairs := []struct {
		from ContractStatus
		to   ContractStatus
	}{
		// Backwards or illegal jumps
		{ContractStatusProposed, ContractStatusSettled},
		{ContractStatusProposed, ContractStatusExecuting},
		{ContractStatusAccepted, ContractStatusSettled},
		{ContractStatusExecuting, ContractStatusAcceptedFinal},
		{ContractStatusExecuting, ContractStatusProposed},

		// Transitions out of terminal absorbing states
		{ContractStatusAcceptedFinal, ContractStatusProposed},
		{ContractStatusAcceptedFinal, ContractStatusExecuting},
		{ContractStatusRejected, ContractStatusAccepted},
		{ContractStatusRejected, ContractStatusProposed},
		{ContractStatusExpired, ContractStatusExecuting},
		{ContractStatusSettlementFailed, ContractStatusSettling},
		{ContractStatusSettlementFailed, ContractStatusSettled},
		{ContractStatusSettlementTimeout, ContractStatusSettling},
		{ContractStatusSettlementTimeout, ContractStatusAcceptedFinal},
		{ContractStatusAbandoned, ContractStatusExecuting},
		{ContractStatusResolved, ContractStatusDisputed},
		{ContractStatusResolved, ContractStatusAcceptedFinal},
	}

	for _, p := range invalidPairs {
		if err := ValidateContractTransition(p.from, p.to); err == nil {
			t.Errorf("R5 VIOLATION: expected invalid transition %s -> %s to be rejected with ErrInvalidContractTransition", p.from, p.to)
		} else if !errors.Is(err, ErrInvalidContractTransition) {
			t.Errorf("expected ErrInvalidContractTransition, got: %v", err)
		}
	}
}

// TestR5_ContractStateMachine_EveryNonTerminalReachesTerminal verifies the R5 liveness guarantee:
// from EVERY non-terminal state, there exists at least one valid path reaching a terminal state.
func TestR5_ContractStateMachine_EveryNonTerminalReachesTerminal(t *testing.T) {
	allStatuses := []ContractStatus{
		ContractStatusProposed,
		ContractStatusAccepted,
		ContractStatusExecuting,
		ContractStatusSettling,
		ContractStatusRemediating,
		ContractStatusSettled,
		ContractStatusAcceptedFinal,
		ContractStatusRejected,
		ContractStatusExpired,
		ContractStatusSettlementFailed,
		ContractStatusDisputed,
		ContractStatusAbandoned,
		ContractStatusSettlementTimeout,
		ContractStatusResolved,
	}

	for _, start := range allStatuses {
		if start.IsTerminal() {
			continue
		}

		// BFS to find a terminal state
		visited := make(map[ContractStatus]bool)
		queue := []ContractStatus{start}
		visited[start] = true
		foundTerminal := false

		for len(queue) > 0 {
			curr := queue[0]
			queue = queue[1:]

			if curr.IsTerminal() {
				foundTerminal = true
				break
			}

			nexts := NextContractStatuses(curr)
			if len(nexts) == 0 {
				t.Fatalf("R5 VIOLATION: non-terminal state %s has no outgoing transitions (dead-end)", curr)
			}

			for _, n := range nexts {
				if !visited[n] {
					visited[n] = true
					queue = append(queue, n)
				}
			}
		}

		if !foundTerminal {
			t.Errorf("R5 VIOLATION: state %s cannot reach any terminal state (hung cycle)", start)
		}
	}
}

// TestR5_ContractStateMachine_Instance tests the stateful ContractStateMachine methods.
func TestR5_ContractStateMachine_Instance(t *testing.T) {
	sm := NewContractStateMachine(ContractStatusProposed)
	if sm.Current() != ContractStatusProposed {
		t.Fatalf("initial state = %s, want PROPOSED", sm.Current())
	}
	if sm.IsTerminal() {
		t.Fatalf("PROPOSED should not be terminal")
	}

	// Legal sequence: PROPOSED -> ACCEPTED -> EXECUTING -> SETTLING -> SETTLED -> ACCEPTED_FINAL
	if err := sm.TransitionTo(ContractStatusAccepted); err != nil {
		t.Fatalf("transition to ACCEPTED: %v", err)
	}
	if err := sm.TransitionTo(ContractStatusExecuting); err != nil {
		t.Fatalf("transition to EXECUTING: %v", err)
	}
	if err := sm.TransitionTo(ContractStatusSettling); err != nil {
		t.Fatalf("transition to SETTLING: %v", err)
	}
	if err := sm.TransitionTo(ContractStatusSettled); err != nil {
		t.Fatalf("transition to SETTLED: %v", err)
	}
	if !sm.IsTerminal() {
		t.Fatalf("SETTLED should be terminal")
	}
	if err := sm.TransitionTo(ContractStatusAcceptedFinal); err != nil {
		t.Fatalf("transition to ACCEPTED_FINAL: %v", err)
	}
	if !sm.IsTerminal() {
		t.Fatalf("ACCEPTED_FINAL should be terminal")
	}

	// Illegal transition out of terminal state ACCEPTED_FINAL
	if err := sm.TransitionTo(ContractStatusExecuting); err == nil {
		t.Fatalf("expected error transitioning out of ACCEPTED_FINAL, got nil")
	}

	history := sm.History()
	if len(history) != 5 {
		t.Errorf("history length = %d, want 5", len(history))
	}
}

// TestR5_ContractStateMachine_Concurrency verifies thread-safety under concurrent transition requests.
func TestR5_ContractStateMachine_Concurrency(t *testing.T) {
	sm := NewContractStateMachine(ContractStatusExecuting)

	var wg sync.WaitGroup
	var successCount int
	var mu sync.Mutex

	targets := []ContractStatus{
		ContractStatusSettling,
		ContractStatusAbandoned,
		ContractStatusSettlementTimeout,
		ContractStatusProposed, // illegal
		ContractStatusAccepted, // illegal
	}

	for _, target := range targets {
		wg.Add(1)
		go func(tgt ContractStatus) {
			defer wg.Done()
			if err := sm.TransitionTo(tgt); err == nil {
				mu.Lock()
				successCount++
				mu.Unlock()
			}
		}(target)
	}

	wg.Wait()

	if successCount != 1 {
		t.Errorf("expected exactly 1 successful transition from EXECUTING, got %d", successCount)
	}
}
