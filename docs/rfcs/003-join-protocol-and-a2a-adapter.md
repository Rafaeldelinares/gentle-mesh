# RFC-003 — Protocolo de unión y adaptador A2A: objetivos y no-objetivos

> Estado: propuesta · Aplica a: RFC-003 (Join Protocol + A2A Adapter) v1
> Este documento es el criterio de decisión del protocolo. Cualquier cambio a la especificación
> o a la implementación debe poder justificarse contra estos objetivos y no contradecir ningún no-objetivo.

> **Nota de lectura.** Este documento expresa los objetivos del protocolo y el criterio de decisión.
> El estado real de cada objetivo —con su test y su incidencia— vive en la sección 11, «Estado de
> conformidad». Que un objetivo figure en esta lista no significa que esté cumplido hoy: la tabla
> de conformidad se llena **PR a PR**, con su evidencia, y no al final.

> **Tracking.** El seguimiento de esta RFC (tareas, commits, conteos de tests) vive en
> [`odd/tasks/rfc-003-join-protocol.md`](../../odd/tasks/rfc-003-join-protocol.md), con la misma
> convención que RFC-002 y que el resto de los frentes del repo.

## 1. Propósito

Hoy el conocimiento de la malla vive **solo en el coordinador**: el estado de las tareas, los
territorios reservados y las colisiones se guardan en su almacenamiento local (SQLite más logs
JSONL), y un nodo que se une recibe la vista del coordinador a través de la API.

RFC-003 mueve ese conocimiento a un **log de eventos firmado y encadenado por hash**:

1. El log pasa a ser la **fuente de verdad** de la malla, y el coordinador pasa a ser **un lector
   más** del log, no una torre de control obligatoria.
2. Un agente que se une **nace situado**: recibe un *snapshot* verificable del estado y continúa
   desde un *stream* en vivo, en vez de arrancar ciego y preguntar.
3. Las declaraciones de territorio se **contrastan** contra la actividad observada (ficheros
   tocados, operaciones git) en lugar de aceptarse a ciegas.

En paralelo, RFC-003 añade un **adaptador A2A**: una Agent Card servida y firmada, para que un
worker de gentle-mesh sea descubrible y delegable desde un orquestador externo sin que ese
orquestador necesite conocer el protocolo interno de gentle-mesh.

Promesa pública del protocolo:

> *El conocimiento de la malla no vive en la cabeza de nadie: vive en un log firmado y encadenado
> que cualquiera puede leer, verificar y rederive desde cero.* (estado real en la sección 11)

## 2. Qué existe hoy (y por qué esto cambia)

| Hoy | Consecuencia | Pieza de RFC-003 que lo resuelve |
|------|---------------|-----------------------------------|
| El estado vive en el SQLite del coordinador (`pkg/server/store`), con logs JSONL por tarea | El coordinador es un punto único de fallo y de confianza | J6, J7 |
| Un nodo que se une consulta al coordinador y recibe su vista | Un nodo nuevo no sabe nada hasta que el coordinador responde; sin coordinador no hay red | J3, J4 |
| El territorio se declara y se bloquea en memoria del coordinador (`pkg/server/registry/lock.go`, aplicado por `pkg/server/http/scheduler.go`) | Una declaración es una promesa sin contraste | J5 |
| No hay descubrimiento fuera de la malla | Un orquestador externo no puede delegar en un worker | A1, A2 |

El antecedente más cercano ya está escrito: RFC-002 §4 N9 declara el «tablón gobernado» (entradas
firmadas y encadenadas, lectura por ámbito) como extensión futura bajo la RFC-004. **RFC-003 es esa
extensión**, con un alcance acotado y verificable.

## 3. Modelo de adversario (resumen)

El protocolo asume que **cualquier participante puede ser malicioso o estar comprometido**,
incluidos:

- un **actor** que emite eventos falsos, retroactively alters el log o intenta ocupar un territorio
  que otro actor ocupa de verdad;
