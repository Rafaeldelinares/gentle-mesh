// Package pki provides lightweight PKI utilities for the Gentle Mesh security layer.
// It implements a simple centralized CA model where the coordinator acts as the root CA.
package pki

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	// DefaultRSAKeySize is the default RSA key size in bits.
	DefaultRSAKeySize = 2048

	// DefaultValidDuration is the default validity period for generated certificates.
	DefaultValidDuration = 365 * 24 * time.Hour // 1 year

	// CACertValidDuration is the validity period for CA certificates (10 years).
	CACertValidDuration = 10 * 365 * 24 * time.Hour
)

// File names for generated artifacts.
const (
	CAPemFile     = "gentle-mesh-ca.pem"
	CAPrivateFile = "gentle-mesh-ca.key"
	CertPemFile   = "cert.pem"
	CertKeyFile   = "cert.key"
)

// Errors.
var (
	ErrFileExists      = errors.New("file already exists")
	ErrFileNotFound    = errors.New("file not found")
	ErrInvalidKey      = errors.New("invalid key type")
	ErrInvalidCert     = errors.New("invalid certificate")
	ErrNotCACert       = errors.New("certificate is not a CA")
	ErrGenerationFailed = errors.New("certificate generation failed")
)

// MeshCA holds a CA certificate and its private key for signing other certificates.
type MeshCA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// ServerCert holds a server certificate and its private key.
type ServerCert struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
}

// GenerateCA creates a new root CA certificate with the given organizational details.
// The CA is self-signed and can sign other certificates.
func GenerateCA(org, orgUnit string, validFor time.Duration) (*MeshCA, error) {
	if validFor == 0 {
		validFor = CACertValidDuration
	}

	ca := &MeshCA{}

	// Generate CA private key (ECDSA P-256 for performance)
	var err error
	ca.Key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to generate CA key: %v", ErrGenerationFailed, err)
	}

	// Build CA certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to generate serial: %v", ErrGenerationFailed, err)
	}

	ca.Cert = &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization:       []string{org},
			OrganizationalUnit: []string{orgUnit},
			CommonName:         "Gentle Mesh CA",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(validFor),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
	}

	// Self-sign the CA certificate
	caDER, err := x509.CreateCertificate(rand.Reader, ca.Cert, ca.Cert, &ca.Key.PublicKey, ca.Key)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to self-sign CA: %v", ErrGenerationFailed, err)
	}

	ca.Cert, err = x509.ParseCertificate(caDER)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to parse CA certificate: %v", ErrGenerationFailed, err)
	}

	return ca, nil
}

// GenerateServerCert creates a server certificate signed by the provided CA.
// The certificate is valid for the specified hostnames/IPs.
func (ca *MeshCA) GenerateServerCert(hostnames []string, validFor time.Duration) (*ServerCert, error) {
	if validFor == 0 {
		validFor = DefaultValidDuration
	}

	// Generate server private key
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to generate server key: %v", ErrGenerationFailed, err)
	}

	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("%w: failed to generate serial: %v", ErrGenerationFailed, err)
	}

	// Build server certificate template
	cert := &x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			CommonName: "Gentle Mesh Coordinator",
		},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(validFor),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    hostnames,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	// Sign with CA
	certDER, err := x509.CreateCertificate(rand.Reader, cert, ca.Cert, &key.PublicKey, ca.Key)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to sign server cert: %v", ErrGenerationFailed, err)
	}

	cert, err = x509.ParseCertificate(certDER)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to parse server cert: %v", ErrGenerationFailed, err)
	}

	return &ServerCert{Cert: cert, Key: key}, nil
}

// SaveCAPemFiles saves the CA certificate and key to separate PEM files.
// If force is false and files exist, returns ErrFileExists.
func (ca *MeshCA) SaveCAPemFiles(dir string, force bool) error {
	// Save certificate
	certPath := filepath.Join(dir, CAPemFile)
	if !force {
		if _, err := os.Stat(certPath); err == nil {
			return fmt.Errorf("%w: %s", ErrFileExists, certPath)
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := WriteCertificatePemFile(certPath, ca.Cert); err != nil {
		return err
	}

	// Save CA private key (more restrictive permissions)
	keyPath := filepath.Join(dir, CAPrivateFile)
	if err := WritePrivateKeyPemFile(keyPath, ca.Key); err != nil {
		return err
	}

	// Fix permissions for CA key (more restrictive)
	if err := os.Chmod(keyPath, 0600); err != nil {
		return err
	}

	return nil
}

// SaveServerCertFiles saves the server certificate and key to PEM files.
// If force is false and files exist, returns ErrFileExists.
func (cert *ServerCert) SaveServerCertFiles(dir string, force bool) error {
	certPath := filepath.Join(dir, CertPemFile)
	keyPath := filepath.Join(dir, CertKeyFile)

	if !force {
		if _, err := os.Stat(certPath); err == nil {
			return fmt.Errorf("%w: %s", ErrFileExists, certPath)
		}
		if _, err := os.Stat(keyPath); err == nil {
			return fmt.Errorf("%w: %s", ErrFileExists, keyPath)
		}
	}

	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	if err := WriteCertificatePemFile(certPath, cert.Cert); err != nil {
		return err
	}
	if err := WritePrivateKeyPemFile(keyPath, cert.Key); err != nil {
		return err
	}

	// Certificate is not sensitive, key is
	if err := os.Chmod(keyPath, 0600); err != nil {
		return err
	}

	return nil
}

// WritePrivateKeyPemFile writes an ECDSA private key to a PEM file.
func WritePrivateKeyPemFile(path string, key *ecdsa.PrivateKey) error {
	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	keyFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to open key file: %w", err)
	}
	defer keyFile.Close()

	err = pem.Encode(keyFile, &pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyBytes,
	})
	if err != nil {
		return fmt.Errorf("failed to encode PEM: %w", err)
	}

	return nil
}

