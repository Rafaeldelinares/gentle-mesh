# RFC-002 v2 — Tareas de evolución

> Proyecto: `rfc002` / RFC-002 Settlement Protocol v2
> Doc rector: `docs/rfcs/002-goals-and-non-goals.md`
> Planning: `docs/planning/agent-rfc002-hardening-prompt.md`

## Resumen de faseado

| Fase | Alcance | Tiempo | PR |
|-------|---------|--------|-----|
| **0a** | Hygiene pura — CI, gitignore, artefactos fuera | 1 día | PR0 |
| **0b** | Seguridad — InsecureSkipVerify, test endpoints fuera, key_pem | 2 días | PR0 |
| **1** | Cerrar explotables — S1, S2, S6 parcial, S7, S9, R4, R7 | 1 semana | PR1 |
| **2** | Autorización + robustez — S3, S4, S5, S6 completo, R1–R6, A1, A2 | 2 semanas | PR2 |
| **3** | Contención + detección (issues + RFC-003 draft) | — | — |

Perfil mínimo conforme (sección 5 del doc de objetivos): S1, S2, S3, S6, S7, S9, R1, R2, R5, R7, A1, A2.

---

## Fase 0a — Hygiene pura [COMPLETADA]

### 0a.1 CI en verde [COMPLETADO]
- [x] `go build ./...` — pasa
- [x] `go vet ./...` — pasa
- [x] `go test -race ./...` — pasa
- [x] `staticcheck` — configurado y pasando sin advertencias
- [x] `govulncheck` — configurado y pasando
- [x] `gosec` — configurado, HIGH+ en pkg/ (bloqueante, 0 issues), medium en integration/ (no bloqueante, sube informe como artefacto)
- [x] Fuzz tests (`FuzzCanonicalize`) — implementado y pasando
- [x] Suite Docker de integración (`//go:build docker`, `-p 1`, cleanup garantizado) — en CI y pasando
- [x] Versiones fijadas por SHA en GitHub Actions (checkout v4.2.2, setup-go v5.4.0, setup-buildx-action v3.7.0, upload-artifact v4.6.2)

### 0a.2 Hygiene de repo [COMPLETADO]
- [x] `data/*.db*`, `*.db-wal`, `*.db-shm` fuera del repo — git rm --cached aplicado
- [x] `integration/certs-generated/` en `.gitignore`
- [x] `*.key` con clave privada en `.gitignore`
- [x] Ningún `.pem` con clave privada commiteado
- [x] `.atl/` y `*.visual-check.*` ignorados en `.gitignore`
- [x] `git rm --cached` de artefactos ya commitados: repo limpio y validado

### 0a.3 Documentación [COMPLETADO]
- [x] `SECURITY.md` — canal de reporte, plazos, estado "experimental no auditado"
- [x] `docs/planning/agent-rfc002-hardening-prompt.md` — este plan
- [x] `docs/rfcs/002-goals-and-non-goals.md` — criterios de decisión canonicos

**DoD 0a:** COMPLETADA con el merge de PR #5 (Run 36462125322); repo limpio; docs presentes; fuzz tests pasando.

---

## Fase 0b — Controles de seguridad heredados [COMPLETADA]

### 0b.1 InsecureSkipVerify — baseline documentado, eliminado en Fase F1 (S1)
- [x] `security-gates.sh`: detecta `InsecureSkipVerify` y lo allowlista por fase (F1)
- [x] Allowlist: todos los archivos con `InsecureSkipVerify` → fase `F1` (cuando `--dev-insecure` esté implementado)
- [ ] `RequireAndVerifyClientCert` obligatorio por defecto en servidor (Fase 1)
- [ ] Flag `--dev-insecure` para desarrollo local, rechaza si `GENTLE_ENV=production` (Fase 1)
- [ ] Test: sin flag, TLS inválido → connection refused/rejected (Fase 1)

### 0b.2 Endpoints de test fuera de producción [COMPLETADO]
- [x] `server_shell.go`: `//go:build testharness` al inicio del archivo
- [x] `server.go`: `/execute` y `/inject-receipt` extraídos a `server_harness.go` (tagged)
- [x] Stub en `server_harness_stub.go` (`//go:build !testharness`) — no-op en prod
- [x] Binario sin tag: `/execute` y `/inject-receipt` devuelven 404 (no registrados)
- [x] Binario con `-tags testharness`: endpoints registrados normalmente
- [x] Build verificado en CI (Run 36462125322 verde en todos los jobs)
- [x] Dockerfile compila con `-tags testharness` para tests de integración

### 0b.3 Claves privadas fuera del servidor [COMPLETADO — bootstrap CSR diferido a Fase 1]
- [x] `pkg/keystore/store.go`: genera clave local, solo `private.pem` en disco (0600)
- [x] Nunca se transmite `key_pem` por la red
- [x] `key_pem` NO está en el esquema SQLite

