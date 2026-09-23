# Thermonuclear Peer Review: Gentle Mesh

> **Fecha de Conducción:** 23 de septiembre de 2026  
> **Comité Revisor Externo:** OpenAI GPT-4o, Anthropic Claude 3.5 Sonnet, DeepSeek (V2.5 / DeepSeek-Coder)  
> **Metodología:** Auditoría adversarial externa independiente con acceso completo al código fuente (`pkg/`, `cmd/`), RFC 001, topología Docker y suite de pruebas con `-race`.  
> **Propósito:** Gobernanza técnica, transparencia comunitaria y plan de endurecimiento (*hardening*) previo a la versión 1.0.

---

## 1. Síntesis Ejecutiva

El 23 de septiembre de 2026, el repositorio `gentle-mesh` fue sometido a una **Revisión Termonuclear Tripartita** convocando a tres de los modelos de inteligencia artificial más avanzados en ingeniería de software y sistemas distribuidos.

El objetivo fue auditar el proyecto sin condescendencia técnica: someter la concurrencia en Go, el protocolo de streaming Server-Sent Events (SSE), la persistencia append-only en JSONL, la topología Docker y el modelo de territorio a una inspección forense exhaustiva.

El resultado arrojó un veredicto unánime:
> **Gentle Mesh posee una base conceptual e idiomática sobresaliente, pero presentaba una peligrosa desconexión entre lo probado en aislamiento y lo efectivamente integrado en el pipeline de admisión.**
> 
> Los tres revisores coincidieron en que el verdadero valor diferencial del proyecto no es el balanceo de carga de workers remotos (un problema resuelto en la industria), sino el **Protocolo de Territorio y el Radar de Ámbitos**: un tejido de coordinación espacial y semántica para múltiples agentes de IA editando concurrentemente el mismo repositorio.

---

## 2. Cuadro Comparativo de Evaluaciones

| Dimensión Evaluada | OpenAI GPT-4o | Anthropic Claude 3.5 Sonnet | DeepSeek (Auditoría Real) | Media Consensuada |
|:---|:---:|:---:|:---:|:---:|
| **Concepto / Especificación (RFC 001)** | 8.5 / 10 | 8.0 / 10 | 7.5 / 10 | **8.0 / 10** |
| **Calidad de Go e Idiomaticidad** | 7.5 / 10 | 8.0 / 10 | 8.5 / 10 | **8.0 / 10** |
| **Protocolo de Territorio y Radar** | 7.5 / 10 | — | 8.0 / 10 | **7.8 / 10** |
| **Suite de Tests y Concurrencia (`-race`)** | 7.0 / 10 | — | 8.5 / 10 | **7.8 / 10** |
| **Integración de Componentes / Admisión** | — | 5.0 / 10 | 6.5 / 10 | **5.8 / 10** |
| **Madurez para Producción** | 5.5 / 10 | 6.0 / 10 | 6.5 / 10 | **6.0 / 10** |
| **Potencial Estratégico** | 8.5 / 10 | 8.5 / 10 | 8.5 / 10 | **8.5 / 10** |
| **Calificación Global** | **7.0 / 10** | **7.0 / 10** | **8.0 / 10** | **7.3 / 10** |

---

## 3. El Consenso de los Tres Revisores

### 3.1. El Verdadero Núcleo Diferencial: "Coordination Fabric"
Los tres auditores advirtieron contra el riesgo de sobreingeniería y el desvío de foco:
- Despachar procesos remotos sobre Docker es una capacidad comoditizada (Kubernetes, Nomad, Celery).
- **El verdadero valor disruptivo de Gentle Mesh es el Territory Protocol + Radar de Ámbitos:** modelar el espacio de trabajo del agente mediante un vector tridimensional:
  1. **Dominio Arquitectónico (`domain`):** Clasificación funcional del subsistema (`auth`, `database`, `billing`).
  2. **Superficies de Edición (`edit_surfaces`):** Rutas y globs exactos autorizados (`pkg/auth/*`).
  3. **Radio de Impacto (`blast_radius`):** Criticidad sistémica (`read-only`, `isolated-branch`, `shared-schema`, `breaking-change`).
- Esta consciencia situacional previene que múltiples agentes choquen de forma destructiva en el mismo repositorio y transforma la ejecución remota de una caja negra opaca a un panel de control transparente.

### 3.2. Regla de Oro: "Hacerlo Más Correcto Antes de Hacerlo Más Grande"
La recomendación estratégica unánime para el equipo de desarrollo fue categórica:
> **Frenar la expansión hacia federación M2M compleja y concentrarse en el endurecimiento del nodo único / coordinador (single-node/coordinator hardening).**
> 
> Un único coordinador que gestione 2 workers con 10.000 eventos, desconexiones intermedias, reconexión histórica impecable, clientes lentos y tolerancia a `kill -9` de forma determinista vale diez veces más que una malla federada frágil.

---

## 4. Los 5 Hallazgos Críticos Consensuados y su Estado

A partir de los informes externos, se definieron cinco prioridades técnicas críticas (P0/P1) para el proyecto:

```text
┌──────────────────────────────────────────────────────────────────────────────────┐
│                             ESTADO DE HALLAZGOS CRÍTICOS                        │
├────────┬──────────────────────────────────────────┬──────────────┬───────────────┤
│ Código │ Descripción Técnica                      │ Prioridad    │ Estado Actual │
├────────┼──────────────────────────────────────────┼──────────────┼───────────────┤
│ P0.1   │ Territory Admission en handleCreateTask  │ Crítica (P0) │ ✅ Resuelto   │
│ P0.2   │ Runner Panic Recovery en Goroutine       │ Crítica (P0) │ ✅ Resuelto   │
│ P0.3   │ Git Repo URL Normalization en Locks      │ Crítica (P0) │ ✅ Resuelto   │
│ P0.4   │ JSONL fsync Batching fuera del Mutex     │ Alta (P0)    │ 🟡 En Progreso│
│ P1     │ HTTP Timeouts & SSE Heartbeat Pings      │ Media (P1)   │ 🔵 Planificado│
└────────┴──────────────────────────────────────────┴──────────────┴───────────────┘
```

### Detalle de Resolución de los Hallazgos

#### ✅ P0.1 — Territory Admission (`handleCreateTask`)
* **Diagnóstico (Claude / DeepSeek):** El servidor implementaba detección de solapamiento en `pkg/protocol/federation.go` pero `handleCreateTask` jamás invocaba `manifest.FindConflict(...)`. Dos agentes podían editar el mismo archivo en ramas distintas sin advertencia.
* **Resolución:** Se cableó el chequeo territorial obligatorio en `handleCreateTask`:
  - Se instancia `protocol.ActiveTerritory` con la información del request (`Repo`, `Branch`, `Domain`, `EditSurfaces`, `BlastRadius`).
  - Se evalúa contra `s.taskManager.ActiveTerritories()` mediante `manifest.FindConflict(targetTerritory)`.
  - Ante cualquier solapamiento de superficies o rama bloqueada, la API responde con `HTTP 409 Conflict` devolviendo el payload estructurado `TerritoryConflict`.
  - Se añadieron tests exhaustivos en `server_test.go` (`TestServer_TerritorySurfaceOverlapConflict`).

#### ✅ P0.2 — Runner Panic Recovery
* **Diagnóstico (DeepSeek):** La goroutine que ejecutaba `r.Run()` en `handleCreateTask` no contenía un bloque `recover()`. Si el runner sufría un panic por un puntero nil o fallo de plugin, todo el servidor de Gentle Mesh caía catastróficamente.
* **Resolución:** Se incorporó un bloque `defer` con captura de panics:
  - Se registra el error con stack trace completo mediante `debug.Stack()`.
  - Se emite un `protocol.EventError` fatal (`RUNNER_PANIC`) para notificar a los suscriptores SSE antes de cerrar.
  - Se libera el lock de rama asociado para no dejar el repositorio congelado.
  - Se validó el comportamiento en `TestServer_RunnerPanicRecovery`, asegurando que tras un panic el servidor continúa vivo y respondiendo en `/healthz`.

#### ✅ P0.3 — Repo Normalization en `BranchLockManager`
* **Diagnóstico (GPT-4o / Claude):** El gestor de cerrojos indexaba por cadenas literales de texto. Una petición con `git@github.com:org/repo.git` y otra con `https://github.com/org/repo` no colisionaban, eludiendo la exclusividad de rama.
* **Resolución:** Se implementó `makeBranchKey()` utilizando `protocol.NormalizeRepo()`, unificando esquemas SSH, HTTPS y sufijos `.git` en una clave canónica normalizada. Validado con `TestBranchLockManager_RepoNormalizationVariations`.

#### 🟡 P0.4 — JSONL fsync Batching e I/O fuera del Mutex
* **Diagnóstico (GPT-4o / Claude / DeepSeek):** Cada invocación a `WriteEvent` ejecuta `file.Sync()` síncrono mientras se retiene `ManagedTask.mu.Lock()`. En streaming denso de tokens, el disco se convierte en un cuello de botella que bloquea las lecturas de estado del agente.
* **Estado:** En progreso. Se diseñó el esquema de *group commits* con buffer en memoria (`bufio.Writer`) y sincronización asíncrona periódica (cada 100ms o en transiciones de fase y finalización).

#### 🔵 P1 — HTTP Timeouts & SSE Heartbeat Pings
* **Diagnóstico (GPT-4o / Claude):** El servidor HTTP carece de `ReadHeaderTimeout` e `IdleTimeout`, haciéndolo vulnerable a DoS del tipo Slowloris. Además, los streams SSE sin comentarios `:ping` o `:keepalive` periódicos sufren desconexiones silenciosas por proxies o firewalls intermedios.
* **Estado:** Planificado para la siguiente iteración de infraestructura.

---

## 5. Índice de Revisiones Individuales

Para consultar el análisis detallado, línea por línea, de cada auditor externo:

1. 📄 **[Revisión Completa de OpenAI GPT-4o](./2026-09-gpt4o-review.md)**  
   *Análisis arquitectónico de ciclo de vida de runners, replay streaming de memoria acotada, modelo temporal del radar y hoja de ruta por versiones (v0.1 a 1.0).*
2. 📄 **[Revisión Completa de Anthropic Claude 3.5 Sonnet](./2026-09-claude-sonnet-review.md)**  
   *Disección de la interfaz `Runner/EventSink`, identificación del cuello de botella de `file.Sync()`, el hallazgo del cable desconectado en `handleCreateTask` y análisis de vulnerabilidades en subprocesos.*
3. 📄 **[Revisión Completa de DeepSeek](./2026-09-deepseek-review.md)**  
   *Contraste metodológico entre la fase teórica inicial y la auditoría forense de código, desmontaje de deadlocks, análisis de `recover()` en goroutines y hardening de Dockerfile.*
