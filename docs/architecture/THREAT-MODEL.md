# THREAT MODEL: Red Cognitiva de Agentes (RFC-002)

> **Documento:** `docs/architecture/THREAT-MODEL.md`  
> **Fecha de Elaboración:** 2026-09-30  
> **Estado:** Documento Rector de Seguridad (Punto 1.1 / Fase 1)  
> **Área:** Modelo de Amenazas, Actores, STRIDE y Mitigaciones Criptográficas  
> **Referencias:** `docs/rfcs/002-objectives-and-non-goals.md`, `docs/rfcs/002-cognitive-agent-network.md`, `docs/architecture/CONCURRENCY-SETTLEMENT-SECURITY.md`

---

## 1. Alcance y Premisa de Seguridad

Este documento define el modelo formal de amenazas para la **Red Cognitiva de Agentes (RFC-002)** montada sobre Gentle Mesh. Establece los límites de confianza, las capacidades del adversario y el análisis sistemático STRIDE por endpoint, justificando los controles técnicos de seguridad (**S1–S9**) y robustez (**R1–R7**).

### Principio Fundamental
El protocolo asume que los procesos de ejecución de los agentes (LLMs, subprocesos de shell) son **intrínsecamente no confiables** (pueden alucinar, omitir pasos o ser manipulados vía prompt injection). La seguridad y la finalización del trabajo descansan exclusivamente en el **arnés Go nativo del host**, que evalúa precondiciones y aserciones de forma determinista y emite recibos criptográficos auditables.

---

## 2. Activos Críticos a Proteger (Assets)

| ID | Activo | Descripción | Impacto si se Compromete |
|---|---|---|---|
| **A1** | **Claves Privadas de Nodos** | Claves asimétricas Ed25519 (`pkg/keystore`) y pares mTLS (`/nodes/<id>`). | Suplantación de identidad (Spoofing) y firma fraudulenta de tareas y recibos. |
| **A2** | **Integridad Territorial Git** | Ramas y árboles de trabajo (`repo:branch`) en los hosts ejecutores. | Contaminación de código fuente o ejecución de código cruzado no autorizado. |
| **A3** | **Determinismo de Liquidación** | Veredicto falsable de las aserciones (`pkg/settlement`) ejecutadas por el arnés Go. | Aceptación de tareas rotas ("Green Checkbox fallacy") o fraude de completitud. |
| **A4** | **Cadena Inmutable de Recibos** | Historial encadenado por SHA-256 (`prev_receipt_hash`, `seq`) en SQLite WAL. | Repudio de acuerdos, reescritura de auditoría o disputas inconsistentes. |
| **A5** | **Contención del Host Ejecutor** | Sistema operativo, red y filesystem del nodo ejecutor frente a comandos arbitrarios. | RCE, fuga de credenciales o denegación de servicio del host. |

---

## 3. Actores y Modelo de Adversario

### 3.1 Actores Legítimos
- **Emisor (A):** Nodo que define y firma el `CognitiveTaskEnvelope`, encargando trabajo y comprometiéndose a aceptar o disputar.
- **Ejecutor (B):** Nodo que valida localmente precondiciones, ejecuta la tarea en su territorio y evalúa aserciones con su arnés.
- **Auditor / Tercero (C):** Nodo independiente que verifica la validez criptográfica de la cadena de recibos sin participar en la ejecución.

### 3.2 Capacidades del Adversario
El modelo contempla cuatro perfiles de atacante:
1. **Atacante de Red (MitM / Eavesdropper):** Controla el enlace de red entre nodos; puede interceptar, alterar, descartar o reinyectar tráfico HTTP/SSE.
2. **Nodo de Malla Hostil / Malicioso:** Posee credenciales mTLS válidas pero envía envelopes maliciosos, solicita capacidades abusivas o emite recibos falsos.
3. **Agente Ejecutor Desalineado / Alucinante:** Un subproceso LLM en B que falla silenciosamente, devuelve exit code 0 falso o intenta alterar los ficheros de prueba para forzar el paso de aserciones.
4. **Inyector de Estado / Deserialización:** Atacante que intenta inyectar payloads estructurados para explotar motores de deserialización dinámica.

