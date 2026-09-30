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

## 4. Análisis de Vulnerabilidades del Estado del Arte (CVEs Verificados)

Los frameworks multi-agente contemporáneos han sufrido vulnerabilidades críticas derivadas de la deserialización de estado y la confianza ciega en datos estructurados:

- **CVE-2025-64439 (LangGraph Checkpoint RCE en `JsonPlusSerializer`):** Ejecución remota de código derivada de deserializar objetos arbitrarios durante la recuperación de checkpoints de agentes.
- **CVE-2026-28277 (LangGraph Deserialización insegura msgpack):** Reconstrucción de objetos no seguros desde el almacén de persistencia permitiendo compromiso total del host.

### Mitigación en Gentle Mesh:
Gentle Mesh neutraliza esta clase completa de vulnerabilidades mediante **S6 (Sin ejecución de datos)**:
1. **Serialización Canónica Estricta:** Uso exclusivo de **RFC 8785 JCS** (`pkg/jcs`).
2. **Rechazo de Campos Desconocidos:** Todos los decodificadores de red configuran `json.Decoder.DisallowUnknownFields()`.
3. **Cero Polimorfismo Dinámico:** Los tipos son Go structs fijas y estrictamente tipadas; ningún camino de código admite deserialización dinámica, `interface{}` arbitrario ni evaluación de strings como código.

---

## 5. Matriz STRIDE por Endpoint

### 5.1 Endpoint `/v1/envelopes/submit` (o `/v1/tasks/negotiate`)

| Amenaza (STRIDE) | Vector de Ataque | Mitigación Técnica (Objetivos) |
|---|---|---|
| **S** (Spoofing) | Un atacante se hace pasar por un emisor legítimo para comisionar tareas. | **S1** (mTLS obligatorio; CN/SAN del certificado del cliente coincide con `EmitterAgentID` → 403 si discrepa) + **S2** (verificación de `EmitterSignature` contra `KnownAgents`). |
| **T** (Tampering) | Modificación del sobre en tránsito (alteración de aserciones o repo). | **S2** (firma Ed25519 sobre hash canónico JCS RFC 8785) + **S9** (`protocol_version` "2" y `mesh_id` cubiertos por la firma). |
| **R** (Repudiation) | El emisor niega haber emitido o autorizado el contrato. | **S2** (firma criptográfica no repudiable) + persistencia inmutable en SQLite WAL. |
| **I** (Info Disclosure) | Espionaje de intenciones o secretos en tránsito. | **S1** (canal TLS 1.2+ cifrado mTLS) + CA pinning en bootstrap (`pkg/tlsutil`). |
| **D** (Denial of Service) | Envío de payloads enormes o infinitos loops de reintento. | **S6** (`DisallowUnknownFields`, límites HTTP de lectura) + **R2** (timeout local de lease). |
| **E** (Elevation of Priv) | El sobre exige capacidades no autorizadas (ej. exec o red). | **S3** (Deny-by-default; sin capacidades = perfil mínimo) + **S4** (política del ejecutor en host, inviolable por el agente). |

---

### 5.2 Endpoint `/v1/tasks/settle`

| Amenaza (STRIDE) | Vector de Ataque | Mitigación Técnica (Objetivos) |
|---|---|---|
| **S** (Spoofing) | Un nodo ajeno envía un recibo falso intentando cerrar la tarea. | **S1** (mTLS verificado) + **S2** (`ExecutorSignature` verificada contra la clave registrada de B). |
| **T** (Tampering) | El ejecutor altera el resultado de las aserciones para ocultar un fallo. | **A3/S7** (el arnés Go del host ejecuta directamente los checks de git y exit codes de forma aislada del LLM; el resultado no proviene del agente) + **S7** (`envelope_hash` correlacionado). |
| **R** (Repudiation) | El ejecutor niega haber emitido un veredicto de liquidación. | **S2/S7** (`ExecutorSignature` sobre el recibo canónico con timestamp UTC RFC 3339). |
| **I** (Info Disclosure) | Fuga de logs sensibles de tests o diffs en evidencia. | Aislamiento de evidencia; **N1** (la aserción produce un veredicto booleano estructurado). |
| **D** (Denial of Service) | Bloqueo indefinido en fase `SETTLING`. | **R2/R6** (el arnés aplica timeout estricto con su propio reloj y emite `SETTLEMENT_TIMEOUT` / `TASK_STATE_FAILED`). |
| **E** (Elevation of Priv) | El agente modifica ficheros fuera del workspace durante la liquidación. | **S4** (confinamiento del arnés y comprobación territorial estricta en Git). |

