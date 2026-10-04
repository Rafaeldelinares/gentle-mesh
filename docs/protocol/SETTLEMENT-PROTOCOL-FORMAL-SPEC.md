# Settlement Protocol — Formal Specification

> **RFC-002 §3 — Contrato Cognitivo y Liquidación Determinista**
> Esta especificación es canónica. El código en `pkg/envelope`, `pkg/settlement`,
> `pkg/receipt` es la implementación de referencia.

---

## 1. Documentos del Protocolo

El protocolo define **tres documentos JSON** con roles distintos:

| Documento | Emisor | Receptor | Propósito |
|---|---|---|---|
| `CognitiveTaskEnvelope` | Agente A (emisor) | Agente B (ejecutor) | Contrato firmable |
| `SettlementReceipt` | Agente B (ejecutor) | Agente A (emisor) | Veredicto firmable |
| `Lease` | Agente B (ejecutor) | Agente A (emisor) | Compromiso pre-flight |

---

## 2. Notación

- `string` — UTF-8, sin nulos embebidos.
- `int` — entero con signo de 64 bits.
- `bool` — `true` o `false` (JSON).
- `time.Time` — ISO 8601 / RFC 3339 en JSON (`"2026-01-01T12:00:00Z"`).
- SHA-256 — 64 caracteres hex lowercase.
- UUIDv7 — string de 36 caracteres (ej. `"0192de5f-7c01-8000-b000-000000000001"`).
- Ed25519 — firma Base64URL sin padding (ej. `"zaRgbabIlrctStvKzl8u..."`).
- `omitempty` — campo omitido si es valor zero/empty del tipo Go.
- JCS — JSON Canonicalization Scheme (RFC 8785): ordena claves UTF-16, número ECMAScript.

---

## 3. CognitiveTaskEnvelope

### 3.1 Gramática EBNF

```ebnf
Envelope        ::= "{"
                    '"envelope_id"' ":" NonEmptyString ","
                    '"emitter_agent_id"' ":" NonEmptyString ","
                    '"executor_agent_id"' ":" NonEmptyString ","
                    '"territory"' ":" Territory ","
                    '"preconditions"' ":" "[" PreconditionList "]" ","
                    '"assertions"' ":" "[" AssertionList "]" ","
                    '"timeout_seconds"' ":" PositiveInt ","
                    '"max_remediations"' ":" NonNegativeInt ","
                    '"no_subdelegation"' ":" Bool ","
                    '"created_at"' ":" ISODateTime ","
                    '"version"' ":" '"1.0"' ","
                    '"emitter_signature"' ":" Base64URLString ","
                    '"envelope_hash"' ":" Hex64String
                    "}"

Territory       ::= "{" '"repository"' ":" NonEmptyString ","
                       '"branch"' ":" NonEmptyString ","
                       '"workspace_path"' ":" AbsolutePath
                       "}"

PreconditionList ::= "" | Precondition ("," Precondition)*

Precondition    ::= "{" '"type"' ":" PreconditionType ","
                       '"params"' ":" "{" (KeyValue ("," KeyValue)*)? "}"
                       "}"

PreconditionType ::= '"command_exit_code"' | '"git_clean_worktree"'
                   | '"port_available"' | '"tool_available"'

AssertionList   ::= Assertion ("," Assertion)*

Assertion       ::= "{" '"id"' ":" NonEmptyString ","
                       '"type"' ":" AssertionType ","
                       '"params"' ":" AssertionParams
                       "}"

AssertionType   ::= '"file_modified"' | '"file_hash_equals"'
                 | '"command_exit_code"' | '"command_output_contains"'
                 | '"git_clean_worktree"' | '"port_available"'
                 | '"no_regression"'

AssertionParams ::= "{" (ParamPair ("," ParamPair)*)? "}"

ParamPair       ::= Key ":" Value

Key             ::= '"description"' | '"file_path"' | '"expected_sha256"'
                 | '"command"' | '"expected_exit_code"' | '"contains_pattern"'
                 | '"contains_regex"' | '"working_dir"' | '"timeout_seconds"'
                 | '"port"'

Value           ::= NonEmptyString | Integer | Bool | "[" StringList "]"

StringList      ::= "" | NonEmptyString ("," NonEmptyString)*
```

### 3.2 Campos — CognitiveTaskEnvelope

