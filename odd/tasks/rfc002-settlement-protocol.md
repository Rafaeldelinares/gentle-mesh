# Feature: RFC-002 — Settlement Protocol (Cognitive Collaboration)

**Proyecto:** rfc002 / gentle-mesh  
**Inicio:** 2026-01-XX  
**Estado:** Activo  
**Model Bootstrapping:** A — Manual enrollment (v1)  
**Spec base:** RFC-002 completa (6 módulos, documento del usuario)  

---

## Meta

Evolucionar gentle-mesh de remote runner pasivo (RFC-001) a protocolo de colaboración bilateral con contratos firmados, liquidación determinista y receipts encadenados criptográficamente.

## Decisiones Clave Resueltas

- **Bootstrapping:** Modelo A — Enrollment manual de claves Ed25519 entre agentes antes de operar.
  Un agente genera su par localmente, exporta la pública a archivo, el otro la importa.
  Extensible a Agent Card dinámico en v2.
- **No subdelegación:** B ejecuta o rechaza; no puede delegar a C.
- **Aserciones:** Conjunto cerrado de 7 tipos. Sin DSL extensible.
- **JCS:** RFC 8785 para canonicalización antes de firma Ed25519.
- **Chain:** `previous_receipt_hash = SHA256(executor_signature_del_receipt_anterior)`.
- **Dispute:** Timeout unilateral después de N mensajes o tiempo. Sin tercero.
- **Docker:** Solo para sandbox de laboratorio, no para producción.

## Decisiones Pendientes (a resolver en implementación)

- [ ] Límite exacto de remediaciones (SETTLED_WITH_REPAIR): ¿2? ¿3?
- [ ] Timeout de dispute: ¿30 min? ¿configurable?
- [ ] Ubicación del keystore: archivo JSON por agente o integrados en DB?

## Arquitectura de Paquetes

```
gentle-mesh/
├── pkg/
│   ├── jcs/                    # RFC 8785 canonicalization
│   ├── envelope/               # CognitiveTaskEnvelope + hashing
│   ├── keystore/               # Agent key storage (Ed25519)
│   ├── receipt/                # SettlementReceipt, chain, signing
│   ├── settlement/             # Settlement DSL evaluator
│   └── handshake/              # Pre-flight resource validation
├── internal/
│   └── api/                    # HTTP endpoints (nuevos)
│       └── settlement.go       # Settlement, receipts, chain
└── cmd/
    └── agent/main.go            # Servicio daemon A2A + API
```

## Estados del Contrato

```
PROPOSED → ACCEPTED → EXECUTING → SETTLING → REMEDIATING → SETTLED → ACCEPTED_FINAL
    │
    ├──> REJECTED (Pre-flight fail)
    ├──> EXPIRED (Lease TTL)
    ├──> SETTLEMENT_FAILED
    ├──> DISPUTED
    └──> ABANDONED
```

## Estados del Receipt

```
EMITTED → ACCEPTED (inmutable)
         → DISPUTED → RESOLVED (inmutable) / STALE
         → STALE (timeout)
```

## Aserciones (Settlement DSL)

| Tipo | Descripción |
|------|-------------|
| `file_modified` | Hash SHA-256 cambió vs. baseline |
| `file_hash_equals` | SHA-256 igual a valor esperado |
| `command_exit_code` | Exit code de comando == 0 |
| `command_output_contains` | Stdout/stderr contiene patrón |
| `git_clean_worktree` | Git worktree sin cambios sin commit |
| `port_available` | Puerto TCP libre |
| `no_regression` | Suite de tests pasa |

## Veredictos

- `SETTLED_CLEAN` — Éxito en primera evaluación
- `SETTLED_WITH_REPAIR` — Éxito tras remediación acotada
- `SETTLEMENT_FAILED` — Aserciones no cumplidas
- `SETTLEMENT_DISPUTED` — A no convalida
- `SETTLEMENT_TIMEOUT` — Ventana de ejecución agotada

---

## Work Units (en orden de implementación)

| 1 | `jcs-canonicalization` | RFC 8785: order keys, serialize, hash | `323abf9` |
| 2 | `envelope-types` | CognitiveTaskEnvelope struct, validation, JCS hash | `fe32e2e` |
| 3 | `envelope-signing` | Ed25519 sign/verify envelopes | `bdddf04` |
| 4 | `keystore` | Agent key storage, import/export PEM, key lookup | `e6ca38f` |
| 5 | `receipt-types` | SettlementReceipt struct, verdict types | `e8fd993` |
| 6 | `receipt-chain` | Encadenamiento con previous_hash | ¿? |
| 7 | `receipt-signing` | Firmar receipt, verificar firma | ¿? |
| 8 | `settlement-dsl` | 7 aserciones, evaluador determinista | ¿? |
| 9 | `settlement-engine` | Motor: ejecutar aserciones, emitir veredicto | ¿? |
| 10 | `handshake` | Pre-flight validation de precondiciones | ¿? |
| 11 | `executor-a2a` | Receive envelope, decide, ejecutar, liquidar | ¿? |
| 12 | `api-settlement` | Endpoints HTTP: POST /contracts, GET /receipts, etc. | ¿? |
| 13 | `integration-tests` | Tests end-to-end del flujo completo | `4ca8b9a` |
| 14 | `accept-e2e` | POST /accept, AcceptReceipt, full cryptographic loop | `f23b342` |
| 15 | `fanout-e2e` | Fan-out E2E: parallel dispatch + accept to multiple executors | `5ec8dd8` |

---

## Progreso

- [x] Spec analizada
- [x] Decisiones clave resueltas (bootstrapping A)
- [x] Work unit 1: JCS canonicalization
- [x] Work unit 2: Envelope types
- [x] Work unit 3: Envelope signing
- [x] Work unit 4: Keystore
- [x] Work unit 5: Receipt types
- [x] Work unit 6: Receipt chain (`66fd60f`) — chain linkage, verification, SQLite persistence
- [x] Work unit 7: Receipt signing (`4866053`) — SignReceipt, AcceptReceipt, DisputeReceipt, verification
- [x] Work unit 8: Settlement DSL (`ba8fe52`) — 7 assertion types, parser, evaluator, 83.3% coverage
- [x] Work unit 9: Settlement engine (`8396a3a`) — execute assertions, emit verdict, 85.3% coverage
- [x] Work unit 10: Integration test infrastructure (`979d60b`) — cmd/test-harness, formal spec
- [x] Work unit 11: Phases 1-4 negative/chain/fuzz tests (`fa79975`) — chain tampering, parser fuzzing
- [x] Work unit 12: TLS/HTTPS transport (`3c5b7de`) — mTLS, strong cipher suites, HTTP API
- [x] Work unit 13: Distributed Docker fan-out tests (`4ca8b9a`+`065b023`) — 5 tests, Bug #45 fixed
- [x] Work unit 14: E2E AcceptReceipt test (`f23b342`) — POST /accept endpoint, full cryptographic loop
- [x] Work unit 15: Fan-out E2E with acceptance (`5ec8dd8`) — parallel dispatch + accept to B and C, chain isolation
