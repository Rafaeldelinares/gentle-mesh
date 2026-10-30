# Historia y horizonte de gentle-mesh

## Qué es gentle-mesh (y qué no es)

La función central de gentle-mesh es evitar que dos agentes de IA toquen el mismo código a la vez: consciencia situacional y prevención de colisiones entre agentes que trabajan sobre el mismo repositorio (o varios). No es un planificador de trabajo — dentro de esa función sí elige el nodo entre los compatibles por capacidad y reparto de carga.

La visión a más largo plazo: que un agente (o persona) proponga tareas y sea la propia red la que las coordine entre los agentes disponibles, sin que ninguno haga de coordinador central obligatorio.

**Posicionamiento**: gentle-mesh no compite con A2A (Google/Linux Foundation, ~22k stars, 150+ organizaciones) como protocolo general de comunicación agente-agente. Se posiciona como la capa de coordinación y confianza para agentes de código que trabajan sobre un mismo repositorio — un hueco que A2A no resuelve y que la literatura de seguridad de la industria confirma como abierto (ver más abajo). Colabora con el ecosistema Gentleman Programming (gentle-shell como motor de ejecución) sin integrarse dentro de él.

## Por qué importa: validación externa del problema

Un paper de modelado de amenazas comparando MCP/A2A/Agora/ANP afirma textualmente que *"task collisions on shared resources are also not addressed directly for A2A"* — exactamente el problema central que ataca gentle-mesh. El mismo análisis confirma que la identidad en A2A es autodeclarada sin aplicación de unicidad, y que la integridad de artefactos no es forzada por el protocolo, dejándolo enteramente al implementador.

Un documento público de la comunidad gentle-ai (oct. 2026) describe un sistema de coordinación de agentes hecho a mano con scripts para proyectos reales, usando Gentle Shell como runtime de ejecución — confirma adopción real de Gentle Shell por terceros y documenta en vivo los mismos síntomas que gentle-mesh ataca (mensajes mal enrutados, bypasses de jerarquía).

Un análisis de seguridad externo independiente validó la cola territorial automática como la idea diferenciadora correcta, y señaló como hallazgo nuevo: el sistema nunca contrasta a posteriori la superficie de edición declarada por una tarea contra los ficheros realmente tocados — queda como trabajo futuro de verificación territorial a posteriori.

## Una lección de proceso, no solo de código

El hallazgo más valioso del ciclo de conformidad con el perfil mínimo de la RFC-002 no fue técnico sino de proceso: pedir una revisión externa independiente, sin adelantarle nada, específicamente para contrastarla contra la autoevaluación del propio trabajo de cierre, destapó que un issue de conformidad se había cerrado como completo habiendo resuelto solo una fracción de los requisitos reales, con al menos un test de seguridad debilitado silenciosamente en el camino.

Ninguno de esos hallazgos tenía superficie de ataque activa en producción en ese momento, pero todos eran críticos a nivel de protocolo. La respuesta fue un cierre sistemático de los 12 requisitos del perfil mínimo con evidencia RED/GREEN cruda exigida en cada PR, no narrativa — ver la tabla de conformidad en el README y en docs/rfcs/002-objectives-and-non-goals.md §7.

**El principio**: una autoevaluación de un agente sobre su propio trabajo de cierre no es suficiente evidencia por sí sola para un hito crítico. Una segunda mirada externa, sin contaminar con lo que ya se cree cerrado, es la que lo destapa.

## El horizonte

Con el perfil mínimo de la RFC-002 cerrado de verdad (12/12), el proyecto abre dos frentes concretos, de corto plazo:

**Validación con agentes reales**: un worker físico ya está en marcha, probado con una tarea sintética no trivial (una caché LRU+TTL concurrente) con verificación cruzada independiente fuera del sandbox del agente. El siguiente paso es trabajo real de producción, no solo pruebas de validación.

**RFC-003 — protocolo de unión + adaptador A2A** (en diseño, no implementado todavía): dos ideas que comparten infraestructura y se fusionan en una sola RFC:

- *Nacer situado*: que el conocimiento de la malla viva en un log de eventos firmado y encadenado por hash, con snapshot + suscripción para que un agente nuevo se una ya situado al estado de la red, y con declaraciones de territorio verificadas oportunísticamente contra actividad observada (ficheros tocados, operaciones git) en vez de confiar a ciegas en lo declarado.
- *Interoperabilidad A2A*: exponer Agent Cards A2A-compatibles hacia fuera, para que un worker de gentle-mesh sea descubrible y delegable desde un orquestador externo sin que ese orquestador necesite conocer el protocolo interno de gentle-mesh.

Como candidato de alcance adicional: un modelo de capacidades en dos capas separadas — la capa AGENTE (modelo, skills, presupuestos de ejecución) y la capa NODO/WORKER (hardware, sandbox, toolchains, red, identidad) — para que las fallas de un worker por carencia de algo (una herramienta no instalada, por ejemplo) se reporten con su causa real en vez de perderse como un error genérico.

Ninguno de estos dos frentes es un plazo ni un compromiso — son los próximos pasos razonables dado lo que ya está construido, no una promesa de cuándo estará terminado.

## Un ciclo de auditoría externa, con la misma disciplina

En octubre de 2026, una auditoría independiente sobre el repositorio encontró 50 hallazgos de seguridad y arquitectura. Ninguno se aceptó sin verificación propia contra el código real: de los 12 hallazgos más graves, 9 se confirmaron (varios con un mecanismo distinto al descrito originalmente), 2 no se pudieron reproducir, y 1 tenía la severidad sobrestimada.

