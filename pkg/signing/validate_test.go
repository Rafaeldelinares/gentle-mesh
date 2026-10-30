package signing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
)

func TestValidateEd25519PublicKey(t *testing.T) {
	// 1. Valid generated Ed25519 public keys must pass
	for i := 0; i < 10; i++ {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("failed to generate key: %v", err)
		}
		pubHex := hex.EncodeToString(pub)
		b, err := ValidateEd25519PublicKey(pubHex)
		if err != nil {
			t.Errorf("expected valid key to pass, got: %v", err)
		}
		if len(b) != 32 {
			t.Errorf("expected 32 bytes, got %d", len(b))
		}
	}

	// 2. Length errors
	badLengths := []string{
		"",
		"abcd",
		hex.EncodeToString(make([]byte, 16)),
		hex.EncodeToString(make([]byte, 31)),
		hex.EncodeToString(make([]byte, 33)),
		hex.EncodeToString(make([]byte, 64)),
	}
	for _, bad := range badLengths {
		if _, err := ValidateEd25519PublicKey(bad); err != ErrInvalidKeyLength {
			t.Errorf("expected ErrInvalidKeyLength for len %d, got %v", len(bad), err)
		}
	}

	// 3. Hex decoding errors
	notHex := "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"
	if _, err := ValidateEd25519PublicKey(notHex); err == nil {
		t.Errorf("expected error for non-hex input, got nil")
	}

	// 4. All zeroes rejected
	zeroHex := hex.EncodeToString(make([]byte, 32))
	if _, err := ValidateEd25519PublicKey(zeroHex); err != ErrZeroKey {
		t.Errorf("expected ErrZeroKey, got %v", err)
	}

	// 5. Non-canonical point y >= p
	// p in LE = [0xed, 0xff, ..., 0xff, 0x7f]
	nonCanonical := make([]byte, 32)
	nonCanonical[0] = 0xed
	for i := 1; i <= 30; i++ {
		nonCanonical[i] = 0xff
	}
	nonCanonical[31] = 0x7f // y = p exactly (invalid)
	if _, err := ValidateEd25519PublicKey(hex.EncodeToString(nonCanonical)); err != ErrNonCanonicalPoint {
		t.Errorf("expected ErrNonCanonicalPoint for y == p, got %v", err)
	}

	nonCanonical[0] = 0xee // y = p + 1 (invalid)
	if _, err := ValidateEd25519PublicKey(hex.EncodeToString(nonCanonical)); err != ErrNonCanonicalPoint {
		t.Errorf("expected ErrNonCanonicalPoint for y == p + 1, got %v", err)
	}

	// Canonical boundary: y = p - 1 = [0xec, 0xff, ..., 0xff, 0x7f] (valid)
	canonicalBoundary := make([]byte, 32)
	copy(canonicalBoundary, nonCanonical)
	canonicalBoundary[0] = 0xec
	if _, err := ValidateEd25519PublicKey(hex.EncodeToString(canonicalBoundary)); err != nil {
		t.Errorf("expected canonical boundary y = p - 1 to pass, got: %v", err)
	}
}
