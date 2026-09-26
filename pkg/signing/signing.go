// Package signing provides Ed25519 cryptographic signing for cognitive envelopes
// and settlement receipts.
//
// The package separates the signing interface from the key storage backend,
// allowing different key management strategies (in-memory, filesystem, HSM, KMS)
// without changing the signing logic.
package signing
