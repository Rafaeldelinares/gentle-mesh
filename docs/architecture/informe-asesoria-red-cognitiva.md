# Informe de Asesoría Técnica: Evolución de Gentle Mesh hacia una Red Cognitiva de Agentes (RFC-002)

> **Destinatarios:** Arquitectos de software, ingenieros de sistemas distribuidos y especialistas en arneses de IA / agentes autónomos.  
> **Autor:** Rafael De Linares & el Gentleman (Ecosistema Gentle AI)  
> **Proyecto:** Gentle Mesh (`gentle-mesh`)  
> **Versión Actual de Referencia:** v1.0.1 (Producción en Go / Zero-Dependency)  
> **Objetivo del Documento:** Someter a juicio y asesoría externa la transición de un modelo de *transporte de ejecución remota pasiva* hacia una *malla de agentes conscientes de recursos con liquidación determinista (Task Settlement)*.

---

## 1. Resumen Ejecutivo (TL;DR)

**Gentle Mesh** nació como una solución de infraestructura (RFC-001) para desacoplar el cómputo de la máquina local del desarrollador en el ecosistema Pi / Gentle AI. Mediante un binario único en Go (~11MB, sin dependencias de C/CGO), provee streaming HTTPS/SSE de pensamientos y tokens, persistencia dual (SQLite en modo WAL + JSONL append-only), semáforos territoriales en Git (`repo:branch`) y seguridad Zero-Trust (mTLS, CSR enrollment y rate limiting).

**El Límite Detectado:** Al operar con subagentes LLM autónomos en hardware real, descubrimos que los modelos tradicionales de balanceo de carga (*round-robin* o servidores ociosos) **fracasan con agentes de IA**. Despachar una tarea como un simple string (`task: "hacer X"`) genera dos patologías graves:
1. **Falta de afinidad y fallos ciegos:** Tareas enviadas a nodos que carecen de los datos locales, herramientas específicas, GPU o contexto de repositorio adecuado.
2. **La falacia del "Green Checkbox":** El agente concluye con exit code 0 y afirma *"Tarea completada exitosamente"*, pero en la práctica no modificó los archivos correctos, alucinó la respuesta o no satisfizo los invariantes de negocio.

**La Propuesta (RFC-002):** Convertir a Gentle Mesh en una **Red Cognitiva de Agentes** basada en:
1. **Cognitive Task Envelope:** Contrato explícito con requerimientos de recursos, precondiciones y aserciones de éxito.
2. **Pre-flight Handshake:** Negociación previa entre nodos para garantizar idoneidad técnica antes de consumir cómputo o tokens.
3. **Task Settlement (Liquidación Determinista):** Verificación matemática/falsable de resultados por parte del arnés (independiente de la opinión del LLM) con emisión de un **Recibo de Liquidación** inmutable.

Buscamos la perspectiva y crítica de asesores externos en **cuatro dilemas de diseño distribuidos** detallados en la Sección 5.

---

## 2. Dónde estamos hoy: Gentle Mesh v1.0.1 (Capa de Infraestructura)

Para que el asesor comprenda las bases operativas ya construidas y probadas:

```text
┌─────────────────────────────────────────────────────────────┐
│                      GENTLE MESH v1.0.1                     │
│               Arquitectura Actual (Nivel Transporte)        │
└─────────────────────────────────────────────────────────────┘

       Cliente (Laptop / Open Pi Viewer / CLI)
                         │
                         │ HTTPS REST + Streaming SSE (Zero-Trust / mTLS)
                         ▼
       Coordinador Gentle Mesh (Binario Go Estático)
       ┌──────────────────────────────────────────────────────┐
       │ • SQLite WAL Embebido (crash recovery & reanudación) │
       │ • Logs JSONL append-only (reproducción con Last-ID)  │
       │ • TerritoryScheduler (bloqueo semafórico repo:branch)│
       │ • Dispatcher de Webhooks (HMAC-SHA256)               │
       │ • Token Bucket Rate Limiting per-IP                  │
       └──────────────────────────┬───────────────────────────┘
                                  │
                 ┌────────────────┴────────────────┐
                 ▼                                 ▼
           PiRunner (Local)                 Worker Nodes
       Subproceso Pi headless           Nodos remotos conectados
       (con memoria Engram)             con auto-enrollment mTLS
```

