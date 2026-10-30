package signing

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
)

var (
	// ErrInvalidKeyLength indicates the public key string does not represent exactly 32 bytes.
	ErrInvalidKeyLength = errors.New("invalid public key length: must be exactly 32 bytes (64 hex characters)")

	// ErrZeroKey indicates the public key is all zeroes, which is not an admissible agent identity.
	ErrZeroKey = errors.New("invalid public key: all-zero key is rejected")

	// ErrNonCanonicalPoint indicates the y-coordinate is >= 2^255 - 19 (non-canonical encoding per RFC 8032 §5.1.3).
	ErrNonCanonicalPoint = errors.New("invalid public key: non-canonical curve point (y >= 2^255 - 19)")
)

// ValidateEd25519PublicKey strictly verifies that a public key hex string:
// 1. Is valid hex and exactly 64 characters (32 bytes).
// 2. Is not the all-zero key.
// 3. Encodes a canonical field element y < p where p = 2^255 - 19 (RFC 8032 §5.1.3).
func ValidateEd25519PublicKey(pubHex string) ([]byte, error) {
	if len(pubHex) != 64 {
		return nil, ErrInvalidKeyLength
	}

	pubBytes, err := hex.DecodeString(pubHex)
	if err != nil {
		return nil, fmt.Errorf("invalid hex encoding: %w", err)
	}

	// Reject all-zero public key
	var allZeros [32]byte
	if bytes.Equal(pubBytes, allZeros[:]) {
		return nil, ErrZeroKey
	}

	// Canonical check per RFC 8032 §5.1.3:
	// p = 2^255 - 19.
	// In little-endian, p is:
	// byte 0: 0xed
	// bytes 1..30: 0xff
	// byte 31 (bits 0-6): 0x7f (bit 7 is sign of x)
	//
	// If y >= p, the encoding is non-canonical.
	yLast := pubBytes[31] & 0x7f
	if yLast == 0x7f {
		// Check bytes 30 down to 1
		allFF := true
		for i := 30; i >= 1; i-- {
			if pubBytes[i] != 0xff {
				allFF = false
				break
			}
		}
		if allFF && pubBytes[0] >= 0xed {
			return nil, ErrNonCanonicalPoint
		}
	}

	return pubBytes, nil
}
