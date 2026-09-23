import fs from 'node:fs';

const TARGET_PATH = 'docs/architecture/gentle-mesh-secure-environment.html';

let html = fs.readFileSync(TARGET_PATH, 'utf8');

if (html.includes('id="step-explainer-modal"')) {
  console.log('Step Explainer Modal is already injected into ' + TARGET_PATH);
  process.exit(0);
}

// 1. Modal CSS to inject inside <style> right before </style>\n</head>
const modalCss = `
    /* Step Explainer Modal & Trigger Buttons */
    .btn-step-guide-toolbar {
      font-weight: 700 !important;
      color: #a5b4fc !important;
      border-color: color-mix(in srgb, #6366f1 45%, var(--toolbar-border)) !important;
      background: color-mix(in srgb, #6366f1 15%, transparent) !important;
    }
    .btn-step-guide-toolbar:hover {
      background: color-mix(in srgb, #6366f1 30%, transparent) !important;
      border-color: #818cf8 !important;
    }
    .btn-focus-explain {
      appearance: none;
      font-family: inherit;
      font-size: 0.6875rem;
      font-weight: 600;
      padding: 0.25rem 0.55rem;
      border-radius: 0.25rem;
      border: 1px solid color-mix(in srgb, #6366f1 50%, var(--toolbar-border));
      background: color-mix(in srgb, #6366f1 18%, transparent);
      color: var(--text);
      cursor: pointer;
      transition: all 0.15s ease;
      white-space: nowrap;
    }
    .btn-focus-explain:hover {
      background: color-mix(in srgb, #6366f1 32%, transparent);
      border-color: #818cf8;
    }
    .card[data-step-trigger="true"] {
      cursor: pointer;
      transition: border-color 0.18s ease, transform 0.18s ease;
    }
    .card[data-step-trigger="true"]:hover {
      border-color: #818cf8;
      transform: translateY(-1px);
    }
    .card-explain-badge {
      font-size: 0.6875rem;
      font-weight: 600;
      opacity: 0.85;
      margin-left: auto;
      color: #a5b4fc;
    }

    /* Modal Element */
    dialog.step-explainer-modal:not([open]) {
      display: none !important;
    }
    .step-explainer-modal {
      border: none;
      padding: 0;
      background: transparent;
      max-width: 680px;
      width: calc(100vw - 32px);
      max-height: 88vh;
      margin: auto;
      border-radius: 12px;
      box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.85), 0 0 0 1px rgba(255, 255, 255, 0.12);
      color: var(--text);
      overflow: hidden;
      z-index: 1000;
    }
    .step-explainer-modal::backdrop {
      background: rgba(3, 7, 18, 0.78);
      backdrop-filter: blur(8px);
      -webkit-backdrop-filter: blur(8px);
    }
    .step-modal-wrapper {
      display: flex;
      flex-direction: column;
      max-height: 88vh;
      background: color-mix(in srgb, var(--surface) 92%, #0b1120);
      border-radius: 12px;
      overflow: hidden;
    }
    .step-modal-header {
      padding: 1.1rem 1.25rem 0.9rem;
      border-bottom: 1px solid color-mix(in srgb, var(--toolbar-border) 80%, rgba(255, 255, 255, 0.08));
      background: color-mix(in srgb, var(--surface) 70%, #1e293b 30%);
    }
    .step-modal-header-top {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 0.5rem;
      margin-bottom: 0.4rem;
    }
    .step-modal-badge {
      font-size: 0.65rem;
      font-weight: 700;
      letter-spacing: 0.06em;
      text-transform: uppercase;
      padding: 0.18rem 0.5rem;
      border-radius: 999px;
      background: color-mix(in srgb, #6366f1 20%, transparent);
      border: 1px solid color-mix(in srgb, #6366f1 45%, transparent);
      color: #a5b4fc;
    }
    .step-modal-counter {
      font-size: 0.72rem;
      font-family: monospace;
      color: var(--text-muted);
      font-weight: 600;
    }
    .step-modal-close {
      background: transparent;
      border: none;
      font-size: 1.2rem;
      color: var(--text-muted);
      cursor: pointer;
      padding: 0.2rem 0.5rem;
      border-radius: 4px;
      margin-left: auto;
      line-height: 1;
    }
    .step-modal-close:hover {
      color: var(--text);
      background: rgba(255, 255, 255, 0.1);
    }
    .step-modal-title {
      margin: 0;
      font-size: 1.15rem;
      font-weight: 700;
      color: var(--text);
    }
    .step-modal-sublabel {
      font-size: 0.8rem;
      color: var(--text-muted);
      margin-top: 0.2rem;
    }
    .step-modal-body {
      padding: 1.1rem 1.25rem;
      overflow-y: auto;
      display: flex;
      flex-direction: column;
      gap: 0.9rem;
      line-height: 1.5;
    }
    .step-section {
      padding: 0.85rem 0.95rem;
      border-radius: 8px;
      background: color-mix(in srgb, var(--surface) 60%, rgba(255, 255, 255, 0.03));
      border: 1px solid color-mix(in srgb, var(--toolbar-border) 60%, transparent);
    }
    .step-section-heading {
      display: flex;
      align-items: center;
      gap: 0.4rem;
      font-size: 0.82rem;
      font-weight: 700;
      margin-bottom: 0.4rem;
    }
    .what-section .step-section-heading { color: #38bdf8; }
    .why-section .step-section-heading { color: #f43f5e; }
    .test-section .step-section-heading { color: #34d399; }
    .step-section-text {
      margin: 0;
      font-size: 0.8rem;
      color: var(--text);
    }
    .step-section-code {
      display: block;
      font-family: monospace;
      font-size: 0.73rem;
      padding: 0.35rem 0.5rem;
      background: rgba(0, 0, 0, 0.35);
      border-radius: 4px;
      color: #6ee7b7;
      border: 1px solid rgba(52, 211, 153, 0.2);
      word-break: break-all;
    }
    .step-modal-footer {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 0.5rem;
      padding: 0.85rem 1.25rem;
      border-top: 1px solid color-mix(in srgb, var(--toolbar-border) 80%, rgba(255, 255, 255, 0.08));
      background: color-mix(in srgb, var(--surface) 80%, #0b1120 20%);
    }
    .step-nav-btn {
      padding: 0.45rem 0.85rem;
      font-family: inherit;
      font-size: 0.78rem;
      font-weight: 600;
      border-radius: 6px;
      border: 1px solid color-mix(in srgb, var(--toolbar-border) 80%, transparent);
      background: rgba(255, 255, 255, 0.07);
      color: var(--text);
      cursor: pointer;
      transition: all 0.15s ease;
      white-space: nowrap;
    }
    .step-nav-btn:hover:not(:disabled) {
      background: rgba(255, 255, 255, 0.14);
      border-color: #818cf8;
    }
    .step-nav-btn:disabled {
      opacity: 0.4;
      cursor: not-allowed;
    }
    .step-selector {
      font-family: inherit;
      font-size: 0.75rem;
      padding: 0.4rem 0.6rem;
      border-radius: 6px;
      border: 1px solid color-mix(in srgb, var(--toolbar-border) 80%, transparent);
      background: color-mix(in srgb, var(--surface) 90%, #0f172a);
      color: var(--text);
      max-width: 250px;
    }
    @media (max-width: 600px) {
      .step-modal-footer {
        flex-wrap: wrap;
      }
      .step-selector {
        order: 3;
        width: 100%;
        max-width: 100%;
        margin-top: 0.3rem;
      }
      .step-nav-btn {
        flex: 1;
        text-align: center;
      }
    }
`;