* **Estado de Código:** 21.000 líneas de Go (13.300 de tests, ratio 1.7x test-to-code, `-race` safe).
* **Despliegues reales:** Operativo en clústeres locales y hardware dedicado remoto (nodo *La Fábrica* bajo Tailscale WireGuard).
* **Conectores:** Integrado de forma nativa en `open-pi-viewer` (PR #1) y conector stdio JSON-RPC (`gentle-mesh rpc`).

---

## 3. Diagnóstico de Campo: Por qué falla la distribución ingenua de Agentes

Al coordinar subagentes en entornos de desarrollo reales, identificamos tres fallas estructurales que ninguna cola de mensajes tradicional (RabbitMQ, SQS o Celery) resuelve por sí misma:

### A. La falacia del cómputo homogéneo (*Context & Tool Locality*)
En microservicios estándar, cualquier pod de Kubernetes procesa un request HTTP idéntico. En agentes autónomos, **el contexto es el recurso**:
- Un agente necesita acceso físico a un repositorio Git con un branch clonado.
- Otro agente necesita una GPU con VRAM disponible para correr un modelo local con Ollama.
- Otro agente requiere herramientas específicas instaladas en el SO del host (Playwright, Docker, compiladores Rust/Go).
*Si el despachador trata a los nodos como "cómputo intercambiable", la tarea fracasa o pierde minutos descargando contextos masivos.*

### B. El costo del fallo tardío (*Falta de Pre-flight Handshake*)
Hoy en día, un cliente despacha:
```json
{
  "agent": "worker",
  "task": "Generá un reporte en PDF de las subastas usando Weasyprint y convertí a WebP"
}
```
El nodo remoto acepta la tarea, levanta el subproceso del LLM, consume tokens durante 3 minutos, y al momento de ejecutar la herramienta en bash descubre que `weasyprint` no está instalado en el sistema. **El fallo ocurrió al final, costó dinero, tiempo y rompió el flujo del usuario.**

### C. La falacia del "Green Checkbox" (*Ausencia de Settlement*)
Los LLMs sufren de complacencia conversacional. Frente a un error complejo, es común que un agente:
1. Pruebe un comando, falle.
2. Haga un bypass o mock improvisado.
3. Termine su respuesta diciendo: *"He implementado exitosamente la autenticación con JWT y todos los tests pasan."*
4. Devuelva exit code 0.
En un pipeline automatizado, esto se marca como `TASK_COMPLETED`. En la realidad, el código está roto. **La finalización no puede depender del testimonio del propio agente.**

---

## 4. La Arquitectura Propuesta: Red Cognitiva (RFC-002)

Para resolver estos problemas, proponemos estructurar la interacción entre agentes en tres mecanismos deterministas:

```text
┌──────────────────┐
│  Agente Emisor   │
└────────┬─────────┘
         │
         │ 1. Emite CognitiveTaskEnvelope
         ▼
┌─────────────────────────────────────────────────────────────┐
│ 2. Pre-flight Handshake                                      │
│    • ¿Tenés acceso al repo X en branch Y?                   │
│    • ¿Disponés de GPU >= 8GB VRAM?                          │
│    • ¿Tenés las herramientas [git, go, docker] en PATH?     │
└────────────────────────┬────────────────────────────────────┘
                         │
                         ├─ [RECHAZO EXPLICATIVO] ──► Re-enrutar a otro nodo
                         │
                         ▼ [ACEPTADO / LEASE ASIGNADO]
┌─────────────────────────────────────────────────────────────┐
│ 3. Ejecución de la Tarea (Streaming HTTPS/SSE)              │
│    El agente piensa, llama herramientas y genera código     │
└────────────────────────┬────────────────────────────────────┘
                         │
                         │ Agente concluye ejecución
                         ▼
┌─────────────────────────────────────────────────────────────┐
│ 4. Task Settlement (Liquidación Invariable por el Arnés)   │
│    El demonio Gentle Mesh evalúa aserciones deterministas:  │
│    • FileHash(pkg/auth/jwt.go) != initial_hash              │
│    • Command("go test ./pkg/auth") == ExitCode 0            │
│    • SQLiteQuery("SELECT COUNT(*) FROM users") >= 1         │
└────────────────────────┬────────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────────┐
│ 5. Emisión de Recibo de Liquidación (Settlement Receipt)    │
│    • Estado final inmutable persistido en SQLite            │
│    • Recibo criptográfico entregado al emisor               │
└─────────────────────────────────────────────────────────────┘
```

### Componente 1: `CognitiveTaskEnvelope` (Sobre Cognitivo)
El payload de la tarea evoluciona de un simple texto a una estructura contractual:

```json
{
  "task_id": "tsk-091f6a57",
  "intent": "Implementar middleware de autenticación JWT y tests",
  "territory": {
    "repository": "github.com/org/repo",
    "branch": "feature/auth-jwt"
  },
  "required_resources": [
    { "type": "tool", "name": "go", "version": ">=1.22" },
    { "type": "filesystem", "path": "/workspace/repo", "writeable": true },
    { "type": "vram_mb", "min": 0 }
  ],
  "preconditions": [
    { "type": "git_clean_worktree", "expected": true },
    { "type": "port_available", "port": 8443 }
  ],
  "settlement": {
    "strategy": "strict_all",
    "assertions": [
      {
        "id": "code_modified",
        "type": "file_modified",
        "path": "pkg/auth/jwt.go"
      },
      {
        "id": "tests_passing",
        "type": "command_exit_code",
        "command": "go test ./pkg/auth/...",
        "expected_code": 0
      }
    ],
    "timeout_seconds": 300
  }
}
```

### Componente 2: Pre-flight Handshake
Antes de pasar la tarea a la cola de ejecución (`PENDING` -> `RUNNING`):
* El nodo receptor valida los recursos y precondiciones de forma local y síncrona.
* Si un requisito no se cumple, el nodo responde de inmediato con un **Handshake Diagnostics**:
  ```json
  {
    "accepted": false,
    "reason": "resource_missing",
    "details": "Tool 'go' found version 1.21.0, required >=1.22",
    "suggested_nodes": ["node-worker-gpu-02"]
  }
  ```
* Esto evita quemar tokens y permite al coordinador redirigir la misión instantáneamente.

### Componente 3: Task Settlement & Receipts
Cuando el subproceso del agente finaliza:
1. El estado no pasa automáticamente a `COMPLETED`. Pasa a `SETTLING`.
2. El **arnés del servidor Gentle Mesh** (en Go nativo, aislado del LLM) ejecuta las aserciones declaradas en `settlement.assertions`.
3. Si todas las aserciones pasan, se emite un **Recibo de Liquidación** (`SettlementReceipt`):
   - Timestamp de verificación.
   - Hashes de los archivos tocados.
   - Salida truncada y firmas de los comandos ejecutados.
   - Veredicto final: `SETTLED`.
4. Si alguna aserción falla, el veredicto es `SETTLEMENT_FAILED` (con detalle explícito del fallo), impidiendo que el emisor asuma falsamente que el trabajo está hecho.

---

## 5. Preguntas Clave para los Asesores Técnicos

Agradecemos su criterio, experiencia y recomendaciones sobre estos cuatro dilemas arquitectónicos:

### Dilema 1: Protocolo de Handshake (Síncrono vs. Two-Phase Leases)
* **Escenario A (Handshake Síncrono RPC):** El cliente hace un `POST /v1/tasks/negotiate`, el nodo valida y responde `200 OK` (reservando el slot) o `412 Precondition Failed`. Simple, pero sensible a condiciones de carrera si varios clientes negocian al mismo tiempo.
* **Escenario B (Two-Phase Commit / Lease Temporal):** El cliente solicita un *Lease* de recursos con un TTL de 30 segundos. Si el handshake es exitoso, el cliente confirma el despacho con el ID del lease.
* **Pregunta para el asesor:** *Para una red de agentes heterogénea (entre 3 y 50 nodos), ¿justifica la complejidad de un modelo con Leases o un handshake síncrono atómico dentro de SQLite con busy timeout es suficiente?*

### Dilema 2: El Lenguaje de las Aserciones de Liquidación (*Settlement DSL*)
* **Riesgo:** Si permitimos que el `TaskEnvelope` defina aserciones complejas ejecutando comandos arbitrarios de bash en la fase de liquidación, abrimos una superficie de inyección o bucles infinitos en el arnés de verificación.
* **Alternativa 1:** Un DSL declarativo estricto y cerrado en Go:
  - `file_exists`, `file_modified`, `file_hash_equals`, `file_contains_regex`.
  - `command_exit_code` (con timeout estricto de arnés de 10s y sandbox).
  - `git_diff_non_empty`, `git_branch_clean`.
* **Alternativa 2:** Permitir un script de verificación embebido (ej. un micro script en Lua o Starlark hermético).
* **Pregunta para el asesor:** *¿Cuál es el balance adecuado entre expresividad de verificación y seguridad/hermeticidad para evitar que la fase de settlement se vuelva un vector de ataque o inestabilidad?*

### Dilema 3: Manejo de Liquidación Parcial y Ciclos de Reparación
* Si un agente resuelve 4 de 5 aserciones (por ejemplo: modificó los archivos, agregó documentación, pero falló 1 test unitario de regresión):
  - ¿Debe la tarea abortarse y descartarse por completo?
  - ¿O el arnés debe reinyectar el fallo de settlement como un nuevo turno de contexto al mismo agente (`remediation loop`), dándole un presupuesto acotado (ej. máximo 2 intentos) para corregir el invariante insatisfecho antes de marcar `SETTLEMENT_FAILED`?
* **Pregunta para el asesor:** *En su experiencia con pipelines de desarrollo, ¿cuál es la mejor estrategia de remediación automática en sistemas multiagente sin caer en bucles infinitos de consumo de tokens?*

### Dilema 4: Anuncio de Capacidades vs. Descubrimiento P2P
* En la v1.0.1, los workers envían un heartbeat periódico a SQLite con metadatos básicos (`tags: [gpu, linux, arm64]`).
* Al requerir recursos cognitivos más ricos (herramientas locales, repositorios clonados, modelos de LLM locales disponibles con sus contextos):
* **Pregunta para el asesor:** *¿Conviene mantener un registro centralizado de capacidades en el nodo Coordinador (aprovechando SQLite WAL), o transicionar hacia un protocolo de gossip / descubrimiento federado? ¿A partir de qué escala de nodos el modelo centralizado se vuelve un cuello de botella en este tipo de arquitectura?*

---

## 6. Formato de Respuesta Sugerido para el Asesor

Agradecemos que las devoluciones se enfoquen en:
1. **Puntos Ciegos:** ¿Qué problemas obvios no estamos viendo en el diseño del `TaskEnvelope` o el `Settlement`?
2. **Recomendaciones de Simplicidad:** ¿Qué partes de la propuesta recortarías por ser sobreingeniería prematura?
3. **Casos Análogos:** ¿Conocés estándares, RFCs o proyectos abiertos (en sistemas distribuidos, blockchain, contratos inteligentes o workflows tipo Temporal/Cadence) que hayan resuelto este problema de manera elegante?

---
*Gentle Mesh — Ecosistema Gentle AI & Gentleman Programming*  
*Contacto y Repositorio:* [github.com/Rafaeldelinares/gentle-mesh](https://github.com/Rafaeldelinares/gentle-mesh)