**Pendiente de Fase 1 (diferido — requiere CSR enrollment bootstrapping):**
- [ ] Nodo genera clave localmente, envía CSR al servidor
- [ ] Servidor firma y devuelve solo certificado, nunca `key_pem`
- [ ] Test: ninguna ruta del servidor recibe, guarda o devuelve clave privada

> **Nota:** La sección 0b.3 original del plan incluía CSR enrollment y bootstrapping de claves.
> Esto requiere diseño de PKI out-of-band y está fuera del alcance mínimo de Phase 0.
> Tracking Issue #4: `feat(security): enrolamiento PKI mediante CSR y eliminación de key_pem en servidor (Fase 1 - 0.5)`

### 0b.4 Codeowners y protección de rama [COMPLETADO]
- [x] `CODEOWNERS`: `@Rafaeldelinares` en `pkg/signing/`, `pkg/keystore/`, `pkg/receipt/`, `pkg/envelope/`, `pkg/jcs/`, `.github/`
- [x] `scripts/security-gates.sh` ownership en CODEOWNERS

**DoD 0b:** COMPLETADA con el merge de PR #5 (Run 36462125322); `security-gates.sh` reporta 0 violaciones; 0 endpoints de test en binario sin tag; `key_pem` fuera de SQLite; CSR enrollment documentado en Issue #4.

---

## Resumen del estado actual

| Check | Status |
|-------|--------|
| Build (`go build ./...`) | ✅ Pasa |
| Vet (`go vet ./...`) | ✅ Pasa |
| Security gates (`security-gates.sh`) | ✅ 0 violaciones |
| Staticcheck (`staticcheck ./...`) | ✅ 0 advertencias (resuelto sin excepciones) |
| Gosec (pkg/, HIGH+) | ✅ 0 issues |
| Gosec (integración) | ⚠️ Reporte artifact (4 G115 en testharness) |
| Govulncheck | ✅ 0 vulnerabilidades conocidas |
| JCS Canonicalizer RFC 8785 | ✅ Reemplazado con standard library + red team tests pasando |
| Base64 Signature Malleability | ✅ Eliminada con strict decode + roundtrip check |
| Test endpoints con build tag | ✅ Completado (`//go:build testharness` + stub no-op) |
| Docker Integration Isolation | ✅ Aislado: volumen por agente, WAL mode, busy timeout 5000ms, teardown garantizado |
| CI GitHub Actions | ✅ 100% verde (Lint & Security, Unit Tests, Integration Tests Docker) |

---

## Fase 1 — Cerrar lo explotable

> **Regla de Ejecución RDD desde Fase 1:** Cada PR debe mantenerse estrictamente por debajo de 400 líneas de diff (excluyendo ficheros autogenerados), recurriendo a PRs encadenados (*chained PRs*) si es necesario, garantizando que el presupuesto de contexto de las lentes de revisión RDD no sea desbordado (`lens_context_budget_exceeded`) y la revisión nativa pueda ejecutarse completamente en cada unidad de trabajo.

### 1.1 Docs rectores
- [ ] `docs/architecture/THREAT-MODEL.md` — activos, actores, STRIDE por endpoint, modelo de adversario
- [ ] Verificar CVEs cited antes de citarlos en el threat model
- [ ] `docs/rfcs/002-cognitive-agent-network.md` sección 2: definición "cognitiva" + enlace N9

### 1.2 Verificar firmas en todos los endpoints (S2)
- [ ] `handleSubmitEnvelope`: verificar `EmitterSignature` antes de `runPreconditions`
- [ ] `handleAccept` / `handleDispute`: verificar firma emisor + ejecutor antes de guardar
- [ ] `/settle`, `/dispatch`, `/execute-local`: verificación de firma
- [ ] Test adversarial: clave ajena → 401; emisor no registrado → 401; alterado post-firma → 401

### 1.3 Firmas deterministas (R7)
- [ ] Timestamps normalizados: RFC 3339 UTC, precisión fija
- [ ] Test: firma → serializa → SQLite → lee → verifica × 1000 con `-race`, 0 fallos
- [ ] Todas las verificaciones degradadas a log restauradas como FATAL

### 1.4 Integridad al escribir (S7)
- [ ] `SaveReceipt`: validar `prev_hash` dentro del mutex; si no coincide → `ErrChainBroken`
- [ ] Tests concurrencia: 8 y 50 goroutines (R4)

### 1.5 Sin ejecución de datos (S6), parte 1
- [ ] `json.Decoder.DisallowUnknownFields()` en todos los decodificadores de red
- [ ] Invariante documentada en `SETTLEMENT-PROTOCOL-FORMAL-SPEC.md`

