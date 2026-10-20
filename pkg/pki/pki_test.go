package pki

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"net"
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
	cert, err := ca.GenerateServerCert(hostnames, nil, 0)
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
	if !origPub.Equal(&loadedPub) {
		t.Error("Loaded CA public key doesn't match")
	}
}

func TestSaveAndLoadServerCert(t *testing.T) {
	tmpDir := t.TempDir()

	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	cert, err := ca.GenerateServerCert([]string{"localhost"}, nil, 0)
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

	err := InitMeshTLS(tmpDir, org, orgUnit, hostnames, nil)
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

// Every server certificate keeps the loopback SANs and adds the requested ones.
func TestGenerateServerCertSANs(t *testing.T) {
	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	cert, err := ca.GenerateServerCert([]string{"coord.local"}, []string{"192.168.122.50"}, 0)
	if err != nil {
		t.Fatalf("GenerateServerCert failed: %v", err)
	}

	for _, name := range []string{"coord.local", "localhost", "127.0.0.1"} {
		found := false
		for _, got := range cert.Cert.DNSNames {
			if got == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DNSNames must contain %q, got %v", name, cert.Cert.DNSNames)
		}
	}
	for _, raw := range []string{"192.168.122.50", "127.0.0.1", "::1"} {
		want := net.ParseIP(raw)
		found := false
		for _, got := range cert.Cert.IPAddresses {
			if got.Equal(want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("IPAddresses must contain %s, got %v", raw, cert.Cert.IPAddresses)
		}
	}
}

// A value that is not an IP in the IP SAN list is an error, not a silently useless SAN.
func TestGenerateServerCertRejectsInvalidIP(t *testing.T) {
	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	if _, err := ca.GenerateServerCert(nil, []string{"not-an-ip"}, 0); err == nil {
		t.Error("expected an error for a non-IP value in the IP SAN list")
	}
	if _, err := ca.GenerateServerCert(nil, []string{""}, 0); err == nil {
		t.Error("expected an error for an empty IP SAN")
	}
}

// An existing certificate that does not cover the requested SANs is regenerated, and
// only the server certificate changes: the CA stays byte for byte identical.
func TestEnsureServerCertSANsRegeneratesServerCertOnly(t *testing.T) {
	tmpDir := t.TempDir()
	ca, err := GenerateCA("Gentle Mesh Test", "testing", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}
	if err := ca.SaveCAPemFiles(tmpDir, false); err != nil {
		t.Fatalf("SaveCAPemFiles failed: %v", err)
	}

	old, err := ca.GenerateServerCert(nil, nil, 0)
	if err != nil {
		t.Fatalf("GenerateServerCert failed: %v", err)
	}
	if err := old.SaveServerCertFiles(tmpDir, false); err != nil {
		t.Fatalf("SaveServerCertFiles failed: %v", err)
	}

	caPath := filepath.Join(tmpDir, CAPemFile)
	caBefore, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("reading the CA failed: %v", err)
	}

	fresh, regenerated, err := EnsureServerCertSANs(tmpDir, ca, nil, []string{"192.168.122.50"})
	if err != nil {
		t.Fatalf("EnsureServerCertSANs failed: %v", err)
	}
	if !regenerated {
		t.Error("a certificate without the requested IP must be regenerated")
	}
	want := net.ParseIP("192.168.122.50")
	found := false
	for _, got := range fresh.Cert.IPAddresses {
		if got.Equal(want) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("the regenerated certificate must cover the requested IP, got %v", fresh.Cert.IPAddresses)
	}
	if fresh.Cert.SerialNumber.Cmp(old.Cert.SerialNumber) == 0 {
		t.Error("the server certificate serial should have changed")
	}

	caAfter, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("re-reading the CA failed: %v", err)
	}
	if !bytes.Equal(caBefore, caAfter) {
		t.Error("the CA must not change when only the server certificate is regenerated")
	}

	// A directory without any certificate performs a first generation, not a regeneration.
	empty := t.TempDir()
	first, regeneratedFirst, err := EnsureServerCertSANs(empty, ca, nil, nil)
	if err != nil {
		t.Fatalf("EnsureServerCertSANs on an empty dir failed: %v", err)
	}
	if regeneratedFirst {
		t.Error("a first generation must not be reported as a regeneration")
	}
	if first == nil || first.Cert == nil {
		t.Error("expected a generated certificate")
	}

	// Second round: it already covers the SANs, so it is left untouched.
	kept, regeneratedAgain, err := EnsureServerCertSANs(tmpDir, ca, nil, []string{"192.168.122.50"})
	if err != nil {
		t.Fatalf("EnsureServerCertSANs failed: %v", err)
	}
	if regeneratedAgain {
		t.Error("a certificate that already covers the SANs must not be regenerated")
	}
	if kept.Cert.SerialNumber.Cmp(fresh.Cert.SerialNumber) != 0 {
		t.Error("the certificate should not have changed")
	}
}
