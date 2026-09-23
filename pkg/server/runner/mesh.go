package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

// NodeSelector defines the capability to choose an appropriate worker node from the mesh.
type NodeSelector interface {
	SelectNodeForTask(agent string, requiredTags ...string) (*protocol.NodeInfo, error)
}

// MeshRunnerOptions configures routing, fallback execution, and HTTP connectivity for MeshRunner.
type MeshRunnerOptions struct {
	Selector       NodeSelector
	FallbackRunner Runner
	Client         *http.Client
	Token          string
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
	return &MeshRunner{
		selector:       opts.Selector,
		fallbackRunner: opts.FallbackRunner,
		client:         opts.Client,
		token:          opts.Token,
	}
}

// Run attempts to route the task to a selected mesh worker, or delegates to the fallback runner.
func (m *MeshRunner) Run(ctx context.Context, req protocol.TaskRequest, sink EventSink) error {
	var err error = errors.New("no node selector configured")

	if m.selector != nil {
		var node *protocol.NodeInfo
		node, err = m.selector.SelectNodeForTask(req.Agent, req.Tags...)
		if err == nil && node != nil && node.Endpoint != "" {
			log.Printf("[mesh-router] dispatching task (agent: %q, tags: %v) -> node %q (endpoint: %s)", req.Agent, req.Tags, node.NodeID, node.Endpoint)
			remote := NewRemoteRunner(RemoteRunnerConfig{
				Endpoint: node.Endpoint,
				Client:   m.client,
				Token:    m.token,
			})
			return remote.Run(ctx, req, sink)
		}
		if err == nil {
			err = errors.New("selected node is nil or missing endpoint")
		}
	}

	if m.fallbackRunner != nil {
		log.Printf("[mesh-router] falling back to local runner for task (agent: %q, tags: %v): %v", req.Agent, req.Tags, err)
		return m.fallbackRunner.Run(ctx, req, sink)
	}

	return fmt.Errorf("no mesh worker available for task (agent: %q, tags: %v): %w", req.Agent, req.Tags, err)
}
