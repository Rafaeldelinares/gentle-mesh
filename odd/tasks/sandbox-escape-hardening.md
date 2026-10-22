# Sandbox escape — Evaluator y git del host

> Issue: [#145](https://github.com/Rafaeldelinares/gentle-mesh/issues/145)
> Rama: `fix/sandbox-escape-evaluator-and-git`
> Base: `c3e900a` (merge PR #143)
> Frente: **sandbox**. PKI (F1-01/F1-02) va aparte con decisión de diseño abierta.

## Specs

Las specs citan el texto verificado del issue #145 y del pedido del mantenedor.

**S1** — `pkg/settlement/evaluate.go` `absPath`: "no hay rechazo de absolutos, ni de `..`, ni verificación posterior con `filepath.Rel`". Consecuencia: "un `path` como `/etc/shadow` o `../../root/.ssh/id_rsa` produce un `os.Stat`/`os.ReadFile` sobre cualquier archivo legible del host".

**S2** — `pkg/settlement/evaluate.go:66-71,354-367`: "no restringe qué binario se ejecuta: cualquier binario con cualquier argumentos. Y el hijo recibe `os.Environ()` completo: claves de API, tokens, `HOME`, variables del agente."

**S3** — `pkg/server/runner/gentleshell.go:612,616,626,633,654,669`: "**Ninguno** lleva `-c core.hooksPath=`, `-c core.fsmonitor=`, `-c diff.external=`, `-c protocol.file.allow=` ni variables `GIT_CONFIG_NOSYSTEM`/`GIT_CONFIG_GLOBAL`." PoC: "El binario definido en `core.fsmonitor` **se ejecutó en el host**."

**S4** — Extensión evaluada y **descartada**: "`gentle-mesh apply` también corre git contra el mismo cache escrito por el sandbox", sí, pero el `git fetch` que usa no ejecuta ningún programa definido en la config del cache (`uploadpack.packObjectsHook`, `core.fsmonitor`, `diff.external`, `credential.helper`, `core.gitProxy`, probados uno por uno). No hay escape demostrado, así que **no se modifica `apply`**.

**S5** — Criterio del mantenedor: "mismo patrón TDD RED/GREEN de siempre, en su propia rama/issue, separado de PKI", y documentar "el mecanismo real que vos mismo identificaste, no la descripción original de Codex".

**S6** — Y de paso, del mantenedor: "F1-01 ya tenés la decisión de diseño pendiente... dámela a elegir cuando lleguemos ahí, no la resuelvas sola todavía." → **no** se toca PKI en esta rama.

## Tasks

| ID | Spec | Trabajo | Allowed edit surface | Commit |
|----|------|---------|----------------------|--------|
| T1 | S5, S3 | Hardening de las 7 invocaciones de git del host del runner (6 con `exec.Command` + `prepareWorktree`, que va por `commandFactory`) | `pkg/server/runner/gentleshell.go`, `pkg/server/runner/gentleshell_test.go` | `50b2028` **hecho** |
| T2 | S1 | `absPath` con contención real: rechaza absolutos y `..`, resuelve symlinks, verifica con `Rel` sobre raíz resuelta | `pkg/settlement/evaluate.go`, `pkg/settlement/evaluate_test.go` | `a17c8d5` **hecho** |
| T3 | S2 | Env allowlist deny-by-default para el Evaluator; `WithEnv` queda como escape hatch explícito | `pkg/settlement/evaluate.go`, `pkg/settlement/evaluate_test.go` | `4d349eb` **hecho** |
| T4 | S3 | Bypass de `filter.<driver>`: paquete compartido `pkg/gitsafe` con enumeración y neutralización de drivers | `pkg/gitsafe/`, `pkg/server/runner/gentleshell.go`, `pkg/server/runner/filter_driver_bypass_test.go` | `fb7d7d5` **hecho** |
| T5 | S3 | El mismo escape en los otros 4 `git status` del host (Evaluator, `pkg/shell`, `integration/agent` x2) | `pkg/settlement/evaluate.go`, `pkg/shell/shell.go`, `integration/agent/server_shell.go`, `integration/agent/server.go` | `4a89698` **hecho** |
| T6 | S3 | Bypass `=`: neutralización por `GIT_CONFIG_KEY_n` + enumeración con `--name-only -z` | `pkg/gitsafe/`, `pkg/server/runner/gentleshell.go`, `pkg/settlement/evaluate.go` | `f4044b0` **hecho** |

PR: #146. Cada task: test RED observado → fix → GREEN. Al cierre, `./scripts/security-gates.sh`, `go vet`, `staticcheck`, `go test -race ./pkg/... ./cmd/... ./integration/agent/...`.

### Evidencia por work unit

| Task | RED observado | GREEN |
|------|----------------|-------|
| T1 | 8 ejecuciones del payload en el host | 0; mutar (quitar `-c core.fsmonitor=false`) → FAIL con 4 |
| T2 | 4 escapes devolvían `PASS` (absoluto, `..`, symlink, `cwd`) | 7/7 |
| T3 | el secreto llegaba al comando (`exit 0, expected 1`) | 3/3 |
| T4 | `ESCAPE: the sandbox-defined smudge filter ran on the host 1 time(s)` | 0; paridad de diff byte a byte con git normal |
| T5 | `ESCAPE: the workspace core.fsmonitor ran on the host 2 time(s)` | 0; worktree limpio sigue pasando |
| T6 | `ESCAPE: driver con '=' ejecutado en el host (1 vez)` | 0, en el camino smudge (`worktree add`) **y** en el clean (`git status`) |

## Log

**L1 (pedido del mantenedor, verbatim):** "Gracias por la verificación, calidad excelente — corregiste el mecanismo exacto en varios casos, eso es justo lo que necesitaba. Orden de trabajo: sandbox primero (F2-01, F2-02, F2-03), porque es lo que bloquea retomar NiPoGi con trabajo real — el Evaluator corre en el host del worker. PKI (F1-01, F1-02) va segundo, antes de abrir la malla a cualquier nodo externo (el piloto con Daniel), pero no bloquea tu propio uso con NiPoGi. Empezá por el sandbox, mismo patrón TDD RED/GREEN de siempre, en su propia rama/issue, separado de PKI"

**L2 (restricción vigente):** NiPoGi queda en pausa. No se despachan tareas a NiPoGi sin autorización explícita.

**L3 (evidencia propia, pre-implementación):** PoC ejecutado sobre git 2.55.0 con cache bare + worktree enlazado: `core.fsmonitor` inyectado desde el cache **se ejecutó** vía `git status` del worktree (4 ejecuciones). Con `-c core.fsmonitor=false -c core.hooksPath=/dev/null -c diff.external= -c protocol.file.allow=never --no-optional-locks` + `GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null`, **no** se ejecuta. Todos los flags validados uno por uno contra el binario local antes de escribirlos.

**L4 (verificación de alcance):** `go list -deps ./cmd/gentle-mesh` no devuelve `pkg/settlement`, `pkg/shell` ni `integration/agent`. El Evaluator corre en el harness y en el host del worker cuando corre `gentle-shell`.

**L5 (hallazgo lateral):** `pkg/envelope/validate.go:340 ResolvePath` repite el patrón de `absPath` pero no tiene ningún llamador fuera de tests: código muerto. No se toca en esta rama; queda anotado.

**L6 (corrección propia, T1):** yo mismo escribí en el issue que la insura de `apply` "aplica" porque git lee el config del cache como remote. **Es falso y lo corregí antes de tocar el código**: el PoC muestra que el fetch desde un cache envenenado no ejecuta ninguno de los cinco vectores. El issue #145 quedó actualizado con el resultado negativo y la evidencia. También corregí el conteo: son **7** invocaciones de git del host, no 6, porque `prepareWorktree` usa `commandFactory` y no aparecía al grep-ear `exec.Command("git"`.

**L7 (trampa evitada):** `protocol.file.allow=never` **rompe** el fetch local de un repo a otro (`apply` depende de eso; `git fetch` desde un path local queda bloqueado). Los dos call sites necesitan conjuntos distintos: el runner nunca usa file transport (nunca clona), así que lleva `never`; `apply` lo necesita y no se toca. Todos los flags se validaron uno por uno contra el binario local de git antes de escribirlos.

**L8 (test que mentía):** el primer RED de T1 pasó sin querer: el probe de sanity usaba `git rev-parse`, que no dispara `core.fsmonitor`, y el test terminaba en `Skip` — verde por la razón equivocada. Corregido para usar un worktree enlazado + `git status`, que es lo que hace el runner. RED real: **8 ejecuciones del binario envenenado en el host**. GREEN: 0. Mutación (quitar `-c core.fsmonitor=false`) → vuelve a FAIL con 4, así que el test tiene dientes.

**L9 (segundo test que mentía, T2):** `TestAbsPath_RejectsCwdEscapingWorkspace` pasaba porque `git status` en un directorio que no es repo ya devuelve error, no por contención. Reescrito con un repo git real afuera del workspace, y agregado el caso legítimo espejo (`AllowsCwdInsideWorkspace`) para que el fix no pueda pasar por negarlo todo.

**L10 (gates de cierre):** `./scripts/security-gates.sh` 0 violaciones, `go vet ./...` y `staticcheck ./...` limpios, `go test -race -timeout 10m ./pkg/... ./cmd/... ./integration/agent/...` en verde (EXIT=0), sin regresión en `pkg/shell` ni `integration/agent`. El drift de gofmt de `pkg/settlement/evaluate.go` es preexistente en `c3e900a` (verificado con `git show`) y mis bloques no aportan ningún hunk de `gofmt -d`.

**L15 (gates repetidos sobre el estado final):`security-gates.sh` 0 violaciones, `go vet ./...` y `staticcheck ./...` limpios, y `go test -race -timeout 10m ./pkg/... ./cmd/... ./integration/agent/...` en verde (EXIT=0) ya con T4 y T5 aplicados, no solo sobre el estado previo. Los cinco paquetes tocados por T5 (`gitsafe`, `settlement`, `shell`, `runner`, `integration/agent`) pasan individualmente con `-race`.

**L16 (segundo bypass, T6 — el primero no cerraba lo que decía cerrar):** la neutralización por `-c` **no** podía alcanzar un driver llamado `a=b`, porque git parte `-c` en el **primer** `=`: `-c filter.a=b.smudge=` fija `filter.a` y deja viva `filter.a=b.smudge`. Y ese nombre es sección de config válida *y* valor válido de gitattributes, o sea que la enumeración lo encontraba y lo neutralizaba en la nada. **Reproducido por mí** contra `fb7d7d5`: el payload corrió en el host por el `worktree add` endurecido. La lección de fondo no es el `=` sino el **footgun de la API**: `Argv()` sin env era una forma natural de construirse un comando con la neutralización a medias, en silencio. Por eso `Plan()` devuelve argv **y** env juntos, y `Env()` desaparece.

**L17 (fail-open de enumeración, T6):** `--get-regexp` imprime `key value` y yo cortaba por el primer espacio, así que un driver con espacio se descartaba en silencio de la lista: enumeración fail-open. Ahora se parsea con `--name-only -z`. También la enumeración corre solo con los overrides de git y no con el env del host, para que un `GIT_DIR` del entorno no la haga leer otro repo que el que se va a neutralizar (no alcanzable por el sandbox —no puede escribir el env del host— pero estaba mal).

**Pendiente de esta fase:** verificación independiente de seguridad (subagente read-only). Va en tercera pasada sobre T6; si encuentra algo, entra como commit de fix en la PR #146 antes de dar el frente por cerrado.

**L11 (bypass encontrado por la verificación independiente, T4):** el primer hardening de T1 cerró las claves **fijas** de git, pero los *filter drivers* tienen clave dinámica (`filter.<nombre>.smudge`): ningún `-c` global los alcanza, y las dos mitades del ataque viven en el cache escribible (`info/attributes` + la definición del driver). `git worktree add` hace checkout, así que ejecuta el smudge **en el host**. Reproducido por mí contra el commit anterior, con todos los flags puestos: el payload corrió. La clase se cierra enumerando los drivers del repo y neutralizándolos uno por uno (`clean`/`smudge`/`process` vacíos + `required=false`), lo que verifiqué que deja el checkout como pass-through: contenido idéntico y payload sin ejecutar. La enumeración es **fail-closed**: una lista incompleta devolvería el escape en silencio.

**L12 (dos correcciones al pie del bypass, T4):** un `git diff` sin `--no-ext-diff` ahora se **rechaza** con error explícito (con `diff.external=` vacío, git falla duro en vez de diffear, así que sin el flag parecería un runner roto y no uno neutralizado); y un test fija la **paridad de salida**: el diff endurecido debe ser idéntico byte a byte al de git normal, o el operador estaría revisando una propuesta que no es la del agente.

**L13 (alcance ampliado con evidencia, T5):** la verificación señaló como *advisory* que los otros cuatro `git status` del host (evaluator, `pkg/shell`, `integration/agent` x2) tenían el mismo mecanismo contra el mismo config escribible por el sandbox. Aunque fuera preexistente y no estuviera en el pedido, es la misma clase sobre el componente que corre en el host del worker, así que entra en este frente. En el Evaluator el endurecimiento se compone con su env allowlisted (`gitsafe.Overrides()`) para no deshacer F2-02.

**L14 (deuda consciente que NO cierro aquí):** la env allowlist de F2-02 no tiene forma de que el operador restaure `GOPROXY`/`HTTPS_PROXY`/`GOFLAGS`/`GOPRIVATE` en un worker detrás de proxy o con registry privado. Ningún llamador de producción usa `WithEnv`. Es una regresión de usabilidad real de mi propio cambio: queda anotada para decidir con el mantenedor (flag de CLI vs variable de entorno) en vez de resolverla por mi cuenta.

**Siguiente frente (no esta rama):** PKI. F1-01 tiene una decisión de diseño abierta que el mantenedor quiere elegir: si la rehidratación de revocaciones lee el store en cada handshake, o si la rotación deja de borrar el serial revocado.