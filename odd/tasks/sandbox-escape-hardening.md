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
| T1 | S5, S3 | Hardening de las 7 invocaciones de git del host del runner (6 con `exec.Command` + `prepareWorktree`, que va por `commandFactory`) | `pkg/server/runner/gentleshell.go`, `pkg/server/runner/gentleshell_test.go` | `fix(runner): harden host-side git against sandbox-written cache config` |
| T2 | S1 | `absPath` con contención real: rechaza absolutos y `..`, resuelve symlinks, verifica con `Rel` sobre raíz resuelta | `pkg/settlement/evaluate.go`, `pkg/settlement/evaluate_test.go` | `fix(settlement): contain assertion paths to the workspace root` |
| T3 | S2 | Env allowlist deny-by-default para el Evaluator; `WithEnv` queda como escape hatch explícito | `pkg/settlement/evaluate.go`, `pkg/settlement/evaluate_test.go` | `fix(settlement): run assertions with an allowlisted environment, not os.Environ()` |

Cada task: test RED observado → fix → GREEN. Al cierre, `./scripts/security-gates.sh`, `go vet`, `staticcheck`, `go test -race ./pkg/... ./cmd/... ./integration/agent/...`.

## Log

**L1 (pedido del mantenedor, verbatim):** "Gracias por la verificación, calidad excelente — corregiste el mecanismo exacto en varios casos, eso es justo lo que necesitaba. Orden de trabajo: sandbox primero (F2-01, F2-02, F2-03), porque es lo que bloquea retomar NiPoGi con trabajo real — el Evaluator corre en el host del worker. PKI (F1-01, F1-02) va segundo, antes de abrir la malla a cualquier nodo externo (el piloto con Daniel), pero no bloquea tu propio uso con NiPoGi. Empezá por el sandbox, mismo patrón TDD RED/GREEN de siempre, en su propia rama/issue, separado de PKI"

**L2 (restricción vigente):** NiPoGi queda en pausa. No se despachan tareas a NiPoGi sin autorización explícita.

**L3 (evidencia propia, pre-implementación):** PoC ejecutado sobre git 2.55.0 con cache bare + worktree enlazado: `core.fsmonitor` inyectado desde el cache **se ejecutó** vía `git status` del worktree (4 ejecuciones). Con `-c core.fsmonitor=false -c core.hooksPath=/dev/null -c diff.external= -c protocol.file.allow=never --no-optional-locks` + `GIT_CONFIG_NOSYSTEM=1 GIT_CONFIG_GLOBAL=/dev/null`, **no** se ejecuta. Todos los flags validados uno por uno contra el binario local antes de escribirlos.

**L4 (verificación de alcance):** `go list -deps ./cmd/gentle-mesh` no devuelve `pkg/settlement`, `pkg/shell` ni `integration/agent`. El Evaluator corre en el harness y en el host del worker cuando corre `gentle-shell`.

**L5 (hallazgo lateral):** `pkg/envelope/validate.go:340 ResolvePath` repite el patrón de `absPath` pero no tiene ningún llamador fuera de tests: código muerto. No se toca en esta rama; queda anotado.

**L6 (corrección propia, T1):** yo mismo escribí en el issue que la insura de `apply` "aplica" porque git lee el config del cache como remote. **Es falso y lo corregí antes de tocar el código**: el PoC muestra que el fetch desde un cache envenenado no ejecuta ninguno de los cinco vectores. El issue #145 quedó actualizado con el resultado negativo y la evidencia. También corregí el conteo: son **7** invocaciones de git del host, no 6, porque `prepareWorktree` usa `commandFactory` y no aparecía al grep-ear `exec.Command("git"`.

**L7 (trampa evitada):** `protocol.file.allow=never` **rompe** el fetch local de un repo a otro (`apply` depende de eso; `git fetch` desde un path local queda bloqueado). Los dos call sites necesitan conjuntos distintos: el runner nunca usa file transport (nunca clona), así que lleva `never`; `apply` lo necesita y no se toca. Todos los flags se validaron uno por uno contra el binario local de git antes de escribirlos.

**L8 (test que mentía):** el primer RED de T1 pasó sin querer: el probe de sanity usaba `git rev-parse`, que no dispara `core.fsmonitor`, y el test terminaba en `Skip` — verde por la razón equivocada. Corregido para usar un worktree enlazado + `git status`, que es lo que hace el runner. RED real: **8 ejecuciones del binario envenenado en el host**. GREEN: 0. Mutación (quitar `-c core.fsmonitor=false`) → vuelve a FAIL con 4, así que el test tiene dientes.