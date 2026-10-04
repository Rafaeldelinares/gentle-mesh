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
| **A2A Issue #2150 (signed-receipts/v1)** | `https://github.com/a2aproject/A2A/issues/2150` | 2026-09-30 | **NO VERIFICADA (404 a fecha 2026-09-30)** (Inaccesible en upstream). |
| **CSOAI a2a-signed-receipts** | `https://github.com/CSOAI-ORG/a2a-signed-receipts` | 2026-09-30 | **NO VERIFICADA (404 a fecha 2026-09-30)** (Inaccesible en upstream). |

*Nota metodológica sobre fuentes no accesibles:* Las referencias a `a2aproject/A2A#2150` y `CSOAI-ORG/a2a-signed-receipts` arrojaron HTTP 404. La investigación cruzada identificó que CSOAI Ltd publica el paquete `inspect-signed-receipt` (recibos Ed25519 sobre evaluaciones de Inspect AI con `did:web`) y que en A2A se debatieron recibos en el PR #1915 (`scoped authorization receipt`), Issue #1769 e Issue #2236. No se asumen datos no verificados de las fuentes con error 404.

---

## 2. Respuestas a los Puntos de Posicionamiento (a–i)

### a. Mapa de Capas: Qué cubre A2A y qué cubre exclusivamente Gentle Mesh

**Hipótesis:** Gentle Mesh cubre de forma exclusiva el pre-flight handshake con lease, las aserciones de liquidación deterministas, el ciclo de aceptación/disputa bilateral y la cadena criptográfica inmutable.  
**Veredicto:** **No se ha encontrado, en las fuentes consultadas, nada que cubra** el pre-flight territorial con lease, las aserciones de liquidación deterministas, la aceptación/disputa bilateral ni la cadena criptográfica de recibos.

