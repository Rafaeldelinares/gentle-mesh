# PKI — revocación autoritativa e identidad en el enrollment

> Issue: [#148](https://github.com/Rafaeldelinares/gentle-mesh/issues/148)
> Rama: `fix/pki-authoritative-revocation`
> Base: `c3e900a` (merge PR #143). Frente independiente del sandbox (PR #146): verificado que no solapa `certstore`, `pkg/server/http`, `pki` ni `keystore`.

## Specs

**S1** — Del issue #148, mecanismo de F1-01: "`node_certs` tiene `node_id TEXT PRIMARY KEY` y `serial TEXT UNIQUE`: **una fila por nodo**. Rotar el certificado de un nodo **reemplaza su serial** y pone `revoked = 0`. El serial revocado anterior desaparece de la tabla."

**S2** — "La verificación en el handshake solo mira el cache" (`server.go:312-316`): "Nunca consulta SQLite." Criterio de cierre: "Un certificado revocado **sigue** rechazado después de rotar el certificado de ese nodo **y** reiniciar el coordinador."

**S3** — Del issue, dirección ya decidida: "El disco es la fuente de verdad, el cache en memoria queda como fast path, y el sistema falla cerrado por diseño." Y: "Un error de base de datos durante la verificación **no** degrada a 'no revocado'."

**S4** — Del issue, mecanismo de F1-02: "El `node_id` viene del body y no se valida contra nada" y "un token de inscripción **no está atado** a una identidad… Un token válido sirve para cualquier `node_id`." Criterio: "`POST /v1/certs/enroll` con un `node_id` que ya tiene certificado responde error explícito y **no consume el token**."

**S5** — Del mantenedor: "F1-01: implementá la Opción A… Preferí esto sobre tu recomendación de B porque elimina por completo la clase de bug de divergencia caché/disco que causó F1-01 — no la minimiza, la hace imposible."

**S6** — "empezá el frente PKI (F1-01, F1-02) en su propia rama con la decisión de diseño ya tomada arriba", y el merge de la PR #146 lo hace él.

## Tasks

| ID | Spec | Trabajo | Allowed edit surface | Commit |
|----|------|---------|----------------------|--------|
| T1 | S1, S2, S3, S5 | `revoked_serials` append-only + backfill, `IsCertRevoked` autoritativo con cache fast path y fail-closed, rehidratación desde la fuente autoritativa | `pkg/server/store/certstore.go`, `pkg/server/http/server.go`, tests | `6b0e874` **hecho** |
| T2 | S4 | `handleCertsEnroll` rechaza un `node_id` que ya tiene certificado, antes de consumir el token | `pkg/server/http/handlers.go`, tests | `368a2c8` **hecho** |

### Evidencia por work unit

| Task | RED observado | GREEN |
|------|----------------|-------|
| T1 | "the revoked serial 99887766554433221 verified again after its node rotated and the coordinator restarted" + "a store failure must surface as an error, got revoked=false with no error" | ambos |
| T2 | `ESCAPE: a second token minted a certificate for the existing identity "worker-alpha"` y `ESCAPE: a revoked node was resurrected through enrollment` | 3/3, incluido que una identidad nueva sigue enrolando y que el rechazo **no** consume el token |

Cada task: test RED observado → fix → GREEN. Cierre: `./scripts/security-gates.sh`, `go vet`, `staticcheck`, `go test -race ./pkg/... ./cmd/... ./integration/agent/...`.

## Log

**L1 (pedido del mantenedor, verbatim):** "F1-01: implementá la Opción A (el disco como fuente de verdad, el cache queda como fast path, fail-closed por diseño). Preferí esto sobre tu recomendación de B porque elimina por completo la clase de bug de divergencia caché/disco que causó F1-01 — no la minimiza, la hace imposible. El costo de una query por handshake ya lo calculaste como marginal." … "Después arrancá el frente PKI (F1-01, F1-02) en su propia rama con la decisión de diseño ya tomada arriba."

**L2 (verificación previa, commit c3e900a):** la cadena de F1-01 completa y sus cuatro eslabones citados arriba. Corrección importante al reporte original de la auditoría: el método `store.IsCertRevoked` que hace fail-open con `sql.ErrNoRows` **no participa** de la verificación mTLS — el servidor decide solo con el cache. El fallo real es que la rotación borra el registro del que se rehidrata ese cache.

**L3 (modelo de datos, verificado):** `node_certs(node_id PK, serial UNIQUE)`. Una fila por nodo, así que la rotación no puede conservar el serial anterior dentro de `node_certs` sin cambiar el modelo. Por eso Opción A usa una tabla de revocación append-only **separada**: la revocación es un hecho que la rotación no debe poder borrar, y eso no obliga a supportar múltiples certificados por nodo.

**L4 (alcance de la corrección, en el issue):** F1-02 se cierra rechazando identidades ya tomadas, que es lo que elimina el secuestro de identidad. Atar cada token a una identidad determinada es una mejora de superficie que queda propuesta aparte.

**L5 (mi mock probaba el camino equivocado, T1):** el primer RED del fail-closed quedó en verde solo porque mi `failingCertStore` fallaba **todas** las operaciones, así que el arranque moría antes ("failed to warm up certificate revocation cache: database is locked") y la aserción nunca llegaba a ejecutarse. O sea: el test pasaba por una vía que no era la que probaba. Lo dividí para que solo la decisión del handshake falle, y el RED apareció como correspondía.

**L6 (mi propio "fail closed" rompió el bootstrap, T2):** cerré F1-02 y rompí cuatro tests de una vez. El síntoma era `503 certificate store is not configured`, no un rechazo por identidad ya tomada: mi guarda `certStore == nil → 503` rechazaba **todo** enrollment en la configuración sin almacén de certificados, que es exactamente como arman el servidor los tests de bootstrap. La lección es que "fail closed" no es gratis y hay que preguntar *qué se está defendiendo*: sin store no hay identidad persistida que tomar ni estado de revocación que corromper, y esa configuración tampoco cablea verificador de revocación, así que el chequeo se salta ahí en vez de rechazar. Los cuatro tests volvieron a verde sin aflojar nada de F1-02: con store presente el rechazo sigue igual de estricto, y eso está fijado por los dos tests de ESCAPE y por el de identidad nueva.