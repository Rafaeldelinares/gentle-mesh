# RFC 001: Transporte Distribuido y Ejecución de Subagentes Remotos (Gentle Mesh)

* **Autor:** Rafael De Linares & el Gentleman (Ecosistema ByBusiness / Gentle AI)  
* **Fecha:** Septiembre 2026  
* **Estado:** Borrador / Propuesta para Gentleman Programming  
* **Área:** Infraestructura, Concurrencia Distribuida, Arneses de Agentes  

---

## 1. Motivación y Diagnóstico

El ecosistema Pi (`@earendil-works/pi-coding-agent`) y el arnés de `gentle-pi` han establecido un estándar formidable para el desarrollo asistido por agentes en la terminal:
* Invocación controlada de subagentes (`subagent_run`).
* Coordinación entre sesiones locales (`orchestrator_send_message`).
* Arneses rigurosos de Organic Driven Development (ODD) y revisión por pares.

### El Límite Actual: La Asunción "Localhost"
Actualmente, el ciclo de vida de los subagentes está acoplado al proceso y al sistema de archivos local (`child_process.fork` en la misma CPU/RAM).

Cuando un equipo de ingeniería o un desarrollador trabaja en un entorno distribuido:
1. **Saturación de la máquina cliente:** Tareas pesadas (crawlers masivos con Playwright, compilaciones pesadas, benchmarks o ejecución de LLMs locales con vLLM/Ollama) ahogan la laptop de trabajo.
2. **Corte de ejecución por interrupción:** Si el desarrollador cierra la tapa de la laptop o pierde la conexión interactiva, la misión en curso se interrumpe.
3. **Cercanía a los datos (Data Locality):** Operaciones sobre bases de datos de gigabytes o proxies residenciales situados en servidores dedicados obligan a transportar datos masivos por la red hacia la laptop del desarrollador.

---

## 2. La Propuesta: "Gentle Mesh"

Proponemos introducir un **Transporte Enchufable (Pluggable Transport)** dentro de la arquitectura de subagentes de Gentle AI:

```text
                        ┌──────────────────────────────────────────────┐
                        │   subagent_run(agent="...", task="...")      │
                        └──────────────────────┬───────────────────────┘
                                               │
                                 ¿Qué transporte está configurado?
                                               │
                     ┌─────────────────────────┴────────────────────────┐
                     ▼                                                  ▼
          [Transport: "local"] (Default)                     [Transport: "remote"]
          Pi corre como subproceso hijo                      Gentle Mesh Client
          en el host local (como hoy).                       Despacha HTTP REST + SSE
                                                                        │
                                                                        ▼ (Tailscale / Red Privada)
                                                             [Gentle Mesh Daemon en Servidor]
                                                             Ejecuta Pi headless en el nodo
                                                             Streaming de eventos al cliente
```

---

## 3. Principios de Diseño

1. **No-Breaking Change (100% Retrocompatible):** Si el desarrollador no configura un nodo remoto, el 100% de Pi y Gentle AI se comporta exactamente igual que hoy.
2. **Binario Único en Go (`zero-dependency`):** El demonio del servidor y el cliente se distribuyen en un único binario compilado estáticamente (`CGO_ENABLED=0`).
3. **Streaming Reactivo mediante SSE (Server-Sent Events):** La terminal del cliente recibe pensamientos, llamadas a herramientas (`toolCalls`) y respuestas en tiempo real mediante HTTP unidireccional estándar, compatible con cualquier proxy.
4. **Handoff y Trazabilidad basada en Git:** El código remoto se commitea en ramas Git dedicadas (`feature/mesh-...`). El cliente local sincroniza vía `git pull`.

---

## 4. Contrato de la API REST Mínima (v1)

### `POST /v1/tasks` (Despachar Misión)
* **Headers:** `Authorization: Bearer <TOKEN>`
* **Payload:**
  ```json
  {
    "agent": "worker",
    "task": "Ejecutar migración de esquema y verificar tests",
    "context": "Contexto técnico adicional...",
    "workspace_root": "/opt/servidor/proyecto",
    "git_branch": "feature/migracion-04"
  }
  ```
* **Respuesta:** `202 Accepted` con `task_id` y URL del stream SSE (`/v1/tasks/{id}/events`).

### `GET /v1/tasks/{id}/events` (Streaming SSE)
Flujo continuo de eventos tipados:
* `event: thought` ➔ Pensamientos del modelo.
* `event: tool_call` ➔ Herramienta invocada y argumentos.
* `event: tool_result` ➔ Salida de la herramienta ejecutada en el servidor.
* `event: completion` ➔ Resultado final, commits generados y resumen.
* `event: error` ➔ Anomalía o detención de emergencia.

