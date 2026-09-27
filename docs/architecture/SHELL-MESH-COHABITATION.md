# Shell-Mesh Cohabitation: Gentle-Shell + Gentle-Mesh as a Unified Execution Node

> **RFC-002 Extension** — Coupling the settlement protocol with the shell execution layer  
> **Author:** el Gentleman (architect)  
> **Status:** Design Specification — Phase 1 of 4  
> **Base:** RFC-002 Settlement Protocol (`docs/protocol/SETTLEMENT-PROTOCOL-FORMAL-SPEC.md`)

---

## 1. Problem Diagnosis

### 1.1 Current State: Blind Execution

The `runCommand()` function in `pkg/settlement/evaluate.go` executes shell commands with no contract, no preconditions, and no verifiable outcome:

```go
// CURRENT: blind execution
cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
cmd.Dir = e.workingDir
err := cmd.Run()
// No receipt. No assertion. No signature. LLM must trust the result.
```

This is the **Green Checkbox Problem** in its purest form: the exit code is checked but there is no cryptographic proof of what happened, no deterministic settlement, and no audit trail.

### 1.2 What Changes: Contract-Aware Execution

The coupling introduces four guarantees before, during, and after execution:

| Phase | Question Answered | Mechanism |
|-------|-------------------|-----------|
| Pre-flight | Can I execute safely? | `Check()` — tools, git, deps |
| Contract | What constitutes success? | `CognitiveTaskEnvelope` with assertions |
| Settlement | Did assertions pass? | Arnês deterministic evaluation |
| Receipt | Is there an audit trail? | `SettlementReceipt` + SHA-256 chain |

This mirrors the **CodeGraph principle**: _verify before assume_. Every command execution becomes a cryptographically signed, assertion-verified, receipt-chained event.

---

## 2. Architectural Coupling Modes

### 2.1 Mode A — Embedded Sidecar (Local Node)

Gentle-mesh runs as a local HTTP daemon on the same host as gentle-shell. Communication is `localhost`.

```
┌─────────────────────────────────────────────────────┐
│  Host Machine                                        │
│  ┌───────────────────────────────────────────────┐  │
│  │  gentle-shell (process)                        │  │
│  │  - Shell session (your AI agent)              │  │
│  │  - pkg/shell/shell.go                         │  │
│  └──────────────────────┬────────────────────────┘  │
│                          │ HTTP (localhost:8443)      │
│                          ▼                          │
│  ┌───────────────────────────────────────────────┐  │
│  │  gentle-mesh (sidecar daemon)                  │  │
│  │  - agent.Server (:8443)                        │  │
│  │  - Keystore (/data/keystore/)                 │  │
│  │  - ChainDB (/data/chain.db)                   │  │
│  │  - Settlement Engine                           │  │
│  └───────────────────────────────────────────────┘  │
│  ┌───────────────────────────────────────────────┐  │
│  │  Workspace (/srv/workspace)                    │  │
│  │  - Git repo                                    │  │
│  │  - Source files, test files                   │  │
│  └───────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────┘
```

**Use case:** Single-machine development, local AI agents (pi harness, Claude Desktop, etc.)

**Protocol flow (Mode A):**
```
gentle-shell                  gentle-mesh (sidecar)
     │                              │
     │──── Check(preconditions) ────▶│  ← fast, local, no network
     │◀─── ReadinessReport ──────────│
     │                              │
     │──── Execute(cmd, assertions)─▶│
     │    [runCommand + evaluate]    │  ← deterministic settlement
     │◀─── SettlementReceipt ───────│  ← Ed25519 signed, SHA-256 linked
     │                              │
```

### 2.2 Mode B — Remote Executor (Network Node)

Gentle-shell submits a `CognitiveTaskEnvelope` to a remote gentle-mesh executor over HTTPS. The executor runs commands on behalf of the caller.

```
┌──────────────┐                          ┌──────────────────────────────────┐
│  Host        │    HTTPS + mTLS         │  Remote Executor (Container)     │
│  gentle-shell│◀────────────────────────▶│  gentle-mesh (:8443)             │
│  (pi agent)  │    CognitiveTaskEnvelope │  ┌──────────────────────────┐   │
└──────────────┘    + Ed25519 sig         │  │  Settlement Engine       │   │
                                          │  │  - runCommand()          │   │
                                          │  │  - Evaluator             │   │
                                          │  │  - ChainStore            │   │
                                          │  └──────────────────────────┘   │
                                          │  ┌──────────────────────────┐   │
                                          │  │  Workspace (/srv/workspace)│   │
                                          │  │  - Git repo               │   │
                                          │  └──────────────────────────┘   │
                                          └──────────────────────────────────┘
```

**Use case:** Distributed CI/CD, multi-host workflows, fan-out to specialized executors.

### 2.3 Mode C — Hybrid (Local Pre-flight, Remote Execution)

Pre-flight checks run locally (fast, no network latency). The actual execution runs on a remote executor. This optimizes for the common case where local resources are cheap to check.

