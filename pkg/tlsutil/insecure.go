package tlsutil

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

var (
	// ErrInsecureInProduction is returned when insecure TLS is requested in production.
	ErrInsecureInProduction = errors.New("insecure TLS verification is prohibited in production (GENTLE_ENV=production)")

	// ErrExplicitFlagRequired is returned when dev insecure config is requested without explicit confirmation.
	ErrExplicitFlagRequired = errors.New("insecure TLS verification requires an explicit development flag")

	// ErrEmptyPinHash is returned when a pinned bootstrap config is requested without an expected hash.
	ErrEmptyPinHash = errors.New("expected CA fingerprint cannot be empty")

	// ErrInvalidPinFormat is returned when the fingerprint does not have the sha256: prefix.
	ErrInvalidPinFormat = errors.New("invalid CA fingerprint format: must start with sha256: prefix")

	// ErrPinMismatch is returned when the peer certificate chain does not contain a certificate matching the expected pin.
	ErrPinMismatch = errors.New("peer certificate chain does not contain a certificate matching expected fingerprint")

	// WarnWriter is the default output destination for TLS security warnings (defaults to os.Stderr).
	WarnWriter io.Writer = os.Stderr
)

// IsProduction reports whether the current environment is production,
// based on the GENTLE_ENV environment variable.
func IsProduction() bool {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("GENTLE_ENV")))
	return env == "production" || env == "prod"
}

// DevInsecureConfig returns a *tls.Config with InsecureSkipVerify: true
// intended strictly for local development and integration testing.
//
// Invariants enforced:
// 1. explicitFlag must be true (flag gating).
// 2. GENTLE_ENV must not be "production" or "prod".
// 3. A warning is emitted to WarnWriter (os.Stderr by default) on every invocation.
func DevInsecureConfig(explicitFlag bool) (*tls.Config, error) {
	return DevInsecureConfigWithOutput(explicitFlag, WarnWriter)
}

// DevInsecureConfigWithOutput allows specifying a custom writer for the security warning.
func DevInsecureConfigWithOutput(explicitFlag bool, out io.Writer) (*tls.Config, error) {
	if !explicitFlag {
		return nil, ErrExplicitFlagRequired
	}
	if IsProduction() {
		return nil, ErrInsecureInProduction
	}
	if out != nil {
		fmt.Fprintln(out, "⚠️  WARNING: InsecureSkipVerify is enabled. Server TLS certificates are NOT being verified! DO NOT USE IN PRODUCTION.")
	}
	return &tls.Config{
		// #nosec G402 -- Intentional dev-only bypass gated by explicit flag and prohibited in production.
		InsecureSkipVerify: true,
	}, nil
}

// ApplyDevInsecure enables InsecureSkipVerify on an existing *tls.Config.
func ApplyDevInsecure(cfg *tls.Config, explicitFlag bool) error {
	if cfg == nil {
		return errors.New("tls.Config cannot be nil")
	}
	devCfg, err := DevInsecureConfig(explicitFlag)
	if err != nil {
		return err
	}
	cfg.InsecureSkipVerify = devCfg.InsecureSkipVerify
	return nil
}

// PinnedBootstrapConfig returns a *tls.Config configured to verify that the server's certificate
// is signed by a trust anchor matching expectedHash (format: "sha256:<hex>").
//
// This config does NOT require a development flag, does NOT emit insecure warnings,
// and IS allowed in production because it cryptographically verifies the peer certificate
// chain against the pinned CA fingerprint during the TLS handshake before transmitting any data.
func PinnedBootstrapConfig(expectedHash string) (*tls.Config, error) {
	return PinnedBootstrapConfigWithServerName("", expectedHash)
}

// PinnedBootstrapConfigWithServerName returns a *tls.Config configured with an expected ServerName
// and verifying that the server's presented certificate chain is signed by the trust anchor matching
// expectedHash (format: "sha256:<hex>") and matches serverName.
func PinnedBootstrapConfigWithServerName(serverName, expectedHash string) (*tls.Config, error) {
	expected := strings.TrimSpace(strings.ToLower(expectedHash))
	if expected == "" {
		return nil, ErrEmptyPinHash
	}
	if !strings.HasPrefix(expected, "sha256:") {
		return nil, fmt.Errorf("%w: %s", ErrInvalidPinFormat, expectedHash)
	}

	cleanServerName := serverName
	if h, _, err := net.SplitHostPort(serverName); err == nil {
		cleanServerName = h
	}

	return &tls.Config{
		// #nosec G402 -- Custom verification implemented via VerifyConnection against expected SHA-256 fingerprint and chain signature.
		InsecureSkipVerify: true,
		ServerName:         cleanServerName,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("server presented no certificates")
			}
			var anchor *x509.Certificate
			for _, c := range cs.PeerCertificates {
				if fingerprintMatches(c, expected) {
					anchor = c
					break
				}
			}
			if anchor == nil {
				return fmt.Errorf("%w: expected %s", ErrPinMismatch, expected)
			}
			roots := x509.NewCertPool()
			roots.AddCert(anchor)
			inter := x509.NewCertPool()
			for _, c := range cs.PeerCertificates[1:] {
				if c != anchor {
					inter.AddCert(c)
				}
			}
			_, err := cs.PeerCertificates[0].Verify(x509.VerifyOptions{
				Roots:         roots,
				Intermediates: inter,
				DNSName:       cs.ServerName,
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			})
			return err
		},
	}, nil
}

func fingerprintMatches(cert *x509.Certificate, expected string) bool {
	if cert == nil {
		return false
	}
	sum := sha256.Sum256(cert.Raw)
	actual := fmt.Sprintf("sha256:%x", sum)
	return strings.EqualFold(actual, expected)
}

