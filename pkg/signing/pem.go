package signing

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// MarshalPrivateKeyPEM encodes an Ed25519 private key in PKCS8 PEM format.
func MarshalPrivateKeyPEM(key ed25519.PrivateKey) ([]byte, error) {
	derBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("x509 marshal pkcs8: %w", err)
	}
	block := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: derBytes,
	}
	return pem.EncodeToMemory(block), nil
}

// ParsePrivateKeyPEM decodes a PKCS8 PEM-encoded Ed25519 private key.
func ParsePrivateKeyPEM(pemBytes []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("pem: no valid block found")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("x509 parse pkcs8: %w", err)
	}
	edKey, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("pem: key is not Ed25519")
	}
	return edKey, nil
}

// MarshalPublicKeyPEM encodes an Ed25519 public key in PEM format.
func MarshalPublicKeyPEM(key ed25519.PublicKey) []byte {
	block := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: key,
	}
	return pem.EncodeToMemory(block)
}

// ParsePublicKeyPEM decodes a PEM-encoded Ed25519 public key.
func ParsePublicKeyPEM(pemBytes []byte) (ed25519.PublicKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("pem: no valid block found")
	}
	if len(block.Bytes) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key: invalid size %d, want %d",
			len(block.Bytes), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(block.Bytes), nil
}
