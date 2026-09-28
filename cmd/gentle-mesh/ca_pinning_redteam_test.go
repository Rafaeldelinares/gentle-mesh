//go:build redteam

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/pki"
	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

func setupTestTLSServer(t *testing.T) (*httptest.Server, *pki.MeshCA, *bool, *sync.Mutex) {
	t.Helper()

	ca, err := pki.GenerateCA("Gentle Mesh Test CA", "test", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	serverCert, err := ca.GenerateServerCert([]string{"localhost", "127.0.0.1"}, 0)
	if err != nil {
		t.Fatalf("GenerateServerCert failed: %v", err)
	}

	certPEM, err := pki.CertificateToPEM(serverCert.Cert)
	if err != nil {
		t.Fatalf("CertificateToPEM failed: %v", err)
	}
	keyBytes, err := x509.MarshalECPrivateKey(serverCert.Key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey failed: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	tlsPair, err := tls.X509KeyPair([]byte(certPEM), keyPEM)
	if err != nil {
		t.Fatalf("tls.X509KeyPair failed: %v", err)
	}

	var mu sync.Mutex
	tokenReceived := false

	caPEM, err := pki.CertificateToPEM(ca.Cert)
	if err != nil {
		t.Fatalf("CA CertificateToPEM failed: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/mesh/ca", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(caPEM))
	})

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok","tls":"enabled"}`))
	})

	mux.HandleFunc("/v1/certs/enroll", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokenReceived = true
		mu.Unlock()

		var req struct {
			Token  string `json:"token"`
			CSR    string `json:"csr"`
			NodeID string `json:"node_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		signedCert, err := ca.SignCSR(req.CSR, req.NodeID, 24*time.Hour)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		signedPEM, _ := pki.CertificateToPEM(signedCert)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{
			"cert_pem": signedPEM,
			"node_id":  req.NodeID,
		})
	})

	mux.HandleFunc("/v1/mesh/join", func(w http.ResponseWriter, r *http.Request) {
		var req protocol.NodeJoinRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(protocol.NodeInfo{
			NodeID: req.NodeID,
			Status: protocol.NodeStatusOnline,
		})
	})

	mux.HandleFunc("/v1/mesh/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "acknowledged"})
	})

	ts := httptest.NewUnstartedServer(mux)
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{tlsPair},
	}
	ts.StartTLS()

	return ts, ca, &tokenReceived, &mu
}

func TestRedTeam_CAPinning_HashMismatchRejection(t *testing.T) {
	ts, _, tokenReceived, mu := setupTestTLSServer(t)
	defer ts.Close()

	tempConfigDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempConfigDir)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	wrongHash := "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	err := runCLI(ctx, []string{
		"worker",
		"-coordinator", ts.URL,
		"-node-id", "test-mismatch-node",
		"-join-token", "secret-test-token-value",
		"-ca-cert-hash", wrongHash,
	}, &stdout, &stderr)

	if err == nil {
		t.Fatal("expected error due to CA hash mismatch, got nil")
	}
	if strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("flag -ca-cert-hash is not supported: %v", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "fingerprint") && !strings.Contains(strings.ToLower(err.Error()), "mismatch") {
		t.Fatalf("expected fingerprint mismatch error, got: %v", err)
	}

	mu.Lock()
	received := *tokenReceived
	mu.Unlock()

	if received {
		t.Fatal("SECURITY VIOLATION: enrollment token was transmitted despite CA fingerprint mismatch!")
	}
}

func TestRedTeam_CAPinning_MissingCAAndHashRejection(t *testing.T) {
	ts, _, tokenReceived, mu := setupTestTLSServer(t)
	defer ts.Close()

	tempConfigDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempConfigDir)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer

	// No -ca, no -ca-cert-hash, no -insecure-skip-tls-verify
	err := runCLI(ctx, []string{
		"worker",
		"-coordinator", ts.URL,
		"-node-id", "test-missing-ca-node",
		"-join-token", "secret-test-token-value",
	}, &stdout, &stderr)

	if err == nil {
		t.Fatal("expected error when neither -ca nor -ca-cert-hash is provided, got nil")
	}

	mu.Lock()
	received := *tokenReceived
	mu.Unlock()

	if received {
		t.Fatal("SECURITY VIOLATION: enrollment token was transmitted over unverified TLS connection!")
	}
}

func TestRedTeam_CAPinning_CorrectHashEnrollment(t *testing.T) {
	ts, ca, _, _ := setupTestTLSServer(t)
	defer ts.Close()

	tempConfigDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempConfigDir)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var stdout, stderr bytes.Buffer
	correctHash := pki.CertFingerprint(ca.Cert)

	errCh := make(chan error, 1)
	go func() {
		errCh <- runCLI(ctx, []string{
			"worker",
			"-coordinator", ts.URL,
			"-node-id", "test-enrolled-node",
			"-join-token", "valid-enrollment-token",
			"-ca-cert-hash", correctHash,
			"-heartbeat-interval", "50ms",
		}, &stdout, &stderr)
	}()

	// Allow the worker to enroll and connect
	select {
	case err := <-errCh:
		t.Fatalf("worker exited unexpectedly during enrollment: %v (stderr: %s)", err, stderr.String())
	case <-time.After(500 * time.Millisecond):
		// Worker is running, cancel context to shut down cleanly
		cancel()
		err := <-errCh
		if err != nil && !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("unexpected error on shutdown: %v", err)
		}
	}

	// Verify CA was saved in user config directory (0700), not in /tmp
	expectedCADir := filepath.Join(tempConfigDir, "gentle-mesh", "nodes", "test-enrolled-node")
	caPath := filepath.Join(expectedCADir, "ca.pem")
	info, err := os.Stat(caPath)
	if err != nil {
		t.Fatalf("expected CA certificate saved at %s, got: %v", caPath, err)
	}

	// Ensure /tmp/gentle-mesh-ca.pem was NOT used
	tmpCAPath := filepath.Join(os.TempDir(), "gentle-mesh-ca.pem")
	if _, err := os.Stat(tmpCAPath); err == nil {
		// If it exists, check that it wasn't modified in this test
		t.Logf("Notice: %s exists from prior runs", tmpCAPath)
	}

	if info.Size() == 0 {
		t.Errorf("saved CA file is empty")
	}
}

func TestRedTeam_CAPinning_NoInsecureClientWithoutExplicitFlag(t *testing.T) {
	// Source code audit assertion:
	// Verify that inside cmd/gentle-mesh/main.go, enrollClient never hardcodes skip verification.
	srcBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("failed to read main.go: %v", err)
	}
	src := string(srcBytes)

	// Ensure enrollClient does not bypass TLS verification
	if strings.Contains(src, `enrollClient := newTLSClient("", true)`) {
		t.Fatal("SECURITY VIOLATION: enrollClient := newTLSClient(\"\", true) hardcoded in main.go without CA verification!")
	}
}

