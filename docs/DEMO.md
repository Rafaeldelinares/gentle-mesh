# Demo: Territorio en cola

Esta demo muestra como Gentle Mesh detecta dos tareas con superficies solapadas y deja la segunda en cola hasta que la primera libera el territorio. No se ejecuta ningun agente de IA real.

**Requisitos:** Docker, Go 1.26.9+ (solo para compilar si se usa el metodo local).

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
  -v "$(pwd)/docs/demo/fake-pi:/usr/local/bin/pi:ro" \
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

**Nota sobre el token:** el flag `-token demo123` es un valor de demo; la cabecera `Authorization: Bearer demo123` debe acompanar cada peticion.

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
{"task_id":"task-1791360731164526224-3de60020","status":"running","events_url":"/v1/tasks/task-1791360731164526224-3de60020/events","created_at":1791360731}
```

*(El campo `edit_surfaces` es el que detecta solapamientos. Sin el, la superficie no se registra y no hay deteccion de conflicto. El campo `git_repo` es obligatorio para que el territorio se rastree.)*

---

## Paso 2 — Despachar segunda tarea con superficie solapada

*(Los comandos `curl` de este paso y los siguientes se ejecutan desde otro terminal del host, no desde dentro del contenedor.)* Inmediatamente despues:

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
{"task_id":"task-1791360735578759070-172c11f1","status":"queued","events_url":"/v1/tasks/task-1791360735578759070-172c11f1/events","created_at":1791360735}
```

La segunda tarea devuelve `"status":"queued"`. El territorio `org/repo` con superficie `pkg/auth/jwt.go` ya esta ocupado por la tarea 1.

---

## Paso 3 — Consultar el radar a los 2 segundos

Se consulta el radar 2 segundos despues de despachar la tarea 2. La tarea 1 esta en ejecucion desde su creacion (`started_at: 1791360731`); la tarea 2 esta en cola (`started_at: 0`).

```bash
sleep 2 && curl -s -H "Authorization: Bearer demo123" http://localhost:8080/v1/mesh/radar
{"cluster_name":"gentle-mesh","timestamp":1791360737,"active_agents":[
  {"task_id":"task-1791360735578759070-172c11f1","repo":"org/repo","branch":"","edit_surfaces":["pkg/auth/jwt.go","cmd/server.go"],"agent":"verify","task_summary":"Anadir logs al modulo auth","node_id":"","started_at":0,"blast_radius":"isolated-branch","last_activity_at":1791360735},
  {"task_id":"task-1791360731164526224-3de60020","repo":"org/repo","branch":"","edit_surfaces":["pkg/auth/jwt.go"],"agent":"worker","task_summary":"Refactorizar validacion JWT","node_id":"","started_at":1791360731,"blast_radius":"isolated-branch","last_activity_at":1791360731}
]}
```

*(El radar muestra las dos tareas: la 1 en ejecucion, la 2 en cola.)*

---

## Paso 4 — Esperar a que terminen las dos tareas

Espera unos 16 segundos y consulta el radar desde otro terminal:

```bash
sleep 16 && curl -s -H "Authorization: Bearer demo123" http://localhost:8080/v1/mesh/radar
{"cluster_name":"gentle-mesh","timestamp":1791360751,"active_agents":[]}
```

Estado final:

```bash
# Tarea 1: completada a los 8 segundos
curl -s -H "Authorization: Bearer demo123" http://localhost:8080/v1/tasks/task-1791360731164526224-3de60020
{"task_id":"task-1791360731164526224-3de60020","status":"completed","created_at":1791360731,"started_at":1791360731,"finished_at":1791360739,"completion":{"result":"Task completed","text":"Task completed"}}

# Tarea 2: auto-arranque a los 8s, completada a los 16s
curl -s -H "Authorization: Bearer demo123" http://localhost:8080/v1/tasks/task-1791360735578759070-172c11f1
{"task_id":"task-1791360735578759070-172c11f1","status":"completed","created_at":1791360735,"started_at":1791360739,"finished_at":1791360747,"completion":{"result":"Task completed","text":"Task completed"}}
```

La tarea 2 comenzo automaticamente cuando la 1 libero el territorio (`started_at: 1791360739 = finished_at de la tarea 1`).

---

## Verificacion de integridad del fake-pi

El fake-pi que ejecuta el runner simulado dentro del contenedor es identico al del repositorio. Se verifica con sha256sum:

```
$ docker cp gm-demo:/usr/local/bin/pi /tmp/fake-pi.extracted
$ sha256sum docs/demo/fake-pi /tmp/fake-pi.extracted
4513f28959e37d1e73f044cddb209812fc5bf09b0a70102ec6411d3cb1fca97d  docs/demo/fake-pi
4513f28959e37d1e73f044cddb209812fc5bf09b0a70102ec6411d3cb1fca97d  /tmp/fake-pi.extracted
```

*(Los hashes coinciden: el contenedor ejecuta exactamente el fake-pi del repositorio.)*

---


## Paso 5 — Limpieza

```bash
docker rm -f gm-demo 2>/dev/null
```

El contenedor es efimero (`--rm`); al pararlo se eliminan todos los datos del coordinador.

---

## Metodo alternativo: ejecucion local

Si Docker no esta disponible, compilar y ejecutar directamente en un directorio temporal, sin tocar el repositorio. Requiere Go 1.26.9+. Ejecutar todos los comandos desde la raiz del repositorio.

```bash
WORK=$(mktemp -d)
go build -o "$WORK/gentle-mesh" ./cmd/gentle-mesh
cp docs/demo/fake-pi "$WORK/pi"
chmod +x "$WORK/pi"

# Comprobacion: $WORK/pi debe ser exactamente el que resuelve con PATH prefijado.
# El servidor solo arranca si la comprobacion pasa.
RESOLVED=$(PATH="$WORK:$PATH" command -v pi)
if [ "$RESOLVED" = "$WORK/pi" ]; then
  PATH="$WORK:$PATH" "$WORK/gentle-mesh" server -addr localhost:8080 -workspace "$WORK/ws" -territory-mode queue -runner pi &
  COORD_PID=$!
  echo "coordinador PID: $COORD_PID"
  echo "endpoint: http://localhost:8080"
else
  echo "ERROR: pi no resuelve a $WORK/pi (resolvio a $RESOLVED)"
fi

# Despachar tareas desde otro terminal con los mismos comandos curl
# que en la seccion de contenedor, pero sin la cabecera Authorization

# Al terminar: kill $COORD_PID; rm -rf "$WORK"
```

Con este metodo no hace falta token ni cabecera de autorizacion (escucha en localhost). El repositorio queda intacto: ni `./pi` ni `./gentle-mesh` se crean dentro del repo. La variable PATH no se modifica fuera del comando que la establece (prefijo de cada linea).

---

## Tiempos observados (contenedor Docker, 2026-10-07)

| Evento | Tiempo real |
|--------|-------------|
| fake-pi sleep | 8,0 s (dormir 8 segundos) |

*(Los demas tiempos del flujo (health check, dispatch, auto-arranque) varian segun la maquina y la carga del sistema y no se midieron en este entorno.)*
