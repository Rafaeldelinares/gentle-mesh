//go:build redteam

package signing

import (
	"strings"
	"testing"
)

func TestRedTeam_Base64SignatureMalleability(t *testing.T) {
	signer, err := GenerateSigner("agent-test")
	if err != nil {
		t.Fatalf("GenerateSigner: %v", err)
	}

	data := []byte("critical settlement message")
	sigB64, err := signer.Sign(data)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	// Verify original signature passes
	if err := Verify(signer.PublicKey(), data, sigB64); err != nil {
		t.Fatalf("Verify original signature failed: %v", err)
	}

	// Mutate unused padding bits in the 86th character of Base64 RawURLEncoding
	alphabet := "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"
	lastChar := sigB64[len(sigB64)-1]
	lastIdx := strings.IndexByte(alphabet, lastChar)
	if lastIdx == -1 {
		t.Fatalf("invalid character in signature: %c", lastChar)
	}

	// Flip lowest unused bit (bit 0 of the 4 padding bits)
	mutatedIdx := lastIdx ^ 1
	mutatedSig := sigB64[:len(sigB64)-1] + string(alphabet[mutatedIdx])

	// A secure verification MUST reject non-canonical / malleable base64 signatures.
	// In vulnerable code, Verify() succeeds because non-strict base64 ignores unused bits.
	err = Verify(signer.PublicKey(), data, mutatedSig)
	if err == nil {
		t.Fatalf("SECURITY VULNERABILITY: Verify accepted malleable signature with mutated unused bits: original=%s, mutated=%s",
			sigB64, mutatedSig)
	}
}
