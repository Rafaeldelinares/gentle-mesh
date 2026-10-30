# Guía de Puesta en Marcha en Producción (Getting Started)

Esta guía explica **cómo desplegar y operar Gentle Mesh en un entorno real**: un coordinador seguro con mTLS y almacenamiento inmutable, y workers remotos ejecutando agentes de IA aislados dentro de Bubblewrap (`bwrap`) sobre repositorios de código reales.

> ℹ️ **¿Buscas una prueba rápida sin configurar agentes ni certificados?**  
> Si solo quieres ver el semáforo territorial en acción en 2 minutos con un contenedor local, consulta [docs/DEMO.md](DEMO.md).

---

## 1. Escenario de Uso y Fronteras

Gentle Mesh está diseñado hoy para:
> **Un desarrollador individual o un equipo técnico de confianza, operando dentro de su propia red local (LAN) o VPN privada (Tailscale, WireGuard), ejecutando agentes autónomos sobre repositorios Git compartidos.**

### Lo que garantiza de verdad
- **Cero colisiones territoriales:** dos agentes no pueden modificar la misma rama ni las mismas superficies de archivos al mismo tiempo.
- **Aislamiento de ejecución:** los agentes corren en worktrees efímeros dentro de sandboxes Bubblewrap (`bwrap`), sin acceso a la red del host ni a credenciales no autorizadas.
- **Conocimiento situado inmutable (RFC-003):** estado proyectado deterministamente desde un log encadenado por SHA-256 (`JoinLog`), con snapshots verificables y stream de eventos en vivo.
- **Liquidación determinista (RFC-002):** cuando el agente termina, el arnés verifica que dejó un commit en una rama de propuesta (`proposal/<task_id>`) y emite un recibo firmado criptográficamente (`SettlementReceipt`).
- **Adaptador A2A estándar:** descubrimiento de agentes vía `GET /.well-known/agent-card.json` con sobre JWS compacto sobre cuerpo canónico JCS con firma Ed25519.