// Inject CSS inside <style> right before </style>\n</head>
const styleNeedle = html.includes('  </style>\n</head>') ? '  </style>\n</head>' : '</style>\n</head>';
html = html.replace(styleNeedle, modalCss + '\n' + styleNeedle);

// 2. Add button to .diagram-nav toolbar
const navNeedle = '<div class="diagram-nav no-print" role="toolbar" aria-label="Diagram view controls">';
const navReplacement = navNeedle + '\n        <button id="btn-step-guide" class="btn-step-guide-toolbar" type="button" aria-haspopup="dialog" aria-expanded="false" title="Explicación paso a paso de cada nodo de la topología: ¿Qué hace y por qué existe?">STEPS</button>';
html = html.replace(navNeedle, navReplacement);

// 3. Add button to Semantic Passport right after Relations button
const lensNeedle = '<button id="btn-focus-relations" type="button" aria-label="Show connected relationships" aria-expanded="false" aria-controls="relationship-lens-list">Relations</button>';
const lensReplacement = lensNeedle + '\n            <button id="btn-focus-explain" class="btn-focus-explain" type="button" aria-label="Ver explicación técnica paso a paso" title="Ver qué hace y por qué existe este nodo">📖 Explicación</button>';
html = html.replace(lensNeedle, lensReplacement);

