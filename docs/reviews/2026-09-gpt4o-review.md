# Thermonuclear Review: GPT-4o

> **Fecha:** 23 de septiembre de 2026  
> **Evaluador:** OpenAI GPT-4o  
> **Rol:** Arquitecto Senior de Sistemas Distribuidos y Concurrencia en Go  
> **Alcance:** Repositorio completo (`pkg/protocol`, `pkg/server`, `cmd/gentle-mesh`), RFC 001, Dockerfile, topología multi-nodo y suite de tests.  

---

## Veredicto ejecutivo

**La idea es buena y el núcleo conceptual tiene bastante sentido. Pero el código actual no está todavía al nivel de un runtime distribuido de producción ni de una 1.0 comunitaria.**

Mi lectura actual sería:

| Dimensión | Evaluación |
|---|---:|
| Concepto arquitectónico | **8.5/10** |
| Diseño Go | **7.5/10** |
| Concurrencia local | **7/10** |
| Protocolo SSE | **6/10** |
| Persistencia JSONL | **6.5/10** |
| Resiliencia distribuida | **4/10** |
| Territory/Radar | **7.5/10** |
| Seguridad | **4.5/10** |
| Madurez actual | **5.5/10** |
| Potencial | **8.5/10** |

Y hay una conclusión especialmente importante:

> **`gentle-mesh` tiene más valor como "coordination + execution fabric para agentes de programación" que como simple sistema de balanceo de workers.**

Esa diferenciación es precisamente donde creo que está su potencial.

---

# 1. Arquitectura Go

## Lo que está bien

La separación:

```text
HTTP
  ↓
TaskManager
  ↓
ManagedTask
  ↓
Runner
  ↓
EventSink
```

es conceptualmente correcta.

La interfaz:

```go
type Runner interface {
    Run(ctx context.Context, req protocol.TaskRequest, sink EventSink) error
}
```

es especialmente buena.

El runner no conoce:

- HTTP
- SSE
- JSONL
- `TaskManager`
- Registry
- almacenamiento

y solamente conoce un `EventSink`.

Eso es buen diseño Go.

También me gusta que `ManagedTask` sea el aggregate que concentra:

- estado
- cancelación
- secuencia de eventos
- subscribers
- queries
- persistencia

y que `TaskManager` sea el catálogo/lifecycle manager.

La utilización de `sync.RWMutex` y `atomic.Int64` es razonable. La máquina de estados está explícita y no se está intentando resolver con una maraña de goroutines. 

---

# Pero aquí empieza la parte "thermonuclear"

## 🔴 Problema 1 — `ManagedTask` hace demasiadas cosas

Actualmente `ManagedTask` es simultáneamente:

```text
Task state machine
+ event sequencer
+ event store adapter
+ pub/sub broker
+ query coordinator
+ territory tracker
+ activity tracker
```

Eso funciona para una PoC.

Para una 1.0 empezaría a separarlo:

```text
ManagedTask
 ├── TaskState
 ├── EventLog
 ├── EventHub
 ├── QueryBroker
 └── Territory
```

No porque el código actual sea incorrecto, sino porque estás construyendo un componente que eventualmente tendrá muchísima lógica de lifecycle.

La interfaz `Runner/EventSink` sí la conservaría.

---

# 🔴 Problema 2 — `TaskManager.Close()` tiene un problema de lifecycle

`Close()` cancela los contextos, cierra loggers y cierra subscribers. 

Pero no espera a que terminen los runners.

El servidor hace:

```go
go func(...) {
    defer releaseLock()
    _ = r.Run(...)
}()
```

y no registra esa goroutine en ningún `WaitGroup`. 

Eso significa que puedes tener:

```text
Shutdown()
   ↓
TaskManager.Close()
   ↓
logger.Close()
   ↓
HTTP server stopped

             ↓
       runner goroutine
             ↓
       EmitEvent()
             ↓
       logger cerrado
```

El `context` ayuda **solamente si el Runner coopera**.

El `SimulatedRunner` sí coopera correctamente comprobando `ctx.Done()`. 

Pero un runner real que ejecute:

```text
Pi
 └── child process
      └── shell
           └── docker
                └── subprocess
```

necesita garantías adicionales.

### Lo que haría

