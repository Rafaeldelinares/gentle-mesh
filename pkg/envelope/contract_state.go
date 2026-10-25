package envelope

import (
	"errors"
	"fmt"
	"sync"
)

// ErrInvalidContractTransition indicates an illegal transition between contract statuses.
var ErrInvalidContractTransition = errors.New("invalid contract transition")

// allowedTransitions maps each non-absorbing ContractStatus to the permitted next statuses.
var allowedTransitions = map[ContractStatus][]ContractStatus{
	ContractStatusProposed: {
		ContractStatusAccepted,
		ContractStatusRejected,
	},
	ContractStatusAccepted: {
		ContractStatusExecuting,
		ContractStatusExpired,
		ContractStatusAbandoned,
	},
	ContractStatusExecuting: {
		ContractStatusSettling,
		ContractStatusAbandoned,
		ContractStatusSettlementTimeout,
	},
	ContractStatusSettling: {
		ContractStatusSettled,
		ContractStatusRemediating,
		ContractStatusSettlementFailed,
		ContractStatusSettlementTimeout,
	},
	ContractStatusRemediating: {
		ContractStatusSettling,
		ContractStatusSettlementFailed,
	},
	ContractStatusSettled: {
		ContractStatusAcceptedFinal,
		ContractStatusDisputed,
	},
	ContractStatusDisputed: {
		ContractStatusResolved,
		ContractStatusAbandoned,
	},
}

// TransitionRecord captures an individual state transition in a contract lifecycle.
type TransitionRecord struct {
	From   ContractStatus
	To     ContractStatus
	Reason string
}

// ContractStateMachine tracks contract lifecycle state with concurrency safety.
type ContractStateMachine struct {
	mu      sync.RWMutex
	current ContractStatus
	history []TransitionRecord
}

// NewContractStateMachine creates a new state machine initialized at the given status.
func NewContractStateMachine(initial ContractStatus) *ContractStateMachine {
	return &ContractStateMachine{
		current: initial,
		history: make([]TransitionRecord, 0),
	}
}

// Current returns the current contract status.
func (sm *ContractStateMachine) Current() ContractStatus {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.current
}

// IsTerminal reports whether the current contract status is terminal.
func (sm *ContractStateMachine) IsTerminal() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.current.IsTerminal()
}

// TransitionTo attempts to transition to the target status without a reason.
func (sm *ContractStateMachine) TransitionTo(target ContractStatus) error {
	return sm.TransitionWithReason(target, "")
}

// TransitionWithReason attempts to transition to the target status recording the given reason.
func (sm *ContractStateMachine) TransitionWithReason(target ContractStatus, reason string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if err := ValidateContractTransition(sm.current, target); err != nil {
		return err
	}

	sm.history = append(sm.history, TransitionRecord{
		From:   sm.current,
		To:     target,
		Reason: reason,
	})
	sm.current = target
	return nil
}

// TransitionFrom attempts to transition from expectedFrom to target status.
// It fails if current status does not match expectedFrom or if the transition is illegal.
func (sm *ContractStateMachine) TransitionFrom(expectedFrom, target ContractStatus, reason string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	if sm.current != expectedFrom {
		return fmt.Errorf("%w: state is %s, expected %s", ErrInvalidContractTransition, sm.current, expectedFrom)
	}

	if err := ValidateContractTransition(sm.current, target); err != nil {
		return err
	}

	sm.history = append(sm.history, TransitionRecord{
		From:   sm.current,
		To:     target,
		Reason: reason,
	})
	sm.current = target
	return nil
}

// History returns a copy of all transitions made by this state machine.
func (sm *ContractStateMachine) History() []TransitionRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	res := make([]TransitionRecord, len(sm.history))
	copy(res, sm.history)
	return res
}

// NextContractStatuses returns the allowed next statuses from a given status.
func NextContractStatuses(current ContractStatus) []ContractStatus {
	nexts, ok := allowedTransitions[current]
	if !ok {
		return nil
	}
	res := make([]ContractStatus, len(nexts))
	copy(res, nexts)
	return res
}

// ValidateContractTransition validates whether a transition from one ContractStatus
// to another is permitted by the RFC-002 R5 contract state machine.
func ValidateContractTransition(from, to ContractStatus) error {
	nexts, ok := allowedTransitions[from]
	if !ok {
		return fmt.Errorf("%w: cannot transition from terminal absorbing state %s to %s", ErrInvalidContractTransition, from, to)
	}
	for _, next := range nexts {
		if next == to {
			return nil
		}
	}
	return fmt.Errorf("%w: cannot transition from %s to %s", ErrInvalidContractTransition, from, to)
}
