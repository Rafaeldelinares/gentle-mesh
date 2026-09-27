// Package jcs implements RFC 8785: JSON Canonicalization Scheme.
//
// Canonicalization produces a deterministic byte sequence from any JSON value,
// regardless of how it was originally serialized. This is essential for
// cryptographic operations like hashing and signing.
//
// Key rules (RFC 8785):
//   - Object members are sorted lexicographically by key (UTF-16 code units)
//   - Numbers are in ECMAScript canonical form
//   - Strings use canonical escape sequences
//   - Only syntactically required whitespace is preserved
package jcs