`TaskManager` debería poseer explícitamente el lifecycle:

```go
type TaskManager struct {
    ...
    wg sync.WaitGroup
}
```

y:

```text
Create
 ↓
wg.Add(1)
 ↓
runner
 ↓
defer wg.Done()
 ↓
Shutdown
 ↓
cancel
 ↓
wait
 ↓
close persistent resources
```

Eso es **P0**.

---

# 🔴 Problema 3 — cleanup puede matar la persistencia mientras existe un runner externo

`CleanupExpired()` elimina tareas terminales y cierra su logger. 

Pero "terminal" y "runner realmente muerto" no son necesariamente lo mismo.

Especialmente porque `EmitEvent(EventCompletion)` marca inmediatamente:

```text
Completed
```

y cancela el contexto. 

El sistema debería tener una distinción:

```text
Task state = Completed
Execution state = ProcessExited
```

No asumir que ambas cosas son idénticas.

---

# 2. SSE

Aquí hay una mezcla de cosas muy buenas y un fallo importante.

## Lo bueno

La estructura:

```text
event:
id:
data:

```

es correcta.

`Last-Event-ID` se procesa y se convierte en cursor numérico. 

El servidor también hace:

```go
flusher.Flush()
```

después de cada evento. 

Y el contexto HTTP se observa:

```go
case <-r.Context().Done():
    return
```

Bien.

---

# 🔴 Problema SSE más importante: estás perdiendo eventos silenciosamente

En `EmitEvent()`:

```go
select {
case sub <- *evt:
default:
}
```

Es decir:

> si el buffer del cliente está lleno, descartamos el evento.

Esto está hecho deliberadamente para que un cliente lento nunca bloquee al runner. La RFC incluso lo describe como "Non-blocking Pub/Sub". 

Pero hay una consecuencia muy seria:

```text
event 100 → cliente recibe
event 101 → recibe
event 102 → buffer lleno → DESCARTADO
event 103 → DESCARTADO
event 104 → recibe
```

El cliente puede seguir conectado y jamás enterarse de que perdió 102 y 103.

`Last-Event-ID` **no arregla esto automáticamente**, porque el cliente no se desconectó.

Por tanto tienes:

> **at-most-once live delivery + replay-on-reconnect**

cuando probablemente necesitas:

> **ordered durable delivery**

### Solución

Si el subscriber se queda atrás:

```text
buffer full
    ↓
mark subscriber as lagging
    ↓
disconnect stream
    ↓
client reconnects
    ↓
Last-Event-ID
    ↓
replay
    ↓
live
```

Mucho mejor:

```go
type Subscriber struct {
    ch       chan Event
    lastSent int64
    lagging  atomic.Bool
}
```

Y nunca descartaría silenciosamente eventos de un stream que se supone fiable.

---

# 🔴 Segundo problema SSE: replay no es O(1) RAM

La RFC dice que JSONL permite consumo de RAM constante O(1). 

Pero el código hace:

```go
history, err = t.logger.ReadEvents(sinceID)
```

y:

```go
events := make([]protocol.Event, 0)
...
events = append(events, evt)
```

Después `Subscribe()` crea un canal cuyo tamaño es:

```go
bufSize = len(history) + 64
```



Por tanto:

```text
100 eventos       → pequeña RAM
10.000 eventos    → bastante RAM
100.000 eventos   → problema
1.000.000 eventos → desastre
```

Y además:

```text
history
+
channel buffer
+
JSON parsing
+
HTTP/SSE
```

multiplican memoria.

Así que:

> **La implementación actual NO tiene consumo O(1) de RAM para replay.**

Esto es uno de los hallazgos más claros de la revisión.

---

# Solución

El replay debería ser streaming:

```go
func (l *JSONLLogger) Replay(
    ctx context.Context,
    sinceID int64,
    emit func(Event) error,
) error
```

y:

```text
open file
 ↓
scanner
 ↓
decode line
 ↓
if id > sinceID
    ↓
emit
 ↓
next line
```

Nunca:

```go
[]Event
```

para todo el histórico.

Eso sí te daría un replay aproximadamente O(1) en memoria.

---

# 🔴 Tercer problema SSE: parser incompleto

`ParseSSE()` es razonable como parser de **tu propio formato**, pero no es realmente un parser SSE completo.

