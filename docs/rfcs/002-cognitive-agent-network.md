# RFC 002: Red Cognitiva de Agentes — TaskEnvelope, Pre-flight Handshake y Task Settlement

* **Autor:** Rafael De Linares & el Gentleman (Ecosistema Gentle AI)  
* **Fecha:** Septiembre 2026  
* **Estado:** Especificación Técnica / En Diseño  
* **Gobernanza:** *Sujeto a la revisión, orientación y aprobación explícita de Alan Buscaglia (@gentleman-programming)*  
* **Área:** Coordinación Multi-Agente, Contratos de Ejecución, Aserciones Deterministas  
* **Dependencia Base:** RFC 001 (Gentle Mesh v1 — Transporte Distribuido y Persistencia)  

---

## 1. Qué significa "Cognitiva"

> *"Cognitiva" se refiere a que la red transporta **intención, contexto y criterios de verificación**, no texto plano. La red no razona ni decide: garantiza que el razonamiento de los agentes opere dentro de límites autorizados y deje evidencia verificable.*

**Concreto:**
- Un CognitiveTaskEnvelope transporta *qué se autoriza* y *cómo se verifica*, no la descripción libre de una tarea.
- La red no interpreta, no completa, no alucina. Opera sobre contratos.
- El ejecutor razona (con su LLM). La red solo asegura que lo autorizado sea lo ejecutado y que quede prueba.
- El settlement receipt es la evidencia: lo que se pidió, lo que se ejecutó, lo que se verificó, quién lo firmó.

**Distinción operativa:**

| Analogía | Red de transporte (RFC-001) | Red cognitiva (RFC-002) |
|---|---|---|
| Lo que transporta | Bytes, streaming, comandos shell | Intención, contexto, aserciones |
| Decisión | El agente decide qué ejecutar | El envelope declara qué está autorizado |
| Prueba | Exit code | SettlementReceipt firmado + cadena SHA-256 |
| Error | "me falló la red" | Receipt con veredicto fallido + evidencia |

---

## 2. Motivación y Diagnóstico

Gentle Mesh v1 (RFC-001) resolvió el transporte y la infraestructura distribuida:
- Demonio en Go estático con persistencia SQLite WAL y streaming HTTPS/SSE.
- Aislamiento territorial con semáforos Git (`repo:branch`).
- Cifrado Zero-Trust con mTLS y CSR auto-enrollment.

Sin embargo, al operar con agentes autónomos en hardware real, despachar tareas como texto plano (`task: "hacer X"`) genera tres patologías graves:

1. **La falacia del cómputo homogéneo (*Context & Tool Locality*):** Los agentes no son pods idénticos de Kubernetes. Requieren acceso a repositorios clonados, GPU con VRAM para inferencia local o herramientas específicas del host.
2. **El costo del fallo tardío (*Falta de Pre-flight Handshake*):** Tareas que queman tokens y tiempo para descubrir a los 3 minutos que faltaba una dependencia en el host remoto.
3. **La falacia del "Green Checkbox" (*Ausencia de Settlement*):** El agente concluye con exit code 0 y afirma *"Todo implementado con éxito"*, pero en la práctica el código está roto o alucinado.

> **Definición de "Red Cognitiva" y Frontera (No-Objetivo N9):**  
> Una "red cognitiva" en RFC-002 **no** es una mente colectiva ni un canal desgobernado de memoria compartida entre LLMs (ver no-objetivo **[N9](002-objectives-and-non-goals.md#4-no-objetivos)**).  
> Es una red donde los mensajes transportan **intención semántica, precondiciones de recursos y aserciones deterministas**, gobernadas por un arnés Go nativo que audita y liquida cada tarea con evidencia criptográfica falsable.

---

## 3. La Propuesta: Red Cognitiva de Agentes

El RFC-002 introduce una **capa semántica y contractual** sobre el transporte de Gentle Mesh:

```text
┌──────────────────┐
│  Agente Emisor   │
└────────┬─────────┘
         │ 1. Emite CognitiveTaskEnvelope
         ▼
┌─────────────────────────────────────────────────────────────┐
│ 2. Pre-flight Handshake                                      │
│    • Valida herramientas, GPU, repositorios y precondiciones │
└────────────────────────┬────────────────────────────────────┘
                         │
                         ├─ [RECHAZO EXPLICATIVO] ──► Re-enrutar a otro nodo
                         ▼ [ACEPTADO / LEASE ASIGNADO]
┌─────────────────────────────────────────────────────────────┐
│ 3. Ejecución de la Tarea (Streaming HTTPS/SSE)              │
└────────────────────────┬────────────────────────────────────┘
                         ▼
┌─────────────────────────────────────────────────────────────┐
│ 4. Task Settlement (Liquidación Determinista por el Arnés)  │
│    • Hashes de archivos tocados                             │
│    • Exit codes de suites de tests                          │
│    • Invariantes de base de datos o estado                  │
└────────────────────────┬────────────────────────────────────┘
                         ▼
┌─────────────────────────────────────────────────────────────┐
│ 5. Emisión de Recibo de Liquidación (Settlement Receipt)    │
│    • Persistido de forma inmutable en SQLite                │
│    • Estado final: SETTLED o SETTLEMENT_FAILED              │
└─────────────────────────────────────────────────────────────┘
```

---

## 4. Especificación de Contratos

### 3.1 `CognitiveTaskEnvelope` (Contrato de Misión)

```json
{
  "task_id": "tsk-091f6a57",
  "intent": "Implementar middleware de autenticación JWT y tests",
  "territory": {
    "repository": "github.com/org/repo",
    "branch": "feature/auth-jwt"
  },
  "required_resources": [
    { "type": "tool", "name": "go", "version": ">=1.22" },
    { "type": "filesystem", "path": "/workspace/repo", "writeable": true },
    { "type": "vram_mb", "min": 0 }
  ],
  "preconditions": [
    { "type": "git_clean_worktree", "expected": true },
    { "type": "port_available", "port": 8443 }
  ],
  "settlement": {
    "strategy": "strict_all",
    "assertions": [
      {
        "id": "code_modified",
        "type": "file_modified",
        "path": "pkg/auth/jwt.go"
      },
      {
        "id": "tests_passing",
        "type": "command_exit_code",
        "command": "go test ./pkg/auth/...",
        "expected_code": 0
      }
    ],
    "timeout_seconds": 300
  }
}
```

### 3.2 Pre-flight Handshake
Endpoint de negociación previa:
`POST /v1/tasks/negotiate`

Respuesta afirmativa: `200 OK` (con lease temporal o confirmación de slot).  
Respuesta de rechazo: `412 Precondition Failed` con diagnóstico explicativo:
```json
{
  "accepted": false,
  "reason": "resource_missing",
  "details": "Tool 'go' found version 1.21.0, required >=1.22",
  "suggested_nodes": ["node-worker-gpu-02"]
}
```

### 3.3 Task Settlement & Receipts
Al finalizar el agente:
* Estado transitorio: `SETTLING`.
* El arnés Go evalúa las aserciones declaradas en `settlement.assertions`.
* Estado final: `SETTLED` (con `SettlementReceipt`) o `SETTLEMENT_FAILED`.

---

## 5. Hoja de Ruta de Implementación

1. **Fase 1: Definición de Tipos y Parsers en Go** (`pkg/envelope`).
2. **Fase 2: Motor de Negociación y Handshake** (`pkg/handshake`).
3. **Fase 3: Motor de Settlement y Evaluación Determinista de Aserciones** (`pkg/settlement`).
4. **Fase 4: Adaptador de Integración con Gentle Mesh v1** (`pkg/adapter/mesh`).
