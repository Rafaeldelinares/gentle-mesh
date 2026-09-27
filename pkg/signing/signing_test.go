package signing

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
)

func TestGenerateSigner(t *testing.T) {
	signer, err := GenerateSigner("agent-b")
	if err != nil {
		t.Fatalf("GenerateSigner() error = %v, want nil", err)
	}

	if signer.AgentID() != "agent-b" {
		t.Errorf("AgentID() = %q, want %q", signer.AgentID(), "agent-b")
	}
	if len(signer.PublicKey()) != ed25519.PublicKeySize {
		t.Errorf("PublicKey() length = %d, want %d", len(signer.PublicKey()), ed25519.PublicKeySize)
	}
}

func TestSignAndVerify(t *testing.T) {
	signer, err := GenerateSigner("agent-a")
	if err != nil {
		t.Fatalf("GenerateSigner() error = %v, want nil", err)
	}

	data := []byte("hello, world")
	sig, err := signer.Sign(data)
	if err != nil {
		t.Fatalf("Sign() error = %v, want nil", err)
	}

	if sig == "" {
		t.Fatal("Sign() returned empty signature")
	}

	// Verify with correct public key.
	err = Verify(signer.PublicKey(), data, sig)
	if err != nil {
		t.Errorf("Verify() error = %v, want nil", err)
	}
}

func TestSign_Deterministic(t *testing.T) {
	signer, err := GenerateSigner("agent-a")
	if err != nil {
		t.Fatalf("GenerateSigner() error = %v", err)
	}

	data := []byte("deterministic test")
	sig1, _ := signer.Sign(data)
	sig2, _ := signer.Sign(data)

	if sig1 != sig2 {
		t.Errorf("Sign() not deterministic: %q != %q", sig1, sig2)
	}
}

func TestSign_DifferentData(t *testing.T) {
	signer, err := GenerateSigner("agent-a")
	if err != nil {
		t.Fatalf("GenerateSigner() error = %v", err)
	}

	sig1, _ := signer.Sign([]byte("data 1"))
	sig2, _ := signer.Sign([]byte("data 2"))

	if sig1 == sig2 {
		t.Error("Sign() produced same signature for different data")
	}
}

func TestVerify_WrongKey(t *testing.T) {
	signer1, _ := GenerateSigner("agent-a")
	signer2, _ := GenerateSigner("agent-b")

	data := []byte("test data")
	sig, _ := signer1.Sign(data)

	// Verify with wrong public key.
	err := Verify(signer2.PublicKey(), data, sig)
	if err == nil {
		t.Error("Verify() with wrong key should fail")
	}
	if !errors.Is(err, ErrVerifyFailed) {
		t.Errorf("Verify() error = %v, want %v", err, ErrVerifyFailed)
	}
}

func TestVerify_TamperedData(t *testing.T) {
	signer, _ := GenerateSigner("agent-a")

	data := []byte("original data")
	sig, _ := signer.Sign(data)

	// Verify with tampered data.
	err := Verify(signer.PublicKey(), []byte("tampered data"), sig)
	if err == nil {
		t.Error("Verify() with tampered data should fail")
	}
}

func TestVerify_InvalidSignature(t *testing.T) {
	signer, _ := GenerateSigner("agent-test")
	pubKey := signer.PublicKey()
	data := []byte("test")

	tests := []struct {
		name string
		sig  string
	}{
		{"empty", ""},
		{"invalid base64", "not-valid-base64!!!"},
		{"wrong length", "YWJjMTIz"}, // 8 chars = 6 bytes, not 64
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Verify(pubKey, data, tt.sig)
			if err == nil {
				t.Errorf("Verify(%q) expected error, got nil", tt.sig)
			}
		})
	}
}

func TestVerify_EmptyData(t *testing.T) {
	signer, _ := GenerateSigner("agent-a")
	sig := "dummy"
	err := Verify(signer.PublicKey(), []byte{}, sig)
	if !errors.Is(err, ErrEmptyData) {
		t.Errorf("Verify(empty data) = %v, want %v", err, ErrEmptyData)
	}
}

func TestSign_EmptyData(t *testing.T) {
	signer, _ := GenerateSigner("agent-a")
	_, err := signer.Sign([]byte{})
	if !errors.Is(err, ErrEmptyData) {
		t.Errorf("Sign(empty data) = %v, want %v", err, ErrEmptyData)
	}
}

