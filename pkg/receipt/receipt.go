// Package receipt implements the SettlementReceipt: a cryptographically signed
// record of a completed contract execution and its settlement verdict.
//
// The receipt is produced by the executor (B) after evaluating all settlement
// assertions. It contains the evidence of each assertion's result, the
// remediation chain if any, and is signed by B. The emitter (A) then reviews
// the evidence and signs either ACCEPTED or DISPUTED.
//
// Receipts are chained: each receipt contains the SHA-256 of B's signature
// from the previous receipt in the pair's chain, creating an immutable audit
// trail.
//
// Receipt states:
//   EMITTED → ACCEPTED | DISPUTED → RESOLVED | STALE
package receipt
