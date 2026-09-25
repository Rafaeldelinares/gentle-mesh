package pki

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateCA(t *testing.T) {
	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	if ca == nil {
		t.Fatal("GenerateCA returned nil")
	}

	if ca.Cert == nil {
		t.Error("CA certificate is nil")
	}

	if ca.Key == nil {
		t.Error("CA key is nil")
	}

	if !ca.Cert.IsCA {
		t.Error("Generated certificate is not a CA")
	}

	if len(ca.Cert.Subject.Organization) == 0 {
		t.Error("CA organization is empty")
	}
}

func TestGenerateServerCert(t *testing.T) {
	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	hostnames := []string{"localhost", "coordinator.local"}
	cert, err := ca.GenerateServerCert(hostnames, 0)
	if err != nil {
		t.Fatalf("GenerateServerCert failed: %v", err)
	}

	if cert == nil {
		t.Fatal("GenerateServerCert returned nil")
	}

	if cert.Cert == nil {
		t.Error("Server certificate is nil")
	}

	if cert.Key == nil {
		t.Error("Server key is nil")
	}

	if cert.Cert.IsCA {
		t.Error("Server certificate should not be a CA")
	}

	// Check that server cert is signed by CA
	caPool := x509.NewCertPool()
	caPool.AddCert(ca.Cert)

	opts := x509.VerifyOptions{
		DNSName: "localhost",
		Roots:   caPool,
	}

	if _, err := cert.Cert.Verify(opts); err != nil {
		t.Errorf("Server certificate not verified by CA: %v", err)
	}
}

func TestSaveAndLoadCA(t *testing.T) {
	tmpDir := t.TempDir()

	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	// Save CA
	err = ca.SaveCAPemFiles(tmpDir, false)
	if err != nil {
		t.Fatalf("SaveCAPemFiles failed: %v", err)
	}

	// Load CA
	loadedCA, err := LoadCAPemFiles(tmpDir)
	if err != nil {
		t.Fatalf("LoadCAPemFiles failed: %v", err)
	}

	// Verify loaded CA matches original
	if loadedCA.Cert.SerialNumber.Cmp(ca.Cert.SerialNumber) != 0 {
		t.Error("Loaded CA serial number doesn't match")
	}

	// Check public key matches
	origPub := ca.Key.PublicKey
	loadedPub := loadedCA.Key.PublicKey
	if origPub.X.Cmp(loadedPub.X) != 0 || origPub.Y.Cmp(loadedPub.Y) != 0 {
		t.Error("Loaded CA public key doesn't match")
	}
}

func TestSaveAndLoadServerCert(t *testing.T) {
	tmpDir := t.TempDir()

	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	cert, err := ca.GenerateServerCert([]string{"localhost"}, 0)
	if err != nil {
		t.Fatalf("GenerateServerCert failed: %v", err)
	}

	// Save server cert
	err = cert.SaveServerCertFiles(tmpDir, false)
	if err != nil {
		t.Fatalf("SaveServerCertFiles failed: %v", err)
	}

	// Load server cert
	loadedCert, err := LoadServerCertFiles(tmpDir)
	if err != nil {
		t.Fatalf("LoadServerCertFiles failed: %v", err)
	}

	// Verify loaded cert matches original
	if loadedCert.Cert.SerialNumber.Cmp(cert.Cert.SerialNumber) != 0 {
		t.Error("Loaded server cert serial number doesn't match")
	}
}

func TestSaveCAFileExists(t *testing.T) {
	tmpDir := t.TempDir()

	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	// Save once
	err = ca.SaveCAPemFiles(tmpDir, false)
	if err != nil {
		t.Fatalf("First SaveCAPemFiles failed: %v", err)
	}

	// Try to save again without force
	err = ca.SaveCAPemFiles(tmpDir, false)
	if err == nil {
		t.Error("Expected ErrFileExists on second save without force")
	}

	// Force save should work
	err = ca.SaveCAPemFiles(tmpDir, true)
	if err != nil {
		t.Errorf("SaveCAPemFiles with force=true failed: %v", err)
	}
}

