# Demo: Territorio en cola

Esta demo muestra cómo Gentle Mesh detecta dos tareas con superficies solapadas y deja la segunda en cola hasta que la primera libera el territorio. El coordinador se levanta en local sin TLS y se despachan dos tareas por la API REST.

**Requisitos:** Go 1.26.7+ en local (sin Docker).

---

## Paso 1 — Compilar y levantar el coordinador

```bash
go build -o gentle-mesh ./cmd/gentle-mesh
./gentle-mesh server -addr 127.0.0.1:8080 -workspace /tmp/gm-demo -territory-mode queue -runner pi &
COORD_PID=$!
sleep 8
```

Salida real (directorio limpio, 2026-10-06):

```
Gentle Mesh coordinator starting on 127.0.0.1:8080 (HTTP, tasks dir: /tmp/gm-demo/tasks, territory mode: queue, runner: pi)
```

Verificación:

```bash
curl -s http://127.0.0.1:8080/healthz
# {"status":"ok","tls":"disabled","uptime_seconds":8,"version":"v1"}
```

*Tiempo real: binario compilado ~0,5 s; primera ejecución con `go run` ~5-8 s por la compilación. No es un benchmark.*

---

## Paso 2 — Despachar la primera tarea

```bash
curl -s -X POST http://127.0.0.1:8080/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "agent": "coder",
    "task": "Refactorizar validacion JWT",
    "git_repo": "org/repo",
    "edit_surfaces": ["pkg/auth/jwt.go"],
    "prompt": "Refactorizar validacion JWT"
  }'
```

Salida real:

```
{"task_id":"task-1791313859840242033-fd095990","status":"running","events_url":"/v1/tasks/task-1791313859840242033-fd095990/events","created_at":1791313859}
```

*El campo `edit_surfaces` es el que detecta solapamientos. Sin él, la superficie no se registra y no hay detección de conflicto. El `git_repo` es obligatorio para que el territorio se rastree.*

---

## Paso 3 — Despachar segunda tarea con superficie solapada

Desde otro terminal, inmediatamente después:

```bash
curl -s -X POST http://127.0.0.1:8080/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "agent": "reviewer",
    "task": "Anadir logs al modulo auth",
    "git_repo": "org/repo",
    "edit_surfaces": ["pkg/auth/jwt.go", "cmd/server.go"],
    "prompt": "Anadir logs al modulo auth"
  }'
```

Salida real:

```
{"task_id":"task-1791313864670273911-bc57e064","status":"queued","events_url":"/v1/tasks/task-1791313864670273911-bc57e064/events","created_at":1791313864}
```

La segunda tarea devuelve `"status":"queued"` — el territorio `org/repo` con superficie `pkg/auth/jwt.go` ya está ocupado. El coordinador la arranca automáticamente cuando la primera termina.

---

## Paso 4 — Consultar el radar

```bash
curl -s http://127.0.0.1:8080/v1/mesh/radar
```

Salida real durante la cola (fragmento):

```
{
  "active_agents": [
    {"task_id":"task-1791313859840242033-fd095990","edit_surfaces":["pkg/auth/jwt.go"],"current_action":"El proyecto es un..."},
    {"task_id":"task-1791313864670273911-bc57e064","edit_surfaces":["pkg/auth/jwt.go","cmd/server.go"],"current_action":""}
  ]
}
```

La tarea 1 está activa (pi ejecutándose). La tarea 2 no tiene `current_action` — está en cola esperando.

---

## Paso 5 — Limpieza

```bash
kill $COORD_PID 2>/dev/null
```

---

## Tiempos observados (directorio limpio, 2026-10-06)

| Paso | Comando | Tiempo real |
|------|---------|-------------|
| Coordinador arranca | binario compilado (`go build` + `./gentle-mesh server`) | ~0,5 s |
| Coordinador arranca | `go run` primera vez | ~5-8 s |
| health check | `curl /healthz` | ~0,006 s |
| dispatch tarea | `curl -X POST /v1/tasks` | ~0,007 s |
| `go run ... nodes` | sobre coordinador Docker Compose | ~0,004 s |
| `go run ... radar` | respuesta local | ~0,005 s |
| Docker Compose up | 6 contenedores, imágenes ya descargadas | ~3 s |

*No son benchmarks; varían según la máquina y la carga del sistema.*
