import fs from 'node:fs';
import path from 'node:path';

/**
 * scripts/inject-test-player.mjs
 *
 * Injects the interactive Test Tour Player into:
 *   - docs/architecture/gentle-mesh-test-verification-gates.html (9 verification steps)
 *   - docs/architecture/gentle-mesh-secure-environment.html (11 topology components)
 * And updates:
 *   - docs/architecture/index.html with direct play links for all 7 test vectors.
 *
 * Supports --dry-run for non-destructive verification without writing to disk.
 */

const DRY_RUN = process.argv.includes('--dry-run') || process.argv.includes('--test');

function escapeHtml(str) {
  if (typeof str !== 'string') return '';
  return str
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#039;');
}

/* ============================================================
   Test Dictionaries
   ============================================================ */

const VERIFICATION_GATES_STEPS = [
  {
    id: 'test_runner',
    label: 'Go Test Runner (go test -race)',
    sublabel: 'Suite unit & int · 100% Pass',
    phase: 'Verificación Automatizada',
    badge: 'Test Harness',
    intro: '¿Para qué sirve este test? Ejecuta concurrentemente más de 140 tests unitarios y de integración con el detector de carreras de memoria (-race) de Go. Valida el servidor HTTP, el streaming SSE, la máquina de estados de tareas, el locking de ramas y el protocolo M2M, garantizando thread-safety absoluto antes de desplegar.',
    test: 'go test -race -count=1 ./... (140+ tests en <5s, 0 data races)'
  },
  {
    id: 'gate_auth',
    label: 'G1: Auth Guard (Timing-safe Bearer & MaxBytes 1MB)',
    sublabel: 'Bearer Token timing-safe & Límite de Payload',
    phase: 'Fase 1: Admisión Segura',
    badge: 'Seguridad & Perímetro',
    intro: '¿Para qué sirve este test? Previene ataques de temporización de canal lateral (timing attacks) comparando el Bearer Token con crypto/subtle.ConstantTimeCompare, y bloquea ataques de denegación de servicio (DoS) por memoria envolviendo el body HTTP con http.MaxBytesReader(w, r.Body, 1<<20) limitándolo a 1MB estricto.',
    test: 'pkg/server/http/server_test.go -> TestServer_AuthBearer, TestServer_MaxBytesReader'
  },
  {
    id: 'gate_territory',
    label: 'G2: Territory Gate (FindConflict en superficies y ramas)',
    sublabel: 'FindConflict en superficies y ramas concurrentes',
    phase: 'Fase 1: Admisión Segura',
    badge: 'Coordinación Espacial',
    intro: '¿Para qué sirve este test? En entornos multi-agente, si dos agentes modifican concurrentemente los mismos archivos o ramas provocan colisiones destructivas. Este test comprueba que FindConflict(territory) analice la tupla (git_repo, branch, edit_surfaces) para asegurar exclusividad espacial en el repositorio.',
    test: 'pkg/server/http/server_test.go -> TestServer_TerritorySurfaceOverlapConflict, pkg/server/federation/manager_test.go -> TestTerritoryManager_FindConflict_LocalAndFederated'
  },
  {
    id: 'err_collision',
    label: 'HTTP 409 Conflict (Rechazo preventivo Fail-Fast)',
    sublabel: 'Rechazo determinista por solapamiento de territorio',
    phase: 'Fase 1: Admisión Segura',
    badge: 'Excepción Fail-Fast',
    intro: '¿Para qué sirve este test? Aplica el principio Fail-Fast: si se detecta solapamiento territorial, la solicitud se rechaza en milisegundos con HTTP 409 Conflict y detalles del conflicto, evitando desperdiciar tiempo de GPU/cómputo y miles de tokens de LLM en código que no podrá fusionarse.',
    test: 'pkg/protocol/federation_test.go -> TestClashesWith_SurfaceOverlap, TestTerritoryConflictJSONSerialization'
  },
  {
    id: 'gate_branch',
    label: 'G3: Branch Lock (Bloqueo exclusivo y normalización Git)',
    sublabel: 'Locking en memoria con normalización de repositorio',
    phase: 'Fase 2: Despacho Federado',
    badge: 'Aislamiento Git',
    intro: '¿Para qué sirve este test? El BranchLockManager adquiere cerrojos exclusivos en memoria para cada tupla (repositorio, rama) normalizando formatos SSH y HTTPS. Previene condiciones de carrera en el puntero HEAD de Git y asegura que no haya commits cruzados desordenados entre agentes concurrentes.',
    test: 'pkg/server/registry/lock_test.go -> TestBranchLockManager_BasicClaimAndRelease, TestBranchLockManager_RepoNormalizationVariations'
  },
  {
    id: 'gate_dispatch',
    label: 'G4: Dispatcher (Despacho heterogéneo por tags)',
    sublabel: 'Selección de nodo worker por tags y carga ponderada',
    phase: 'Fase 2: Despacho Federado',
    badge: 'Despacho Distribuido',
    intro: '¿Para qué sirve este test? El MeshRunner consulta el registro de nodos de la malla, filtra los workers remotos disponibles según sus tags (gpu, heavy, arm64, research, fast) y redirige la carga según balanceo ponderado, desacoplando el cómputo pesado de la máquina cliente o laptop del usuario.',
    test: 'pkg/server/runner/mesh_test.go -> TestMeshRunner_SelectNodeSuccess, TestMeshRunner_Triangulation'
  },
  {
    id: 'err_panic',
    label: 'Panic Recovery (Aislamiento de fallos con defer recover y SIGKILL)',
    sublabel: 'defer recover() con SIGKILL en grupos de procesos POSIX',
    phase: 'Fase 2: Despacho Federado',
    badge: 'Resiliencia Fail-Safe',
    intro: '¿Para qué sirve este test? Garantiza que un fallo catastrófico o pánico en el código de un subagente jamás derribe el demonio central de gentle-mesh. Mediante defer recover() captura el stack trace, emite un error SSE y elimina los procesos hijos zombis enviando SIGKILL al Process Group ID (-pgid).',
    test: 'pkg/server/http/server_test.go -> TestServer_RunnerPanicRecovery, TestServer_PanicRecovery'
  },
  {
    id: 'gate_stream',
    label: 'G5: SSE Stream (Reconexión resiliente con Last-Event-ID)',
    sublabel: 'Server-Sent Events con Last-Event-ID y Heartbeat',
    phase: 'Fase 3: Persistencia y Cierre',
    badge: 'Streaming Resiliente',
    intro: '¿Para qué sirve este test? Valida el streaming de salida en tiempo real sobre text/event-stream. Ante desconexiones de red móvil o Tailscale, el cliente reconecta enviando Last-Event-ID y el servidor reanuda exactamente desde el último evento sin pérdida de logs ni reinicio de la tarea.',
    test: 'pkg/server/http/server_test.go -> TestServer_LastEventIDReconnection, TestServer_SSEHeartbeat'
  },
  {
    id: 'verified',
    label: 'Verified Exit (Durabilidad con fsync selectivo y retención)',
    sublabel: 'file.Sync() selectivo y liberación transaccional',
    phase: 'Fase 3: Persistencia y Cierre',
    badge: 'Durabilidad ACID',
    intro: '¿Para qué sirve este test? Al finalizar la tarea, valida la persistencia duradera de logs ejecutando file.Sync() selectivo en almacenamiento permanente, liberando cerrojos de territorio y rama y transicionando la tarea a estado completed sin degradar la vida útil del SSD.',
    test: 'pkg/server/task/logger_test.go -> TestJSONLLogger_SelectiveSync, pkg/server/task/manager_test.go -> TestTaskManager_TrackRunnerGracefulShutdown'
  }
];