// WriteCertificatePemFile writes an X.509 certificate to a PEM file.
func WriteCertificatePemFile(path string, cert *x509.Certificate) error {
	certFile, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open cert file: %w", err)
	}
	defer certFile.Close()

	err = pem.Encode(certFile, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: cert.Raw,
	})
	if err != nil {
		return fmt.Errorf("failed to encode PEM: %w", err)
	}

	return nil
}

// LoadCAPemFiles loads a CA certificate and key from PEM files.
func LoadCAPemFiles(dir string) (*MeshCA, error) {
	certPath := filepath.Join(dir, CAPemFile)
	keyPath := filepath.Join(dir, CAPrivateFile)

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrFileNotFound, certPath)
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrFileNotFound, keyPath)
	}

	return ParseCAPem(certPEM, keyPEM)
}

// ParseCAPem parses CA certificate and key from PEM bytes.
func ParseCAPem(certPEM, keyPEM []byte) (*MeshCA, error) {
	// Parse certificate
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block found", ErrInvalidCert)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCert, err)
	}

	if !cert.IsCA {
		return nil, ErrNotCACert
	}

	// Parse private key
	block, _ = pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block found", ErrInvalidKey)
	}

	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}

	return &MeshCA{Cert: cert, Key: key}, nil
}

// LoadServerCertFiles loads a server certificate and key from PEM files.
func LoadServerCertFiles(dir string) (*ServerCert, error) {
	certPath := filepath.Join(dir, CertPemFile)
	keyPath := filepath.Join(dir, CertKeyFile)

	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrFileNotFound, certPath)
	}

	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrFileNotFound, keyPath)
	}

	return ParseServerCertPem(certPEM, keyPEM)
}

// ParseServerCertPem parses server certificate and key from PEM bytes.
func ParseServerCertPem(certPEM, keyPEM []byte) (*ServerCert, error) {
	// Parse certificate
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block found", ErrInvalidCert)
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCert, err)
	}

	// Parse private key
	block, _ = pem.Decode(keyPEM)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block found", ErrInvalidKey)
	}

	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidKey, err)
	}

	return &ServerCert{Cert: cert, Key: key}, nil
}

// LoadCACertOnly loads only the CA certificate (public key) from a PEM file.
// This is what nodes need to trust the coordinator.
func LoadCACertOnly(path string) (*x509.Certificate, error) {
	certPEM, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrFileNotFound, path)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, fmt.Errorf("%w: no PEM block found", ErrInvalidCert)
	}

	return x509.ParseCertificate(block.Bytes)
}

// InitMeshTLS creates a new CA and server certificate in the specified directory.
// It generates files: gentle-mesh-ca.pem, gentle-mesh-ca.key, cert.pem, cert.key
func InitMeshTLS(dir, org, orgUnit string, hostnames []string) error {
	// Generate CA
	ca, err := GenerateCA(org, orgUnit, 0)
	if err != nil {
		return fmt.Errorf("failed to generate CA: %w", err)
	}

	// Save CA
	if err := ca.SaveCAPemFiles(dir, false); err != nil {
		return fmt.Errorf("failed to save CA: %w", err)
	}

	// Generate server certificate
	serverCert, err := ca.GenerateServerCert(hostnames, 0)
	if err != nil {
		return fmt.Errorf("failed to generate server cert: %w", err)
	}

	// Save server certificate
	if err := serverCert.SaveServerCertFiles(dir, false); err != nil {
		return fmt.Errorf("failed to save server cert: %w", err)
	}

	return nil
}

// EnsureMeshTLS initializes TLS files if they don't exist, or loads them if they do.
func EnsureMeshTLS(dir, org, orgUnit string, hostnames []string, force bool) (*MeshCA, *ServerCert, error) {
	// Try to load existing files
	ca, err := LoadCAPemFiles(dir)
	if err == nil && !force {
		cert, err2 := LoadServerCertFiles(dir)
		if err2 == nil {
			return ca, cert, nil
		}
	}

	// Generate new files
	if force {
		// Remove existing files
		os.Remove(filepath.Join(dir, CAPemFile))
		os.Remove(filepath.Join(dir, CAPrivateFile))
		os.Remove(filepath.Join(dir, CertPemFile))
		os.Remove(filepath.Join(dir, CertKeyFile))
	}

	if err := InitMeshTLS(dir, org, orgUnit, hostnames); err != nil {
		return nil, nil, err
	}

	// Load what we just created
	ca, err = LoadCAPemFiles(dir)
	if err != nil {
		return nil, nil, err
	}

	cert, err := LoadServerCertFiles(dir)
	if err != nil {
		return nil, nil, err
	}

	return ca, cert, nil
}
