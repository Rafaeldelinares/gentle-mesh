# Demo: Territorio en cola — 60 segundos

Esta demo muestra cómo Gentle Mesh detecta dos tareas con superficies solapadas y deja la segunda en cola hasta que la primera libera el territorio.

**Requisitos:** Go 1.26.7+ en local (sin Docker).

**Alcance:** El runner local termina en milisegundos; para observar la cola con tareas que duran más de un segundo se necesita un script de prueba que mantenga una tarea viva. Ese script se propone al final y queda fuera de este documento a la espera de visto bueno antes de incluirlo.

---

## Paso 1 — Lanzar el coordinador

```bash
cd /tmp/gm-demo && go run github.com/gentleman-programming/gentle-mesh/cmd/gentle-mesh server -addr :8080 -workspace . -territory-mode queue &
COORD_PID=$!
sleep 5
```

Salida esperada:

```
Gentle Mesh coordinator starting on :8080 (HTTP, tasks dir: /tmp/gm-demo/tasks, territory mode: queue, runner: mesh)
2026/10/06 18:15:00 [gentle-mesh] WARNING: server running on non-loopback address :8080 without authentication
```

Verificación:

```bash
curl -s http://localhost:8080/healthz
# {"status":"ok","tls":"disabled","uptime_seconds":4,"version":"v1"}
```

---

## Paso 2 — Despachar la primera tarea (Terminal A)

```bash
go run github.com/gentleman-programming/gentle-mesh/cmd/gentle-mesh run \
  -coordinator http://localhost:8080 \
  -task "Refactorizar validación JWT" \
  -domain "auth" \
  -surfaces "pkg/auth/jwt.go"
```

Salida esperada (resumen):

```
Task ID: task-xxx (status: running)
[status] running
[completion] Task completed successfully
```

---

## Paso 3 — Despachar segunda tarea con superficie solapada (Terminal B)

Desde otro terminal, con la tarea anterior aún en curso o inmediatamente después:

```bash
go run github.com/gentleman-programming/gentle-mesh/cmd/gentle-mesh run \
  -coordinator http://localhost:8080 \
  -task "Añadir logs al módulo auth" \
  -domain "auth" \
  -surfaces "pkg/auth/jwt.go,cmd/server.go"
```

Salida esperada con territorio en cola:

```
Task ID: task-yyy (status: running)
[status] queued
[status] running
[completion] Task completed successfully
```

La segunda tarea muestra `queued` mientras espera que el territorio `auth` con superficie `pkg/auth/jwt.go` quede libre. El coordinador la arranca automáticamente cuando la primera termina.

---

## Paso 4 — Consultar el radar

```bash
go run github.com/gentleman-programming/gentle-mesh/cmd/gentle-mesh radar -coordinator http://localhost:8080
```

Salida esperada (durante la ejecución):

```
AGENT ID           DOMAIN   SURFACE                 STATUS   STARTED
task-xxx           auth     pkg/auth/jwt.go         running  2s ago
task-yyy           auth     pkg/auth/jwt.go,cmd...  queued   -
```

---

## Paso 5 — Limpieza

```bash
kill $COORD_PID 2>/dev/null
```

---

## Tiempos observados (hardware de referencia, 2026-10-06)

| Paso | Comando | Tiempo real |
|------|---------|-------------|
| Coordinador arranca | `go run ... server` | ~7 s |
| health check | `curl /healthz` | ~0,07 s |
| `go run ... nodes` | sobre coordinador Docker | ~0,09 s |
| `go run ... radar` | respuesta local | ~0,08 s |
| `go run ... run` | dispatch local | ~0,15 s |
| Docker compose up | 6 servicios | ~4 s |

---

## Script de prueba para observar la cola en vivo *(propuesto, fuera de este documento)*

El runner local (`mesh`) termina en milisegundos, por lo que la cola no es observable con tareas normales. Se propone un script de <=60 líneas que lance un proceso persistente por tarea para que el territorio quede ocupado durante varios segundos. El script queda fuera de este documento a la espera de visto bueno antes de integrarlo.