const SECURE_ENV_STEPS = [
  {
    id: 'client',
    label: 'Gentle Client CLI (Pi Agent Harness)',
    sublabel: 'gentle-mesh run / Pi Subagent SDK',
    phase: 'Perímetro Cliente',
    badge: 'Cliente CLI / Subagent',
    intro: '¿Para qué sirve este componente? Punto de entrada para el desarrollador y el harness de subagentes de Pi. Envía la tarea mediante HTTP POST /v1/tasks con su manifiesto territorial (git_repo, branch, edit_surfaces), ruteo por tags y consume los eventos en streaming en tiempo real vía SSE, desacoplando la sesión interactiva local.',
    test: 'cmd/gentle-mesh/main_test.go -> TestRun_DispatchAndSSEConsumption, TestRun_DomainBlastRadiusSurfacesFlags'
  },
  {
    id: 'auth_gate',
    label: 'Security Shield (Bearer timing-safe & MaxBytes)',
    sublabel: 'Autenticación Bearer en tiempo constante + 1MB limit',
    phase: 'Fase 1: Admisión y Hardening',
    badge: 'crypto/subtle & Hardening',
    intro: '¿Para qué sirve este componente? Valida el encabezado Authorization: Bearer <token> mediante crypto/subtle.ConstantTimeCompare previniendo ataques de temporización de canal lateral, y limita las peticiones HTTP a 1MB con MaxBytesReader para evitar ataques DoS por agotamiento de RAM.',
    test: 'pkg/server/http/server_test.go -> TestServer_AuthBearer, TestServer_MaxBytesReader'
  },
  {
    id: 'territory_gate',
    label: 'Territory Gate (Evaluación de Conflicto)',
    sublabel: 'FindConflict en superficies y ramas concurrentes',
    phase: 'Fase 1: Admisión y Hardening',
    badge: 'HTTP 409 Conflict',
    intro: '¿Para qué sirve este componente? Evalúa la tupla territorial (git_repo, branch, edit_surfaces) antes de admitir cualquier tarea. Normaliza esquemas Git y rechaza con HTTP 409 Conflict cualquier solapamiento destructivo entre agentes concurrentes.',
    test: 'pkg/server/http/server_test.go -> TestServer_TerritorySurfaceOverlapConflict, pkg/server/federation/manager_test.go -> TestTerritoryManager_FindConflict_LocalAndFederated'
  },
  {
    id: 'branch_lock',
    label: 'Branch Lock Manager',
    sublabel: 'Normalización canónica de Git y exclusión mutua',
    phase: 'Fase 1: Admisión y Hardening',
    badge: 'Race-free Git',
    intro: '¿Para qué sirve este componente? Adquiere cerrojos exclusivos en memoria (sync.Mutex) para cada tupla (repositorio, rama) normalizada. Evita colisiones de punteros HEAD en Git y asegura que no haya commits cruzados desordenados.',
    test: 'pkg/server/registry/lock_test.go -> TestBranchLockManager_BasicClaimAndRelease, TestBranchLockManager_RepoNormalizationVariations'
  },
  {
    id: 'task_store',
    label: 'Task Log Store',
    sublabel: 'JSONL append-only con fsync selectivo',
    phase: 'Fase 1: Admisión y Hardening',
    badge: 'Crash-Safe I/O',
    intro: '¿Para qué sirve este componente? Registro persistente en disco en formato JSONL append-only. Escribe secuencialmente cada evento y ejecuta file.Sync() selectivo en transiciones de estado críticas (pending -> running -> completed), asegurando durabilidad crash-safe ante cortes de energía sin desgastar innecesariamente el SSD.',
    test: 'pkg/server/task/logger_test.go -> TestJSONLLogger_SelectiveSync, pkg/server/task/manager_test.go -> TestTaskManager_TrackRunnerGracefulShutdown'
  },
  {
    id: 'coordinator',
    label: 'Central Coordinator',
    sublabel: 'HTTP REST + SSE + Idempotencia',
    phase: 'Fase 2: Coordinación y Despacho',
    badge: 'Orchestrator Hub',
    intro: '¿Para qué sirve este componente? Núcleo de orquestación en Go. Expone endpoints REST y streaming SSE, gestiona el ciclo de vida de tareas, aplica deduplicación con Idempotency-Key y despacha las cargas a workers especializados mediante el MeshRunner.',
    test: 'pkg/server/http/server_test.go -> TestServer_TaskCreateAndEventsStreaming, TestServer_Idempotency, TestServer_LastEventIDReconnection'
  },
  {
    id: 'worker_alpha',
    label: 'Worker Alpha (Fast Execution)',
    sublabel: 'Tags: fast, go · Concurrencia: 2',
    phase: 'Fase 3: Malla Pentagonal',
    badge: 'Alpha · fast/go',
    intro: '¿Para qué sirve este componente? Worker circular especializado en tareas rápidas de compilación Go, análisis de sintaxis y micro-ejecuciones de baja latencia con concurrencia de hasta 2 tareas, evitando que queden bloqueadas detrás de suites pesadas.',
    test: 'pkg/server/runner/mesh_test.go -> TestMeshRunner_SelectNodeSuccess, pkg/server/worker/server_test.go -> TestWorkerServer_ExecuteAndStreamSSE'
  },
  {
    id: 'worker_beta',
    label: 'Worker Beta (Heavy Execution)',
    sublabel: 'Tag: heavy · Concurrencia: 4',
    phase: 'Fase 3: Malla Pentagonal',
    badge: 'Beta · heavy',
    intro: '¿Para qué sirve este componente? Worker circular de alta capacidad para pruebas masivas, linters completos y suites de empaquetado intensivas en CPU/RAM con concurrencia máxima de 4 tareas, protegiendo al coordinador y a los workers rápidos de saturación.',
    test: 'pkg/server/runner/mesh_test.go -> TestMeshRunner_SelectNodeSuccess, pkg/server/worker/server_test.go -> TestWorkerServer_ExecuteAndStreamSSE'
  },
  {
    id: 'worker_gamma',
    label: 'Worker Gamma (Research Execution)',
    sublabel: 'Tag: research · Concurrencia: 2',
    phase: 'Fase 3: Malla Pentagonal',
    badge: 'Gamma · research',
    intro: '¿Para qué sirve este componente? Worker circular asignado a agentes de exploración de repositorios, indexación semántica y análisis de grafos de código (codegraph / blast radius), aislando el alto consumo de I/O de lectura.',
    test: 'pkg/server/runner/mesh_test.go -> TestMeshRunner_Triangulation, pkg/server/worker/server_test.go -> TestWorkerServer_ExecuteAndStreamSSE'
  },
  {
    id: 'worker_delta',
    label: 'Worker Delta (GPU/ML Execution)',
    sublabel: 'Tags: gpu, ml · Concurrencia: 1',
    phase: 'Fase 3: Malla Pentagonal',
    badge: 'Delta · gpu/ml',
    intro: '¿Para qué sirve este componente? Worker circular equipado para inferencia de modelos de lenguaje locales (LLMs) y aceleración por GPU con concurrencia estricta de 1 tarea para preservar VRAM y evitar fallos Out-Of-Memory (OOM).',
    test: 'cmd/gentle-mesh/main_test.go -> TestWorker_JoinAndHeartbeatCycle'
  },
  {
    id: 'worker_epsilon',
    label: 'Worker Epsilon (Testing & CI Verification)',
    sublabel: 'Tags: ci, test · Concurrencia: 2',
    phase: 'Fase 3: Malla Pentagonal',
    badge: 'Epsilon · ci/test',
    intro: '¿Para qué sirve este componente? Worker circular enfocado en la verificación automatizada continua, ejecución de go test -race y suites de integración con concurrencia de 2 tareas en red aislada antes de aceptar cambios.',
    test: 'pkg/server/runner/mesh_test.go -> TestMeshRunner_FallbackWhenNoNodeAvailable, pkg/server/worker/server_test.go -> TestWorkerServer_ExecuteAndStreamSSE'
  }
];