---

## 4. Análisis de Vulnerabilidades y Resiliencia en Concurrencia

### 4.1 Vulnerabilidades del Estado del Arte (CVEs Verificados)
Los frameworks multi-agente contemporáneos han sufrido vulnerabilidades críticas derivadas de la deserialización de estado y la confianza ciega en datos estructurados:

- **CVE-2025-64439 (LangGraph Checkpoint RCE en `JsonPlusSerializer`):** Ejecución remota de código derivada de deserializar objetos arbitrarios durante la recuperación de checkpoints de agentes.
- **CVE-2026-28277 (LangGraph Deserialización insegura msgpack):** Reconstrucción de objetos no seguros desde el almacén de persistencia permitiendo compromiso total del host.

**Mitigación en Gentle Mesh (S6 - Sin ejecución de datos):**
1. **Serialización Canónica Estricta:** Uso exclusivo de **RFC 8785 JCS** (`pkg/jcs`).
2. **Rechazo de Campos Desconocidos:** Todos los decodificadores de red configuran `json.Decoder.DisallowUnknownFields()`.
3. **Cero Polimorfismo Dinámico:** Los tipos son Go structs fijas y estrictamente tipadas; ningún camino de código admite deserialización dinámica, `interface{}` arbitrario ni evaluación de strings como código.

