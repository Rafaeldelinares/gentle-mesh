package registry_test

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
)

func sampleJoinRequest(nodeID, endpoint string) protocol.NodeJoinRequest {
	return protocol.NodeJoinRequest{
		NodeID:   nodeID,
		Endpoint: endpoint,
		Hardware: protocol.NodeHardware{
			CPUs:   8,
			RAMGB:  32,
			HasGPU: true,
			OS:     "linux/amd64",
		},
		Agents: []protocol.AgentProfile{
			{
				Name:        "worker",
				Description: "Standard task runner",
				Tags:        []string{"git", "docker"},
			},
			{
				Name:        "heavy-tester",
				Description: "High concurrency tester",
				Tags:        []string{"docker", "postgres", "gpu"},
			},
		},
		MaxConcurrency: 4,
	}
}

func TestRegistry_RegisterNode(t *testing.T) {
	reg := registry.NewRegistry(30 * time.Second)

	// Validation: empty node_id or endpoint
	_, err := reg.RegisterNode(protocol.NodeJoinRequest{NodeID: "", Endpoint: "http://localhost:8080"})
	if !errors.Is(err, registry.ErrInvalidNodeInfo) {
		t.Fatalf("expected ErrInvalidNodeInfo for empty node_id, got: %v", err)
	}
	_, err = reg.RegisterNode(protocol.NodeJoinRequest{NodeID: "node-1", Endpoint: ""})
	if !errors.Is(err, registry.ErrInvalidNodeInfo) {
		t.Fatalf("expected ErrInvalidNodeInfo for empty endpoint, got: %v", err)
	}

	// Successful registration
	req := sampleJoinRequest("node-1", "http://10.0.0.1:8080")
	info, err := reg.RegisterNode(req)
	if err != nil {
		t.Fatalf("unexpected error registering node: %v", err)
	}

	if info.NodeID != "node-1" {
		t.Errorf("expected nodeID node-1, got %q", info.NodeID)
	}
	if info.Status != protocol.NodeStatusOnline {
		t.Errorf("expected status online, got %q", info.Status)
	}
	if info.ActiveTasks != 0 {
		t.Errorf("expected 0 active tasks, got %d", info.ActiveTasks)
	}
	if info.MaxConcurrency != 4 {
		t.Errorf("expected max concurrency 4, got %d", info.MaxConcurrency)
	}
	if len(info.Agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(info.Agents))
	}
	if info.LastHeartbeat == 0 {
		t.Error("expected non-zero last heartbeat")
	}

	// Re-registering existing node updates fields
	updateReq := req
	updateReq.Endpoint = "http://10.0.0.2:9090"
	updateReq.MaxConcurrency = 8
	updateReq.Hardware.RAMGB = 64

	updatedInfo, err := reg.RegisterNode(updateReq)
	if err != nil {
		t.Fatalf("unexpected error re-registering node: %v", err)
	}
	if updatedInfo.Endpoint != "http://10.0.0.2:9090" {
		t.Errorf("expected updated endpoint, got %q", updatedInfo.Endpoint)
	}
	if updatedInfo.MaxConcurrency != 8 {
		t.Errorf("expected updated max concurrency 8, got %d", updatedInfo.MaxConcurrency)
	}
	if updatedInfo.Hardware.RAMGB != 64 {
		t.Errorf("expected updated RAM 64, got %d", updatedInfo.Hardware.RAMGB)
	}

	// Deep copy mutation check: mutating returned info should not affect registry
	updatedInfo.Endpoint = "mutated-endpoint"
	retrieved, ok := reg.GetNode("node-1")
	if !ok {
		t.Fatal("expected node-1 to be found")
	}
	if retrieved.Endpoint == "mutated-endpoint" {
		t.Fatal("registry internal state was mutated via external reference")
	}
}

