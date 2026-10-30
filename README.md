# Gentle Mesh 🌐

[![CI](https://github.com/Rafaeldelinares/gentle-mesh/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/Rafaeldelinares/gentle-mesh/actions/workflows/ci.yml)

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

## En pocas palabras

**Gentle Mesh es una torre de control para agentes de IA que trabajan sobre el mismo proyecto.** Cada tarea avisa de qué parte del código va a tocar. Gentle Mesh lo anota en un radar que todos pueden mirar (`gentle-mesh radar`) y detecta dos choques: **misma rama** con otra tarea en curso, y **superficies declaradas que se solapan**, aunque las ramas sean distintas. El modo elegido (`-territory-mode`) decide qué hacer con el solapamiento de superficies: `queue` (por defecto) deja la tarea en cola y la arranca sola al liberarse el territorio; `warn` deja constancia en un evento `thought` y la deja pasar; `strict` la rechaza con `409`; `disabled` no comprueba ese solapamiento. El **bloqueo de rama** se aplica en los cuatro modos: en `queue` la tarea queda en cola, y en `warn`, `strict` y `disabled` se rechaza con `409`. Solo se detecta lo que cada tarea declara: no se lee el código. Además, permite mandar el trabajo pesado (compilar, pasar tests) a otras máquinas, y lo reparte entre las compatibles (por rol y etiquetas) según su carga.

**Un ejemplo.** Ana y Luis lanzan cada uno un agente sobre el mismo repositorio, y los dos quieren modificar el módulo de login.

- **Sin Gentle Mesh:** nadie sabe qué está haciendo el otro agente. Los dos escriben sobre lo mismo y el conflicto aparece horas después, al juntar los cambios.
- **Con Gentle Mesh:** la tarea de Ana se registra primero. Cuando llega la de Luis, Gentle Mesh ve que tocan lo mismo y, en el modo por defecto, la deja en cola (`status: "queued"`). Arranca sola cuando la de Ana termina. Y cualquiera puede mirar el radar para ver quién está tocando qué.

**Qué no es**

- No es un agente ni un modelo de IA: no escribe código ni decide qué hay que hacer. Eso lo indica quien manda la tarea.
- No lee tu código: se fía de lo que cada tarea declara que va a tocar.
- No sustituye a Git ni a la revisión humana.
- No es un producto listo para producción ni un proyecto oficial de Gentle AI: es una propuesta y prueba de concepto (alfa) de la comunidad.
- No sustituye a las sesiones remotas de pi: pi te conecta a un agente en otra máquina; Gentle Mesh coordina a varios.
- La parte de recibos firmados (RFC-002) es experimental y no está en el binario.

**RFC-002.** El protocolo de liquidación entre agentes con recibos Ed25519 firmados y cadena SHA-256 está implementado y probado en `main`, cumpliendo el 100% (12/12) de los requisitos del perfil mínimo conforme respaldado por tests automatizados y cerrado bajo el issue [#70](https://github.com/Rafaeldelinares/gentle-mesh/issues/70). Ver la matriz completa en [`docs/rfcs/002-objectives-and-non-goals.md`](docs/rfcs/002-objectives-and-non-goals.md).

---

## ⚠️ Nota de Gobernanza y Comunidad

> **Este repositorio es una propuesta de arquitectura técnica (RFC) y Prueba de Concepto (PoC) comunitaria creada para el ecosistema Gentle AI.**  
>
> **Este proyecto avanzará, evolucionará y se integrará de forma oficial única y exclusivamente bajo la revisión, orientación y aprobación explícita de [Alan Buscaglia (@gentleman-programming)](https://github.com/gentleman-programming), creador y líder del ecosistema Gentle AI.**  
>
> Hasta contar con su feedback y visto bueno, este repositorio permanece como un espacio de investigación abierta, validación técnica, prototipado riguroso y experimentación colaborativa por y para la comunidad.

---

## Estado y hoja de ruta

**Hoy:** 
- **RFC-001 (transporte distribuido):** Servidor HTTP/SSE en Go puro sin dependencias (`CGO_ENABLED=0`), mTLS Zero-Trust, semáforos territoriales en Git y soporte de workers remotos.
- **RFC-002 (liquidación contractual entre agentes):** Perfil mínimo conforme 100% cumplido (12/12 requisitos, recibos firmados Ed25519, hash chain y leases con reaper autónomo en `main`).
- **RFC-003 (protocolo de unión y adaptador A2A):** 100% completado y cerrado en `main` (J1–J6 cumplidos con tests bajo `-race`, A1–A2 cumplidos en producción, C1–C4 documentados).

### Para qué escenario es usable hoy (Honestidad de alcance)

Gentle Mesh es usable hoy en producción para:
> **Un desarrollador individual o un equipo técnico de confianza, operando dentro de su propia red local (LAN) o VPN privada, ejecutando sus propios agentes de IA sobre repositorios de código compartidos.**

**Qué resuelve de verdad hoy (respaldado por tests deterministas en CI):**
1. **Resiliencia de red y arranque situado:** el conocimiento de la malla vive en un log inmutable de eventos (`join_log`) encadenado por SHA-256 (J1), serializado sin colisiones mediante Opción C (`BEGIN IMMEDIATE` + `UNIQUE(prev_hash)` en SQLite, J2). Un worker que se une o reconecta no arranca ciego: descarga un snapshot verificable con doble digest (`state_digest = SHA256(JCS(state))`, J3) y se engancha al stream SSE reanudable por cursor `id: <seq>:<hash>` con detección 409 de divergencia (J4).
2. **Replay determinista sin reloj de pared:** el estado proyectado en SQLite se reconstruye desde cero leyendo el log (J6); las expiraciones de leases se registran como eventos discretos (`reason: "lease_expired"`), eliminando inferencias dependientes del reloj local.
3. **Verificación territorial oportunista:** el arnés en Go fuera del sandbox Bubblewrap (`bwrap`) contrasta a posteriori los archivos modificados en Git (`git diff BaseRef..HEAD`) contra las superficies declaradas (`edit_surfaces`), emitiendo evidencia aditiva `territory.verified` en el log (J5).
4. **Admisión autenticada mTLS (S1):** el evento `node.joined` solo puede ser emitido en el log si el certificado mTLS del cliente coincide estrictamente con el `node_id` y la clave pública Ed25519 es canónica en la curva Edwards (RFC 8032 §5.1.3, PR [#181](https://github.com/Rafaeldelinares/gentle-mesh/pull/181)).
5. **Adaptador A2A estándar:** `GET /.well-known/agent-card.json` sirve una Agent Card canónica JCS firmada con sobre JWS compacto Ed25519 (A1, A2). El coordinador sirve la card del host local y distribuye las de workers federados sin firmar jamás en su nombre (§4.2.1.2).

**Qué queda FUERA del alcance actual (Fronteras y Deuda Abierta):**
- **Multi-worker concurrente en un solo host:** soporte para orquestar múltiples workers compitiendo intensivamente en la misma máquina ([#147](https://github.com/Rafaeldelinares/gentle-mesh/issues/147)).
- **Exposición a Internet pública / WAN Zero-Trust:** el sistema no está endurecido para exponerse a internet abierta sin VPN/LAN ([#156](https://github.com/Rafaeldelinares/gentle-mesh/issues/156)).
- **Webhooks con firma HMAC:** disparador de notificaciones HTTP externas con firma de eventos ([#150](https://github.com/Rafaeldelinares/gentle-mesh/issues/150) / [#63](https://github.com/Rafaeldelinares/gentle-mesh/issues/63)).
- **Límite de verificación territorial:** archivos sin commitear ignorados por `.gitignore` escapan al diff observado de Git ([#174](https://github.com/Rafaeldelinares/gentle-mesh/issues/174)).
- **Ligado de SAN en PKI:** `SignCSR` valida el CommonName pero no restringe exhaustivamente los Subject Alternative Names ([#160](https://github.com/Rafaeldelinares/gentle-mesh/issues/160)).

**Guías de uso:**
- **Guía de uso real en producción (Recomendada):** [docs/GETTING-STARTED.md](docs/GETTING-STARTED.md) — instalación, coordinador con mTLS, worker con sandbox `bwrap` (`-runner gentle-shell`), despacho de tareas reales y liquidación.
- **Demostración rápida de 2 minutos:** [docs/DEMO.md](docs/DEMO.md) — entorno simulado en Docker para ver el semáforo territorial sin agentes reales.

#### Conformidad del Perfil Mínimo RFC-003 (Log de Unión y A2A)

| Objetivo | Qué significa | Estado | Test representativo / Referencia |
|----------|---------------|--------|----------------------------------|
| **J1** — Formato de evento encadenado | Eventos encadenados por SHA-256 (`prev_hash`) con hash inmutable. | cumplido | `pkg/joinlog/concurrency_test.go` (PR [#168](https://github.com/Rafaeldelinares/gentle-mesh/pull/168)) |
| **J2** — Serialización sin colisión | Cadena lineal estricta bajo concurrencia (Opción C: `BEGIN IMMEDIATE` + `UNIQUE(prev_hash)`). | cumplido | `pkg/joinlog/concurrency_test.go` (PR [#168](https://github.com/Rafaeldelinares/gentle-mesh/pull/168)) |
| **J3** — Snapshot con doble digest | Snapshot con posición en log y digest semántico JCS (`state_digest = SHA256(JCS(state))`). | cumplido | `pkg/joinlog/snapshot_test.go` (PR [#172](https://github.com/Rafaeldelinares/gentle-mesh/pull/172)) |
| **J4** — Stream SSE reanudable | Stream en vivo con cursor `id: <seq>:<hash>` y detección de bifurcación HTTP 409. | cumplido | `pkg/joinlog/http_test.go` (PR [#172](https://github.com/Rafaeldelinares/gentle-mesh/pull/172)) |
| **J5** — Verificación territorial | Contraste de `git diff` commiteado contra surfaces declaradas fuera del sandbox. | cumplido | `pkg/joinlog/territory_test.go` (PR [#175](https://github.com/Rafaeldelinares/gentle-mesh/pull/175)) |
| **J6** — Reconstrucción desde cero | Proyección determinista de estado en SQLite desde el log sin reloj de pared. | cumplido | `pkg/joinlog/projector_test.go` (PR [#170](https://github.com/Rafaeldelinares/gentle-mesh/pull/170)) |
| **A1** — Agent Card servida | Endpoint `GET /.well-known/agent-card.json` servido en el servidor de producción. | cumplido | `pkg/server/http/a2a_server_test.go` (PR [#183](https://github.com/Rafaeldelinares/gentle-mesh/pull/183)) |
| **A2** — Agent Card firmada | Sobre JWS compacto Ed25519 sobre cuerpo canónico JCS con clave del sujeto. | cumplido | `pkg/a2a/card_test.go` (PR [#183](https://github.com/Rafaeldelinares/gentle-mesh/pull/183)) |
| **C1–C4** — Capacidades en 2 capas | Capa Agente vs. Capa Nodo, Tier 1 instantáneo, Tier 2 observado y fallos tipados. | documentado | `docs/rfcs/003-join-protocol-and-a2a-adapter.md` §4.3 (PR [#185](https://github.com/Rafaeldelinares/gentle-mesh/pull/185)) |

#### Conformidad del Perfil Mínimo RFC-002 (12/12)

| Requisito | Qué significa | Estado | Test representativo |
|-----------|---------------|--------|---------------------|
| **S1** — Identidad fuerte mTLS ligada a agent_id | Cada agente se autentica con un certificado criptográfico propio, no con una contraseña o token que se pueda copiar fácilmente. El certificado está atado a su identidad, así que nadie puede hacerse pasar por otro agente. | cumplido | `integration/agent/s1_mtls_identity_test.go:44` |
| **S2** — Verificación de firma Ed25519 antes de mutar | Antes de que un agente pueda cambiar algo en el sistema, se comprueba que la orden viene firmada con su clave privada. Si la firma no cuadra, la operación se rechaza — evita que alguien inyecte comandos falsos. | cumplido | `integration/agent/s2_signature_verification_test.go:42` |
| **S3** — Capacidades deny-by-default y perfil mínimo | Por defecto un agente no puede hacer nada; solo tiene permiso para las acciones concretas que se le han habilitado explícitamente. Es el principio de "mínimo privilegio" aplicado estrictamente. | cumplido | `integration/agent/s3_capabilities_test.go:40` |
| **S6** — Cero subshells y sandbox allowlist en precondiciones | El sistema no ejecuta comandos de shell arbitrarios (que podrían usarse para escapar del sandbox), y solo permite las operaciones que están en una lista blanca explícita. | cumplido | `integration/agent/s6_preconditions_test.go:20` |
| **S7** — Cadena inviolable y contrafirmas con dominio separado | Los recibos/eventos se encadenan criptográficamente (como un mini-blockchain) y cada contraparte firma con su propia clave en un "dominio" separado, para que nadie pueda falsificar o reordenar el historial. | cumplido | `pkg/receipt/chain_adversarial_test.go:30` |
| **S9** — Estricto protocolo v2 sin downgrade | El sistema rechaza intentos de usar una versión antigua del protocolo (que podría tener vulnerabilidades ya corregidas) para forzar un comportamiento menos seguro. | cumplido | `pkg/envelope/version_test.go:11` |
| **R1** — Idempotencia determinista por envelope_id | Si la misma petición llega duplicada (por un reintento de red, por ejemplo), el sistema la reconoce y no la ejecuta dos veces. | cumplido | `integration/agent/r1_idempotence_test.go:44` |
| **R2** — Leases con expiración y reaper autónomo | Cuando un agente "reserva" una tarea, esa reserva caduca sola si el agente se cuelga o desconecta, y un proceso de limpieza automático ("reaper") libera esas tareas abandonadas. | cumplido | `integration/agent/r2_lease_expiration_test.go:35` |
| **R5** — Estados terminales claros y absorción CAS (RESOLVED) | Una tarea, una vez resuelta, queda en un estado final e inmutable; el mecanismo que la marca como resuelta evita condiciones de carrera (dos procesos intentando resolverla a la vez). | cumplido | `pkg/envelope/contract_state_test.go:15` |
| **R7** — Firmas deterministas (1000 iteraciones SQLite) | Firmar el mismo dato siempre produce la misma firma, sin aleatoriedad — se probó 1000 veces contra la base SQLite para asegurar que no hay variabilidad que rompa la verificación. | cumplido | `pkg/envelope/signer_test.go:241` |
| **A1** — Seguro por defecto sin configurar (minimal profile) | Si no configuras nada, el sistema arranca ya en modo seguro (sin permisos abiertos), no al revés. | cumplido | `integration/agent/a1_zero_config_test.go:57` |
| **A2** — Descubrimiento por intento vía /preflight estructurado | Un agente puede preguntar de antemano, mediante un endpoint /preflight, si una acción que quiere hacer sería aceptada o no — sin tener que intentarla a ciegas. | cumplido | `integration/agent/a2_preflight_test.go:51` |

Ver la matriz completa de todos los objetivos en [docs/rfcs/002-objectives-and-non-goals.md](docs/rfcs/002-objectives-and-non-goals.md) y el histórico en el issue [#70](https://github.com/Rafaeldelinares/gentle-mesh/issues/70).

**Para la historia y el horizonte del proyecto, ver [docs/PROJECT_HISTORY.md](docs/PROJECT_HISTORY.md).**

**Roadmap completo y lo que no haremos:** [ROADMAP.md](ROADMAP.md).

**Proceso de revisión y excepción documentada:** [PROCESO-REVISION.md](docs/PROCESO-REVISION.md).

---

## Relación con las sesiones remotas de pi

**pi** (`@earendil-works/pi-protocol` 1.0.4, `@earendil-works/pi-client` 1.0.4, `@earendil-works/pi-server` 1.0.4 y `@earendil-works/pi-coding-agent` 1.0.4) proporciona sesiones remotas de agentes. `pi-protocol` implementa un protocolo con codificación CBOR y byte-stream framing sobre el protocolo Pi experimental (versión 8; el README de `pi-protocol` indica que "The protocol is experimental and has no compatibility guarantees"); `pi-client` es un cliente que trabaja sobre cualquier transporte de bytes ordenado mediante la interfaz `ByteTransport` (`dist/transport.d.ts`: `export interface ByteTransport`; npm: "Transport-neutral client for the experimental Pi service protocol"); `pi-server` es experimental (npm: "experimental server package for pi") y se aloja en la aplicación que lo usa. El subcomando `rpc` de gentle-mesh puentea el modo RPC de pi por stdin/stdout. Gentle Mesh no implementa el protocolo de sesiones remotas de pi-protocol.

**Gentle Mesh** coordina varios agentes y nodos que trabajan sobre el mismo proyecto. Sus preocupaciones son:
- **Territorios y colisiones:** evita que dos tareas toquen el mismo código a la vez y da visibilidad de quién hace qué.
- **Reparto de tareas entre nodos:** elige un nodo compatible por rol, etiquetas, capacidad y carga.
- **Seguridad de la malla:** PKI, mTLS y enrollment de nodos con certificados verificados.

Las dos herramientas atacan problemas distintos. Gentle Mesh no pretende sustituir las sesiones remotas de pi y no tiene hoy un adaptador para el protocolo de sesiones remotas de pi. Una integración futura es posible, pero es un camino abierto sin desarrollo planificado.

---

## 1. La Visión: Crear Sistemas Entre Varios

La potencia distribuida en sistemas multi-agente no se trata únicamente de velocidad o de que una tarea tarde menos tiempo en compilar. Se trata de **la posibilidad de construir sistemas de software complejos entre varios seres humanos y múltiples agentes concurrentes**, cooperando sobre el mismo proyecto sin pisarse la cabeza.

Hoy en día, el uso de agentes de IA es aislado y solitario: un desarrollador con su terminal local. Si varios miembros de un equipo o de la comunidad lanzan agentes al mismo tiempo, el conflicto de merge es inevitable y nadie sabe qué está haciendo el otro.

`gentle-mesh` actúa como el **sistema nervioso central** de una red de agentes:
1. **Desacopla el Cómputo del Cliente:** Tu laptop sólo despacha intenciones y consume texto; la carga pesada (compilaciones, linters, suites de tests) se delega a nodos remotos (servidores, VPS o máquinas secundarias).
2. **Consciencia Situacional Compartida (Radar de Ámbitos):** Cada agente declara su **Dominio Arquitectónico**, sus **Superficies de Edición** autorizadas y su **Radio de Impacto** (*Blast Radius*).
3. **Detección Preventiva de Colisiones y Live Stream Hooking:** La malla detecta choques de territorio antes de escribir una sola línea de código y permite que clientes secundarios se enganchen en vivo a tareas duplicadas ya en curso.
4. **Binario Único en Go Puro:** Cero dependencias pesadas, compilación estática (`CGO_ENABLED=0`), arranque en milisegundos y consumo de memoria ridículamente bajo (~30 MB de RAM para un clúster de 6 nodos).

**Enrutado de nodos explícito:** quien envía la tarea indica el rol (`agent`) y las etiquetas (`tags`) requeridas; el `registry` la entrega a un nodo registrado que soporta ese rol y esas etiquetas. Si hay varios compatibles, elige por capacidad y reparto de carga; si no hay ninguno, la tarea cae al runner local o falla. La malla **coordina territorios** (evita que dos agentes toquen el mismo código a la vez); no planifica el trabajo.

---

## Alternativas y cómo se relaciona

Gentle Mesh no es la única herramienta que intenta coordinar agentes de IA sobre el mismo proyecto. Existen otros enfoques con objetivos similares o parcialmente solapados.

### Lo que ya existe

**Git worktrees.** La forma más directa de aislar agentes es crear un worktree de Git por tarea (por ejemplo, `git worktree add ../rama-ana ana/feature-login`). Cada worktree tiene su propio directorio de trabajo y su propio índice; así dos agentes pueden editar el mismo fichero simultáneamente sin que Git lo detecte. El conflicto aparece al integrar, no al trabajar. Varias herramientas de agentes usan esto por debajo.

**[CoordinationHub](https://github.com/IronAdamant/coordinationhub)** — MCP server en Python stdlib, cero dependencias de terceros. Tablón compartido con registro de agentes, bloqueos de fichero con TTL y bloqueo por región, y dashboard web. Último commit: `2026-06-11` ([último commit de CoordinationHub](https://github.com/IronAdamant/coordinationhub/commits): `2026-06-11T07:21:12Z`).

**[Wit](https://github.com/amaar-mc/wit)** — Bun, SQLite, protocolo JSON-RPC sobre Unix socket. Bloqueo semántico mediante tree-sitter y contratos de firma de función con git pre-commit hook. Solo máquina local.

**[Shepherd](https://github.com/Korso-AI/Shepherd)** — Hub con Fastify y Postgres, MCP server stdio compatible con cualquier cliente MCP (Claude Code, Codex, Pi, Cursor) y tablero React para visibilidad y leases.

**[MCP Agent Mail](https://github.com/Dicklesworthstone/mcp_agent_mail)** — Servidor FastMCP con identidad persistente por agente, bandeja de entrada y salida, leases consultivos de fichero y archivos Git para auditoría.

*Proyectos verificados el 2026-10-07 (HTTP 200). No se han probado de forma práctica; las descripciones se basan en la documentación de sus repositorios.*

### Qué cambia Gentle Mesh

Gentle Mesh se distingue en tres puntos verificables en el código:

- **El coordinador despacha las tareas** (`pkg/server/http/handlers.go`, `pkg/server/registry/`): el agente no necesita consultar una herramienta ni consultar un tablón por su cuenta; el coordinador recibe la tarea y la entrega al nodo más apropiado. La cola con auto-arranque (`queue` mode, `pkg/server/http/scheduler.go`) garantiza que una tarea que no puede ejecutarse ahora espera y arranca sola cuando se libera el territorio.
- **Nodos remotos con mTLS.** Los nodos se unen a la malla con certificados emitidos por la CA del coordinador (`pkg/server/store/certstore.go`, `pkg/server/http/handlers.go`). Las alternativas listadas no declaran autenticación mutua de certificados entre máquinas remotas de serie en sus documentaciones (ver los repos respectivos).
- **Límites de lo que Gentle Mesh declara:** el coordinador solo conoce lo que cada tarea declara como superficie; no lee el código (`pkg/protocol/`, `pkg/server/http/scheduler.go`). Los bloqueos se basan en lo declarado, no en análisis estático ni en parseo de AST.

Los worktrees aíslan los ficheros pero el conflicto aparece al integrar; Gentle Mesh intenta evitar que dos agentes trabajen la misma zona a la vez y dar visibilidad de quién hace qué. Son enfoques distintos y no se ha verificado que se combinen sin conflicto.

---

## 2. Pila Tecnológica y Arquitectura

* **Lenguaje:** Go 1.26.9+ estándar (`net/http`, `encoding/json`, `sync`, `context`, `database/sql`). Cero frameworks web externos ni librerías de C.
* **Transporte:** HTTPS REST + Server-Sent Events (HTTPS/SSE) para streaming continuo y seguro de pensamientos (`thought`), llamadas a herramientas (`tool_call`) y resultados, con cifrado de transporte TLS; la autenticación mutua (mTLS) solo se exige con `-require-mtls`.
* **Persistencia Dual y Resiliencia ante Caídas (Crash Recovery):**
  * **Streaming de Eventos:** Append-only logs en formato **JSONL** (`<tasks-dir>/{id}.jsonl`). Permite reconexión histórica instantánea vía el header estándar `Last-Event-ID` con consumo de RAM constante $O(1)$.
  * **Estado Maestro y Rehidratación:** Base de datos embebida **SQLite en Go puro** (`modernc.org/sqlite`, `CGO_ENABLED=0`) con modo **WAL** (*Write-Ahead Logging*). Si el servidor se apaga o reinicia, rehidrata automáticamente el catálogo de tareas sin pérdida de estado.
* **Ejecución y Supervisión:** cuatro motores (`mesh`, `gentle-shell`, `pi`, `simulated`, ver 2.4). El runner `pi` detecta inactividad (5 minutos sin salida → `TASK_INACTIVITY_TIMEOUT`); el runner `gentle-shell` aplica además presupuestos de tiempo, límite de `tool_call` y aislamiento Bubblewrap. La detección de bucles y el auto-commit WIP **están planificados** (RFC-001 §8) y **no están implementados**.

```text
gentle-mesh/
├── cmd/
│   └── gentle-mesh/          # CLI principal: server, worker, nodes, radar, run
├── pkg/
│   ├── protocol/             # Tipos canónicos, eventos SSE, serialización y colisiones
│   ├── server/
│   │   ├── http/             # Servidor HTTPS REST, middleware Bearer y streaming SSE
│   │   ├── registry/         # Catálogo de nodos, branch locking e idempotencia
│   │   ├── store/            # TaskStore: MemoryStore y SQLiteStore embebido (WAL mode)
│   │   ├── task/             # Gestor de tareas, state machine, crash recovery y logger JSONL
│   │   ├── runner/           # Motores: MeshRunner (routing), GentleShellRunner (bwrap+worktree), PiRunner y SimulatedRunner
│   │   ├── federation/       # Peering M2M y TerritoryManager para colisiones compartidas
│   │   └── worker/           # Servidor worker autónomo con despacho remoto HTTP
├── docs/
│   ├── architecture/         # Diagramas interactivos y Hub de verificación (Archify)
│   ├── rfcs/                 # RFC 001: Especificación técnica canónica
│   └── EVALUATION_GUIDE_FOR_LLMS.md # Guía para revisión externa (Claude / GPT)
└── docker-compose.test.yml   # Topología multi-nodo de prueba (1 coordinator + 5 workers)
```

### 2.1 Persistencia Dual y Recuperación ante Caídas (Crash Recovery)

Para funcionar como un demonio de infraestructura desatendido sin requerir servidores de base de datos externos (PostgreSQL, MySQL o Redis):

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
   * Parámetro `-db-path`: Define la ruta del archivo de base de datos (por defecto `<tasks-dir>/gentle-mesh.db`). Para entornos efímeros o pruebas puramente en memoria, basta con indicar `-db-path=none`. Con `-tls` el valor `none` **no** es válido: el almacén de tokens de enrollment y los webhooks necesitan SQLite, así que `server -tls -db-path none` falla con un error explícito.

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
   * `warn`: ante un solapamiento de superficies (ramas distintas), emite una **advertencia no fatal** en el stream de eventos (un evento `thought`) y despacha igualmente la tarea. Si la **rama** ya está bloqueada por otra tarea, la petición se rechaza con `HTTP 409 Conflict` (`registry.ErrBranchLocked`).
   * `strict`: rechaza de inmediato con `HTTP 409 Conflict` (modo estricto tradicional, útil para pipelines de CI que exigen exclusión dura).
   * `disabled`: desactiva la comprobación de conflictos territoriales **por solapamiento de superficies** y despacha siempre en ese caso. El **bloqueo de rama** sigue aplicándose: dos tareas sobre la misma rama con otra en curso se rechazan con `HTTP 409 Conflict`.
4. **Configuración vía CLI:**
   * El subcomando `server` expone el flag `-territory-mode=queue|warn|strict|disabled`. Un valor desconocido aborta el arranque con un error explícito, evitando degradaciones silenciosas de política.

### 2.3 Compatibilidad con Open Pi Viewer y Clientes Desacoplados

Gentle Mesh está diseñado para interoperar de forma nativa con interfaces gráficas de usuario, clientes de escritorio y móviles sin necesidad de instalar o ejecutar subprocesos de Node.js en la máquina cliente:

1. **Protocol Aliasing de Entrada y Salida:**
   * `TaskRequest` acepta indistintamente `prompt` o `task`, mapea el `session_id` del visor, asigna agente por defecto (`worker`) y replica el texto completado en `CompletionPayload` tanto en `result` como en `text`.
2. **CORS y Streaming SSE Amigable para Tauri y Navegadores:**
   * Middleware CORS que reconoce orígenes Tauri (`tauri://localhost`, `http://tauri.localhost`), localhosts web (`http://localhost:*`) y redes seguras privadas Tailscale (`100.*.*.*`, `*.ts.net`), con bypass preflight `OPTIONS` (204 No Content).
3. **Exploración Remota de Archivos Segura:**
   * Endpoints `GET /v1/workspace/tree` y `GET /v1/workspace/file` con contención estricta anti-traversal previa a la normalización (bloqueando escapes `..` con `403 Forbidden` y límite de lectura de 5MB).
4. **Conector Nativo HTTPS/SSE (`open-pi-viewer`):**
   * El visor `open-pi-viewer` incorpora soporte de primera clase para `connectionType: 'mesh'`, conectándose directamente por HTTPS/SSE a Gentle Mesh mediante `GentleMeshClient`, traduciendo eventos remotos en tiempo real al bus del visor sin requerir binarios locales de Node.js o Pi CLI.

### 2.4 Motores de ejecución (`-runner`): de la simulación al sandbox

El coordinador no ejecuta agentes: elige un motor de ejecución y delega. Los cuatro valores del flag `-runner` no son equivalentes, y confundirlos es la causa habitual de "creo que el agente está trabajando" cuando en realidad nada se ejecutó.

| `-runner` | Dónde corre | Qué ejecuta | Aislamiento |
|-----------|-------------|-------------|-------------|
| `mesh` (default del `server`) | el nodo que el registry elige por rol y etiquetas | lo que ese nodo tenga configurado | el del nodo receptor |
| `simulated` (default del `worker`) | en memoria | **nada**: emite `tool_call`/`completion` sintéticos y un `thought` con `WARNING: running in SIMULATED mode` | ninguno (no toca el disco) |
| `pi` | subproceso local | el binario `pi` en el directorio de trabajo | **ninguno**: sin sandbox ni worktree |
| `gentle-shell` | subproceso aislado | `gentle-shell` (que a su vez invoca `pi`) dentro de `bwrap`, sobre un worktree efímero | **Bubblewrap**, con `HOME` fuera del namespace |

Con `-runner=mesh`, si no hay ningún nodo elegible la tarea **falla con `DISPATCH_FAILED`**: el fallback local simulado está desactivado por defecto (`-local-fallback=true` lo vuelve a habilitar, y solo afecta a `mesh`).

**`gentle-shell` es el runner de producción.** Por cada tarea:

1. Crea un **worktree efímero** a partir de un cache bare local **sin remotos** (`-gentle-shell-repo-cache`), bajo `-gentle-shell-worktrees`.
2. Ejecuta `gentle-shell` dentro de `bwrap` (`-bwrap-bin`), montando solo lo necesario para el toolchain y el worktree.
3. El agente deja su trabajo **commiteado en la rama `proposal/<task_id>`** del cache bare: el runner le exige el commit en su prompt y después verifica, de forma determinista, que HEAD se movió más allá del ref base. Si el agente termina sin commit, la tarea falla con `NO_COMMIT_PRODUCED`, por más confiado que suene su mensaje final. El runner devuelve el diff (inline si cabe, o como archivo en `-gentle-shell-tasks-dir`).
4. Elimina el worktree al terminar; la rama de propuesta queda lista para que el operador la revise con `gentle-mesh apply`.

Flags requeridos (típicamente obligatorios) y presupuestos:

* `-gentle-shell-repo-cache <path>` (**requerido**) — cache bare sin remotos que sirve de base a los worktrees.
* `-gentle-shell-worktrees <path>` (**requerido**) — directorio padre de los worktrees efímeros.
* `-gentle-shell-base-ref <ref>` (default `main`) — ref base de la rama de propuesta.
* `-gentle-shell-total-timeout` (default `15m`) y `-gentle-shell-idle-timeout` (default `5m`) — presupuestos de reloj de pared y de inactividad.
* `-gentle-shell-max-tool-calls` (default `15`) — corta la tarea si emite más `tool_call` de los permitidos.
* `-gentle-shell-bin`, `-bwrap-bin`, `-gentle-shell-thinking`, `-gentle-shell-provider`, `-gentle-shell-model`, `-gentle-shell-tasks-dir` — binarios y parámetros que se reenvían a `pi`.

Los worktrees efímeros y las ramas `proposal/*` de intentos fallidos **no se recolectan automáticamente todavía** ([#120](https://github.com/Rafaeldelinares/gentle-mesh/issues/120)).

### 2.5 Validación con agentes y hardware reales (Fase 2)

La ejecución real está verificada empíricamente, no descrita:

* Ejecución de Pi dentro de `bwrap` sobre worktrees efímeros ([#118](https://github.com/Rafaeldelinares/gentle-mesh/pull/118)).
* Caída de worker bajo sandbox: detección `NODE_LOST`, reaper de leases y re-despacho ([#119](https://github.com/Rafaeldelinares/gentle-mesh/pull/119)).
* Captura de uso de tokens y costo del paralelismo frente a la ejecución serial ([#121](https://github.com/Rafaeldelinares/gentle-mesh/pull/121)).
* Worker físico en LAN (Debian 13) resolviendo DNS dentro de `bwrap` ([#124](https://github.com/Rafaeldelinares/gentle-mesh/pull/124)).
* Tarea sintética resuelta con un ciclo TDD propio dentro del sandbox, 21/21 tests en verde con `-race` ([#128](https://github.com/Rafaeldelinares/gentle-mesh/pull/128)).
* Validación de inter-host con dos máquinas distintas —una como coordinador, otra como ejecutor— completando una tarea de principio a fin con un agente real dentro del entorno aislado, con el binario de ambas verificado como idéntico byte a byte antes de confiar en el resultado (ver [`docs/PROJECT_HISTORY.md`](docs/PROJECT_HISTORY.md)).

Los E2E viven en `integration/f2/`. La relación entre RFC-001 y RFC-002 en uso: RFC-001 mueve y aísla la tarea (transporte HTTPS/SSE, worktree, territory); RFC-002 la obliga a un contrato verificable (`CognitiveTaskEnvelope`, liquidación con recibos firmados) antes y después de ejecutarla.

---

## 3. Demostración Rápida en Local (Entorno Seguro)

> 📖 **Guía de uso real en producción:** si buscas poner en marcha un clúster real con mTLS, workers remotos, aislamiento Bubblewrap (`bwrap`), ejecución de agentes reales y liquidación con recibos firmados, consulta la guía paso a paso: **[docs/GETTING-STARTED.md](docs/GETTING-STARTED.md)**.
>
> **Demo en vivo:** [docs/DEMO.md](docs/DEMO.md) — coordinator local sin TLS, dos tareas con superficies solapadas que demuestra la cola automática.

> ⚠️ **Qué demuestra esta sección, y qué no.** La demo levanta el coordinador con `-runner pi` (subproceso local, **sin sandbox ni worktree**) usando `docs/demo/fake-pi`, un script que duerme y devuelve texto: sirve para ver el territorio y la cola, **no** para juzgar el aislamiento ni la calidad de la ejecución. Para ejecución real con agentes dentro de `bwrap` sobre worktrees efímeros, mira el runner `gentle-shell` (2.4), sus flags y los E2E de `integration/f2/`.

### Requisitos
* Go 1.26.9+ (mínimo declarado en `go.mod`) o Docker / Docker Compose.

### Ejecutar todas las pruebas con detector de carreras
```bash
go test -v -race ./...
```
*(Los paquetes `./cmd/...` y `./pkg/...` cuentan con cobertura unitaria y el CI ejecuta `go test -race` en ellos. Algunos paquetes auxiliares (por ejemplo `pkg/server/webhook/`) no tienen tests automatizados todavía.)*

### Levantar el coordinador en local (sin TLS, desarrollo)
```bash
go run ./cmd/gentle-mesh server -workspace . -territory-mode queue
```
*(Por defecto escucha de forma segura en `127.0.0.1:8080`. La primera compilación con `go run` puede tardar más que la ejecución de un binario ya compilado. No es un benchmark.)*
El flag `-territory-mode` acepta `queue` (por defecto), `warn`, `strict` o `disabled`; consulta el semáforo inteligente en la sección 2.2.

Para el camino TLS/mTLS usa `server -addr :8443 -tls -tls-init -require-mtls`. Ten en cuenta que los clientes CLI (`nodes`, `radar`, `run`, `rpc`) todavía **no** presentan certificado de cliente, así que no alcanzan un coordinador con `-require-mtls` hasta la v1.0.4.

### Levantar el clúster de prueba de 6 nodos en Docker
El repositorio incluye una topología lista para probar en una red bridge aislada (`gentle-mesh-net`):
```bash
docker compose -f docker-compose.test.yml up -d
```
*(La primera vez se descargan las imágenes, lo que puede tardar más que en ejecuciones posteriores. No es un benchmark.)*

### Consultar los nodos registrados en la malla
```bash
go run ./cmd/gentle-mesh nodes -coordinator http://localhost:8080
```
Salida esperada:
```text
NODE ID         ENDPOINT                     STATUS  CONCURRENCY  AGENTS   TAGS
worker-alpha    http://worker-alpha:8081     online  0/2          worker   go,fast
worker-beta     http://worker-beta:8081      online  0/4          worker   heavy,docker
worker-gamma    http://worker-gamma:8081     online  0/2          explore  research
worker-delta    http://worker-delta:8081     online  0/1          worker   gpu,ml
worker-epsilon  http://worker-epsilon:8081   online  0/2          verify   ci,testing
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

> **`-branch` / `git_branch` es una reserva, no un checkout.** El campo `git_branch` de `POST /v1/tasks` (y el flag `-branch` del CLI)
> es la **clave del lock de rama**: reserva en exclusiva el par `repo`+`rama` para que dos tareas no trabajen el mismo
> branch a la vez, y entra en el territorio (conflicto `branch_locked`, `HTTP 409`; en modo `queue` la segunda tarea
> espera). El coordinador **nunca hace checkout ni valida que la rama exista**: sólo guarda el valor en la columna `branch`
> de la tarea y en el lock, que vive **en memoria** y no sobrevive a un reinicio. El único motor que toca Git es
> `-runner gentle-shell`, y lo hace **en el worker**, sobre un cache bare local y un worktree efímero, creando su propia
> rama `proposal/<task_id>` (ver 2.4).

### Aplicar la propuesta de una tarea con `apply`

Después de que un worker termina una tarea y produce una rama `proposal/<task_id>` en el cache bare (`RepoCache`), el operador puede **materializarla como una rama local** en el repo de destino para revisarla con `git log` / `git diff` normales y decidir el merge final a mano. **Nunca se hace push, nunca se mergea a `main`, nunca se modifica un remoto**: el resultado es siempre una rama local nombrada `proposal-merge/<task_id>` lista para inspección.

```bash
go run ./cmd/gentle-mesh apply -task <task_id> \
  -cache /path/to/worker-cache.git \
  -repo /path/to/target-repo
```

- `-task` (required): ID de la tarea cuya rama `proposal/<task_id>` se aplica.
- `-cache` (**requerido**): ruta al cache bare donde el runner deja la propuesta (el mismo valor que el worker usa en `-gentle-shell-repo-cache`).
- `-repo` (default: cwd): ruta al repo donde se crea la rama local.
- `-base` (default `main`): ref base desde la que se crea la rama merge.

El subcomando:
1. Añade el cache como **remote temporal** `apply-cache-<task_id>` (se elimina siempre, incluso en error).
2. Hace **fetch** solo del refspec `proposal/<task_id>:proposal/<task_id>` (forzado, sobreescribe la rama local si ya existía de un apply anterior).
3. Verifica que la propuesta tiene **al menos un commit ahead** de la base y que el **working tree** del repo está limpio.
4. Crea la rama `proposal-merge/<task_id>` desde la base y mergea la propuesta con `--no-ff` (commit de merge explícito).
5. Reporta qué hacer a continuación (`git log` / `git diff` para inspeccionar, `git checkout main && git branch -D proposal-merge/<task_id>` para descartar).

**No se hace push** bajo ningún concepto. La rama vive solo en el repo del operador hasta que él decida qué hacer con ella.

### Puente Drop-in JSON-RPC (`rpc`) para Open Pi Viewer

Si ya contás con una interfaz gráfica que habla el protocolo RPC de Pi (Open Pi Viewer stock, cockpits móviles o cualquier herramienta que dialogue por `stdin`/`stdout`), no hace falta reescribir el cliente: el subcomando `gentle-mesh rpc` actúa como un **puente transparente drop-in** sobre `stdin`/`stdout`.

Acepta los mismos flags de compatibilidad que un entrypoint local de Pi (`--mode rpc --approve [--session <file>]`) y opera así:

1. **Entrada (`stdin`):** lee solicitudes JSON-RPC delimitadas por saltos de línea (`prompt`, `get_state`, `get_messages`, `new_session`, `abort`), tolerando líneas vacías o malformadas sin abortar la sesión y respetando la concurrencia de múltiples prompts.
2. **Traducción a REST:** convierte cada `prompt` en un despacho `POST /v1/tasks` contra el coordinador, con el agente configurado y el token Bearer opcional.
3. **Streaming HTTPS/SSE:** consume el stream cifrado de eventos de la tarea (`GET /v1/tasks/{id}/events`) y traduce los eventos remotos (`thought`, `tool_call`, `tool_result`, `completion`, `status`).
4. **Salida (`stdout`):** escribe líneas JSON-RPC estándar de Pi (`message_start`, `message_update`, `tool_execution_start`, `tool_execution_end`, `agent_settled`, `message_end`), de modo que el visor no distingue que la ejecución ocurrió en un nodo remoto.

```bash
go run ./cmd/gentle-mesh rpc -coordinator https://<coordinator-ip>:8443
```

Flags propios del puente: `-coordinator` (por defecto `GENTLE_MESH_COORDINATOR` o `https://localhost:8443`), `-token` (opcional; por defecto `GENTLE_MESH_TOKEN`) y `-agent` (rol despachado por cada prompt, por defecto `worker`). Los flags `--mode`, `--approve` y `--session` se aceptan e ignoran, permitiendo lanzarlo con la misma forma de argumentos que un binario Pi local. El comando `abort` cancela todos los prompts en vuelo.

---

### 3.1 Seguridad TLS/mTLS

Gentle Mesh soporta cifrado de tráfico con TLS y autenticación mutua (mTLS) para garantizar que solo nodos verificados puedan unirse a la malla.

#### 3.1.1 PKI Centralizada

El coordinator actúa como **CA raíz** de la malla, emitiendo certificados para cada nodo:

```text
┌─────────────────────────────────────────────────────────────┐
│                     Coordinator (CA Raíz)                     │
│  ┌─────────────────────────────────────────────────────┐    │
│  │  gentle-mesh-ca.pem (público, compartir)          │    │
│  │  gentle-mesh-ca.key (privado, NUNCA compartir)     │    │
│  └─────────────────────────────────────────────────────┘    │
└─────────────────────────────────────────────────────────────┘
                              │
          ┌───────────────────┼───────────────────┐
          ▼                   ▼                   ▼
    ┌──────────┐       ┌──────────┐       ┌──────────┐
    │ worker-α │       │ worker-β │       │  viewer  │
    │  (mTLS)  │       │  (mTLS)  │       │ (viewer) │
    └──────────┘       └──────────┘       └──────────┘
```

#### 3.1.2 Inicializar TLS

```bash
# Genera CA + certificado de servidor automáticamente
gentle-mesh server -tls-init -tasks-dir /tmp/mesh
```

Archivos generados:
- `tls/gentle-mesh-ca.pem` — CA pública (compartir con nodos)
- `tls/gentle-mesh-ca.key` — Clave CA (privada)
- `tls/cert.pem` — Certificado del servidor
- `tls/cert.key` — Clave del servidor

#### 3.1.3 Modos de Seguridad

| Modo | Descripción | Uso |
|------|------------|-----|
| **HTTP** | Sin cifrado (desarrollo local) | `gentle-mesh server` |
| **TLS** | Cifrado de canal (servidor → cliente) | `gentle-mesh server -tls` |
| **mTLS** | Cifrado + autenticación mutua | `gentle-mesh server -tls -require-mtls` |

#### 3.1.4 mTLS: Autenticación Mutua

Con `-require-mtls`, el coordinator **exige** un certificado de cliente verificado en las rutas protegidas. Las rutas de arranque `/healthz`, `GET /v1/mesh/ca` y `POST /v1/certs/enroll` quedan accesibles **sin** certificado para permitir el enrollment:

```bash
# 1. Iniciar coordinator con mTLS obligatorio
gentle-mesh server -tls -tls-dir /tmp/mesh/tls -require-mtls -addr :8443

# 2. Emitir certificado para un nodo
gentle-mesh cert-issue -tls-dir /tmp/mesh/tls -node-id worker-alpha

# 3. Listar certificados emitidos
gentle-mesh cert-list -tls-dir /tmp/mesh/tls

# 4. Worker conecta con certificado
gentle-mesh worker \
  -coordinator https://localhost:8443 \
  -ca /tmp/mesh/tls/gentle-mesh-ca.pem \
  -cert /tmp/mesh/tls/worker-alpha.pem \
  -key /tmp/mesh/tls/worker-alpha.key
```

El certificado de servidor se emite siempre con `localhost` y los loopbacks como
SAN. Para servir sobre una IP de LAN hay que pedirla explícitamente, o cualquier
cliente que verifique el nombre falla con `x509: certificate is valid for
localhost, not <ip>`:

```bash
gentle-mesh server -tls -tls-dir /var/mesh/tls -require-mtls \
  -addr 192.168.1.26:8630 -tls-san 192.168.1.26
```

`server` y `worker` toman su token Bearer de `$GENTLE_MESH_TOKEN` cuando no se
pasa `-token`, de modo que la credencial no aparezca nunca en la lista de
argumentos del proceso (que cualquier usuario local puede leer con `ps`). Un
flag explícito gana sobre la variable:

```bash
export GENTLE_MESH_TOKEN="$(cat /var/mesh/.token)"   # archivo 0600
gentle-mesh server -tls -tls-dir /var/mesh/tls -require-mtls -addr 192.168.1.26:8630
gentle-mesh worker -coordinator https://192.168.1.26:8630 -node-id worker-alpha \
  -cert /var/mesh/tls/worker-alpha.pem -key /var/mesh/tls/worker-alpha.key \
  -ca /var/mesh/tls/gentle-mesh-ca.pem
```

#### 3.1.5 Comandos de Gestión de Certificados

```bash
# Emitir certificado para un nodo
gentle-mesh cert-issue -tls-dir /tmp/mesh/tls -node-id worker-alpha \
  -valid-days 365 -output /tmp/node-certs

# Listar certificados emitidos
gentle-mesh cert-list -tls-dir /tmp/mesh/tls

# Revocar certificado contra el almacén local
gentle-mesh cert-revoke -node-id worker-alpha -db-path /tmp/mesh/gentle-mesh.db

# Revocar contra un coordinador remoto (requiere el token de administración)
gentle-mesh cert-revoke -node-id worker-alpha \
  -coordinator https://coordinator:8443 -token <token-admin>
```

La revocación está implementada, no pendiente: el coordinador expone `POST /v1/certs/revoke` (token de administración; sin él responde `503` en modo fail-closed), marca el certificado en su almacén, **desregistra el nodo** y **cancela sus tareas en vuelo** con el código `NODE_REVOKED`, de forma atómica ([#25](https://github.com/Rafaeldelinares/gentle-mesh/issues/25)). Lo que aún no hay es distribución de una **CRL** descargable por un tercero que no hable con el coordinador.

#### 3.1.6 Flags TLS/mTLS

**Server:**
- `-tls` — Habilitar HTTPS
- `-tls-dir <path>` — Directorio con certificados
- `-tls-init` — Generar nueva CA y certificados
- `-require-mtls` — Exigir certificado de cliente verificado en las rutas protegidas (requiere `-tls` y una CA)
- `-cors-origins <lista>` — Orígenes CORS adicionales, separados por comas, que se **suman** a la allowlist por defecto (`tauri://localhost`; `http://localhost`/`http://127.0.0.1` en cualquier puerto; Tailscale `100.64.0.0/10` y `*.ts.net`)
- `-allowed-hosts <lista>` — Nombres de host adicionales permitidos, separados por comas (necesario para nombres cortos de MagicDNS o nombres propios; por defecto se deriva de la dirección de escucha más `localhost`, `127.0.0.1` y `::1`)
- `-insecure-no-auth` — Permite arrancar `-runner pi` en una dirección no loopback sin `-token` ni `-require-mtls` (por defecto se rechaza)

**Worker/Cliente:**
- `-ca <path>` — CA para verificar el servidor
- `-ca-cert-hash sha256:<hex>` — huella fijada del CA: el worker lo descarga de `GET /v1/mesh/ca` y lo rechaza si no coincide
- `-cert <path>` — Certificado de cliente (mTLS)
- `-key <path>` — Clave del certificado (mTLS)
- `-insecure-skip-tls-verify` — Para desarrollo (NO usar en producción)

#### 3.1.7 Descarga Automática de CA

El worker exige confiar en el CA del coordinator cuando usa HTTPS. Las opciones son: `-ca <path>` (CA local), `-ca-cert-hash sha256:<hex>` (descarga el CA de `GET /v1/mesh/ca` y verifica la huella fijada) o `-insecure-skip-tls-verify` (solo desarrollo). Sin ninguna de ellas, el worker falla:

```bash
# Descarga el CA desde el coordinator y lo verifica contra la huella fijada
gentle-mesh worker -coordinator https://coordinator:8443 \
  -ca-cert-hash sha256:<huella-del-ca>
```

#### 3.1.8 Enrollment Automático con Tokens

Para entornos de producción, Gentle Mesh soporta **enrollment automático de certificados** mediante tokens de invitación. Este flujo usa **CSR (Certificate Signing Request)** para que la clave privada del nodo **nunca salga de la máquina local**.

**Flujo de enrollment:**

```
1. Admin genera token:
   gentle-mesh gen-token -db-path /var/mesh/gentle-mesh.db
   → Token: xyz123... (usar 1 vez, expira en 30 días)

2. Worker usa token para enroll:
   gentle-mesh worker -join-token xyz123... -coordinator https://mesh.example.com \
     -ca-cert-hash sha256:<huella-del-ca>

3. Worker genera CSR localmente (clave privada stays local)

4. Worker envía CSR + token al coordinator

5. Coordinator valida token, firma CSR con CA

6. Coordinator devuelve certificado firmado

7. Worker guarda cert+key, usa mTLS para unirse
```

**Comandos de tokens:**

```bash
# Generar token de enrollment (max-uses: 1 por defecto)
gentle-mesh gen-token -db-path /var/mesh/gentle-mesh.db

# Generar token multi-uso (para varios workers)
gentle-mesh gen-token -db-path /var/mesh/gentle-mesh.db -max-uses 10 -valid-days 7

# Listar tokens activos
gentle-mesh token-list -db-path /var/mesh/gentle-mesh.db

# Revocar token
gentle-mesh token-revoke -db-path /var/mesh/gentle-mesh.db -token xyz123...
```

**Seguridad del CSR:**

El enrollment automático usa un flujo **Zero-Knowledge**:

| Paso | Qué ocurre | Dónde está la clave |
|------|-----------|-------------------|
| 1 | Worker genera par de claves ECDSA P-256 | Local |
| 2 | Worker genera CSR con clave pública | Local |
| 3 | Worker envía CSR al coordinator | CSR tiene clave pública, NO la privada |
| 4 | Coordinator firma CSR con CA | Coordinator |
| 5 | Coordinator devuelve certificado | Worker |
| **6** | **Worker tiene clave privada + certificado** | **Local** |

**Ventajas sobre issuance manual:**

- ✅ Clave privada nunca sale del nodo
- ✅ No requiere acceso SSH al nodo
- ✅ Tokens auditables y revocables
- ✅ Perfect Forward Secrecy (cada nodo tiene su propia clave)

#### 3.1.9 Seguridad: Viewer vs Worker

| Función | open-pi-viewer como Viewer | open-pi-viewer como Worker |
|---------|---------------------------|---------------------------|
| Confiar en CA | ✅ Necesario | ✅ Necesario |
| Certificado de cliente | ❌ No | ✅ Necesario |
| Flags | `-ca` | `-ca -cert -key` |

---

### 3.2 Webhooks de Notificación

Gentle Mesh soporta **webhooks** para recibir notificaciones cuando las tareas terminan, fallan o expiran. Esto elimina la necesidad de hacer polling constante.

#### 3.2.1 Registrar un Webhook

```bash
# Registrar webhook para recibir notificaciones
curl -X POST https://localhost:8443/v1/webhooks \
  -H "Content-Type: application/json" \
  -d '{
    "url": "https://tu-servidor.com/webhook",
    "secret": "tu-secret-opcional",
    "events": ["task.completed", "task.failed", "task.timeout"]
  }'
```

#### 3.2.2 Eventos Soportados

| Evento | Descripción |
|--------|-------------|
| `task.completed` | Tarea completada exitosamente |
| `task.failed` | Tarea falló por error |
| `task.timeout` | Tarea expiró por timeout |
| `*` | Todos los eventos |

#### 3.2.3 Payload del Webhook

```json
{
  "id": "notif-1699999999123456789",
  "event": "task.completed",
  "timestamp": 1699999999,
  "task_id": "task-abc123",
  "status": "completed",
  "result": "Resultado de la tarea...",
  "task": {
    "task_id": "task-abc123",
    "status": "completed",
    "created_at": 1699999990,
    "request": {
      "agent": "worker",
      "task": "..."
    }
  }
}
```

#### 3.2.4 Firma de las entregas

El servidor envía siempre las cabeceras `X-Webhook-Event` y `X-Webhook-ID`. Cuando el webhook se registró con `secret`, añade además `X-Webhook-Signature`: el HMAC-SHA256 del payload, en hexadecimal y con el prefijo `sha256=`.

```
X-Webhook-Event: task.completed
X-Webhook-ID: notif-1699999999123456789
X-Webhook-Signature: sha256=<hex>   # solo si se configuró "secret"
```

Cálculo de la firma, reproducible en el receptor:

```python
import hmac, hashlib

def verify_signature(payload, signature, secret):
    expected = 'sha256=' + hmac.new(
        secret.encode(),
        payload,
        hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(signature, expected)
```

#### 3.2.5 Listar y Eliminar Webhooks

```bash
# Listar webhooks
curl https://localhost:8443/v1/webhooks

# Eliminar webhook
curl -X DELETE https://localhost:8443/v1/webhooks/wh-123
```

#### 3.2.6 Casos de Uso

| Uso | Ejemplo |
|-----|---------|
| CI/CD | Notificar cuando termina un build |
| Slack | Enviar mensajes a un canal |
| Logging | Archivar resultados en S3/Datadog |
| Monitoring | Alertar si una tarea falla |

---

### 3.3 Task Priority

Gentle Mesh soporta **prioridad de tareas** para ejecutar tareas importantes primero.

#### 3.3.1 Configurar Prioridad

```bash
# Tarea de alta prioridad (se ejecuta antes)
curl -X POST https://localhost:8443/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "agent": "worker",
    "task": "Deploy crítico",
    "priority": 100
  }'

# Tarea de baja prioridad (se ejecuta después)
curl -X POST https://localhost:8443/v1/tasks \
  -d '{"agent": "worker", "task": "Limpieza", "priority": -100}'
```

#### 3.3.2 Escala de Prioridad

| Valor | Significado |
|-------|-------------|
| 100 | Crítico (se ejecuta primero) |
| 1-99 | Alta prioridad |
| 0 | Normal (default) |
| -1 a -99 | Baja prioridad |
| -100 | Mínimo |

#### 3.3.3 Con Retry

Combina prioridad y retry para tareas importantes:

```bash
curl -d '{
  "agent": "worker",
  "task": "Deploy producción",
  "priority": 50,
  "max_retries": 3,
  "retry_delay_seconds": 60
}'
```

---

### 3.4 Checkpoint / Resume

Gentle Mesh soporta **checkpoint y resume** para tareas largas. El worker puede guardar progreso periódico y retomarlo si falla o si se desconecta.

#### 3.4.1 Guardar Checkpoint

```bash
# Guardar checkpoint durante ejecución
curl -X POST https://localhost:8443/v1/tasks/{task_id}/checkpoint \
  -H "Content-Type: application/json" \
  -d '{
    "step": 3,
    "total_steps": 10,
    "progress": "Compilando módulo auth...",
    "files_done": ["src/auth/login.go", "src/auth/logout.go"],
    "context": {"build_id": "abc123", "env": "production"}
  }'
```

#### 3.4.2 Obtener Checkpoint

```bash
# Recuperar último checkpoint
curl https://localhost:8443/v1/tasks/{task_id}/checkpoint
```

#### 3.4.3 Eventos de Checkpoint

Los suscriptores SSE reciben eventos `checkpoint`:

```json
{
  "type": "checkpoint",
  "id": 15,
  "payload": {
    "step": 3,
    "total_steps": 10,
    "progress": "Compilando módulo auth...",
    "files_done": ["src/auth/login.go"],
    "context": {"build_id": "abc123"},
    "last_updated": 1699999999
  }
}
```

#### 3.4.4 Casos de Uso

| Escenario | Solución |
|-----------|----------|
| Tarea larga | Guardar progreso cada N pasos |
| Worker desconectado | Resume con último checkpoint |
| Falla temporal | Reintentar desde checkpoint |
| Debug | Ver progreso en tiempo real |

---

### 3.5 Rate Limiting

Protege el coordinator de abuse con rate limiting configurable.

#### 3.5.1 Configurar Rate Limit

```bash
# 100 requests por minuto, burst de 20
gentle-mesh server -rate-limit 100 -rate-limit-window 1m -rate-limit-burst 20
```

#### 3.5.2 Parámetros

| Flag | Default | Descripción |
|------|---------|-------------|
| `-rate-limit` | 0 (disabled) | Requests por ventana |
| `-rate-limit-window` | 1m | Duración de la ventana |
| `-rate-limit-burst` | 10 | Tamaño máximo de ráfaga |

#### 3.5.3 Respuesta de Rate Limit

```json
{
  "error": "rate limit exceeded",
  "retry_after": "1m0s"
}
```

---

### 3.6 Automatic Retry

Gentle Mesh soporta **reintento automático** para tareas que fallan, útil para operaciones no determinísticas o redes inestables.

#### 3.6.1 Configurar Retry

```bash
# Enviar tarea con retry automático
curl -X POST https://localhost:8443/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "agent": "worker",
    "task": "Compilar y desplegar aplicación",
    "max_retries": 3,
    "retry_delay_seconds": 30
  }'
```

#### 3.6.2 Comportamiento

| Campo | Default | Descripción |
|-------|---------|-------------|
| `max_retries` | 0 (sin retry) | Número máximo de reintentos |
| `retry_delay_seconds` | 30 | Segundos entre intentos |

#### 3.6.3 Eventos de Retry

El servidor emite eventos `retry` cuando programa un reintento:

```json
{
  "type": "retry",
  "id": 5,
  "payload": {
    "retry_number": 1,
    "max_retries": 3,
    "retry_after_seconds": 30,
    "last_error": "connection timeout"
  }
}
```

#### 3.6.4 Ejemplo con gentle-mesh run

```bash
# Con max_retries=3 y retry_delay=60s
gentle-mesh run -task "mi tarea" -max-retries 3 -retry-delay 60
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
* 🤖 **[Guía de Evaluación Externa para LLMs](docs/EVALUATION_GUIDE_FOR_LLMS.md):** Prompt y metodología estructurada para someter este código a una revisión externa por parte de otro modelo o evaluador.
* 📋 **[Convenciones del Proyecto](AGENTS.md):** Reglas operativas, competencias y filosofía de desarrollo.

---

## 5. Built with Gentle-AI

<div align="center">

<a href="https://github.com/Gentleman-Programming/gentle-ai">
  <img width="220" src="https://raw.githubusercontent.com/Gentleman-Programming/gentle-ai/main/docs/assets/brand/built-with-gentle-ai.png" alt="Built with Gentle-AI" />
</a>

<p><sub>Construido y disciplinado con <strong><a href="https://github.com/Gentleman-Programming/gentle-ai">Gentle-AI™</a></strong> — Memory, Workflows & Evidence.</sub></p>

</div>

Gentle-Mesh es una PoC/RFC comunitaria propuesta para el ecosistema: no es un proyecto oficial de Gentle AI ni cuenta con su respaldo mientras no lo apruebe Alan Buscaglia. Gentle AI™ y Engram™ son marcas de Alan Buscaglia (ver su [TRADEMARKS.md](https://github.com/Gentleman-Programming/gentle-ai/blob/main/TRADEMARKS.md)).

Los cambios relevantes pasan por revisión RDD.

Este repositorio se desarrolla con ayuda de agentes de IA (flujo de Gentle-AI) bajo revisión y responsabilidad humana del mantenedor.

---

## ¿Qué es RFC-002?

Si **Gentle Mesh v1 (RFC-001)** es el *sistema circulatorio* (transporte HTTPS/SSE, persistencia SQLite WAL y semáforos Git), **RFC-002** es el *sistema cognitivo y contractual*.

Resuelve tres problemas críticos en la colaboración autónoma entre agentes:
1. **Contratos en lugar de strings libres:** Mediante `CognitiveTaskEnvelope`.
2. **Negociación previa de idoneidad:** Mediante `Pre-flight Handshake` de recursos (herramientas, GPU, repositorios).
3. **Liquidación verificable:** Mediante `Task Settlement` determinista, eliminando la falacia del "Green Checkbox".

---

## Documentación Canónica

* 🎯 **[Objetivos y No-Objetivos (Criterio de Decisión)](docs/rfcs/002-objectives-and-non-goals.md)**
* 📄 **[Especificación Técnica RFC-002](docs/rfcs/002-cognitive-agent-network.md)**
* 📋 **[Informe de Asesoría Técnica para Terceros](docs/architecture/informe-asesoria-red-cognitiva.md)**
* 🌐 **[Informe Interactivo HTML](docs/architecture/informe-asesoria-red-cognitiva.html)**
* 🏛️ **[Convenciones y Competencias de Desarrollo](AGENTS.md)**

---

## Recuperación de tareas: NODE_LOST y re-dispatch del emisor

Cuando un worker muere durante la ejecución de una tarea (validado por el E2E de recuperación de la Fase 2, con el runner real y pi ejecutando dentro del sandbox):

1. **El coordinador NO re-despacha automáticamente.** La tarea en vuelo queda `failed` con código `NODE_LOST` y el territorio deja de ser despachable: el registry marca al worker muerto `offline` a los 3× heartbeat-timeout (`PruneStaleNodes` no borra el nodo — un nodo `offline` nunca recibe work). La reintención es una decisión del emisor.
2. **El re-dispatch es responsabilidad del emisor y debe ser un contrato nuevo** (nuevo `EnvelopeID`). El `contract_id` del intento fallido ya lleva su liquidación: si el lease venció, el reaper de expiración emite el recibo `SETTLEMENT_TIMEOUT` de forma autónoma, y la idempotencia por `contract_id` (R1) impide liquidar un segundo intento del mismo contrato como `SETTLED_CLEAN`. Cadena final observada: `[SETTLEMENT_TIMEOUT(intento A), SETTLED_CLEAN(intento B)]` — sin recibos fantasma ni liquidaciones dobles.
3. **Residuos del intento muerto**: el sandbox bwrap muere con el worker (el kill del proceso derriba el árbol completo); quedan un worktree efímero y una branch `proposal/<taskID>` sin commit en el repo cache. No hay colisión (cada intento es dueño de su `proposal/<taskID>`), pero acumulan sin GC — tracked en issue aparte.

---

## Licencia

Código abierto bajo licencia MIT; ver [LICENSE](LICENSE).