### 4.2 Vulnerabilidad Crítica Descubierta: DoS por Concurrencia en `s.leases` (PR #30)
- **Descripción:** En el servidor de agentes (`integration/agent/server.go`), las peticiones simultáneas a `POST /envelopes` (`handleSubmitEnvelope`) y `POST /leases` (`handleCreateLease`) mutaban concurrentemente el mapa en memoria `s.leases` sin sincronización. En el runtime de Go, las colisiones de escritura en mapas no sincronizados provocan un pánico inmediato e irrecuperable (`fatal error: concurrent map writes`), cerrando de golpe el socket TCP (`connection reset by peer` / `EOF`) y causando una Denegación de Servicio (DoS) total del nodo ejecutor.
- **Descubrimiento Empírico:** Detectado en CI durante los tests distribuidos de integración (`TestWU13_ConcurrentWritesToSameExecutor` y `TestWU15_FanOutE2E`).
- **Mitigación Técnica (PR #30 - `fix/agent-server-leases-mutex`):** Incorporación de `leasesMu sync.RWMutex` en `Server`, sincronización con `Lock()` en escrituras, lector seguro `GetLease()` con `RLock()`, y test de regresión in-process `TestServer_ConcurrentLeaseWrites` bajo `go test -race`.

---

## 5. Matriz STRIDE por Endpoint

Todas las rutas analizadas corresponden exactamente a los handlers registrados en el código fuente del proyecto, validadas automáticamente mediante `TestThreatModel_AllRegisteredRoutesCovered`.

### 5.1 Servidor de Agentes / Ejecutor RFC-002 (`integration/agent/server.go` y `server_harness.go`)

| Ruta / Patrón | Método | Fichero Fuente | Función Handler | Amenazas STRIDE | Vector de Ataque y Mitigación Técnica |
|---|---|---|---|---|---|
| `POST /envelopes` | POST | `integration/agent/server.go` | `handleSubmitEnvelope` | **S, T, R, I, D, E** | **S:** Spoofing de emisor (mitigado con **S1** mTLS y **S2** firma Ed25519). **T:** Alteración de aserciones en tránsito (mitigado con hash JCS RFC 8785). **R:** Repudio de misión (firma no repudiable en SQLite WAL). **I:** Fuga de intención (mTLS cifrado). **D:** Payload flooding y colisión en mapa (mitigado con **S6** límites JSON y **PR #30** `leasesMu`). **E:** Solicitud de capacidades no permitidas (mitigado con **S3** deny-by-default). |
| `POST /leases` | POST | `integration/agent/server.go` | `handleCreateLease` | **S, T, R, D** | **S/T:** Precondiciones falsificadas (evaluación local estricta en host B). **R:** Repudio de aceptación (lease firmado por ejecutor con timestamp UTC). **D:** Pánico por escritura concurrente en leases (resuelto con `sync.RWMutex` en **PR #30**); agotamiento de leases (**R2** expiración determinista con timeout local). |
| `POST /settle` | POST | `integration/agent/server.go` | `handleSettle` | **S, T, R, D, E** | **S:** Cierre de tarea por nodo no asignado (**S1/S2** validación de identidad y firma de B). **T:** Manipulación de veredicto de aserciones (**A3/S4** arnés Go ejecuta tests/git fuera del control del LLM). **R:** Repudio de liquidación (recibo firmado y encadenado). **D:** Bloqueo en settling (**R2/R6** timeout estricto). **E:** Escape territorial fuera de workspace (**S4/A2** confinamiento en Git). |
| `POST /accept` | POST | `integration/agent/server.go` | `handleAccept` | **S, T, R, D, E** | **S/T:** Aceptación fraudulenta por tercero (verificación de firma de A y coincidencia con `EmitterAgentID`). **R:** Repudio de aceptación (contrafirma persistida en SQLite). **D:** Bifurcación de cadena por aceptaciones concurrentes (**S7/R4** `SaveReceipt` bajo mutex con `seq INTEGER`). **E:** Replay entre redes (**S9** verificación de `mesh_id`). |
| `POST /dispute` | POST | `integration/agent/server.go` | `handleDispute` | **S, T, R, D, E** | **S/T:** Disputa apócrifa o manipulación de causa (firma canónica de A sobre motivo y evidencia). **R:** Repudio de disputa (contrafirma registrada). **D/E:** Inyección de disputas masivas o cross-mesh (**S7/S9** validación estricta de hash previo y `mesh_id`). |
| `POST /verify` | POST | `integration/agent/server.go` | `handleVerifyReceipt` | **S, T, I, D** | **S/T:** Presentación de recibos con firmas o aserciones adulteradas (recálculo de hash canónico JCS y verificación criptográfica Ed25519). **I:** Fuga de estado a nodos no autorizados (autenticación mTLS). **D:** DoS por verificación de firmas complejas (algoritmo Ed25519 de tiempo constante y acotado). |
| `POST /verify-chain` | POST | `integration/agent/server.go` | `handleVerifyChain` | **S, T, D** | **S/T:** Inyección de eslabones falsos o saltos en la cadena (validación lineal estricta de `prev_receipt_hash`, secuencias monótonas y firmas acumuladas). **D:** Cadenas cíclicas o excesivamente largas (validación acotada por profundidad de histórico). |
| `GET /receipts/` | GET | `integration/agent/server.go` | `handleGetReceipt` | **I, D** | **I:** Exfiltración de recibos por clientes no autorizados (restringido a nodos de la malla vía mTLS). **D:** Peticiones masivas sobre SQLite (SQLite WAL con `busy_timeout=5000` y `MaxOpenConns(1)`). |
| `GET /chain` | GET | `integration/agent/server.go` | `handleGetChain` | **I, D** | **I:** Enumeración de todo el historial de liquidaciones (protegido por canal seguro mTLS). **D:** Descarga intensiva de la cadena completa (lectura paginada o acotada en memoria). |
| `/health` | GET | `integration/agent/server.go` | `handleHealth` | **I, D** | **I:** Reconocimiento de rol (`RoleEmitter`/`RoleExecutor`) y clave pública (información operativa básica). **D:** Flooding HTTP (endpoint ligero sin consultas a base de datos). |
| `/status` | GET | `integration/agent/server.go` | `handleStatus` | **I, D** | **I:** Fuga de número de leases activos o estado interno del arnés (canal mTLS). **D:** Consulta excesiva de estado (lectura protegida por `RLock` y métricas en memoria). |
| `POST /execute` | POST | `integration/agent/server_harness.go` | `handleExecute` | **E, T, D** | **Endpoint Exclusivo de Test Harness:** Ejecuta comandos de shell en el workspace para pruebas de integración. **Riesgo:** Ejecución arbitraria de comandos (RCE). **Mitigación:** Excluido absolutamente en binarios de producción mediante build tag `//go:build testharness` (stub no-op en `server_harness_stub.go` probado por `TestEndpointsAbsentInProduction`). |
| `POST /inject-receipt` | POST | `integration/agent/server_harness.go` | `handleInjectReceipt` | **T, S** | **Endpoint Exclusivo de Test Harness:** Inyecta recibos arbitrarios en `ChainStore` para simular bifurcaciones o ataques. **Riesgo:** Falsificación de historial. **Mitigación:** Excluido en producción vía build tag `//go:build testharness`. |

---

### 5.2 Coordinador de Malla Gentle Mesh (`pkg/server/http/handlers.go`)

| Ruta / Patrón | Método | Fichero Fuente | Función Handler | Amenazas STRIDE | Vector de Ataque y Mitigación Técnica |
|---|---|---|---|---|---|
| `GET /healthz` | GET | `pkg/server/http/handlers.go` | `handleHealthz` | **I, D** | **I/D:** Sondeo de uptime y estado de TLS por agentes externos; flooding de comprobaciones (respuesta estática ligera en memoria sin I/O de disco). |
| `GET /v1/mesh/ca` | GET | `pkg/server/http/handlers.go` | `handleMeshCA` | **S, T** | **S/T:** Descarga pública de certificado CA de la malla. **Mitigación:** Descarga verificada por el nodo mediante pinning criptográfico (`-ca-cert-hash` sha256) antes de confiar en la CA (`pkg/tlsutil.PinnedBootstrapConfig`). |
| `POST /v1/certs/enroll` | POST | `pkg/server/http/handlers.go` | `handleCertsEnroll` | **S, T, I** | **S:** Enrolamiento no autorizado de nodos (requiere token de un solo uso). **T:** Ataque MitM durante el enrolamiento (verificación de cadena y `ServerName`). **I:** Fuga de clave privada (clave privada generada localmente en cliente, jamás enviada al servidor; Issue #4 / PR #26). |
| `POST /v1/mesh/join` | POST | `pkg/server/http/handlers.go` | `handleMeshJoin` | **S, D** | **S:** Nodo ilegítimo intenta unirse a la malla (requiere certificado cliente válido emitido por la CA de la malla). **D:** Saturación de registros de nodos (registro acotado y protegido por store SQLite). |
| `POST /v1/mesh/heartbeat` | POST | `pkg/server/http/handlers.go` | `handleMeshHeartbeat` | **S, D** | **S:** Suplantación de estado de vida de nodos (mTLS obligatorio, nodeID ligado al certificado). **D:** Tormenta de heartbeats (actualización eficiente de timestamp en memoria/store). |
| `GET /v1/mesh/nodes` | GET | `pkg/server/http/handlers.go` | `handleMeshNodes` | **I** | **I:** Enumeración de nodos activos, capacidades y cargas de trabajo (restringido a clientes mTLS autorizados). |
| `GET /v1/mesh/radar` | GET | `pkg/server/http/handlers.go` | `handleMeshRadar` | **I** | **I:** Exfiltración de la topología y latencias de la malla (canal mTLS autenticado). |
| `POST /v1/mesh/peers/register` y `POST /v1/mesh/peers` | POST | `pkg/server/http/handlers.go` | `handleMeshPeerRegister` | **S, E, T** | **S/E:** Registro de peers no autorizados en federación (requiere autenticación mTLS y privilegios de nodo coordinador). **T:** Enrutamiento a endpoints maliciosos (validación de URLs y certificados del peer). |
| `GET /v1/mesh/peers` | GET | `pkg/server/http/handlers.go` | `handleMeshPeersList` | **I** | **I:** Reconocimiento de topología federada entre coordinadores (canal seguro mTLS). |
| `DELETE /v1/mesh/peers/{id}` | DELETE | `pkg/server/http/handlers.go` | `handleMeshPeerDelete` | **D, S** | **D/S:** Desconexión maliciosa de enlaces federados (requiere autenticación estricta y control de acceso por peer ID). |
| `POST /v1/mesh/peers/{id}/sync` | POST | `pkg/server/http/handlers.go` | `handleMeshPeerSync` | **T, D** | **T/D:** Inyección de sincronización de estado corrupto entre peers o bucles de sincronización infinita (validación de firmas y marcas de agua). |
| `GET /v1/mesh/territory` | GET | `pkg/server/http/handlers.go` | `handleMeshTerritory` | **I, T** | **I/T:** Lectura y manipulación de semáforos territoriales (`repo:branch`). Mitigado con exclusión mutua de territorio en SQLite para evitar colisiones entre agentes. |
| `POST /v1/tasks` | POST | `pkg/server/http/handlers.go` | `handleCreateTask` | **S, T, D, E** | **S/T:** Inyección de tareas no autorizadas o malformadas (validación estricta de esquema y serialización). **D:** Creación masiva de tareas (límites de cola en scheduler). **E:** Solicitud de acceso territorial restringido. |
| `GET /v1/tasks/{id}` | GET | `pkg/server/http/handlers.go` | `handleGetTask` | **I** | **I:** Acceso no autorizado a descripciones, prompts o resultados de tareas (restringido a nodos de la malla con mTLS). |
| `GET /v1/tasks/{id}/events` | GET | `pkg/server/http/handlers.go` | `handleTaskEvents` | **I, D** | **I:** Espionaje de SSE stream con pensamientos, herramientas y tokens del agente en tiempo real (mTLS obligatorio). **D:** Conexiones SSE huérfanas saturando el servidor (gestión de `r.Context().Done()` con timeouts). |
| `POST /v1/tasks/{id}/reply` | POST | `pkg/server/http/handlers.go` | `handleTaskReply` | **S, T** | **S/T:** Inyección de respuestas apócrifas o respuestas fuera de orden (validación de estado de la tarea en scheduler). |
| `POST /v1/tasks/{id}/cancel` | POST | `pkg/server/http/handlers.go` | `handleTaskCancel` | **S, D** | **S/D:** Cancelación maliciosa de tareas legítimas de otros agentes (autorización ligada a la identidad del emisor o coordinador). |
| `POST /v1/tasks/{id}/checkpoint` | POST | `pkg/server/http/handlers.go` | `handleTaskCheckpoint` | **T, E** | **T/E:** Explotación estilo CVE-2025-64439 (inyección de checkpoints maliciosos). Mitigado con **S6**: deserialización tipada en Go struct fija sin evaluación dinámica ni `JsonPlusSerializer`. |
| `GET /v1/tasks/{id}/checkpoint` | GET | `pkg/server/http/handlers.go` | `handleGetCheckpoint` | **I** | **I:** Exfiltración de estado serializado de memoria del agente (canal mTLS autenticado). |
| `GET /v1/workspace/tree` | GET | `pkg/server/http/handlers.go` | `handleWorkspaceTree` | **I, E** | **I/E:** Path traversal fuera del workspace. Mitigado con `filepath.Clean`, rechazo de rutas relativas con `..` y validación de prefijo contra `WorkspaceDir`. |
| `GET /v1/workspace/file` | GET | `pkg/server/http/handlers.go` | `handleWorkspaceFile` | **I, E** | **I/E:** Lectura arbitraria de ficheros del sistema operativo (`/etc/passwd`, claves privadas). Mitigado con confinamiento estricto al workspace e inspección de symlinks. |
| `POST /v1/webhooks` | POST | `pkg/server/http/handlers.go` | `handleCreateWebhook` | **S, E, D** | **S/E:** Registro de webhooks maliciosos para SSRF contra redes internas o exfiltración. Mitigado con validación de URL y permisos de administración. |
| `GET /v1/webhooks` | GET | `pkg/server/http/handlers.go` | `handleListWebhooks` | **I** | **I:** Enumeración de endpoints de notificación y secretos asociados (canal mTLS). |
| `DELETE /v1/webhooks/{id}` | DELETE | `pkg/server/http/handlers.go` | `handleDeleteWebhook` | **S, D** | **S/D:** Borrado no autorizado de webhooks legítimos (verificación de identidad y autorización por ID de webhook). |

---

## 6. Mapeo Sistemático de Mitigaciones a Objetivos RFC-002

| Objetivo | Denominación | Mitigación STRIDE Principal | Invariante Implementada |
|---|---|---|---|
| **S1** | Identidad fuerte | Spoofing en transporte | mTLS obligatorio (`RequireAndVerifyClientCert`), CN/SAN == `EmitterAgentID` (403). |
| **S2** | Verificar antes de actuar | Spoofing & Tampering | Firma Ed25519 verificada antes de precondiciones, liquidación, aceptación y disputa. |
| **S3** | Deny-by-default | Elevation of Privilege | Perfil mínimo por defecto (sin red, sin exec); rechazo explícito `REJECTED_CAPABILITY`. |
| **S4** | Política en host | Elevation of Privilege | Políticas impuestas por arnés Go fuera del alcance de modificación del LLM. |
| **S6** | Sin ejecución de datos | Tampering, RCE, DoS | JCS RFC 8785, `DisallowUnknownFields()`, structs fijas, sin deserialización dinámica. |
| **S7** | Integridad de la historia | Tampering & Repudiation | Cadena criptográfica SHA-256 validada **al escribir** bajo mutex (`ErrChainBroken`). |
| **S8** | Revocación | Spoofing tras compromiso | Kill switch y control de ciclo de vida de certificados sin retención de claves privadas. |
| **S9** | Sin downgrade | Tampering & Protocol Attack | `protocol_version` "2" estricto; rechazo inmediato de v1 o versiones desconocidas. |
| **R1** | Idempotencia | Replay & DoS | Deduplicación por `envelope_id`; reenvíos devuelven el recibo existente. |
| **R2** | Leases que caducan | DoS por estancamiento | Todo lease expira localmente; terminación determinista a `TASK_STATE_FAILED`. |
| **R4** | Concurrencia correcta | DoS & Desincronización | Columna `seq INTEGER` monótona asignada atómicamente; sincronización con `sync.RWMutex`. |
| **R7** | Firmas deterministas | Falsos positivos en firma | Timestamps normalizados RFC 3339 UTC; 1000 iteraciones con 0 fallos bajo `-race`. |

---

## 7. No-Objetivos y Fronteras de Responsabilidad (N1–N9)

El modelo reconoce explícitamente qué amenazas **no** cubre el protocolo, delegándolas en la infraestructura o el diseño organizativo:
- **N1 (Daño en perímetro):** Si a un agente se le concede escribir en `src/`, puede escribir código erróneo dentro de `src/` (cubierto por revisión humana y aserciones).
- **N2 (Aislamiento de Kernel):** Escapes de contenedor o kernel bugs escapan a Gentle Mesh (cubierto por microVMs, gVisor o nsjail).
- **N4 (Host Comprometido):** Si el host del ejecutor es vulnerado a nivel root, sus firmas pierden validez (cubierto por anclaje externo de recibos y HSMs).
- **N9 (Cognición no gobernada):** La red no es una mente colectiva ni ofrece canales libres de memoria compartida (ver RFC-004 para tablones gobernados tipados).