func TestRegistry_Heartbeat(t *testing.T) {
	reg := registry.NewRegistry(30 * time.Second)

	// Heartbeat on nonexistent node
	err := reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID:      "unknown-node",
		ActiveTasks: 1,
	})
	if !errors.Is(err, registry.ErrNodeNotFound) {
		t.Fatalf("expected ErrNodeNotFound, got: %v", err)
	}

	// Register node
	req := sampleJoinRequest("node-hb", "http://10.0.0.1:8080")
	_, err = reg.RegisterNode(req)
	if err != nil {
		t.Fatalf("failed to register node: %v", err)
	}

	// Send heartbeat with explicit timestamp
	customTime := time.Now().Add(10 * time.Minute).Unix()
	err = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID:      "node-hb",
		ActiveTasks: 3,
		Timestamp:   customTime,
	})
	if err != nil {
		t.Fatalf("unexpected error sending heartbeat: %v", err)
	}

	node, ok := reg.GetNode("node-hb")
	if !ok {
		t.Fatal("expected node to exist")
	}
	if node.ActiveTasks != 3 {
		t.Errorf("expected ActiveTasks 3, got %d", node.ActiveTasks)
	}
	if node.LastHeartbeat != customTime {
		t.Errorf("expected LastHeartbeat %d, got %d", customTime, node.LastHeartbeat)
	}
	if node.Status != protocol.NodeStatusOnline {
		t.Errorf("expected Status online, got %s", node.Status)
	}

	// Send heartbeat with zero timestamp (defaults to current time)
	err = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID:      "node-hb",
		ActiveTasks: 1,
		Timestamp:   0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	node, _ = reg.GetNode("node-hb")
	if node.ActiveTasks != 1 {
		t.Errorf("expected ActiveTasks 1, got %d", node.ActiveTasks)
	}
}

func TestRegistry_GetAndListNodes(t *testing.T) {
	reg := registry.NewRegistry(30 * time.Second)

	if len(reg.ListNodes()) != 0 {
		t.Fatal("expected empty list initially")
	}

	_, _ = reg.RegisterNode(sampleJoinRequest("node-1", "http://10.0.0.1:8080"))
	_, _ = reg.RegisterNode(sampleJoinRequest("node-2", "http://10.0.0.2:8080"))

	nodes := reg.ListNodes()
	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(nodes))
	}

	// Deregister node-1
	err := reg.DeregisterNode("node-1")
	if err != nil {
		t.Fatalf("unexpected error deregistering node-1: %v", err)
	}

	if _, ok := reg.GetNode("node-1"); ok {
		t.Fatal("expected node-1 to be removed")
	}

	// Deregister again should return ErrNodeNotFound
	if err := reg.DeregisterNode("node-1"); !errors.Is(err, registry.ErrNodeNotFound) {
		t.Fatalf("expected ErrNodeNotFound on second deregister, got: %v", err)
	}
}

func TestRegistry_PruneStaleNodes(t *testing.T) {
	timeout := 10 * time.Second
	reg := registry.NewRegistry(timeout)

	// Register 3 nodes
	_, _ = reg.RegisterNode(sampleJoinRequest("node-fresh", "http://10.0.0.1:8080"))
	_, _ = reg.RegisterNode(sampleJoinRequest("node-degraded", "http://10.0.0.2:8080"))
	_, _ = reg.RegisterNode(sampleJoinRequest("node-offline", "http://10.0.0.3:8080"))

	now := time.Now()

	// Simulate heartbeats at different ages
	// Fresh: 5 seconds ago (< 1.5 * 10s = 15s)
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID:    "node-fresh",
		Timestamp: now.Add(-5 * time.Second).Unix(),
	})
	// Degraded: 20 seconds ago (> 15s and <= 30s)
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID:    "node-degraded",
		Timestamp: now.Add(-20 * time.Second).Unix(),
	})
	// Offline: 40 seconds ago (> 3 * 10s = 30s)
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID:    "node-offline",
		Timestamp: now.Add(-40 * time.Second).Unix(),
	})

	reg.PruneStaleNodes()

	freshNode, _ := reg.GetNode("node-fresh")
	if freshNode.Status != protocol.NodeStatusOnline {
		t.Errorf("expected node-fresh to be online, got %s", freshNode.Status)
	}

	degNode, _ := reg.GetNode("node-degraded")
	if degNode.Status != protocol.NodeStatusDegraded {
		t.Errorf("expected node-degraded to be degraded, got %s", degNode.Status)
	}

	offNode, _ := reg.GetNode("node-offline")
	if offNode.Status != protocol.NodeStatusOffline {
		t.Errorf("expected node-offline to be offline, got %s", offNode.Status)
	}

	// Sending heartbeat should recover degraded node to online
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID:    "node-degraded",
		Timestamp: time.Now().Unix(),
	})
	degNode, _ = reg.GetNode("node-degraded")
	if degNode.Status != protocol.NodeStatusOnline {
		t.Errorf("expected node-degraded to return to online, got %s", degNode.Status)
	}

	// Prune with timeout <= 0 should do nothing
	noTimeoutReg := registry.NewRegistry(0)
	_, _ = noTimeoutReg.RegisterNode(sampleJoinRequest("n1", "http://localhost:8080"))
	noTimeoutReg.PruneStaleNodes()
	n1, _ := noTimeoutReg.GetNode("n1")
	if n1.Status != protocol.NodeStatusOnline {
		t.Errorf("expected n1 to remain online with timeout 0, got %s", n1.Status)
	}
}