| Campo | Tipo | Obligatorio | Semántica |
|---|---|---|---|
| `envelope_id` | `string` | **Sí** | UUIDv7 que identifica el contrato. |
| `emitter_agent_id` | `string` | **Sí** | ID del agente A (creador). |
| `executor_agent_id` | `string` | **Sí** | ID del agente B (destinatario). `≠ emitter_agent_id`. |
| `territory` | `Territory` | **Sí** | Contexto de ejecución. |
| `preconditions` | `[]Precondition` | No | Invariantes del pre-flight. |
| `assertions` | `[]Assertion` | **Sí** | Criterios de liquidación (≥1). |
| `timeout_seconds` | `int` | **Sí** | Ventana máxima. > 0. |
| `max_remediations` | `int` | **Sí** | Reparaciones permitidas. ≥ 0. |
| `no_subdelegation` | `bool` | **Sí** | Siempre `true` en v1. |
| `created_at` | `string (ISO 8601)` | **Sí** | Época de creación. |
| `version` | `string` | **Sí** | Valor `"1.0"`. |
| `emitter_signature` | `string` | **Sí\*** | Firma Ed25519 de A (ver §5). |
| `envelope_hash` | `string` | **Sí\*** | Hash JCS de A (ver §5). |

\* Requiere que el emisor haya completado la firma.

### 3.3 Territorio

| Campo | Tipo | Semántica |
|---|---|---|
| `repository` | `string` | URI canónica del repo Git. Normalizado: sin trailing `/` ni sufijo `.git`. |
| `branch` | `string` | Branch o ref de Git. Sin espacios ni caracteres de control. |
| `workspace_path` | `string` | **Ruta absoluta** en el host del ejecutor. |

### 3.4 Aserciones — Gramática del Parser

```bnf
<assertion> ::= <id> ":" <type> ":" <params>
<id>        ::= [^:,]+
<type>      ::= "file_modified" | "file_hash_equals" | "command_exit_code"
              | "command_output_contains" | "git_clean_worktree"
              | "port_available" | "no_regression"
<params>    ::= (<pair> ("," <pair>)*)?
<pair>      ::= <key> "=" <value>
<key>       ::= [a-z_]+
<value>     ::= [^,"']+ | "'" [^']* "'" | '"' [^"]* '"'
```

**Ejemplos:**
```
tests_passing:command_exit_code:command=go test ./...,expected_code=0
readme_updated:file_modified:path=README.md,expected_sha256=a948904f2f0f479b8f8564cbf12dae62c683b2a5f1677e1af51a92d4ed30a89f
output_ok:command_output_contains:command=go test -v,contains=ok
clean:git_clean_worktree:cwd=/home/user/project
port_free:port_available:port=8080
no_regression:no_regression:command=make test
```

### 3.5 Reglas de Validación Semántica

```
file_modified:
  • file_path: REQUERIDO
  • expected_sha256: REQUERIDO, 64 hex chars

file_hash_equals:
  • file_path: REQUERIDO
  • expected_sha256: REQUERIDO, 64 hex chars

command_exit_code:
  • command: REQUERIDO
  • expected_code: REQUERIDO (cualquier entero, incluyendo 0)

command_output_contains:
  • command: REQUERIDO
  • contains_pattern: REQUERIDO
  • contains_regex: bool, default false
    → si true, debe ser regex Go válido

git_clean_worktree:
  • Sin parámetros requeridos.

port_available:
  • port: REQUERIDO, entero 1–65535.

no_regression:
  • command: REQUERIDO.
```

---

## 4. SettlementReceipt

### 4.1 Gramática EBNF