const VECTOR_PLAY_MAPPING = [
  { category: 'git', playId: 'gate_territory' },
  { category: 'security', playId: 'gate_auth' },
  { category: 'resilience', playId: 'err_panic' },
  { category: 'persistence', playId: 'verified' },
  { category: 'streaming', playId: 'gate_stream' },
  { category: 'cluster', playId: 'gate_dispatch' },
  { category: 'race', playId: 'test_runner' }
];

/* ============================================================
   CSS Templates
   ============================================================ */

const TOUR_PLAYER_CSS = `
    /* Interactive Test Tour Player */
    #test-tour-player {
      position: fixed;
      bottom: 4.5rem;
      left: 50%;
      transform: translateX(-50%);
      width: min(600px, calc(100vw - 1.5rem));
      background: color-mix(in srgb, var(--surface) 95%, transparent);
      backdrop-filter: blur(14px);
      -webkit-backdrop-filter: blur(14px);
      border: 1px solid var(--border);
      box-shadow: 0 16px 36px rgba(0, 0, 0, 0.18);
      border-radius: 12px;
      z-index: 1000;
      display: flex;
      flex-direction: column;
      overflow: hidden;
      box-sizing: border-box;
      color: var(--text);
      font-family: inherit;
      transition: opacity 0.2s ease, transform 0.2s ease;
    }
    #test-tour-player[hidden] {
      display: none !important;
    }

    .tour-player-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 0.5rem;
      padding: 0.75rem 1rem 0.5rem;
      border-bottom: 1px solid color-mix(in srgb, var(--border) 60%, transparent);
    }
    .tour-header-left {
      display: flex;
      flex-direction: column;
      gap: 0.15rem;
      min-width: 0;
    }
    .tour-header-top-row {
      display: flex;
      align-items: center;
      gap: 0.45rem;
    }
    .tour-step-badge {
      font-size: 0.65rem;
      font-weight: 700;
      letter-spacing: 0.05em;
      text-transform: uppercase;
      padding: 0.15rem 0.45rem;
      border-radius: 999px;
      background: color-mix(in srgb, #6366f1 20%, transparent);
      border: 1px solid color-mix(in srgb, #6366f1 45%, transparent);
      color: #a5b4fc;
      white-space: nowrap;
    }
    .tour-phase-badge {
      font-size: 0.65rem;
      color: var(--text-muted);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .tour-step-title {
      margin: 0;
      font-size: 0.95rem;
      font-weight: 700;
      color: var(--text);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .tour-btn-close {
      background: transparent;
      border: none;
      font-size: 1.15rem;
      color: var(--text-muted);
      cursor: pointer;
      padding: 0.25rem 0.5rem;
      border-radius: 4px;
      line-height: 1;
      transition: background 0.15s ease, color 0.15s ease;
      flex-shrink: 0;
    }
    .tour-btn-close:hover {
      color: var(--text);
      background: rgba(255, 255, 255, 0.1);
    }

    /* Distinctive Intro Callout */
    .tour-intro-box {
      margin: 0.6rem 1rem 0.4rem;
      padding: 0.65rem 0.85rem;
      border-radius: 8px;
      background: color-mix(in srgb, #6366f1 10%, var(--surface));
      border-left: 4px solid #6366f1;
      border-top: 1px solid color-mix(in srgb, #6366f1 25%, var(--border));
      border-right: 1px solid color-mix(in srgb, #6366f1 25%, var(--border));
      border-bottom: 1px solid color-mix(in srgb, #6366f1 25%, var(--border));
    }
    .tour-intro-title {
      display: flex;
      align-items: center;
      gap: 0.35rem;
      font-size: 0.75rem;
      font-weight: 700;
      color: #818cf8;
      margin-bottom: 0.25rem;
      text-transform: uppercase;
      letter-spacing: 0.03em;
    }
    .tour-intro-text {
      margin: 0;
      font-size: 0.8125rem;
      line-height: 1.45;
      color: var(--text);
    }

    /* Test suite badge row */
    .tour-test-row {
      margin: 0.2rem 1rem 0.6rem;
      display: flex;
      align-items: center;
      gap: 0.4rem;
      font-size: 0.75rem;
    }
    .tour-test-label {
      font-weight: 600;
      color: var(--text-muted);
      white-space: nowrap;
      flex-shrink: 0;
    }
    .tour-suite-badge {
      display: inline-block;
      font-family: monospace;
      font-size: 0.7rem;
      padding: 0.2rem 0.5rem;
      border-radius: 4px;
      background: rgba(0, 0, 0, 0.28);
      border: 1px solid color-mix(in srgb, #34d399 35%, transparent);
      color: #34d399;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      max-width: 100%;
    }

    /* Controls */
    .tour-player-controls {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 0.5rem;
      padding: 0.5rem 1rem 0.65rem;
      background: color-mix(in srgb, var(--surface) 80%, #000 20%);
      border-top: 1px solid color-mix(in srgb, var(--border) 60%, transparent);
    }
    .tour-controls-left {
      display: flex;
      align-items: center;
      gap: 0.4rem;
    }
    .tour-btn {
      appearance: none;
      font-family: inherit;
      font-size: 0.75rem;
      font-weight: 600;
      padding: 0.35rem 0.7rem;
      border-radius: 6px;
      border: 1px solid color-mix(in srgb, var(--border) 80%, transparent);
      background: rgba(255, 255, 255, 0.08);
      color: var(--text);
      cursor: pointer;
      transition: all 0.15s ease;
      white-space: nowrap;
    }
    .tour-btn:hover:not(:disabled) {
      background: rgba(255, 255, 255, 0.15);
      border-color: #818cf8;
    }
    .tour-btn:disabled {
      opacity: 0.4;
      cursor: not-allowed;
    }
    .tour-btn-play {
      background: color-mix(in srgb, #6366f1 25%, transparent);
      border-color: color-mix(in srgb, #6366f1 55%, transparent);
      color: #c7d2fe;
    }
    .tour-btn-play:hover:not(:disabled) {
      background: color-mix(in srgb, #6366f1 40%, transparent);
      border-color: #a5b4fc;
    }
    .tour-timer-label {
      font-family: monospace;
      font-size: 0.72rem;
      color: var(--text-muted);
      font-weight: 600;
      padding: 0.2rem 0.4rem;
      border-radius: 4px;
      background: rgba(0, 0, 0, 0.2);
    }

    /* Progress bar */
    .tour-progress-track {
      width: 100%;
      height: 4px;
      background: color-mix(in srgb, var(--border) 60%, transparent);
      position: relative;
      overflow: hidden;
    }
    .tour-progress-bar {
      height: 100%;
      width: 0%;
      background: linear-gradient(90deg, #6366f1, #38bdf8);
      transition: width 0.08s linear;
    }

    /* Toolbar buttons */
    .btn-nav-tour {
      font-weight: 700 !important;
      color: #38bdf8 !important;
      border-color: color-mix(in srgb, #38bdf8 45%, var(--toolbar-border)) !important;
      background: color-mix(in srgb, #38bdf8 15%, transparent) !important;
    }
    .btn-nav-tour:hover {
      background: color-mix(in srgb, #38bdf8 30%, transparent) !important;
      border-color: #38bdf8 !important;
    }
    .btn-guided-tour {
      font-weight: 700 !important;
      color: #38bdf8 !important;
      border-color: color-mix(in srgb, #38bdf8 45%, var(--toolbar-border)) !important;
      background: color-mix(in srgb, #38bdf8 15%, transparent) !important;
      display: inline-flex;
      align-items: center;
      gap: 0.25rem;
      padding: 0.2rem 0.55rem;
      border-radius: 0.35rem;
      font-size: 0.6875rem;
      cursor: pointer;
      transition: all 0.15s ease;
      white-space: nowrap;
    }
    .btn-guided-tour:hover {
      background: color-mix(in srgb, #38bdf8 30%, transparent) !important;
      border-color: #38bdf8 !important;
    }

    /* Responsive styling for mobile (including Samsung Galaxy Z Fold at 412px wide) */
    @media (max-width: 600px) {
      #test-tour-player {
        bottom: 3.8rem;
        width: calc(100vw - 1rem);
        border-radius: 10px;
      }
      .tour-player-header {
        padding: 0.55rem 0.75rem 0.4rem;
      }
      .tour-step-title {
        font-size: 0.85rem;
      }
      .tour-intro-box {
        margin: 0.4rem 0.75rem 0.35rem;
        padding: 0.5rem 0.65rem;
      }
      .tour-intro-title {
        font-size: 0.7rem;
      }
      .tour-intro-text {
        font-size: 0.75rem;
      }
      .tour-test-row {
        margin: 0.2rem 0.75rem 0.45rem;
      }
      .tour-suite-badge {
        font-size: 0.65rem;
      }
      .tour-player-controls {
        padding: 0.4rem 0.75rem 0.5rem;
        gap: 0.3rem;
      }
      .tour-btn {
        padding: 0.3rem 0.5rem;
        font-size: 0.7rem;
      }
    }
`;