Por ejemplo:

```go
scanner := bufio.NewScanner(...)
```

sin aumentar el buffer.

El límite de `Scanner` es ~64 KiB.

Sin embargo el logger admite líneas de hasta 1 MiB.

Tenemos:

```text
JSONL
  ↓
1 MB permitido

SSE parser
  ↓
~64 KB
```

Inconsistencia.

Además `ParseSSE()` presupone que recibe un bloque completo. No es un parser incremental de stream SSE. Si recibe varios eventos juntos, conceptualmente los trata como una sola unidad.

Para una implementación realmente robusta haría:

```text
io.Reader
   ↓
SSEDecoder
   ↓
Event
```

y tests con:

- CRLF
- LF
- fragmentación arbitraria
- múltiples `data:`
- comentarios
- eventos vacíos
- `id:`
- campos desconocidos
- payload grande
- desconexión a mitad de evento

El código actual cubre bastante del caso feliz, pero no alcanza todavía ese nivel.

---

# 🔴 Problema adicional: "thoughts" como contrato

Esto me preocupa arquitectónicamente más que el parser.

El protocolo define:

```go
ThoughtPayload
```

y la RFC habla explícitamente de:

> pensamientos del modelo

y el Radar incluso extrae las primeras 100 runas del pensamiento. 

Yo **no convertiría el chain-of-thought en una API pública de infraestructura**.

Separaría:

```text
agent_internal_reasoning     ❌
agent_progress               ✅
tool_call                    ✅
tool_result                  ✅
phase_change                 ✅
status                       ✅
current_action               ✅
```

Por ejemplo:

```json
{
  "type": "progress",
  "phase": "explore",
  "action": "Inspecting authentication middleware"
}
```

El Radar no necesita conocer el razonamiento interno del modelo.

Eso además reduce:

- coste de almacenamiento
- coste de streaming
- superficie de privacidad
- exposición accidental de información sensible
- dependencia del protocolo respecto al proveedor de LLM

---

# 3. JSONL vs SQLite

Aquí mi conclusión es:

## Para PoC: JSONL

**Sí.**

Es extremadamente sencillo:

```text
append
fsync
replay
grep
backup
```

y encaja perfectamente con Go stdlib.

No metería SQLite simplemente porque "es más profesional".

---

## Para 1.0: JSONL necesita evolucionar

El problema no es JSONL.

El problema es:

```text
JSONL + replay completo
+
sin index
+
fsync por evento
+
sin rotation
+
sin compaction
```

El logger hace `Sync()` después de **cada evento**. 

Para streaming de tokens eso puede ser brutal:

```text
token
 ↓
JSON marshal
 ↓
write
 ↓
fsync
 ↓
broadcast
```

Si produces cientos/miles de eventos, conviertes el disco en parte del critical path del agente.

### Yo haría:

```text
Event
 ↓
memory buffer
 ↓
append
 ↓
group commit
 ↓
fsync cada X ms
```

y configurable:

```text
durability:
  relaxed
  normal
  durable
```

---

# ¿SQLite?

Lo reservaría para:

```text
task index
territory index
idempotency
node registry
metadata
```

pero **no necesariamente para el event stream**.

Una arquitectura muy buena sería:

```text
SQLite / metadata
       +
JSONL event log
```

aunque contradiga un poco el principio de cero dependencia externa si SQLite se introduce mediante CGO/driver.

Si el objetivo principal es un binario extremadamente pequeño, seguiría con JSONL.

---

# 4. Territory / Radar

Aquí creo que está una de las mejores ideas del proyecto.

El concepto:

```text
repo
 ↓
branch
 ↓
edit surfaces
 ↓
domain
 ↓
blast radius
 ↓
phase
 ↓
current action
```

es mucho más útil para agentes de programación que:

```text
CPU = 60%
RAM = 40%
worker available = true
```

La RFC articula muy bien esa diferencia. 

---

# Pero hay una gran diferencia entre "metadata" y "enforcement"

Ahora mismo el sistema conoce:

```text
domain = auth
surface = pkg/auth/*
blast = shared-schema
```

pero eso no significa necesariamente que el agente **no pueda modificar**:

```text
pkg/payment/
```

Es decir:

> **Territory actualmente es principalmente un sistema de coordinación/admisión, no un sistema de seguridad de escritura.**

Eso está bien.

Pero la documentación debería decirlo explícitamente.

---

# 🔴 Blast Radius no debería ser una dimensión equivalente a surface

Este modelo:

```text
domain
edit_surfaces
blast_radius
```

es útil, pero conceptualmente las tres cosas no son equivalentes.

Yo lo modelaría:

```text
                 Territory
                    │
       ┌────────────┼────────────┐
       │            │            │
    Identity      Scope       Risk
       │            │            │
     domain     surfaces    blast_radius
```

Porque:

```text
domain = clasificación
surface = autoridad espacial
blast_radius = riesgo
```

son semánticas distintas.

Esto te ayudará mucho cuando aparezcan políticas más complejas.

---

# 🔴 Otro problema: branch locking es solamente local

`BranchLockManager` es:

```go
map[branchKey]branchLock
```

protegido por mutex. 

Eso significa:

```text
Coordinator A
   branch X locked

Coordinator B
   branch X unlocked
```

si tienes dos coordinadores.

Por tanto el sistema **no tiene todavía un lock distribuido real**.

Y esto es crítico porque la RFC dedica una parte importante a Mesh-to-Mesh y territorio federado. 

---

# Esto lleva al mayor problema arquitectónico de todos

## La federación descrita en RFC > implementación actual

La RFC define:

```text
POST /v1/mesh/peers/register
GET  /v1/mesh/peers
GET  /v1/mesh/territory
```

y un protocolo de intercambio territorial entre coordinadores. 

Pero los endpoints registrados actualmente en `handlers.go` son:

```text
/v1/mesh/join
/v1/mesh/heartbeat
/v1/mesh/nodes
/v1/mesh/radar
/v1/tasks
...
```

No aparecen los endpoints M2M definidos en la RFC. 

Por tanto:

> **La federación M2M es todavía principalmente una arquitectura especificada, no una capacidad implementada de extremo a extremo.**

Esto no es malo para una PoC.

Pero hay que etiquetarlo como tal.

---

# 5. Live Stream Hooking

## Conceptualmente: sí

Me gusta mucho.

La secuencia:

```text
Task A
   ↓
running

Task B
   ↓
same idempotency/fingerprint
   ↓
don't execute
   ↓
attach to A
```

es exactamente lo que uno quiere en un sistema de agentes.

La RFC lo plantea correctamente. 

Es especialmente interesante porque evita:

```text
tokens duplicados
CPU duplicada
tests duplicados
merge duplicado
```

---

# Pero hay que separar dos conceptos

Actualmente:

```text
idempotency key
```

y:

```text
semantic duplicate
```

son conceptualmente diferentes.

### Idempotency

Significa:

> "Esta petición ya fue aceptada."

### Semantic duplicate

Significa:

> "Esta tarea parece equivalente a otra."

No son la misma garantía.

Yo usaría:

```text
IdempotencyKey
    ↓
exact identity

TaskFingerprint
    ↓
semantic similarity / dedup
```

y no trataría una huella semántica como garantía fuerte de equivalencia.

---

# 6. Idempotencia: hay una carrera interesante

El código primero hace:

```text
Get(key)
```

y después:

```text
CreateTask()
```

y posteriormente:

```text
RecordOrGet(key)
```



Esto está protegido finalmente por `RecordOrGet`, por lo que no veo una duplicación persistente si las dos peticiones compiten.

Pero sí puedes crear temporalmente:

```text
Request A
  Create Task A

Request B
  Create Task B

RecordOrGet
  A wins

B cancelled
```

Es decir, el sistema puede ejecutar trabajo efímero que luego descarta.

En un entorno barato:

```text
crear → cancelar
```

es tolerable.

En un agente real:

```text
crear proceso Pi
crear worktree
descargar repo
arrancar LLM
consumir tokens
...
cancelar
```

puede ser carísimo.

La reserva de idempotency debería ser **antes de materializar la ejecución**.

---

# 7. Seguridad

Aquí soy bastante más crítico.

## 🔴 Bearer token opcional

El middleware permite:

```text
BearerToken == ""
     ↓
authentication disabled
```



Para desarrollo está bien.