// ─────────────────────────────────────────────────────────────────
// High-level Sign / Verify functions
// ─────────────────────────────────────────────────────────────────

func TestSign_HighLevel(t *testing.T) {
	signer, err := GenerateSigner("agent-a")
	if err != nil {
		t.Fatalf("GenerateSigner() error = %v", err)
	}

	data := []byte("high-level test")
	sig, err := Sign(signer, data)
	if err != nil {
		t.Fatalf("Sign() error = %v, want nil", err)
	}

	err = Verify(signer.PublicKey(), data, sig)
	if err != nil {
		t.Errorf("Verify() after Sign() error = %v, want nil", err)
	}
}

func TestSignEnvelope(t *testing.T) {
	signer, err := GenerateSigner("agent-a")
	if err != nil {
		t.Fatalf("GenerateSigner() error = %v", err)
	}

	hash := "abc123def456"
	sig, err := SignEnvelope(signer, hash)
	if err != nil {
		t.Fatalf("SignEnvelope() error = %v, want nil", err)
	}

	err = VerifyEnvelopeHash(signer.PublicKey(), hash, sig)
	if err != nil {
		t.Errorf("VerifyEnvelopeHash() error = %v, want nil", err)
	}
}

// ─────────────────────────────────────────────────────────────────
// PEM round-trip
// ─────────────────────────────────────────────────────────────────

func TestPEM_PrivateKeyRoundTrip(t *testing.T) {
	signer, err := GenerateSigner("agent-c")
	if err != nil {
		t.Fatalf("GenerateSigner() error = %v", err)
	}

	// Export private key.
	pemBytes, err := signer.PrivateKeyPEM()
	if err != nil {
		t.Fatalf("PrivateKeyPEM() error = %v, want nil", err)
	}

	if len(pemBytes) == 0 {
		t.Fatal("PrivateKeyPEM() returned empty PEM")
	}

	// Parse back.
	parsedKey, err := ParsePrivateKeyPEM(pemBytes)
	if err != nil {
		t.Fatalf("ParsePrivateKeyPEM() error = %v, want nil", err)
	}

	// Create new signer from parsed key.
	signer2, err := NewBasicSigner(parsedKey, "agent-c")
	if err != nil {
		t.Fatalf("NewBasicSigner() error = %v, want nil", err)
	}

	// Both signers must produce the same signature.
	data := []byte("pem round-trip test")
	sig1, _ := signer.Sign(data)
	sig2, _ := signer2.Sign(data)

	if sig1 != sig2 {
		t.Error("Parsed signer produces different signature")
	}
}

func TestPEM_PublicKeyRoundTrip(t *testing.T) {
	signer, err := GenerateSigner("agent-d")
	if err != nil {
		t.Fatalf("GenerateSigner() error = %v", err)
	}

	pemBytes := MarshalPublicKeyPEM(signer.PublicKey())

	parsedPub, err := ParsePublicKeyPEM(pemBytes)
	if err != nil {
		t.Fatalf("ParsePublicKeyPEM() error = %v, want nil", err)
	}

	if string(parsedPub) != string(signer.PublicKey()) {
		t.Error("Public key round-trip failed")
	}
}

func TestPEM_ParseInvalid(t *testing.T) {
	_, err := ParsePrivateKeyPEM([]byte("not pem at all"))
	if err == nil {
		t.Error("ParsePrivateKeyPEM(invalid) expected error, got nil")
	}

	_, err = ParsePublicKeyPEM([]byte(""))
	if err == nil {
		t.Error("ParsePublicKeyPEM(empty) expected error, got nil")
	}

	// Valid PEM but wrong key size.
	shortPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: []byte{1, 2, 3},
	})
	_, err = ParsePublicKeyPEM(shortPEM)
	if err == nil {
		t.Error("ParsePublicKeyPEM(short) expected error, got nil")
	}
}