const INDEX_VECTOR_BTN_CSS = `
    /* Play Test Vector in Interactive Gates Tour */
    .btn-play-test-vector {
      display: inline-flex;
      align-items: center;
      gap: 0.45rem;
      margin-top: 0.85rem;
      padding: 0.45rem 0.85rem;
      font-size: 0.8rem;
      font-weight: 600;
      color: #38bdf8;
      background: color-mix(in srgb, #38bdf8 12%, transparent);
      border: 1px solid color-mix(in srgb, #38bdf8 40%, transparent);
      border-radius: 6px;
      text-decoration: none;
      transition: all 0.18s ease;
      cursor: pointer;
    }
    .btn-play-test-vector:hover {
      background: color-mix(in srgb, #38bdf8 26%, transparent);
      border-color: #38bdf8;
      color: #7dd3fc;
      transform: translateY(-1px);
    }
`;

/* ============================================================
   Player Markup & Controller Generator
   ============================================================ */

function generatePlayerMarkupAndScript(options) {
  const {
    steps,
    introHeading = '¿Para qué sirve este test?',
    testLabel = 'Prueba Go:',
    tourTitle = 'Tour guiado interactivo',
    unitType = 'Paso'
  } = options;

  return `
  <!-- Interactive Test Tour Player -->
  <aside id="test-tour-player" class="no-print" aria-label="${escapeHtml(tourTitle)}" hidden>
    <div class="tour-player-header">
      <div class="tour-header-left">
        <div class="tour-header-top-row">
          <span class="tour-step-badge" id="tour-step-badge">${unitType} 01 de ${steps.length < 10 ? '0' : ''}${steps.length}</span>
          <span class="tour-phase-badge" id="tour-phase-badge"></span>
        </div>
        <h3 class="tour-step-title" id="tour-step-title"></h3>
      </div>
      <button type="button" class="tour-btn-close" id="tour-btn-close" aria-label="Cerrar tour interactivo">✕</button>
    </div>

    <div class="tour-intro-box">
      <div class="tour-intro-title">
        <span class="tour-intro-icon">💡</span>
        <span id="tour-intro-heading">${escapeHtml(introHeading)}</span>
      </div>
      <p class="tour-intro-text" id="tour-intro-text"></p>
    </div>

    <div class="tour-test-row">
      <span class="tour-test-label" id="tour-test-label">${escapeHtml(testLabel)}</span>
      <code class="tour-suite-badge" id="tour-suite-badge"></code>
    </div>

    <div class="tour-player-controls">
      <div class="tour-controls-left">
        <button type="button" class="tour-btn" id="tour-btn-prev" aria-label="Paso anterior" title="Paso anterior (←)">⏮ Anterior</button>
        <button type="button" class="tour-btn tour-btn-play" id="tour-btn-toggle" aria-label="Pausar o reanudar" title="Pausar / Reanudar (Espacio)">⏸ Pausar</button>
        <button type="button" class="tour-btn" id="tour-btn-next" aria-label="Paso siguiente" title="Paso siguiente (→)">Siguiente ⏭</button>
      </div>
      <span class="tour-timer-label" id="tour-timer-label" aria-live="polite">5s</span>
    </div>

    <div class="tour-progress-track" aria-hidden="true">
      <div class="tour-progress-bar" id="tour-progress-bar"></div>
    </div>
  </aside>

  <script>
    /* ============================================================
       Interactive Tour Player Controller
       ============================================================ */
    (function () {
      var STEPS = ${JSON.stringify(steps, null, 2)};
      var UNIT_TYPE = ${JSON.stringify(unitType)};

      var currentStepIndex = 0;
      var isPlaying = false;
      var isHovered = false;
      var dwellMs = 5000;
      var remainingMs = 5000;
      var lastTick = 0;
      var animFrameId = null;

      var player = document.getElementById("test-tour-player");
      if (!player) return;

      var stepBadge = document.getElementById("tour-step-badge");
      var phaseBadge = document.getElementById("tour-phase-badge");
      var titleEl = document.getElementById("tour-step-title");
      var introEl = document.getElementById("tour-intro-text");
      var suiteBadge = document.getElementById("tour-suite-badge");
      var timerLabel = document.getElementById("tour-timer-label");
      var progressBar = document.getElementById("tour-progress-bar");
      var btnPrev = document.getElementById("tour-btn-prev");
      var btnNext = document.getElementById("tour-btn-next");
      var btnToggle = document.getElementById("tour-btn-toggle");
      var btnClose = document.getElementById("tour-btn-close");
      var btnNavTour = document.getElementById("btn-nav-tour");
      var btnGuidedTour = document.getElementById("btn-guided-tour");

      function updateProgressUI(pct, secs) {
        if (progressBar) progressBar.style.width = pct + "%";
        if (timerLabel) timerLabel.textContent = secs + "s";
      }

      function renderStep(index, syncFocus) {
        currentStepIndex = Math.max(0, Math.min(STEPS.length - 1, index));
        var step = STEPS[currentStepIndex];

        var curNum = (currentStepIndex + 1 < 10 ? "0" : "") + (currentStepIndex + 1);
        var totNum = (STEPS.length < 10 ? "0" : "") + STEPS.length;
        if (stepBadge) stepBadge.textContent = UNIT_TYPE + " " + curNum + " de " + totNum;
        if (phaseBadge) phaseBadge.textContent = step.phase ? (step.phase + " · " + (step.badge || "")) : (step.badge || "");
        if (titleEl) titleEl.textContent = step.label;
        if (introEl) introEl.textContent = step.intro || step.what || "";
        if (suiteBadge) suiteBadge.textContent = step.test || step.testBadge || "";

        if (btnPrev) btnPrev.disabled = currentStepIndex === 0;
        if (btnNext) btnNext.disabled = currentStepIndex === STEPS.length - 1;

        if (syncFocus !== false && window.Archify && Archify.focus && typeof Archify.focus.set === "function") {
          Archify.focus.set(step.id, { updateUrl: false, preserveView: true });
        }
        if (syncFocus !== false && window.Archify && Archify.view && typeof Archify.view.reveal === "function") {
          try { Archify.view.reveal([step.id], { includeNeighbors: true, reason: "tour" }); } catch (_) {}
        }
      }

      function onTick(now) {
        if (!isPlaying) return;
        var delta = now - lastTick;
        lastTick = now;

        if (!isHovered) {
          remainingMs -= delta;
          if (remainingMs <= 0) {
            remainingMs = 0;
            updateProgressUI(100, 0);
            if (currentStepIndex < STEPS.length - 1) {
              startTour(currentStepIndex + 1, true);
            } else {
              pauseTour();
              if (timerLabel) timerLabel.textContent = "Fin";
            }
            return;
          }
          var pct = Math.min(100, Math.max(0, ((dwellMs - remainingMs) / dwellMs) * 100));
          var secs = Math.ceil(remainingMs / 1000);
          updateProgressUI(pct, secs);
        }

        animFrameId = requestAnimationFrame(onTick);
      }

      function startCountdown() {
        isPlaying = true;
        remainingMs = dwellMs;
        lastTick = performance.now();
        if (btnToggle) {
          btnToggle.textContent = "⏸ Pausar";
          btnToggle.setAttribute("aria-label", "Pausar tour");
        }
        updateProgressUI(0, 5);
        if (animFrameId) cancelAnimationFrame(animFrameId);
        animFrameId = requestAnimationFrame(onTick);
      }

      function pauseTour() {
        isPlaying = false;
        if (animFrameId) cancelAnimationFrame(animFrameId);
        if (btnToggle) {
          btnToggle.textContent = "▶ Reanudar";
          btnToggle.setAttribute("aria-label", "Reanudar tour");
        }
      }

      function resumeTour() {
        if (remainingMs <= 0) {
          if (currentStepIndex < STEPS.length - 1) {
            startTour(currentStepIndex + 1, true);
            return;
          }
          remainingMs = dwellMs;
        }
        isPlaying = true;
        lastTick = performance.now();
        if (btnToggle) {
          btnToggle.textContent = "⏸ Pausar";
          btnToggle.setAttribute("aria-label", "Pausar tour");
        }
        if (animFrameId) cancelAnimationFrame(animFrameId);
        animFrameId = requestAnimationFrame(onTick);
      }

      function startTour(stepIndex, autoplay) {
        player.removeAttribute("hidden");
        player.style.display = "flex";

        var idx = typeof stepIndex === "number" ? stepIndex : currentStepIndex;
        renderStep(idx, true);

        if (autoplay !== false) {
          startCountdown();
        } else {
          pauseTour();
        }
      }

      function nextStep() {
        if (currentStepIndex < STEPS.length - 1) {
          startTour(currentStepIndex + 1, isPlaying);
        }
      }

      function prevStep() {
        if (currentStepIndex > 0) {
          startTour(currentStepIndex - 1, isPlaying);
        }
      }

      function stopTour() {
        isPlaying = false;
        if (animFrameId) cancelAnimationFrame(animFrameId);
        player.setAttribute("hidden", "");
        player.style.display = "none";
        updateProgressUI(0, 5);
      }

      // Event Listeners
      if (btnToggle) {
        btnToggle.addEventListener("click", function () {
          if (isPlaying) pauseTour();
          else resumeTour();
        });
      }
      if (btnNext) btnNext.addEventListener("click", nextStep);
      if (btnPrev) btnPrev.addEventListener("click", prevStep);
      if (btnClose) btnClose.addEventListener("click", stopTour);

      player.addEventListener("mouseenter", function () {
        isHovered = true;
      });
      player.addEventListener("mouseleave", function () {
        isHovered = false;
        lastTick = performance.now();
      });

      if (btnNavTour) {
        btnNavTour.addEventListener("click", function () {
          startTour(0, true);
        });
      }
      if (btnGuidedTour) {
        btnGuidedTour.addEventListener("click", function () {
          startTour(0, true);
        });
      }

      // Keyboard navigation
      document.addEventListener("keydown", function (e) {
        if (player.hasAttribute("hidden") || player.style.display === "none") return;
        if (e.target && (e.target.tagName === "INPUT" || e.target.tagName === "TEXTAREA" || e.target.tagName === "SELECT")) return;

        if (e.key === "Escape") {
          e.preventDefault();
          stopTour();
        } else if (e.key === " " || e.code === "Space") {
          e.preventDefault();
          if (isPlaying) pauseTour();
          else resumeTour();
        } else if (e.key === "ArrowLeft") {
          e.preventDefault();
          prevStep();
        } else if (e.key === "ArrowRight") {
          e.preventDefault();
          nextStep();
        }
      });

      // Deep link support (?play=<id>, ?tour=true, #play=<id>, #tour=true)
      function checkDeepLink() {
        var params = new URLSearchParams(window.location.search);
        var playId = params.get("play");
        var tourParam = params.get("tour");
        var hash = window.location.hash || "";

        if (!playId && hash) {
          var match = hash.match(/play=([^&]+)/);
          if (match) playId = match[1];
        }

        if (playId) {
          var foundIdx = STEPS.findIndex(function (s) { return s.id === playId; });
          if (foundIdx >= 0) {
            startTour(foundIdx, true);
            return;
          }
        }

        if (tourParam === "true" || hash.indexOf("tour=true") !== -1) {
          startTour(0, true);
        }
      }

      if (document.readyState === "loading") {
        document.addEventListener("DOMContentLoaded", checkDeepLink);
      } else {
        setTimeout(checkDeepLink, 100);
      }
      window.addEventListener("hashchange", checkDeepLink);

      // Expose globally on Archify
      if (!window.Archify) window.Archify = {};
      window.Archify.testPlayer = {
        start: function (idx, autoplay) { startTour(typeof idx === "number" ? idx : 0, autoplay !== false); },
        pause: pauseTour,
        resume: resumeTour,
        next: nextStep,
        prev: prevStep,
        stop: stopTour,
        isPlaying: function () { return isPlaying; }
      };
    })();
  </script>
`;
}