### Lo que queda fuera hoy
- Exposición directa a internet pública sin VPN/LAN ([#156](https://github.com/Rafaeldelinares/gentle-mesh/issues/156)).
- Múltiples workers concurrentes en la misma máquina física compitiendo intensivamente ([#147](https://github.com/Rafaeldelinares/gentle-mesh/issues/147)).
- Notificaciones salientes por Webhooks ([#150](https://github.com/Rafaeldelinares/gentle-mesh/issues/150)).

---

## 2. Requisitos Previos e Instalación

### Requisitos del Sistema
- **Coordinador:** Linux o macOS, Go 1.26.9+, SQLite3.
- **Worker (Ejecutor):** Linux (kernel con soporte de namespaces de usuario para `bwrap`), `git`, `bubblewrap` (`bwrap`), y el arnés de agente ([Gentle Shell / Pi](https://github.com/gentleman-programming/gentle-ai)).

En Debian/Ubuntu:
```bash
sudo apt-get update && sudo apt-get install -y git bubblewrap
```

### Compilación Reproducible
En ambas máquinas, compila el binario estático sin dependencias (`CGO_ENABLED=0`):

```bash
git clone https://github.com/Rafaeldelinares/gentle-mesh.git
cd gentle-mesh
CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o gentle-mesh ./cmd/gentle-mesh
sudo mv gentle-mesh /usr/local/bin/
```

Verifica la instalación:
```bash
gentle-mesh help
```

---

## 3. Puesta en Marcha del Coordinador (Host Central)

El coordinador central gestiona la autoridad de certificados (CA de la malla), los semáforos territoriales, el log de eventos encadenado y la recepción de tareas.

Supongamos que el coordinador tiene la IP privada `192.168.1.50`.

### Paso 3.1: Directorios y Token de Administración
Crea un directorio de estado persistente y define el token de administración (usando variables de entorno para que nunca viaje en `argv`):

```bash
sudo mkdir -p /var/lib/gentle-mesh/tasks /var/lib/gentle-mesh/tls
sudo chown -R $(whoami):$(whoami) /var/lib/gentle-mesh

# Genera un token fuerte de administración
export GENTLE_MESH_TOKEN=$(openssl rand -hex 32)
echo "Guarda tu token de administración: $GENTLE_MESH_TOKEN"
```

### Paso 3.2: Iniciar el Coordinador con mTLS y JoinLog
Inicia el coordinador forzando mTLS y persistencia SQLite en modo WAL:

```bash
gentle-mesh server \
  -addr 0.0.0.0:8443 \
  -tls -tls-init -require-mtls \
  -tls-san "192.168.1.50,coordinator.mesh.local,localhost" \
  -tasks-dir /var/lib/gentle-mesh/tasks \
  -tls-dir /var/lib/gentle-mesh/tls \
  -db-path /var/lib/gentle-mesh/tasks/coordinator.db \
  -joinlog-path /var/lib/gentle-mesh/tasks/join.db \
  -territory-mode queue
```

El servidor inicializará la CA de la malla en `/var/lib/gentle-mesh/tls/ca.pem` y comenzará a escuchar en el puerto `8443`.

---

## 4. Enrolamiento y Puesta en Marcha de un Worker

El worker es la máquina donde corren los agentes de IA (ej. `w-dev-2` en IP `192.168.1.26`).

### Paso 4.1: Generar Token de Enrolamiento en el Coordinador
En el host del coordinador, emite un token de enrolamiento de un solo uso para el nuevo nodo:

```bash
gentle-mesh gen-token \
  -node-id w-dev-2 \
  -ttl 1h \
  -db-path /var/lib/gentle-mesh/tasks/coordinator.db
```
Salida esperada:
```
Token: enroll-w-dev-2-xxxxxxxxxxxxxxxx
NodeID: w-dev-2
Expires: 2026-10-30 21:00:00
```

### Paso 4.2: Descargar la CA de la Malla en el Worker
En la máquina del worker, descarga el certificado público de la CA de la malla (este endpoint es público y seguro):

```bash
sudo mkdir -p /etc/gentle-mesh/tls
curl -k https://192.168.1.50:8443/v1/mesh/ca -o /etc/gentle-mesh/tls/ca.pem
```

*(Opcional: verifica la huella SHA-256 de la CA si quieres asegurar pinning estricto)*.

### Paso 4.3: Preparar el Caché de Repositorios Git (`RepoCache`)
El runner seguro `gentle-shell` exige que el worker mantenga un clon **bare** (sin working copy ni remotos activos) del repositorio donde trabajarán los agentes:

```bash
sudo mkdir -p /var/cache/gentle-mesh/repos /var/tmp/gentle-mesh/worktrees
sudo chown -R $(whoami):$(whoami) /var/cache/gentle-mesh /var/tmp/gentle-mesh

# Clona el repositorio en modo bare
git clone --bare https://github.com/mi-organizacion/mi-proyecto.git /var/cache/gentle-mesh/repos/mi-proyecto.git
```

### Paso 4.4: Iniciar el Worker con Runner `gentle-shell` y Enrolamiento Automático
Lanza el worker pasando el token de enrolamiento. El nodo generará un CSR localmente, lo enviará al coordinador, recibirá su certificado mTLS y arrancará su runner confinado:

```bash
gentle-mesh worker \
  -coordinator https://192.168.1.50:8443 \
  -node-id w-dev-2 \
  -endpoint https://192.168.1.26:8081 \
  -ca /etc/gentle-mesh/tls/ca.pem \
  -join-token "enroll-w-dev-2-xxxxxxxxxxxxxxxx" \
  -runner gentle-shell \
  -gentle-shell-bin gentle-shell \
  -bwrap-bin /usr/bin/bwrap \
  -gentle-shell-repo-cache /var/cache/gentle-mesh/repos/mi-proyecto.git \
  -gentle-shell-worktrees /var/tmp/gentle-mesh/worktrees \
  -agents worker \
  -tags "go,linux,fast" \
  -concurrency 1
```

En la salida verás:
```
[gentle-mesh] CSR generated, submitting enrollment request to coordinator...
[gentle-mesh] Enrollment successful. Certificate issued for node w-dev-2.
[gentle-mesh] Node w-dev-2 registered successfully. Heartbeat active.
```

---

## 5. Despacho y Ciclo de Vida de una Tarea Real

Una vez conectado el worker, puedes despachar tareas desde tu laptop o CLI.

### Paso 5.1: Enviar una Tarea con Territorio Declarado
Despacha una orden de desarrollo indicando el repositorio, la rama base y las superficies protegidas:

```bash
gentle-mesh run \
  -coordinator https://192.168.1.50:8443 \
  -ca /etc/gentle-mesh/tls/ca.pem \
  -token "$GENTLE_MESH_TOKEN" \
  -agent worker \
  -tags "go" \
  -repo "https://github.com/mi-organizacion/mi-proyecto.git" \
  -branch "main" \
  -surfaces "pkg/auth,pkg/tokens" \
  -task "Implementar rotación determinista de tokens en pkg/auth/tokens.go con tests TDD"
```

### Paso 5.2: Qué Ocurre en la Malla (El Ciclo Completo)
1. **Reserva Territorial (J5):** el coordinador verifica que ninguna otra tarea tenga reservada la rama `main` o las superficies `pkg/auth`. Si hay conflicto, la encola (`queue`) hasta que quede libre.
2. **Despacho y Admisión (RFC-003):** la tarea se despacha al worker `w-dev-2`. El worker arranca una transacción de lease con reaper autónomo (RFC-002 R2).
3. **Aislamiento en `bwrap`:** el worker crea un worktree efímero en `/var/tmp/gentle-mesh/worktrees/task-<id>` a partir de `/var/cache/gentle-mesh/repos/mi-proyecto.git` en una rama nueva `proposal/<task_id>`. El agente corre dentro del sandbox sin acceso a internet del host ni al filesystem raíz.
4. **Streaming en Vivo:** la salida del agente, pensamientos y llamadas a herramientas fluyen por SSE al CLI en tiempo real.
5. **Verificación Territorial a Posteriori:** cuando el agente termina y commitea su trabajo, el arnés de Go fuera de `bwrap` corre `git diff --name-only main..HEAD` y valida que solo tocó archivos dentro de `pkg/auth`. Emite el evento `territory.verified` en el log de la malla.
6. **Liquidación y Recibo:** el arnés valida las aserciones, emite el recibo inmutable `SettlementReceipt` (RFC-002) y libera el territorio.

### Paso 5.3: Inspeccionar y Aplicar la Propuesta (`apply`)
En tu máquina de desarrollo, puedes revisar el commit producido por el worker sin hacer pull a ciegas ni ensuciar tu rama de trabajo:

```bash
# Materializa la propuesta en una rama local de revisión
gentle-mesh apply \
  -task "<task_id>" \
  -cache /var/cache/gentle-mesh/repos/mi-proyecto.git \
  -repo /home/usuario/proyectos/mi-proyecto \
  -base main
```
`gentle-mesh apply` creará la rama local `proposal-merge/<task_id>`. Puedes hacer `git log -p` o `git diff main` para auditar el código antes de mergearlo a `main`.

---

## 6. Descubrimiento A2A (Agent Card)

Cualquier orquestador externo compatible con el estándar A2A puede descubrir las capacidades de la malla sin conocer su protocolo interno:

```bash
curl -k https://192.168.1.50:8443/.well-known/agent-card.json
```
Respuesta:
```json
{
  "protected": { "alg": "EdDSA", "typ": "JOSE", "kid": "coordinator" },
  "payload": "<base64url(JCS(card))>",
  "signature": "<base64url(Ed25519_signature)>"
}
```
El payload contiene la Agent Card canónica con las capacidades de la malla (`streaming: true`, `state_transition_history: true`) y el bloque `mesh` con el `state_digest` criptográfico del `JoinLog`.

Para consultar la card de un worker específico a través del gateway:
```bash
curl -k "https://192.168.1.50:8443/.well-known/agent-card.json?node_id=w-dev-2"
```

---

## 7. Diagnóstico y Resolución de Problemas (Troubleshooting)

### 1. Estado de Salud del Coordinador
```bash
curl -k https://192.168.1.50:8443/healthz
# {"status":"ok","tls":"enabled","uptime_seconds":3600,"version":"v1"}
```

### 2. Inspeccionar Nodos y Radar de Territorios
Usa el CLI con tu token de administración para ver qué nodos están vivos y qué tareas están ejecutándose:
```bash
gentle-mesh nodes \
  -coordinator https://192.168.1.50:8443 \
  -ca /etc/gentle-mesh/tls/ca.pem \
  -token "$GENTLE_MESH_TOKEN"

gentle-mesh radar \
  -coordinator https://192.168.1.50:8443 \
  -ca /etc/gentle-mesh/tls/ca.pem \
  -token "$GENTLE_MESH_TOKEN"
```

### 3. Caída de un Worker (`NODE_LOST`)
Si un worker pierde conectividad o su máquina se apaga:
- El heartbeat del coordinador detecta la inactividad tras el timeout configurado (`-heartbeat-timeout`, por defecto 30s).
- El coordinador marca al nodo como `offline` y finaliza las tareas en vuelo con error `NODE_LOST`.
- Las ramas territoriales retenidas se liberan automáticamente en el log inmutable (`reason: "lease_expired"`), evitando bloqueos huérfanos.

### 4. Revocación Inmediata de un Certificado Comprometido
Si un worker es comprometido, puedes revocar su acceso en caliente. La revocación desaloja al nodo del registro y cancela todas sus tareas en ejecución en una sola operación atómica:

```bash
gentle-mesh cert-revoke \
  -node-id w-dev-2 \
  -db-path /var/lib/gentle-mesh/tasks/coordinator.db
```
Cualquier intento posterior del worker de enviar heartbeats o solicitar tareas será rechazado con `403 Forbidden: certificate revoked`.