func TestInitMeshTLS(t *testing.T) {
	tmpDir := t.TempDir()
	org := "Gentle Mesh Test"
	orgUnit := "integration"
	hostnames := []string{"localhost", "mesh.local"}

	err := InitMeshTLS(tmpDir, org, orgUnit, hostnames)
	if err != nil {
		t.Fatalf("InitMeshTLS failed: %v", err)
	}

	// Check files exist
	expectedFiles := []string{CAPemFile, CAPrivateFile, CertPemFile, CertKeyFile}
	for _, f := range expectedFiles {
		path := filepath.Join(tmpDir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("Expected file %s not found", f)
		}
	}

	// Load and verify
	ca, err := LoadCAPemFiles(tmpDir)
	if err != nil {
		t.Fatalf("LoadCAPemFiles failed: %v", err)
	}
	if ca == nil || !ca.Cert.IsCA {
		t.Error("Loaded CA is invalid")
	}

	cert, err := LoadServerCertFiles(tmpDir)
	if err != nil {
		t.Fatalf("LoadServerCertFiles failed: %v", err)
	}
	if cert == nil || cert.Cert.IsCA {
		t.Error("Loaded server cert is invalid")
	}
}

func TestEnsureMeshTLS(t *testing.T) {
	tmpDir := t.TempDir()
	org := "Gentle Mesh Test"
	orgUnit := "ensure"
	hostnames := []string{"localhost"}

	// First call should create files
	ca1, cert1, err := EnsureMeshTLS(tmpDir, org, orgUnit, hostnames, false)
	if err != nil {
		t.Fatalf("First EnsureMeshTLS failed: %v", err)
	}

	// Second call should load existing files (same serial)
	ca2, cert2, err := EnsureMeshTLS(tmpDir, org, orgUnit, hostnames, false)
	if err != nil {
		t.Fatalf("Second EnsureMeshTLS failed: %v", err)
	}

	if ca1.Cert.SerialNumber.Cmp(ca2.Cert.SerialNumber) != 0 {
		t.Error("CA serial changed on reload")
	}
	if cert1.Cert.SerialNumber.Cmp(cert2.Cert.SerialNumber) != 0 {
		t.Error("Cert serial changed on reload")
	}

	// Force should regenerate
	ca3, cert3, err := EnsureMeshTLS(tmpDir, org, orgUnit, hostnames, true)
	if err != nil {
		t.Fatalf("Force EnsureMeshTLS failed: %v", err)
	}

	if ca1.Cert.SerialNumber.Cmp(ca3.Cert.SerialNumber) == 0 {
		t.Error("CA serial should have changed after force regenerate")
	}
	if cert1.Cert.SerialNumber.Cmp(cert3.Cert.SerialNumber) == 0 {
		t.Error("Cert serial should have changed after force regenerate")
	}
}

func TestLoadCACertOnly(t *testing.T) {
	tmpDir := t.TempDir()

	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	caPath := filepath.Join(tmpDir, CAPemFile)
	err = WriteCertificatePemFile(caPath, ca.Cert)
	if err != nil {
		t.Fatalf("WriteCertificatePemFile failed: %v", err)
	}

	loadedCert, err := LoadCACertOnly(caPath)
	if err != nil {
		t.Fatalf("LoadCACertOnly failed: %v", err)
	}

	if !loadedCert.IsCA {
		t.Error("Loaded cert is not a CA")
	}

	if loadedCert.SerialNumber.Cmp(ca.Cert.SerialNumber) != 0 {
		t.Error("Loaded cert serial doesn't match")
	}
}

func TestLoadCACertOnlyFileNotFound(t *testing.T) {
	_, err := LoadCACertOnly("/nonexistent/path/ca.pem")
	if err == nil {
		t.Error("Expected error for nonexistent file")
	}
}

func TestParseInvalidPEM(t *testing.T) {
	invalidCert := []byte("NOT A VALID PEM BLOCK")
	_, err := parseCertFromPEM(invalidCert)
	if err == nil {
		t.Error("Expected error for invalid PEM")
	}
}

func parseCertFromPEM(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, ErrInvalidCert
	}
	return x509.ParseCertificate(block.Bytes)
}

// NodeCert tests