/* ============================================================
   Injection Logic
   ============================================================ */

/**
 * Pure transformation for architecture diagram HTML files.
 */
export function transformDiagramHtml(inputHtml, options) {
  let html = inputHtml;

  // 1. Inject CSS if not already present
  if (!html.includes('#test-tour-player')) {
    if (html.includes('  </style>\n</head>')) {
      html = html.replace('  </style>\n</head>', TOUR_PLAYER_CSS + '\n  </style>\n</head>');
    } else if (html.includes('</style>\n</head>')) {
      html = html.replace('</style>\n</head>', TOUR_PLAYER_CSS + '\n</style>\n</head>');
    }
  }

  // 2. Inject TOUR button in .diagram-nav
  const navNeedle = '<div class="diagram-nav no-print" role="toolbar" aria-label="Diagram view controls">';
  if (!html.includes('id="btn-nav-tour"') && html.includes(navNeedle)) {
    const navBtn = '<button id="btn-nav-tour" class="btn-nav-tour" type="button" title="' + escapeHtml(options.tourTitle || 'Reproducir tour guiado con intro explicativa') + '">TOUR</button>';
    html = html.replace(navNeedle, navNeedle + '\n        ' + navBtn);
  }

  // 3. Inject Tour button in .guided-view-actions
  const guidedNeedle = '<div class="guided-view-actions">';
  if (!html.includes('id="btn-guided-tour"') && html.includes(guidedNeedle)) {
    const guidedBtn = '<button id="btn-guided-tour" class="btn-guided-tour" type="button" title="Reproducir tour guiado con intro explicativa">🎬 Tour con Intro</button>';
    html = html.replace(guidedNeedle, guidedNeedle + '\n        ' + guidedBtn);
  }

  // 4. Inject markup and script before </body>
  if (!html.includes('id="test-tour-player"') && html.includes('</body>')) {
    const markupAndScript = generatePlayerMarkupAndScript(options);
    html = html.replace('</body>', markupAndScript + '\n</body>');
  }

  return html;
}

