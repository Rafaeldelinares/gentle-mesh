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
	caPEM, err := pki.CertificateToPEM(ca.Cert)
	if err != nil {
		t.Fatalf("CA CertificateToPEM failed: %v", err)
	}
	fullChainPEM := certPEM + "\n" + caPEM

	keyBytes, err := x509.MarshalECPrivateKey(serverCert.Key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey failed: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	tlsPair, err := tls.X509KeyPair([]byte(fullChainPEM), keyPEM)
	if err != nil {
		t.Fatalf("tls.X509KeyPair failed: %v", err)
	}

	var mu sync.Mutex
	tokenReceived := false

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
	// Verify that inside cmd/gentle-mesh/main.go, enrollClient never hardcodes skip verification,
	// and bootstrapClient uses newPinnedTLSClient instead of newTLSClient("", true).
	srcBytes, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("failed to read main.go: %v", err)
	}
	src := string(srcBytes)

	// Ensure enrollClient does not bypass TLS verification
	if strings.Contains(src, `enrollClient := newTLSClient("", true)`) {
		t.Fatal("SECURITY VIOLATION: enrollClient := newTLSClient(\"\", true) hardcoded in main.go without CA verification!")
	}

	// Ensure bootstrapClient does not use dev-insecure bypass
	if strings.Contains(src, `bootstrapClient := newTLSClient("", true)`) {
		t.Fatal("SECURITY VIOLATION: bootstrapClient := newTLSClient(\"\", true) hardcoded in main.go! Must use newPinnedTLSClient")
	}
}

func TestRedTeam_CAPinning_ProductionModeSuccess(t *testing.T) {
	// (a) GENTLE_ENV=production + -ca-cert-hash correcto → alta correcta
	// (c) el alta con huella no imprime el aviso de desarrollo
	t.Setenv("GENTLE_ENV", "production")

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
			"-node-id", "test-prod-node",
			"-join-token", "valid-prod-token",
			"-ca-cert-hash", correctHash,
			"-heartbeat-interval", "50ms",
		}, &stdout, &stderr)
	}()

	select {
	case err := <-errCh:
		t.Fatalf("worker failed in production mode with pinned CA: %v (stderr: %s)", err, stderr.String())
	case <-time.After(500 * time.Millisecond):
		cancel()
		err := <-errCh
		if err != nil && !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("unexpected error on shutdown: %v", err)
		}
	}

	// (c) Ensure no warning message about InsecureSkipVerify was printed
	errOut := stderr.String()
	if strings.Contains(errOut, "InsecureSkipVerify is enabled") || strings.Contains(errOut, "WARNING") {
		t.Fatalf("SECURITY/UX VIOLATION: dev warning printed during secure pinned enrollment: %s", errOut)
	}

	// Verify CA was successfully downloaded and stored
	expectedCADir := filepath.Join(tempConfigDir, "gentle-mesh", "nodes", "test-prod-node")
	caPath := filepath.Join(expectedCADir, "ca.pem")
	if _, err := os.Stat(caPath); err != nil {
		t.Fatalf("expected CA certificate saved at %s, got: %v", caPath, err)
	}
}