// 4. Add trigger attribute and badges to cards
const card1Needle = '<div class="card">\n        <div class="card-header">\n          <div class="card-dot rose"></div>\n          <h3>Hardening de Admisión y Territorio</h3>';
const card1Replacement = '<div class="card" id="card-admission" data-step-trigger="true" title="Clic para ver explicación de Admisión y Territorio">\n        <div class="card-header">\n          <div class="card-dot rose"></div>\n          <h3>Hardening de Admisión y Territorio <span class="card-explain-badge">📖 Ver admisión</span></h3>';
html = html.replace(card1Needle, card1Replacement);

const card2Needle = '<div class="card">\n        <div class="card-header">\n          <div class="card-dot emerald"></div>\n          <h3>Clúster Pentagonal Federado</h3>';
const card2Replacement = '<div class="card" id="card-cluster" data-step-trigger="true" title="Clic para ver explicación del Clúster Pentagonal">\n        <div class="card-header">\n          <div class="card-dot emerald"></div>\n          <h3>Clúster Pentagonal Federado <span class="card-explain-badge">📖 Ver clúster</span></h3>';
html = html.replace(card2Needle, card2Replacement);

// 5. Modal Dialog HTML and Controller to inject right before </body>
const modalAndScript = `
  <!-- Modal Dialog: Explicación Paso a Paso de Nodos de la Topología Pentagonal -->
  <dialog id="step-explainer-modal" class="step-explainer-modal no-print" aria-labelledby="step-modal-title">
    <div class="step-modal-wrapper">
      <div class="step-modal-header">
        <div class="step-modal-header-top">
          <span class="step-modal-badge" id="step-modal-badge">Fase 1: Admisión y Hardening</span>
          <span class="step-modal-counter" id="step-modal-counter">Paso 01 de 11</span>
          <button type="button" class="step-modal-close" id="step-modal-close" aria-label="Cerrar modal de explicación">✕</button>
        </div>
        <h2 class="step-modal-title" id="step-modal-title">Gentle Client CLI</h2>
        <div class="step-modal-sublabel" id="step-modal-sublabel">gentle-mesh run / Pi Subagent SDK</div>
      </div>

      <div class="step-modal-body">
        <div class="step-section what-section">
          <div class="step-section-heading">
            <span class="step-icon">🎯</span>
            <strong>¿Qué hace este componente?</strong>
          </div>
          <p id="step-modal-what" class="step-section-text"></p>
        </div>

        <div class="step-section why-section">
          <div class="step-section-heading">
            <span class="step-icon">🛡️</span>
            <strong>¿Por qué existe? (Riesgo o propósito técnico)</strong>
          </div>
          <p id="step-modal-why" class="step-section-text"></p>
        </div>

        <div class="step-section test-section">
          <div class="step-section-heading">
            <span class="step-icon">🧪</span>
            <strong>Verificación / Test en Go</strong>
          </div>
          <code id="step-modal-test" class="step-section-code"></code>
        </div>
      </div>

      <div class="step-modal-footer">
        <button type="button" class="step-nav-btn" id="step-btn-prev" aria-label="Paso anterior">← Anterior</button>
        <div class="step-selector-container">
          <select id="step-selector" aria-label="Seleccionar nodo a inspeccionar" class="step-selector">
            <!-- populated dynamically -->
          </select>
        </div>
        <button type="button" class="step-nav-btn" id="step-btn-next" aria-label="Paso siguiente">Siguiente →</button>
      </div>
    </div>
  </dialog>

  <script>
    /* ============================================================
       Topology Step Explainer Modal Controller
       ============================================================ */
    (function () {
      var GENTLE_MESH_STEPS = [
        {
          id: "client",
          label: "Gentle Client CLI (Pi Agent Harness)",
          sublabel: "gentle-mesh run / Pi Subagent SDK",
          phase: "Perímetro Cliente & Despacho",
          badge: "Cliente CLI / Subagent",
          what: "Punto de entrada para el desarrollador y el harness de subagentes de Pi. Envía la tarea mediante HTTP POST /v1/tasks especificando el manifiesto territorial (git_repo, branch, edit_surfaces), ruteo por tags y consume los eventos en streaming en tiempo real vía Server-Sent Events (SSE).",
          why: "Desacopla la sesión interactiva del orquestador local. Permite delegar tareas pesadas o concurrentes a nodos de cómputo dedicados sin agotar recursos locales de CPU/RAM ni bloquear la terminal interactiva de trabajo.",
          test: "cmd/gentle-mesh/main_test.go -> TestRun_DispatchAndSSEConsumption, TestRun_DomainBlastRadiusSurfacesFlags"
        },
        {
          id: "auth_gate",
          label: "Security Shield (Bearer timing-safe & MaxBytes)",
          sublabel: "Autenticación Bearer en tiempo constante + 1MB limit",
          phase: "Fase 1: Admisión y Hardening",
          badge: "crypto/subtle & Hardening",
          what: "Valida el encabezado Authorization: Bearer <token> mediante crypto/subtle.ConstantTimeCompare contra el token secreto configurado. Además, envuelve el cuerpo HTTP con http.MaxBytesReader(w, r.Body, 1<<20) limitando el payload a un máximo estricto de 1MB.",
          why: "Previene ataques de temporización (timing attacks) de canal lateral que deducen el secreto analizando variaciones de nanosegundos en la respuesta. Mitiga ataques DoS por agotamiento de memoria ante payloads sobredimensionados.",
          test: "pkg/server/http/server_test.go -> TestServer_AuthBearer, TestServer_MaxBytesReader"
        },
        {
          id: "territory_gate",
          label: "Territory Gate (Evaluación de Conflicto)",
          sublabel: "FindConflict en superficies y ramas concurrentes",
          phase: "Fase 1: Admisión y Hardening",
          badge: "HTTP 409 Conflict",
          what: "Evalúa la tupla territorial (git_repo, branch, edit_surfaces) antes de admitir la tarea. Normaliza esquemas Git (HTTPS vs SSH) y busca solapamientos contra tareas activas locales y remotas mediante FindConflict(territory).",
          why: "En desarrollo multi-agente ('crear sistemas entre varios'), si dos agentes editan simultáneamente las mismas superficies o ramas, generan colisiones destructivas y merge conflicts irresolubles. El territorio garantiza exclusividad espacial en el árbol Git.",
          test: "pkg/server/http/server_test.go -> TestServer_TerritorySurfaceOverlapConflict, pkg/server/federation/manager_test.go -> TestTerritoryManager_FindConflict_LocalAndFederated"
        },
        {
          id: "branch_lock",
          label: "Branch Lock Manager",
          sublabel: "Normalización canónica de Git y exclusión mutua",
          phase: "Fase 1: Admisión y Hardening",
          badge: "Race-free Git",
          what: "Adquiere un cerrojo exclusivo en memoria (sync.Mutex) para la tupla normalizada (repositorio, rama). Normaliza URLs de Git eliminando prefijos y sufijos .git para asegurar identidad unívoca.",
          why: "Garantiza exclusión mutua sobre el puntero HEAD de Git, previniendo condiciones de carrera, commits cruzados desordenados y corrupción del árbol de ramas cuando múltiples subagentes operan sobre el mismo repositorio.",
          test: "pkg/server/registry/lock_test.go -> TestBranchLockManager_BasicClaimAndRelease, TestBranchLockManager_RepoNormalizationVariations"
        },
        {
          id: "task_store",
          label: "Task Log Store",
          sublabel: "JSONL append-only con fsync selectivo",
          phase: "Fase 1: Admisión y Hardening",
          badge: "Crash-Safe I/O",
          what: "Registro persistente en disco en formato JSONL append-only. Escribe de manera secuencial cada evento y ejecuta file.Sync() selectivo en transiciones de estado críticas (pending -> running -> completed) y eventos clave.",
          why: "Protege la durabilidad de los logs ante caídas imprevistas del proceso o cortes de energía sin degradar la vida útil del SSD por fsync excesivo en cada token, garantizando auditoría crash-safe completa de la ejecución.",
          test: "pkg/server/task/logger_test.go -> TestJSONLLogger_SelectiveSync, pkg/server/task/manager_test.go -> TestTaskManager_TrackRunnerGracefulShutdown"
        },
        {
          id: "coordinator",
          label: "Central Coordinator",
          sublabel: "HTTP REST + SSE + Idempotencia",
          phase: "Fase 2: Coordinación y Despacho",
          badge: "Orchestrator Hub",
          what: "Núcleo de orquestación en Go. Expone endpoints REST y streaming SSE, gestiona el ciclo de vida de tareas, aplica deduplicación con Idempotency-Key y despacha las cargas a workers especializados mediante el MeshRunner.",
          why: "Centraliza la máquina de estados, el balanceo de carga ponderado por tags y la reanudación transparente mediante Last-Event-ID, permitiendo que la malla sea escalable y tolerante a particiones de red.",
          test: "pkg/server/http/server_test.go -> TestServer_TaskCreateAndEventsStreaming, TestServer_Idempotency, TestServer_LastEventIDReconnection"
        },
        {
          id: "worker_alpha",
          label: "Worker Alpha (Fast Execution)",
          sublabel: "Tags: fast, go · Concurrencia: 2",
          phase: "Fase 3: Malla Pentagonal de Workers",
          badge: "Alpha · fast/go",
          what: "Worker circular especializado en tareas rápidas de compilación Go, análisis de sintaxis y micro-ejecuciones de baja latencia con hasta 2 tareas concurrentes.",
          why: "Aísla las operaciones ligeras y de alta frecuencia de compilación para que no queden bloqueadas detrás de ejecuciones pesadas, suites de integración o modelos de ML.",
          test: "pkg/server/runner/mesh_test.go -> TestMeshRunner_SelectNodeSuccess, pkg/server/worker/server_test.go -> TestWorkerServer_ExecuteAndStreamSSE"
        },
        {
          id: "worker_beta",
          label: "Worker Beta (Heavy Execution)",
          sublabel: "Tag: heavy · Concurrencia: 4",
          phase: "Fase 3: Malla Pentagonal de Workers",
          badge: "Beta · heavy",
          what: "Worker circular de alta capacidad para pruebas masivas, linters completos y suites de empaquetado intensivas en CPU/RAM con concurrencia máxima de 4 tareas.",
          why: "Concentra el consumo intensivo de recursos de cómputo en un nodo dedicado de alta capacidad, protegiendo al coordinador y a los workers rápidos de saturación de CPU y agotamiento de memoria.",
          test: "pkg/server/runner/mesh_test.go -> TestMeshRunner_SelectNodeSuccess, pkg/server/worker/server_test.go -> TestWorkerServer_ExecuteAndStreamSSE"
        },
        {
          id: "worker_gamma",
          label: "Worker Gamma (Research Execution)",
          sublabel: "Tag: research · Concurrencia: 2",
          phase: "Fase 3: Malla Pentagonal de Workers",
          badge: "Gamma · research",
          what: "Worker circular asignado a agentes de exploración de repositorios, indexación semántica y análisis de grafos de código (codegraph / blast radius) con concurrencia de 2 tareas.",
          why: "El análisis estático profundo y la navegación de dependencias consumen gran cantidad de contexto e I/O de lectura; su aislamiento previene interferencias con los ciclos de build y prueba continua.",
          test: "pkg/server/runner/mesh_test.go -> TestMeshRunner_Triangulation, pkg/server/worker/server_test.go -> TestWorkerServer_ExecuteAndStreamSSE"
        },
        {
          id: "worker_delta",
          label: "Worker Delta (GPU/ML Execution)",
          sublabel: "Tags: gpu, ml · Concurrencia: 1",
          phase: "Fase 3: Malla Pentagonal de Workers",
          badge: "Delta · gpu/ml",
          what: "Worker circular equipado para inferencia de modelos de lenguaje locales (LLMs) y aceleración por GPU. Configurado con concurrencia estricta de 1 tarea para preservar VRAM.",
          why: "La memoria gráfica en GPUs es finita y sensible a fallos Out-Of-Memory (OOM). La exclusión mutua (concurrencia 1) asegura que los modelos de lenguaje locales operen con estabilidad absoluta sin desbordar la memoria de la GPU.",
          test: "pkg/server/runner/mesh_test.go -> TestMeshRunner_SelectNodeSuccess, cmd/gentle-mesh/main_test.go -> TestWorker_JoinAndHeartbeatCycle"
        },
        {
          id: "worker_epsilon",
          label: "Worker Epsilon (Testing & CI Verification)",
          sublabel: "Tags: ci, test · Concurrencia: 2",
          phase: "Fase 3: Malla Pentagonal de Workers",
          badge: "Epsilon · ci/test",
          what: "Worker circular enfocado en la verificación automatizada continua, ejecución de go test -race y suites de integración con concurrencia de 2 tareas en red aislada.",
          why: "Garantiza un entorno estéril e independiente para certificar que el código compila y pasa todas las pruebas con detector de carreras de memoria activado antes de aceptar cambios o cerrar tareas.",
          test: "pkg/server/runner/mesh_test.go -> TestMeshRunner_FallbackWhenNoNodeAvailable, pkg/server/worker/server_test.go -> TestWorkerServer_ExecuteAndStreamSSE"
        }
      ];

      var currentStepIndex = 0;
      var modal = document.getElementById("step-explainer-modal");
      var badge = document.getElementById("step-modal-badge");
      var counter = document.getElementById("step-modal-counter");
      var title = document.getElementById("step-modal-title");
      var sublabel = document.getElementById("step-modal-sublabel");
      var whatEl = document.getElementById("step-modal-what");
      var whyEl = document.getElementById("step-modal-why");
      var testEl = document.getElementById("step-modal-test");
      var btnPrev = document.getElementById("step-btn-prev");
      var btnNext = document.getElementById("step-btn-next");
      var btnClose = document.getElementById("step-modal-close");
      var selector = document.getElementById("step-selector");
      var btnGuide = document.getElementById("btn-step-guide");
      var btnFocusExplain = document.getElementById("btn-focus-explain");
      var cardAdmission = document.getElementById("card-admission");
      var cardCluster = document.getElementById("card-cluster");

      if (!modal) return;

      // Populate selector
      selector.innerHTML = "";
      GENTLE_MESH_STEPS.forEach(function (step, idx) {
        var opt = document.createElement("option");
        opt.value = String(idx);
        opt.textContent = (idx + 1 < 10 ? "0" : "") + (idx + 1) + ". " + step.label.split(" (")[0];
        selector.appendChild(opt);
      });

      function renderStep(index, syncFocus) {
        currentStepIndex = Math.max(0, Math.min(GENTLE_MESH_STEPS.length - 1, index));
        var step = GENTLE_MESH_STEPS[currentStepIndex];
        badge.textContent = step.phase + " · " + step.badge;
        counter.textContent = "Paso " + (currentStepIndex + 1 < 10 ? "0" : "") + (currentStepIndex + 1) + " de " + (GENTLE_MESH_STEPS.length < 10 ? "0" : "") + GENTLE_MESH_STEPS.length;
        title.textContent = step.label;
        sublabel.textContent = step.sublabel;
        whatEl.textContent = step.what;
        whyEl.textContent = step.why;
        testEl.textContent = step.test;
        btnPrev.disabled = currentStepIndex === 0;
        btnNext.disabled = currentStepIndex === GENTLE_MESH_STEPS.length - 1;
        selector.value = String(currentStepIndex);

        if (syncFocus !== false && window.Archify && Archify.focus && typeof Archify.focus.set === "function") {
          Archify.focus.set(step.id, { updateUrl: false, preserveView: true });
        }
      }

      function openModal(index) {
        if (typeof index === "number") {
          renderStep(index, true);
        } else if (window.Archify && Archify.focus && typeof Archify.focus.active === "function") {
          var activeId = Archify.focus.active();
          if (Array.isArray(activeId)) activeId = activeId[0];
          var foundIdx = GENTLE_MESH_STEPS.findIndex(function (s) { return s.id === activeId; });
          if (foundIdx >= 0) renderStep(foundIdx, false);
          else renderStep(currentStepIndex, false);
        } else {
          renderStep(currentStepIndex, false);
        }
        if (typeof modal.showModal === "function") {
          modal.showModal();
        } else {
          modal.setAttribute("open", "");
        }
      }

      function closeModal() {
        if (typeof modal.close === "function") {
          modal.close();
        } else {
          modal.removeAttribute("open");
        }
      }

      btnPrev.addEventListener("click", function () {
        if (currentStepIndex > 0) renderStep(currentStepIndex - 1, true);
      });
      btnNext.addEventListener("click", function () {
        if (currentStepIndex < GENTLE_MESH_STEPS.length - 1) renderStep(currentStepIndex + 1, true);
      });
      selector.addEventListener("change", function () {
        renderStep(Number(selector.value), true);
      });
      btnClose.addEventListener("click", closeModal);
      modal.addEventListener("click", function (e) {
        if (e.target === modal) closeModal();
      });

      if (btnGuide) btnGuide.addEventListener("click", function () { openModal(); });
      if (btnFocusExplain) btnFocusExplain.addEventListener("click", function () { openModal(); });
      if (cardAdmission) cardAdmission.addEventListener("click", function () { openModal(1); }); // Step 1: auth_gate
      if (cardCluster) cardCluster.addEventListener("click", function () { openModal(5); }); // Step 5: coordinator

      // Keyboard navigation in modal
      modal.addEventListener("keydown", function (e) {
        if (e.key === "ArrowLeft" && currentStepIndex > 0) {
          e.preventDefault();
          renderStep(currentStepIndex - 1, true);
        } else if (e.key === "ArrowRight" && currentStepIndex < GENTLE_MESH_STEPS.length - 1) {
          e.preventDefault();
          renderStep(currentStepIndex + 1, true);
        }
      });

      // Expose globally
      if (window.Archify) {
        Archify.stepExplainer = {
          open: openModal,
          close: closeModal,
          step: function (idx) { renderStep(idx, true); }
        };
      }
    })();
  </script>
`;

html = html.replace('</body>', modalAndScript + '\n</body>');

fs.writeFileSync(TARGET_PATH, html, 'utf8');
console.log('Successfully injected Step Explainer Modal, Controls, and Script into ' + TARGET_PATH + '!');
