# Roadmap

Roadmap público de Gentle Mesh. Este documento refleja el **estado real y verificado** del proyecto, diferenciando lo completado con pruebas automatizadas, los hallazgos técnicos en seguimiento y las decisiones estratégicas abiertas. **No hay fechas de entrega** comprometidas; el proyecto se rige por evidencia y consenso comunitario bajo la supervisión de Gentle AI.

---

## 1. Lo cerrado (Realidad implementada y verificada)

### Fase 0 — Saneamiento y Fundaciones de Seguridad
- **Cierre de brechas críticas:** Implementación de esquemas SQLite en modo WAL, consumo atómico e idempotencia en tokens de enrollment (issues #29, #42, #6b).
- **Serialización determinista:** Normalización de firmas criptográficas Ed25519 sobre canonicalización JSON (JCS / RFC 8785) para envelopes y contratos.
- **Seguridad por defecto (v1.1.0):** Cierre de configuraciones inseguras (PR #107 / issue #57) y ciclo completo de revocación de certificados mTLS con desconexión atómica de nodos (PRs #110 y #111 / issue #25).

### Fase 1 — Arquitectura y Visibilidad de Seguridad
- **Hub de Arquitectura y Puertas de Verificación:** Creación de los portales interactivos y diagramas SVG/HTML autónomos (Archify) con el mapeo de compuertas técnicas (G1 a G5).
- **Radar de Ámbitos y Control Territorial:** Detección de colisiones a nivel de superficies (`edit_surfaces`) y cerrojos exclusivos de rama (`git_branch`) con cuatro modos de conflicto (`queue`, `warn`, `strict`, `disabled`).

### Conformidad Total del Perfil Mínimo RFC-002 (Liquidación entre Agentes)
Implementación y endurecimiento de los 12 objetivos del perfil mínimo conforme integrados y demostrados por tests automatizados en `main` (el umbrella issue [#70](https://github.com/Rafaeldelinares/gentle-mesh/issues/70) queda formalmente cerrado con el 100% de los requisitos del perfil de 12 cumplidos):
- **S1 (mTLS Identity):** Validación de identidad criptográfica ligada al certificado de transporte (PR #112 & PR #113 / issue #66).
- **S2 (Firma de Emisor):** Registro y verificación de la firma del emisor previa a la mutación (PR #114 / issue #67).
- **S3 (Capacidades Deny-by-Default):** Validación estricta con `REJECTED_CAPABILITY` ante capacidades no concedidas y soporte de perfil mínimo sin capacidades (PR #136).
- **S6 (Sin ejecución de datos / Cero subshell):** Eliminación total de `sh -c` en precondiciones mediante `exec.LookPath`, tokenización léxica y allowlist binaria estricta con 0 excepciones en security gates (PR #121, PR #129, PR #130, PR #133 / issues #35, #130).
- **S7 (Integridad de la historia y contrafirmas):** Preservación de `previous_receipt_hash` en el recibo y contrafirma del emisor bajo dominio criptográfico separado (`ComputeEmitterReceiptHash`) (PR #135 / issues #43, #48).
- **S9 (Sin downgrade):** Cumplimiento estricto del protocolo v2 y rechazo de versiones incompatibles.
- **R1 (Idempotencia en Settlement):** Procesamiento único de liquidaciones con mitigación de colisiones de timestamps UUIDv7 bajo RFC 9562 (PR #115 / issue #68).
- **R2 (Expiración de Leases):** Monitor de expiración con reaper autónomo y emisión determinista de recibos `SETTLEMENT_TIMEOUT` (PR #116 / issue #69).
- **R5 (Estados terminales claros):** Inclusión del estado absorbente `RESOLVED` para disputas y máquina de estados formal validada por CAS (PR #134).
- **R7 (Firmas deterministas):** Verificación de firma determinista en 1000 iteraciones sobre SQLite.
- **A1 (Seguro por defecto, sin configurar):** Encargo y liquidación de tareas de bajo riesgo sin declarar capacidades ni requerir configuración previa en el nodo ejecutor (PR #137).
- **A2 (Descubrimiento por intento):** Inspección de precondiciones y capacidades estructuradas sin efectos secundarios mediante `POST /preflight` (PR #132 / issue #37).

### Fase 2 — Validación con Agentes y Hardware Real
Demostración empírica de las dos preguntas centrales del protocolo A2A mediante ejecución real:
1. **El protocolo funciona bajo ejecución real (F2-1b a F2-3b):**
   - Ejecución controlada de subprocesos Pi dentro de sandbox Bubblewrap (`bwrap`) sobre worktrees efímeros (PR #118).
   - Resiliencia y crash recovery: detección de caída de workers (`NODE_LOST`), reaper con timeout y re-despacho automático a nodos vivos (PR #119).
   - Medición empírica de costos: captura de uso de tokens y evaluación del overhead de paralelismo vs ejecución serial (2.28x–2.34x) junto a la amortización de caché (PR #121).
2. **Capacidad de resolución autónoma no trivial (F2-5 a F2-7):**
   - Validación física sobre red local (LAN) contra un worker dedicado Debian 13 (NiPoGi), resolviendo el stub de DNS de `systemd-resolved` en `bwrap` (PR #124).
   - Resolución de tarea sintética compleja (F2-7): despacho de una caché concurrente LRU con expiración por TTL en Go; el agente iteró su propio ciclo TDD dentro del sandbox, corrigió fallos y entregó 21/21 tests pasando con `-race` sin condiciones de carrera (PR #128).

### Conformidad Total de RFC-003 (Protocolo de Unión y Adaptador A2A)
Diseño, implementación y verificación de todos los objetivos de RFC-003 integrados y demostrados en `main`:
- **J1 (Formato de evento encadenado):** Log inmutable de eventos encadenados deterministamente por SHA-256 (`prev_hash`) (PR [#168](https://github.com/Rafaeldelinares/gentle-mesh/pull/168) / issue [#166](https://github.com/Rafaeldelinares/gentle-mesh/issues/166)).
- **J2 (Serialización sin colisión):** Cadena lineal estricta bajo concurrencia implementada con Opción C (`BEGIN IMMEDIATE` + `UNIQUE(prev_hash)` en SQLite) (PR [#168](https://github.com/Rafaeldelinares/gentle-mesh/pull/168)).
- **J3 (Snapshot con doble digest):** `GET /v1/mesh/snapshot` sirviendo posición de cadena inmutable y digest semántico JCS (`state_digest = SHA256(JCS(state))`) (PR [#172](https://github.com/Rafaeldelinares/gentle-mesh/pull/172) / issue [#171](https://github.com/Rafaeldelinares/gentle-mesh/issues/171)).
- **J4 (Stream SSE reanudable):** `GET /v1/mesh/events` con cursor monótono `id: <seq>:<hash>`, soporte de `Last-Event-ID` y detección de bifurcación con HTTP 409 Conflict (PR [#172](https://github.com/Rafaeldelinares/gentle-mesh/pull/172)).
- **J5 (Verificación territorial oportunista):** El arnés fuera del sandbox bwrap contrasta los archivos tocados por `git diff BaseRef..HEAD` contra las superficies declaradas, emitiendo evidencia `territory.verified` en el log con endurecimiento de argumentos git (PR [#175](https://github.com/Rafaeldelinares/gentle-mesh/pull/175) / issue [#173](https://github.com/Rafaeldelinares/gentle-mesh/issues/173)).
- **J6 (Reconstrucción desde cero):** Proyección determinista de estado en SQLite derivada exclusivamente del log, con expiraciones de lease como eventos explícitos (`reason: "lease_expired"`), eliminando dependencias de reloj de pared (PR [#170](https://github.com/Rafaeldelinares/gentle-mesh/pull/170) / issue [#169](https://github.com/Rafaeldelinares/gentle-mesh/issues/169)).
- **Punto 4.5 (Admisión autenticada mTLS S1):** `POST /v1/mesh/join` atado a certificado cliente (`Subject.CommonName == req.NodeID`), validación canónica de claves Ed25519 en curva Edwards (RFC 8032 §5.1.3, $y < 2^{255}-19$, sin claves todo-ceros) y atomicidad estricta (JoinLog primero, Projector segundo, Registry tercero) (PR [#181](https://github.com/Rafaeldelinares/gentle-mesh/pull/181) / issue [#180](https://github.com/Rafaeldelinares/gentle-mesh/issues/180)).
- **A1/A2 (Adaptador A2A y Agent Card firmada):** `GET /.well-known/agent-card.json` servido en producción con sobre compacto JWS sobre cuerpo canónico JCS con Ed25519 (PR [#183](https://github.com/Rafaeldelinares/gentle-mesh/pull/183) / issue [#182](https://github.com/Rafaeldelinares/gentle-mesh/issues/182)). El coordinador sirve la card del host local y distribuye workers sin firmar en su nombre (§4.2.1.2).
- **C1–C4 (Modelo de capacidades en dos capas):** Especificación formal documentada en §4.3 distinguiendo Capa Agente de Capa Nodo/Worker, Tier 1 instantáneo en pre-flight, Tier 2 observado en JoinLog y diagnósticos tipados (PR [#185](https://github.com/Rafaeldelinares/gentle-mesh/pull/185)).

### Saneamiento de Fiabilidad e Infraestructura (Cerrado en main)
- **Persistencia SQLite con Pragmas en DSN:** `busy_timeout(5000)` y `journal_mode(WAL)` transportados en el DSN con percent-encoding seguro para todo el pool de conexiones (PR [#178](https://github.com/Rafaeldelinares/gentle-mesh/pull/178) / issue [#177](https://github.com/Rafaeldelinares/gentle-mesh/issues/177)).
- **Sincronización determinista en CI (cero esperas por reloj):** Eliminación de sleeps fijos de 800ms en `TestTaskManager_CleanupExpired` (reemplazado por sondeo de condición) y timeouts fijos de 200ms en `TestSilentNodeConcurrentEOFAndSweepSingleTransitionAndEvent` (reemplazado por cierre de canal con timeout de guarda de 2s) (PR [#184](https://github.com/Rafaeldelinares/gentle-mesh/pull/184) / issues [#164](https://github.com/Rafaeldelinares/gentle-mesh/issues/164) y [#176](https://github.com/Rafaeldelinares/gentle-mesh/issues/176)).

---

## 2. Hallazgos técnicos y deuda trackeada (Issues abiertos)

El proyecto mantiene un registro transparente de sus límites y trabajo pendiente:

- **[#117] Documentación de seguridad desactualizada:** `SECURITY.md` afirma en sus limitaciones conocidas que no existe revocación de certificados, cuando los PRs #110 y #111 ya la implementaron en `main`.
- **[#120] Recolección de residuos (GC de worktrees y ramas):** Falta de mecanismo automático para limpiar directorios de trabajo efímeros y ramas de propuesta huérfanas tras intentos fallidos o caídas de workers.
- **[#122] Sobrecarga de contexto en sandbox:** Medición e instrumentación del overhead de ~14k tokens de contexto base que la directiva `--isolated` de `gentle-shell` introduce sobre el suelo de Pi.
- **[#125] Buffer fijo en parser SSE:** `protocol.ParseSSE` utiliza el buffer por defecto de 64 KB de `bufio.Scanner`, provocando fallos por `token too long` cuando las herramientas devuelven salidas o argumentos voluminosos.
- **[#126] Límite de tool calls por tarea:** El límite fijo de 15 llamadas a herramientas en `gentle-shell` resulta insuficiente para tareas complejas con ciclos iterativos de TDD; debe ser configurable por tarea o runner.
- **[#147] Concurrencia multi-worker en un solo host:** Endurecimiento del soporte para ejecutar múltiples subprocesos de workers compitiendo intensivamente sobre el mismo hardware físico.
- **[#156] Exposición fuera de la LAN / WAN Zero-Trust:** El transporte actual asume una red local (LAN) o VPN privada; la exposición directa a internet pública requiere defensas adicionales de rate-limiting, WAF y autenticación de borde.
- **[#150] / [#63] Webhooks con firma HMAC:** Disparador de notificaciones HTTP asíncronas con firma criptográfica de eventos.
- **[#160] Ligado de SAN a node_id en PKI:** `pki.SignCSR` valida el CommonName pero no restringe ni valida exhaustivamente los Subject Alternative Names (SANs) contra el `node_id`.
- **[#174] Límite de la observación territorial por `.gitignore`:** Archivos creados sin commitear cuyas extensiones coincidan con `.gitignore` no aparecen en `git status --porcelain` ni en `git diff BaseRef..HEAD`, escapando a la verificación territorial a posteriori.

---

## 3. Decisiones pendientes y próximos pasos

Con RFC-001, RFC-002 y RFC-003 formalmente cerradas y verificadas en `main`, las siguientes líneas de trabajo están abiertas:

1. **Presentación a la comunidad Gentle AI:**  
   Someter la arquitectura completa, la evidencia física de Fase 2, la liquidación determinista (RFC-002) y el log de unión / A2A (RFC-003) a la revisión y orientación de Alan Buscaglia y la comunidad de Gentle AI.
2. **Puesta en producción del backlog en workers remotos:**  
   Utilizar la infraestructura física validada (coordinador + worker NiPoGi en LAN con `-runner gentle-shell`) para ejecutar y resolver tareas reales del propio backlog de `gentle-mesh` de forma distribuida y autónoma.
3. **Guía de adopción y puesta en marcha:**  
   Publicar la guía de uso real de producción ([docs/GETTING-STARTED.md](docs/GETTING-STARTED.md)) para permitir que colaboradores desplieguen clústeres propios con mTLS y sandbox bwrap.

---

## Lo que no haremos

Esta lista delimita las fronteras del proyecto:

- **No convertiremos Gentle Mesh en un orquestador de sesiones remotas de pi:** Pi resuelve sesiones remotas interactivas (`pi-protocol`, `pi-server`); Gentle Mesh resuelve coordinación territorial, contratos y balanceo de carga entre múltiples nodos.
- **No añadiremos parseo de AST ni análisis estático del código para detectar colisiones:** El coordinador opera exclusivamente sobre los contratos y superficies declaradas por cada tarea; no inspecciona el código fuente.
- **No prometeremos compatibilidad binaria entre versiones** mientras la arquitectura se encuentre en fase de especificación (PoC / RFC).
- **No ofreceremos SLAs ni soporte comercial:** El canal de evolución es la comunidad de código abierto.

---

## Cómo participar

- Para reportar problemas o proponer mejoras, abrir un issue detallando el comportamiento observado o la necesidad técnica.
- Consultar las etiquetas correspondientes (`bug`, `enhancement`, `documentation`, `feat/rfc-002`).
- Toda contribución de código debe mantener las compuertas de seguridad (`scripts/security-gates.sh`), pruebas con detector de carreras (`go test -race`) y trazabilidad de evidencia.