- **A2A Core (v1.0.0):** Resuelve el transporte interoperable, descubrimiento dinámico (`AgentCard`), catálogo de habilidades (`AgentSkill`), streaming reactivo (SSE) y mensajería en turnos (`Message`, `Part`, `Artifact`). Opera bajo el principio explícito de **"Opaque Execution"** (Sección 1.2): los agentes no comparten estado interno, razonamiento ni herramientas. El protocolo confía en la finalización declarada por el agente.
- **Propuestas A2A analizadas:**
  - *APS (#1575):* Identidad Ed25519, atenuación de permisos (narrowing) y atestación de valores (*Values Floor*).
  - *Artifact Receipts (#2236 - cerrada NOT_PLANNED):* Acuse de entrega/recepción de artefactos ("yo envié / yo recibí"), admitiendo explícitamente que no valida la calidad ni el cumplimiento.
  - *AP2:* Mandatos de pago e intención de compra (VDCs) para transacciones comerciales.
- **Lo que SOLO cubre Gentle Mesh:**
  1. **Pre-flight Handshake territorial:** Verificación síncrona local de precondiciones (`repo:branch`, herramientas de sistema, VRAM, estado de árbol de trabajo) *antes* de aceptar la tarea y gastar tokens.
  2. **Aserciones de Liquidación Deterministas:** Reglas falsables evaluadas por el arnés Go nativo del host, aislado de la alucinación del LLM (tests con exit code 0, hashes de archivos modificados, invariantes SQL).
  3. **Aceptación / Disputa Bilateral:** Contrafirma obligatoria del emisor o emisión de disputa formal (`/dispute`) con firma criptográfica.
  4. **Cadena de Recibos con Integridad Verificable:** Encadenamiento secuencial (`prev_receipt_hash`, `seq`) auditado. **Los enlaces y la secuencia aún no están firmados (issue #43) y la firma del emisor no cubre la decisión (issue #48); hasta entonces, la cadena es tamper-evident por recibo, no estructuralmente.**

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
│  - Task Lifecycle (SUBMITTED, WORKING, COMPLETED, etc.)     │
│  - SSE Streaming, Artifacts, AgentCard Discovery            │
├─────────────────────────────────────────────────────────────┤
│ Capa 1: Transporte y Red (mTLS, HTTP/2, WebSockets, SSE)    │
└─────────────────────────────────────────────────────────────┘
```

---

### b. Compatibilidad con Recibos Firmados y Modelo de Metadatos

Dado que la propuesta upstream `a2aproject/A2A#2150` no estuvo accesible (HTTP 404 a fecha 2026-09-30), la compatibilidad exacta campo a campo con `signed-receipts/v1` queda clasificada como **HIPÓTESIS PENDIENTE DE VERIFICAR**.

**Lo que sí está comprobado técnicamente:**
1. Ambos modelos convergen en los mismos fundamentos criptográficos: serialización canónica **JCS (RFC 8785)** y firmas asimétricas **Ed25519**.
2. La especificación A2A prohíbe alterar los esquemas Protobuf centrales (`Task`, `TaskStatus`, `Artifact`), pero habilita de forma normativa el transporte de datos estructurados arbitrarios mediante mapas `metadata` (`map<string, string>` / JSON object).
3. `SettlementReceipt` puede viajar íntegramente dentro de `Task.metadata["settlement_receipt"]`, preservando compatibilidad absoluta con clientes A2A estándar que desconozcan la extensión, al tiempo que clientes compatibles con Gentle Mesh pueden verificar las aserciones, la firma del ejecutor, la contrafirma del emisor y el enlace a la cadena criptográfica (`prev_receipt_hash`, `seq`).

---

### c. Identidad de Claves: DIDs (`did:web`, `did:key`) vs Keystore Local y mTLS

- **Ecosistema A2A:** Emplea DIDs (`did:web` para servidores con dominio público y `.well-known/did.json`; `did:key` para identidades efímeras autónomas).
- **Gentle Mesh Actual:** Emplea mTLS Zero-Trust con CA pinning en el transporte (`pkg/tlsutil`, S1) y un almacén de claves local (`pkg/keystore`) que mapea `node_id` a pares Ed25519 locales con permisos de archivo 0600.
- **Resolución de la tensión entre Identidad Local y DIDs (c vs d):**  
  Existe una tensión aparente entre recomendar el Keystore local propio (sin dependencias de red ni DNS externos) y adoptar UCAN o APS (que operan con DIDs).  
  **Solución de encaje:** Los nodos de Gentle Mesh adoptan **`did:key` derivado determinísticamente de su clave pública Ed25519** almacenada en el Keystore local (`did:key:z6M...`).
  - `did:key` es un método DID completamente autónomo y estático: la clave pública está codificada en el propio identificador (multicodec + base58btc).
  - No requiere ninguna resolución por red, consultas HTTP ni infraestructura DNS (`did:web`).
  - Permite que el Keystore local siga siendo la autoridad primaria autónoma (operable en entornos aislados o air-gapped) manteniendo al mismo tiempo compatibilidad nativa con tokens de delegación UCAN y firmas del ecosistema A2A.

---

### d. Delegación (Fase 2): Comparativa de Modelos

| Modelo | Ventajas | Desventajas | Veredicto |
|---|---|---|---|
| **Diseño Propio** | Ajustado 100% al RFC-002; sin dependencias. | Alto riesgo de vulnerabilidades criptográficas (replay, revocación inconsistente). | **Descartado** |
| **Macaroons** | Caveats rápidos, compactos. | Criptografía simétrica (HMAC); requiere compartir secreto del emisor. Inadecuado para federación abierta. | **Descartado** |
| **Biscuit** | Atenuación criptográfica asimétrica basada en Datalog. Gran expresividad lógica. | Requiere incorporar un runtime de Datalog en Go; complejidad operativa alta. | **Alternativa futura** |
| **Agent Passport (APS #1575)** | Específico para agentes AI; soporte de *Values Floor* y atenuación de alcance; Ed25519 nativo. | Implementación monolítica en TypeScript (v5.0.3); la revocación en cascada original fue rediseñada a listas provistas por el caller. | **Referencia conceptual** |
| **UCAN (RFC 021)** | Estándar formalizado IETF/W3C; JWT asimétrico Ed25519; DIDs (`did:key`); atenuación matemática pura; librerías activas en Go. | Carga útil JWT más verbosa. | **RECOMENDADO para Fase 2** |

*Recomendación:* Basar la delegación en **UCAN** o en la semántica de atenuación de **APS**, descartando inventar criptografía ad-hoc.

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
| `SETTLEMENT_TIMEOUT` | `TASK_STATE_FAILED` | `reason: "timeout"`, `error: "lease_expired"` |
| `ABORTED` | `TASK_STATE_CANCELED` | `reason: "canceled_by_client"` |

*Nota sobre `SETTLEMENT_TIMEOUT`:* En A2A, `TASK_STATE_CANCELED` está reservado exclusivamente para cancelaciones explícitas; cuando una tarea expira por vencimiento de lease o falta de latidos, la terminación corresponde normativamente a `TASK_STATE_FAILED` con motivo `timeout`.

---

### f. Tipo de Extensión A2A y URI Canónica

1. **Tipo de Extensión:** **Profile Extension** (Perfil de Protocolo). Establece un perfil estricto sobre `SendMessage`, `GetTask` y eventos SSE de `TaskStatusUpdateEvent`, exigiendo el sobre cognitivo y devolviendo el `SettlementReceipt` en la finalización. Opcionalmente actúa como **Method Extension** al exponer endpoints RPC auxiliares (`tasks/settle`, `tasks/dispute`).
2. **Propuesta de URIs:**
   - **URI canónica primaria propuesta:**  
     `https://github.com/Rafaeldelinares/gentle-mesh/tree/main/docs/rfcs/extensions/settlement/v1`  
     (Espacio de nombres bajo control del repositorio).
   - **Opción comunitaria sujeta a aprobación:**  
     `https://gentleman-programming.io/a2a/ext/settlement/v1`  
     (Sujeta a la previa autorización explícita de Alan Buscaglia como titular del dominio).
   - **Propuesta oficial upstream (futuro):**  
     `https://a2a-protocol.org/extensions/settlement/v1`

---

### g. Oportunidad: Propuesta de Extensión de Liquidación para la Comunidad A2A

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

El análisis de A2A v1.0.0 aporta soluciones estructurales directas a los problemas detectados en los Issues #7 y #8:

1. **Resolución del Issue #7 (Timeout total mata tareas largas en RFC-001):**
   - *Modelo A2A:* Desacopla el transporte HTTP/SSE del ciclo de vida de la tarea. La conexión de red puede reiniciarse mientras la tarea continúa en ejecución en background (`TASK_STATE_WORKING`), permitiendo al cliente reconectarse vía `SubscribeToTask(taskId)` o consultar `GetTask(taskId)`.
   - *Adopción:* Sustituir el timeout total monolítico de 300s por los tres relojes propuestos en el Issue #7:
     - `heartbeat_timeout`: latido de vida del ejecutor.
     - `stall_timeout`: tiempo máximo sin eventos de progreso o checkpoint.
     - `max_duration`: presupuesto total máximo de la tarea.

2. **Resolución del Issue #8 (Lease renovable y presupuesto decreciente en RFC-002 / R2):**
   - *Modelo A2A:* Emisión periódica de eventos de progreso que renuevan el lease de ejecución de forma activa.
   - *Adopción e Invariante de Seguridad:* Cada latido de progreso firmado por el ejecutor renueva el lease temporal. Si una tarea requiere autorización de gasto o decisión humana, pasa al estado interrumpido `TASK_STATE_INPUT_REQUIRED` (o `TASK_STATE_AUTH_REQUIRED`).
   - **Invariante crítica contra estiramiento de presupuesto:** El estado `TASK_STATE_INPUT_REQUIRED` puede pausar el consumo del presupuesto de cómputo del runner, **pero el tope de tiempo total (`max_duration`) sigue corriendo SIEMPRE en tiempo real (wall-clock)**. Este invariante impide de forma determinista que un agente malicioso o desalineado pueda eludir el vencimiento de la tarea entrando en estados de espera en bucle.

---

## 3. Conclusiones

1. Gentle Mesh no compite con A2A: se posiciona como su **motor de liquidación determinista indispensable**.
2. La arquitectura actual (`pkg/jcs`, `pkg/receipt`, `pkg/settlement`) es 100% compatible con la estrategia de extensiones de A2A.
3. El ciclo de vida de tareas de A2A v1.0.0 aporta la base conceptual idónea para cerrar limpiamente los Issues #7 y #8.

---

## 4. Decisión (humano, 2026-09-30)

- gentle-mesh funciona SOBRE A2A.
- No se introduce el SDK de A2A (gRPC/Protobuf) en el núcleo hasta el puente (fin de Fase 2 o Fase 3).
- Desde YA, los tipos nuevos se diseñan para mapear 1:1 con A2A: estados de tarea de A2A (WORKING, INPUT_REQUIRED, COMPLETED, FAILED, CANCELED, REJECTED) y el detalle de gentle-mesh en metadata (substate, recibo).
- Los issues #7 y #8 se diseñan con el ciclo de vida de A2A.
- La delegación de la Fase 2 se basará en UCAN o en la semántica de APS, no en un diseño propio.