- un **atacante con acceso al almacenamiento** que borra, reordena o inserta eventos;
- un **orquestador A2A externo** que consume la Agent Card y la trata como si el worker cumpliera
  lo declarado;
- dos **escritores legítimos que escriben a la vez** en el mismo log y producen una cadena
  divergente sin que ninguno sea malicioso (el riesgo técnico central de §7).

El detalle completo está en `docs/architecture/THREAT-MODEL.md`.

## 4. Objetivos

Cada objetivo tiene un criterio medible. «Cumplido» significa que existe un test automatizado que
lo demuestra.

### 4.1 Unión y conocimiento de la malla (J)

| ID | Objetivo | Criterio medible |
|----|----------|------------------|
| J1 | **Evento firmado y encadenado** | Todo evento del log lleva `id`, `tipo`, `actor`, `payload`, el hash del evento anterior y una firma Ed25519 sobre su forma canónica JCS. Un evento alterado o con un enlace roto no verifica. |
| J2 | **Serialización sin colisión** | N escrituras concurrentes al mismo log producen una cadena **lineal**: ningún par de eventos comparte el mismo hash de predecesor y todo evento referencia al hash real de su anterior (test con N ≥ 16 bajo `-race`). |
| J3 | **Snapshot verificable** | El endpoint de snapshot devuelve el estado derivado del log junto con un digest; el digest es recomputable por el cliente a partir del log y una discrepancia se detecta. |
| J4 | **Stream en vivo** | Un cliente suscrito recibe los eventos posteriores a su snapshot, en el orden de la cadena, sin huecos ni duplicados, y puede reanudar desde un cursor. |
| J5 | **Territorio contrastado** | Una declaración de territorio se contrasta contra la actividad observada (ficheros tocados, operaciones git) y la discrepancia se reporta. El contraste **no reemplaza** la declaración: la evidencia va al log. |
| J6 | **El log es la fuente de verdad** | El estado completo de la malla se puede reconstruir leyendo el log desde cero, sin consultar ningún estado derivado. Un test reconstruye el estado y lo compara con el del coordinador. |
| J7 | **El coordinador es un lector más** | Un nodo puede unirse y operar leyendo el log y su stream, sin que el coordinador esté disponible ni sea su punto de必经ada. |

### 4.2 Adaptador A2A (A)

| ID | Objetivo | Criterio medible |
|----|----------|------------------|
| A1 | **Agent Card servida** | El endpoint `/.well-known/agent-card.json` sirve una Agent Card conforme al perfil A2A, derivada de las capacidades reales del nodo. |
| A2 | **Agent Card firmada** | La Agent Card se firma como JWS sobre su forma canónica JCS, con las primitivas Ed25519 ya existentes en `pkg/envelope` / `pkg/signing` / `pkg/receipt`. Un cliente verifica la firma con la clave publicada y rechaza una card alterada. |
| A3 | **A2A no es transporte interno** | El adaptador expone la card hacia fuera; el tráfico entre nodos de gentle-mesh sigue hablando el protocolo propio. Un test demuestra que la malla no depende de mensajes A2A para funcionar. |

### 4.3 Modelo de capacidades en dos capas (C) — **documentar, no implementar**

| ID | Objetivo | Criterio medible |
|----|----------|------------------|
| C1 | **Dos capas separadas** | El documento distingue la capa **AGENTE** (modelo, skills, presupuestos de ejecución) de la capa **NODO/WORKER** (hardware, sandbox, toolchains, red, identidad). |
| C2 | **Tier 1: verificable al instante** | Lo verificable sin historial se responde en el momento — vía el handshake de pre-flight ya existente (`POST /preflight`, RFC-002 A2) — y con su causa real cuando falta algo. |
| C3 | **Tier 2: confianza por observación** | Lo que no se puede verificar al instante (calidad de un resultado, estabilidad de un nodo) se modela como confianza que crece con la observación, no como un hecho. |
| C4 | **Fallo con causa, no genérico** | Una carencia del nodo (una herramienta que no está instalada, un sandbox que no arranca) se reporta con su causa concreta, en vez de perderse como un error genérico. |

