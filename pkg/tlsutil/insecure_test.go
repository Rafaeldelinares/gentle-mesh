package tlsutil_test

import (
	"bytes"
	"crypto/tls"
	"errors"
	"strings"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/tlsutil"
)

func TestDevInsecureConfig_ExplicitFlagRequired(t *testing.T) {
	t.Setenv("GENTLE_ENV", "development")

	cfg, err := tlsutil.DevInsecureConfig(false)
	if err == nil {
		t.Fatal("expected error when explicitFlag is false, got nil")
	}
	if !errors.Is(err, tlsutil.ErrExplicitFlagRequired) {
		t.Fatalf("expected ErrExplicitFlagRequired, got: %v", err)
	}
	if cfg != nil {
		t.Fatalf("expected nil config, got: %+v", cfg)
	}
}

func TestDevInsecureConfig_ProhibitedInProduction(t *testing.T) {
	prodEnvs := []string{"production", "PRODUCTION", "prod", "PROD", " Production "}
	for _, env := range prodEnvs {
		t.Run(env, func(t *testing.T) {
			t.Setenv("GENTLE_ENV", env)

			cfg, err := tlsutil.DevInsecureConfig(true)
			if err == nil {
				t.Fatalf("expected error in production env %q, got nil", env)
			}
			if !errors.Is(err, tlsutil.ErrInsecureInProduction) {
				t.Fatalf("expected ErrInsecureInProduction, got: %v", err)
			}
			if cfg != nil {
				t.Fatalf("expected nil config in production, got: %+v", cfg)
			}
		})
	}
}

func TestDevInsecureConfig_WarningEmitted(t *testing.T) {
	t.Setenv("GENTLE_ENV", "development")

	var buf bytes.Buffer
	cfg, err := tlsutil.DevInsecureConfigWithOutput(true, &buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if !cfg.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify to be true")
	}

	warning := buf.String()
	if !strings.Contains(warning, "WARNING") || !strings.Contains(warning, "InsecureSkipVerify") {
		t.Fatalf("expected prominent warning message, got: %q", warning)
	}
}

func TestApplyDevInsecure(t *testing.T) {
	t.Run("nil config returns error", func(t *testing.T) {
		t.Setenv("GENTLE_ENV", "development")
		err := tlsutil.ApplyDevInsecure(nil, true)
		if err == nil {
			t.Fatal("expected error on nil config, got nil")
		}
	})

	t.Run("explicit flag required", func(t *testing.T) {
		t.Setenv("GENTLE_ENV", "development")
		cfg := &tls.Config{}
		err := tlsutil.ApplyDevInsecure(cfg, false)
		if !errors.Is(err, tlsutil.ErrExplicitFlagRequired) {
			t.Fatalf("expected ErrExplicitFlagRequired, got: %v", err)
		}
		if cfg.InsecureSkipVerify {
			t.Error("InsecureSkipVerify should remain false")
		}
	})

	t.Run("prohibited in production", func(t *testing.T) {
		t.Setenv("GENTLE_ENV", "production")
		cfg := &tls.Config{}
		err := tlsutil.ApplyDevInsecure(cfg, true)
		if !errors.Is(err, tlsutil.ErrInsecureInProduction) {
			t.Fatalf("expected ErrInsecureInProduction, got: %v", err)
		}
		if cfg.InsecureSkipVerify {
			t.Error("InsecureSkipVerify should remain false in production")
		}
	})

	t.Run("success in development", func(t *testing.T) {
		t.Setenv("GENTLE_ENV", "development")
		cfg := &tls.Config{}
		err := tlsutil.ApplyDevInsecure(cfg, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !cfg.InsecureSkipVerify {
			t.Error("expected InsecureSkipVerify to be true")
		}
	})
}