### 1.6 Versionado (S9)
- [ ] `protocol_version` en envelope, lease y receipt, cubierto por la firma
- [ ] Versión desconocida → rechazo
- [ ] Sin modo de compatibilidad v1 (N8)

### 1.7 Keystore
- [ ] `private.pem` → 0600; directorio → 0700; rechazo si permisos más laxos

### 1.8 R4: Ordenación determinista de recibos concurrentes (seq INTEGER)
**Problema:** `GetChain` ordena por `executor_signed_at RFC3339` (segundos). Recibos del mismo segundo tienen orden indefinido, lo que rompe `prev_hash` bajo alta concurrencia.
**Solución:** columna `seq INTEGER` monótona por pareja emisor:ejecutor, asignada dentro del mutex de `SaveReceipt`.
- `schema.go` / `InitSchema`: `ALTER TABLE receipts ADD COLUMN seq INTEGER`
- `SaveReceipt`: tras bloquear el mutex, consultar `MAX(seq)` para la pareja y asignar `seq = max + 1`
- `GetChain` y `GetLastReceipt`: `ORDER BY seq ASC` en vez de `ORDER BY executor_signed_at ASC`
- `ExecutorSignedAt` se sigue guardando en `RFC3339Nano` para trazabilidad
- Test: 50 recibos concurrentes del mismo segundo → cadena válida en orden seq ascendente

