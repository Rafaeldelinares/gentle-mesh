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

---

## 5. Hoja de Ruta de la Prueba de Concepto (PoC)

* [ ] **Fase 1:** Especificación de tipos en Go (`pkg/protocol/`).
* [ ] **Fase 2:** Servidor HTTP con simulación de runner (`pkg/server/`).
* [ ] **Fase 3:** Cliente CLI para ejecutar tareas remotas (`cmd/gentle-mesh/`).
* [ ] **Fase 4:** Prueba real entre Laptop y Servidor La Fábrica.
* [ ] **Fase 5:** Presentación formal en el canal `#ideas-y-propuestas` de Discord de Gentleman Programming.
