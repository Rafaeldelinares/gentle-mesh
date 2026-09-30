package agent

import (
	"strings"
	"testing"
)

func TestNewHTTPClientTLS_PropagatesOptionErrors(t *testing.T) {
	// 1. WithDevInsecureTLS(false) must fail because explicit flag is false.
	_, err := NewHTTPClientTLS("https://localhost:8443", WithDevInsecureTLS(false))
	if err == nil {
		t.Fatal("expected error when WithDevInsecureTLS is false, got nil")
	}
	if !strings.Contains(err.Error(), "explicit development flag") {
		t.Errorf("expected explicit flag error, got: %v", err)
	}

	// 2. WithDevInsecureTLS(true) must fail when GENTLE_ENV=production.
	t.Setenv("GENTLE_ENV", "production")
	_, err = NewHTTPClientTLS("https://localhost:8443", WithDevInsecureTLS(true))
	if err == nil {
		t.Fatal("expected error when WithDevInsecureTLS is true in production, got nil")
	}
	if !strings.Contains(err.Error(), "prohibited in production") {
		t.Errorf("expected production error, got: %v", err)
	}

	// 3. WithCACert with missing file must fail.
	t.Setenv("GENTLE_ENV", "development")
	_, err = NewHTTPClientTLS("https://localhost:8443", WithCACert("/nonexistent/ca.crt"))
	if err == nil {
		t.Fatal("expected error with nonexistent CA cert, got nil")
	}
	if !strings.Contains(err.Error(), "read CA cert file") {
		t.Errorf("expected read CA cert error, got: %v", err)
	}
}