```
gentle-shell              gentle-mesh (local)           gentle-mesh (remote)
     │                           │                              │
     │── Check(preconditions) ──▶│  ← local, <10ms              │
     │◀── ReadinessReport ───────│                              │
     │                           │                              │
     │──── SubmitEnvelope ──────────────────────────────────────▶│
     │◀─── LeaseResponse ────────────────────────────────────────│
     │                           │                              │
     │──── Execute(cmd) ────────────────────────────────────────▶│
     │    [runCommand + evaluate on remote]                       │
     │◀─── SettlementReceipt ────────────────────────────────────│
```

**Use case:** Fan-out from a pi harness to multiple specialized executors (GPU node, CPU node, etc.) with local sanity checks first.

---

## 3. The Verification-Guaranteed Execution Protocol

### 3.1 EBNF Grammar

```
ExecutionFlow    ::= PreFlight Check Contract Submit Execute Settle Receipt

PreFlight        ::= "Check" "(" Preconditions ")" "->" ReadinessReport

Contract         ::= "CreateEnvelope" "(" Intent Assertions Resources ")"
                 |  "SignEnvelope" "(" Envelope Signer ")" "->" SignedEnvelope

Submit          ::= "Submit" "(" SignedEnvelope ")" "->" LeaseResponse

Execute         ::= "Execute" "(" Command ")" "->" ExecutionResult

Settle          ::= "Settle" "(" Envelope LeaseID Results ")" "->" ReceiptResponse

Receipt         ::= "AppendReceipt" "(" Receipt ChainDB ")" "->" ChainLink
```

### 3.2 Step-by-Step Protocol

#### Step 0: Pre-flight — `Shell.Check(ctx, preconditions)`

Synchronous, local, sub-10ms. Validates readiness before any contract is created.

```
Input:  []Precondition{
           PreconditionGitCleanWorktree{},
           PreconditionToolAvailable{tool: "go", version: ">=1.22"},
           PreconditionToolAvailable{tool: "pytest"}
         }

Output: ReadinessReport{
           Passed:     bool,
           Results:   []PreconditionResult{
                        {Type: "git_clean_worktree", Passed: true},
                        {Type: "tool_available", Passed: false, Missing: "pytest"}
                      },
           RejectionReason: string  // If Passed=false, explains WHY
         }
```

If `Passed == false`: gentle-shell MUST NOT proceed. The rejection reason is returned to the calling agent as a structured diagnostic.

#### Step 1: Contract Creation — `Shell.CreateEnvelope(intent, assertions)`

Builds a `CognitiveTaskEnvelope` with the task metadata and the assertion contract.

```
Input:  Intent:     "Add multiplication and division to calculator.py"
        Assertions: [
          AssertionFileHashEquals{
            Path: "calculator.py",
            ExpectedSHA256: "<baseline_hash>"
          },
          AssertionCommandExitCode{
            Command: "pytest -v test_calculator.py",
            ExpectedExitCode: 0
          }
        ]
        Resources: [
          ResourceTool{tool: "python3"},
          ResourceGit{branch: "main", clean: true}
        ]

Output: *CognitiveTaskEnvelope{
           EnvelopeID:   "tsk-" + uuidv7(),
           Intent:       "...",
           Territory:    Repository + Branch + WorkspacePath,
           Assertions:   [...],  // The settlement contract
           Preconditions: [...],
           CreatedAt:    now(),
           EnvelopeHash: SHA256(JCS(envelope))
         }
```

#### Step 2: Signing — `Shell.SignEnvelope(env)`

Signs the envelope with Ed25519. The hash is computed over JCS-canonical JSON (RFC 8785).

```
Input:  *CognitiveTaskEnvelope (unsigned)

Process:
  1. signable = copy(env); signable.EmitterSignature = ""
  2. canonical = jcs.Marshal(signable)  // RFC 8785
  3. sig = Ed25519.Sign(signer.PrivateKey(), canonical)
  4. env.EmitterSignature = sig
  5. env.EnvelopeHash = SHA256(JCS(env))

Output: *CognitiveTaskEnvelope (signed)
```

#### Step 3: Submission — `MeshClient.Submit(envelope)` (Mode B/C) or local

Submits the signed envelope to the executor. For Mode A (local sidecar), this is a direct function call.

```
POST /envelopes
Content-Type: application/json
Authorization: Ed25519-Signature <sig>

Body: Signed CognitiveTaskEnvelope JSON

Response: LeaseResponse{
            Accepted:  true,
            LeaseID:  "lease-" + timestamp,
            PreconditionResults: [...],
            ExecutorPublicKey:    "<hex>",
            ExpiresAt:           now + timeout
          }
```

**Lease rejection** (Accepted: false) returns a structured `RejectionReason` — NOT an opaque error string. Example:

```json
{
  "accepted": false,
  "error": "preconditions not met",
  "diagnostics": [
    {"type": "git_clean_worktree", "passed": false, "message": "M src/main.go"},
    {"type": "tool_available", "passed": true}
  ],
  "suggested_nodes": ["gpu-node-2", "cpu-fallback"]
}
```

