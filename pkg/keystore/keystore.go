// Package keystore provides persistent key storage for agents.
//
// Each agent has:
//   - A private key (keystore/<agent_id>/private.pem) — for signing.
//   - A public key entry in the registry — for verifying others.
//
// Design principles (v1):
//   - Private keys are stored as PKCS8 PEM files with filesystem permissions (0600).
//   - Public keys are stored as PEM files and registered in keystore.json.
//   - No encryption at rest (filesystem security is the operator's responsibility).
//   - Keys are loaded lazily and cached in memory during operation.
package keystore
