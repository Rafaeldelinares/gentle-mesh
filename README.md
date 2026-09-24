# Gentle Mesh 🌐

> **Transporte Distribuido Federado, Ejecución Remota de Subagentes y Consciencia Situacional para el Ecosistema Pi & Gentle AI.**

<p align="center">
  <a href="https://rafaeldelinares.github.io/gentle-mesh/"><strong>🚀 Ver Diagramas y Hub Interactivo en Vivo (GitHub Pages)</strong></a>
</p>

<p align="center">
  <a href="https://rafaeldelinares.github.io/gentle-mesh/"><strong>🏛️ Hub de Arquitectura y Pruebas</strong></a> &bull;
  <a href="https://rafaeldelinares.github.io/gentle-mesh/architecture/gentle-mesh-secure-environment.html"><strong>🌐 Topología de Nodos</strong></a> &bull;
  <a href="https://rafaeldelinares.github.io/gentle-mesh/architecture/gentle-mesh-test-verification-gates.html"><strong>🛡️ Compuertas de Seguridad</strong></a> &bull;
  <a href="docs/rfcs/001-remote-agent-transport.md"><strong>📄 RFC 001</strong></a>
</p>

---

### ⚠️ Nota de Gobernanza y Comunidad

> **Este repositorio es una propuesta de arquitectura técnica (RFC) y Prueba de Concepto (PoC) comunitaria creada para el ecosistema Gentle AI.**  
>
> **Este proyecto avanzará, evolucionará y se integrará de forma oficial única y exclusivamente bajo la revisión, orientación y aprobación explícita de [Alan Buscaglia (@gentleman-programming)](https://github.com/gentleman-programming), creador y líder del ecosistema Gentle AI.**  
>
> Hasta contar con su feedback y visto bueno, este repositorio permanece como un espacio de investigación abierta, validación técnica, prototipado riguroso y experimentación colaborativa por y para la comunidad.

---

## 1. La Visión: Crear Sistemas Entre Varios

La potencia distribuida en sistemas multi-agente no se trata únicamente de velocidad o de que una tarea tarde menos tiempo en compilar. Se trata de **la posibilidad de construir sistemas de software complejos entre varios seres humanos y múltiples agentes concurrentes**, cooperando sobre el mismo proyecto sin pisarse la cabeza.

Hoy en día, el uso de agentes de IA es aislado y solitario: un desarrollador con su terminal local. Si varios miembros de un equipo o de la comunidad lanzan agentes al mismo tiempo, el conflicto de merge es inevitable y nadie sabe qué está haciendo el otro.

`gentle-mesh` actúa como el **sistema nervioso central** de una red de agentes:
1. **Desacopla el Cómputo del Cliente:** Tu laptop sólo despacha intenciones y consume texto; la carga pesada (compilaciones, linters, suites de tests) se delega a nodos remotos (servidores, VPS o máquinas secundarias).
2. **Consciencia Situacional Compartida (Radar de Ámbitos):** Cada agente declara su **Dominio Arquitectónico**, sus **Superficies de Edición** autorizadas y su **Radio de Impacto** (*Blast Radius*).
3. **Detección Preventiva de Colisiones y Live Stream Hooking:** La malla detecta choques de territorio antes de escribir una sola línea de código y permite que clientes secundarios se enganchen en vivo a tareas duplicadas ya en curso.
4. **Binario Único en Go Puro:** Cero dependencias pesadas, compilación estática (`CGO_ENABLED=0`), arranque en milisegundos y consumo de memoria ridículamente bajo (~30 MB de RAM para un clúster de 6 nodos).

---

## 2. Pila Tecnológica y Arquitectura

* **Lenguaje:** Go 1.22+ estándar (`net/http`, `encoding/json`, `sync`, `context`, `database/sql`). Cero frameworks web externos ni librerías de C.
* **Transporte:** HTTP REST + Server-Sent Events (SSE) para streaming continuo de pensamientos (`thought`), llamadas a herramientas (`tool_call`) y resultados.
* **Persistencia Dual y Resiliencia ante Caídas (Crash Recovery):**
  * **Streaming de Eventos:** Append-only logs en formato **JSONL** (`<tasks-dir>/{id}.jsonl`). Permite reconexión histórica instantánea vía el header estándar `Last-Event-ID` con consumo de RAM constante $O(1)$.
  * **Estado Maestro y Rehidratación:** Base de datos embebida **SQLite en Go puro** (`modernc.org/sqlite`, `CGO_ENABLED=0`) con modo **WAL** (*Write-Ahead Logging*). Si el servidor se apaga o reinicia, rehidrata automáticamente el catálogo de tareas sin pérdida de estado.
* **Supervisión Autónoma:** Detección de inactividad, loop detection (bloqueo tras 3 fallas idénticas de herramientas) y auto-commit de seguridad (WIP) sin popups interactivos.

```text
gentle-mesh/
├── cmd/
│   └── gentle-mesh/          # CLI principal: server, worker, nodes, radar, run
├── pkg/
│   ├── protocol/             # Tipos canónicos, eventos SSE, serialización y colisiones
│   ├── server/
│   │   ├── http/             # Servidor REST, middleware Bearer y streaming SSE
│   │   ├── registry/         # Catálogo de nodos, branch locking e idempotencia
│   │   ├── store/            # TaskStore: MemoryStore y SQLiteStore embebido (WAL mode)
│   │   ├── task/             # Gestor de tareas, state machine, crash recovery y logger JSONL
│   │   ├── runner/           # Abstracción Runner, MeshRunner inteligente y SimulatedRunner
│   │   ├── federation/       # Peering M2M y TerritoryManager para colisiones compartidas
│   │   └── worker/           # Servidor worker autónomo con despacho remoto HTTP
├── docs/
│   ├── architecture/         # Diagramas interactivos y Hub de verificación (Archify)
│   ├── rfcs/                 # RFC 001: Especificación técnica canónica
│   └── EVALUATION_GUIDE_FOR_LLMS.md # Guía para revisión externa (Claude / GPT)
└── docker-compose.test.yml   # Topología multi-nodo de prueba (1 coordinator + 5 workers)
```

### 2.1 Persistencia Dual y Recuperación ante Caídas (Crash Recovery)

Para garantizar que Gentle Mesh funcione como un demonio de infraestructura confiable de grado de producción sin requerir servidores de base de datos externos (PostgreSQL, MySQL o Redis):

1. **Separación de Responsabilidades en Almacenamiento:**
   * **JSONL para el flujo de eventos:** Los eventos de streaming se escriben directamente a archivos append-only con `fsync` selectivo en eventos críticos, garantizando velocidad de escritura y lectura lineal para clientes SSE.
   * **SQLite para el estado maestro:** Cada transición de ciclo de vida (`queued` $\to$ `running` $\to$ `completed` / `failed` / `canceled`) se sincroniza en SQLite con índices optimizados en `status`, `created_at` y `branch`.
2. **Rehidratación Automática y Crash Resilience:**
   * Si el proceso de `gentle-mesh` se detiene o se reinicia el sistema operativo, al arrancar nuevamente **reconstruye de forma transparente todas las tareas históricas** en memoria, reanudando la capacidad de consulta (`GET /v1/tasks/{id}`) y el filtrado sin intervención humana.
3. **Go Puro sin CGO (`CGO_ENABLED=0`):**
   * Emplea `modernc.org/sqlite`, una traducción directa del motor SQLite a Go puro. Mantiene intacta la promesa de **binario estático único sin dependencias del sistema** (~16 MB) ejecutable en x86_64, ARM64 o dispositivos embebidos.
4. **Modo WAL y Alta Concurrencia:**
   * Configurado con `PRAGMA journal_mode=WAL` y `PRAGMA busy_timeout=5000` para permitir lecturas masivas concurrentes sin bloquear las escrituras de los agentes.
5. **Configuración Flexible vía CLI:**
   * Parámetro `-db-path`: Define la ruta del archivo de base de datos (por defecto `<tasks-dir>/gentle-mesh.db`). Para entornos efímeros o pruebas puramente en memoria, basta con indicar `-db-path=none`.

### 2.2 Planificación Territorial Transparente y Semáforo Inteligente (TerritoryMode)

Un control de admisión territorial que sólo sabe rechazar rompe la fluidez del trabajo colaborativo. Cuando varias sesiones, orquestadores o miembros de un equipo despachan misiones concurrentes sobre el mismo repositorio, el rechazo abrupto (`HTTP 409 Conflict`) obliga a intervención humana o a reintentos en bucle desde el cliente: alguien tiene que mirar el radar, esperar a que la rama se libere y volver a lanzar la tarea a mano. Ese ida y vuelta destruye exactamente la autonomía que la malla promete.

Para resolverlo, el coordinador incorpora un **Semáforo Inteligente de Territorio (`TerritoryScheduler`)**: en lugar de rebotar la tarea, la **retiene y la despacha sola** cuando el territorio se libera.

1. **Semáforo transparente (`TerritoryModeQueue`, modo por defecto):**
   * Cuando una tarea nueva colisiona territorialmente con otra en ejecución (misma rama bloqueada o superficies de edición superpuestas), el coordinador **no falla**: responde `HTTP 201 Created` con `status: "queued"` y la coloca en una **cola FIFO atómica**.
   * El cliente recibe su `task_id` y su `events_url` de inmediato, y puede engancharse al stream SSE aunque la tarea aún no haya arrancado.
2. **Despacho automático secuencial:**
   * Tan pronto como la tarea en curso finaliza (o se cancela) y libera su cerradura de rama, el planificador invoca `TerritoryScheduler.Drain()`, que reexamina la cola y **despacha sin intervención humana** la siguiente tarea cuyo territorio ya no colisiona.
   * El respeto del orden FIFO y el despacho secuencial garantizan que dos agentes jamás comiteen concurrentemente sobre el mismo ref de Git.
3. **Los 4 modos soportados (`-territory-mode`):**
   * `queue` (**por defecto**): encola transparentemente ante conflictos de territorio y despacha de forma secuencial en orden FIFO.
   * `warn`: despacha la tarea de inmediato pese al conflicto, pero emite una **advertencia no fatal** en el stream de eventos (un evento `thought`) para dejar constancia del solapamiento.
   * `strict`: rechaza de inmediato con `HTTP 409 Conflict` (modo estricto tradicional, útil para pipelines de CI que exigen exclusión dura).
   * `disabled`: desactiva por completo la comprobación de conflictos territoriales y despacha siempre.
4. **Configuración vía CLI:**
   * El subcomando `server` expone el flag `-territory-mode=queue|warn|strict|disabled`. Un valor desconocido aborta el arranque con un error explícito, evitando degradaciones silenciosas de política.

---

## 3. Demostración Rápida en Local (Entorno Seguro)

### Requisitos
* Go 1.22+ o Docker / Docker Compose.

### Ejecutar todas las pruebas con detector de carreras
```bash
go test -v -race ./...
```
*(Todos los paquetes cuentan con cobertura unitaria y 0 race conditions).*

### Levantar el coordinador en local
```bash
go run ./cmd/gentle-mesh server \
  -addr :8080 \
  -territory-mode queue
```
El flag `-territory-mode` acepta `queue` (por defecto), `warn`, `strict` o `disabled`; consultá el semáforo inteligente en la sección 2.2.

### Levantar el clúster de prueba de 6 nodos en Docker
El repositorio incluye una topología lista para probar en una red bridge aislada (`gentle-mesh-net`):
```bash
docker compose -f docker-compose.test.yml up -d
```

### Consultar los nodos registrados en la malla
```bash
go run ./cmd/gentle-mesh nodes -coordinator http://localhost:8080
```
Salida esperada:
```text
NODE ID         ENDPOINT                    STATUS  CONCURRENCY  AGENTS   TAGS
worker-alpha    http://worker-alpha:8081    online  0/2          worker   go,fast
worker-beta     http://worker-beta:8081     online  0/4          worker   heavy,docker
worker-gamma    http://worker-gamma:8081    online  0/2          explore  research
worker-delta    http://worker-delta:8081    online  0/1          worker   gpu,ml
worker-epsilon  http://worker-epsilon:8081  online  0/2          verify   ci,testing
```

### Consultar el Radar de Ámbitos y Actividad en Vivo
```bash
go run ./cmd/gentle-mesh radar -coordinator http://localhost:8080
```

### Despachar una tarea con ámbito acotado
```bash
go run ./cmd/gentle-mesh run -coordinator http://localhost:8080 \
  -task "Refactorizar validación de JWT" \
  -domain "auth" \
  -blast-radius "isolated-branch" \
  -surfaces "pkg/auth/jwt.go"
```

---

## 4. Documentación y Arquitectura Interactiva (Archify)

El repositorio incluye diagramas de arquitectura interactivos y autocontenidos (HTML puro sin dependencias externas) generados con **Archify**, diseñados para explorar visualmente el sistema, simular rutas y comprender el porqué de cada compuerta de seguridad.

> 🚀 **Demos interactivas en vivo (ejecutables directamente en el navegador vía GitHub Pages):**
> * 🏛️ **[Hub Central de Arquitectura y Verificación](https://rafaeldelinares.github.io/gentle-mesh/):** Portal principal con la justificación de los 7 vectores de prueba y botones directos para reproducir cada test con tour e intro explicativa.
> * 🛡️ **[Matriz de Pruebas y Compuertas de Seguridad](https://rafaeldelinares.github.io/gentle-mesh/architecture/gentle-mesh-test-verification-gates.html):** Pipeline paso a paso con las 9 compuertas técnicas (G1 a G5), tour interactivo ("¿Para qué sirve este test?"), mitigación de riesgos y aserciones en Go.
> * 🌐 **[Topología Pentagonal y Clúster Seguro](https://rafaeldelinares.github.io/gentle-mesh/architecture/gentle-mesh-secure-environment.html):** Visualización geométrica regular del clúster de 6 nodos en Docker, enrutamiento por tags (GPU, heavy, fast, etc.) y modal explicativo de cada componente.

### Código y Especificación Técnica en el Repositorio

* 📁 **Archivos locales autocontenidos:** En [`docs/architecture/`](docs/architecture/) se encuentran los archivos `.html` originales. Al ser 100% autónomos y sin dependencias, también pueden abrirse con doble clic directamente en local desde cualquier navegador.
* 📄 **[RFC 001 — Remote Agent Transport & M2M Federation](docs/rfcs/001-remote-agent-transport.md):** Especificación técnica canónica y formal.
* 🤖 **[Guía de Evaluación Externa para LLMs](docs/EVALUATION_GUIDE_FOR_LLMS.md):** Prompt y metodología estructurada para someter este código a revisión con Claude 3.5 Sonnet o GPT-4o.
* 📋 **[Convenciones del Proyecto](AGENTS.md):** Reglas operativas, competencias y filosofía de desarrollo.

---

## 5. Built with Gentle-AI

<div align="center">

<a href="https://github.com/Gentleman-Programming/gentle-ai">
  <img width="220" src="https://raw.githubusercontent.com/Gentleman-Programming/gentle-ai/main/docs/assets/brand/built-with-gentle-ai.png" alt="Built with Gentle-AI" />
</a>

<p><sub>Construido y disciplinado con <strong><a href="https://github.com/Gentleman-Programming/gentle-ai">Gentle-AI</a></strong> — Memory, Workflows & Evidence.</sub></p>

</div>

---

## 6. Licencia

Código abierto bajo licencia MIT (o la que determine la gobernanza comunitaria de Gentleman Programming).
