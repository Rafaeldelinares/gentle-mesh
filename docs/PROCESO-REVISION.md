# Proceso de revision y excepción documentada

Este documento registra un caso excepcional: dos PRs del proyecto se mergearon sin pasar la revisión automática (RDD, receipt-driven development) porque el entorno donde se ejecuta la revision no admite la ejecucion de este caso. La excepcion esta documentada y los PRs fueron verificados por otros medios.

## Motivos observados

La revisión automática (RDD) se intentó varias veces y no pudo ejecutarse por dos motivos combinados:

- La herramienta del host de Pi no admite una base explicita para revisar una rama ya confirmada (probado: `gentle_review start` con `lineage` explicito; la herramienta no acepta los flags `--base-ref` ni `--committed-only` del CLI, y sin ellos el sistema computa un `current-changes` vacio cuando el worktree esta limpio tras `git push`).
- El CLI exige el relay del host de Pi (`GENTLE_PI_REVIEW_RELAY_CONTRACT=gentle-pi.review-relay/v1`). El host de Pi no se exporta, y la variable por si sola sin relay activo no es suficiente (error: `immutable_review_transport_unsupported`).

Esto es incompatible con ejecutar la revision en un entorno aislado, que es donde la automatización corre por defecto.

## Que se hizo en su lugar

Para cada PR mergeado bajo esta excepcion, la verificacion manual sustituyo a la revisión automática. Se hicieron:

- CI en verde sobre el head (los tres checks de `Lint & Security`, `Unit Tests`, `Integration Tests (Docker)`).
- Lectura manual del diff completo, fichero a fichero.
- Comprobacion de la fuente de cada afirmacion externa (por ejemplo: para `ROADMAP.md`, se busco en el código la existencia de cancelacion de tareas y la entrada se retiro cuando se encontro el endpoint `POST /v1/tasks/{id}/cancel` en `pkg/server/http/handlers.go` y `TerritoryScheduler.Cancel` en `pkg/server/http/scheduler.go`).
- Escaneo de voseo sobre el diff completo, caracteres no latinos (CJK + Hangul) y palabras de cierre (los terminos en ingles close, fix, resolve seguidos de numeral y almohadilla, en commits y cuerpo de PR).
- Demo del proyecto ejecutada en contenedor y en local.

El caso será informado a `gentle-ai`.

## Alcance de la excepcion

La excepción aplica a los siguientes PRs mergeados sin RDD:

- PR #72 (`docs/quickstart-pi-relation-roadmap`): 4 ficheros de documentacion. El comentario públicado en el PR (`6077334554`) describe el mismo caso.
- PR #74 (`docs/roadmap`): 1 fichero de documentacion (`ROADMAP.md`). El comentario públicado en el PR (`6078584759`) describe el mismo caso.

La excepción no aplica a cambios de código, configuracion o infraestructura. Cambios no documentales seguiran requiriendo revisión automática (RDD) por el camino normal. Si en el futuro la herramienta del host de Pi admite `--base-ref` o el relay es accesible desde un entorno aislado, esta excepcion deja de tener efecto.