Pero para producción:

> **debe fallar cerrado si el servidor no está explícitamente en modo local/development.**

---

# 🔴 No hay límites de body

Los handlers hacen:

```go
json.NewDecoder(r.Body).Decode(&req)
```

sin un:

```go
http.MaxBytesReader(...)
```

Por tanto una API expuesta puede recibir payloads enormes.

Esto es especialmente peligroso en:

```text
TaskRequest
ToolCall
ToolResult
Reply
Join
Heartbeat
```

Añadiría límites explícitos:

```text
TaskRequest      1 MB
Join              256 KB
Reply             64 KB
Heartbeat         64 KB
```

aproximadamente, dependiendo del contrato.

---

# 🔴 `http.Server` necesita hardening

Actualmente se construye básicamente:

```go
http.Server{
    Addr: cfg.Addr,
    Handler: ...
}
```



Para Internet/producción faltan parámetros como:

```go
ReadHeaderTimeout
ReadTimeout
IdleTimeout
MaxHeaderBytes
```

Hay que tener cuidado con `WriteTimeout` porque SSE necesita conexiones largas.

---

# 🔴 No hay rate limiting

Un atacante puede hacer:

```text
POST /v1/tasks
POST /v1/tasks
POST /v1/tasks
...
```

y cada tarea puede:

- abrir archivo
- crear contexto
- crear estructura
- lanzar goroutine
- eventualmente ejecutar agente

Eso es un DoS trivial si está expuesto.

Necesitas como mínimo:

```text
max active tasks
max queued tasks
per-token rate limit
per-node concurrency
max SSE subscribers/task
max task lifetime
```

---

# 🔴 Endpoint de join es una frontera de confianza

`/v1/mesh/join` acepta un endpoint proporcionado por el nodo. 

Si en una evolución futura el coordinador hace:

```text
join endpoint
   ↓
HTTP request
```

entonces aparece un potencial SSRF.

Deberías tratar:

```text
node endpoint
```

como **un dato no confiable**, no como una URL que se puede consultar libremente.

---

# 8. Partición de red

Este es el verdadero problema de una Mesh.

Supongamos:

```text
             Coordinator
             /         \
          Node A       Node B
```

y se corta:

```text
Coordinator ──X── Node B
```

Node B puede estar todavía ejecutando:

```text
task-123
branch feature/auth
```

mientras Coordinator considera:

```text
node B = offline
```

Entonces otro agente puede obtener:

```text
feature/auth = libre
```

y empezar a trabajar.

Resultado:

```text
Agent A ── modifies auth
Agent B ── modifies auth
```

El heartbeat **no es un lock distribuido**.

Necesitas leases.

Por ejemplo:

```text
TerritoryLease {
    territory_id
    owner
    epoch
    acquired_at
    expires_at
}
```

y renovación:

```text
lease 30s
renew every 10s
```

Pero incluso esto no resuelve completamente split-brain.

Para garantías fuertes necesitas una autoridad de consenso o un mecanismo externo.

---

# 9. Git branch lock tampoco protege Git realmente

El lock actual es:

```text
memory
```

No es:

```text
Git ref lock
```

ni:

```text
distributed lease
```

Además, la RFC dice que el repo debería normalizarse para evitar:

```text
https://github.com/x/y
git@github.com:x/y.git
https://github.com/x/y.git
```

como repositorios distintos. 

Pero el `BranchLockManager` recibe directamente:

```go
repo string
```

y hace:

```go
branchKey{repo: repo, branch: branch}
```



Por tanto esa normalización tiene que hacerse **antes de entrar en el lock manager**, o el mismo repositorio puede tener representaciones diferentes.

---

# 10. Docker

El Dockerfile es sencillo y correcto conceptualmente:

```dockerfile
CGO_ENABLED=0
GOOS=linux
go build
```

y runtime Alpine mínimo. 

Eso encaja con tu filosofía.

Pero ojo:

> el Dockerfile actual no demuestra por sí mismo que el binario final sea de ~15 MB.

Hay que medirlo.

También añadiría:

```dockerfile
USER nonroot
```

o equivalente.

Y un filesystem de runtime cuidadosamente diseñado:

