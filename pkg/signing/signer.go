package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

// Errors.
var (
	ErrNilData      = errors.New("sign: data is nil")
	ErrEmptyData    = errors.New("sign: data is empty")
	ErrInvalidSig   = errors.New("verify: invalid signature format")
	ErrVerifyFailed = errors.New("verify: signature verification failed")
)

// Signer is the interface for cryptographic signing operations.
// Implementations handle key storage and retrieval; the signing logic is shared.
type Signer interface {
	// Sign signs the data and returns the base64url-encoded signature.
	Sign(data []byte) (signature string, err error)
	// PublicKey returns the raw public key bytes.
	PublicKey() []byte
	// AgentID returns the identifier for this signer.
	AgentID() string
}

// ─────────────────────────────────────────────────────────────────
// Implementation: BasicSigner (in-memory key)
// ─────────────────────────────────────────────────────────────────

// BasicSigner holds an Ed25519 keypair in memory.
// Suitable for development, testing, and single-host deployments.
// For production, replace with a KeyStore implementation (see keystore package).
type BasicSigner struct {
	privateKey ed25519.PrivateKey
	publicKey  ed25519.PublicKey
	agentID    string
}

// NewBasicSigner creates a BasicSigner from an existing keypair.
func NewBasicSigner(privateKey ed25519.PrivateKey, agentID string) (*BasicSigner, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key: invalid size %d, want %d",
			len(privateKey), ed25519.PrivateKeySize)
	}
	if agentID == "" {
		return nil, errors.New("agent_id: empty")
	}
	return &BasicSigner{
		privateKey: privateKey,
		publicKey:  privateKey.Public().(ed25519.PublicKey),
		agentID:    agentID,
	}, nil
}

// GenerateSigner creates a new random Ed25519 keypair and signer.
func GenerateSigner(agentID string) (*BasicSigner, error) {
	if agentID == "" {
		return nil, errors.New("agent_id: empty")
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ed25519 key generation failed: %w", err)
	}
	return NewBasicSigner(priv, agentID)
}

// Sign implements Signer.
func (s *BasicSigner) Sign(data []byte) (string, error) {
	if len(data) == 0 {
		return "", ErrEmptyData
	}

	sig := ed25519.Sign(s.privateKey, data)
	return base64.RawURLEncoding.EncodeToString(sig), nil
}

// PublicKey implements Signer.
func (s *BasicSigner) PublicKey() []byte {
	return s.publicKey
}

// AgentID implements Signer.
func (s *BasicSigner) AgentID() string {
	return s.agentID
}

// PublicKeyHex returns the public key as a hex string (useful for keystore).
func (s *BasicSigner) PublicKeyHex() string {
	return fmt.Sprintf("%x", s.publicKey)
}

// PrivateKeyPEM returns the private key in PKCS8 PEM format.
// WARNING: This exposes the private key. Use only for testing or export.
func (s *BasicSigner) PrivateKeyPEM() ([]byte, error) {
	return MarshalPrivateKeyPEM(s.privateKey)
}

// ─────────────────────────────────────────────────────────────────
// Core signing and verification
// ─────────────────────────────────────────────────────────────────

// Sign delegates to the signer's Sign method.
// For signing pre-hashed data (e.g., envelope hash), use SignHash.
func Sign(signer Signer, data []byte) (string, error) {
	if signer == nil {
		return "", errors.New("signer is nil")
	}
	return signer.Sign(data)
}

// SignHash signs a pre-computed SHA-256 hash with the given signer.
// This is used for signing JCS envelope hashes.
func SignHash(signer Signer, hash []byte) (string, error) {
	if signer == nil {
		return "", errors.New("signer is nil")
	}
	if len(hash) != 32 {
		return "", errors.New("hash: must be 32 bytes (SHA-256)")
	}
	return signer.Sign(hash)
}

// Verify checks that the base64url-encoded signature is valid for the data
// and was produced by the given public key (Ed25519 raw verification).
func Verify(publicKey []byte, data []byte, signatureB64 string) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("public key: invalid size %d", len(publicKey))
	}
	if len(data) == 0 {
		return ErrEmptyData
	}
	if signatureB64 == "" {
		return ErrInvalidSig
	}

	sig, err := base64.RawURLEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("%w: base64 decode error: %v", ErrInvalidSig, err)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: wrong size %d, want %d",
			ErrInvalidSig, len(sig), ed25519.SignatureSize)
	}

	if !ed25519.Verify(publicKey, data, sig) {
		return ErrVerifyFailed
	}
	return nil
}

// VerifyHash verifies a signature over a pre-computed SHA-256 hash.
// Used for verifying JCS envelope hash signatures.
func VerifyHash(publicKey []byte, hash []byte, signatureB64 string) error {
	if len(hash) != 32 {
		return errors.New("hash: must be 32 bytes (SHA-256)")
	}
	return Verify(publicKey, hash, signatureB64)
}

// SignEnvelope signs a JCS canonical hash string with the given signer.
func SignEnvelope(signer Signer, hashHex string) (signature string, err error) {
	// Convert hex hash to bytes for signing.
	// For the envelope, the hash is already computed externally (JCS SHA-256).
	// We pass it as raw bytes to the signer.
	hashBytes, err := hexToBytes(hashHex)
	if err != nil {
		return "", fmt.Errorf("sign envelope: %w", err)
	}
	return signer.Sign(hashBytes)
}

// VerifyEnvelopeHash verifies a signature over a JCS canonical hash hex string.
func VerifyEnvelopeHash(publicKey []byte, hashHex string, signatureB64 string) error {
	hashBytes, err := hexToBytes(hashHex)
	if err != nil {
		return fmt.Errorf("verify envelope hash: %w", err)
	}
	return Verify(publicKey, hashBytes, signatureB64)
}

// ─────────────────────────────────────────────────────────────────
// Key export (see pem.go for PEM utilities)
// ─────────────────────────────────────────────────────────────────

// hexToBytes converts a hex string to bytes.
func hexToBytes(hexStr string) ([]byte, error) {
	return hex.DecodeString(hexStr)
}
