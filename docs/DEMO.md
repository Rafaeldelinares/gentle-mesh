# Demo: Territorio en cola

Esta demo muestra como Gentle Mesh detecta dos tareas con superficies solapadas y deja la segunda en cola hasta que la primera libera el territorio. No se ejecuta ningun agente de IA real.

**Requisitos:** Docker, Go 1.26.7+ (solo para compilar si se usa el metodo local).

---

## Metodo principal: contenedor Docker

Compila la imagen y ejecuta en un contenedor desechable. El coordinador escucha en `0.0.0.0` dentro del contenedor; Docker publica el puerto solo en el loopback del host (`localhost:8080`). Se usa un token de demo porque `-runner pi` necesita autenticacion fuera de loopback.

```bash
# 1. Compilar la imagen Docker
docker build -t gentle-mesh:demo .

# 2. Levantar el coordinador en un contenedor efimero
#    - puerto publicado solo en loopback del host
#    - fake-pi montado como /usr/local/bin/pi (solo lectura)
#    - sin montar HOME ni el repositorio
docker run --rm \
  -p 127.0.0.1:8080:8080 \
  -v /home/rafael/proyectos/gentle-mesh/docs/demo/fake-pi:/usr/local/bin/pi:ro \
  --name gm-demo \
  gentle-mesh:demo server \
    -addr :8080 \
    -workspace /app \
    -territory-mode queue \
    -runner pi \
    -token demo123
```

Salida esperada (primeras lineas):

```
Gentle Mesh coordinator starting on :8080 (HTTP, tasks dir: /tmp/gentle-mesh/tasks, territory mode: queue, runner: pi)
```

Verificacion:

```bash
curl -s -H "Authorization: Bearer demo123" http://localhost:8080/healthz
{"status":"ok","tls":"disabled","uptime_seconds":7,"version":"v1"}
```

**Nota sobre el token:** el flag `-token demo123` es un valor de demo; la cabecera `Authorization: Bearer demo123` debe acompanar cada peticion. Esto es equivalente a un token de sesion real de Gentle Mesh.

---

## Paso 1 — Despachar la primera tarea

```bash
curl -s -H "Authorization: Bearer demo123" -X POST http://localhost:8080/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "agent": "worker",
    "task": "Refactorizar validacion JWT",
    "git_repo": "org/repo",
    "edit_surfaces": ["pkg/auth/jwt.go"],
    "prompt": "Refactorizar validacion JWT"
  }'
{"task_id":"task-1791358080272840626-f66c49ad","status":"running","events_url":"/v1/tasks/task-1791358080272840626-f66c49ad/events","created_at":1791358080}
```

*(El campo `edit_surfaces` es el que detecta solapamientos. Sin el, la superficie no se registra y no hay deteccion de conflicto. El campo `git_repo` es obligatorio para que el territorio se rastree.)*

---

## Paso 2 — Despachar segunda tarea con superficie solapada

Desde otro terminal, inmediatamente despues:

```bash
curl -s -H "Authorization: Bearer demo123" -X POST http://localhost:8080/v1/tasks \
  -H 'Content-Type: application/json' \
  -d '{
    "agent": "verify",
    "task": "Anadir logs al modulo auth",
    "git_repo": "org/repo",
    "edit_surfaces": ["pkg/auth/jwt.go", "cmd/server.go"],
    "prompt": "Anadir logs al modulo auth"
  }'
{"task_id":"task-1791358083775349725-801c632d","status":"queued","events_url":"/v1/tasks/task-1791358083775349725-801c632d/events","created_at":1791358083}
```

La segunda tarea devuelve `"status":"queued"`. El territorio `org/repo` con superficie `pkg/auth/jwt.go` ya esta ocupado por la tarea 1.

---

## Paso 3 — Consultar el radar

```bash
curl -s -H "Authorization: Bearer demo123" http://localhost:8080/v1/mesh/radar
{"cluster_name":"gentle-mesh","timestamp":1791358091,"active_agents":[
  {"task_id":"task-1791358083775349725-801c632d","repo":"org/repo","branch":"","edit_surfaces":["pkg/auth/jwt.go","cmd/server.go"],"agent":"verify","task_summary":"Anadir logs al modulo auth","node_id":"","started_at":1791358088,"blast_radius":"isolated-branch","last_activity_at":1791358088}
]}
```

La tarea 2 muestra `started_at: 1791358088` (en curso, auto-arranque tras liberar territorio). La tarea 1 no aparece porque ya termino.

---

## Paso 4 — Esperar y observar la cola

Despues de ~8 segundos (fake-pi duerme 8 segundos):

```bash
curl -s -H "Authorization: Bearer demo123" http://localhost:8080/v1/mesh/radar
{"cluster_name":"gentle-mesh","timestamp":1791358108,"active_agents":[]}
```

Ambas tareas han terminado. Estado final:

```bash
# Tarea 1: completada en 8 segundos
curl -s -H "Authorization: Bearer demo123" http://localhost:8080/v1/tasks/task-1791358080272840626-f66c49ad
{"task_id":"task-1791358080272840626-f66c49ad",...,"status":"completed","created_at":1791358080,"started_at":1791358080,"finished_at":1791358088,"completion":{"result":"Task completed","text":"Task completed"}}

# Tarea 2: auto-arranque a los 8s, completada a los 16s
curl -s -H "Authorization: Bearer demo123" http://localhost:8080/v1/tasks/task-1791358083775349725-801c632d
{"task_id":"task-1791358083775349725-801c632d",...,"status":"completed","created_at":1791358083,"started_at":1791358088,"finished_at":1791358096,"completion":{"result":"Task completed","text":"Task completed"}}
```

La tarea 2 comenzo automaticamente cuando la 1 libero el territorio (`started_at: 1791358088 = finished_at de la tarea 1`).

---

## Paso 5 — Limpieza

```bash
docker rm -f gm-demo 2>/dev/null
```

El contenedor es efimero (`--rm`); al pararlo se eliminan todos los datos del coordinador.

---

## Metodo alternativo: ejecucion local

Si Docker no esta disponible, compilar y ejecutar directamente en la maquina local. Requiere Go 1.26.7+ y que el fake-pi este en el PATH del sistema.

```bash
go build -o gentle-mesh ./cmd/gentle-mesh
cp docs/demo/fake-pi fake-pi
chmod +x fake-pi
PATH="$(pwd):$PATH" ./gentle-mesh server \
  -addr localhost:8080 \
  -workspace /tmp/gm-demo \
  -territory-mode queue \
  -runner pi &
COORD_PID=$!
sleep 8
```

Con este metodo no hace falta token ni cabecera de autorizacion (escucha en localhost).

---

## Tiempos observados (contenedor Docker, 2026-10-07)

| Paso | Comando | Tiempo real |
|------|---------|-------------|
| Docker compose up | 6 contenedores, imagenes en cache | ~3 s |
| Imagen Docker build | primera vez | ~30 s |
| health check | `curl /healthz` con Authorization | ~0,005 s |
| dispatch tarea | `curl -X POST /v1/tasks` | ~0,007 s |
| fake-pi sleep | duracion del runner simulado | 8,0 s |
| auto-arranque cola | cuando se libera el territorio | ~0,5 s |

*(No son benchmarks; varian segun la maquina y la carga del sistema.)*