---

### 5.3 Endpoints `/v1/tasks/accept` y `/v1/tasks/dispute`

| Amenaza (STRIDE) | Vector de Ataque | Mitigación Técnica (Objetivos) |
|---|---|---|
| **S** (Spoofing) | Un tercero intenta aceptar o disputar un recibo ajeno. | **S1** (mTLS) + **S2** (verificación de `EmitterSignature` contra `KnownAgents` y coincidencia estricta con `EmitterAgentID` del recibo). |
| **T** (Tampering) | Modificación del motivo de disputa o manipulación del timestamp. | **S2** (firma sobre mensaje canónico) + **R7** (timestamps RFC 3339 UTC a segundos normalizados). |
| **R** (Repudiation) | El emisor acepta y luego desconoce la aceptación. | **S7** (`EmitterSignature` persistida como contrafirma del recibo en SQLite). |
| **D** (DoS / Desync) | Reinyectar aceptaciones o disputas concurrentes para bifurcar la cadena. | **S7/R4** (`SaveReceipt` valida `prev_receipt_hash` bajo mutex; asignación atómica de `seq INTEGER`; `ErrChainBroken` en inconsistencia). |
| **E** (Elevation of Priv) | Replay de recibos entre redes o mallas distintas. | Validación estricta de `mesh_id` firmado y rechazo si difiere del `mesh_id` local. |

---

### 5.4 Endpoint `/v1/certs/enroll` (Capa de Transporte Base)

| Amenaza (STRIDE) | Vector de Ataque | Mitigación Técnica (Objetivos) |
|---|---|---|
| **S** (Spoofing) | Un nodo no autorizado intenta enrolarse y obtener certificado mTLS. | Requisito de token de enrolamiento uniuso y de vida corta (`pkg/server/store`). |
| **T** (Tampering) | Ataque MitM durante la descarga de la CA de bootstrap. | **S1** (`PinnedBootstrapConfig` exige huella sha256 `-ca-cert-hash` y valida cadena completa contra `ServerName`). |
| **I** (Info Disclosure) | Fuga de clave privada del nodo durante el enrolamiento. | Generación de clave local en el nodo; el servidor solo recibe y firma el CSR (clave privada nunca viaja ni se almacena en el servidor). |

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
| **R4** | Concurrencia correcta | DoS & Desincronización | Columna `seq INTEGER` monótona asignada atómicamente; ordenación determinista. |
| **R7** | Firmas deterministas | Falsos positivos en firma | Timestamps normalizados RFC 3339 UTC; 1000 iteraciones con 0 fallos bajo `-race`. |

---

## 7. No-Objetivos y Fronteras de Responsabilidad (N1–N9)

El modelo reconoce explícitamente qué amenazas **no** cubre el protocolo, delegándolas en la infraestructura o el diseño organizativo:
- **N1 (Daño en perímetro):** Si a un agente se le concede escribir en `src/`, puede escribir código erróneo dentro de `src/` (cubierto por revisión humana y aserciones).
- **N2 (Aislamiento de Kernel):** Escapes de contenedor o kernel bugs escapan a Gentle Mesh (cubierto por microVMs, gVisor o nsjail).
- **N4 (Host Comprometido):** Si el host del ejecutor es vulnerado a nivel root, sus firmas pierden validez (cubierto por anclaje externo de recibos y HSMs).
- **N9 (Cognición no gobernada):** La red no es una mente colectiva ni ofrece canales libres de memoria compartida (ver RFC-004 para tablones gobernados tipados).