```ebnf
Receipt         ::= "{"
                    '"receipt_id"' ":" NonEmptyString ","
                    '"contract_id"' ":" NonEmptyString ","
                    '"envelope_hash"' ":" Hex64String ","
                    '"emitter_agent_id"' ":" NonEmptyString ","
                    '"executor_agent_id"' ":" NonEmptyString ","
                    '"territory"' ":" Territory ","
                    '"verdict"' ":" Verdict ","
                    '"assertions"' ":" "[" AssertionResultList "]" ","
                    '"remidiation_chain"' ":" "[" RemediationList "]"? ","
                    '"executor_signature"' ":" Base64URLString ","
                    '"executor_signed_at"' ":" ISODateTime ","
                    '"previous_receipt_hash"' ":" Hex64String ","
                    '"emitter_acceptance"' ":" Acceptance? ","
                    '"emitter_acceptance_at"' ":" ISODateTime? ","
                    '"dispute_reason"' ":" String? ","
                    '"emitter_signature"' ":" Base64URLString?
                    "}"

Verdict         ::= '"SETTLED_CLEAN"' | '"SETTLED_WITH_REPAIR"'
                 | '"SETTLEMENT_FAILED"' | '"SETTLEMENT_DISPUTED"'
                 | '"SETTLEMENT_TIMEOUT"'

Acceptance      ::= '"ACCEPTED"' | '"DISPUTED"'

AssertionResultList ::= AssertionResult ("," AssertionResult)*

AssertionResult ::= "{" '"assertion_index"' ":" int ","
                       '"assertion_id"' ":" NonEmptyString ","
                       '"assertion_type"' ":" NonEmptyString ","
                       '"result"' ":" Result ","
                       '"evidence"' ":" Evidence ","
                       '"message"' ":" String?
                       "}"

Result          ::= '"PASS"' | '"FAIL"' | '"SKIP"'

Evidence        ::= "{" ("'"command'"'" ":" String)? ","
                       ("'"exit_code'"'" ":" int)? ","
                       ("'"stdout_hash'"'" ":" Hex64String)? ","
                       ("'"stderr_hash'"'" ":" Hex64String)? ","
                       ("'"file_path'"'" ":" String)? ","
                       ("'"expected_sha256'"'" ":" Hex64String)? ","
                       ("'"actual_sha256'"'" ":" Hex64String)? ","
                       ("'"file_was_modified'"'" ":" Bool)? ","
                       ("'"git_status'"'" ":" String)? ","
                       ("'"port'"'" ":" int)? ","
                       ("'"port_was_free'"'" ":" Bool)? ","
                       '"'"checked_at'"'" ":" ISODateTime
                       "}"

RemediationList ::= RemediationAttempt ("," RemediationAttempt)*

RemediationAttempt ::= "{" '"'"attempt_index'"'" ":" int ","
                           '"'"triggered_by_assertion_id'"'" ":" String ","
                           '"'"action'"'" ":" String ","
                           '"'"command'"'" ":" String? ","
                           '"'"exit_code'"'" ":" int ","
                           '"'"assertions_rechecked'"'" ":" "[" StringList "]"? ","
                           '"'"result'"'" ":" '"'"SUCCESS'"'" | '"'"FAILURE'"'" ","
                           '"'"message'"'" ":" String? ","
                           '"'"attempted_at'"'" ":" ISODateTime
                           "}"
```

### 4.2 Campos — SettlementReceipt

| Campo | Tipo | Semántica |
|---|---|---|
| `receipt_id` | `string` | UUIDv7 del receipt. |
| `contract_id` | `string` | Copia de `envelope_id`. |
| `envelope_hash` | `string` | SHA-256 del envelope original (JCS). |
| `emitter_agent_id` | `string` | ID del agente A. |
| `executor_agent_id` | `string` | ID del agente B. |
| `territory` | `Territory` | Copia del envelope original. |
| `verdict` | `Verdict` | Resultado de la liquidación. |
| `assertions` | `[]AssertionResult` | Resultado de cada aserción. |
| `remidiation_chain` | `[]RemediationAttempt` | Solo si `verdict = SETTLED_WITH_REPAIR`. |
| `executor_signature` | `string` | Firma Ed25519 de B (ver §5). |
| `executor_signed_at` | `string (ISO 8601)` | Época de firma de B. |
| `previous_receipt_hash` | `string` | SHA-256(`executor_signature` del receipt anterior). Vacío para el primero. |
| `emitter_acceptance` | `string` | `"ACCEPTED"` \| `"DISPUTED"`. |
| `emitter_acceptance_at` | `string` | Época de aceptación de A. |
| `dispute_reason` | `string` | Razón de disputa (si aplica). |
| `emitter_signature` | `string` | Firma Ed25519 de A (ver §5). |

### 4.3 Veredictos

```
SETTLED_CLEAN        — Todas las aserciones pasaron en la primera evaluación.
SETTLED_WITH_REPAIR  — Todas las aserciones pasaron tras ≥1 reparación.
SETTLEMENT_FAILED    — Aserciones fallaron y no quedan remediaciones.
SETTLEMENT_DISPUTED  — El emisor rechazó formalmente el receipt.
SETTLEMENT_TIMEOUT   — La ventana del contrato venció antes de liquidar.
```

