package pki

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
)

// CertFingerprint returns the SHA-256 fingerprint of an X.509 certificate in "sha256:<hex>" format.
func CertFingerprint(cert *x509.Certificate) string {
	if cert == nil || len(cert.Raw) == 0 {
		return ""
	}
	sum := sha256.Sum256(cert.Raw)
	return fmt.Sprintf("sha256:%x", sum)
}

// CertFingerprintFromPEM parses a PEM certificate and returns its SHA-256 fingerprint in "sha256:<hex>" format.
func CertFingerprintFromPEM(pemBytes []byte) (string, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return "", ErrInvalidCert
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidCert, err)
	}
	return CertFingerprint(cert), nil
}

// VerifyCACertHash verifies that the certificate or PEM bytes match the expected "sha256:<hex>" fingerprint.
func VerifyCACertHash(caPEM []byte, expectedHash string) error {
	expected := strings.TrimSpace(strings.ToLower(expectedHash))
	if expected == "" {
		return errors.New("expected CA fingerprint cannot be empty")
	}
	if !strings.HasPrefix(expected, "sha256:") {
		return fmt.Errorf("invalid CA fingerprint format %q: must start with sha256 prefix", expectedHash)
	}
	actual, err := CertFingerprintFromPEM(caPEM)
	if err != nil {
		return fmt.Errorf("failed to parse CA certificate for fingerprint verification: %w", err)
	}
	if !strings.EqualFold(actual, expected) {
		return fmt.Errorf("CA certificate fingerprint mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}
