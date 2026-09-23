package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

func TestNodeStatusConstants(t *testing.T) {
	tests := []struct {
		got  protocol.NodeStatus
		want string
	}{
		{protocol.NodeStatusOnline, "online"},
		{protocol.NodeStatusDegraded, "degraded"},
		{protocol.NodeStatusOffline, "offline"},
	}

	for _, tc := range tests {
		if string(tc.got) != tc.want {
			t.Errorf("expected NodeStatus %q, got %q", tc.want, tc.got)
		}
	}
}

func TestNodeHardwareJSONSerialization(t *testing.T) {
	hw := protocol.NodeHardware{
		CPUs:   32,
		RAMGB:  64,
		HasGPU: true,
		OS:     "linux/amd64",
	}

	data, err := json.Marshal(hw)
	if err != nil {
		t.Fatalf("failed to marshal NodeHardware: %v", err)
	}

	var parsed protocol.NodeHardware
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal NodeHardware: %v", err)
	}

	if parsed != hw {
		t.Errorf("NodeHardware mismatch: got %+v, want %+v", parsed, hw)
	}
}

func TestAgentProfileJSONSerialization(t *testing.T) {
	profile := protocol.AgentProfile{
		Name:        "heavy-tester",
		Description: "Massive test runner with Docker",
		Tags:        []string{"docker", "postgres", "long-running"},
	}

	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("failed to marshal AgentProfile: %v", err)
	}

	var parsed protocol.AgentProfile
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal AgentProfile: %v", err)
	}

	if parsed.Name != profile.Name || parsed.Description != profile.Description {
		t.Errorf("AgentProfile mismatch: got %+v, want %+v", parsed, profile)
	}
	if len(parsed.Tags) != len(profile.Tags) {
		t.Fatalf("Tags count mismatch: got %d, want %d", len(parsed.Tags), len(profile.Tags))
	}
}

func TestNodeJoinRequestJSONSerialization(t *testing.T) {
	joinReq := protocol.NodeJoinRequest{
		NodeID:   "vps-la-fabrica-gpu",
		Endpoint: "http://100.64.0.15:8080",
		Hardware: protocol.NodeHardware{
			CPUs:   32,
			RAMGB:  64,
			HasGPU: true,
			OS:     "linux/amd64",
		},
		Agents: []protocol.AgentProfile{
			{
				Name:        "heavy-tester",
				Description: "Massive suite runner",
				Tags:        []string{"docker", "long-running"},
			},
		},
		MaxConcurrency: 4,
	}

	data, err := json.Marshal(joinReq)
	if err != nil {
		t.Fatalf("failed to marshal NodeJoinRequest: %v", err)
	}

	s := string(data)
	if !strings.Contains(s, "agents_advertised") {
		t.Errorf("expected JSON to contain 'agents_advertised', got: %s", s)
	}
	if !strings.Contains(s, "agents") {
		t.Errorf("expected JSON to contain 'agents', got: %s", s)
	}

	var parsed protocol.NodeJoinRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal NodeJoinRequest: %v", err)
	}

	if parsed.NodeID != joinReq.NodeID {
		t.Errorf("expected NodeID %q, got %q", joinReq.NodeID, parsed.NodeID)
	}
	if parsed.MaxConcurrency != joinReq.MaxConcurrency {
		t.Errorf("expected MaxConcurrency %d, got %d", joinReq.MaxConcurrency, parsed.MaxConcurrency)
	}
	if len(parsed.Agents) != 1 || parsed.Agents[0].Name != "heavy-tester" {
		t.Errorf("Agents mismatch: got %+v", parsed.Agents)
	}
}

