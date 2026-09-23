import fs from 'node:fs';

let html = fs.readFileSync('docs/architecture/gentle-mesh-test-verification-gates.html', 'utf8');

// 1. CSS to inject before </style>\n</head>
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
html = html.replace('  </style>\n</head>', modalCss + '\n  </style>\n</head>');

// Add button to .diagram-nav toolbar
const navNeedle = '<div class="diagram-nav no-print" role="toolbar" aria-label="Diagram view controls">';
const navReplacement = navNeedle + '\n        <button id="btn-step-guide" class="btn-step-guide-toolbar" type="button" aria-haspopup="dialog" aria-expanded="false" title="Explicación paso a paso de cada compuerta: ¿Qué se hace y por qué existe?">STEPS</button>';
html = html.replace(navNeedle, navReplacement);

// Add button to Semantic Passport .relationship-lens-actions
const lensNeedle = '<button id="btn-focus-relations" type="button" aria-label="Show connected relationships" aria-expanded="false" aria-controls="relationship-lens-list">Relations</button>';
const lensReplacement = lensNeedle + '\n            <button id="btn-focus-explain" class="btn-focus-explain" type="button" aria-label="Ver explicación técnica paso a paso" title="Ver qué se hace y por qué existe esta compuerta">📖 Explicación</button>';
html = html.replace(lensNeedle, lensReplacement);

// Add trigger attribute and badges to cards
html = html.replace(
  '<h3>Admisión y Seguridad</h3>',
  '<h3>Admisión y Seguridad <span class="card-explain-badge">📖 Ver 4 pasos</span></h3>'
);
html = html.replace(
  '<h3>Ejecución y Resiliencia</h3>',
  '<h3>Ejecución y Resiliencia <span class="card-explain-badge">📖 Ver 5 pasos</span></h3>'
);
html = html.replace(
  '<div class="card">\n        <div class="card-header">\n          <div class="card-dot rose"></div>\n          <h3>Admisión y Seguridad',
  '<div class="card" id="card-admission" data-step-trigger="true" title="Clic para ver explicación paso a paso de Admisión y Seguridad">\n        <div class="card-header">\n          <div class="card-dot rose"></div>\n          <h3>Admisión y Seguridad'
);
html = html.replace(
  '<div class="card">\n        <div class="card-header">\n          <div class="card-dot emerald"></div>\n          <h3>Ejecución y Resiliencia',
  '<div class="card" id="card-runtime" data-step-trigger="true" title="Clic para ver explicación paso a paso de Ejecución y Resiliencia">\n        <div class="card-header">\n          <div class="card-dot emerald"></div>\n          <h3>Ejecución y Resiliencia'
);