```text
/usr/local/bin/gentle-mesh  read-only
/var/lib/gentle-mesh        writable
/var/log/gentle-mesh        writable
/workspaces                 writable
```

La aplicación no debería necesitar root.

---

# 11. La topología Docker es útil, pero no demuestra todavía la Mesh real

La composición crea:

```text
1 coordinator
+
5 workers
```

con heartbeats y capacidades distintas. 

Eso es bueno como entorno de integración.

Pero veo una diferencia importante entre:

```text
5 contenedores registrados
```

y:

```text
5 workers ejecutando realmente agentes remotos coordinados
```

El runner por defecto del servidor es `SimulatedRunner`. 

Por tanto la prueba actual demuestra principalmente:

```text
HTTP
registry
task lifecycle
SSE
JSONL
heartbeat
Docker topology
```

pero no todavía:

```text
real Pi process
real worktree
real Git
real process tree
real LLM
real cancellation
real remote execution
```

Eso debe quedar muy claro en la documentación de madurez.

---

# 12. Tests

Hay bastante trabajo de testing.

Por ejemplo, el logger tiene tests para:

- escritura secuencial
- replay
- `sinceID`
- payload grande
- escrituras concurrentes
- logger cerrado



Y el README declara:

```bash
go test -v -race ./...
```

como test de referencia. 

Eso es positivo.

Pero hay una distinción fundamental:

> **Que exista una suite que pasa `-race` no demuestra ausencia de bugs de concurrencia de protocolo.**

`-race` detecta data races.

No detecta necesariamente:

```text
lost events
incorrect state machines
distributed split-brain
wrong ownership
semantic duplicate
stale lease
event replay gaps
process zombies
resource exhaustion
```

Y precisamente esos son los riesgos principales de Gentle Mesh.

---

# 13. El test que yo considero obligatorio

Crearía un paquete:

```text
tests/chaos/
```

con escenarios como:

### Test A

```text
start task
emit 10.000 events
kill SSE connection
reconnect Last-Event-ID
assert exact sequence
```

### Test B

```text
slow subscriber
emit 100.000 events
assert no silent event loss
```

### Test C

```text
kill coordinator
restart
assert task state
```

### Test D

```text
kill worker
while task running
assert lease expiry
assert territory released
```

### Test E

```text
network partition
A sees task
B sees task
both attempt admission
assert no double ownership
```

### Test F

```text
shutdown during:
    runner
    fsync
    SSE
    query
    git operation
```

### Test G

```text
10 concurrent identical idempotency requests
assert exactly one execution
```

### Test H

```text
1 million events
replay from event 999000
memory usage remains bounded
```

Este último probablemente descubriría inmediatamente el problema O(1) del diseño actual.

---

# 14. Un problema muy importante: los eventos son demasiado pesados

Actualmente:

```go
Event {
    ID
    TaskID
    Timestamp
    Type
    Payload
}
```

y se serializa el evento completo dentro del:

```text
data:
```

Por tanto cada SSE contiene:

```text
id: 100

event: thought

data: {
   "id":100,
   "task_id":"...",
   "timestamp":...,
   "type":"thought",
   "payload":...
}
```

El `id` y `type` están duplicados.

No es terrible.

Pero a millones de eventos sí importa.

Podrías tener:

```text
id:
event:
data: payload
```

y mantener el envelope solamente en JSONL.

---

# 15. El Radar es bueno, pero necesita un modelo temporal

Ahora tienes:

```text
last_activity_at
```

Eso está bien.

Pero necesitas distinguir:

```text
active
idle
stalled
disconnected
lease_expired
unknown
```

No simplemente:

```text
last event = 5 min ago
```

porque un agente puede estar ejecutando:

```text
go test ./...
```

durante 8 minutos sin emitir un evento.

La propia RFC propone detección de inactividad a los 5 minutos. 

Eso puede producir falsos positivos.

Yo usaría heartbeats de ejecución separados:

```text
event activity
+
process heartbeat
+
CPU/process state
+
lease heartbeat
```

---

# 16. La supervisión autónoma está demasiado prometida para la implementación actual

La RFC habla de:

- loop detection
- auto-abort
- WIP auto-commit
- process groups
- workspace jail
- environment scrubbing
- recuperación automática
- worktree cleanup



