// Package envelope implements the CognitiveTaskEnvelope: a typed, signable
// contract between an emitter agent (A) and an executor agent (B).
//
// The envelope carries the full contract specification: territory, preconditions,
// settlement assertions, and execution constraints. Before transmission, A computes
// a JCS canonical hash (RFC 8785) and signs it with its Ed25519 key.
//
// The envelope is immutable after creation. Any modification (including adding
// a signature) produces a new logical envelope.
package envelope