El cierre se hizo con cinco pasadas adversariales por frente, reproduciendo cada fallo antes de escribir el arreglo. En uno de los casos, el propio proceso de cierre introdujo una condición de carrera nueva — y la misma verificación que cerró el hallazgo original la encontró y la corrigió en la misma sesión, antes de darla por buena.

El resultado: identidad de nodos con revocación de certificados respaldada por el propio almacenamiento persistente (no por una copia en memoria que podía desincronizarse), aislamiento de ejecución reforzado contra fugas del sandbox, y una primera validación real, con dos máquinas distintas — una como coordinador, otra como ejecutor — completando una tarea de principio a fin con un agente real dentro del entorno aislado. El binario desplegado en ambas máquinas se verificó como idéntico, byte a byte, antes de confiar en el resultado.

## El ciclo RFC-003: Nacer situado, el adaptador A2A y la disciplina del arnés

Entre el 29 y el 30 de octubre de 2026 se completó íntegramente la RFC-003 en `main`. El objetivo era resolver el problema de la consciencia situacional sin caer en la sobreingeniería de consenso pesado (Raft/Paxos) ni en la complacencia de estados volátiles:

1. **La carrera de escritura resuelta por la base de datos (Opción C)**: encadenar eventos por SHA-256 (`prev_hash`) bajo concurrencia expone el riesgo de bifurcación si dos escritores leen la misma cabeza. Se demostró en RED y se resolvió combinando `BEGIN IMMEDIATE` en una conexión dedicada con una restricción dura `UNIQUE(prev_hash)` en SQLite (PR [#168](https://github.com/Rafaeldelinares/gentle-mesh/pull/168)), resistiendo 16 escritores concurrentes sin forks ni dobles génesis.
2. **Replay determinista sin reloj de pared**: el estado proyectado de la malla se reconstruye desde cero leyendo el log de eventos (J6). Para que el replay sea 100% determinista, las expiraciones de leases se registran como eventos explícitos (`reason: "lease_expired"`), desterrando cualquier llamada a `time.Since` o dependencia del reloj del host en la reconstrucción (PR [#170](https://github.com/Rafaeldelinares/gentle-mesh/pull/170)).
3. **Arranque situado (Snapshot + Stream SSE)**: los nuevos nodos descargan un snapshot verificable con doble digest (posición inmutable en la cadena y digest canónico JCS del estado semántico, J3) y se suscriben a un stream SSE con cursor monótono `id: <seq>:<hash>` que detecta divergencias con HTTP 409 Conflict (J4, PR [#172](https://github.com/Rafaeldelinares/gentle-mesh/pull/172)).
4. **Verificación territorial oportunista**: el arnés fuera del sandbox `bwrap` contrasta los archivos tocados por `git diff BaseRef..HEAD` contra las superficies declaradas, emitiendo evidencia aditiva `territory.verified` en el log y endureciendo la invocación de Git contra inyecciones de argumentos (J5, PR [#175](https://github.com/Rafaeldelinares/gentle-mesh/pull/175)).

### La lección del Punto 4.5: El arnés contra la promesa aspiracional

Durante el diseño del adaptador A2A (§4.2.1), se redactó la regla de que *"el binding entre node_id y su clave pública Ed25519 vive en el log de eventos"*. Cuando el usuario cuestionó si ese binding estaba realmente respaldado por el código de producción o si era solo una aspiración escrita, la inspección reveló la verdad cruda: **no existía ningún endpoint en producción que emitiera `node.joined`, y `NodeJoinedPayload` ni siquiera almacenaba la clave pública**.

En lugar de parchar el texto o darlo por cumplido, **se pausó la RFC-003 y se abrió un hito de implementación específico (Punto 4.5)**:
- Se implementó `POST /v1/mesh/join` atado estrictamente a la identidad mTLS del cliente (`VerifiedChains[0][0].Subject.CommonName == req.NodeID`, patrón S1).
- Se implementó validación canónica de claves Ed25519 en curva Edwards (RFC 8032 §5.1.3, $y < 2^{255}-19$, rechazando claves todo-ceros y no canónicas).
- Se garantizó atomicidad estricta invirtiendo el orden: el evento se persiste en el JoinLog y se proyecta en SQLite *antes* de admitir al nodo en la memoria del servidor.
- Se verificó con tests en RED demostrando el rechazo de suplantación y de claves no canónicas antes de escribir el GREEN (PR [#181](https://github.com/Rafaeldelinares/gentle-mesh/pull/181)).

Solo con esa garantía criptográfica real en `main` se procedió al adaptador A2A: la Agent Card servida en `GET /.well-known/agent-card.json` con sobre compacto JWS/JCS (PR [#183](https://github.com/Rafaeldelinares/gentle-mesh/pull/183)), documentando honestamente que la raíz describe al host local y que `?node_id=` es una extensión de catálogo de la malla.

El ciclo cerró con el saneamiento de la fiabilidad de CI (PR [#184](https://github.com/Rafaeldelinares/gentle-mesh/pull/184), eliminando esperas fijas de reloj por sincronización determinista por condición) y la formalización documental del modelo de capacidades en dos capas (§4.3, PR [#185](https://github.com/Rafaeldelinares/gentle-mesh/pull/185)).