Pero en el código que he podido revisar, buena parte de esto pertenece todavía al **diseño objetivo**, no a un runtime completo ya implementado.

Yo separaría documentalmente:

```text
IMPLEMENTED
PARTIAL
DESIGN
ROADMAP
```

Esto es muy importante para la confianza de la comunidad.

---

# 17. La arquitectura que yo llevaría a 1.0

No añadiría Kubernetes.

No añadiría Kafka.

No añadiría Redis.

No añadiría NATS.

No añadiría gRPC.

Al menos todavía.

Mantendría:

```text
                   ┌─────────────────────┐
                   │   Gentle Mesh CLI    │
                   └──────────┬──────────┘
                              │
                         HTTP + SSE
                              │
                    ┌─────────▼─────────┐
                    │    Coordinator    │
                    │                   │
                    │ Admission         │
                    │ Territory         │
                    │ Task lifecycle    │
                    │ Registry          │
                    └───────┬───────────┘
                            │
                 ┌──────────┼──────────┐
                 │          │          │
              Worker A   Worker B   Worker C
                 │          │          │
              Runner     Runner     Runner
                 │          │          │
                Pi         Pi         Pi
```

Y dentro del coordinator:

```text
TaskManager
│
├── TaskState
├── EventStore
├── EventHub
├── QueryBroker
└── TerritoryManager
```

---

# 18. Territory Manager

Lo convertiría en una abstracción explícita:

```go
type TerritoryManager interface {
    Acquire(ctx context.Context, claim TerritoryClaim) (Lease, error)
    Renew(ctx context.Context, lease Lease) error
    Release(ctx context.Context, lease Lease) error
    Conflicts(claim TerritoryClaim) []Conflict
    Snapshot() TerritorySnapshot
}
```

Y:

```text
LocalTerritoryManager
```

para PoC.

Posteriormente:

```text
FederatedTerritoryManager
```

sin modificar `TaskManager`.

Eso es exactamente el tipo de desacoplamiento que te permitirá crecer sin destruir la arquitectura.

---

# 19. EventStore

Haría:

```go
type EventStore interface {
    Append(ctx context.Context, event Event) error
    Replay(ctx context.Context, taskID string, after int64) EventIterator
}
```

No:

```go
ReadEvents() []Event
```

El concepto de iterator/stream es mucho más apropiado.

---

# 20. EventHub

Y separaría:

```go
type EventHub interface {
    Subscribe(ctx context.Context, taskID string, after int64) (<-chan Event, error)
}
```

El Hub debería ser responsable de:

```text
live delivery
subscriber lifecycle
slow clients
lag detection
replay
backpressure
```

No `ManagedTask`.

---

# 21. El verdadero producto

Aquí viene mi valoración estratégica.

Creo que sería un error posicionar Gentle Mesh como:

> "otro sistema para ejecutar agentes remotamente".

Hay muchos sistemas que pueden hacer eso.

El núcleo diferencial es:

```text
Agent A
       \
        \
Agent B ----> Shared Territory
        /
Agent C
```

Es decir:

> **un protocolo de coordinación espacial/semántica para agentes que modifican software concurrentemente.**

Eso es bastante más interesante.

El "Radar" podría convertirse en algo muy potente:

```text
AUTH
 ├─ Agent A → apply → pkg/auth/*
 └─ Agent B → verify → tests/auth/*

DATABASE
 └─ Agent C → shared-schema → migrations/*

UI
 └─ Agent D → explore → web/*
```

y permitir al humano entender:

> "¿Quién está tocando qué parte del sistema ahora mismo?"

Ese sí es un problema emergente de los equipos que empiezan a usar múltiples agentes simultáneamente.

---

# 22. ¿Sobreingeniería?

## Ahora mismo: parcialmente.

Hay tres proyectos mezclados:

### Proyecto A

Remote execution:

```text
Pi → remote worker
```

### Proyecto B

Reliable event transport:

```text
REST + SSE + JSONL
```

### Proyecto C

Multi-agent territorial coordination:

```text
domain
surface
blast radius
radar
federation
```

Los tres son válidos.

El peligro es intentar terminar los tres simultáneamente.

---

# Yo dividiría las versiones

## v0.1 — Remote execution

```text
Task
Runner
REST
SSE
JSONL
Git
```