> C1–C4 son **diseño documentado** en esta RFC: dejan sitio y fijan el vocabulario, pero **no**
> se implementan en esta tanda. Su estado en la tabla de conformidad es «documentado», no «cumplido».

## 5. No-objetivos

Lo que RFC-003 **no** pretende resolver. Declararlo evita prometer lo que no se cumple.

| ID | No-objetivo | Quién lo cubre |
|----|-------------|----------------|
| N1 | **Nada de CRDTs.** | Revisión humana, aserciones de liquidación, y el hecho de que el log es de eventos de alto nivel, no de estado convergente. |
| N2 | **Nada de consenso distribuido tipo Raft/Paxos.** | La derivación de estado y la serialización local; el modelo de un único escritor vigente se documenta en §6. |
| N3 | **Nada de event-sourcing "completo" que capture cada micro-cambio.** Esto es un log de eventos de alto nivel (tarea creada, territorio declarado, colisión detectada, tarea terminada), no un sistema de reconstrucción universal. | Aserciones de liquidación y estado derivado por proyección, no por replay universal. |
| N4 | **Nada de A2A como transporte interno.** El adaptador es una fachada hacia fuera, no un cambio de protocolo interno. | RFC-001 y el protocolo propio de la malla. |
| N5 | **Nada de resolver la disponibilidad del log.** Si el log no está disponible, los nodos que dependen de él no pueden avanzar; esta RFC no replica ni replica-el log. | La restricción de diseño de §6 y, más adelante, un debate explícito sobre réplicas. |
| N6 | **Nada de cambiar el transporte de la malla.** HTTPS/SSE y mTLS siguen siendo el transporte; RFC-003 cambia dónde vive el conocimiento, no cómo viaja. | RFC-001, RFC-002. |

## 6. Restricción de diseño: una sola copia, por ahora

Esta sección existe para que la decisión sea explícita y para no cerrarle la puerta a réplicas sin
haberlo decidido.

**Lo que sí está en alcance:**

- El log es la **única fuente de verdad** del conocimiento de la malla.
- Cualquier nodo debe poder **reconstruir el estado completo leyéndolo desde cero** (J6).

**Lo que NO se resuelve en esta RFC:**

- Qué pasa si existe **más de una copia del log** y hace falta ponerse de acuerdo sobre cuál es la
  autoritativa. Eso es consenso y replicación, y queda **descartado como sobreingeniería** para el
  tamaño actual de la red.

**La condición que sí se impone:** el formato del evento y el esquema de encadenado por hash
**no deben asumir implícitamente que solo existe un coordinador posible**. Si una decisión de esta
RFC hiciera más caro añadir réplicas más adelante, queda **anotada explícitamente** aquí, aunque no
se resuelva ahora.

| Decisión de diseño | Por qué encarece réplicas más adelante | Anotación en el diseño |
|--------------------|-------------------------------------------|--------------------------|
| El escritor lee la cabeza y luego anexa | Con dos copias, cada una tiene su cabeza: el mismo evento nace con predecesores distintos | El enlace por hash es la autoridad; el evento **no** lleva un número de secuencia global que un segundo escritor tendría que coordinar. |
| El hash del evento anterior es parte de lo firmado | Obliga a que dos copias del log compartan historia, pero es lo que hace detectable una bifurcación | Se acepta: es el mecanismo de integridad que da valor al log. Una futura réplica tendría que reconciliar por hash, no por índice. |
| El actor del evento es quien lo firma | Un coordinador que reescribe eventos en nombre de otros rompería la atribución | La identidad del firmante es la del actor; no hay un «autor de segundo» que pueda reescribir historia. |
| El snapshot lleva un digest del log | Dos copias con el mismo contenido pero distinto orden darían digests distintos | El digest se define sobre la cadena de hashes, no sobre el estado derivado, para que sea comparable entre copias. |
| La marca de tiempo es informativa | Ordenar por reloj reintroduce la dependencia del reloj ajeno (RFC-002 R6) | El orden de la malla es el de la cadena; el tiempo es metadato, no criterio de orden. |