func TestPEM_ParsePKCS8WrongType(t *testing.T) {
	// Valid PKCS8 PEM header but too short content to be valid.
	shortPKCS8 := pem.EncodeToMemory(&pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: []byte{1, 2, 3}, // too short to be valid PKCS8
	})
	_, err := ParsePrivateKeyPEM(shortPKCS8)
	if err == nil {
		t.Error("ParsePrivateKeyPEM(invalid pkcs8) expected error, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────
// BasicSigner correctness
// ─────────────────────────────────────────────────────────────────

func TestNewBasicSigner_InvalidKey(t *testing.T) {
	shortKey := make([]byte, 16) // too short
	_, err := NewBasicSigner(shortKey, "agent-a")
	if err == nil {
		t.Error("NewBasicSigner(short key) expected error, got nil")
	}
}

func TestNewBasicSigner_EmptyAgentID(t *testing.T) {
	_, edPriv, _ := ed25519.GenerateKey(nil)
	_, err := NewBasicSigner(edPriv, "")
	if err == nil {
		t.Error("NewBasicSigner(empty agent_id) expected error, got nil")
	}
}

func TestBasicSigner_PublicKeyHex(t *testing.T) {
	signer, err := GenerateSigner("agent-a")
	if err != nil {
		t.Fatalf("GenerateSigner() error = %v", err)
	}

	hex := signer.PublicKeyHex()

	if len(hex) != 64 {
		t.Errorf("PublicKeyHex() length = %d, want 64", len(hex))
	}

	// Must be consistent.
	hex2 := signer.PublicKeyHex()
	if hex != hex2 {
		t.Error("PublicKeyHex() not deterministic")
	}
}

func TestBasicSigner_PrivateKeyPEM(t *testing.T) {
	signer, err := GenerateSigner("agent-e")
	if err != nil {
		t.Fatalf("GenerateSigner() error = %v", err)
	}

	pemBytes, err := signer.PrivateKeyPEM()
	if err != nil {
		t.Fatalf("PrivateKeyPEM() error = %v", err)
	}

	// Should be valid PEM.
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		t.Fatal("PrivateKeyPEM() produced invalid PEM")
	}
	if block.Type != "PRIVATE KEY" {
		t.Errorf("PEM type = %q, want %q", block.Type, "PRIVATE KEY")
	}
}

func TestBasicSigner_NewSignerFromPEM(t *testing.T) {
	// Generate, export, import, sign again.
	signer1, _ := GenerateSigner("agent-f")
	pemBytes, _ := signer1.PrivateKeyPEM()

	signer2, err := NewBasicSignerFromPEM(pemBytes, "agent-f")
	if err != nil {
		t.Fatalf("NewBasicSignerFromPEM() error = %v", err)
	}

	data := []byte("re-imported key")
	sig1, _ := signer1.Sign(data)
	sig2, _ := signer2.Sign(data)

	if sig1 != sig2 {
		t.Error("Re-imported signer produces different signature")
	}
}

// NewBasicSignerFromPEM is a helper that combines ParsePrivateKeyPEM and NewBasicSigner.
func NewBasicSignerFromPEM(pemBytes []byte, agentID string) (*BasicSigner, error) {
	key, err := ParsePrivateKeyPEM(pemBytes)
	if err != nil {
		return nil, err
	}
	return NewBasicSigner(key, agentID)
}

func TestSign_ChecksSignerNil(t *testing.T) {
	_, err := Sign(nil, []byte("data"))
	if err == nil {
		t.Error("Sign(nil signer) expected error, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────
// x509 PKCS8 integration
// ─────────────────────────────────────────────────────────────────

func TestX509_PKCS8RoundTrip(t *testing.T) {
	_, edPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	// Marshal via x509 directly (what PrivateKeyPEM does internally).
	derBytes, err := x509.MarshalPKCS8PrivateKey(edPriv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: derBytes})

	// Parse via x509 directly.
	block, _ := pem.Decode(pemBytes)
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("ParsePKCS8PrivateKey() error = %v", err)
	}

	parsedPriv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		t.Fatal("Parsed key is not ed25519.PrivateKey")
	}

	// Sign with both keys.
	data := []byte("x509 round-trip")
	sig1 := ed25519.Sign(edPriv, data)
	sig2 := ed25519.Sign(parsedPriv, data)

	if string(sig1) != string(sig2) {
		t.Error("x509 round-trip produced different signatures")
	}
}

// ─────────────────────────────────────────────────────────────────
// Benchmark
// ─────────────────────────────────────────────────────────────────

func BenchmarkSign(b *testing.B) {
	signer, _ := GenerateSigner("agent-bench")
	data := make([]byte, 1024) // 1KB of data

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		signer.Sign(data)
	}
}

func BenchmarkVerify(b *testing.B) {
	signer, _ := GenerateSigner("agent-bench")
	data := make([]byte, 1024)
	sig, _ := signer.Sign(data)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Verify(signer.PublicKey(), data, sig)
	}
}
