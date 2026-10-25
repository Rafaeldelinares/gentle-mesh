package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

// meshDispatchAttempts bounds how many nodes a single task may be sent to when the
// previous one never accepted it. A task is never retried after a worker answered.
const meshDispatchAttempts = 3

// NodeSelector defines the capability to choose an appropriate worker node from the mesh.
type NodeSelector interface {
	SelectNodeForTask(agent string, requiredTags ...string) (*protocol.NodeInfo, error)
	// MarkNodeUnreachable takes a node out of the rotation after a dispatch that failed
	// before the worker accepted the task.
	MarkNodeUnreachable(nodeID string) error
}

// MeshRunnerOptions configures routing, fallback execution, and HTTP connectivity for MeshRunner.
type MeshRunnerOptions struct {
	Selector       NodeSelector
	FallbackRunner Runner
	Client         *http.Client
	Token          string
	DispatchToken  string
}

// MeshRunner routes task execution to remote federated nodes when available, falling back as configured.
type MeshRunner struct {
	selector       NodeSelector
	fallbackRunner Runner
	client         *http.Client
	token          string
}

// NewMeshRunner instantiates a new MeshRunner.
func NewMeshRunner(opts MeshRunnerOptions) *MeshRunner {
	token := opts.DispatchToken
	if token == "" {
		token = opts.Token
	}
	return &MeshRunner{
		selector:       opts.Selector,
		fallbackRunner: opts.FallbackRunner,
		client:         opts.Client,
		token:          token,
	}
}

// localExecutionSink marks the completion of a run executed by the coordinator's own
// fallback runner, so a client can tell it apart from a run performed on a mesh
// worker. Every other event is forwarded untouched, and the remote path never uses
// this wrapper.
type localExecutionSink struct {
	EventSink
}

// EmitEvent adds the local-execution marker to a completion payload. Runners emit
// protocol.CompletionPayload by value, as the simulated and the pi runner do.
func (s localExecutionSink) EmitEvent(eventType protocol.EventType, payload any) (protocol.Event, error) {
	if eventType == protocol.EventCompletion {
		if completion, ok := payload.(protocol.CompletionPayload); ok {
			completion.ExecutedLocally = true
			payload = completion
		}
	}
	return s.EventSink.EmitEvent(eventType, payload)
}

// Run attempts to route the task to a selected mesh worker, or delegates to the fallback runner.
//
// A dispatch that fails before the worker accepted the task (connection refused, no route,
// DNS) marks that node unreachable and the task is offered to another node, up to
// meshDispatchAttempts times. A dispatch that fails after the connection was established,
// or a task the worker reported as failed, is returned unchanged: retrying it elsewhere
// could execute it twice.
func (m *MeshRunner) Run(ctx context.Context, req protocol.TaskRequest, sink EventSink) error {
	var err error = errors.New("no node selector configured")

	if m.selector != nil {
		dispatchErr, selectErr := m.dispatchToMesh(ctx, req, sink)
		if dispatchErr == nil && selectErr == nil {
			return nil
		}
		if dispatchErr != nil {
			return dispatchErr
		}
		err = selectErr
	}

	if m.fallbackRunner != nil {
		log.Printf("[mesh-router] falling back to local runner for task (agent: %q, tags: %v): %v", req.Agent, req.Tags, err)
		return m.fallbackRunner.Run(ctx, req, localExecutionSink{EventSink: sink})
	}

	return fmt.Errorf("no mesh worker available for task (agent: %q, tags: %v): %w", req.Agent, req.Tags, err)
}

// dispatchToMesh offers the task to mesh workers.
//
// It returns (nil, nil) when a worker ran it, (dispatchErr, nil) when the task reached a
// worker and failed (or the retry budget ran out), and (nil, selectErr) when no node could
// be selected, which is the only case the caller answers with the local fallback.
// nodeAttributionSink carries the identity of the mesh node that accepted a task to the event
// sink, so the task state and the store can record who executed it. The attribution happens on the
// first event the node produces, which is only reached after the node accepted the task: a failed
// dispatch attempt that never emitted anything does not claim the task. Every event is forwarded
// untouched.
type nodeAttributionSink struct {
	EventSink
	nodeID string
	once   sync.Once
}

// EmitEvent attributes the wrapped sink with this node on the first forwarded event, when that
// sink supports attribution, and then forwards the event untouched.
func (s *nodeAttributionSink) EmitEvent(eventType protocol.EventType, payload any) (protocol.Event, error) {
	s.once.Do(func() {
		if s.nodeID == "" {
			return
		}
		if attributor, ok := s.EventSink.(NodeAttributor); ok {
			attributor.AttributeNode(s.nodeID)
		}
	})
	return s.EventSink.EmitEvent(eventType, payload)
}

