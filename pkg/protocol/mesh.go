package protocol

import "encoding/json"

// NodeStatus defines the operational health status of a node in the mesh.
type NodeStatus string

const (
	NodeStatusOnline   NodeStatus = "online"
	NodeStatusDegraded NodeStatus = "degraded"
	NodeStatusOffline  NodeStatus = "offline"
)

// NodeHardware describes the physical or virtual hardware resources of a node.
type NodeHardware struct {
	CPUs   int    `json:"cpus"`
	RAMGB  int    `json:"ram_gb"`
	HasGPU bool   `json:"has_gpu"`
	OS     string `json:"os"`
}

// AgentProfile specifies a subagent capability advertised by a node.
type AgentProfile struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
}

// NodeJoinRequest represents a node announcement payload when registering with the mesh coordinator.
type NodeJoinRequest struct {
	NodeID         string         `json:"node_id"`
	Endpoint       string         `json:"endpoint"`
	Hardware       NodeHardware   `json:"hardware"`
	Agents         []AgentProfile `json:"agents"`
	MaxConcurrency int            `json:"max_concurrency"`
}

func (r NodeJoinRequest) MarshalJSON() ([]byte, error) {
	type Alias NodeJoinRequest
	return json.Marshal(&struct {
		Alias
		AgentsAdvertised []AgentProfile `json:"agents_advertised,omitempty"`
	}{
		Alias:            Alias(r),
		AgentsAdvertised: r.Agents,
	})
}

func (r *NodeJoinRequest) UnmarshalJSON(data []byte) error {
	type Alias NodeJoinRequest
	aux := struct {
		*Alias
		AgentsAdvertised []AgentProfile `json:"agents_advertised"`
	}{
		Alias: (*Alias)(r),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(r.Agents) == 0 && len(aux.AgentsAdvertised) > 0 {
		r.Agents = aux.AgentsAdvertised
	}
	return nil
}

// NodeHeartbeatRequest represents a periodic keepalive ping from a worker node.
type NodeHeartbeatRequest struct {
	NodeID      string `json:"node_id"`
	ActiveTasks int    `json:"active_tasks"`
	Timestamp   int64  `json:"timestamp"`
}

// NodeInfo represents catalog metadata and current health state for a federated node.
type NodeInfo struct {
	NodeID         string         `json:"node_id"`
	Endpoint       string         `json:"endpoint"`
	Hardware       NodeHardware   `json:"hardware"`
	Agents         []AgentProfile `json:"agents"`
	MaxConcurrency int            `json:"max_concurrency"`
	ActiveTasks    int            `json:"active_tasks"`
	Status         NodeStatus     `json:"status"`
	LastHeartbeat  int64          `json:"last_heartbeat"`
}

func (info *NodeInfo) UnmarshalJSON(data []byte) error {
	type Alias NodeInfo
	aux := struct {
		*Alias
		AgentsAdvertised []AgentProfile `json:"agents_advertised"`
	}{
		Alias: (*Alias)(info),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(info.Agents) == 0 && len(aux.AgentsAdvertised) > 0 {
		info.Agents = aux.AgentsAdvertised
	}
	return nil
}