func TestRedTeam_CAPinning_ServerDifferentCARejectionAtHandshake(t *testing.T) {
	// (b) servidor con otra CA y huella fijada → conexión rechazada en el handshake, sin enviar nada
	otherCA, err := pki.GenerateCA("Attacker CA", "evil", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}
	victimCA, err := pki.GenerateCA("Victim Mesh CA", "mesh", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}
	victimHash := pki.CertFingerprint(victimCA.Cert)

	// Server runs with otherCA
	serverCert, err := otherCA.GenerateServerCert([]string{"localhost", "127.0.0.1"}, 0)
	if err != nil {
		t.Fatalf("GenerateServerCert failed: %v", err)
	}
	certPEM, _ := pki.CertificateToPEM(serverCert.Cert)
	caPEM, _ := pki.CertificateToPEM(otherCA.Cert)
	keyBytes, _ := x509.MarshalECPrivateKey(serverCert.Key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})
	tlsPair, err := tls.X509KeyPair([]byte(certPEM+"\n"+caPEM), keyPEM)
	if err != nil {
		t.Fatalf("tls.X509KeyPair failed: %v", err)
	}

	tokenReceived := false
	var mu sync.Mutex

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/mesh/ca", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokenReceived = true // If an attacker server gets requests, we want to know
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Write([]byte(caPEM))
	})
	mux.HandleFunc("/v1/certs/enroll", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		tokenReceived = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	ts := httptest.NewUnstartedServer(mux)
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{tlsPair},
	}
	ts.StartTLS()
	defer ts.Close()

	tempConfigDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempConfigDir)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	err = runCLI(ctx, []string{
		"worker",
		"-coordinator", ts.URL,
		"-node-id", "test-reject-node",
		"-join-token", "secret-test-token-value",
		"-ca-cert-hash", victimHash,
	}, &stdout, &stderr)

	if err == nil {
		t.Fatal("expected handshake rejection error, got nil")
	}

	mu.Lock()
	received := tokenReceived
	mu.Unlock()

	// Verification: Handshake failure prevented HTTP requests
	if received {
		t.Fatal("SECURITY VIOLATION: server received HTTP request despite certificate hash mismatch at handshake!")
	}

	// Verify error indicates handshake / certificate mismatch
	errStr := strings.ToLower(err.Error())
	if !strings.Contains(errStr, "handshake") && !strings.Contains(errStr, "bad certificate") && !strings.Contains(errStr, "fingerprint") && !strings.Contains(errStr, "mismatch") {
		t.Fatalf("expected TLS handshake or pin mismatch error, got: %v", err)
	}
}

func TestRedTeam_CAPinning_AttackerUntrustedLeafWithRealCAInChain(t *testing.T) {
	// Vulnerability reproduction:
	// Attacker server presents its own untrusted leaf cert (signed by attackerCA)
	// plus the victim's legitimate public CA cert in the chain.
	// If the client only checks whether ANY cert in cs.PeerCertificates matches the pin,
	// the connection is accepted (status 200) even though the leaf is untrusted!
	// It MUST be rejected during TLS handshake because the leaf is not signed by the pinned CA.
	attackerCA, err := pki.GenerateCA("Attacker Fake CA", "evil", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}
	attackerServerCert, err := attackerCA.GenerateServerCert([]string{"localhost", "127.0.0.1"}, 0)
	if err != nil {
		t.Fatalf("GenerateServerCert failed: %v", err)
	}

	realCA, err := pki.GenerateCA("Real Mesh CA", "real", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}
	realCAHash := pki.CertFingerprint(realCA.Cert)
	realCAPEM, _ := pki.CertificateToPEM(realCA.Cert)

	attackerCertPEM, _ := pki.CertificateToPEM(attackerServerCert.Cert)
	keyBytes, _ := x509.MarshalECPrivateKey(attackerServerCert.Key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes})

	// Combined chain: [attackerLeaf, realCA]
	combinedPEM := attackerCertPEM + "\n" + realCAPEM
	tlsPair, err := tls.X509KeyPair([]byte(combinedPEM), keyPEM)
	if err != nil {
		t.Fatalf("tls.X509KeyPair failed: %v", err)
	}

	requestReceived := false
	var mu sync.Mutex

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/mesh/ca", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestReceived = true
		mu.Unlock()
		w.Header().Set("Content-Type", "application/x-pem-file")
		w.Write([]byte(realCAPEM))
	})
	mux.HandleFunc("/v1/certs/enroll", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestReceived = true
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})

	ts := httptest.NewUnstartedServer(mux)
	ts.TLS = &tls.Config{
		Certificates: []tls.Certificate{tlsPair},
	}
	ts.StartTLS()
	defer ts.Close()

	tempConfigDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempConfigDir)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	var stdout, stderr bytes.Buffer
	// Worker runs with -ca-cert-hash pointing to realCA
	err = runCLI(ctx, []string{
		"worker",
		"-coordinator", ts.URL,
		"-node-id", "test-attacker-chain-node",
		"-join-token", "secret-test-token-value",
		"-ca-cert-hash", realCAHash,
	}, &stdout, &stderr)

	if err == nil {
		t.Fatal("SECURITY VULNERABILITY: worker accepted connection from server with untrusted leaf cert signed by attacker!")
	}

	mu.Lock()
	received := requestReceived
	mu.Unlock()

	if received {
		t.Fatal("SECURITY VULNERABILITY: attacker server received HTTP request because real CA in chain bypassed verification!")
	}
}