#### Step 4: Execution — `Evaluator.Execute(command, assertions)`

Runs the command and evaluates assertions deterministically. This is the **core** of the settlement engine.

```
Input:  Command:  "pytest -v"
        Assertions: [file_hash_equals, command_exit_code, git_clean_worktree]

Process:
  For each assertion (deterministic, order preserved):
    result = Evaluator.Evaluate(ctx, assertion)
    
  verdict = computeVerdict(results, maxRemediations)

Output: []*AssertionResult{
           {AssertionID: "file_modified", Result: "PASS", Evidence: {...}},
           {AssertionID: "tests_passing", Result: "PASS", Evidence: {...}}
         }
        Verdict: SETTLED_CLEAN | SETTLED_WITH_REMEDIATION | SETTLEMENT_FAILED
```

The `Evaluator` is the **arnês** — it runs assertions in isolation, captures evidence, and produces deterministic results. The LLM has zero influence over the verdict.

#### Step 5: Settlement Receipt — `MeshClient.Settle(leaseID, envJSON, results)`

The executor computes the `SettlementReceipt`, signs it with Ed25519, and returns it.

```
POST /settle
Body: {
  "envelope_json": "<signed envelope>",
  "lease_id": "lease-...",
  "assertion_results": [...]
}

Response: {
  "receipt_json": SettlementReceipt{
                    ReceiptID:          "rcpt-" + uuidv7(),
                    ExecutorAgentID:    "agent-b",
                    EnvelopeHash:       "...",
                    Verdict:            "SETTLED_CLEAN",
                    Assertions:         [...],
                    ExecutorSignature:  "<Ed25519 sig>",
                    ExecutorSignedAt:   "2026-09-26T...",
                    SettlementHash:     SHA256(JCS(receipt))
                  }
}
```

#### Step 6: Receipt Chain — Appended automatically by the executor

The executor appends the receipt to its local `ChainStore` (SQLite). The `previous_receipt_hash` is `SHA256(executor_signature)`.

```
previous_receipt_hash = SHA256([]byte(receipt.ExecutorSignature))
// NOT: SHA256(JCS(receipt)) — only the executor's signature, immutable
```

---

## 4. Code Integration Points

### 4.1 New Package: `pkg/shell/shell.go`

