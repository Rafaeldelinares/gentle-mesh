package pki

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestCertFingerprint(t *testing.T) {
	ca, err := GenerateCA("Test Mesh", "test-org", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	fp := CertFingerprint(ca.Cert)
	if !strings.HasPrefix(fp, "sha256:") {
		t.Fatalf("expected fingerprint to start with sha256:, got: %s", fp)
	}

	expectedHex := fmt.Sprintf("%x", sha256.Sum256(ca.Cert.Raw))
	if fp != "sha256:"+expectedHex {
		t.Errorf("expected sha256:%s, got: %s", expectedHex, fp)
	}

	if nilFP := CertFingerprint(nil); nilFP != "" {
		t.Errorf("expected empty string for nil cert, got: %s", nilFP)
	}
}

func TestCertFingerprintFromPEM(t *testing.T) {
	ca, err := GenerateCA("Test Mesh", "test-org", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	pemStr, err := CertificateToPEM(ca.Cert)
	if err != nil {
		t.Fatalf("CertificateToPEM failed: %v", err)
	}

	fp, err := CertFingerprintFromPEM([]byte(pemStr))
	if err != nil {
		t.Fatalf("CertFingerprintFromPEM failed: %v", err)
	}

	if fp != CertFingerprint(ca.Cert) {
		t.Errorf("fingerprint mismatch between cert and PEM: %s vs %s", fp, CertFingerprint(ca.Cert))
	}

	if _, err := CertFingerprintFromPEM([]byte("invalid pem data")); err == nil {
		t.Errorf("expected error for invalid PEM data, got nil")
	}
}

func TestVerifyCACertHash(t *testing.T) {
	ca, err := GenerateCA("Test Mesh", "test-org", 0)
	if err != nil {
		t.Fatalf("GenerateCA failed: %v", err)
	}

	pemStr, err := CertificateToPEM(ca.Cert)
	if err != nil {
		t.Fatalf("CertificateToPEM failed: %v", err)
	}
	pemBytes := []byte(pemStr)

	correctFP := CertFingerprint(ca.Cert)

	t.Run("correct hash passes", func(t *testing.T) {
		if err := VerifyCACertHash(pemBytes, correctFP); err != nil {
			t.Errorf("expected pass, got: %v", err)
		}
		// Upper-case prefix / hex tolerance
		upperFP := "SHA256:" + strings.ToUpper(strings.TrimPrefix(correctFP, "sha256:"))
		if err := VerifyCACertHash(pemBytes, upperFP); err != nil {
			t.Errorf("expected pass with uppercase, got: %v", err)
		}
	})

	t.Run("wrong hash fails", func(t *testing.T) {
		wrongFP := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		if err := VerifyCACertHash(pemBytes, wrongFP); err == nil {
			t.Errorf("expected mismatch error, got nil")
		}
	})

	t.Run("malformed hash fails", func(t *testing.T) {
		if err := VerifyCACertHash(pemBytes, "invalid-format"); err == nil {
			t.Errorf("expected error for format without sha256:, got nil")
		}
		if err := VerifyCACertHash(pemBytes, ""); err == nil {
			t.Errorf("expected error for empty hash, got nil")
		}
	})
}
