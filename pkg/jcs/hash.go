package jcs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

func sha256Hash(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

func hexEncode(data []byte) string {
	return hex.EncodeToString(data)
}

// HashHexString is a convenience for computing JCS hash and returning hex.
func HashHexString(v interface{}) (string, error) {
	std, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return HashHex(std)
}