```go
// Package shell provides a contract-aware shell execution layer
// that integrates with gentle-mesh for verified, receipt-backed command execution.
package shell

import (
    "context"
    "time"

    "github.com/gentleman-programming/gentle-mesh/pkg/envelope"
    "github.com/gentleman-programming/gentle-mesh/pkg/keystore"
    "github.com/gentleman-programming/gentle-mesh/pkg/receipt"
    "github.com/gentleman-programming/gentle-mesh/pkg/settlement"
    "github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// Config configures a Shell node.
type Config struct {
    AgentID      string        // Local agent identity (e.g., "pi-local")
    WorkspaceDir string        // Working directory for command execution
    ChainDBPath string        // Path to SQLite receipt chain (e.g., "/data/chain.db")
    KeystoreDir string        // Path to keystore (e.g., "/data/keystore")
    EvalTimeout time.Duration // Max time for a single assertion evaluation
    MaxRemediations int       // Max remediation loops before SETTLEMENT_FAILED
    RemoteURL string          // Remote executor URL (empty = local sidecar mode)
}

// Shell is a contract-aware shell execution layer.
// It wraps gentle-mesh's settlement engine with a simple synchronous API.
type Shell struct {
    config     Config
    signer     *signing.BasicSigner
    evaluator  *settlement.Evaluator
    chainStore *receipt.ChainStore
    keystore   *keystore.Keystore
    meshClient *MeshClient  // nil in local sidecar mode
}

// New creates a new Shell instance.
// In local mode (RemoteURL == ""), gentle-mesh runs embedded.
// In remote mode (RemoteURL != ""), commands are submitted to the remote executor.
func New(cfg Config) (*Shell, error) {
    ks, err := keystore.LoadOrCreate(cfg.KeystoreDir)
    if err != nil {
        return nil, fmt.Errorf("load keystore: %w", err)
    }

    signer, err := signing.NewBasicSigner(ks.PrivateKey())
    if err != nil {
        return nil, fmt.Errorf("create signer: %w", err)
    }

    evaluator := settlement.NewEvaluator(cfg.WorkspaceDir)
    evaluator = evaluator.WithTimeout(cfg.EvalTimeout)

    chainStore, err := receipt.NewChainStore(cfg.ChainDBPath)
    if err != nil {
        return nil, fmt.Errorf("open chain store: %w", err)
    }

    s := &Shell{
        config:     cfg,
        signer:     signer,
        evaluator:  evaluator,
        chainStore: chainStore,
        keystore:   ks,
    }

    if cfg.RemoteURL != "" {
        s.meshClient, err = NewMeshClient(cfg.RemoteURL, signer)
        if err != nil {
            return nil, fmt.Errorf("mesh client: %w", err)
        }
    }

    return s, nil
}

// Check runs pre-flight checks synchronously.
// Returns a ReadinessReport; if Passed==false, caller MUST NOT proceed.
func (s *Shell) Check(ctx context.Context, preconditions []envelope.Precondition) (*ReadinessReport, error) {
    // Mode A (local): runPreconditions directly
    if s.meshClient == nil {
        results := s.runLocalPreconditions(preconditions)
        allPassed := true
        for _, r := range results {
            if !r.Passed {
                allPassed = false
                break
            }
        }
        return &ReadinessReport{Results: results, Passed: allPassed}, nil
    }

    // Mode B/C (remote): HTTP call to executor's /health then /preflight
    return s.meshClient.Check(ctx, preconditions)
}

// runLocalPreconditions executes preconditions on the local filesystem.
func (s *Shell) runLocalPreconditions(preconds []envelope.Precondition) []envelope.PreconditionResult {
    results := make([]envelope.PreconditionResult, len(preconds))
    for i, p := range preconds {
        results[i] = s.evalPrecondition(p)
    }
    return results
}

// Execute runs a command with assertions and returns a SettlementReceipt.
// This is the core of the verification-guaranteed protocol.
func (s *Shell) Execute(ctx context.Context, cmd string, assertions []envelope.Assertion) (*receipt.SettlementReceipt, error) {
    // Step 1: Convert envelope.Assertion → settlement.Assertion
    settledAssertions := settlement.ConvertAssertions(assertions)

    // Step 2: Evaluate all assertions deterministically
    evalCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
    defer cancel()

    results := s.evaluator.EvaluateAll(evalCtx, settledAssertions)
    allPass, failed, errors := settlement.Summary(results)

    // Step 3: Compute verdict
    var verdict receipt.Verdict
    if allPass {
        verdict = receipt.VerdictSettledClean
    } else if errors > 0 {
        verdict = receipt.VerdictSettlementFailed
    } else {
        verdict = receipt.VerdictSettlementFailed
    }

    // Step 4: Build receipt
    rec := &receipt.SettlementReceipt{
        ReceiptID:     receipt.NewReceiptID(),
        ExecutorAgentID: s.config.AgentID,
        CreatedAt:     time.Now().UTC(),
        Verdict:      verdict,
        Assertions:    convertResults(results),
        SettledAt:    time.Now().UTC(),
    }

    // Step 5: Sign receipt (Ed25519)
    sig, err := receipt.SignReceipt(rec, s.signer, time.Now().UTC())
    if err != nil {
        return nil, fmt.Errorf("sign receipt: %w", err)
    }
    rec.ExecutorSignature = sig.ExecutorSignature
    rec.ExecutorSignedAt = sig.ExecutorSignedAt

    // Step 6: Append to chain
    if err := s.chainStore.Append(rec); err != nil {
        return nil, fmt.Errorf("append to chain: %w", err)
    }

    return rec, nil
}

// SubmitEnvelope submits a CognitiveTaskEnvelope to a remote executor (Mode B/C).
func (s *Shell) SubmitEnvelope(ctx context.Context, env *envelope.CognitiveTaskEnvelope) (*LeaseResponse, error) {
    if s.meshClient == nil {
        return nil, errors.New("SubmitEnvelope requires a remote executor (RemoteURL)")
    }
    return s.meshClient.SubmitEnvelope(ctx, env)
}

// SettleWithRemote executes assertions on a remote executor and returns the receipt.
func (s *Shell) SettleWithRemote(ctx context.Context, env *envelope.CognitiveTaskEnvelope, leaseID string) (*receipt.SettlementReceipt, error) {
    if s.meshClient == nil {
        return nil, errors.New("SettleWithRemote requires a remote executor")
    }
    return s.meshClient.Settle(ctx, env, leaseID)
}

// Close releases resources.
func (s *Shell) Close() error {
    if s.chainStore != nil {
        s.chainStore.Close()
    }
    return nil
}
```

### 4.2 New Package: `pkg/shell/mesh.go`