/**
 * Pure transformation for docs/architecture/index.html.
 */
export function transformIndexHtml(inputHtml) {
  let html = inputHtml;

  // 1. Inject button CSS if missing
  if (!html.includes('.btn-play-test-vector')) {
    if (html.includes('  </style>\n</head>')) {
      html = html.replace('  </style>\n</head>', INDEX_VECTOR_BTN_CSS + '\n  </style>\n</head>');
    } else if (html.includes('</style>\n</head>')) {
      html = html.replace('</style>\n</head>', INDEX_VECTOR_BTN_CSS + '\n</style>\n</head>');
    }
  }

  // 2. Inject play link inside each test-vector-item
  for (const { category, playId } of VECTOR_PLAY_MAPPING) {
    const linkHref = `gentle-mesh-test-verification-gates.html?play=${playId}`;
    if (html.includes(linkHref)) {
      continue;
    }

    // Pattern to locate the closing of vector-body within this details
    const categoryPattern = new RegExp(`(<details[^>]*data-category=["']${category}["'][\\s\\S]*?)(</div>\\s*</details>)`, 'i');
    const match = html.match(categoryPattern);
    if (match) {
      const linkTag = `\n            <a href="${linkHref}" class="btn-play-test-vector" title="Reproducir este test con intro en el diagrama">▶ Reproducir test con intro</a>\n          `;
      html = html.replace(categoryPattern, `$1${linkTag}$2`);
    }
  }

  return html;
}