### 4.4 Resultados por Aserción

```
PASS — La condición de la aserción se cumplió.
FAIL — La condición no se cumplió.
SKIP — La aserción no fue evaluada (ej. timeout).
```

---

## 5. Lease

```json
{
  "lease_id":         "uuid-v7",
  "envelope_id":      "envelope_id del contrato",
  "executor_agent_id":"id-del-ejecutor",
  "accepted":         true | false,
  "precondition_results": [
    {
      "precondition_index": 0,
      "type": "command_exit_code",
      "passed": true | false,
      "message": "diagnóstico opcional"
    }
  ],
  "rejection_reason": "string (si accepted=false)",
  "expires_at":       "ISO 8601",
  "executor_signature":"Ed25519 Base64URL"
}
```

---

## 6. Esquemas JSON (RFC 8785 — JCS)

### 6.1 CognitiveTaskEnvelope

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "CognitiveTaskEnvelope",
  "description": "Contrato cognitivo entre agente A (emisor) y B (ejecutor).",
  "type": "object",
  "required": [
    "envelope_id", "emitter_agent_id", "executor_agent_id",
    "territory", "assertions", "timeout_seconds", "max_remediations",
    "no_subdelegation", "created_at", "version"
  ],
  "additionalProperties": false,
  "properties": {
    "envelope_id": {
      "type": "string", "pattern": "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"
    },
    "emitter_agent_id": { "type": "string", "minLength": 1 },
    "executor_agent_id": { "type": "string", "minLength": 1 },
    "territory": { "$ref": "#/definitions/Territory" },
    "preconditions": {
      "type": "array",
      "items": { "$ref": "#/definitions/Precondition" }
    },
    "assertions": {
      "type": "array",
      "minItems": 1,
      "items": { "$ref": "#/definitions/Assertion" }
    },
    "timeout_seconds": { "type": "integer", "exclusiveMinimum": 0 },
    "max_remediations": { "type": "integer", "minimum": 0 },
    "no_subdelegation": { "type": "boolean" },
    "created_at": { "type": "string", "format": "date-time" },
    "emitter_signature": { "type": "string" },
    "envelope_hash": { "type": "string", "pattern": "^[0-9a-f]{64}$" },
    "version": { "type": "string", "const": "1.0" }
  },
  "definitions": {
    "Territory": {
      "type": "object",
      "required": ["repository", "branch", "workspace_path"],
      "additionalProperties": false,
      "properties": {
        "repository":    { "type": "string", "minLength": 1 },
        "branch":       { "type": "string", "minLength": 1 },
        "workspace_path": { "type": "string", "pattern": "^/" }
      }
    },
    "Precondition": {
      "type": "object",
      "required": ["type", "params"],
      "additionalProperties": false,
      "properties": {
        "type": {
          "type": "string",
          "enum": ["command_exit_code", "git_clean_worktree", "port_available", "tool_available"]
        },
        "params": { "type": "object" }
      }
    },
    "Assertion": {
      "type": "object",
      "required": ["id", "type", "params"],
      "additionalProperties": false,
      "properties": {
        "id":     { "type": "string", "minLength": 1 },
        "type":   { "$ref": "#/definitions/AssertionType" },
        "params": { "$ref": "#/definitions/AssertionParams" }
      }
    },
    "AssertionType": {
      "type": "string",
      "enum": [
        "file_modified", "file_hash_equals",
        "command_exit_code", "command_output_contains",
        "git_clean_worktree", "port_available",
        "no_regression"
      ]
    },
    "AssertionParams": {
      "type": "object",
      "additionalProperties": true
    }
  }
}
```

### 6.2 SettlementReceipt

```json
{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "SettlementReceipt",
  "description": "Registro verificable de liquidación.",
  "type": "object",
  "required": [
    "receipt_id", "contract_id", "envelope_hash",
    "emitter_agent_id", "executor_agent_id", "territory",
    "verdict", "assertions", "executor_signature",
    "executor_signed_at", "previous_receipt_hash"
  ],
  "additionalProperties": false,
  "properties": {
    "receipt_id": {
      "type": "string",
      "pattern": "^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$"
    },
    "contract_id": { "type": "string" },
    "envelope_hash": { "type": "string", "pattern": "^[0-9a-f]{64}$" },
    "emitter_agent_id": { "type": "string" },
    "executor_agent_id": { "type": "string" },
    "territory": { "$ref": "#/definitions/Territory" },
    "verdict": {
      "type": "string",
      "enum": ["SETTLED_CLEAN", "SETTLED_WITH_REPAIR", "SETTLEMENT_FAILED",
               "SETTLEMENT_DISPUTED", "SETTLEMENT_TIMEOUT"]
    },
    "assertions": {
      "type": "array",
      "items": { "$ref": "#/definitions/AssertionResult" }
    },
    "remediation_chain": {
      "type": "array",
      "items": { "$ref": "#/definitions/RemediationAttempt" }
    },
    "executor_signature": { "type": "string" },
    "executor_signed_at": { "type": "string", "format": "date-time" },
    "previous_receipt_hash": { "type": "string", "pattern": "^[0-9a-f]{64}$" },
    "emitter_acceptance": { "type": "string", "enum": ["ACCEPTED", "DISPUTED"] },
    "emitter_acceptance_at": { "type": "string", "format": "date-time" },
    "dispute_reason": { "type": "string" },
    "emitter_signature": { "type": "string" }
  },
  "definitions": {
    "Territory": {
      "type": "object",
      "required": ["repository", "branch", "workspace_path"],
      "properties": {
        "repository":    { "type": "string" },
        "branch":       { "type": "string" },
        "workspace_path": { "type": "string" }
      }
    },
    "AssertionResult": {
      "type": "object",
      "required": ["assertion_index", "assertion_id", "assertion_type", "result", "evidence"],
      "properties": {
        "assertion_index": { "type": "integer" },
        "assertion_id":   { "type": "string" },
        "assertion_type": { "type": "string" },
        "result":         { "type": "string", "enum": ["PASS", "FAIL", "SKIP"] },
        "evidence":       { "$ref": "#/definitions/Evidence" },
        "message":        { "type": "string" }
      }
    },
    "Evidence": {
      "type": "object",
      "required": ["checked_at"],
      "properties": {
        "command":           { "type": "string" },
        "exit_code":        { "type": "integer" },
        "stdout_hash":      { "type": "string" },
        "stderr_hash":      { "type": "string" },
        "file_path":        { "type": "string" },
        "expected_sha256":  { "type": "string" },
        "actual_sha256":    { "type": "string" },
        "file_was_modified":{ "type": "boolean" },
        "git_status":       { "type": "string" },
        "port":             { "type": "integer" },
        "port_was_free":    { "type": "boolean" },
        "checked_at":       { "type": "string", "format": "date-time" }
      }
    },
    "RemediationAttempt": {
      "type": "object",
      "required": ["attempt_index", "triggered_by_assertion_id", "action", "result", "attempted_at"],
      "properties": {
        "attempt_index":            { "type": "integer" },
        "triggered_by_assertion_id": { "type": "string" },
        "action":                   { "type": "string" },
        "command":                  { "type": "string" },
        "exit_code":                { "type": "integer" },
        "assertions_rechecked":     { "type": "array", "items": { "type": "string" } },
        "result":                   { "type": "string", "enum": ["SUCCESS", "FAILURE"] },
        "message":                  { "type": "string" },
        "attempted_at":             { "type": "string", "format": "date-time" }
      }
    }
  }
}
```

---

## 7. Canonicalización y Firmas (RFC 8785)

### 7.1 Contenido Firmable

Antes de firmar, se produce una copia del documento con los campos de firma
establecidos en el string vacío `""`. Los campos omitidos (`omitempty`) no
aparecen en el JSON.

#### Envelope
```json
{
  "envelope_id":"0192de5f-7c01-8000-b000-000000000001",
  "emitter_agent_id":"agent-a",
  "executor_agent_id":"agent-b",
  "territory":{
    "repository":"github.com/gentleman-programming/gentle-mesh",
    "branch":"main",
    "workspace_path":"/srv/workspace"
  },
  "preconditions":[],
  "assertions":[...],
  "timeout_seconds":300,
  "max_remediations":2,
  "no_subdelegation":true,
  "created_at":"2026-01-01T12:00:00Z",
  "version":"1.0"
}
```

**Nota:** `emitter_signature` y `envelope_hash` NO aparecen (vacíos tras la copia).

#### Receipt (para firma del ejecutor)
```json
{
  "receipt_id":"0192de5f-7c01-8000-c000-000000000002",
  "contract_id":"0192de5f-7c01-8000-b000-000000000001",
  "envelope_hash":"e3b0c44298fc1c149afbf4c8996fb924...",
  "emitter_agent_id":"agent-a",
  "executor_agent_id":"agent-b",
  "territory":{...},
  "verdict":"SETTLED_CLEAN",
  "assertions":[...],
  "executor_signed_at":"2026-01-01T12:05:00Z",
  "previous_receipt_hash":"..."
}
```

**Nota:** `executor_signature`, `emitter_acceptance`, `emitter_acceptance_at`,
`dispute_reason`, `emitter_signature` NO aparecen.

### 7.2 Pipeline de Hash y Firma

```
Documento Go
  → Marshal (jcs.Marshal): JSON canónico RFC 8785
  → Hash (jcs.HashHex): SHA-256 del JSON canónico → hex64
  → Sign (Ed25519.Sign): Firma Ed25519 sobre []byte(hash)
  → Base64URLEncode → executor_signature