### 1.9 S2: Test adversario de emitter_signature en /accept y /dispute
**Estado actual:** `handleAccept` y `handleDispute` ya verifican ExecutorSignature (stored) + EmitterSignature (acceptance/dispute) contra `KnownAgents`.
**Falta:** test adversario que demuestre que una firma con clave ajena produce 401.
- Setup: B conoce a A (`KnownAgents[A] = aPubkey)`
- Test: A firma aceptación con clave de C (`cSigner`) → `handleAccept` → 401
- Test: firma con clave de C en `/dispute` → 401
- Test: firma de A con clave de A → 200 (happy path)

### 1.10 Posicionamiento de gentle-mesh sobre A2A — documento docs/rfcs/002-a2a-positioning.md
Se hace al INICIO de la Fase 1, antes de escribir código nuevo de transporte, descubrimiento, formato de mensajes o delegación. La decisión final la toma el humano.

Premisa acordada: gentle-mesh funciona SOBRE A2A. A2A aporta descubrimiento, autenticación y ciclo de vida de tareas; gentle-mesh aporta la capa de confianza y liquidación.

Fuentes que hay que leer DIRECTAMENTE (no resúmenes):
- Especificación A2A v1.0.0: https://a2a-protocol.org/latest/specification/
- Extensiones A2A: https://a2a-protocol.org/latest/topics/extensions/
- signed-receipts/v1: https://github.com/a2aproject/A2A/issues/2150 y su implementación https://github.com/CSOAI-ORG/a2a-signed-receipts
- Identidad, delegación y aplicación de políticas: https://github.com/a2aproject/A2A/issues/1575 (Agent Passport System y el resto de implementaciones citadas)
- Artifact receipts (cerrada): https://github.com/a2aproject/A2A/issues/2236
- AP2, como patrón de autorización firmada: https://ap2-protocol.org/

El documento debe responder:
a. Mapa de capas: qué cubre A2A, qué cubren las propuestas y extensiones existentes y qué solo cubre gentle-mesh (hipótesis: pre-flight con lease, aserciones de liquidación, aceptación/disputa y cadena). Confírmala o refútala con las fuentes.
b. Compatibilidad con signed-receipts/v1: ¿puede el SettlementReceipt ser un superconjunto compatible (mismos campos base, JCS + Ed25519, en Task.metadata) y añadir encima cadena, aserciones y contrafirma del emisor? Diferencias concretas campo a campo.
c. Identidad de claves: ¿adoptar resolución por DID (did:web) según signed-receipts/v1 en lugar de, o además de, el keystore propio? Cómo encaja con el mTLS ligado a identidad (S1).
d. Delegación (Fase 2): comparar Agent Passport System, UCAN, biscuit y macaroons frente a diseñar la nuestra. Recomendación, sin implementar.
e. Mapeo de estados gentle-mesh ↔ A2A (p. ej. REJECTED_CAPABILITY → REJECTED con motivo; SETTLED_CLEAN → COMPLETED + recibo en Task.metadata; SETTLEMENT_FAILED → FAILED + recibo).
f. Qué tipo de extensión A2A sería gentle-mesh (data-only, profile, method o state machine) y su URI.
g. Oportunidad: esbozo de una propuesta de "extensión de liquidación" para la comunidad A2A, construida sobre signed-receipts/v1.
h. Recomendación razonada: ¿la v2 se implementa como extensión A2A desde la Fase 2, o después? Qué cambia en el plan de la Fase 1 y la Fase 2 en cada caso.

Anota la fecha de consulta de cada fuente: estas propuestas cambian con frecuencia.

**DoD F1:** Perfil mínimo parcial (S1, S2, S6 parte 1, S7, S9, R4, R7); 0 `InsecureSkipVerify` fuera de `--dev-insecure`; 0 verificaciones degradadas a log.

> ⚠️ Pendiente en Fase 1: WU5 (test adversarios), R7 × 1000 iteraciones, N8 (compatibility mode), keystore permisos, 1.8 (seq INTEGER), 1.9 (test emitter sig), 1.10 (A2A positioning).

---

## Fase 2 — Autorización y robustez

### 2.1 Política local del ejecutor (S3, S4)
- [ ] `policy.yaml` cargado al arranque
- [ ] Perfil `minimal` codificado: sin exec, sin red, solo lectura workspace
- [ ] Agente no puede leer ni modificar política en runtime

### 2.2 Capacidades opcionales en envelope (S3)
- [ ] Bloque `capabilities` opcional, dentro del hash JCS y firma
- [ ] Capacidades efectivas = envelope ∩ política del emisor
- [ ] Si se pide más de lo permitido → `REJECTED_CAPABILITY` con lista de denegaciones
- [ ] Recibo incluye hash de capacidades efectivas y denegaciones

### 2.3 Ejecución sin shell (S6), parte 2
- [ ] Eliminar `exec.CommandContext(ctx, "sh", "-c", ...)` en producción
- [ ] Assertions y precondiciones `command_*` → `{bin, args}` validados contra allowlist
- [ ] `cmd.Env` desde cero, PATH mínimo, sin heredar secretos
- [ ] Rutas confinadas a `WorkspaceDir` con `filepath.Clean` + `filepath.EvalSymlinks`
- [ ] `security-gates.sh`: 0 `"sh", "-c"` fuera de `testharness`

### 2.4 Robustez
- [ ] R1: deduplicación por `envelope_id`
- [ ] R2: ejecutor aborta al vencer lease → `SETTLEMENT_TIMEOUT`
- [ ] R5: estados `REJECTED_UNAUTHORIZED`, `REJECTED_CAPABILITY` en spec y código
- [ ] R6: expiraciones evaluadas por quien las aplica, con su propio reloj

### 2.5 Kit de conformidad
- [ ] Suite `conformance/` — un test por ID (S1…S9, R1…R7, A1…A7)
- [ ] Ejecutable contra cualquier implementación via URL + credenciales
- [ ] Perfil mínimo declarado en `/health` como `conformance: "rfc002-v2-minimal"`

### 2.6 Dogfooding
- [ ] `integration/dogfood/phase-dod.envelope.json` — aserciones = DoD de cada fase
- [ ] Recibo de dogfooding adjunto al PR

**DoD F2:** Perfil mínimo conforme completo; `security-gates.allowlist` vacía; suite `conformance/` verde; recibo dogfooding; 0 `"sh", "-c"` en producción.

---

## Fase 3 — Issues + RFC-003 draft

- [ ] Issue por cada punto de Fase 3 (S4 por infra, S8, A5, telemetría, anclaje externo, HSM/KMS, RFC-004)
- [ ] `docs/rfcs/003-containment-and-detection.md` — borrador

---

## Estado

```
[X] Bug #48 resuelto (R7 determinismo)                              — 0430826
[X] Tests distribuidos: WU13–WU17 pasan
[X] Repo base y tracking formal
[X] Fase 0a: hygiene pura                                         — PR #5 (merge a60083a)
[X] Fase 0b: controles de seguridad heredados                      — PR #5 (merge a60083a)
[ ] Fase 1: cerrar explotables
[ ] Fase 2: autorización + robustez
[ ] Fase 3: issues + RFC-003 draft
```

### Fases 0a y 0b completadas (PR #5)

- **Merge:** PR #5 (`fix/phase0-clean-v2` -> `feat/rfc-002-settlement`, commit de merge `a60083a`).
- **CI:** 100% verde en todos los jobs (Run 36462125322): `Lint & Security`, `Unit Tests`, `Integration Tests (Docker)`.
- **Security Gates:** `scripts/security-gates.sh` con 0 violaciones.
- **Higiene de Repo:** `.gitignore` estricto; artefactos de DB (`mesh.db*`), certificados y claves privadas eliminados del tracking.
- **JCS Canonicalizer RFC 8785:** Reemplazo de parser vulnerable, claves duplicadas rechazadas, UTF-8 y surrogates validados, precisión IEEE 754 > 2^53 documentada, bypass de `HashHexString` corregido con TDD estricto.
- **Base64 Malleability:** Decodificación estricta y round-trip canónico.
- **Aislamiento Docker:** Endpoints bajo tag `testharness`, volumen aislado por agente, WAL y timeout configurados.
