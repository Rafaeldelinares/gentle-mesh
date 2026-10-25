package protocol_test

import (
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

func TestA2A_StateMappingTable(t *testing.T) {
	// Table defined in docs/rfcs/002-a2a-positioning.md (Section e)
	expectedMappings := []struct {
		gentleState  protocol.CognitiveState
		wantA2AState protocol.A2ATaskState
		wantSubstate string
		wantReason   string
		isTerminal   bool
	}{
		{
			gentleState:  protocol.CognitiveStateSubmitted,
			wantA2AState: protocol.A2AStateSubmitted,
			wantSubstate: "acknowledged",
			isTerminal:   false,
		},
		{
			gentleState:  protocol.CognitiveStatePreflight,
			wantA2AState: protocol.A2AStateWorking,
			wantSubstate: "preflight_validation",
			isTerminal:   false,
		},
		{
			gentleState:  protocol.CognitiveStateNegotiating,
			wantA2AState: protocol.A2AStateWorking,
			wantSubstate: "preflight_validation",
			isTerminal:   false,
		},
		{
			gentleState:  protocol.CognitiveStateRejectedPrecondition,
			wantA2AState: protocol.A2AStateRejected,
			wantReason:   "precondition_failed",
			isTerminal:   true,
		},
		{
			gentleState:  protocol.CognitiveStateRejectedCapability,
			wantA2AState: protocol.A2AStateRejected,
			wantReason:   "missing_capability",
			isTerminal:   true,
		},
		{
			gentleState:  protocol.CognitiveStateRunning,
			wantA2AState: protocol.A2AStateWorking,
			wantSubstate: "executing",
			isTerminal:   false,
		},
		{
			gentleState:  protocol.CognitiveStateSettling,
			wantA2AState: protocol.A2AStateWorking,
			wantSubstate: "evaluating_assertions",
			isTerminal:   false,
		},
		{
			gentleState:  protocol.CognitiveStateSettledClean,
			wantA2AState: protocol.A2AStateCompleted,
			isTerminal:   true,
		},
		{
			gentleState:  protocol.CognitiveStateSettlementFailed,
			wantA2AState: protocol.A2AStateFailed,
			isTerminal:   true,
		},
		{
			gentleState:  protocol.CognitiveStateDisputed,
			wantA2AState: protocol.A2AStateFailed,
			isTerminal:   true,
		},
		{
			gentleState:  protocol.CognitiveStateSettlementTimeout,
			wantA2AState: protocol.A2AStateFailed,
			wantReason:   "timeout",
			isTerminal:   true,
		},
		{
			gentleState:  protocol.CognitiveStateAborted,
			wantA2AState: protocol.A2AStateCanceled,
			wantReason:   "canceled_by_client",
			isTerminal:   true,
		},
	}

	for _, tt := range expectedMappings {
		t.Run(string(tt.gentleState), func(t *testing.T) {
			proj, ok := protocol.MapCognitiveToA2A(tt.gentleState)
			if !ok {
				t.Fatalf("expected mapping for state %s, got false", tt.gentleState)
			}
			if proj.State != tt.wantA2AState {
				t.Errorf("state mismatch for %s: got %s, want %s", tt.gentleState, proj.State, tt.wantA2AState)
			}
			if proj.Substate != tt.wantSubstate {
				t.Errorf("substate mismatch for %s: got %q, want %q", tt.gentleState, proj.Substate, tt.wantSubstate)
			}
			if proj.Reason != tt.wantReason {
				t.Errorf("reason mismatch for %s: got %q, want %q", tt.gentleState, proj.Reason, tt.wantReason)
			}
			if proj.State.IsTerminal() != tt.isTerminal {
				t.Errorf("terminal invariant mismatch for %s (%s): got terminal=%v, want %v",
					tt.gentleState, proj.State, proj.State.IsTerminal(), tt.isTerminal)
			}
		})
	}
}

func TestA2A_UnknownCognitiveState(t *testing.T) {
	_, ok := protocol.MapCognitiveToA2A(protocol.CognitiveState("NON_EXISTENT_STATE"))
	if ok {
		t.Error("expected ok=false for unknown cognitive state, got true")
	}
}