```

### 7.3 Chain Link — PreviousReceiptHash

```
previous_receipt_hash = SHA-256([]byte(executor_signature))
                      = hex(sha256.Sum256([]byte(base64signature)))
```

**No se usa JCS aquí.** La firma raw (base64) no es JSON válido; JCS fallaría
con `"invalid JSON"`. SHA-256 directo sobre los bytes de la firma es la
operación correcta.

### 7.4 Verificación de Firmas

```
1. ComputeReceiptHash(receipt)  → clear sigs → jcs.Marshal → sha256 → hex
2. executor.Sign(hex_bytes)     → firma
3. Verify(sig, pubkey, hex_bytes) → bool
```

### 7.5 Precisión Numérica y Claves Duplicadas (RFC 8785 & RFC 7493 I-JSON)

De acuerdo con RFC 8785 (§3.2.2.3) y RFC 7493:
- **Claves Duplicadas:** Quedan estrictamente prohibidas en cualquier nivel de anidamiento de objetos JSON. Cualquier documento con claves duplicadas es rechazado con error inmediato.
- **Precisión de Enteros:** El modelo numérico de ECMAScript / RFC 8785 se basa en punto flotante IEEE 754 de doble precisión (64 bits). Los números enteros cuyo valor absoluto sea superior a **2^53** (`9,007,199,254,740,992`) pierden precisión al ser canonicalizados. Por tanto, en los contratos de RFC-002:
  - Los campos enteros actuales (`timeout_seconds`, `max_remediations`, `expected_exit_code`, `port`, `assertion_index`, `attempt_index`, `exit_code`) operan dentro del rango de enteros estándar (<< 2^53).
  - Si futuras extensiones del protocolo requieren identificadores de 64 bits o marcas temporales en nanosegundos como enteros de 64 bits, **deben ser codificados como cadenas de texto (`string`)** o formateados según RFC 3339 (como se hace con `created_at`, `executor_signed_at`).
- **Codificación Unicode:** Las cadenas deben ser UTF-8 válido y no pueden contener sustitutos huérfanos (*lone surrogates*, `U+D800` a `U+DFFF`) ni caracteres de control sin escapar.

### 7.6 Known divergences (spec ↔ implementation)

Estado a la fecha de esta revisión. Cada divergencia está rastreada en un issue; **no** se considera un comportamiento deseable ni definitivo.

| ID | Qué se firma | La spec dice (§) | La implementación hace | Issue |
|---|---|---|---|---|
| **D1** | `previous_receipt_hash` (ejecutor) | **Incluido** en el contenido firmable del ejecutor (línea 556) | **Excluido**: `ComputeReceiptHash` lo pone a cero | **#43** |
| **D2** | `sequence_number` (ejecutor) | No listado | Excluido; `SaveReceipt` lo asigna **después** de firmar | **#43** |
| **D3** | `protocol_version` | No listado | Firmado (tag sin `omitempty`) | (a documentar) |
| **D4** | `mesh_id` | No listado | Firmado (tag sin `omitempty`) | (a documentar) |
| **D5** | `remediation_chain` | No listado | Firmado cuando no está vacío | (a documentar) |
| **D6** | Contenido firmable del **emisor** | **No definido en ningún apartado de §7** | Firma el mismo payload que el ejecutor → **no cubre decisión, motivo ni fecha**; la contrafirma se persiste sin verificar | **#48** |
| **D7** | §7.4 `executor.Sign(hex_bytes)` | Ambiguo: ¿bytes del hex o string hex? | Firma el **string hex** de 64 caracteres ASCII | **#38** |
| **D8** | Esquema recibo vs envelope/lease | §7.2 define el del recibo (hash hex) | El recibo firma el hex; envelope/lease firman **bytes JCS** | **#38** |

**Nota sobre verificabilidad:** D1 y D6 no son matices de redacción: `VerifyChainIntegrity` devuelve `Valid=true` en cadenas reescritas y en decisiones invertidas, respectivamente. La resolución propuesta es un esquema de firma **v2** (versión dentro de lo firmado, bytes JCS, `prev` incluido y payload propio del emisor), manteniendo la verificación **v1** para los recibos históricos, que no se pueden re-firmar.

---

## 8. Estado del Contrato

```
┌─────────────┐
│  PROPOSED   │ ← Envelope creado por A
└──────┬──────┘
       │ Pre-flight handshake OK
       ▼
