package tlsutil

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

var (
	// ErrInsecureInProduction is returned when insecure TLS is requested in production.
	ErrInsecureInProduction = errors.New("insecure TLS verification is prohibited in production (GENTLE_ENV=production)")

	// ErrExplicitFlagRequired is returned when dev insecure config is requested without explicit confirmation.
	ErrExplicitFlagRequired = errors.New("insecure TLS verification requires an explicit development flag")

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
