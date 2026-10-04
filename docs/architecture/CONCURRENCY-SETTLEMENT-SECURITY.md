# Concurrency, Settlement Determinism, and Security — RFC-002 Technical Reference

> **RFC-002 Reference** — This document complements `SHELL-MESH-COHABITATION.md` with
> deep technical details on the concurrency model, cryptographic canonicity, and
> the security invariants of the settlement protocol.
>
> **Base:** RFC-002 Settlement Protocol (`docs/protocol/SETTLEMENT-PROTOCOL-FORMAL-SPEC.md`)
> **Tests:** 9 distributed Docker integration tests pass (WU13–WU17)

---

## Table of Contents

1. [Agent Cohabitation Topology](#1-agent-cohabitation-topology)
2. [Pre-flight Principle: Verify Before Compute](#2-pre-flight-principle-verify-before-compute)
3. [Concurrency Architecture: SQLite WAL + Mutex + SetMaxOpenConns(1)](#3-concurrency-architecture-sqlite-wal--mutex--setmaxopenconns1)
4. [Identifier Collisions: crypto/rand vs UnixNano](#4-identifier-collisions-cryptorand-vs-unixnano)
5. [JCS RFC 8785 Canonicity and Cross-Signature Determinism](#5-jcs-rfc-8785-canonicity-and-cross-signature-determinism)
6. [ComputeReceiptHash: The Field Clearing Invariant](#6-computereceipthash-the-field-clearing-invariant)
7. [VerifyChain Security: Blocking Identity Spoofing](#7-verifychain-security-blocking-identity-spoofing)
8. [Summary: Security Invariants](#8-summary-security-invariants)

---

## 1. Agent Cohabitation Topology

### 1.1 The Two-Role Model

RFC-002 defines exactly two roles per task execution:

```
┌─────────────────────────────────────────────────────────────────┐
│                        EMITTER (A)                               │
│  Orchestrator agent: pi harness, opencode, claude code,         │
│  gentle-shell in emitter mode                                    │
│                                                                  │
│  Responsibilities:                                                │
│  • Build CognitiveTaskEnvelope (intent + assertions)            │
│  • Sign envelope with Ed25519 private key                        │
│  • Review SettlementReceipt evidence                            │
│  • Sign ACCEPTED or DISPUTED with Ed25519                       │
│  • Call VerifyChain before accepting                             │
│  • Call AcceptReceipt / DisputeReceipt locally                   │
│                                                                  │
│  Tools: envelope pkg, jcs pkg, receipt signer                   │
└────────────────────────┬────────────────────────────────────────┘
                         │  CognitiveTaskEnvelope (Ed25519 signed)
                         │  HTTPS + mTLS (or localhost)
                         ▼
┌─────────────────────────────────────────────────────────────────┐
│                       EXECUTOR (B)                               │
│  Isolated agent: gentle-shell + gentle-mesh sidecar             │
│                                                                  │
│  Responsibilities:                                               │
│  • Run pre-flight checks (git clean, tools, ports)              │
│  • Receive and validate envelope signature                        │
│  • Execute task in isolated workspace                           │
│  • Evaluate assertions deterministically via settlement engine   │
│  • Sign SettlementReceipt with Ed25519 private key              │
│  • Persist to SQLite WAL (ChainStore)                          │
│  • Serve GET /receipts/:id, GET /chain, POST /accept/dispute   │
│                                                                  │
│  Tools: settlement pkg, receipt pkg, keystore pkg               │
└─────────────────────────────────────────────────────────────────┘
```

### 1.2 Constellation of Local Agents

The protocol is designed to serve a constellation of orchestrator agents on the same host:

| Agent | Role | Communication |
|-------|------|----------------|
| `pi` (Gentle AI harness) | Emitter | localhost HTTPS to gentle-mesh |
| `opencode` | Emitter | localhost HTTPS to gentle-mesh |
| `claude code` | Emitter | localhost HTTPS to gentle-mesh |
| `gentle-shell` | Emitter or Executor | localhost HTTP or subprocess |
| `gentle-mesh` (sidecar) | Executor | localhost:8443 |

**Key property:** Multiple emitters can share one executor sidecar. Each emitter has its own Ed25519 keypair in its own keystore. The executor identifies the emitter by `EmitterAgentID` in the envelope and the pair `(emitter, executor)` for chain isolation.

### 1.3 Separation of Concerns: Keystore vs. ChainDB

```
/data/
├── keystore/
│   ├── agent-a.private.pem    (0600, Ed25519 private key)
│   ├── agent-a.public.pem     (0644, Ed25519 public key)
│   ├── agent-b.private.pem
│   └── agent-b.public.pem
└── chain.db                  (SQLite WAL, receipt chain)
```

- **Keystore** (`pkg/keystore/`): Per-agent key storage. Private keys never leave the agent. Public keys are exchanged at handshake time.
- **ChainDB** (`pkg/receipt/chain.go`): Shared SQLite WAL database. Contains signed receipts for all agent pairs. Stored separately from the git workspace to avoid polluting `git status`.

---

## 2. Pre-flight Principle: Verify Before Compute

### 2.1 The Green Checkbox Problem

The fundamental failure mode of autonomous agents is the **Green Checkbox Problem**: an LLM reports success (exit code 0, "all tests pass") after an improvised workaround that bypassed the actual requirement. This is not a code defect — it is a trust defect.

### 2.2 The Verify > Suppose Invariant

RFC-002 enforces a strict ordering:

```
┌──────────────────────────────────────────────────────────────────┐
│  PRE-FLIGHT CHECK          │  EXECUTION + SETTLEMENT            │
│  (fast, synchronous)      │  (deterministic, signed)           │
│                            │                                     │
│  Shell.Check()            │  Shell.Execute()                   │
│    ├─ git_clean_worktree  │    ├─ runCommand()                 │
│    ├─ tool_available      │    └─ settlement_engine.Evaluate() │
│    ├─ port_available      │         └─ verdict + receipt       │
│    └─ no_regression       │                                     │
│                            │                                     │
│  MUST pass before any     │  Arnês (not LLM) decides verdict  │
│  computation is spent     │  Receipt is Ed25519 signed         │
└────────────────────────────┴───────────────────────────────────┘
```

### 2.3 Pre-flight Diagnostic on Rejection

When pre-flight fails, the executor returns a structured rejection:

```go
// Shell.Check() returns ReadinessReport
type ReadinessReport struct {
    Passed          bool
    Results         []PreconditionResult
    RejectionReason string  // Human-readable: which tool missing, dirty files, etc.
}
```

This allows the emitter to:
- Re-route to a different executor with the required tools
- Notify the user of missing dependencies
- Abort before spending a single token on a doomed task

### 2.4 Implementation: `pkg/shell/shell.go`

```go
// Check runs all preconditions synchronously.
// Returns immediately on first failure.
func (s *Shell) Check(ctx context.Context, preconditions []Precondition) *ReadinessReport {
    report := &ReadinessReport{Passed: true}
    for _, p := range preconditions {
        result := p.Check(ctx, s.workingDir)
        report.Results = append(report.Results, result)
        if !result.Passed {
            report.Passed = false
            report.RejectionReason = fmt.Sprintf("%s: %s", result.Type, result.Error)
        }
    }
    return report
}
```

### 2.5 Git Clean Worktree Precondition

The most important pre-flight check. A dirty worktree means uncommitted changes could interfere with the task or produce non-deterministic results.

```go
type PreconditionGitCleanWorktree struct{ CWD string }

func (p PreconditionGitCleanWorktree) Check(ctx context.Context) PreconditionResult {
    out, err := exec.CommandContext(ctx, "git", "-C", p.CWD, "status", "--porcelain").Output()
    if len(bytes.TrimSpace(out)) > 0 {
        return PreconditionResult{
            Type:    "git_clean_worktree",
            Passed:  false,
            Error:   fmt.Sprintf("dirty worktree: %s", strings.TrimSpace(string(out))),
        }
    }
    return PreconditionResult{Type: "git_clean_worktree", Passed: true}
}
```

---

## 3. Concurrency Architecture: SQLite WAL + Mutex + SetMaxOpenConns(1)

### 3.1 The Problem: Concurrent Writes from Multiple Emitters

Multiple emitters (pi, opencode, claude code) may submit envelopes concurrently to the same executor sidecar. Each settlement produces a `SaveReceipt` call. Without coordination, two concurrent goroutines could:

1. Read the same "last receipt" from SQLite
2. Compute the same `prev_hash = SHA256(last.ExecutorSignature)`
3. Both insert receipts with identical `prev_hash` → broken chain link

### 3.2 The Three-Layer Defense

RFC-002 uses three independent concurrency controls, not one:

```
Layer 1: SQLite WAL mode
  → Allows concurrent READS while a WRITE is in progress
  → Prevents write-write conflicts at the database level
  → Only one writer at a time; WAL enables readers to not block

Layer 2: db.SetMaxOpenConns(1)
  → Serializes ALL database operations through a single connection
  → Prevents the Go sqlite driver from opening multiple connections
  → Eliminates any possibility of two goroutines racing at the driver level

Layer 3: Go mutex (sync.Mutex) in SaveReceipt
  → Serializes prev_hash computation + INSERT
  → prev_hash is computed INSIDE the critical section
  → No two goroutines can read the same "last receipt"
```

### 3.3 Why SetMaxOpenConns(1) Is Safe

```
Without SetMaxOpenConns(1):
  Go sqlite driver may open multiple connections (connection pool)
  Connection A: BEGIN transaction → read last receipt
  Connection B: BEGIN transaction → read last receipt  ← SAME last receipt
  Connection A: INSERT with prev_hash_A
  Connection B: INSERT with prev_hash_B  ← DUPLICATE prev_hash → BROKEN CHAIN

With SetMaxOpenConns(1):
  All operations go through the SINGLE connection
  Connection is always in one of: idle, in-transaction, or done
  No concurrent transactions possible at the driver level
  prev_hash computation inside mutex is guaranteed to see the latest committed state
```

### 3.4 Why Not Just Use the SQLite Transaction?

SQLite transactions alone are not sufficient because:

1. **Go's driver is concurrent by default**: Without `SetMaxOpenConns(1)`, multiple goroutines can open separate connections and run concurrent transactions.
2. **SQLite default isolation is SERIALIZABLE but not MULTI-CONNECTION**: SQLite serializes writes within ONE connection, but multiple connections can race.
3. **The mutex is the critical section**: By holding the mutex during prev_hash computation, we ensure that only one goroutine reads the last receipt at a time.

### 3.5 The Critical Section: prev_hash Computation

```go
// pkg/receipt/chain.go — SaveReceipt
func (cs *ChainStore) SaveReceipt(ctx context.Context, r *SettlementReceipt) error {
    cs.mu.Lock()         // Layer 3: Only one goroutine here at a time
    defer cs.mu.Unlock()

    // prev_hash is computed INSIDE the critical section
    if r.PreviousReceiptHash == "" {
        last, err := cs.getLastReceiptUnlocked(ctx, ...)  // Guaranteed latest
        if last != nil {
            h := sha256.Sum256([]byte(last.ExecutorSignature))
            r.PreviousReceiptHash = hex.EncodeToString(h[:])  // Unique per goroutine
        }
    }

    // Retry loop handles SQLite write conflicts (WAL checkpoint, etc.)
    for attempt := 0; attempt < maxSaveRetries; attempt++ {
        err := cs.saveReceiptOnce(ctx, r)
        if err == nil {
            return nil
        }
        time.Sleep(time.Millisecond * 5 * time.Duration(attempt+1))
    }
    return fmt.Errorf("save after %d attempts", maxSaveRetries)
}
```

### 3.6 Retry Loop: Handling SQLite Write Conflicts

Even with the mutex, SQLite can return `SQLITE_BUSY` during WAL checkpointing. The retry loop with exponential backoff handles this gracefully:

```go
const maxSaveRetries = 3

for attempt := 0; attempt < maxSaveRetries; attempt++ {
    err := cs.saveReceiptOnce(ctx, r)
    if err == nil {
        return nil
    }
    lastErr = err
    if attempt < maxSaveRetries-1 {
        time.Sleep(time.Millisecond * 5 * time.Duration(attempt+1))
    }
}
return fmt.Errorf("save receipt after %d attempts: %w", maxSaveRetries, lastErr)
```

### 3.7 Production Alternative: Sequence Service

The mutex serializes all writes, which limits throughput to ~1 settlement per mutex acquisition cycle. For production systems requiring high throughput:

| Approach | Throughput | Complexity | Use Case |
|----------|-----------|------------|----------|
| Mutex + SQLite (current) | ~10–50/s | Low | Single-node, prototype |
| SQLite with lock service | ~100–500/s | Medium | Multi-node local |
| Dedicated sequence service | ~1000+/s | High | Distributed production |

A production path would replace the mutex with a lightweight sequence service (Redis INCR, ZooKeeper ephemeral node, or a custom coordinator) that provides monotonically increasing sequence numbers. The receipt's `prev_hash` would be `SHA256(executor_signature || sequence_number)` instead of `SHA256(executor_signature)`.

### 3.8 WAL Mode: Why It Matters

SQLite WAL (Write-Ahead Logging) mode is set on every chain database:

```go
_, err = db.ExecContext(ctx, "PRAGMA journal_mode=WAL")
```

WAL mode allows:
- **Concurrent readers**: Readers do not block writers and vice versa. Multiple emitters can `GetChain()` while a settlement is writing.
- **Crash resilience**: WAL ensures atomic writes. If the process crashes mid-write, the WAL replay recovers the database.
- **No locking contention on reads**: The receipt chain can be audited by multiple parties simultaneously.

---

## 4. Identifier Collisions: crypto/rand vs UnixNano

### 4.1 The Problem

Receipt IDs must be globally unique. Two concurrent settlements could generate the same ID if using `time.Now().UnixNano()` as the identifier:

```
Goroutine A: time.Now().UnixNano() → 1727432198765432000
Goroutine B: time.Now().UnixNano() → 1727432198765432000  ← Same! (same tick)
Result: UNIQUE constraint violation → one settlement fails
```

### 4.2 The Solution: crypto/rand for IDs

RFC-002 uses `crypto/rand` for receipt and envelope ID generation:

```go
// pkg/receipt/receipt.go — NewReceipt
func NewReceipt() (*SettlementReceipt, error) {
    var id [16]byte
    if _, err := io.ReadFull(cryptoRand.Reader, id[:]); err != nil {
        return nil, fmt.Errorf("generate receipt ID: %w", err)
    }
    return &SettlementReceipt{
        ReceiptID: hex.EncodeToString(id[:]),  // 32 hex chars, 128 bits of entropy
    }, nil
}
```

**Why 128 bits is sufficient:**
- Probability of collision with 10^9 receipts: ~1 in 2^28 (≈ 268 million)
- Probability of collision with 10^12 receipts: ~1 in 2^8 (256) — still negligible for most use cases
- 16 bytes = 128 bits of cryptographic randomness from `crypto/rand`

### 4.3 Collision Handling

If a collision ever occurs despite `crypto/rand`:

1. `SaveReceipt` returns a retryable error (`UNIQUE constraint failed`)
2. The retry loop in `SaveReceipt` regenerates the ID and retries
3. The emitter receives the error and can retry the entire settlement

```go
// In saveReceiptOnce, the INSERT uses:
//   INSERT INTO receipts (receipt_id, ...) VALUES (?, ...)
// If receipt_id already exists: SQLITE_CONSTRAINT_UNIQUE → retry
```

---

## 5. JCS RFC 8785 Canonicity and Cross-Signature Determinism

### 5.1 The Cross-Signature Problem

Both the executor and the emitter sign the SAME content for the receipt:

```
Executor (B):  Hash(receipt_content) → Sign(hash) with B's private key
Emitter (A):   Hash(receipt_content) → Sign(hash) with A's private key
```

If the executor serializes the receipt as JSON in one order and the emitter serializes it in a different order, their hashes differ, and the signatures don't verify.

**Example of the problem:**
```go
// Executor serializes:
{"verdict":"SETTLED_CLEAN","executor_signed_at":"2026-01-15T10:00:00Z"}

// Emitter serializes (different field order):
{"executor_signed_at":"2026-01-15T10:00:00Z","verdict":"SETTLED_CLEAN"}

// SHA-256(executor_serialization) ≠ SHA-256(emitter_serialization)
// → Emitter cannot verify executor's signature
```

### 5.2 JCS: Canonical Serialization (RFC 8785)

JCS (JCS: JSON Canonicalization Scheme, RFC 8785) solves this by mandating a specific serialization order:

```go
// pkg/jcs/jcs.go
import "github.com/IBM/ugorji/go/codec"

// Marshal encodes v as JSON with deterministic key ordering per RFC 8785.
// - Keys are sorted lexicographically (UTF-8 code points)
// - Numbers use minimum necessary representation
// - Whitespace and key order are canonical
func Marshal(v interface{}) ([]byte, error) { ... }
```

**JCS guarantees:**
- Executor and emitter produce **byte-for-byte identical** JSON for the same receipt content
- SHA-256 hashes match regardless of the serialization library used
- Cross-language compatibility (Go, Python, JavaScript all produce the same bytes)

### 5.3 Example: Canonical Receipt Serialization

```go
receipt := &SettlementReceipt{
    ReceiptID:       "abc123",
    Verdict:          "SETTLED_CLEAN",
    ExecutorSignedAt: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
}

// JCS always produces the same byte sequence:
canonical := `{"contract_id":"","emitter_agent_id":"","emitter_acceptance":"",...}`
//   Key order: contract_id < emitter_acceptance < emitter_agent_id < ...
//   NOT: random map iteration order (Go's default)
```

### 5.4 Hash Computation: sha256.Sum256 After JCS

```go
// pkg/jcs/hash.go
func HashHex(data []byte) (string, error) {
    h := sha256.Sum256(data)  // Direct SHA-256, not HMAC
    return hex.EncodeToString(h[:]), nil
}
```

SHA-256 (not HMAC-SHA256) is used because:
- The content being hashed is already authenticated by Ed25519 signatures
- HMAC requires a shared secret key, which would need to be distributed to all agents
- The Ed25519 signature provides authentication and integrity of the receipt content. **Non-repudiation is partial**: the executor's signature does not cover `previous_receipt_hash` (issue #43) and the emitter's signature does not cover the acceptance/dispute decision, its reason or its timestamp, nor is it verified when persisted (issue #48).

---

## 6. ComputeReceiptHash: The Field Clearing Invariant

### 6.1 The Mutable Fields Problem

A `SettlementReceipt` has fields that change over its lifetime:

```
Initial state (after settle):           After acceptance:
┌────────────────────────────┐         ┌────────────────────────────┐
│ ExecutorSignature:  B's sig  │         │ ExecutorSignature:  B's sig │
│ EmitterSignature:  ""       │         │ EmitterSignature:  A's sig  │
│ EmitterAcceptance: ""       │         │ EmitterAcceptance: ACCEPTED │
│ EmitterAcceptanceAt: nil   │         │ EmitterAcceptanceAt: time  │
│ DisputeReason:    ""       │         │ DisputeReason:    ""       │
│ PreviousReceiptHash: "..." │         │ PreviousReceiptHash: "..."  │
└────────────────────────────┘         └────────────────────────────┘
```

If `ComputeReceiptHash` includes these mutable fields, the hash changes when the emitter signs, causing the executor's signature to become invalid.

### 6.2 The Invariant

**`ComputeReceiptHash` computes the hash over the EXECUTOR-signed content only.**

The executor signs the receipt content as it existed at settlement time (without any emitter response). The emitter must compute the same hash by clearing all emitter-controlled fields before hashing.

```go
// pkg/receipt/chain.go — ComputeReceiptHash
func ComputeReceiptHash(r *SettlementReceipt) (string, error) {
    cleared := *r  // Shallow copy

    // Clear ALL fields that are set AFTER the executor signs
    cleared.ExecutorSignature    = ""
    cleared.EmitterAcceptance    = ""
    cleared.EmitterAcceptanceAt  = nil
    cleared.EmitterSignature     = ""
    cleared.DisputeReason       = ""
    cleared.PreviousReceiptHash = ""

    data, err := jcs.Marshal(&cleared)
    if err != nil {
        return "", fmt.Errorf("jcs marshal: %w", err)
    }
    return jcs.HashHex(data)  // SHA-256 of canonical JSON
}
```

### 6.3 Why Both Signatures Are Cleared

Both `ExecutorSignature` and `EmitterSignature` are cleared because:

1. **`ExecutorSignature`**: The executor hasn't signed yet when computing the hash for signing. The signature is the OUTPUT of the signing process, not an INPUT.

2. **`EmitterSignature`**: The emitter's signature is computed over the same content as the executor's signature. If it were included, the hash would be different for each signer's perspective, breaking mutual verification.

### 6.4 Why PreviousReceiptHash Is Cleared

`PreviousReceiptHash` is set by the `ChainStore` after the executor signs. Including it in the hash would mean:
- The executor computes the hash BEFORE `PreviousReceiptHash` is set
- The stored receipt has `PreviousReceiptHash` set
- The hash doesn't match → verification fails

### 6.5 Bug #45: The Hidden PreviousReceiptHash Bug

In a concurrent scenario, `SaveReceipt` calls `UpdateReceipt` AFTER signing but BEFORE the mutex is released, adding `PreviousReceiptHash` to the stored JSON. This caused the stored JSON to have 91 extra bytes (`"previous_receipt_hash":"<sha256hex>"`) that weren't in the original signed payload.

**Root cause:** `ComputeReceiptHash` was NOT clearing `PreviousReceiptHash`, so the hash computed by the executor didn't match the hash recomputed from the stored receipt.

**Fix:**
```go
// Before fix (WRONG):
cleared.ExecutorSignature  = ""
cleared.EmitterSignature  = ""
// PreviousReceiptHash was NOT cleared → Bug #45

// After fix (CORRECT):
cleared.ExecutorSignature    = ""
cleared.EmitterAcceptance    = ""
cleared.EmitterAcceptanceAt  = nil
cleared.EmitterSignature     = ""
cleared.DisputeReason       = ""
cleared.PreviousReceiptHash = ""  // ← Fix: clear it
```

### 6.6 The Dual-Signature Verification Flow

```
1. Executor settles:
   executor_content = cleared_receipt (no sigs, no acceptance, no prev_hash)
   executor_hash = SHA256(JCS(executor_content))
   executor_sig = Ed25519.Sign(executor_hash, B_private_key)
   receipt.ExecutorSignature = executor_sig

2. Emitter accepts:
   emitter_content = cleared_receipt (same as step 1)
   emitter_hash = SHA256(JCS(emitter_content))  ← SAME hash as executor!
   emitter_sig = Ed25519.Sign(emitter_hash, A_private_key)
   receipt.EmitterSignature = emitter_sig

3. Verification (anyone):
   hash = SHA256(JCS(cleared_receipt))
   Ed25519.Verify(hash, executor_sig, B_public_key) → true
   Ed25519.Verify(hash, emitter_sig,  A_public_key) → true
   ✓ Both signatures valid over the SAME content
```

---

## 7. VerifyChain Security: Blocking Identity Spoofing

### 7.1 The Attack Vector

An attacker who compromises an executor's private key (or steals a receipt) could:

1. Create a forged receipt claiming to be from `agent-b`
2. Sign it with the stolen key
3. Submit it to the chain store

The `VerifyChain` function must detect this and reject the receipt.

### 7.2 The Defense: Authoritative Public Key Validation

`VerifyChain` takes the **authoritative** public key as a parameter and uses it for ALL signature verification:

```go
// pkg/receipt/chain.go — VerifyChain
func (cs *ChainStore) VerifyChain(
    ctx context.Context,
    emitterID, executorID string,
    executorPublicKey []byte,   // Authoritative key for executorID
    emitterPublicKey []byte,    // Authoritative key for emitterID
) ([]VerificationResult, error) {
    chain, err := cs.GetChain(ctx, emitterID, executorID)
    // ...
    for i, r := range chain {
        // Verify executor signature using the AUTHORITATIVE executor key
        receiptHash, _ := ComputeReceiptHash(r)
        err := signing.Verify(executorPublicKey, []byte(receiptHash), r.ExecutorSignature)
        result.ExecutorSignatureValid = (err == nil)
        if err != nil {
            result.Error = "executor signature invalid: " + err.Error()
        }
    }
}
```

### 7.3 The Security Invariant

```
For a receipt claiming to be from executor=E:
  VerifyChain(E, E_pubkey, ...) → VALID if and only if
  the signature was produced by the holder of E's private key
```

If an attacker signs a receipt with key C's private key but claims to be executor B:
```
  VerifyChain("agent-a", "agent-b", B_pubkey, ...) on receipt signed by C:
    signing.Verify(B_pubkey, receipt_hash, sig_by_C) → FAIL ✓
    ExecutorSignatureValid = false ✓
```

### 7.4 WU17 Test: Validating the Defense

The distributed test `TestWU17_VerifyChain_WrongExecutorKey` validates this:

```
Step 1: A → B: Legitimate settle (B signs with B's key) → VALID ✓
Step 2: A re-signs receipt with test harness cSigner (wrong key)
Step 3: POST /verify: detects tampered receipt as INVALID ✓
Step 4: UpdateReceipt replaces receipt in chain
Step 5: POST /verify-chain with B's key:
          ExecutorSigValid = false ✓
          AllValid = false ✓
          Error: "executor signature invalid: verify: signature verification failed"
```

### 7.5 What VerifyChain Does NOT Do

`VerifyChain` is NOT responsible for:
- **Key distribution**: How the authoritative public keys are exchanged is outside its scope. RFC-002 specifies manual enrollment (Model A).
- **Certificate validation**: The mTLS layer validates certificates at the transport layer. `VerifyChain` validates Ed25519 signatures at the application layer.
- **Chain authorization**: Whether a specific emitter-executor pair is allowed is handled by the mesh admission policy.

### 7.6 The Defense in Depth Model

```
Layer 1 — Transport: mTLS
  Client and server certificates signed by a shared CA
  Both parties authenticate each other at TLS handshake time
  → Attacker cannot even connect to the mesh without a valid cert

Layer 2 — Application: Ed25519 signatures on envelopes
  Each CognitiveTaskEnvelope is signed by the emitter
  Executor verifies envelope signature before accepting the lease
  → Attacker cannot submit fake envelopes

Layer 3 — Settlement: Ed25519 signatures on receipts
  Executor signs the receipt with its private key
  Emitter verifies before accepting
  → Attacker cannot forge a settlement receipt

Layer 4 — Chain: VerifyChain with authoritative keys
  Any auditor can verify the entire chain using authoritative public keys
  → Attacker cannot plant a forged receipt without detection
```

---

## 8. Summary: Security Invariants

These invariants must NEVER be broken for the RFC-002 settlement protocol to provide its guarantees:

### Cryptographic Invariants

| # | Invariant | Enforced By |
|---|-----------|-------------|
| C1 | `ComputeReceiptHash` always clears all mutable fields | Source code review + unit test |
| C2 | Both executor and emitter sign the same canonical bytes | JCS RFC 8785 |
| C3 | SHA-256 chain links use `SHA256(executor_signature)`, not content hash | Source code |
| C4 | Receipt IDs use `crypto/rand` (128-bit), not `time.Now().UnixNano` | Source code |

### Concurrency Invariants

| # | Invariant | Enforced By |
|---|-----------|-------------|
| CO1 | `prev_hash` is computed INSIDE the mutex critical section | Source code + integration test |
| CO2 | `SetMaxOpenConns(1)` prevents multi-connection races | Source code |
| CO3 | SQLite WAL mode enables concurrent reads without blocking writers | DB configuration |
| CO4 | Retry loop handles `SQLITE_BUSY` from WAL checkpointing | Source code |

### Security Invariants

| # | Invariant | Enforced By |
|---|-----------|-------------|
| S1 | `VerifyChain` uses authoritative public keys, not embedded keys | Source code + WU17 test |
| S2 | Receipts with wrong-key signatures are rejected with `ExecutorSignatureValid=false` | WU17 integration test |
| S3 | Private keys never leave the keystore (0600 permissions) | OS permissions |
| S4 | `ChainDB` is stored separately from git workspace | Path configuration |
| S5 | Pre-flight must pass before any computation is spent | Protocol ordering |

### Operational Invariants

| # | Invariant | Enforced By |
|---|-----------|-------------|
| O1 | A dirty git worktree always rejects the lease before execution | `PreconditionGitCleanWorktree` |
| O2 | A disputed receipt cannot be accepted (409 Conflict) | `handleDispute` + WU16 test |
| O3 | An already-accepted receipt cannot be disputed (409 Conflict) | `handleDispute` + WU16 test |
| O4 | Multiple emitters share one executor sidecar with isolated chains | `(emitter, executor)` pair key in SQLite |
| O5 | `Bug #45` (PreviousReceiptHash not cleared) cannot recur | `TestComputeReceiptHash_ClearsAllFields` unit test |

---

## References

- **RFC-002 Settlement Protocol Formal Spec:** `docs/protocol/SETTLEMENT-PROTOCOL-FORMAL-SPEC.md`
- **Shell-Mesh Cohabitation:** `docs/architecture/SHELL-MESH-COHABITATION.md`
- **JCS RFC 8785:** `pkg/jcs/`
- **Receipt Chain:** `pkg/receipt/chain.go`
- **Integration Tests:** `integration/testscenario/wu13_test.go` (WU13–WU17)
- **Bug #45 Fix:** Commit `065b023` — fix receipt: clear PreviousReceiptHash in ComputeReceiptHash