func TestGenerateNodeCert(t *testing.T) {
	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	nodeID := "worker-alpha"
	nodeCert, info, err := ca.GenerateNodeCert(nodeID, 0)
	if err != nil {
		t.Fatalf("GenerateNodeCert failed: %v", err)
	}

	if nodeCert == nil {
		t.Fatal("GenerateNodeCert returned nil")
	}

	if nodeCert.NodeID != nodeID {
		t.Errorf("expected NodeID %s, got %s", nodeID, nodeCert.NodeID)
	}

	if nodeCert.Cert == nil {
		t.Error("Node certificate is nil")
	}

	if nodeCert.Key == nil {
		t.Error("Node key is nil")
	}

	if nodeCert.Cert.IsCA {
		t.Error("Node certificate should not be a CA")
	}

	if info.NodeID != nodeID {
		t.Errorf("expected NodeCertInfo.NodeID %s, got %s", nodeID, info.NodeID)
	}

	if info.Revoked {
		t.Error("Newly generated cert should not be revoked")
	}

	if info.Serial == "" {
		t.Error("Serial should not be empty")
	}

	// Verify cert is signed by CA (verify against the CA cert directly)
	if nodeCert.Cert.CheckSignatureFrom(ca.Cert) != nil {
		t.Error("Node certificate should be signed by CA")
	}
}

func TestNodeCertValidity(t *testing.T) {
	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	nodeCert, _, err := ca.GenerateNodeCert("test-node", 0)
	if err != nil {
		t.Fatalf("GenerateNodeCert failed: %v", err)
	}

	if !nodeCert.IsNodeCertValid() {
		t.Error("Newly generated cert should be valid")
	}
}

func TestSaveAndLoadNodeCert(t *testing.T) {
	tmpDir := t.TempDir()

	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	nodeID := "worker-beta"
	nodeCert, _, err := ca.GenerateNodeCert(nodeID, 0)
	if err != nil {
		t.Fatalf("GenerateNodeCert failed: %v", err)
	}

	// Save node cert
	err = nodeCert.SaveNodeCertFiles(tmpDir, false)
	if err != nil {
		t.Fatalf("SaveNodeCertFiles failed: %v", err)
	}

	// Load node cert
	loadedCert, err := LoadNodeCertFiles(tmpDir, nodeID)
	if err != nil {
		t.Fatalf("LoadNodeCertFiles failed: %v", err)
	}

	if loadedCert.NodeID != nodeID {
		t.Errorf("expected NodeID %s, got %s", nodeID, loadedCert.NodeID)
	}

	if loadedCert.Cert.SerialNumber.Cmp(nodeCert.Cert.SerialNumber) != 0 {
		t.Error("Loaded cert serial doesn't match original")
	}
}

func TestSaveNodeCertFileExists(t *testing.T) {
	tmpDir := t.TempDir()

	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	nodeCert, _, err := ca.GenerateNodeCert("test-node", 0)
	if err != nil {
		t.Fatalf("GenerateNodeCert failed: %v", err)
	}

	// Save once
	err = nodeCert.SaveNodeCertFiles(tmpDir, false)
	if err != nil {
		t.Fatalf("First SaveNodeCertFiles failed: %v", err)
	}

	// Try to save again without force
	err = nodeCert.SaveNodeCertFiles(tmpDir, false)
	if err == nil {
		t.Error("Expected error on second save without force")
	}

	// Force save should work
	err = nodeCert.SaveNodeCertFiles(tmpDir, true)
	if err != nil {
		t.Errorf("SaveNodeCertFiles with force=true failed: %v", err)
	}
}

func TestLoadNodeCertNotFound(t *testing.T) {
	_, err := LoadNodeCertFiles("/nonexistent", "node-id")
	if err == nil {
		t.Error("Expected error for nonexistent node cert")
	}
}

func TestNodeCertClientAuth(t *testing.T) {
	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	nodeCert, _, err := ca.GenerateNodeCert("test-client", 0)
	if err != nil {
		t.Fatalf("GenerateNodeCert failed: %v", err)
	}

	// Check that the cert has client auth ext key usage
	hasClientAuth := false
	for _, usage := range nodeCert.Cert.ExtKeyUsage {
		if usage == x509.ExtKeyUsageClientAuth {
			hasClientAuth = true
			break
		}
	}

	if !hasClientAuth {
		t.Error("Node cert should have client auth ext key usage")
	}
}
