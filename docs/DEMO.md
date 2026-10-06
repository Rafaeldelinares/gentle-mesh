# Demo: Territorio en cola

Esta demo muestra como Gentle Mesh detecta dos tareas con superficies solapadas y deja la segunda en cola hasta que la primera libera el territorio. No se ejecuta ningun agente de IA real.

**Requisitos:** Go 1.26.7+ en local (sin Docker). Fake-pi en `docs/demo/fake-pi`.

---

## Paso 1 — Compilar y levantar el coordinador

```bash
go build -o gentle-mesh ./cmd/gentle-mesh
cp docs/demo/fake-pi fake-pi
chmod +x fake-pi
PATH="$(pwd):$PATH" ./gentle-mesh server \
  -addr 127.0.0.1:8080 \
  -workspace /tmp/gm-demo \
  -territory-mode queue \
  -runner pi &
COORD_PID=$!
sleep 8
```

Salida real (directorio limpio, 2026-10-07, binario compilado):

```
Gentle Mesh coordinator starting on 127.0.0.1:8080 (HTTP, tasks dir: /tmp/gm-demo/tasks, territory mode: queue, runner: pi)
```

Verificacion:

```bash
curl -s http://127.0.0.1:8080/healthz
{"status":"ok","tls":"disabled","uptime_seconds":8,"version":"v1"}
```

*(Binario compilado: ~0,5 s. Con `go run`: ~5-8 s la primera vez por la compilacion.)*

---

## Paso 2 — Despachar la primera tarea

```bash
curl -s -X POST http://127.0.0.1:8080/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "agent": "worker",
    "task": "Refactorizar validacion JWT",
    "git_repo": "org/repo",
    "edit_surfaces": ["pkg/auth/jwt.go"],
    "prompt": "Refactorizar validacion JWT"
  }'
{"task_id":"task-1791326511373523502-af3488dc","status":"running","events_url":"/v1/tasks/task-1791326511373523502-af3488dc/events","created_at":1791326511}
```

*(El campo `edit_surfaces` es el que detecta solapamientos. Sin el, la superficie no se registra y no hay deteccion de conflicto. El campo `git_repo` es obligatorio para que el territorio se rastree.)*

---

## Paso 3 — Despachar segunda tarea con superficie solapada

Desde otro terminal, inmediatamente despues:

```bash
curl -s -X POST http://127.0.0.1:8080/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "agent": "verify",
    "task": "Anadir logs al modulo auth",
    "git_repo": "org/repo",
    "edit_surfaces": ["pkg/auth/jwt.go", "cmd/server.go"],
    "prompt": "Anadir logs al modulo auth"
  }'
{"task_id":"task-1791326515015032188-575d2e08","status":"queued","events_url":"/v1/tasks/task-1791326515015032188-575d2e08/events","created_at":1791326515}
```

La segunda tarea devuelve `"status":"queued"`. El territorio `org/repo` con superficie `pkg/auth/jwt.go` ya esta ocupado.

---

## Paso 4 — Consultar el radar

```bash
curl -s http://127.0.0.1:8080/v1/mesh/radar
{"cluster_name":"gentle-mesh","timestamp":1791326518,"active_agents":[
  {"task_id":"task-1791326515015032188-575d2e08","repo":"org/repo","branch":"","edit_surfaces":["pkg/auth/jwt.go","cmd/server.go"],"agent":"verify","task_summary":"Anadir logs al modulo auth","node_id":"","started_at":0,"blast_radius":"isolated-branch","last_activity_at":1791326515},
  {"task_id":"task-1791326511373523502-af3488dc","repo":"org/repo","branch":"","edit_surfaces":["pkg/auth/jwt.go"],"agent":"worker","task_summary":"Refactorizar validacion JWT","node_id":"","started_at":1791326511,"blast_radius":"isolated-branch","last_activity_at":1791326511}
]}
```

La tarea 1 muestra `started_at: 1791326511` (en curso). La tarea 2 muestra `started_at: 0` (en cola, sin accion en curso).

---

## Paso 5 — Esperar y observar el auto-arranque

Despues de ~8 segundos (fake-pi duerme 8 segundos):

```bash
curl -s http://127.0.0.1:8080/v1/mesh/radar
{"cluster_name":"gentle-mesh","timestamp":1791326524,"active_agents":[
  {"task_id":"task-1791326515015032188-575d2e08","repo":"org/repo","branch":"","edit_surfaces":["pkg/auth/jwt.go","cmd/server.go"],"agent":"verify","task_summary":"Anadir logs al modulo auth","node_id":"","started_at":1791326519,"blast_radius":"isolated-branch","last_activity_at":1791326519}
]}
```

La tarea 1 ha terminado. La tarea 2 se ha arrancado sola a las `1791326519` (`started_at` ahora tiene valor, no 0).

Estado final de ambas tareas:

```bash
# Tarea 1
curl -s http://127.0.0.1:8080/v1/tasks/task-1791326511373523502-af3488dc
{"task_id":"task-1791326511373523502-af3488dc",...,"status":"completed","created_at":1791326511,"started_at":1791326511,"finished_at":1791326519,"completion":{"result":"Task completed","text":"Task completed"}}

# Tarea 2
curl -s http://127.0.0.1:8080/v1/tasks/task-1791326515015032188-575d2e08
{"task_id":"task-1791326515015032188-575d2e08",...,"status":"completed","created_at":1791326515,"started_at":1791326519,"finished_at":1791326527,"completion":{"result":"Task completed","text":"Task completed"}}
```

La tarea 2 comenzo automaticamente cuando la 1 libero el territorio (1791326519 = 1791326511 + 8s).

---

## Paso 6 — Limpieza

```bash
kill $COORD_PID 2>/dev/null
```

---

## Tiempos observados (directorio limpio, 2026-10-07)

| Paso | Comando | Tiempo real |
|------|---------|-------------|
| Coordinador arranca | binario compilado (`go build` + `./gentle-mesh server`) | ~0,5 s |
| health check | `curl /healthz` | ~0,006 s |
| dispatch tarea | `curl -X POST /v1/tasks` | ~0,007 s |
| `gentle-mesh nodes` | sobre coordinador Docker Compose | ~0,007 s |
| `gentle-mesh radar` | respuesta local | ~0,006 s |
| Docker Compose up | 6 contenedores, imagenes ya descargadas | ~3 s |

*(No son benchmarks; varian segun la maquina y la carga del sistema.)*
