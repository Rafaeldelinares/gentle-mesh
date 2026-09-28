// Package jcs implements RFC 8785: JSON Canonicalization Scheme.
//
// Canonicalization produces a deterministic byte sequence from any JSON value,
// regardless of how it was originally serialized. This is essential for
// cryptographic operations like hashing and signing.
//
// Key rules (RFC 8785 & RFC 7493 I-JSON):
//   - Object members are sorted lexicographically by key (UTF-16 code units).
//   - Duplicate object keys are strictly rejected at all nesting levels.
//   - Numbers are represented in ECMAScript canonical form (IEEE 754 64-bit float).
//     IMPORTANT: In accordance with RFC 8785 and IEEE 754, integers with absolute
//     value greater than 2^53 (9,007,199,254,740,992) lose precision. Any protocol
//     structures intending to transfer 64-bit integers exceeding 2^53 (e.g. Unix
//     timestamps in nanoseconds or 64-bit counters) must encode them as strings.
//   - Strings are validated for UTF-8 conformity and canonical escape sequences.
//     Lone surrogates (U+D800 to U+DFFF) and raw invalid UTF-8 bytes are rejected.
//   - Only syntactically required whitespace is preserved.
package jcs