func TestRegistry_SelectNodeForTask(t *testing.T) {
	reg := registry.NewRegistry(30 * time.Second)

	// No nodes in registry
	_, err := reg.SelectNodeForTask("worker")
	if !errors.Is(err, registry.ErrAgentNotSupported) {
		t.Fatalf("expected ErrAgentNotSupported on empty registry, got: %v", err)
	}

	// Node 1: supports "worker" (tags: git, docker), MaxConcurrency: 4, ActiveTasks: 2
	n1 := sampleJoinRequest("node-1", "http://10.0.0.1:8080")
	n1.MaxConcurrency = 4
	_, _ = reg.RegisterNode(n1)
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{NodeID: "node-1", ActiveTasks: 2})

	// Node 2: supports "worker" (tags: git, docker, k8s), MaxConcurrency: 4, ActiveTasks: 1
	n2 := sampleJoinRequest("node-2", "http://10.0.0.2:8080")
	n2.MaxConcurrency = 4
	n2.Agents = []protocol.AgentProfile{
		{Name: "worker", Tags: []string{"git", "docker", "k8s"}},
	}
	_, _ = reg.RegisterNode(n2)
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{NodeID: "node-2", ActiveTasks: 1})

	// Agent not supported by any node
	_, err = reg.SelectNodeForTask("designer")
	if !errors.Is(err, registry.ErrAgentNotSupported) {
		t.Fatalf("expected ErrAgentNotSupported for designer, got: %v", err)
	}

	// Tag not supported by any node: worker with tag "solidity"
	_, err = reg.SelectNodeForTask("worker", "solidity")
	if !errors.Is(err, registry.ErrAgentNotSupported) {
		t.Fatalf("expected ErrAgentNotSupported for worker with solidity, got: %v", err)
	}

	// Tag "k8s" only supported by node-2
	sel, err := reg.SelectNodeForTask("worker", "k8s")
	if err != nil {
		t.Fatalf("unexpected error selecting node: %v", err)
	}
	if sel.NodeID != "node-2" {
		t.Errorf("expected node-2 for k8s tag, got %s", sel.NodeID)
	}

	// Both support "worker" without extra tags.
	// Node 1: ActiveTasks: 2, MaxConcurrency: 4 -> ratio 0.5
	// Node 2: ActiveTasks: 1, MaxConcurrency: 4 -> ratio 0.25
	// Node 2 has lowest ratio and lowest active tasks -> should be chosen.
	sel, err = reg.SelectNodeForTask("worker")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sel.NodeID != "node-2" {
		t.Errorf("expected node-2 with lower load, got %s", sel.NodeID)
	}

	// Test weighted load balancing:
	// Node 3: supports "worker", MaxConcurrency: 10, ActiveTasks: 2 -> ratio 0.2
	// Node 2: ActiveTasks: 1, MaxConcurrency: 4 -> ratio 0.25
	// Node 3 has better ratio (0.2 < 0.25), so Node 3 should be chosen despite having 2 active tasks vs 1.
	n3 := sampleJoinRequest("node-3", "http://10.0.0.3:8080")
	n3.MaxConcurrency = 10
	_, _ = reg.RegisterNode(n3)
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{NodeID: "node-3", ActiveTasks: 2})

	sel, err = reg.SelectNodeForTask("worker")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sel.NodeID != "node-3" {
		t.Errorf("expected node-3 with best ratio (0.2), got %s", sel.NodeID)
	}

	// Capacity exhaustion: set all nodes to full capacity
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{NodeID: "node-1", ActiveTasks: 4})  // 4/4
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{NodeID: "node-2", ActiveTasks: 4})  // 4/4
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{NodeID: "node-3", ActiveTasks: 10}) // 10/10

	_, err = reg.SelectNodeForTask("worker")
	if !errors.Is(err, registry.ErrNoNodeAvailable) {
		t.Fatalf("expected ErrNoNodeAvailable when all nodes full, got: %v", err)
	}

	// Degraded / Offline node handling: if only node supporting an agent is offline
	n4 := sampleJoinRequest("node-4", "http://10.0.0.4:8080")
	n4.Agents = []protocol.AgentProfile{{Name: "specialist"}}
	_, _ = reg.RegisterNode(n4)
	// Mark offline via prune or custom timestamp
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
		NodeID:    "node-4",
		Timestamp: time.Now().Add(-100 * time.Second).Unix(),
	})
	reg.PruneStaleNodes()

	_, err = reg.SelectNodeForTask("specialist")
	if !errors.Is(err, registry.ErrAgentNotSupported) {
		t.Fatalf("expected ErrAgentNotSupported for offline node, got: %v", err)
	}

	// Node with MaxConcurrency <= 0 defaults to concurrency 1
	n5 := sampleJoinRequest("node-5", "http://10.0.0.5:8080")
	n5.MaxConcurrency = 0
	n5.Agents = []protocol.AgentProfile{{Name: "single-tasker"}}
	_, _ = reg.RegisterNode(n5)
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{NodeID: "node-5", ActiveTasks: 0})

	sel, err = reg.SelectNodeForTask("single-tasker")
	if err != nil {
		t.Fatalf("unexpected error for single-tasker: %v", err)
	}
	if sel.NodeID != "node-5" {
		t.Errorf("expected node-5, got %s", sel.NodeID)
	}

	// Once active tasks reaches 1, it should be full
	_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{NodeID: "node-5", ActiveTasks: 1})
	_, err = reg.SelectNodeForTask("single-tasker")
	if !errors.Is(err, registry.ErrNoNodeAvailable) {
		t.Fatalf("expected ErrNoNodeAvailable for single-tasker at max 1, got: %v", err)
	}
}

