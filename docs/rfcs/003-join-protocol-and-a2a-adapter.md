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
| J3 | **Snapshot verificable** | El endpoint de snapshot devuelve una tupla con la posición del log (`head_event_seq`, `head_event_hash`), el estado proyectado `state` (`MeshState`) y el `state_digest` (SHA-256 sobre la forma canónica RFC 8785 de `state`). El cliente puede recomputar el digest localmente a partir de su propio replay y detectar cualquier discrepancia byte a byte. |
| J4 | **Stream en vivo y reanudable** | Un cliente suscrito a `/v1/mesh/events` (SSE) recibe los eventos posteriores a su cursor (`id: <seq>`), en orden estricto de cadena, sin huecos ni duplicados. Si se desconecta, reanuda enviando el header estándar `Last-Event-ID: <seq>` o `?since_seq=<seq>` (con validación opcional de `?since_hash=<hash>`), sin solicitar un snapshot nuevo. |
| J5 | **Territorio contrastado** | Una declaración de territorio se contrasta contra la actividad observada (ficheros tocados, operaciones git) y la discrepancia se reporta. El contraste **no reemplaza** la declaración: la evidencia va al log. |
| J6 | **El log es la fuente de verdad** | El estado completo de la malla se puede reconstruir leyendo el log desde cero, sin consultar ningún estado derivado. Toda mutación de estado —incluyendo la expiración de leases por el reaper autónomo (RFC-002 R2)— se emite como evento explícito en el log (`territory.released` con `reason: "lease_expired"`), **nunca inferida del reloj de pared durante el replay**. Un test reconstruye el estado y lo compara con el del coordinador. |
| J7 | **El coordinador es un lector más** | Un nodo puede unirse y operar leyendo el log y su stream, sin que el coordinador esté disponible ni sea su punto de必经ada. |

### 4.1.1 Análisis técnico: Semántica de reanudación del stream (Last-Event-ID vs. anclaje de hash)

En el SSE existente de gentle-mesh (`/v1/tasks/{id}/events`), los eventos son de progreso de ejecución
(`thought`, `token`, `status`) identificados por un entero monótono (`1, 2, 3...`). La reconexión con
`Last-Event-ID: N` solo pide «lo que vino después de N» sobre un flujo efímero.

Para el stream del join log (J4), la semántica es **distinta y más exigente**:
1. **Los eventos están encadenados por hash**: cada evento referencia inmutablemente el hash del anterior (`prev_hash`).
2. **El riesgo de divergencia silenciosa**: si un agente que se desconectó en `seq: 142` reconecta pidiendo
   solo `Last-Event-ID: 142`, y el servidor sufrió un restore de backup, una partición o una reordenación,
   el evento 143 del servidor podría tener un `prev_hash` que no coincide con el hash del evento 142 que
   el agente ya persistió. Su proyección local se rompería de inmediato al validar la cadena (`VerifyChain`).
3. **El diseño adoptado**:
   - El estándar W3C SSE define `id:` como una cadena opaca de texto libre, no como un entero forzoso.
   - En el stream del join log, el servidor emite `id: <seq>:<hash>` (ej. `142:3f75573d...`).
   - Al reconectar, el cliente envía `Last-Event-ID: 142:3f75573d...` (o mediante query params equivalentes
     `?since_seq=142&since_hash=3f75573d...`).
   - El servidor verifica que el evento local en `seq: 142` posea exactamente ese hash:
     - **Si coincide**: transmite los eventos con `seq > 142` en orden estricto.
     - **Si difiere**: rechaza la conexión con `HTTP 409 Conflict` (Divergencia de cadena detectada), forzando
       al agente a solicitar un nuevo snapshot en vez de admitir una bifurcación de datos.

### 4.1.2 Análisis técnico: Frontera de confianza en la verificación de territorio (J5)

La garantía de J5 en esta RFC es: **arnés de Go del host (fuera del sandbox del agente) vs. declaración del agente**.

