# RFC-002: Posicionamiento de Gentle Mesh sobre el Protocolo A2A

> **Documento:** `docs/rfcs/002-a2a-positioning.md`  
> **Fecha de consulta de fuentes:** 2026-09-30  
> **Estado:** Propuesta Técnica de Arquitectura (Punto 1.10 / Fase 1)  
> **Premisa Base:** Gentle Mesh opera como capa semántica y de liquidación determinista SOBRE el transporte distribuido de agentes (A2A u homólogos).

---

## 1. Fuentes Consultadas Directamente y Estado de Accesibilidad

| Fuente | URL / Referencia | Fecha Consulta | Estado |
|---|---|---|---|
| **A2A Specification v1.0.0** | `https://a2a-protocol.org/latest/specification/` | 2026-09-30 | **Accesible** (Normativa v1.0.0, modelo de capas, RPC, SSE, estados). |
| **A2A Extensions Guide** | `https://a2a-protocol.org/latest/topics/extensions/` | 2026-09-30 | **Accesible** (Gobernanza, tipos de extensión, cabecera `A2A-Extensions`, URI). |
| **A2A Issue #1575 (APS)** | `https://github.com/a2aproject/A2A/issues/1575` | 2026-09-30 | **Accesible** (Agent Passport System v5.0.3, delegación Ed25519, 3 firmas). |
| **A2A Issue #2236 (Artifact Receipts)** | `https://github.com/a2aproject/A2A/issues/2236` | 2026-09-30 | **Accesible** (Cerrada como `NOT_PLANNED` el 2026-09-15 por Math1987). |
| **AP2 Specification** | `https://ap2-protocol.org/` | 2026-09-30 | **Accesible** (Agent Payments Protocol v0.2 / FIDO Alliance, mandatos VDC). |
| **A2A Issue #2150 (signed-receipts/v1)** | `https://github.com/a2aproject/A2A/issues/2150` | 2026-09-30 | **HTTP 404** (No accesible / eliminado o renombrado en upstream). |
| **CSOAI a2a-signed-receipts** | `https://github.com/CSOAI-ORG/a2a-signed-receipts` | 2026-09-30 | **HTTP 404** (No accesible / repositorio privado o inexistente). |

*Nota metodológica sobre fuentes no accesibles:* Las referencias a `a2aproject/A2A#2150` y `CSOAI-ORG/a2a-signed-receipts` devolvieron HTTP 404. La investigación cruzada identificó que CSOAI Ltd publica `inspect-signed-receipt` (recibos Ed25519 sobre evaluaciones de Inspect AI con `did:web`) y que el repositorio A2A debatió recibos en PR #1915 (`scoped authorization receipt`), Issue #1769 e Issue #2236. No se asumen datos no verificados del repositorio 404.

---

## 2. Respuestas a los Puntos de Posicionamiento (a–i)

### a. Mapa de Capas: Qué cubre A2A y qué cubre exclusivamente Gentle Mesh

**Hipótesis:** Gentle Mesh cubre de forma exclusiva el pre-flight handshake con lease, las aserciones de liquidación deterministas, el ciclo de aceptación/disputa bilateral y la cadena criptográfica inmutable.  
**Veredicto:** **HIPÓTESIS CONFIRMADA AL 100%.**