## 7. El riesgo técnico central: la carrera de escritura

**Este es el primer riesgo que hay que cerrar, antes de diseñar nada más del protocolo de unión.**

Si varios actores anexan eventos concurrentemente al mismo log encadenado por hash, hace falta un
punto de serialización o una restricción de unicidad sobre el hash siguiente. Sin uno de los dos,
dos escritores que leen la misma cabeza anexan dos eventos **con el mismo predecesor**: la cadena
se bifurca, el enlace se rompe y los dos autores Firman ramas incompatibles.

El repositorio ya tiene el antecedente más cercano: la cadena de recibos de RFC-002 se serializa
con `BEGIN IMMEDIATE` **y** con un índice único por secuencia
(`pkg/receipt/chain.go`: `idx_receipts_pair_seq`), con su test de concurrencia en
`pkg/receipt/chain_concurrency_test.go`.

**Dos mecanismos candidatos, decisión pendiente (humana):**

| Mecanismo | Cómo funciona | A favor | En contra |
|-----------|--------------|---------|----------|
| **A. Restricción de unicidad sobre el predecesor** | `UNIQUE(prev_hash)`: dos anexos concurrentes con el mismo predecesor, uno viola la restricción y **reintenta** leyendo la cabeza nueva | No serializa escrituras: el cuello de botella es solo el conflicto. Es la misma forma que ya usa `idx_receipts_pair_seq`. Detecta la divergencia en la base, no en memoria | Obliga a un camino de reintento (hay que decidir si reintenta el mismo evento con otro predecesor o si falla); el error de restricción tiene que distinguirse de otros |
| **B. Mutex / transacción de escritura serializada** | `BEGIN IMMEDIATE` alrededor de leer-cabeza-y-anexar (o un mutex en el proceso) | Una sola operación atómica: sin reintentos, sin errores que interpretar. Es el patrón ya presente en la cadena de recibos | Serializa a los escritores (con muchos nodos, un mutex de proceso no sirve entre procesos: hace falta el `BEGIN IMMEDIATE` de SQLite, que además solo serializa dentro de la misma base); no escala a varias copias sin cambiar de estrategia |

**Lo que este RFC deja escrito, sin decidirlo todavía:** la elección entre A y B cambia la
superficie del formato (si el evento lleva o no un número de secuencia, y si el append puede fallar
por conflicto), así que se decide **antes** de fijar el formato del evento, no después.

## 8. Perfil mínimo conforme

Un nodo puede declararse *gentle-mesh RFC-003 conforme* si implementa, como mínimo:

- J1, J2, J3, J4, J5, J6
- A1, A2
- C1 (documentado)

El resto son **extensiones**: J7 (unión sin coordinador como punto de必经ada), A3 (prueba de que A2A
no es transporte interno), C2–C4 (modelo de capacidades implementado, con sus dos niveles).

## 9. Orden de construcción

Cada pieza es un PR propio, en su rama, y **no se empieza la siguiente hasta que la anterior está
en `main`**.

1. **Formato del evento firmado + test de la carrera** (§7). El test de concurrencia es lo primero:
   se escribe en rojo, se demuestra la divergencia con la evidencia cruda de la corrida, y recién
   entonces se elige el mecanismo de serialización y se cierra en verde.
2. **Cada cambio de estado como evento**; el SQLite queda como **derivado** del log, no al revés.
3. **Endpoint de snapshot + stream** para agentes que se unen.
4. **Verificación oportunista de territorio** (ficheros tocados, operaciones git).
5. **Adaptador A2A**: Agent Card + firma JWS sobre JCS.
6. **Documentar** (no implementar) el modelo de capacidades en dos capas.

## 10. Principios de decisión

Cuando dos objetivos entren en conflicto, se aplica este orden:

1. **Integridad** sobre disponibilidad: ante duda, rechazar la escritura y dejar constancia, no anexar
   un evento que no se puede verificar.