1. **Nivel de confianza real hoy**:
   - El agente LLM opera enjaulado en un sandbox `bwrap` sobre un worktree efímero.
   - Quien recolecta la actividad observada (`git diff --name-only BaseRef..HEAD`) es el **arnés nativo en Go** (`GentleShellRunner`), que corre en el host fuera del sandbox.
   - El contraste evalúa si los archivos efectivamente modificados en Git respetan las superficies declaradas (`edit_surfaces`). El nivel de confianza real hoy es: *«el agente no mintió, verificado por el mismo binario reproducible que corre en el host del worker»*.
2. **Lo que queda explícitamente fuera de este RFC (Zero-Trust inter-nodo)**:
   - No se implementa la verificación de Git independiente desde el coordinador (donde el coordinador haría fetch de los objetos de `proposal/<task_id>` para correr `git diff` en su propio host).
   - En la arquitectura actual de gentle-mesh, el coordinador no tiene acceso al filesystem ni al `RepoCache` local del worker. Construir un transporte de objetos Git entre worker y coordinador excede el alcance de este RFC.
   - **Beneficio colateral identificado**: Esta transferencia de objetos Git resolvería de paso el soporte de `gentle-mesh apply` en entornos distribuidos multi-nodo (donde hoy `apply` requiere acceso local al `RepoCache` del worker). Queda documentado como candidato para una incidencia de infraestructura separada.
