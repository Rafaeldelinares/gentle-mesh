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

## Fase 0a — Hygiene pura

### 0a.1 CI en verde
- [ ] `go build ./...` — pasa
- [ ] `go vet ./...` — pasa
- [ ] `go test -race ./...` — pasa
- [ ] `staticcheck` — configurado, pasar
- [ ] `govulncheck` — configurado, pasar
- [ ] `gosec` — configurado, severidad media+ falla el build
- [ ] Fuzz tests (`-fuzztime=30s`) — configurados
- [ ] Suite Docker de integración (`//go:build testharness`) — en CI
- [ ] Versiones fijadas por SHA en GitHub Actions

### 0a.2 Hygiene de repo
- [ ] `data/*.db*`, `*.db-wal`, `*.db-shm` fuera del repo (ya en `.gitignore`)
- [ ] `integration/certs-generated/` en `.gitignore`
- [ ] `*.key` con clave privada en `.gitignore`
- [ ] Ningún `.pem` con clave privada commiteado
- [ ] No hay artefactos de ejecución (`*.csr`, `.srl`) en el repo

### 0a.3 Documentación
- [ ] `SECURITY.md` — canal de reporte, plazos, estado "experimental no auditado"
- [ ] `docs/planning/agent-rfc002-hardening-prompt.md` — este plan
- [ ] `docs/rfcs/002-goals-and-non-goals.md` — criterios de decisión canonicos

**DoD 0a:** CI verde en PR; repo limpio; docs presentes.

---

## Fase 0b — Controles de seguridad heredados

### 0b.1 InsecureSkipVerify eliminado
- [ ] `RequireAndVerifyClientCert` obligatorio por defecto en servidor
- [ ] Flag `--dev-insecure` para desarrollo local, rechaza si `GENTLE_ENV=production`
- [ ] `InsecureSkipVerify` solo tras `--dev-insecure`, con warning en logs
- [ ] Test: sin flag, TLS inválido → connection refused/rejected
- [ ] `security-gates.sh` detecta `InsecureSkipVerify` fuera de la ruta `--dev-insecure`

### 0b.2 Endpoints de test fuera de producción
- [ ] `/execute`, `/inject-receipt`, `ChainStore.InjectReceipt` → `//go:build testharness`
- [ ] Binario sin tag devuelve 404 en esas rutas
- [ ] `Makefile`, `Dockerfile` compilan con `-tags testharness` para tests
- [ ] `security-gates.sh` detecta endpoints de test fuera de build tag

### 0b.3 Claves privadas fuera del servidor
- [ ] Nodo genera clave localmente, envía CSR al servidor
- [ ] Servidor firma y devuelve solo certificado, nunca `key_pem`
- [ ] Eliminar `key_pem` del esquema SQLite
- [ ] Test: ninguna ruta del servidor recibe, guarda o devuelve clave privada
- [ ] Migración que borre `key_pem` de DBs existentes

### 0b.4 Codeowners y protección de rama
- [ ] `CODEOWNERS`: `@Rafaeldelinares` en `pkg/signing/`, `pkg/keystore/`, `pkg/receipt/`, `pkg/envelope/`, `pkg/jcs/`, `.github/`, `scripts/security-gates*`
- [ ] Documentar en PR la config de protección de rama recomendada

**DoD 0b:** `security-gates.sh` reporta 0 `InsecureSkipVerify` fuera de dev; 0 endpoints de test en binario sin tag; `key_pem` eliminada.

---

## Fase 1 — Cerrar lo explotable

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

**DoD F1:** Perfil mínimo parcial (S1, S2, S6 parte 1, S7, S9, R4, R7); 0 `InsecureSkipVerify` fuera de `--dev-insecure`; 0 verificaciones degradadas a log.

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
[X] Repo creado: https://github.com/Rafaeldelinares/rfc002
[X] Fase 0a: hygiene pura                                         — 5ad0b70
[ ] Fase 0b: controles de seguridad heredados
[ ] Fase 1: cerrar explotables
[ ] Fase 2: autorización + robustez
[ ] Fase 3: issues + RFC-003 draft
```

### Fase 0a completada (5ad0b70)

- CI: go build/vet/test -race + staticcheck + gosec (severity medium+) + govulncheck
  + Docker integration tests (testharness tag)
- .gitignore: `*.key`, `*.pem`, `*.srl`, `*.csr`, `data/`, `*.db*`, `integration/certs-generated/`,
  `.codegraph/`, `.atl/`
- SECURITY.md: reporting policy, timelines, in/out of scope, experimental status
- CODEOWNERS: @Rafaeldelinares owns pkg/signing, pkg/keystore, pkg/receipt, pkg/envelope,
  pkg/jcs, certstore, .github/, scripts/
- Claves privadas (`.key`, `.srl`) removidas del tracking de git (ahora ignoradas)

**Baseline CI: FAIL** — gosec reporta 26 issues medium+ (G104, G306, otros) que se
arreglarán en Fase 1. govulncheck: 0 vulnerabilidades.