// Modal Dialog HTML and Controller to inject right before </body>
const modalAndScript = `
  <!-- Modal Dialog: Explicación Paso a Paso de Compuertas y Seguridad -->
  <dialog id="step-explainer-modal" class="step-explainer-modal no-print" aria-labelledby="step-modal-title">
    <div class="step-modal-wrapper">
      <div class="step-modal-header">
        <div class="step-modal-header-top">
          <span class="step-modal-badge" id="step-modal-badge">Fase 1: Admisión Segura</span>
          <span class="step-modal-counter" id="step-modal-counter">Paso 02 de 09</span>
          <button type="button" class="step-modal-close" id="step-modal-close" aria-label="Cerrar modal de explicación">✕</button>
        </div>
        <h2 class="step-modal-title" id="step-modal-title">G1: Auth Guard</h2>
        <div class="step-modal-sublabel" id="step-modal-sublabel">Autenticación Bearer en tiempo constante &amp; Límites MaxBytes</div>
      </div>

      <div class="step-modal-body">
        <div class="step-section what-section">
          <div class="step-section-heading">
            <span class="step-icon">🎯</span>
            <strong>¿Qué se hace en este paso?</strong>
          </div>
          <p id="step-modal-what" class="step-section-text"></p>
        </div>

        <div class="step-section why-section">
          <div class="step-section-heading">
            <span class="step-icon">🛡️</span>
            <strong>¿Por qué existe este paso? (Riesgo que mitiga)</strong>
          </div>
          <p id="step-modal-why" class="step-section-text"></p>
        </div>

        <div class="step-section test-section">
          <div class="step-section-heading">
            <span class="step-icon">🧪</span>
            <strong>Prueba en Go que lo verifica</strong>
          </div>
          <code id="step-modal-test" class="step-section-code"></code>
        </div>
      </div>

      <div class="step-modal-footer">
        <button type="button" class="step-nav-btn" id="step-btn-prev" aria-label="Paso anterior">← Anterior</button>
        <div class="step-selector-container">
          <select id="step-selector" aria-label="Seleccionar paso a inspeccionar" class="step-selector">
            <!-- populated dynamically -->
          </select>
        </div>
        <button type="button" class="step-nav-btn" id="step-btn-next" aria-label="Paso siguiente">Siguiente →</button>
      </div>
    </div>
  </dialog>

  <script>
    /* ============================================================
       Step Explainer Modal Controller
       ============================================================ */
    (function () {
      var GENTLE_MESH_STEPS = [
        {
          id: "test_runner",
          label: "Go Test Runner (go test -race)",
          sublabel: "Suite unit & int · 100% Pass",
          phase: "Capa de Verificación Automatizada",
          badge: "Test Harness",
          what: "Ejecuta concurrentemente más de 140 tests unitarios y de integración con detección de carreras de memoria (data race detector de Go). Valida el servidor HTTP, el streaming SSE, la máquina de estados de tareas, el locking de ramas y el protocolo M2M.",
          why: "En sistemas distribuidos y asíncronos en Go, las condiciones de carrera en mapas, punteros o canales son la causa #1 de corrupción de memoria y caídas impredecibles en producción. El flag -race instrumenta la compilación para garantizar thread-safety absoluto antes de desplegar.",
          test: "go test -race -count=1 ./... (140+ tests en <5s, 0 data races)"
        },
        {
          id: "gate_auth",
          label: "G1: Auth Guard (Bearer & MaxBytes)",
          sublabel: "Bearer Token timing-safe & Límite de Payload",
          phase: "Fase 1: Admisión Segura (Perímetro)",
          badge: "Seguridad & Perímetro",
          what: "Interpreta el encabezado Authorization: Bearer <token> y valida la clave contra la configuración mediante crypto/subtle.ConstantTimeCompare. Además, envuelve el cuerpo HTTP con http.MaxBytesReader(w, r.Body, 1<<20) limitando las peticiones a un máximo estricto de 1MB.",
          why: "Previene ataques de temporización (timing side-channel attacks) que permitirían a un atacante deducir el token analizando nanosegundos de respuesta. Además, mitiga denegaciones de servicio (DoS) por agotamiento de memoria RAM ante payloads gigantes.",
          test: "pkg/server/http/server_test.go -> TestServer_AuthBearer, TestServer_MaxBytesReader"
        },
        {
          id: "gate_territory",
          label: "G2: Territory (Evaluación de Conflicto)",
          sublabel: "FindConflict en superficies y ramas concurrentes",
          phase: "Fase 1: Admisión Segura (Malla & Coordinación)",
          badge: "Coordinación Espacial",
          what: "Antes de admitir la tarea, el TerritoryManager evalúa la tupla (git_repo, branch, edit_surfaces). Normaliza las URLs de Git (SSH vs HTTPS) y busca intersecciones contra todas las tareas activas locales y los manifiestos de nodos pares federados mediante FindConflict(territory).",
          why: "En entornos multi-agente ('crear sistemas entre varios'), si dos subagentes modifican los mismos archivos en paralelo, se producen colisiones destructivas, merge conflicts irresolubles y código roto. El territorio garantiza exclusividad espacial en el árbol de archivos.",
          test: "pkg/server/http/server_test.go -> TestServer_TerritorySurfaceOverlapConflict, pkg/server/federation/manager_test.go -> TestTerritoryManager_FindConflict_LocalAndFederated"
        },
        {
          id: "err_collision",
          label: "HTTP 409 Conflict (Rechazo de Colisión)",
          sublabel: "Rechazo determinista por solapamiento de territorio",
          phase: "Fase 1: Admisión Segura (Compuerta de Excepción)",
          badge: "Excepción / Fail-Fast",
          what: "Si FindConflict detecta que otra tarea activa ya está editando la misma superficie o rama en el repositorio, la petición se rechaza de inmediato devolviendo HTTP 409 Conflict y un payload JSON que detalla el ID de la tarea y nodo en colisión.",
          why: "Principio de diseño Fail-Fast: es infinitamente mejor rechazar una tarea en milisegundos en la puerta de entrada que permitir que un agente consuma cómputo y tokens de LLM para luego descubrir que su trabajo no puede fusionarse o corrompió al compañero.",
          test: "pkg/protocol/federation_test.go -> TestClashesWith_SurfaceOverlap, TestTerritoryConflictJSONSerialization"
        },
        {
          id: "gate_branch",
          label: "G3: Branch Lock (Bloqueo Exclusivo de Rama)",
          sublabel: "Locking en memoria con normalización de repositorio",
          phase: "Fase 2: Despacho Federado (Aislamiento de Rama)",
          badge: "Aislamiento Git",
          what: "El BranchLockManager adquiere un cerrojo exclusivo en memoria para la tupla normalizada (repositorio, rama). Si otra tarea intenta operar en la misma rama, es rechazada o retenida según la política configurada.",
          why: "Evita condiciones de carrera en el puntero de HEAD de Git y commits cruzados desordenados cuando múltiples agentes intentan generar cambios sobre una misma línea de desarrollo.",
          test: "pkg/server/registry/lock_test.go -> TestBranchLockManager_BasicClaimAndRelease, TestBranchLockManager_RepoNormalizationVariations"
        },
        {
          id: "gate_dispatch",
          label: "G4: Dispatcher & MeshRunner (Despacho Remoto)",
          sublabel: "Selección de nodo worker por tags y carga ponderada",
          phase: "Fase 2: Despacho Federado (Enrutamiento)",
          badge: "Despacho Distribuido",
          what: "El MeshRunner consulta el registro de nodos (NodeRegistry), filtra los workers activos que posean las etiquetas requeridas (remote, gpu, heavy, arm64), calcula el balanceo por carga y redirige la ejecución vía HTTP al worker seleccionado.",
          why: "Desacopla el consumo de CPU/GPU del orquestador local. Permite que una laptop ligera o dispositivo móvil delegue tareas pesadas a servidores dedicados sin bloquear su propia interfaz ni depender de su batería.",
          test: "pkg/server/runner/mesh_test.go -> TestMeshRunner_SelectNodeSuccess, TestMeshRunner_Triangulation"
        },
        {
          id: "err_panic",
          label: "Panic Recovery (Aislamiento de Crash)",
          sublabel: "defer recover() con SIGKILL en grupos de procesos POSIX",
          phase: "Fase 2: Despacho Federado (Compuerta de Excepción)",
          badge: "Resiliencia & Fail-Safe",
          what: "Cada ejecución de subproceso o runner se envuelve en un bloque protegido con defer func() { if r := recover(); r != nil { ... } }(). Si ocurre un pánico, se registra el stack trace, se emite un evento SSE de error terminal, y se eliminan los procesos hijos usando grupos de procesos POSIX (syscall.Kill(-pgid, SIGKILL)).",
          why: "Un fallo catastrófico o bug en el código de un subagente jamás debe voltear el demonio central de gentle-mesh ni dejar procesos zombies huérfanos consumiendo memoria o bloqueando puertos en el sistema operativo.",
          test: "pkg/server/http/server_test.go -> TestServer_RunnerPanicRecovery, TestServer_PanicRecovery"
        },
        {
          id: "gate_stream",
          label: "G5: SSE Stream (Streaming de Eventos)",
          sublabel: "Server-Sent Events con Last-Event-ID y Heartbeat",
          phase: "Fase 3: Persistencia y Cierre (Transporte)",
          badge: "Streaming Resiliente",
          what: "Transmite la salida del subagente en tiempo real mediante Server-Sent Events (text/event-stream). Cada evento recibe un ID monótonamente creciente. Si el cliente se desconecta y envía el encabezado Last-Event-ID, el servidor reanuda la transmisión desde el último evento no recibido gracias al buffer de memoria y logs en disco.",
          why: "En redes móviles o inalámbricas (WiFi / 4G / 5G / Tailscale), las desconexiones temporales son frecuentes. Sin Last-Event-ID, una desconexión provocaría pérdida de logs o requeriría reiniciar toda la tarea desde cero.",
          test: "pkg/server/http/server_test.go -> TestServer_LastEventIDReconnection, TestServer_SSEHeartbeat"
        },
        {
          id: "verified",
          label: "Verified Exit (Durabilidad Crash-Safe)",
          sublabel: "file.Sync() selectivo y liberación transaccional",
          phase: "Fase 3: Persistencia y Cierre (Durabilidad ACID)",
          badge: "Durabilidad ACID",
          what: "Al finalizar la tarea, el JSONLLogger ejecuta file.Sync() asegurando que todos los buffers del sistema operativo se vacíen a almacenamiento físico permanente. Luego, el TaskManager libera los cerrojos de territorio y rama y transiciona la tarea a estado completed con tracking de sync.WaitGroup.",
          why: "Garantiza la durabilidad de los artefactos y logs: si el servidor sufre un corte de energía inmediatamente después de terminar la tarea, el estado y el historial de ejecución quedan íntegros en disco sin corrupción de archivos.",
          test: "pkg/server/task/logger_test.go -> TestJSONLLogger_SelectiveSync, pkg/server/task/manager_test.go -> TestTaskManager_TrackRunnerGracefulShutdown"
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
      var cardRuntime = document.getElementById("card-runtime");

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
      if (cardAdmission) cardAdmission.addEventListener("click", function () { openModal(1); }); // Step 1: gate_auth
      if (cardRuntime) cardRuntime.addEventListener("click", function () { openModal(4); }); // Step 4: gate_branch

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

fs.writeFileSync('docs/architecture/gentle-mesh-test-verification-gates.html', html, 'utf8');
console.log('Successfully injected Step Explainer Modal, Controls, and Script into HTML!');
