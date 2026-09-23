# Guía de Valoración y Prompt para LLMs (GPT / Claude)

Este documento contiene el prompt estructurado y las preguntas clave para enviar a **Claude 3.5 Sonnet / Opus** y **GPT-4o** junto con el archivo `gentle-mesh.zip`.

---

## Prompt para Copiar y Pegar en Claude / ChatGPT

```markdown
Actúa como un Arquitecto de Sistemas Distribuidos Senior y Experto en Ingeniería de Software en Go (Golang).

Te adjunto el repositorio completo de `gentle-mesh` en un archivo ZIP (`gentle-mesh.zip`).

### Contexto del Proyecto
`gentle-mesh` es un sistema distribuido de federación, transporte y ejecución remota de subagentes de Inteligencia Artificial para el ecosistema Pi y Gentle AI (creado para la comunidad de Gentleman Programming).

### Restricciones Arquitectónicas de Diseño
1. **Lenguaje y Dependencias:** Go 1.22+ estándar puro (`net/http`, `encoding/json`, `sync`, `context`, etc.). Cero dependencias externas pesadas ni frameworks web (para garantizar un binario estático único de ~15MB con `CGO_ENABLED=0`).
2. **Desacople Cómputo vs Coordinación:** El orquestador local del usuario es ultra-liviano. La carga pesada y las colas de concurrencia residen en los nodos trabajadores remotos (servidores, VPS o máquinas secundarias).
3. **Transporte y Persistencia:** HTTP REST + Server-Sent Events (SSE) para streaming continuo de pensamientos y tokens; persistencia append-only en disco (JSONL) para permitir reconexión histórica instantánea (`Last-Event-ID`) con consumo de RAM O(1).
4. **Construcción Colectiva y Consciencia Situacional (Radar de Ámbitos):** Más allá del balanceo de carga, el sistema implementa un Protocolo de Territorio y un Radar en tiempo real con 3 dimensiones de alcance por agente:
   - **Dominio Arquitectónico:** (ej. `auth`, `database`, `billing`, `ui`).
   - **Superficies de Edición:** Rutas y globs autorizados (`pkg/auth/*`).
   - **Radio de Impacto (Blast Radius):** `read-only`, `isolated-branch`, `shared-schema`, `breaking-change`.
   - **Micro-Acciones en Vivo:** Inferencia en tiempo real de fase (`explore`, `plan`, `apply`, `verify`) y acción actual a partir de eventos SSE.
   - **Detección Preventiva de Colisiones:** Bloqueo exclusivo de ramas Git, solapamiento de superficies (`surface_overlap`) y deduplicación con enganche a streams en vivo (`duplicate_task` -> Live Stream Hooking).

---

### Lo que necesito que evalúes con máxima rigurosidad técnica ("Thermonuclear Review")

Por favor, revisa el código fuente (`pkg/protocol/`, `pkg/server/`, `cmd/gentle-mesh/`), los tests unitarios con `-race`, el Dockerfile, la topología multi-nodo y el RFC 001 (`docs/rfcs/001-remote-agent-transport.md`), y respóndeme con honestidad brutal a las siguientes 5 áreas:

1. **Calidad Arquitectónica e Idiomaticidad en Go:**
   - ¿El código sigue los patrones idiomáticos de Go?
   - ¿La concurrencia (`sync.RWMutex`, canales, `context.Context`) es segura frente a condiciones de carrera, bloqueos mutuos (*deadlocks*) o fugas de goroutines?
   - ¿La abstracción entre `Runner`, `ManagedTask` y `TaskManager` es limpia y desacoplada?

2. **Diseño del Protocolo y Resiliencia:**
   - ¿Es robusta la implementación de SSE y el parser bidireccional?
   - ¿Qué opinas de la elección de JSONL append-only en disco versus usar una base de datos embebida (como SQLite)?
   - ¿El manejo de `Last-Event-ID` y reconexión histórica resuelve adecuadamente las caídas de red?

3. **Consciencia Situacional y Protocolo de Territorio (Radar):**
   - ¿Qué tan efectivo es el modelo tridimensional de ámbito (`domain`, `edit_surfaces`, `blast_radius`) para evitar que múltiples agentes o personas choquen al programar concurrentemente?
   - ¿Tiene sentido la estrategia de "Live Stream Hooking" (en vez de rechazar tareas duplicadas, suscribir al segundo cliente al stream en vivo existente)?

4. **Puntos Ciegos, Vulnerabilidades y Casos Límite:**
   - ¿Qué riesgos o fallas operativas no estamos viendo? (Ej: qué pasa ante una partición de red entre nodos, procesos huérfanos que sobrevivan a una cancelación, saturación de disco por logs JSONL, o ataques de denegación de servicio si la API queda expuesta).
   - ¿Qué mejoras críticas recomendarías antes de llevar esto a un entorno de producción o a una versión 1.0 comunitaria?

5. **Valor Real para la Comunidad vs Sobreingeniería:**
   - ¿Esto realmente resuelve un dolor tangible para desarrolladores que usan agentes de IA o corre el riesgo de ser una sobreingeniería innecesaria?
   - ¿Cómo calificarías este proyecto en términos de viabilidad, elegancia y madurez técnica?

Sé directo, constructivo y exigente. No te guardes nada.
```