- **A2A Core (v1.0.0):** Resuelve el transporte interoperable, descubrimiento dinámico (`AgentCard`), catálogo de habilidades (`AgentSkill`), streaming reactivo (SSE) y mensajería en turnos (`Message`, `Part`, `Artifact`). Opera bajo el principio explícito de **"Opaque Execution"** (Sección 1.2): los agentes no comparten estado interno, razonamiento ni herramientas. El protocolo confía ciegamente en la finalización declarada por el agente.
- **Propuestas A2A analizadas:**
  - *APS (#1575):* Identidad Ed25519, atenuación de permisos (narrowing) y atestación de valores (*Values Floor*).
  - *Artifact Receipts (#2236 - rechazada):* Acuse de entrega/recepción de artefactos ("yo envié / yo recibí"), admitiendo explícitamente que no valida la calidad ni el cumplimiento.
  - *AP2:* Mandatos de pago e intención de compra (VDCs) para transacciones comerciales.
- **Lo que SOLO cubre Gentle Mesh:**
  1. **Pre-flight Handshake territorial:** Verificación síncrona local de precondiciones (`repo:branch`, herramientas de sistema, VRAM, estado de árbol de trabajo) *antes* de aceptar la tarea y gastar tokens.
  2. **Aserciones de Liquidación Deterministas:** Reglas falsables evaluadas por el arnés Go nativo del host, aislado de la alucinación del LLM (tests con exit code 0, hashes de archivos modificados, invariantes SQL).
  3. **Aceptación / Disputa Bilateral:** Contrafirma obligatoria del emisor o emisión de disputa formal (`/dispute`) con firma criptográfica.
  4. **Cadena de Recibos Inmutable:** Encadenamiento secuencial (`prev_receipt_hash`, `seq`) auditado.

```text
┌─────────────────────────────────────────────────────────────┐
│ Capa 4: Confianza y Liquidación (GENTLE MESH / RFC-002)     │
│  - Settlement Assertions (Arnés Go)  - Pre-flight Lease     │
│  - Bilateral Counter-signature       - Receipt Hash Chain   │
├─────────────────────────────────────────────────────────────┤
│ Capa 3: Autorización y Políticas (APS, UCAN, AP2)           │
│  - Ed25519 Delegation Chains        - Scoped Capabilities   │
├─────────────────────────────────────────────────────────────┤
│ Capa 2: Coordinación y Tareas (A2A Protocol v1.0.0)         │
│  - Task Lifecycle (SUBMITTED, WORKING, COMPLETED)           │
│  - SSE Streaming, Artifacts, AgentCard Discovery            │
├─────────────────────────────────────────────────────────────┤
│ Capa 1: Transporte y Red (mTLS, HTTP/2, WebSockets, SSE)    │
└─────────────────────────────────────────────────────────────┘
```

---

### b. Compatibilidad con Recibos Firmados y Modelo de Metadatos

La especificación A2A prohíbe alterar las estructuras base de Protobuf (`Task`, `TaskStatus`, `Artifact`), pero habilita explícitamente la extensibilidad mediante mapas `metadata` (`map<string, string>` / JSON object).

`SettlementReceipt` de Gentle Mesh es **100% integrable como superconjunto compatible** embebido en `Task.metadata`:

| Campo Base / Extensión | Semántica en A2A (`Task.metadata`) | Semántica en Gentle Mesh (`SettlementReceipt`) | Compatibilidad |
|---|---|---|---|
| `receipt_id` | Identificador único del recibo | `ReceiptID` (UUIDv7) | Idéntico |
| `task_id` | `Task.id` | `TaskID` (UUID o URI canónico) | Mapeo 1:1 |
| `issued_at` | Timestamp ISO 8601 | `Timestamp` (Unix epoch UTC) | Conversión trivial |
| `executor_id` | DID o clave pública del emisor | `ExecutorID` (Node ID / Key Fingerprint) | Compatible |
| `outcome` | `TaskStatus.state` (`COMPLETED`/`FAILED`) | `SettlementStatus` (`SETTLED_CLEAN`, etc.) | Superconjunto |
| `canonical_jcs` | JSON Canónico (RFC 8785) | `pkg/jcs` (RFC 8785 estricto) | Idéntico |
| `signature` | Firma Ed25519 base64 | Firma Ed25519 base64 | Idéntico |
| **`seq` + `prev_receipt_hash`** | *No contemplado en A2A* | Cadena criptográfica secuencial monótona | **Adición Gentle Mesh** |
| **`assertions_evaluated`** | *No contemplado en A2A* | Matriz de aserciones con veredicto booleano | **Adición Gentle Mesh** |
| **`envelope_hash`** | *No contemplado en A2A* | Hash SHA-256 JCS del contrato original | **Adición Gentle Mesh** |
| **`emitter_signature`** | *No contemplado en A2A* | Contrafirma bilateral de cierre | **Adición Gentle Mesh** |

---

### c. Identidad de Claves: DIDs (`did:web`, `did:key`) vs Keystore Local y mTLS

- **Ecosistema A2A:** Emplea DIDs (`did:web` para servidores con dominio público y `.well-known/did.json`; `did:key` para identidades efímeras autónomas).
- **Gentle Mesh Actual:** Emplea mTLS Zero-Trust con CA pinning en el transporte (`pkg/tlsutil`, S1) y un almacén de claves local (`pkg/keystore`) que mapea `node_id` a pares Ed25519 locales con permisos de archivo 0600.
- **Conclusión y Coexistencia:**
  1. *Separación de capas:* mTLS y CA pinning aseguran la capa de transporte (Capa 1). La firma del recibo asegura la capa de aplicación (Capa 4).
  2. *Resolución de Identidad:* Se recomienda **mantener el Keystore propio como autoridad primaria local** (cero dependencias de red o DNS externos, óptimo para entornos aislados/air-gapped) y exponer opcionalmente un resolver `did:web` / `did:key` como interfaz de compatibilidad cuando un nodo de Gentle Mesh deba autenticarse ante un cliente A2A estándar.

---

### d. Delegación (Fase 2): Comparativa de Modelos

| Modelo | Ventajas | Desventajas | Veredicto |
|---|---|---|---|
| **Diseño Propio** | Ajustado 100% al RFC-002; sin dependencias. | Alto riesgo de vulnerabilidades criptográficas (replay, revocación inconsistente). | **Descartado** |
| **Macaroons** | Caveats rápidos, compactos. | Criptografía simétrica (HMAC); requiere compartir secreto del emisor. Inadecuado para federación abierta. | **Descartado** |
| **Biscuit** | Atenuación criptográfica asimétrica basada en Datalog. Gran expresividad lógica. | Requiere incorporar un runtime de Datalog en Go; complejidad operativa alta. | **Alternativa futura** |
| **Agent Passport (APS #1575)** | Específico para agentes AI; soporte de *Values Floor* y atenuación de alcance; Ed25519 nativo. | Implementación monolítica en TypeScript (v5.0.3); la revocación en cascada original fue rediseñada a listas provistas por el caller. | **Referencia conceptual** |
| **UCAN (RFC 021)** | Estándar formalizado IETF/W3C; JWT asimétrico Ed25519; DIDs; atenuación matemática pura; librerías activas en Go. | Carga útil JWT más verbosa. | **RECOMENDADO para Fase 2** |

*Recomendación:* Adoptar el estándar **UCAN** o la semántica de atenuación de **APS** sobre el canonicalizador JCS RFC 8785 de Gentle Mesh, sin reinventar esquemas de delegación ad-hoc.

---

### e. Mapeo de Ciclo de Vida y Estados: Gentle Mesh ↔ A2A

A2A prohíbe añadir valores a su enum `TaskState` (`TASK_STATE_*`). En consecuencia, los estados cognitivos de Gentle Mesh se proyectan a los estados canónicos de A2A, enriqueciendo `TaskStatus.message` y `Task.metadata["settlement"]`:

| Estado Gentle Mesh (RFC-002) | Estado A2A (`TaskState`) | Metadatos A2A (`substate` / atributos) |
|---|---|---|
| `SUBMITTED` | `TASK_STATE_SUBMITTED` | `substate: "acknowledged"` |
| `PREFLIGHT` / `NEGOTIATING` | `TASK_STATE_WORKING` | `substate: "preflight_validation"` |
| `REJECTED_PRECONDITION` | `TASK_STATE_REJECTED` | `reason: "precondition_failed"`, diagnóstico detallado |
| `REJECTED_CAPABILITY` | `TASK_STATE_REJECTED` | `reason: "missing_capability"`, herramientas faltantes |
| `RUNNING` | `TASK_STATE_WORKING` | `substate: "executing"` |
| `SETTLING` | `TASK_STATE_WORKING` | `substate: "evaluating_assertions"` |
| `SETTLED_CLEAN` | `TASK_STATE_COMPLETED` | `SettlementReceipt` adjunto en `metadata` |
| `SETTLEMENT_FAILED` | `TASK_STATE_FAILED` | `SettlementReceipt` (con fallos) en `metadata` |
| `DISPUTED` | `TASK_STATE_FAILED` | `dispute_record` en `metadata` |
| `ABORTED` / `SETTLEMENT_TIMEOUT` | `TASK_STATE_CANCELED` | `reason: "lease_expired"` |

---

### f. Tipo de Extensión A2A y URI Canónica

De acuerdo a la guía de extensiones de A2A:
1. **Tipo de Extensión:** **Profile Extension** (Perfil de Protocolo).  
   *Justificación:* No requiere modificar los esquemas Protobuf centrales. Establece un perfil estricto sobre `SendMessage`, `GetTask` y los eventos SSE de `TaskStatusUpdateEvent`, exigiendo la inclusión del `CognitiveTaskEnvelope` en el envío y devolviendo el `SettlementReceipt` en la finalización. Opcionalmente actúa como **Method Extension** al exponer endpoints RPC auxiliares (`tasks/settle`, `tasks/dispute`).
2. **URI Canónica:**
   - Dominio independiente: `https://gentleman-programming.io/a2a/ext/settlement/v1`
   - Si se propone al registro oficial de A2A: `https://a2a-protocol.org/extensions/settlement/v1`

---

### g. Propuesta de Extensión de Liquidación para la Comunidad A2A

Existe una oportunidad estratégica clara para liderar la especificación en la comunidad de A2A:

- **Nombre de la propuesta:** *A2A Task Settlement & Verifiable Receipts Extension* (`ext-settlement`).
- **Problema en A2A:** El protocolo asume que si un agente remoto responde `TASK_STATE_COMPLETED`, el trabajo está hecho satisfactoriamente. Esto introduce riesgo de alucinación, fallos no detectados o salidas falsas positivas en flujos multi-agente desatendidos.
- **Solución propuesta:**
  1. Declarar `settlement_assertions` en el payload inicial.
  2. Emitir eventos SSE de fase durante el paso por `preflight` y `settling`.
  3. Adjuntar un `SettlementReceipt` criptográfico inmutable firmado por el arnés del host.
  4. Habilitar la contrafirma de aceptación o emisión de disputa entre agentes.

---

### h. Recomendación Razonada de Implementación: ¿Fase 2 o posterior?

**Recomendación:** **Implementar la Fase 1 y Fase 2 de RFC-002 de forma nativa e independiente, diseñando los contratos para que sean serializables 1:1 en A2A, y construir el adaptador/puente A2A como una capa complementaria al cierre de la Fase 2 o en una Fase 3.**

**Impacto en la planificación:**
- **Fase 1 (Actual):** Cero acoplamiento con gRPC, Protobuf o servidores A2A. Se completan los invariantes deterministas: canonicalización JCS, evaluación de aserciones en Go puro (`pkg/settlement`), persistencia SQLite WAL de recibos y verificación criptográfica Ed25519.
- **Fase 2:** Se implementan la política local del ejecutor (`policy.yaml`), la ejecución segura sin shell (S6), el lease renovable y la suite de conformidad.
- **Capa A2A (Final Fase 2 / Fase 3):** Consiste únicamente en un `A2AAdapter` HTTP que expone `AgentCard`, traduce `SendMessageRequest` a `CognitiveTaskEnvelope`, mapea eventos SSE y serializa el `SettlementReceipt` en `Task.metadata`.

---

### i. Ciclo de Vida de Tareas: Resolución de Issues #7 y #8 mediante Semántica A2A

El análisis de A2A v1.0.0 arroja respuestas directas a los problemas detectados en los Issues #7 y #8 de Gentle Mesh:

1. **Resolución del Issue #7 (Timeout total mata tareas largas en RFC-001):**
   - *Modelo A2A:* A2A maneja tareas desacoplando el tiempo de transporte del ciclo de vida de la tarea. La conexión SSE puede interrumpirse y el cliente puede consultar el estado vía `GetTask` o reconectarse mediante `SubscribeToTask(taskId)`.
   - *Adopción:* Sustituir el timeout total monolítico de 300s por los tres relojes propuestos en el Issue #7:
     - `heartbeat_timeout`: latido de vida del ejecutor.
     - `stall_timeout`: tiempo máximo sin eventos de progreso o checkpoint.
     - `max_duration`: presupuesto total máximo de la tarea.
   - Si la conexión se cae, el runner continúa en background (`TASK_STATE_WORKING`) y el cliente se reconecta con un nuevo contexto de streaming sin reiniciar la tarea.

2. **Resolución del Issue #8 (Lease renovable y presupuesto decreciente en RFC-002 / R2):**
   - *Modelo A2A:* La emisión periódica de `TaskStatusUpdateEvent` o `TaskArtifactUpdateEvent` permite renovar el lease de ejecución de forma activa.
   - *Adopción:* Cada latido de progreso firmado por el ejecutor renueva el lease temporal hasta el límite fijado por `max_duration`.
   - Si una tarea requiere autorización de gasto adicional o decisión humana, pasa al estado interrumpido `TASK_STATE_INPUT_REQUIRED` (o `TASK_STATE_AUTH_REQUIRED`), pausando el consumo de presupuesto hasta recibir el mensaje de continuación.

---

## 3. Conclusiones y Próximos Pasos

1. Gentle Mesh no compite con A2A: se posiciona como su **motor de liquidación determinista indispensable**.
2. La arquitectura actual (`pkg/jcs`, `pkg/receipt`, `pkg/settlement`) es 100% compatible con la estrategia de extensiones de A2A.
3. El ciclo de vida de tareas de A2A v1.0.0 aporta la base conceptual idónea para cerrar limpiamente los Issues #7 y #8.

*Fin del documento — Detenido para revisión humana antes de cualquier implementación de código.*
