# Tarea: Evolución de RFC-002 hacia un protocolo seguro, robusto y ágil entre agentes

## Contexto
Repo: Rafaeldelinares/gentle-mesh. Rama base: `feat/rfc-002-settlement` (PR #2, aún sin mergear).

RFC-002 ya resuelve bien la integridad y el no repudio (JCS RFC 8785 + Ed25519 + cadena SHA-256 en
SQLite WAL). Una revisión de seguridad concluye que le faltan autorización, contención y robustez: hoy el
servidor de integración es, en la práctica, ejecución remota de comandos con TLS.

Nuevo enfoque acordado:
- **No se anuncian capacidades de nodo.** El descubrimiento es por intento: si un ejecutor no puede,
  responde con un lease `accepted: false` y el motivo.
- **Lo obligatorio es la política local del ejecutor**, aplicada fuera del agente.
- **Las capacidades del envelope son opcionales.** Si no vienen, se aplica el perfil mínimo
  (sin red, sin exec, solo lectura del workspace). Sirven para pedir más o para restringir en un fan-out.

Documento rector: te adjunto `002-goals-and-non-goals.md`. Cópialo tal cual en
`docs/rfcs/002-goals-and-non-goals.md` en tu primer commit. Todos los IDs (S1…S9, R1…R7, A1…A7, N1…N8)
de este prompt se refieren a él. Toda decisión debe justificarse contra ese documento.

### Estrategia de ramas
- NO se mergea el PR #2 tal cual en `main`. Las fases 0, 1 y 2 entran en `feat/rfc-002-settlement` y el PR #2
  llega a `main` ya como v2. `main` nunca debe contener la versión con ejecución remota de comandos.
- Una rama por fase (`fix/rfc-002-v2-phase0`, `-phase1`, `-phase2`) partiendo de `feat/rfc-002-settlement`,
  y un PR por fase contra ella.
- PRs pequeños: si un PR supera ~800 líneas de código no generado, divídelo en PRs encadenados.
- Un commit por punto (Conventional Commits, con el ID del objetivo en el cuerpo, p. ej. `Implements: S2`).
- No tienes permiso de push a `main` ni a `feat/rfc-002-settlement`: todo pasa por PR.

### Tiempo por fase
Fase 0: 1 día · Fase 1: 1 semana · Fase 2: 2 semanas. Si una fase va a superar su límite, para y
reporta el motivo y una propuesta de rediseño o recorte, en vez de seguir empujando.

## Reglas no negociables
1. PROHIBIDO arreglar un fallo desactivando, saltando o degradando una verificación o un test
   (`t.Skip`, `t.Fatal`→`t.Logf`, `InsecureSkipVerify`, comentarios tipo "skip verification").
   Si algo falla, encuentra la causa raíz.
2. Verificar antes de actuar (S2): ninguna ejecución ni persistencia sin firma verificada.
3. Deny-by-default (S3).
4. Nunca `sh -c` con datos llegados por red (S6).
5. `go test -race ./...` y la suite Docker de integración en verde al cerrar cada fase.
6. No reinventes criptografía ni sandboxing (N7). Usa la librería estándar de Go o dependencias auditadas.
7. Si una decisión es ambigua, elige la opción más restrictiva, documéntala en el PR y sigue.
8. **Tests rojos primero.** Para cada vulnerabilidad: (a) escribe un test que demuestre que el ataque
   funciona HOY y commitéalo en rojo (`test(red): ...`, marcado con `//go:build redteam` para no romper CI
   hasta el fix); (b) implementa el fix; (c) quita la build tag para que el test pase a formar parte de la
   suite normal. El historial debe mostrar rojo → verde.
9. **No te evalúas a ti mismo.** Tu trabajo lo revisará un revisor independiente (otra sesión o agente que
   solo recibe el diff y el documento de objetivos, con el encargo de romperlo) y un humano en los paquetes
   criptográficos. Deja en cada PR una sección "Cómo intentaría romper esto" con los ataques que consideraste.
10. **Trabaja contenido.** No uses ni pidas secretos de producción; no hagas peticiones de red fuera de
    los registros de paquetes necesarios; no modifiques la configuración de CI ni `CODEOWNERS` fuera de la
    Fase 0 sin decirlo explícitamente en el PR.

---

## FASE 0 — Controles y higiene (PR 0, antes de tocar el protocolo)

### 0.1 CI (GitHub Actions, `.github/workflows/ci.yml`)
- Jobs: `go build ./...`, `go vet ./...`, `go test -race ./...`, `staticcheck`, `govulncheck`, `gosec`
  (fallar en severidad media o superior; excepciones solo con justificación inline) y fuzz tests en modo
  corto (`-fuzztime=30s` por objetivo).
- Job separado para la suite Docker de integración (`-tags testharness`), en push a
  `feat/rfc-002-settlement` y a `main`.
- Versiones fijadas: Go según `go.mod`; las actions fijadas por SHA, no por tag.

### 0.2 Comprobaciones automáticas del DoD (`scripts/security-gates.sh`, ejecutado en CI)
El script falla si encuentra, fuera de ficheros con build tag `testharness` o `redteam`:
- `"sh", "-c"` o `"bash", "-c"`;
- `InsecureSkipVerify` fuera de la ruta `--dev-insecure`;
- `t.Skip(` nuevo sin comentario `// security-gate: allowed <motivo>`;
- verificaciones criptográficas degradadas a log (patrón orientativo: `Logf` cerca de `Verify`);
- ficheros `*.db`, `*.db-wal`, `*.db-shm`, `*.pem` con clave privada o `*.key` en el repo.
En Fase 0 el script arrancará fallando por lo existente: añade una lista de excepciones temporal
(`scripts/security-gates.allowlist`) con cada caso y el ID de la fase que lo elimina. Esa lista debe quedar
vacía al cerrar la Fase 2.

### 0.3 Protección y propiedad
- `CODEOWNERS`: `@Rafaeldelinares` es propietario obligatorio de `pkg/signing/`, `pkg/keystore/`,
  `pkg/receipt/`, `pkg/envelope/`, `pkg/jcs/`, `pkg/server/store/certstore.go`, `.github/` y `scripts/security-gates*`.
- Documenta en el PR la configuración de protección de rama recomendada para `main` y
  `feat/rfc-002-settlement` (CI verde obligatorio, 1 revisión obligatoria, revisión de CODEOWNERS, sin
  force-push). La aplica el humano; tú no tienes permisos para ello.

### 0.4 Higiene del repositorio
- Eliminar del repo `data/mesh.db`, `data/mesh.db-shm` y `data/mesh.db-wal` (contienen un token de
  enrolamiento, ya consumido). Añadir `data/*.db*`, `*.db-wal`, `*.db-shm` y `integration/certs-generated/`
  al `.gitignore`. NO reescribas el historial: deja en el PR una nota para que el humano decida si limpiarlo
  (p. ej. con `git filter-repo`).
- Revisar que no haya ningún otro artefacto de ejecución commiteado (`tls-test/*.csr`, `.srl`, etc.)
  y decidir caso a caso.

### 0.5 Claves privadas fuera del servidor (S1, heredado de RFC-001)
- `pkg/server/store/certstore.go` guarda `key_pem` de los nodos en la tabla `node_certs`. En un modelo de
  auto-enrolamiento con CSR, la clave privada nunca debe salir del nodo.
- Cambios: el nodo genera su clave localmente y envía solo el CSR; el servidor firma y devuelve solo el
  certificado; eliminar `key_pem` del esquema, con una migración que borre los valores existentes; test que
  verifique que ninguna ruta del servidor recibe, guarda o devuelve una clave privada.
- Si algún flujo actual depende de que el servidor genere la clave, documéntalo y sustitúyelo por CSR.

### 0.6 Política de seguridad
- `SECURITY.md`: cómo reportar vulnerabilidades (canal privado, p. ej. GitHub Security Advisories), plazos
  orientativos de respuesta y alcance. Declarar el estado del proyecto como "experimental, no auditado".

**DoD Fase 0:** CI en verde en el PR; `security-gates.sh` en CI con la allowlist documentada; `data/*.db*`
fuera del repo; `key_pem` eliminado con su test; `CODEOWNERS` y `SECURITY.md` presentes.

---

## FASE 1 — Cerrar lo explotable + fundamentos (PR 1)

### 1.1 Documentación rectora
- Añadir `docs/rfcs/002-goals-and-non-goals.md` (adjunto).
- Crear `docs/architecture/THREAT-MODEL.md`: activos, actores, límites de confianza, modelo de adversario
  (sección 2 del documento de objetivos), tabla STRIDE por endpoint y relación explícita con los no-objetivos.
  Como motivación, referenciar los incidentes OpenAI/Hugging Face (julio de 2026) y los CVE de
  deserialización de checkpoints de LangGraph (CVE-2025-64439, CVE-2026-28277).
- En `docs/rfcs/002-cognitive-agent-network.md`, sección 2, añadir la definición de "cognitiva" que figura en
  el documento de objetivos ("la cognición vive en los agentes; la confianza vive en la malla") y enlazar el
  no-objetivo N9.
- Estos dos documentos y el de objetivos van en un PR propio al inicio de la fase, para que el responsable de
  gobernanza (Alan Buscaglia, según la RFC) pueda aprobarlos antes de que se escriba el código de la fase.

### 1.2 Verificar firmas en todos los endpoints de entrada (S2)
- `integration/agent/server.go` `handleSubmitEnvelope`: verificar `EmitterSignature` sobre
  `envelope.ComputeEnvelopeHash` con la clave del keystore **antes** de `runPreconditions`.
  Emisor desconocido o firma inválida → 401 y nada se ejecuta.
- `handleAccept` / `handleDispute`: verificar la firma del emisor y la del ejecutor del recibo almacenado
  antes de guardar. Eliminar el bypass "Bug #48".
- Igual en `/settle`, `/dispatch` y `/execute-local` (`server_shell.go`).
- Tests: válida → OK; clave ajena → 401; emisor no registrado → 401; envelope alterado tras firmar → 401.

### 1.3 Firmas deterministas (R7)
- Encontrar la causa raíz del supuesto "no determinismo" de Ed25519 (Ed25519 es determinista):
  probablemente timestamps con distinta precisión o zona tras el round-trip JSON/SQLite, campos limpiados de
  forma asimétrica en `ComputeReceiptHash` u `omitempty` asimétrico.
- Normalizar los timestamps en el tipo (RFC 3339 UTC, precisión fija).
- Test: firmar → serializar → SQLite → leer → verificar × 1000 con `-race`, 0 fallos.
- Restaurar como FATALES todas las verificaciones degradadas a log en `wu13_test.go`, `wu14` y el resto.

### 1.4 Endpoints de test fuera de producción
- `POST /execute`, `POST /inject-receipt`, `ChainStore.InjectReceipt` y `UpdateReceipt` pasan a ficheros
  con `//go:build testharness`. Makefile, Dockerfile y tests de integración compilan con `-tags testharness`.
- Test: el binario sin tag devuelve 404 en esas rutas.

### 1.5 Identidad fuerte (S1)
- mTLS obligatorio por defecto (`RequireAndVerifyClientCert`).
- Eliminar `WithInsecureSkipVerify()` (`server_shell.go:405`, `cmd/gentle-mesh/main.go`) salvo tras un flag
  `--dev-insecure` que avise y que se rechace si `GENTLE_ENV=production`.
- CN/SAN del certificado cliente == `EmitterAgentID`; si no → 403.
- Reutilizar la CA de la malla de RFC-001 (`/v1/mesh/ca`, `/v1/mesh/join`) en vez de una PKI paralela, si es viable.
- Healthchecks de docker-compose con la CA, sin `--no-check-certificate`.

### 1.6 Integridad al escribir (S7)
- `SaveReceipt` vuelve a validar `prev_hash` dentro del mutex; si no coincide → `ErrChainBroken`.
- Tests de concurrencia con 8 y 50 goroutines (R4).

### 1.7 Sin ejecución de datos (S6), parte 1
- `json.Decoder.DisallowUnknownFields()` en todos los decodificadores de red.
- Añadir el invariante a `SETTLEMENT-PROTOCOL-FORMAL-SPEC.md`: "Los datos recibidos nunca se deserializan
  a código ni a tipos arbitrarios; solo JSON canónico a structs tipados."

### 1.8 Versionado (S9)
- Campo `protocol_version` (valor `"2"`) en envelope, lease y receipt, cubierto por la firma.
  Versión desconocida → rechazo. Sin modo de compatibilidad con v1 (N8).

### 1.9 Keystore
- `private.pem` siempre 0600 (corregir la incoherencia con 0640), directorio 0700, y rechazo al abrir si los
  permisos son más laxos.

**DoD Fase 1:** perfil mínimo conforme parcial (S1, S2, S6 parte 1, S7, S9, R4, R7) con test por cada uno;
0 `InsecureSkipVerify` fuera de `--dev-insecure`; 0 verificaciones degradadas a log.

---

## FASE 2 — Autorización y robustez (PR 2)

### 2.1 Política local del ejecutor (S3, S4)
- Fichero de política del operador (p. ej. `policy.yaml`, cargado al arrancar y firmado o con permisos 0600),
  con perfiles por emisor:
  ```yaml
  default_profile: minimal
  emitters:
    agent-a:
      max:
        exec:    [{bin: /usr/bin/pytest}, {bin: /usr/local/go/bin/go, args_prefix: [test]}]
        fs:      {read: ["**"], write: ["src/**", "tests/**"]}
        network: {egress: deny}
        limits:  {cpu_seconds: 300, memory_mb: 1024, wall_seconds: 600, max_output_bytes: 1048576}
  ```
- Perfil `minimal` codificado en el binario: sin exec, sin red, solo lectura del workspace.
- El agente no puede leer ni modificar la política en tiempo de ejecución.

### 2.2 Capacidades opcionales en el envelope
- Bloque `capabilities` opcional, dentro del hash JCS y firmado. Ausente → `minimal` (A1).
- Capacidades efectivas = envelope ∩ política del emisor en el ejecutor.
  Si el envelope pide más de lo permitido → estado `REJECTED_CAPABILITY` con la lista de denegaciones
  (sin recorte silencioso).
- El recibo incluye el hash de las capacidades efectivas y las denegaciones, cubiertos por la firma.

### 2.3 Ejecución sin shell (S6, parte 2)
- Sustituir todo `exec.CommandContext(ctx, "sh", "-c", ...)` en `pkg/settlement/evaluate.go`,
  `pkg/shell` e `integration/agent` por `exec.CommandContext(ctx, bin, args...)`.
- Aserciones y precondiciones `command_*` pasan a `{bin, args}`, validadas contra las capacidades efectivas
  (ruta absoluta resuelta con `exec.LookPath` comparada con la allowlist).
- `tool_available` con `exec.LookPath` y nombre validado (`^[a-zA-Z0-9._-]+$`).
- `cmd.Env` construido desde cero (PATH mínimo, sin heredar secretos). Ignorar `Env` y `WorkingDir` del cliente.
- Rutas resueltas con `filepath.Clean` + `filepath.EvalSymlinks` y confinadas a `WorkspaceDir`
  (tests contra `../`, symlinks y rutas absolutas).
- Límites: timeout por contexto, truncado de salida a `max_output_bytes`, y rlimits si están disponibles.
- Los flujos que necesiten pipes o `&&` se empaquetan como script versionado en el workspace, cubierto por
  `file_hash_equals`.

### 2.4 Delegación atenuada (S5)
- En `/dispatch` y en cualquier fan-out: capacidades del hijo ⊆ padre ∩ política del ejecutor destino.
- El envelope hijo referencia el hash del padre (`parent_envelope_hash`), firmado.
- Test de propiedad (p. ej. con `testing/quick` o fuzzing): ninguna delegación amplía permisos.
- Evalúa si encaja un modelo estándar (biscuit, UCAN, macaroons) en vez de uno propio (N7);
  documenta la decisión en el PR.

### 2.5 Robustez
- **R1 Idempotencia:** `envelope_id` único por emisor; un reenvío devuelve el recibo existente sin re-ejecutar.
- **R2 Leases:** el ejecutor aborta al vencer el lease y emite `SETTLEMENT_TIMEOUT`.
- **R3 Reintentos:** el cliente reintenta con backoff apoyándose en R1; test de fallo de red a mitad de operación.
- **R5 Estados terminales:** actualizar la máquina de estados (spec y código) con `REJECTED_UNAUTHORIZED`
  y `REJECTED_CAPABILITY`; test de que todo contrato alcanza un estado terminal.
- **R6 Relojes:** las expiraciones las evalúa quien las aplica, con su reloj; test con desfase de ±10 min.

### 2.6 Descubrimiento por intento (A2)
- El lease con `accepted: false` incluye un motivo estructurado:
  `{code: TOOL_UNAVAILABLE | CAPABILITY_DENIED | PRECONDITION_FAILED | UNAUTHORIZED, detail: ...}`.

### 2.7 Kit de conformidad
- Mover los tests que verifican objetivos S/R/A a una suite propia `conformance/`, con un test por ID
  (nombre `TestConformance_S2_VerifyBeforeAct`, etc.), ejecutable contra cualquier implementación
  mediante la URL y las credenciales de un nodo.
- Un README que explique cómo ejecutarla y qué IDs forman el perfil mínimo conforme.

### 2.8 Validar el trabajo con el propio protocolo (dogfooding)
- Crear `integration/dogfood/phase-dod.envelope.json`: un envelope cuyas aserciones son el DoD de cada fase
  (suite en verde, `security-gates.sh` con salida 0, allowlist vacía, suite de conformidad en verde).
- Ejecutarlo contra un ejecutor gentle-mesh v2 y adjuntar al PR el recibo firmado y el resultado de
  `VerifyChain`. El cierre de fase se considera válido solo con ese recibo en estado `SETTLED_CLEAN`.

**DoD Fase 2:** S3, S4 (en la parte de proceso), S5, S6 completo, R1–R6, A1, A2 con tests;
`security-gates.allowlist` vacía; suite `conformance/` en verde; recibo de dogfooding adjunto.
0 apariciones de `"sh", "-c"` fuera de `testharness`. Perfil mínimo conforme (sección 5 del documento de
objetivos) completo y declarado en `/health` como `conformance: "rfc002-v2-minimal"`.

---

## FASE 3 — Contención y detección (issues + borrador de RFC-003)
No implementar en esta PR salvo que sobre tiempo. Crear un issue por punto y un borrador
`docs/rfcs/003-containment-and-detection.md`:
- **S4 por infraestructura:** sandbox efímero por tarea (gVisor/nsjail/contenedor rootless) derivado de las
  capacidades efectivas; red interna sin egress para ejecutores en docker-compose, **DNS incluido**.
- **Credenciales efímeras** por tarea con TTL corto.
- **S8 Revocación:** estado `REVOKED`, lista de revocación firmada propagada por heartbeat y kill switch
  autenticado que cancela todos los leases.
- **A5 Controles por riesgo:** `PENDING_APPROVAL` con co-firma de un rol `approver` cuando se pide red o
  escritura fuera del perfil.
- **Telemetría sobre recibos:** alertas por ráfagas, aserciones que fallan en bucle con remediación,
  timeouts repetidos y emisores o ejecutores nuevos.
- **Anclaje externo** del hash de cabeza en un log de transparencia.
- **A3 Camino rápido:** un round-trip para tareas dentro de `minimal`.
- **A4:** benchmark de firma y verificación en CI (< 1 ms/mensaje).
- **Claves en HSM/KMS** o en el agente del SO (N4).
- **Revisión externa de seguridad** antes de describir el proyecto como "seguro" en README o releases.
- **RFC-004 (borrador de alcance):** memoria compartida como tablón gobernado (N9): escritura por capacidad,
  entradas firmadas y encadenadas, lectura por ámbito, revocación por autor y caducidad, y solo datos tipados.

---

## Entregables por PR
- Descripción del PR con una tabla **ID de objetivo → cambio → test que lo demuestra → commit del test en rojo**.
- Sección "Cómo intentaría romper esto" (regla 9).
- Lista de decisiones de diseño tomadas y alternativas descartadas.
- Actualización de la tabla de invariantes en `CONCURRENCY-SETTLEMENT-SECURITY.md`.
- Al final de cada fase, un resumen: objetivos cumplidos, pendientes y riesgos que no se hayan podido cerrar.