/**
 * Injects tour player into an architecture diagram HTML file.
 */
function injectDiagramTourPlayer(filePath, options) {
  console.log(`Processing ${filePath}...`);
  const origHtml = fs.readFileSync(filePath, 'utf8');
  const transformedHtml = transformDiagramHtml(origHtml, options);
  const modified = origHtml !== transformedHtml;

  if (modified) {
    if (DRY_RUN) {
      console.log(`  [DRY-RUN] ${filePath} would be updated successfully.`);
    } else {
      fs.writeFileSync(filePath, transformedHtml, 'utf8');
      console.log(`  ✓ Updated ${filePath}`);
    }
  } else {
    console.log(`  (i) ${filePath} is already up to date.`);
  }

  return transformedHtml;
}

/**
 * Updates docs/architecture/index.html with play links in the 7 test vectors.
 */
function updateIndexPlayLinks(filePath) {
  console.log(`Processing ${filePath}...`);
  const origHtml = fs.readFileSync(filePath, 'utf8');
  const transformedHtml = transformIndexHtml(origHtml);
  const modified = origHtml !== transformedHtml;

  if (modified) {
    if (DRY_RUN) {
      console.log(`  [DRY-RUN] ${filePath} would be updated with play links.`);
    } else {
      fs.writeFileSync(filePath, transformedHtml, 'utf8');
      console.log(`  ✓ Updated ${filePath} with 7 play links`);
    }
  } else {
    console.log(`  (i) ${filePath} is already up to date.`);
  }

  return transformedHtml;
}