func TestNodeJoinRequestRFC001Compat(t *testing.T) {
	// RFC 001 sample payload using "agents_advertised"
	rawJSON := `{
		"node_id": "vps-la-fabrica-gpu",
		"endpoint": "http://100.64.0.15:8080",
		"hardware": {
			"cpus": 32,
			"ram_gb": 64,
			"has_gpu": true,
			"os": "linux/amd64"
		},
		"agents_advertised": [
			{
				"name": "heavy-tester",
				"description": "Suites masivas con Docker y Postgres",
				"tags": ["docker", "postgres", "long-running"]
			}
		],
		"max_concurrency": 4
	}`

	var joinReq protocol.NodeJoinRequest
	if err := json.Unmarshal([]byte(rawJSON), &joinReq); err != nil {
		t.Fatalf("failed to unmarshal RFC 001 JSON: %v", err)
	}

	if joinReq.NodeID != "vps-la-fabrica-gpu" {
		t.Errorf("expected NodeID vps-la-fabrica-gpu, got %q", joinReq.NodeID)
	}
	if len(joinReq.Agents) != 1 {
		t.Fatalf("expected 1 agent, got %d", len(joinReq.Agents))
	}
	if joinReq.Agents[0].Name != "heavy-tester" {
		t.Errorf("expected agent name 'heavy-tester', got %q", joinReq.Agents[0].Name)
	}
}

func TestNodeHeartbeatRequestJSON(t *testing.T) {
	hb := protocol.NodeHeartbeatRequest{
		NodeID:      "vps-la-fabrica-gpu",
		ActiveTasks: 2,
		Timestamp:   1727085600,
	}

	data, err := json.Marshal(hb)
	if err != nil {
		t.Fatalf("failed to marshal NodeHeartbeatRequest: %v", err)
	}

	var parsed protocol.NodeHeartbeatRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal NodeHeartbeatRequest: %v", err)
	}

	if parsed != hb {
		t.Errorf("NodeHeartbeatRequest mismatch: got %+v, want %+v", parsed, hb)
	}
}

func TestNodeInfoJSONSerialization(t *testing.T) {
	info := protocol.NodeInfo{
		NodeID:   "vps-la-fabrica-gpu",
		Endpoint: "http://100.64.0.15:8080",
		Hardware: protocol.NodeHardware{
			CPUs:   16,
			RAMGB:  32,
			HasGPU: false,
			OS:     "linux/arm64",
		},
		Agents: []protocol.AgentProfile{
			{Name: "worker", Description: "Standard worker"},
		},
		MaxConcurrency: 2,
		ActiveTasks:    1,
		Status:         protocol.NodeStatusOnline,
		LastHeartbeat:  1727085600,
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("failed to marshal NodeInfo: %v", err)
	}

	var parsed protocol.NodeInfo
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal NodeInfo: %v", err)
	}

	if parsed.NodeID != info.NodeID {
		t.Errorf("expected NodeID %q, got %q", info.NodeID, parsed.NodeID)
	}
	if parsed.Status != protocol.NodeStatusOnline {
		t.Errorf("expected Status %q, got %q", protocol.NodeStatusOnline, parsed.Status)
	}
	if parsed.LastHeartbeat != info.LastHeartbeat {
		t.Errorf("expected LastHeartbeat %d, got %d", info.LastHeartbeat, parsed.LastHeartbeat)
	}
}

func TestNodeInfoRFC001Compat(t *testing.T) {
	rawJSON := `{
		"node_id": "vps-edge-node",
		"endpoint": "http://100.64.0.20:8080",
		"hardware": {
			"cpus": 4,
			"ram_gb": 8,
			"has_gpu": false,
			"os": "linux/arm64"
		},
		"agents_advertised": [
			{
				"name": "light-worker",
				"description": "Fast linter and tests",
				"tags": ["lint", "fast"]
			}
		],
		"max_concurrency": 2,
		"active_tasks": 0,
		"status": "online",
		"last_heartbeat": 1727085600
	}`

	var info protocol.NodeInfo
	if err := json.Unmarshal([]byte(rawJSON), &info); err != nil {
		t.Fatalf("failed to unmarshal NodeInfo with agents_advertised: %v", err)
	}

	if info.NodeID != "vps-edge-node" {
		t.Errorf("expected NodeID vps-edge-node, got %q", info.NodeID)
	}
	if len(info.Agents) != 1 || info.Agents[0].Name != "light-worker" {
		t.Errorf("expected 1 agent light-worker, got %+v", info.Agents)
	}
	if info.Status != protocol.NodeStatusOnline {
		t.Errorf("expected status online, got %q", info.Status)
	}
}