## v0.2 — Reliable transport

```text
Last-Event-ID
replay streaming
bounded memory
slow subscribers
durability modes
```

## v0.3 — Territory

```text
branch lock
edit surfaces
domain
blast radius
radar
```

## v0.4 — Federation

```text
peer coordinator
territory exchange
leases
epochs
partition handling
```

## v1.0

Solo cuando:

```text
real Pi runner
+
real worktrees
+
process isolation
+
chaos tests
+
federated consistency
+
security model
```

estén demostrados.

---

# 23. Mis 10 cambios prioritarios

Si yo entrara como arquitecto del proyecto mañana, haría esto en este orden:

### P0 — Lifecycle

Añadir:

```text
WaitGroup
runner ownership
graceful shutdown
process supervision
```

### P0 — SSE correctness

Eliminar:

```go
default:
```

como mecanismo silencioso de pérdida.

Usar:

```text
lag detection → disconnect → replay
```

### P0 — Replay O(1)

Cambiar:

```go
[]Event
```

por streaming incremental.

### P0 — Real execution isolation

Garantizar:

```text
process group
workspace boundary
environment allowlist
timeouts
```

### P0 — Security defaults

Producción:

```text
authentication required
body limits
rate limits
concurrency limits
timeouts
non-root
TLS/reverse proxy contract
```

### P1 — TerritoryManager

Separarlo de `ManagedTask`.

### P1 — EventStore

Separarlo del Task.

### P1 — Lease protocol

Para nodos y territorios.

### P1 — Chaos testing

Especialmente:

```text
partition
kill -9
slow client
disk full
restart
duplicate request
```

### P1 — Separar "thought" de "progress"

El protocolo debe transportar observabilidad, no depender del chain-of-thought.

---

# Mi conclusión final

Después de mirar el código real, **cambio mi impresión respecto a una revisión basada únicamente en la descripción**.

No me parece un proyecto vacío ni una arquitectura inventada sin fundamento.

Hay una base bastante limpia:

```text
Go stdlib
interfaces pequeñas
context
atomic
mutexes
HTTP
SSE
append-only
CLI
Docker
tests
```

y, sobre todo, hay una idea que considero técnicamente interesante:

> **Territory + Radar + deduplicación + live stream hooking.**

La parte que todavía no está madura es la que normalmente hace fracasar los sistemas distribuidos:

```text
failure semantics
durability semantics
distributed ownership
backpressure
process lifecycle
partition handling
security
```

Y hay **tres hallazgos que considero particularmente importantes**:

```text
1. ❌ El replay JSONL actual NO es O(1) en memoria.
2. ❌ El Pub/Sub SSE puede descartar eventos silenciosamente.
3. ❌ El lifecycle de runners no está acoplado al shutdown del TaskManager.
```

Esos tres los corregiría **antes de añadir más funcionalidad**. El código relevante está directamente en `Subscribe`/`ReadEvents`, `EmitEvent` y el lanzamiento de runners desde HTTP. 

Y hay un cuarto hallazgo arquitectónico:

```text
RFC de federación
        >
implementación actual
```

La RFC define una federación M2M bastante más ambiciosa que los endpoints actualmente expuestos. 

Eso no es un defecto de una PoC; **sí sería un problema si el README presenta esas capacidades como ya implementadas**.

## Mi valoración brutal

**Viabilidad:** alta como proyecto experimental y de infraestructura para agentes.

**Elegancia:** bastante alta en el núcleo Go; especialmente `Runner → EventSink → ManagedTask`.

**Madurez:** todavía media-baja para producción distribuida.

**Potencial:** alto si se centra en coordinación territorial y ejecución remota en lugar de intentar convertirse en un "Kubernetes para agentes".

Y la dirección que yo seguiría es muy concreta:

> **No hagas Gentle Mesh más grande todavía. Hazlo más correcto.**

Primero consigue que **un único coordinator + 2 workers + 10.000 eventos + desconexiones + reinicios + tareas concurrentes + cliente lento + `kill -9`** sea absolutamente determinista.

Después construye la federación.

Porque si el núcleo de transporte/lifecycle es perfecto, el **Territory Protocol** puede convertirse en la capa verdaderamente diferenciadora de Gentle AI.

---
