package registry

import (
	"errors"
	"log"
	"sync"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

var (
	// ErrNodeNotFound is returned when a node is not found in the registry.
	ErrNodeNotFound = errors.New("node not found")
	// ErrNodeAlreadyExists is returned when attempting to register a node that already exists.
	ErrNodeAlreadyExists = errors.New("node already exists")
	// ErrNoNodeAvailable is returned when no healthy node has available capacity.
	ErrNoNodeAvailable = errors.New("no healthy node available with required capacity")
	// ErrAgentNotSupported is returned when no online registered node supports the requested agent.
	ErrAgentNotSupported = errors.New("no registered node supports the requested agent")
	// ErrInvalidNodeInfo is returned when node registration payload is missing required fields.
	ErrInvalidNodeInfo = errors.New("invalid node info: node_id and endpoint must not be empty")
)

// Registry manages federated worker nodes, heartbeats, task routing,
// exclusive branch locks, and idempotency tracking.
type Registry struct {
	mu               sync.RWMutex
	nodes            map[string]*protocol.NodeInfo
	heartbeatTimeout time.Duration
	// inFlight counts the dispatches this coordinator already handed out and that have not
	// finished yet. The heartbeat never writes it: it is the load the coordinator knows about
	// but the node has not reported yet, which is what makes work spread during a burst.
	inFlight map[string]int
	// lastSelected orders nodes by how recently they were selected (a counter, not a clock),
	// so equally loaded nodes rotate instead of always resolving to the lowest NodeID.
	lastSelected map[string]uint64
	selectionSeq uint64
	// lastCapacityLog remembers when the soft-cap log line was last written, so a busy mesh
	// with every node full does not flood the log with one line per task. It is only touched
	// by selection, which holds the registry lock for writing.
	lastCapacityLog time.Time
	*BranchLockManager
	*IdempotencyStore
}

// NewRegistry creates a new Registry with the given heartbeat timeout.
// Default branch locking and 24-hour idempotency stores are attached.
func NewRegistry(heartbeatTimeout time.Duration) *Registry {
	return &Registry{
		nodes:             make(map[string]*protocol.NodeInfo),
		heartbeatTimeout:  heartbeatTimeout,
		inFlight:          make(map[string]int),
		lastSelected:      make(map[string]uint64),
		BranchLockManager: NewBranchLockManager(),
		IdempotencyStore:  NewIdempotencyStore(24 * time.Hour),
	}
}

// Locks returns the exclusive branch lock manager.
func (r *Registry) Locks() *BranchLockManager {
	return r.BranchLockManager
}

// Idempotency returns the idempotency store.
func (r *Registry) Idempotency() *IdempotencyStore {
	return r.IdempotencyStore
}

// RegisterNode registers or updates a node in the mesh registry.
// Validates node_id and endpoint. If the node already exists, updates hardware,
// agents, max_concurrency, endpoint, and sets status to Online with refreshed heartbeat.
func (r *Registry) RegisterNode(req protocol.NodeJoinRequest) (*protocol.NodeInfo, error) {
	if req.NodeID == "" || req.Endpoint == "" {
		return nil, ErrInvalidNodeInfo
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().Unix()
	existing, exists := r.nodes[req.NodeID]
	if exists {
		existing.Endpoint = req.Endpoint
		existing.Hardware = req.Hardware
		existing.Agents = copyAgents(req.Agents)
		existing.MaxConcurrency = req.MaxConcurrency
		existing.Status = protocol.NodeStatusOnline
		existing.LastHeartbeat = now
		return cloneNodeInfo(existing), nil
	}

	node := &protocol.NodeInfo{
		NodeID:         req.NodeID,
		Endpoint:       req.Endpoint,
		Hardware:       req.Hardware,
		Agents:         copyAgents(req.Agents),
		MaxConcurrency: req.MaxConcurrency,
		ActiveTasks:    0,
		Status:         protocol.NodeStatusOnline,
		LastHeartbeat:  now,
	}
	r.nodes[req.NodeID] = node
	return cloneNodeInfo(node), nil
}

// DeregisterNode removes a node from the registry.
func (r *Registry) DeregisterNode(nodeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.nodes[nodeID]; !exists {
		return ErrNodeNotFound
	}

	delete(r.nodes, nodeID)
	return nil
}

// Heartbeat processes a periodic keepalive ping from a worker node.
// Updates last heartbeat timestamp, active tasks, and restores status to Online.
func (r *Registry) Heartbeat(req protocol.NodeHeartbeatRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	node, exists := r.nodes[req.NodeID]
	if !exists {
		return ErrNodeNotFound
	}

	timestamp := req.Timestamp
	if timestamp <= 0 {
		timestamp = time.Now().Unix()
	}

	node.LastHeartbeat = timestamp
	node.ActiveTasks = req.ActiveTasks
	node.Status = protocol.NodeStatusOnline
	return nil
}

// GetNode retrieves a point-in-time copy of NodeInfo for the specified node ID.
func (r *Registry) GetNode(nodeID string) (*protocol.NodeInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	node, exists := r.nodes[nodeID]
	if !exists {
		return nil, false
	}
	return cloneNodeInfo(node), true
}

// ListNodes returns a slice of point-in-time copies of all registered nodes.
func (r *Registry) ListNodes() []*protocol.NodeInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*protocol.NodeInfo, 0, len(r.nodes))
	for _, node := range r.nodes {
		result = append(result, cloneNodeInfo(node))
	}
	return result
}

