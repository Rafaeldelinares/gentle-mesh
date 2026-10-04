# AGENTS.md — Convenciones y Misión de RFC-002: Red Cognitiva de Agentes

> **Proyecto:** `rfc002` / Red Cognitiva de Agentes (Gentle Mesh Evolution)  
> **Ubicación:** `/home/rafael/proyectos/rfc002`  
> **Propósito:** Especificación técnica e implementación de la Red Cognitiva de Agentes: TaskEnvelope, Pre-flight Handshake de Recursos y Task Settlement Determinista.  
> **Relación con RFC-001:** Este proyecto es la capa semántica y cognitiva que se monta sobre el transporte distribuido base de Gentle Mesh v1 (RFC-001).  
> **Destino Comunitario:** Propuesta RFC oficial para la comunidad de Gentleman Programming.  

---

## 1. Misión del Proyecto

`rfc002` es la evolución de Gentle Mesh de un *remote runner pasivo* a una **malla inteligente de cooperación entre agentes**. 

En RFC-001 (`gentle-mesh`) se resolvieron los desafíos de infraestructura y transporte:
- Servidor y workers en Go sin dependencias (`CGO_ENABLED=0`).
- Streaming HTTPS/SSE de pensamientos y tokens.
- Persistencia dual (SQLite WAL embebido + JSONL).
- Semáforos territoriales en Git (`repo:branch`).
- Seguridad Zero-Trust (mTLS, CSR enrollment y rate limiting).

**La Misión de RFC-002 es resolver los tres problemas estructurales del trabajo autónomo con agentes de IA:**
1. **La falacia del cómputo homogéneo:** Los agentes no necesitan cómputo intercambiable, necesitan **localidad de contexto y recursos** (acceso a repositorios específicos clonados, GPU/VRAM para modelos locales, herramientas instaladas en el host).
2. **El costo del fallo tardío:** Evitar despachar tareas a ciegas que queman tokens y tiempo para descubrir a los 3 minutos que faltaba una dependencia en el host remoto.
3. **La falacia del "Green Checkbox":** Eliminar la complacencia conversacional donde un LLM alucina que terminó bien o devuelve exit code 0 tras un bypass improvisado.

---

## 2. Los Tres Pilares de RFC-002

### Pilar 1: `CognitiveTaskEnvelope` (Sobre Cognitivo)
Sustituir el texto plano (`task: "hacer X"`) por un contrato estructurado y tipado:
- **Intención:** Metadatos semánticos de la misión.
- **Territorio:** Repositorio y branch requeridos.
- **Requisitos de Recursos:** Herramientas de sistema (`tool: "go" >= 1.22`), memoria VRAM mínima, paths de filesystem con permisos de escritura.
- **Precondiciones:** Invariantes de arranque (ej. `git_clean_worktree: true`, puertos disponibles).
- **Aserciones de Liquidación (Settlement Assertions):** Reglas falsables y deterministas que deben cumplirse para dar la tarea por exitosa.

### Pilar 2: Pre-flight Handshake (Negociación e Idoneidad)
Antes de pasar la tarea a `RUNNING` o gastar un solo token:
- El nodo receptor valida local y síncronamente si posee los recursos y precondiciones requeridos.
- Si no es idóneo, devuelve un diagnóstico estructurado (`Handshake Diagnostics`) explicando el recurso faltante y sugiriendo nodos alternativos para re-enrutar al instante.

### Pilar 3: Task Settlement & Recibos Verificables
Cuando el subproceso del agente finaliza:
- El estado pasa a `SETTLING` (no a `COMPLETED`).
- El **arnés del servidor** (en Go nativo, aislado del LLM) evalúa de forma determinista las aserciones declaradas (hashes de archivos modificados, suites de tests con exit code 0, invariantes en base de datos).
- Si todas pasan, emite un **Recibo de Liquidación (`SettlementReceipt`)** criptográfico e inmutable persistido en SQLite.
- Si alguna falla, emite un veredicto de fallo explícito (`SETTLEMENT_FAILED`) o habilita un bucle acotado de remediación (máximo 1-2 turnos).

---

## 3. Pila Tecnológica y Estructura

* **Lenguaje:** Go (Golang 1.22+) con biblioteca estándar (`net/http`, `context`, `database/sql`).
* **Persistencia:** SQLite puro embebido (`modernc.org/sqlite`, `CGO_ENABLED=0`) con WAL mode.
* **Protocolo:** Contratos JSON deterministas sobre HTTPS/SSE.
* **Testing:** Go testing estricto con cobertura y `-race`.

```text
rfc002/
├── docs/
│   ├── rfcs/                 # RFC-002: Especificación técnica canónica
│   └── architecture/         # Informe de asesoría técnica y diagramas
├── pkg/
│   ├── envelope/             # Definición y parser de CognitiveTaskEnvelope
│   ├── handshake/            # Motor de negociación pre-flight y validación de recursos
│   └── settlement/           # Evaluador de aserciones invariantes y emisor de recibos
└── cmd/                      # Herramientas de prueba y validación de liquidación
```

---

## 4. Reglas de Desarrollo

1. **Determinismo sobre Opinión:** La finalización de una tarea nunca depende de lo que diga el LLM. Depende de las aserciones del arnés.
2. **Cero Dependencias Pesadas:** Todo en Go puro (`CGO_ENABLED=0`) para máxima portabilidad estática.
3. **Compatibilidad:** Diseñado como una capa semántica que se integra y consume el transporte de Gentle Mesh (RFC-001).