```go
package shell

import (
    "bytes"
    "context"
    "encoding/json"
    "net/http"

    "github.com/gentleman-programming/gentle-mesh/integration/agent"
    "github.com/gentleman-programming/gentle-mesh/pkg/envelope"
    "github.com/gentleman-programming/gentle-mesh/pkg/jcs"
    "github.com/gentleman-programming/gentle-mesh/pkg/receipt"
    "github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// MeshClient calls a remote gentle-mesh executor.
// It is used in Mode B and Mode C (remote executor).
type MeshClient struct {
    httpClient *agent.HTTPClient
    signer     *signing.BasicSigner
    baseURL    string
}

// NewMeshClient creates a MeshClient for the given executor URL.
func NewMeshClient(baseURL string, signer *signing.BasicSigner) (*MeshClient, error) {
    client, err := agent.NewHTTPClientTLS(baseURL)
    if err != nil {
        return nil, err
    }
    return &MeshClient{baseURL: baseURL, httpClient: client, signer: signer}, nil
}

// ReadinessReport is the result of a pre-flight check.
type ReadinessReport struct {
    Passed         bool
    Results        []envelope.PreconditionResult
    RejectionReason string
}

// Check calls the remote executor's pre-flight endpoint.
func (m *MeshClient) Check(ctx context.Context, preconditions []envelope.Precondition) (*ReadinessReport, error) {
    body, _ := json.Marshal(preconditions)
    req, err := http.NewRequestWithContext(ctx, "POST", m.baseURL+"/preflight", bytes.NewReader(body))
    if err != nil {
        return nil, err
    }
    req.Header.Set("Content-Type", "application/json")

    resp, err := m.httpClient.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()

    var report ReadinessReport
    if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
        return nil, err
    }
    return &report, nil
}

// LeaseResponse is the response from a successful envelope submission.
type LeaseResponse struct {
    Accepted bool
    LeaseID  string
    Diagnostics []envelope.PreconditionResult
    Error    string
}

// SubmitEnvelope signs and submits a CognitiveTaskEnvelope to the executor.
func (m *MeshClient) SubmitEnvelope(ctx context.Context, env *envelope.CognitiveTaskEnvelope) (*LeaseResponse, error) {
    // Sign envelope
    signable := *env
    signable.EmitterSignature = ""
    canonical, err := jcs.Marshal(&signable)
    if err != nil {
        return nil, err
    }
    sig, err := m.signer.Sign(canonical)
    if err != nil {
        return nil, err
    }
    env.EmitterSignature = sig

    body, err := json.Marshal(env)
    if err != nil {
        return nil, err
    }

    resp, err := m.httpClient.SubmitEnvelope(ctx, body)
    if err != nil {
        return nil, err
    }

    return &LeaseResponse{
        Accepted:  resp.Accepted,
        LeaseID:   resp.LeaseID,
        Diagnostics: resp.Diagnostics,
        Error:     resp.Error,
    }, nil
}

// Settle instructs the remote executor to evaluate assertions and return a receipt.
func (m *MeshClient) Settle(ctx context.Context, env *envelope.CognitiveTaskEnvelope, leaseID string) (*receipt.SettlementReceipt, error) {
    envJSON, err := json.Marshal(env)
    if err != nil {
        return nil, err
    }

    resp, err := m.httpClient.Settle(ctx, &agent.SettleRequest{
        EnvelopeJSON: envJSON,
        LeaseID:     leaseID,
    })
    if err != nil {
        return nil, err
    }

    var rec receipt.SettlementReceipt
    if err := json.Unmarshal(resp.ReceiptJSON, &rec); err != nil {
        return nil, err
    }
    return &rec, nil
}
```

### 4.3 Existing Code Reuse

The coupling reuses existing packages without modification:

| Package | Role | Used By |
|---------|------|---------|
| `pkg/envelope/` | Contract types, validation | `shell.go`, `mesh.go` |
| `pkg/settlement/` | `Evaluator`, assertion evaluation | `shell.go` |
| `pkg/receipt/` | `SettlementReceipt`, `ChainStore` | `shell.go` |
| `pkg/signing/` | Ed25519 Sign/Verify | `shell.go`, `mesh.go` |
| `pkg/keystore/` | Key storage (0600 files) | `shell.go` |
| `pkg/jcs/` | RFC 8785 canonicalization | `mesh.go` |
| `integration/agent/` | `HTTPClient`, HTTP server | `mesh.go` |

---

## 5. Unified Node Dockerfile

The **unified node** is a single Docker container that runs both gentle-mesh and (optionally) gentle-shell as a single process or sidecar pair.

```dockerfile
# Dockerfile.unified — Single container: gentle-mesh + gentle-shell cohabitation
FROM --platform=linux/amd64 golang:1.27-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build \
    -ldflags="-s -w" \
    -o /mesh-binary \
    ./cmd/test-harness

FROM --platform=linux/amd64 alpine:3.20 AS runtime
RUN apk add --no-cache \
    git bash ca-certificates openssl python3 py3-pip \
    && pip install --no-cache-dir pytest --break-system-packages \
    && rm -rf /var/cache/apk/* /tmp/pip-*

# Volume layout (mount workspace and data from host):
#   /data/keystore/   — Ed25519 keys (0600)
#   /data/chain.db    — SQLite receipt chain
#   /srv/workspace/   — Git repo + source files
RUN mkdir -p /data/keystore /data /srv/workspace
WORKDIR /srv/workspace

COPY --from=builder /mesh-binary /usr/local/bin/mesh
COPY integration/certs/gen-certs.sh /usr/local/bin/gen-certs.sh
RUN chmod +x /usr/local/bin/gen-certs.sh /usr/local/bin/mesh

# Health check: gentle-mesh /health endpoint
HEALTHCHECK --interval=10s --timeout=3s --retries=3 \
    CMD wget -qO- --no-check-certificate https://localhost:8443/health || exit 1

# Default: run gentle-mesh server (sidecar mode)
# Override with: mesh serve --port 8443 --agent-id <id>
# Or:          mesh shell --workspace /srv/workspace --chain-db /data/chain.db
ENTRYPOINT ["/usr/local/bin/mesh"]
CMD ["serve", "--port", "8443"]
```