3. **Superficie de la observación (límite conocido y explícito)**:
   - La actividad observada es el diff **commiteado** (`BaseRef..HEAD`) de la rama `proposal/<task_id>`, calculado por el arnés de Go con un `git diff --name-only` endurecido vía `pkg/gitsafe` (sin `diff.external`, sin `textconv`, sin drivers de filtro).
   - **La revisión base se valida y se resuelve antes de entrar al argv**: se exige un nombre de ref plano (sin `-` inicial, sin `..`, solo caracteres de ref) y se resuelve a un SHA completo de commit, de modo que el operando del diff es un string hexadecimal que git no puede interpretar como opción. `pkg/gitsafe` además rechaza cualquier operando de rango que empiece por `-` (la forma `--output=/ruta..HEAD`, que haría que git escribiera el diff a un fichero). Esto cierra la clase de inyección de argumentos de #35 en esta ruta.
   - En consecuencia, un fichero que el agente haya creado o modificado pero **nunca commiteado** no aparece en la observación. El contrato de completitud del runner ya exige working tree limpio, pero `git status --porcelain` **tampoco** lista ficheros ignorados: un fichero con extensión ignorada por el `.gitignore` del host (por ejemplo `*.key`) puede quedar fuera de la observación por completo.
   - Esto no se resuelve en esta RFC (haría falta una enumeración adicional de ficheros no ignorados/ignorados modificados, con su propio coste). Queda anotado como límite conocido del nivel de garantía actual y abierto como incidencia de seguridad propia ([#174](https://github.com/Rafaeldelinares/gentle-mesh/issues/174)).

### 4.2 Adaptador A2A (A)

| ID | Objetivo | Criterio medible |
|----|----------|------------------|
| A1 | **Agent Card servida** | El endpoint `/.well-known/agent-card.json` sirve una Agent Card conforme al perfil A2A, derivada de las capacidades reales del nodo. |
| A2 | **Agent Card firmada** | La Agent Card se firma como JWS sobre su forma canónica JCS, con las primitivas Ed25519 ya existentes en `pkg/envelope` / `pkg/signing` / `pkg/receipt`. Un cliente verifica la firma con la clave publicada y rechaza una card alterada. |
| A3 | **A2A no es transporte interno** | El adaptador expone la card hacia fuera; el tráfico entre nodos de gentle-mesh sigue hablando el protocolo propio. Un test demuestra que la malla no depende de mensajes A2A para funcionar. |

### 4.2.1 Diseño del adaptador A2A: la Agent Card y quién la firma

#### 4.2.1.1 Forma de la card

La card se sirve en `GET /.well-known/agent-card.json`. El **cuerpo** es un objeto canónico (JCS, RFC 8785) y la **firma** va en un sobre contiguo, de forma JWS compacta:

```json
{
  "kind": "gentle-mesh.agent-card/v1",
  "subject": {
    "node_id": "w-dev-2",
    "role": "worker",
    "public_key": "<base64url de la clave Ed25519 del sujeto>"
  },
  "name": "w-dev-2",
  "description": "Nodo worker de la malla gentle-mesh",
  "url": "https://192.168.1.26:8631",
  "version": "1.0.0",
  "capabilities": {
    "streaming": true,
    "push_notifications": false,
    "state_transition_history": true
  },
  "skills": [
    { "id": "go-build", "name": "Compilar Go", "tags": ["go", "build"] }
  ],
  "default_input_modes": ["text/plain"],
  "default_output_modes": ["text/plain", "application/json"],
  "mesh": {
    "log_head_seq": 142,
    "log_head_hash": "3f75573d...",
    "state_digest": "a6be93ab..."
  },
  "issued_at": "2026-10-30T00:00:00Z",
  "expires_at": "2026-11-30T00:00:00Z"
}
```

Sobre de firma (JWS compacto sobre bytes JCS):

```json
{
  "protected": { "alg": "EdDSA", "typ": "JOSE", "kid": "w-dev-2" },
  "payload": "<base64url(JCS(card))>",
  "signature": "<base64url(Ed25519) sobre ASCII(base64url(JCS(protected)) || '.' || base64url(JCS(payload)))>"
}
```

Dos propiedades de este formato que hay que preservar:

- **Determinismo**: la firma se calcula sobre los bytes JCS, así que el mismo contenido lógico produce siempre la misma firma (misma clave, mismo instante). Un verificador no necesita re-serializar: decodifica `payload` y recalcula.
- **Vinculación con el estado verificable**: el bloque `mesh` ata la card a un punto exacto del log (`log_head_seq`, `log_head_hash`) y al digest del estado proyectado (`state_digest`, J3). Un consumidor externo puede comprobar que la card no describe un estado viejo ni alterado, sin confiar en la palabra del coordinador.

#### 4.2.1.2 La decisión: quién firma, y qué identidad se expone hacia afuera

La card se publica para **consumo externo**, así que «quién firma» es exactamente «qué identidad exponemos hacia afuera». Hay tres designs coherentes:

| | **Nodo firma (sujeto)** | **Coordinador firma (representante)** |
|---|---|---|
| Qué se expone | La identidad del **nodo individual**, encadenada al CA de la malla | La identidad del **coordinador** (o de «la malla») |
| ¿Puede el coordinador falsear capacidades? | **No**: alterar la card rompe la firma | **Sí**: firma sobre lo que él afirma de un tercero |
| ¿Hace falta delegación? | No | **Sí**, y hoy no existe ese mecanismo |
| Ancla de confianza externa | La clave pública del sujeto + el encadenamiento de esa clave | El propio coordinador |
| Cobertura | Nodos con identidad de agente | También nodos sin identidad propia |

**Recomendación: el nodo firma; el coordinador distribuye y ancla.**

- La **firma** es del sujeto (el nodo), con su identidad Ed25519 de agente — la misma de RFC-002 (`pkg/signing` / `pkg/keystore`), reusando `pkg/jcs` para la forma canónica, tal como dice A2.
- El **binding** entre `node_id` y esa clave Ed25519 no lo afirma el coordinador en palabras: vive en el **log de eventos** (`node.joined`), que es la fuente de verdad y está encadenado por hash. La card incluye `subject.public_key`; un verificador que tenga acceso al log (o a su snapshot verificado, J3) puede comprobar el binding sin confiar en una declaración del coordinador.
- **Respaldo criptográfico en el servidor de producción (PR #181, issue #180)**: este binding no es una aspiración de diseño, sino una garantía del servidor real. El endpoint `POST /v1/mesh/join` solo permite emitir `node.joined` tras verificar la identidad mTLS del cliente (`r.TLS.VerifiedChains[0][0].Subject.CommonName == req.NodeID`, patrón S1) y validar canónicamente la clave Ed25519 (RFC 8032 §5.1.3, $y < 2^{255}-19$, sin claves todo-ceros). Además, el orden es estrictamente atómico: el evento se escribe en el JoinLog y se proyecta en SQLite *antes* de admitir al nodo en la memoria del runtime (`s.registry`), haciendo imposible registrar un nodo sin respaldo inmutable en el log.
- El **coordinador** sirve la card y el CA de la malla: es **distribuidor y ancla**, no *representante* de los nodos.
- **Nunca** el coordinador firma en nombre de un tercero. Si el sujeto de una card es el propio coordinador, la card lo declara (`subject.role: "coordinator"`) y su firma es sobre su propia identidad.

El precio explícito de esta recomendación: **el ancla de confianza externa es el registro del coordinador (o el log), no una cadena X.509 del sujeto**. La alternativa fuerte —firmar con la identidad mTLS del nodo (ECDSA P-256) y adjuntar su certificado, de modo que un externo que confíe en el CA de la malla verifique la cadena completa— es más cara: exige JWS con ES256 en lugar de EdDSA (desviándose del texto de A2) **o** reenrolar a los nodos con claves Ed25519 (tocando la PKI, donde #160 sigue abierto). Se deja anotado como decisión a futuro; no se construye en esta RFC.

#### 4.2.1.3 Direccionamiento A2A y resolución en malla

Por convención estándar de A2A, la ruta canónica `GET /.well-known/agent-card.json` (sin parámetros) **describe única y exclusivamente al host local** (en este caso, el coordinador de gentle-mesh). Un cliente A2A estándar externo que consulte este endpoint recibe la card del propio coordinador firmada con su propia identidad Ed25519.

Para la resolución de workers federados a través del gateway del coordinador, se implementa una extensión:
- **`GET /.well-known/agent-card.json?node_id=<id>`**: resuelve la Agent Card proyectada del worker desde el log de eventos de la malla.
- **Desviación explícita documentada**: el uso del parámetro `?node_id=` es una extensión de catálogo de gentle-mesh y **no forma parte de la especificación base de A2A**. Un cliente A2A genérico que no conozca esta extensión no sabrá consultar workers federados por query param; en una topología distribuida pura, cada nodo worker que exponga interfaz A2A hacia afuera servirá su propia card local en su respectivo `GET /.well-known/agent-card.json` en su propio puerto o dominio.
- **Invariante anti-suplantación**: el coordinador distribuye la card de los workers pero **nunca** firma en nombre de ellos (§4.2.1.2).

#### 4.2.1.4 Qué NO se decide aquí

- El formato exacto del `Agent Card` del ecosistema A2A externo (perfil, nombres de campo): la card de gentle-mesh usa su propio `kind` y se documenta como *subconjunto* del perfil A2A, no como una implementación completa.
- La rotación de claves y la revocación de una card: hereda el modelo de revocación del ledger PKI, pero su implementación no forma parte de esta pieza.

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
| J3 | sí | Snapshot con digest verificable por el cliente a partir del log. | cumplido | `TestSnapshot_VerifiableDigest`, `TestHTTPSnapshot_Success` | — | [#171](https://github.com/Rafaeldelinares/gentle-mesh/issues/171) |
| J4 | sí | Stream en vivo sin huecos ni duplicados, reanudable por cursor. | cumplido | `TestHTTPEvents_StreamAndResume`, `TestHTTPEvents_ChainDivergenceReturns409` | — | [#171](https://github.com/Rafaeldelinares/gentle-mesh/issues/171) |
| J5 | sí | La declaración de territorio se contrasta contra la actividad observada; la evidencia va al log. | cumplido | `TestTerritory_CleanVerification`, `TestTerritory_DiscrepancyVerification`, `TestTerritory_SurfaceMatchingBoundary`, `TestTerritory_VerifiedEventIsAdditiveEvidence` | — | [#173](https://github.com/Rafaeldelinares/gentle-mesh/issues/173) |
| J6 | sí | El estado completo se reconstruye leyendo el log desde cero. | cumplido | `TestProjector_RebuildFromScratchProducesIdenticalState` | — | [#169](https://github.com/Rafaeldelinares/gentle-mesh/issues/169) |
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