// PruneStaleNodes checks heartbeat freshness for all nodes:
//   - If time since LastHeartbeat > 3 * heartbeatTimeout, marks node Offline.
//   - Else if time since LastHeartbeat > 1.5 * heartbeatTimeout, marks node Degraded.
func (r *Registry) PruneStaleNodes() {
	if r.heartbeatTimeout <= 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	offlineThreshold := r.heartbeatTimeout * 3
	degradedThreshold := time.Duration(float64(r.heartbeatTimeout) * 1.5)

	for _, node := range r.nodes {
		elapsed := now.Sub(time.Unix(node.LastHeartbeat, 0))
		if elapsed > offlineThreshold {
			node.Status = protocol.NodeStatusOffline
		} else if elapsed > degradedThreshold {
			node.Status = protocol.NodeStatusDegraded
		}
	}
}

// SelectNodeForTask selects the optimal healthy online node to execute a task based on:
//  1. Status and heartbeat freshness: a node whose heartbeats stopped is skipped even
//     while its stored status still says Online, so work is never sent to a dead node.
//  2. Agent and tag capabilities (must support requested agent and all required tags).
//  3. Capacity availability, judged on the effective load (see effectiveLoad): nodes with a
//     free slot are preferred, and when every node is at capacity the least loaded one is
//     still chosen (soft cap), so a full mesh never turns into a local fallback.
//  4. Load balancing: lowest effective load / capacity ratio, tie-breaking on lowest effective
//     load, then the least recently selected node, then the lowest NodeID.
//
// It reserves nothing: use SelectAndReserve when the caller is about to dispatch.
func (r *Registry) SelectNodeForTask(agent string, requiredTags ...string) (*protocol.NodeInfo, error) {
	// A write lock (not a read lock) because the selection may rate-limit its own saturation
	// log line, which updates lastCapacityLog.
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.selectLocked(agent, requiredTags)
}

// SelectAndReserve selects a node exactly like SelectNodeForTask and, under the same lock,
// counts one dispatch in flight on it. The caller must Release the node once the dispatch
// attempt ends (success, error, cancellation or panic), which is what keeps concurrent
// dispatches from all choosing the same idle-looking node.
func (r *Registry) SelectAndReserve(agent string, requiredTags ...string) (*protocol.NodeInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	node, err := r.selectLocked(agent, requiredTags)
	if err != nil {
		return nil, err
	}
	r.inFlight[node.NodeID]++
	r.selectionSeq++
	r.lastSelected[node.NodeID] = r.selectionSeq
	return node, nil
}

// Release gives back one dispatch slot taken by SelectAndReserve. Releasing a node with no
// dispatch in flight is a no-op, so the counter can never go below zero.
func (r *Registry) Release(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.inFlight[nodeID] > 0 {
		r.inFlight[nodeID]--
	}
}

// effectiveLoad is the load the coordinator believes a node carries: the maximum of what the
// node reported in its last heartbeat and the dispatches still in flight here. max (not the
// sum) avoids counting the same task twice, and it self-heals: a heartbeat reporting more wins,
// and when the dispatches finish the counter drains back to the reported value.
func (r *Registry) effectiveLoad(node *protocol.NodeInfo) int {
	inFlight := r.inFlight[node.NodeID]
	if node.ActiveTasks > inFlight {
		return node.ActiveTasks
	}
	return inFlight
}

// effectiveCapacity is the node's declared concurrency, with non-positive values treated as a
// single slot, the same default the selection has always applied.
func effectiveCapacity(node *protocol.NodeInfo) int {
	if node.MaxConcurrency <= 0 {
		return 1
	}
	return node.MaxConcurrency
}