/* ============================================================
   Main Execution
   ============================================================ */

function main() {
  console.log(`=== Injecting Interactive Test Tour Player ${DRY_RUN ? '(DRY-RUN MODE)' : ''} ===`);

  const gatesHtmlPath = 'docs/architecture/gentle-mesh-test-verification-gates.html';
  const secureEnvHtmlPath = 'docs/architecture/gentle-mesh-secure-environment.html';
  const indexHtmlPath = 'docs/architecture/index.html';

  const gatesHtml = injectDiagramTourPlayer(gatesHtmlPath, {
    steps: VERIFICATION_GATES_STEPS,
    introHeading: '¿Para qué sirve este test?',
    testLabel: 'Prueba Go:',
    tourTitle: 'Reproducir tour guiado de pruebas con intro explicativa',
    unitType: 'Paso'
  });

  const secureEnvHtml = injectDiagramTourPlayer(secureEnvHtmlPath, {
    steps: SECURE_ENV_STEPS,
    introHeading: '¿Para qué sirve este componente?',
    testLabel: 'Verificación Go:',
    tourTitle: 'Reproducir tour guiado de componentes con intro explicativa',
    unitType: 'Componente'
  });

  const indexHtml = updateIndexPlayLinks(indexHtmlPath);

  // Verification assertions in dry-run mode
  if (DRY_RUN) {
    console.log('\n--- Verifying transformation assertions ---');

    // Gates HTML checks
    if (!gatesHtml.includes('id="test-tour-player"')) throw new Error('gatesHtml missing #test-tour-player');
    if (!gatesHtml.includes('id="btn-nav-tour"')) throw new Error('gatesHtml missing #btn-nav-tour');
    if (!gatesHtml.includes('id="btn-guided-tour"')) throw new Error('gatesHtml missing #btn-guided-tour');
    if (!gatesHtml.includes('Archify.testPlayer =')) throw new Error('gatesHtml missing Archify.testPlayer API');

    // Secure Env HTML checks
    if (!secureEnvHtml.includes('id="test-tour-player"')) throw new Error('secureEnvHtml missing #test-tour-player');
    if (!secureEnvHtml.includes('id="btn-nav-tour"')) throw new Error('secureEnvHtml missing #btn-nav-tour');
    if (!secureEnvHtml.includes('id="btn-guided-tour"')) throw new Error('secureEnvHtml missing #btn-guided-tour');
    if (!secureEnvHtml.includes('Archify.testPlayer =')) throw new Error('secureEnvHtml missing Archify.testPlayer API');

    // Index HTML checks
    if (!indexHtml.includes('btn-play-test-vector')) throw new Error('indexHtml missing btn-play-test-vector');
    for (const { playId } of VECTOR_PLAY_MAPPING) {
      if (!indexHtml.includes(`gentle-mesh-test-verification-gates.html?play=${playId}`)) {
        throw new Error(`indexHtml missing play link for ${playId}`);
      }
    }

    // Idempotence checks: running transformation a second time should produce identical HTML
    const gatesPass2 = transformDiagramHtml(gatesHtml, {
      steps: VERIFICATION_GATES_STEPS,
      introHeading: '¿Para qué sirve este test?',
      testLabel: 'Prueba Go:',
      tourTitle: 'Reproducir tour guiado de pruebas con intro explicativa',
      unitType: 'Paso'
    });
    if (gatesPass2 !== gatesHtml) throw new Error('gatesHtml transformation is not idempotent');

    const secureEnvPass2 = transformDiagramHtml(secureEnvHtml, {
      steps: SECURE_ENV_STEPS,
      introHeading: '¿Para qué sirve este componente?',
      testLabel: 'Verificación Go:',
      tourTitle: 'Reproducir tour guiado de componentes con intro explicativa',
      unitType: 'Componente'
    });
    if (secureEnvPass2 !== secureEnvHtml) throw new Error('secureEnvHtml transformation is not idempotent');

    const indexPass2 = transformIndexHtml(indexHtml);
    if (indexPass2 !== indexHtml) throw new Error('indexHtml transformation is not idempotent');

    console.log('✓ All 10 specifications and idempotency verified cleanly in dry run!');
  }

  console.log('\n=== Completed successfully ===');
}

main();