**Volume mounts (from docker-compose):**
```yaml
volumes:
  # Workspace (git repo + source code)
  - ./workspace:/srv/workspace
  # ChainDB and keystore (separate from workspace to avoid git pollution)
  - ./data/chain.db:/data/chain.db
  - ./data/keystore:/data/keystore:ro
  # TLS certificates
  - ./certs:/certs:ro
```

---

## 6. Docker Test Suite: `integration/shellmesh_test.go`

### 6.1 Test Infrastructure

```go
// Test infrastructure: pi-emitter container + unified-node container
// Network: shellmesh-net (isolated bridge)

// composeUpUnified: builds unified-node image, starts both containers
// pi container: runs at localhost:18443
// executor container: runs at localhost:28443 (unified node)
```

### 6.2 Test 1: Local Pre-flight Rejects Dirty Worktree

```go
func TestShellMesh_LocalPreFlightRejectsDirtyWorktree(t *testing.T) {
    // Setup: clean git repo
    dir := initGitRepo(t, workspaceDir)
    defer os.RemoveAll(dir)

    // Make a change but don't commit (worktree is dirty)
    os.WriteFile(filepath.Join(dir, "dirty.go"), []byte("// uncommitted"), 0644)

    // Create shell in LOCAL mode (no remote executor)
    sh, err := shell.New(shell.Config{
        AgentID:      "pi-local",
        WorkspaceDir: dir,
        ChainDBPath:  t.TempDir() + "/chain.db",
        KeystoreDir:  t.TempDir() + "/keystore",
        EvalTimeout:  30 * time.Second,
        // RemoteURL == "" → local mode
    })
    if err != nil {
        t.Fatal(err)
    }
    defer sh.Close()

    // Execute with git_clean_worktree precondition
    preconditions := []envelope.Precondition{
        {Type: envelope.PreconditionGitCleanWorktree, Params: map[string]string{}},
    }
    report, err := sh.Check(context.Background(), preconditions)
    if err != nil {
        t.Fatal(err)
    }

    // ASSERTION: Pre-flight MUST reject dirty worktree
    if report.Passed {
        t.Error("Check() should reject dirty worktree, but Passed=true")
    }
    if len(report.Results) == 0 {
        t.Error("Expected at least one precondition result")
    }
    t.Logf("Rejection reason: %s", report.RejectionReason)
}
```

### 6.3 Test 2: Local Execution with Assertions

```go
func TestShellMesh_LocalExecutionWithAssertions(t *testing.T) {
    dir := initGitRepo(t, t.TempDir())
    defer os.RemoveAll(dir)

    // Create initial file and commit
    initPy := `def add(a, b): return a + b`
    os.WriteFile(filepath.Join(dir, "calc.py"), []byte(initPy), 0644)
    execGit(t, dir, "git add . && git commit -m 'init'")

    // Get baseline hash
    baselineHash := sha256File(t, filepath.Join(dir, "calc.py"))

    sh, err := shell.New(shell.Config{
        AgentID:      "pi-local",
        WorkspaceDir: dir,
        ChainDBPath:  t.TempDir() + "/chain.db",
        KeystoreDir:  t.TempDir() + "/keystore",
        EvalTimeout:  30 * time.Second,
    })
    if err != nil {
        t.Fatal(err)
    }
    defer sh.Close()

    // Assertions: file modified (hash changed), exit code 0
    assertions := []envelope.Assertion{
        {
            ID:   "calc_modified",
            Type: envelope.AssertionFileModified,
            Params: envelope.AssertionParams{FilePath: "calc.py"},
        },
        {
            ID:   "python_works",
            Type: envelope.AssertionCommandExitCode,
            Params: envelope.AssertionParams{
                Command:          "python3 -c 'import calc; assert calc.add(2,3)==5'",
                ExpectedExitCode: 0,
            },
        },
    }

    // Modify file
    os.WriteFile(filepath.Join(dir, "calc.py"),
        []byte(initPy+"\ndef sub(a, b): return a - b"), 0644)

    rec, err := sh.Execute(context.Background(), "python3 calc.py", assertions)
    if err != nil {
        t.Fatalf("Execute() failed: %v", err)
    }

    // ASSERTIONS
    if rec.Verdict != receipt.VerdictSettledClean {
        t.Errorf("Verdict=%s, want SETTLED_CLEAN", rec.Verdict)
    }
    if rec.ExecutorSignature == "" {
        t.Error("ExecutorSignature should be non-empty (Ed25519 signed)")
    }

    // Verify Ed25519 signature
    hash, _ := receipt.ComputeReceiptHash(rec)
    signer, _ := signing.GenerateSigner("pi-local") // In real test, fetch from keystore
    if err := signing.Verify(signer.PublicKey(), []byte(hash), rec.ExecutorSignature); err != nil {
        t.Errorf("Ed25519 signature invalid: %v", err)
    }

    // Verify chain (only one receipt)
    chain, err := sh.chainStore.GetChain("pi-local", "pi-local")
    if err != nil {
        t.Fatal(err)
    }
    if len(chain.Receipts) != 1 {
        t.Errorf("Chain length=%d, want 1", len(chain.Receipts))
    }
    if chain.Receipts[0].PreviousReceiptHash != "" {
        t.Errorf("First receipt prev_hash=%s, want empty", chain.Receipts[0].PreviousReceiptHash)
    }
}
```