### `POST /v1/tasks/{id}/steer` (Mensaje en Caliente)
Permite reorientar al subagente remoto antes de su siguiente llamada al modelo (paridad con `subagent_send_message`).

### `POST /v1/tasks/{id}/reply` (Respuesta a Consulta / Human-in-the-Loop)
Permite al orquestador o humano responder a un evento `query` emitido por el subagente remoto (paridad con `subagent_reply`).
* **Payload:**
  ```json
  {
    "query_id": "q_42",
    "answer": "Confirmado, aplicar migración"
  }
  ```

---

## 5. Protocolo de Membresía y Descubrimiento de la Malla (`/v1/mesh`)

Para operar como una verdadera malla federada (Mesh), los nodos remotos anuncian sus capacidades al orquestador dinámicamente:

### `POST /v1/mesh/join` (Registro de Nodo)
* **Payload:**
  ```json
  {
    "node_id": "vps-la-fabrica-gpu",
    "endpoint": "http://100.64.0.15:8080",
    "hardware": {
      "cpus": 32,
      "ram_gb": 64,
      "has_gpu": true,
      "os": "linux/amd64"
    },
    "agents_advertised": [
      {
        "name": "heavy-tester",
        "description": "Suites masivas con Docker y Postgres",
        "tags": ["docker", "postgres", "long-running"]
      }
    ],
    "max_concurrency": 4
  }
  ```

### `POST /v1/mesh/heartbeat` (Keepalive)
* Pings periódicos (cada 15-30s) para confirmar disponibilidad y carga activa (`active_tasks`). Desconexión tras 3 latidos perdidos.

### `GET /v1/mesh/nodes` (Catálogo de Nodos y Agentes)
* Permite al orquestador y a `subagent_list_agents` descubrir en tiempo real qué agentes remotos están federados y disponibles.

### Exclusión Mutua, Locks de Territorio e Idempotencia en la Malla
Para garantizar que dos nodos de la malla no colisionen ni ejecuten trabajo redundante:
1. **Lock de Rama Exclusivo (`ExclusiveBranchLock`):**  
   Dos tareas no pueden ejecutar concurrentemente sobre la misma rama del mismo repositorio (`repo:branch`). El registro de la malla adquiere un lock atómico en memoria al asignar la tarea a un nodo. Si una nueva tarea requiere esa misma rama, permanece en cola hasta que la primera finalice, commitee y libere el lock.
2. **Detección de Idempotencia y Tareas Duplicadas:**  
   Si se recibe una solicitud con una clave de idempotencia activa o idéntico fingerprint (`repo + branch + task`), el orquestador no despacha un segundo runner: devuelve el `task_id` existente y reengancha el stream al trabajo en curso.

---

## 6. Resiliencia, Persistencia y Ciclo de Vida de Tareas

Para garantizar desconexiones seguras sin pérdida de progreso ni consumo desmedido de recursos:

1. **Persistencia Append-Only (JSONL en Disco):**
   * Cada tarea mantiene un log continuo en `/var/log/gentle-mesh/tasks/{task_id}.jsonl`.
   * Permite retransmisión desde cualquier punto histórico sin saturar la RAM del servidor.
2. **Reconexión Transparente (`Last-Event-ID`):**
   * Todo evento SSE incluye un ID monótono (`id: 101`). Si el cliente se desconecta, envía el header HTTP estándar `Last-Event-ID` al reconectar y el servidor reproduce los eventos faltantes antes de continuar en vivo.
3. **Desacople de Consumo (Non-blocking Pub/Sub):**
   * Si la conexión del cliente es lenta o inestable, la goroutine de streaming no bloquea la ejecución del runner de Pi en el servidor.
4. **Limpieza en Dos Etapas (Configurable):**
   * *Etapa 1 (Inmediata):* El Git Worktree efímero se destruye inmediatamente tras la finalización o cancelación de la tarea para liberar espacio en disco.
   * *Etapa 2 (TTL diferido):* La metadata y el archivo `.jsonl` se conservan durante un periodo configurable (`GENTLE_MESH_TASK_TTL`, por defecto 24 horas) para permitir inspección y recuperación diferida, tras lo cual se purgan automáticamente.

---

## 7. Seguridad, Supervisión Autónoma y Aislamiento del Host

Para operar de forma 100% desatendida sin necesidad de supervisión humana activa:

1. **Desacople de Capacidad (Cerebro vs Músculo):**
   * El orquestador (laptop/cliente) es ultra-liviano: solo despacha JSON y lee streams de texto, pudiendo coordinar decenas de tareas simultáneas sin consumo de CPU.
   * Los límites de concurrencia y memoria residen exclusivamente en los **Worker Nodes remotos**, protegiendo su hardware físico contra saturación mediante colas de espera FIFO (`status: "queued"`).
2. **Supervisión Autónoma de Tareas (Sin Human-in-the-Loop):**
   * *Detección de Congelamiento / Inactividad:* Si un proceso pasa 5 minutos sin emitir eventos ni actividad de CPU, se aborta automáticamente por inactividad.
   * *Detección Semántica de Bucles:* Ventana deslizante que detecta herramientas fallando 3 veces consecutivas o cambios oscilatorios en el mismo archivo, inyectando auto-corrección o abortando con `loop_detected`.
   * *Auto-Commit de Resguardo:* Si una tarea expira por timeout o se cancela, el demonio ejecuta automáticamente un commit WIP (`mesh(wip): progreso parcial [task-id]`) para preservar todo el trabajo realizado hasta ese segundo.
3. **Reanudación y Autocuración sin Fricción:**
   * La reconexión tras cortes de red o reinicios de host es 100% automática y desatendida, reanudando desde el último checkpoint o sincronizando eventos pasados sin requerir confirmación manual.
4. **Aislamiento Estricto a Nivel de Kernel:**
   * *Process Groups (`Setpgid: true`):* Todo subproceso y sus comandos hijos se encapsulan en un grupo aislado; las señales de terminación jamás alcanzan a servicios del host (Nginx, Postgres, SSH).
   * *Jaula de Directorios:* Confinamiento estricto bajo `GENTLE_MESH_WORKSPACES_ROOT` con validación canónica de symlinks.
   * *Scrubbing de Entorno:* Lista blanca de variables de entorno hacia el subagente para no filtrar secretos del servidor anfitrión.

---

## 8. Trade-offs y Limitaciones Conocidas (Decisiones v1 vs v2)

Durante la conceptualización inicial del Punto 1 (Workspace y Transporte de Código), se identificaron cuatro limitaciones ("pegas") deliberadamente asumidas para la PoC v1:

1. **Triangulación y Dependencia de Git Remoto (`origin`):**
   * *Diagnóstico:* La laptop y el servidor remoto se sincronizan a través de GitHub/GitLab.
   * *Impacto:* El servidor remoto requiere credenciales con permisos de push, y el flujo no opera 100% offline o en LAN pura sin salida a internet.
   * *Evolución v2:* Sincronización punto a punto mediante streaming directo del delta (`git diff` + tarball de archivos untracked vía payload HTTP).

2. **Riesgo de "Stale Code" (Código Desfasado):**
   * *Diagnóstico:* Si el desarrollador olvida hacer `git push` en su máquina local antes de delegar, el agente remoto ejecuta sobre commits antiguos.
   * *Mitigación v1:* Validación en el cliente CLI local que alerte si el working tree local tiene commits o cambios sin pushear.

3. **Resiliencia de Worktrees y Recuperación Post-Crash:**
   * *Diagnóstico:* Tareas abortadas por caídas súbitas del demonio pueden dejar worktrees huérfanos en `/tmp/gentle-mesh/worktrees/`.
   * *Mitigación v1:* Rutina obligatoria de saneamiento en el arranque del servidor (`git worktree prune`).

4. **Conflictos de Merge Asincrónicos:**
   * *Diagnóstico:* Si el desarrollador continúa editando localmente mientras el subagente remoto trabaja, la integración de la rama devuelta puede generar conflictos.
   * *Mitigación v1:* El evento `completion` devuelve el hash y lista de archivos modificados para inspección previa a la integración.

---

## 9. Hoja de Ruta de la Prueba de Concepto (PoC)

* [ ] **Fase 1:** Especificación de tipos en Go (`pkg/protocol/`).
* [ ] **Fase 2:** Servidor HTTP con simulación de runner (`pkg/server/`).
* [ ] **Fase 3:** Cliente CLI para ejecutar tareas remotas (`cmd/gentle-mesh/`).
* [ ] **Fase 4:** Prueba real entre Laptop y Servidor La Fábrica.
* [ ] **Fase 5:** Presentación formal en el canal `#ideas-y-propuestas` de Discord de Gentleman Programming.