// NodeReserver is an optional selector capability. A selector that implements it lets the
// MeshRunner reserve capacity on the node it is about to dispatch to, for exactly as long as
// one dispatch attempt lasts, so two concurrent dispatches cannot both see the same node as
// idle. Selectors that do not implement it keep the previous behaviour: selection only.
type NodeReserver interface {
	SelectAndReserve(agent string, requiredTags ...string) (*protocol.NodeInfo, error)
	Release(nodeID string)
}

// selectNodeForAttempt selects a node for one dispatch attempt. When the selector supports
// reservation the node comes back reserved and the returned function gives that slot back
// exactly once; otherwise the returned function is a no-op.
func (m *MeshRunner) selectNodeForAttempt(req protocol.TaskRequest) (*protocol.NodeInfo, func(), error) {
	if reserver, ok := m.selector.(NodeReserver); ok {
		node, err := reserver.SelectAndReserve(req.Agent, req.Tags...)
		if err != nil {
			return nil, nil, err
		}
		if node == nil || node.Endpoint == "" {
			if node != nil {
				reserver.Release(node.NodeID)
			}
			return nil, nil, errors.New("selected node is nil or missing endpoint")
		}
		return node, func() { reserver.Release(node.NodeID) }, nil
	}

	node, err := m.selector.SelectNodeForTask(req.Agent, req.Tags...)
	if err != nil {
		return nil, nil, err
	}
	if node == nil || node.Endpoint == "" {
		return nil, nil, errors.New("selected node is nil or missing endpoint")
	}
	return node, func() {}, nil
}

// runOnNode dispatches to one node and always gives the reserved slot back when it returns,
// including on an error (like 429) or panic: the release runs before the error is reported
// and before the next attempt reserves another node.
func (m *MeshRunner) runOnNode(ctx context.Context, req protocol.TaskRequest, sink EventSink, node *protocol.NodeInfo, release func()) error {
	defer release()

	log.Printf("[mesh-router] dispatching task (agent: %q, tags: %v) -> node %q (endpoint: %s)", req.Agent, req.Tags, node.NodeID, node.Endpoint)

	// Record who accepted the task, so the state and the store can answer "which node ran
	// this". Only the remote path attributes, and only once the node has produced its first
	// event: an attempt that never got accepted never claims the task, which is what makes a
	// retry on another node report the node that actually ran it.
	attributed := &nodeAttributionSink{EventSink: sink, nodeID: node.NodeID}
	remote := NewRemoteRunner(RemoteRunnerConfig{
		Endpoint: node.Endpoint,
		Client:   m.client,
		Token:    m.token,
	})

	return remote.Run(ctx, req, attributed)
}

func (m *MeshRunner) dispatchToMesh(ctx context.Context, req protocol.TaskRequest, sink EventSink) (dispatchErr, selectErr error) {
	var lastDispatchErr error

	for attempt := 1; attempt <= meshDispatchAttempts; attempt++ {
		node, release, err := m.selectNodeForAttempt(req)
		if err != nil {
			if lastDispatchErr != nil {
				// A dispatch already failed (or nodes were busy): the task must not be silently
				// executed by the local fallback, so the dispatch error is what the caller gets.
				return lastDispatchErr, nil
			}
			// Nothing was ever dispatched: this is the "no node available" case, which is
			// the only one the local fallback answers.
			return nil, err
		}

		runErr := m.runOnNode(ctx, req, sink, node, release)
		if runErr == nil {
			return nil, nil
		}
		lastDispatchErr = runErr

		if IsNodeBusyError(runErr) {
			// Worker returned 429: it is alive, but busy. Mark busy for this attempt without
			// marking unreachable, and try another node within the attempts budget.
			// The inFlight slot was already released by runOnNode before returning.
			log.Printf("[mesh-router] node %q is busy (429); trying another node without marking unreachable (attempt %d/%d)",
				node.NodeID, attempt, meshDispatchAttempts)
			continue
		}

		if !isPreAcceptanceTransportError(runErr) {
			// The worker answered, or the task already started: retrying on another node
			// could run it twice, so the error is reported untouched.
			return runErr, nil
		}

		log.Printf("[mesh-router] node %q did not accept the task (%v); marking it unreachable and retrying (attempt %d/%d)",
			node.NodeID, runErr, attempt, meshDispatchAttempts)
		if markErr := m.selector.MarkNodeUnreachable(node.NodeID); markErr != nil {
			log.Printf("[mesh-router] could not mark node %q as unreachable: %v", node.NodeID, markErr)
		}
	}

	return lastDispatchErr, nil
}

// isPreAcceptanceTransportError reports whether err happened before the worker accepted the
// task: a dial failure (connection refused, no route, DNS, dial timeout). Only those are
// safe to retry on another node; anything that happened after the connection was
// established (an HTTP status, a broken stream, a task the worker reported as failed) is not.
func isPreAcceptanceTransportError(err error) bool {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return netErr.Op == "dial"
	}
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr)
}
