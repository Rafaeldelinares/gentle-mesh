//go:build testharness

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os/exec"

	"github.com/gentleman-programming/gentle-mesh/pkg/receipt"
)

// registerTestHarnessEndpoints registers /execute and /inject-receipt.
// These endpoints bypass security checks and are ONLY for test scenarios.
func (s *Server) registerTestHarnessEndpoints(mux *http.ServeMux) {
	mux.HandleFunc("POST /execute", s.handleExecute)
	mux.HandleFunc("POST /inject-receipt", s.handleInjectReceipt)
}

// handleExecute handles POST /execute (simulated task execution).
func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req ExecuteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Command == "" {
		writeError(w, http.StatusBadRequest, "command required")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.config.EvalTimeout)
	defer cancel()

	workDir := req.WorkingDir
	if workDir == "" {
		workDir = s.config.WorkspaceDir
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", req.Command)
	cmd.Dir = workDir
	for k, v := range req.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}

	log.Printf("[%s] Execute: %q → exit=%d", s.config.AgentID, req.Command, exitCode)

	writeJSON(w, http.StatusOK, &ExecuteResponse{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	})
}

// handleInjectReceipt handles POST /inject-receipt — injects a receipt directly into
// the chain store, bypassing the normal settlement flow. Intended ONLY for testing
// and security validation (e.g., injecting a receipt signed by the wrong executor
// key to verify that VerifyChain correctly detects the signature mismatch).
func (s *Server) handleInjectReceipt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	defer r.Body.Close()

	var req InjectReceiptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if len(req.ReceiptJSON) == 0 {
		writeError(w, http.StatusBadRequest, "receipt_json required")
		return
	}

	var rec receipt.SettlementReceipt
	if err := json.Unmarshal(req.ReceiptJSON, &rec); err != nil {
		writeError(w, http.StatusBadRequest, "parse receipt: "+err.Error())
		return
	}

	ctx := context.Background()
	// Use UpdateReceipt to replace the existing receipt with the tampered one.
	// This simulates the receipt in the chain being tampered with (e.g., by a
	// compromised executor or MITM attack). UpdateReceipt preserves the chain
	// position (prev_hash from the original receipt remains intact).
	if err := s.chainStore.UpdateReceipt(ctx, &rec); err != nil {
		writeError(w, http.StatusInternalServerError, "update: "+err.Error())
		return
	}

	log.Printf("[%s] InjectReceipt: receipt=%s executor=%s",
		s.config.AgentID, rec.ReceiptID, rec.ExecutorAgentID)

	writeJSON(w, http.StatusOK, &InjectReceiptResponse{
		ReceiptID: rec.ReceiptID,
		Injected:  true,
	})
}