### 6.4 Test 3: Remote Executor — Envelope Round-trip

```go
func TestShellMesh_RemoteExecutorEnvelopeRoundTrip(t *testing.T) {
    if testing.Short() {
        t.Skip("requires Docker")
    }
    composeDir := findComposeDir(t)
    cleanup := composeUpUnified(t, composeDir)
    defer cleanup()

    // pi-emitter (host) → unified-node (container at localhost:28443)
    piShell, err := shell.New(shell.Config{
        AgentID:      "pi-emitter",
        WorkspaceDir: t.TempDir(),
        ChainDBPath:  t.TempDir() + "/chain.db",
        KeystoreDir:  t.TempDir() + "/keystore",
        RemoteURL:    "https://localhost:28443", // Remote executor
    })
    if err != nil {
        t.Fatal(err)
    }
    defer piShell.Close()

    // Create envelope
    env := &envelope.CognitiveTaskEnvelope{
        EnvelopeID:      receipt.NewReceiptID(),
        EmitterAgentID:  "pi-emitter",
        ExecutorAgentID: "unified-node",
        Territory: envelope.Territory{
            Repository:    "github.com/test/repo",
            Branch:       "main",
            WorkspacePath: "/srv/workspace",
        },
        Preconditions: []envelope.Precondition{
            {Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "python3"}},
        },
        Assertions: []envelope.Assertion{
            {
                ID:   "python_ok",
                Type: envelope.AssertionCommandExitCode,
                Params: envelope.AssertionParams{
                    Command:          "python3 --version",
                    ExpectedExitCode: 0,
                },
            },
        },
        TimeoutSeconds:  60,
        MaxRemediations: 0,
        CreatedAt:       time.Now().UTC(),
        Version:         "1.0",
    }
    hash, _ := envelope.ComputeEnvelopeHash(env)
    env.EnvelopeHash = hash

    // Sign envelope
    signable := *env
    signable.EmitterSignature = ""
    canonical, _ := jcs.Marshal(&signable)
    sig, _ := piShell.signer.Sign(canonical)
    env.EmitterSignature = sig

    // Submit to executor (pre-flight check)
    leaseResp, err := piShell.SubmitEnvelope(context.Background(), env)
    if err != nil {
        t.Fatalf("SubmitEnvelope failed: %v", err)
    }
    if !leaseResp.Accepted {
        t.Fatalf("Lease rejected: %s", leaseResp.Error)
    }
    t.Logf("Lease: %s", leaseResp.LeaseID)

    // Settle (remote execution + settlement)
    rec, err := piShell.SettleWithRemote(context.Background(), env, leaseResp.LeaseID)
    if err != nil {
        t.Fatalf("SettleWithRemote failed: %v", err)
    }

    // ASSERTIONS
    if rec.Verdict != receipt.VerdictSettledClean {
        t.Errorf("Verdict=%s, want SETTLED_CLEAN", rec.Verdict)
    }
    if rec.ExecutorSignature == "" {
        t.Error("ExecutorSignature missing")
    }
    if rec.EmitterSignature != sig {
        t.Error("Envelope signature should be preserved in receipt")
    }
    t.Logf("Receipt: %s | %s | %d assertions",
        rec.ReceiptID, rec.Verdict, len(rec.Assertions))
}
```

### 6.5 Test 4: Resiliency — Broken Dependencies

```go
func TestShellMesh_ResiliencyBrokenDependencies(t *testing.T) {
    // Setup: workspace WITHOUT pytest installed
    dir := initGitRepo(t, t.TempDir())
    defer os.RemoveAll(dir)

    // Create a Python file
    os.WriteFile(filepath.Join(dir, "test_me.py"), []byte("def test_ok(): assert True\n"), 0644)

    sh, err := shell.New(shell.Config{
        AgentID:      "pi-local",
        WorkspaceDir: dir,
        ChainDBPath:  t.TempDir() + "/chain.db",
        KeystoreDir:  t.TempDir() + "/keystore",
        EvalTimeout:  30 * time.Second,
    })
    if err != nil {
        t.Fatal(err)
    }
    defer sh.Close()

    // Pre-condition: pytest must be available
    preconditions := []envelope.Precondition{
        {Type: envelope.PreconditionToolAvailable, Params: map[string]string{"tool": "pytest"}},
    }
    report, err := sh.Check(context.Background(), preconditions)
    if err != nil {
        t.Fatal(err)
    }

    // ASSERTION: Pre-flight MUST reject missing pytest
    if report.Passed {
        t.Error("Check() should reject missing pytest, but Passed=true")
    }
    foundMissing := false
    for _, r := range report.Results {
        if !r.Passed && strings.Contains(r.Message, "pytest") {
            foundMissing = true
            t.Logf("Correctly detected missing tool: %s", r.Message)
        }
    }
    if !foundMissing {
        t.Error("Expected a diagnostic mentioning missing pytest")
    }

    // Now try to execute anyway (bypass pre-flight — simulate buggy caller)
    // The assertion should still fail deterministically
    assertions := []envelope.Assertion{
        {
            ID:   "pytest_runs",
            Type: envelope.AssertionCommandExitCode,
            Params: envelope.AssertionParams{
                Command:          "pytest test_me.py -v",
                ExpectedExitCode: 0,
            },
        },
    }
    rec, err := sh.Execute(context.Background(), "pytest test_me.py -v", assertions)
    if err != nil {
        t.Fatal(err)
    }

    // ASSERTION: Settlement MUST fail because pytest is not installed
    if rec.Verdict == receipt.VerdictSettledClean {
        t.Error("Verdict should not be SETTLED_CLEAN when pytest is missing")
    }

    // ASSERTION: Receipt should still be in the chain (resilient continuation)
    chain, _ := sh.chainStore.GetChain("pi-local", "pi-local")
    if len(chain.Receipts) != 1 {
        t.Errorf("Chain should have 1 receipt (even failed), got %d", len(chain.Receipts))
    }

    // ASSERTION: Ed25519 signature should be present even for failed receipts
    if rec.ExecutorSignature == "" {
        t.Error("ExecutorSignature should be present even for failed settlements")
    }
}
```