func TestRegistry_CompositionGetters(t *testing.T) {
	reg := registry.NewRegistry(30 * time.Second)

	// Check Locks() getter
	if reg.Locks() == nil {
		t.Fatal("expected non-nil Locks()")
	}
	if err := reg.ClaimLock("repo", "main", "t1"); err != nil {
		t.Fatalf("unexpected error claiming lock: %v", err)
	}
	if err := reg.Locks().ClaimLock("repo", "main", "t2"); !errors.Is(err, registry.ErrBranchLocked) {
		t.Fatalf("expected ErrBranchLocked, got: %v", err)
	}

	// Check Idempotency() getter
	if reg.Idempotency() == nil {
		t.Fatal("expected non-nil Idempotency()")
	}
	_, isDup := reg.RecordOrGet("k1", "t1")
	if isDup {
		t.Fatal("expected first record not duplicate")
	}
	taskID, isDup := reg.Idempotency().RecordOrGet("k1", "t2")
	if !isDup || taskID != "t1" {
		t.Fatalf("expected duplicate returning t1, got taskID=%s, isDup=%v", taskID, isDup)
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	reg := registry.NewRegistry(10 * time.Second)

	const numGoroutines = 60
	var wg sync.WaitGroup

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			nodeID := fmt.Sprintf("node-%d", id%5)
			endpoint := fmt.Sprintf("http://10.0.0.%d:8080", id%5)

			switch id % 6 {
			case 0:
				_, _ = reg.RegisterNode(sampleJoinRequest(nodeID, endpoint))
			case 1:
				_ = reg.Heartbeat(protocol.NodeHeartbeatRequest{
					NodeID:      nodeID,
					ActiveTasks: id % 3,
				})
			case 2:
				_, _ = reg.GetNode(nodeID)
				_ = reg.ListNodes()
			case 3:
				reg.PruneStaleNodes()
			case 4:
				_, _ = reg.SelectNodeForTask("worker")
			case 5:
				repo := fmt.Sprintf("repo-%d", id%3)
				branch := "main"
				taskID := fmt.Sprintf("task-%d", id)
				_ = reg.ClaimLock(repo, branch, taskID)
				_, _, _ = reg.GetLock(repo, branch)
				_ = reg.ReleaseLock(repo, branch, taskID)
				_, _ = reg.RecordOrGet(fmt.Sprintf("key-%d", id%10), taskID)
			}
		}(i)
	}

	wg.Wait()
}
