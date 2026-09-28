# Phase 1: S2, R7, S7, S9, S6-partial

**RFC-002 v2 Phase 1 — Cerrar lo explotable**
**Parent branch:** `master`

## Secuencia de commits (work units)

### WU1: S2 — Verify EmitterSignature in handleSubmitEnvelope
**Archivo:** `integration/agent/server.go`
**Cambio:** Verificar `EmitterSignature` ANTES de `runPreconditions`.

```
handleSubmitEnvelope:
  1. Parse EnvelopeRequest (json)
  2. Verificar EmitterSignature contra KnownAgents[env.EmitterAgentID]
     → si falla: 401, return
  3. Validate envelope
  4. runPreconditions
  5. ... resto igual
```

**Server.Config** necesita `KnownAgents map[string][]byte` (agentID → pubkey).
El emisor registered se carga desde keystore o config al arranque.

### WU2: S2 — Verify EmitterSignature in handleAccept
**Archivo:** `integration/agent/server.go`
**Cambio:** Después de verificar ExecutorSignature (stored), verificar EmitterSignature
sobre el message que A firmó (AcceptReceipt local).

```
handleAccept:
  1. Verificar ExecutorSignature (stored) — YA EXISTE
  2. Reconstruir acceptance message = EmitterAgentID + ReceiptID + "accepted"
  3. Verificar EmitterSignature sobre ese message, usando KnownAgents o stored pubkey
     → si falla: 401, return
  4. ... resto igual
```

### WU3: S2 — Verify EmitterSignature in handleDispute
**Archivo:** `integration/agent/server.go`
**Igual que WU2 pero para dispute message.**

### WU4: S2 — Verify signatures in handleSettle
**Archivo:** `integration/agent/server.go`
**Cambio:** Verificar EmitterSignature en el envelope antes de Settle.

```
handleSettle:
  1. Parse SettleRequest (json)
  2. Verificar EmitterSignature sobre EnvelopeJSON
     → si falla: 401, return
  3. ... resto igual
```

### WU5: S2 — Tests adversarios
**Archivo:** `integration/agent/signature_test.go` (nuevo)
**Tests:**
- Clave ajena firmando → 401
- Emisor no registrado (pubkey unknown) → 401
- Contenido alterado post-firma → 401
- Timestamp modificado post-firma → 401
- Cada endpoint: submit, settle, accept, dispute

### WU6: R7 — Deterministic signatures (Bug #48 fix)
**Archivos:** `pkg/receipt/receipt.go`, `pkg/envelope/envelope.go`
**Cambio:** RFC 3339 UTC en todos los campos de tiempo, precisión fija.

```
Receipt:
  - CreatedAt: time.RFC3339Nano → time.RFC3339
  - SettledAt: time.RFC3339Nano → time.RFC3339
  - ExecutorSignedAt: time.RFC3339Nano → time.RFC3339
  - EmitterAcceptanceAt: time.RFC3339Nano → time.RFC3339

Envelope:
  - CreatedAt: time.RFC3339Nano → time.RFC3339
  - ExpiresAt: time.RFC3339Nano → time.RFC3339

Lease:
  - IssuedAt: time.RFC3339Nano → time.RFC3339
  - ExpiresAt: time.RFC3339Nano → time.RFC3339
```

Test: 1000 iteraciones de sign → serialize → SQLite → read → verify con `-race`, 0 fallos.

### WU7: S7 — prev_hash integrity inside mutex
**Archivo:** `pkg/receipt/chain.go`
**Cambio:** `SaveReceipt`valida `prev_hash` dentro del mutex.

```go
func (s *ChainStore) SaveReceipt(ctx context.Context, r *SettlementReceipt) error {
    s.mu.Lock()
    defer s.mu.Unlock()

    // Verify prev_hash matches latest before writing
    latest, err := s.getLatest(ctx)
    if err != nil && !errors.Is(err, ErrReceiptNotFound) {
        return err
    }
    if latest != nil && r.PrevReceiptHash != latest.ReceiptHash {
        return ErrChainBroken
    }

    return s.saveReceipt(ctx, r)
}
```

### WU8: S9 — protocol_version enforcement
**Archivos:** `pkg/envelope/types.go`, `integration/agent/server.go`
**Cambio:**
1. `CognitiveTaskEnvelope` recibe `ProtocolVersion string` (default "1")
2. `Validate()` rechaza versión desconocida
3. `handleSubmitEnvelope`, `handleSettle` verifican versión antes de procesar

### WU9: S6-partial — DisallowUnknownFields
**Archivos:** `integration/agent/server.go`
**Cambio:** Todos los `json.Decoder` usan `DisallowUnknownFields()`.

## Non-goals (not in Phase 1)
- S6 completo (eliminar exec sh -c) → Phase 2
- S3, S4 (policy.yaml) → Phase 2
- R1-R6 robustness → Phase 2
- THREAT-MODEL.md → Phase 1 docs only