┌─────────────┐
│  ACCEPTED   │ ← Lease concedido por B
└──────┬──────┘
       │ B inicia ejecución
       ▼
┌─────────────┐
│ EXECUTING   │ ← LLM trabaja
└──────┬──────┘
       │ Termina la ejecución
       ▼
┌─────────────┐
│  SETTLING   │ ← Motor evalúa aserciones
└──────┬──────┘
       │ 1. Todas PASS → Receipt.emit()
       │ 2. Fallan + remediaciones > 0 → REMEDIATING
       │ 3. Fallan + remediaciones = 0 → SETTLEMENT_FAILED
       ▼
┌─────────────┐     ┌──────────────┐     ┌────────────────┐
│  SETTLED    │ ←── │SETTLING_FAILED│     │  REMEDIATING  │
└──────┬──────┘     └──────────────┘     └───────┬────────┘
       │ A acepta                               │ remediate()
       ▼                                         ▼
┌──────────────┐     ┌──────────────┐     ┌────────────┐
│ACCEPTED_FINAL│     │  EXPIRED     │     │  SETTLING │
└──────────────┘     └──────────────┘     └───────────┘
       │                                        │
       │ A rechaza                    A rechaza │ todas remediaciones agotadas
       ▼                                        ▼
┌──────────────┐                        ┌─────────────────┐
│  DISPUTED    │                        │ SETTLEMENT_FAILED│
└──────────────┘                        └─────────────────┘
```

| Estado | ¿Terminal? | Descripción |
|---|---|---|
| `PROPOSED` | No | Envelope creado. |
| `ACCEPTED` | No | Lease otorgado por B. |
| `EXECUTING` | No | LLM en ejecución. |
| `SETTLING` | No | Evaluando aserciones. |
| `REMEDIATING` | No | Reparando aserciones fallidas. |
| `SETTLED` | **Sí** | Todas las aserciones pasaron. |
| `ACCEPTED_FINAL` | **Sí** | Emisor aceptó el receipt. |
| `REJECTED` | **Sí** | Lease rechazado en handshake. |
| `EXPIRED` | **Sí** | Ventana de lease vencida. |
| `SETTLEMENT_FAILED` | **Sí** | Aserciones fallaron sin remediación. |
| `DISPUTED` | No | Emisor rechazó el receipt. |
| `SETTLEMENT_TIMEOUT` | **Sí** | Timeout excedido durante SETTLING. |

---

## 9. Reglas de Normalización

### Repository
```
1. Eliminar trailing slash:  "github.com/a/b/"  → "github.com/a/b"
2. Eliminar sufijo .git:    "github.com/a/b.git" → "github.com/a/b"
3. Validar formato:          "host/path" con al menos 2 componentes
```

### Territorio.WorkspacePath
```
1. DEBE ser ruta absoluta: starts with "/"
2. Se resuelve con filepath.Join(workspace, relativePath) para params
```

### Timeout y Remediaciones
```
timeout_seconds    > 0   (exclusivo, debe ser al menos 1)
max_remediations  ≥ 0   (0 = sin remediación)
```

### SHA-256
```
64 caracteres hex lowercase [0-9a-f].
```

### UUIDv7
```
Formato: xxxxxxxx-xxxx-Mxxx-Nxxx-xxxxxxxxxxxx
M = 7 (versión)
N = 8, 9, a, o b (variante)
```