// selectLocked runs the selection algorithm. The caller must hold r.mu for writing.
func (r *Registry) selectLocked(agent string, requiredTags []string) (*protocol.NodeInfo, error) {
	var matchingOnline []*protocol.NodeInfo
	for _, node := range r.nodes {
		if node.Status == protocol.NodeStatusOnline && r.heartbeatIsFresh(node) && nodeSupportsAgent(node, agent, requiredTags) {
			matchingOnline = append(matchingOnline, node)
		}
	}

	if len(matchingOnline) == 0 {
		return nil, ErrAgentNotSupported
	}

	candidates := make([]*protocol.NodeInfo, 0, len(matchingOnline))
	for _, node := range matchingOnline {
		if r.effectiveLoad(node) < effectiveCapacity(node) {
			candidates = append(candidates, node)
		}
	}

	if len(candidates) == 0 {
		// Soft cap: every node is at capacity, and the least loaded one still gets the task.
		// Refusing here would send the client to the local fallback, which reports success for
		// work the mesh never ran. The line is limited to one per second: under a burst every
		// single dispatch would otherwise print it.
		if now := time.Now(); now.Sub(r.lastCapacityLog) >= time.Second {
			log.Printf("[mesh-router] all nodes at capacity: picking least loaded")
			r.lastCapacityLog = now
		}
		candidates = matchingOnline
	}

	var best *protocol.NodeInfo
	var bestRatio float64
	var bestLoad int

	for _, node := range candidates {
		load := r.effectiveLoad(node)
		ratio := float64(load) / float64(effectiveCapacity(node))

		if best == nil || r.preferOver(ratio, load, node, bestRatio, bestLoad, best) {
			best, bestRatio, bestLoad = node, ratio, load
		}
	}

	return cloneNodeInfo(best), nil
}

// preferOver reports whether candidate should replace best: lowest load ratio, then lowest
// effective load, then least recently selected, then lowest NodeID as the last resort.
func (r *Registry) preferOver(ratio float64, load int, candidate *protocol.NodeInfo, bestRatio float64, bestLoad int, best *protocol.NodeInfo) bool {
	if ratio != bestRatio {
		return ratio < bestRatio
	}
	if load != bestLoad {
		return load < bestLoad
	}
	candidateRecent := r.lastSelected[candidate.NodeID]
	bestRecent := r.lastSelected[best.NodeID]
	if candidateRecent != bestRecent {
		return candidateRecent < bestRecent
	}
	return candidate.NodeID < best.NodeID
}

// heartbeatIsFresh reports whether the node's last heartbeat is recent enough to keep
// sending it work. It applies the same 1.5x heartbeatTimeout that PruneStaleNodes uses
// to mark a node Degraded, but on read: a node that stopped beating stops being selected
// immediately, without waiting for the pruning ticker to run.
func (r *Registry) heartbeatIsFresh(node *protocol.NodeInfo) bool {
	if r.heartbeatTimeout <= 0 {
		return true
	}
	return time.Since(time.Unix(node.LastHeartbeat, 0)) <= time.Duration(float64(r.heartbeatTimeout)*1.5)
}

// MarkNodeUnreachable marks a node Degraded after a dispatch failed at the transport
// level, so it leaves the rotation right away instead of waiting for the heartbeat
// timeout. It is a soft mark: the next heartbeat restores the node to Online, so one
// failed dispatch cannot retire an otherwise healthy node.
func (r *Registry) MarkNodeUnreachable(nodeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	node, exists := r.nodes[nodeID]
	if !exists {
		return ErrNodeNotFound
	}
	node.Status = protocol.NodeStatusDegraded
	return nil
}

func nodeSupportsAgent(node *protocol.NodeInfo, agent string, requiredTags []string) bool {
	for _, profile := range node.Agents {
		if profile.Name == agent {
			if hasAllTags(profile.Tags, requiredTags) {
				return true
			}
		}
	}
	return false
}

func hasAllTags(profileTags []string, requiredTags []string) bool {
	if len(requiredTags) == 0 {
		return true
	}
	tagSet := make(map[string]struct{}, len(profileTags))
	for _, t := range profileTags {
		tagSet[t] = struct{}{}
	}
	for _, req := range requiredTags {
		if _, ok := tagSet[req]; !ok {
			return false
		}
	}
	return true
}

func copyAgents(agents []protocol.AgentProfile) []protocol.AgentProfile {
	if agents == nil {
		return nil
	}
	cp := make([]protocol.AgentProfile, len(agents))
	for i, a := range agents {
		cp[i] = a
		if a.Tags != nil {
			cp[i].Tags = make([]string, len(a.Tags))
			copy(cp[i].Tags, a.Tags)
		}
	}
	return cp
}

func cloneNodeInfo(node *protocol.NodeInfo) *protocol.NodeInfo {
	if node == nil {
		return nil
	}
	cp := *node
	cp.Agents = copyAgents(node.Agents)
	return &cp
}
