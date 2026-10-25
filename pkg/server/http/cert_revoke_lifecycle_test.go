package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/pki"
	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/registry"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/task"
)

func setupRevocationServer(t *testing.T, bearerToken string) (*meshhttp.Server, string, *pki.MeshCA, *store.SQLiteTokenStore) {
	t.Helper()
	ca, tlsDir, tokenStore, certStore, _ := setupMTLSTestEnvironment(t)
	tasksDir := t.TempDir()
	tm, err := task.NewTaskManager(tasksDir, 1*time.Hour)
	if err != nil {
		t.Fatalf("NewTaskManager failed: %v", err)
	}
	cfg := meshhttp.ServerConfig{
		TasksDir:         tasksDir,
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          1 * time.Hour,
		TLSEnabled:       true,
		TLSCertFile:      filepath.Join(tlsDir, pki.CertPemFile),
		TLSKeyFile:       filepath.Join(tlsDir, pki.CertKeyFile),
		MeshCA:           ca,
		RequireMTLS:      true,
		TokenStore:       tokenStore,
		CertStore:        certStore,
		Registry:         registry.NewRegistry(5 * time.Second),
		TaskManager:      tm,
		BearerToken:      bearerToken,
	}
	srv, addr := startTestServer(t, cfg)
	return srv, "https://" + addr, ca, tokenStore
}

func TestServer_RevokeCert_EvictsFromRegistry(t *testing.T) {
	srv, srvURL, ca, tokenStore := setupRevocationServer(t, "")
	nodeID := "node-evict-check"
	_ = enrollTestNode(t, srvURL, ca, tokenStore, nodeID)

	_, err := srv.Registry().RegisterNode(protocol.NodeJoinRequest{NodeID: nodeID, Endpoint: "http://127.0.0.1:9099"})
	if err != nil {
		t.Fatalf("RegisterNode failed: %v", err)
	}

	if _, ok := srv.Registry().GetNode(nodeID); !ok {
		t.Fatalf("expected node %s in registry", nodeID)
	}

	if err := srv.RevokeCert(context.Background(), nodeID); err != nil {
		t.Fatalf("RevokeCert failed: %v", err)
	}

	if _, ok := srv.Registry().GetNode(nodeID); ok {
		t.Errorf("expected node %s to be evicted from registry", nodeID)
	}
}

func TestServer_RevokeCert_CancelsActiveTasksWithNodeRevoked(t *testing.T) {
	srv, srvURL, ca, tokenStore := setupRevocationServer(t, "")
	nodeID := "node-task-cancel-check"
	_ = enrollTestNode(t, srvURL, ca, tokenStore, nodeID)

	_, _ = srv.Registry().RegisterNode(protocol.NodeJoinRequest{NodeID: nodeID, Endpoint: "http://127.0.0.1:9099"})

	mTask, err := srv.TaskManager().CreateTask(protocol.TaskRequest{Agent: "worker", Task: "active workload"})
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	mTask.AttributeNode(nodeID)
	if err := mTask.TransitionTo(protocol.TaskStatusRunning); err != nil {
		t.Fatalf("TransitionTo failed: %v", err)
	}

	if err := srv.RevokeCert(context.Background(), nodeID); err != nil {
		t.Fatalf("RevokeCert failed: %v", err)
	}

	tCheck, ok := srv.TaskManager().GetTask(mTask.TaskID)
	if !ok || tCheck == nil {
		t.Fatalf("GetTask failed")
	}
	snap := tCheck.Snapshot()
	if snap.Status != protocol.TaskStatusFailed || snap.Error == nil || snap.Error.Code != "NODE_REVOKED" {
		t.Errorf("expected status=failed with code NODE_REVOKED, got status=%s, error=%+v", snap.Status, snap.Error)
	}
	if !strings.Contains(snap.Error.Message, "revoked") {
		t.Errorf("expected error message to mention 'revoked', got: %q", snap.Error.Message)
	}
}

func TestServer_HTTP_CertsRevoke_Endpoint(t *testing.T) {
	adminToken := "admin-revoker-secret"
	srv, srvURL, ca, tokenStore := setupRevocationServer(t, adminToken)
	nodeID := "node-endpoint-test"
	_ = enrollTestNode(t, srvURL, ca, tokenStore, nodeID)
	_, _ = srv.Registry().RegisterNode(protocol.NodeJoinRequest{NodeID: nodeID, Endpoint: "http://127.0.0.1:9099"})

	mTask, _ := srv.TaskManager().CreateTask(protocol.TaskRequest{Agent: "worker", Task: "endpoint cancel"})
	mTask.AttributeNode(nodeID)
	_ = mTask.TransitionTo(protocol.TaskStatusRunning)

	client := createClient(t, ca, nil)

	// 1. Unauthenticated request -> 401
	revokePayload, _ := json.Marshal(map[string]string{"node_id": nodeID})
	resp, err := client.Post(srvURL+"/v1/certs/revoke", "application/json", bytes.NewReader(revokePayload))
	if err != nil {
		t.Fatalf("POST /v1/certs/revoke failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for unauthenticated request, got %d", resp.StatusCode)
	}

	// 2. Non-existent node -> 404
	reqNonExistent, _ := http.NewRequest(http.MethodPost, srvURL+"/v1/certs/revoke", bytes.NewReader([]byte(`{"node_id":"no-such-node"}`)))
	reqNonExistent.Header.Set("Authorization", "Bearer "+adminToken)
	reqNonExistent.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(reqNonExistent)
	if err != nil {
		t.Fatalf("POST non-existent failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404 for non-existent node, got %d", resp.StatusCode)
	}

	// 3. Authorized revocation of valid node -> 200
	reqValid, _ := http.NewRequest(http.MethodPost, srvURL+"/v1/certs/revoke", bytes.NewReader(revokePayload))
	reqValid.Header.Set("Authorization", "Bearer "+adminToken)
	reqValid.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(reqValid)
	if err != nil {
		t.Fatalf("POST valid revocation failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Status         string `json:"status"`
		NodeID         string `json:"node_id"`
		TasksCancelled int    `json:"tasks_cancelled"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result.Status != "revoked" || result.NodeID != nodeID || result.TasksCancelled != 1 {
		t.Errorf("unexpected response: %+v", result)
	}

	// Verify node is evicted from registry
	if _, ok := srv.Registry().GetNode(nodeID); ok {
		t.Errorf("expected node evicted from registry")
	}

	// Verify task is failed with NODE_REVOKED
	cancelledTask, ok := srv.TaskManager().GetTask(mTask.TaskID)
	if !ok || cancelledTask == nil {
		t.Fatalf("GetTask failed")
	}
	cSnap := cancelledTask.Snapshot()
	if cSnap.Status != protocol.TaskStatusFailed || cSnap.Error == nil || cSnap.Error.Code != "NODE_REVOKED" {
		t.Errorf("expected task failed with NODE_REVOKED, got: status=%s, error=%+v", cSnap.Status, cSnap.Error)
	}
}

func TestServer_HTTP_CertsRevoke_NoBearerConfigured_Returns503(t *testing.T) {
	srv, srvURL, ca, _ := setupRevocationServer(t, "") // no bearer token configured
	_ = srv
	client := createClient(t, ca, nil)

	revokePayload, _ := json.Marshal(map[string]string{"node_id": "test-node"})
	resp, err := client.Post(srvURL+"/v1/certs/revoke", "application/json", bytes.NewReader(revokePayload))
	if err != nil {
		t.Fatalf("POST /v1/certs/revoke failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 503 Service Unavailable when no bearer token configured, got %d: %s", resp.StatusCode, string(body))
	}
}