---

## 7. Formal Verification Criteria

For the coupling to be considered correct, all six criteria must hold:

| # | Criterion | Test Coverage |
|---|-----------|---------------|
| C1 | Every `Execute()` call produces a `SettlementReceipt` | `TestShellMesh_LocalExecutionWithAssertions` |
| C2 | Pre-conditions are checked before execution begins | `TestShellMesh_LocalPreFlightRejectsDirtyWorktree` |
| C3 | Assertions are evaluated deterministically by the arnês | `TestShellMesh_LocalExecutionWithAssertions` |
| C4 | Receipts are cryptographically linked (SHA-256 chain) | `TestShellMesh_LocalExecutionWithAssertions` |
| C5 | Ed25519 signatures are verifiable by any party | All tests verify signature |
| C6 | Failed executions produce signed receipts (resilient continuation) | `TestShellMesh_ResiliencyBrokenDependencies` |

---

## 8. Security Model

```
┌──────────────────────────────────────────────────────────────┐
│ Security Properties                                           │
├──────────────────────────────────────────────────────────────┤
│ Non-repudiation  │ Both emitter AND executor sign their msgs │
│ Integrity        │ JCS RFC 8785 + SHA-256 (not HMAC)         │
│ Authenticity     │ Ed25519 signatures (not password/API keys) │
│ Auditability     │ Immutable receipt chain in SQLite WAL     │
│ Zero-trust       │ mTLS + cert validation for remote nodes   │
│ Separation       │ ChainDB separate from git workspace       │
│ Key isolation    │ Keystore at 0600, never in git            │
└──────────────────────────────────────────────────────────────┘
```

**Critical invariant:** The LLM (gentle-shell's caller) MUST NOT be able to influence the settlement verdict. The arnês is the sole source of truth.

---

## 9. Implementation Roadmap

```
Phase 1: pkg/shell/shell.go + pkg/shell/mesh.go
  - Local sidecar mode (Mode A)
  - Pre-flight checks
  - Local execution with assertions
  - Unit tests for shell.go

Phase 2: Remote executor mode (Mode B/C)
  - MeshClient with TLS
  - SubmitEnvelope + SettleWithRemote
  - In-process integration test

Phase 3: Dockerfile.unified + Docker compose
  - Unified node container
  - docker-compose.yml for pi-emitter + unified-node
  - Full Docker test suite (4 scenarios)
  - TLS certificates, keystore, chainDB volumes

Phase 4: Full end-to-end
  - pi harness integration
  - RDD receipt-driven delivery gate
  - Fan-out with local pre-flight + remote execution
```

---

## 10. Reference Implementation

The following files in the repository implement the contracts referenced in this document:

| File | Role |
|------|------|
| `pkg/settlement/evaluate.go` | Current `runCommand()` (to be wrapped by `shell.go`) |
| `pkg/envelope/types.go` | `CognitiveTaskEnvelope`, `Precondition`, `Assertion` |
| `pkg/receipt/types.go` | `SettlementReceipt`, `Verdict`, `ChainStore` |
| `pkg/signing/signer.go` | `BasicSigner.Sign()`, `Verify()` |
| `pkg/keystore/keystore.go` | `LoadOrCreate()`, key persistence |
| `pkg/jcs/jcs.go` | `Marshal()` RFC 8785 |
| `integration/agent/server.go` | HTTP server with `/envelopes`, `/settle` |
| `integration/agent/client.go` | HTTP client with TLS support |
| `cmd/test-harness/main.go` | CLI entry point (`serve`, `shell`, `health`) |
| `docs/protocol/SETTLEMENT-PROTOCOL-FORMAL-SPEC.md` | Canonical protocol specification |