2. **Corrección** sobreteesura: el log refleja lo que pasó, no lo que se esperaba que pasara.
3. **Simplicidad** sobre generalidad: una capa pequeña y verificable antes que una flexible y opaca.
4. **Estándares** sobre invención propia (JCS RFC 8785, Agent Card de A2A, Ed25519).
5. **Evidencia** sobre confianza: si no hay test que lo demuestre, el objetivo no está cumplido.

## 11. Estado de conformidad

Se verifica sobre `main`, commit a commit. «Cumplido» significa que existe un test automatizado
que demuestra el criterio medible. «Documentado» significa que el diseño está escrito y es
referenciable, pero que **no hay implementación**.

| Objetivo | Perfil mínimo | Qué significa | Estado | Test relacionado | Qué falta | Incidencia |
|----------|---------------|---------------|--------|------------------------|-----------|------------|
| J1 | sí | Formato del evento firmado: id, tipo, actor, payload, hash del evento anterior y firma Ed25519 sobre la forma canónica JCS. | sin código | — | Formato y firma | [#166](https://github.com/Rafaeldelinares/gentle-mesh/issues/166) |
| J2 | sí | Escrituras concurrentes al mismo log producen una cadena lineal, sin bifurcaciones ni predecesores compartidos. | cumplido | `TestJoinLog_ConcurrentAppendsProduceOneLinearChain`, `TestJoinLog_GenesisUniqueConstraint` | — | [#166](https://github.com/Rafaeldelinares/gentle-mesh/issues/166) |
| J3 | sí | Snapshot con digest verificable por el cliente a partir del log. | sin código | — | Endpoint de snapshot | — |
| J4 | sí | Stream en vivo sin huecos ni duplicados, reanudable por cursor. | sin código | — | Endpoint de stream | — |
| J5 | sí | La declaración de territorio se contrasta contra la actividad observada; la evidencia va al log. | sin código | — | Contraste de territorio | — |
| J6 | sí | El estado completo se reconstruye leyendo el log desde cero. | sin código | — | Proyección y test de equivalencia | — |
| J7 | no | El coordinador deja de ser punto de必经ada para unirse. | sin código | — | Extensión futura | — |
| A1 | sí | Agent Card conforme al perfil A2A servida en `/.well-known/agent-card.json`. | sin código | — | Endpoint de la card | — |
| A2 | sí | Card firmada como JWS sobre JCS con las primitivas Ed25519 existentes. | sin código | — | Firma y verificación | — |
| A3 | no | El tráfico interno no depende de A2A. | sin código | — | Extensión futura | — |
| C1 | sí | Las dos capas (agente / nodo-worker) están documentadas y nombradas. | documentado | este documento, §4.3 | — | — |
| C2 | no | Tier 1: lo verificable al instante se responde en el momento, con su causa real. | documentado | este documento, §4.3 | Implementación fuera de esta RFC | — |
| C3 | no | Tier 2: la confianza por observación se modela aparte del hecho verificable. | documentado | este documento, §4.3 | Implementación fuera de esta RFC | — |
| C4 | no | Una carencia del nodo se reporta con su causa concreta. | documentado | este documento, §4.3 | Implementación fuera de esta RFC | — |

### Método y límites de la comprobación

La tabla se llena con la evidencia de cada PR (test y salida cruda), no con narrativa. Los tests
que requieren Docker se leen, pero no se ejecutan en la comprobación de conformidad; el estado real
del CI en GitHub se consulta aparte, cuando se merges.

---

## 12. Lo que este RFC no decide

- **Réplicas del log y consenso.** Descartado (§6). Si algún día hace falta, se abre un RFC aparte.
- **A2A como transporte interno.** Descartado (N4).
- **La implementación del modelo de capacidades.** Solo el diseño (C1–C4).
- **El formato definitivo del evento.** Se fija después de elegir el mecanismo de serialización (§7),
  porque la elección constrain la forma.
- **La limpieza de worktrees y ramas de propuesta huérfanas.** Es [#120](https://github.com/Rafaeldelinares/gentle-mesh/issues/120), no parte de este RFC.
