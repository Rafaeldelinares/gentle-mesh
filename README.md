# Gentle Mesh 🌐

> **Transporte Distribuido Federado, Ejecución Remota de Subagentes y Consciencia Situacional para el Ecosistema Pi & Gentle AI.**

---

### ⚠️ Nota de Gobernanza y Comunidad

> **Este repositorio es una propuesta de arquitectura técnica (RFC) y Prueba de Concepto (PoC) comunitaria creada para el ecosistema Gentle AI.**  
>
> **Este proyecto avanzará, evolucionará y se integrará de forma oficial única y exclusivamente bajo la revisión, orientación y aprobación explícita de [Alan Buscaglia (@gentleman-programming)](https://github.com/gentleman-programming), creador y líder del ecosistema Gentle AI.**  
>
> Hasta contar con su feedback y visto bueno, este repositorio permanece como un espacio de investigación abierta, validación técnica, prototipado riguroso y experimentación colaborativa por y para la comunidad.

---

## 1. La Visión: Crear Sistemas Entre Varios

La potencia distribuida en sistemas multi-agente no se trata únicamente de velocidad o de que una tarea tarde menos tiempo en compilar. Se trata de **la posibilidad de construir sistemas de software complejos entre varios seres humanos y múltiples agentes concurrentes**, cooperando sobre el mismo proyecto sin pisarse la cabeza.

Hoy en día, el uso de agentes de IA es aislado y solitario: un desarrollador con su terminal local. Si varios miembros de un equipo o de la comunidad lanzan agentes al mismo tiempo, el conflicto de merge es inevitable y nadie sabe qué está haciendo el otro.

`gentle-mesh` actúa como el **sistema nervioso central** de una red de agentes:
1. **Desacopla el Cómputo del Cliente:** Tu laptop sólo despacha intenciones y consume texto; la carga pesada (compilaciones, linters, suites de tests) se delega a nodos remotos (servidores, VPS o máquinas secundarias).
2. **Consciencia Situacional Compartida (Radar de Ámbitos):** Cada agente declara su **Dominio Arquitectónico**, sus **Superficies de Edición** autorizadas y su **Radio de Impacto** (*Blast Radius*).
3. **Detección Preventiva de Colisiones y Live Stream Hooking:** La malla detecta choques de territorio antes de escribir una sola línea de código y permite que clientes secundarios se enganchen en vivo a tareas duplicadas ya en curso.
4. **Binario Único en Go Puro:** Cero dependencias pesadas, compilación estática (`CGO_ENABLED=0`), arranque en milisegundos y consumo de memoria ridículamente bajo (~30 MB de RAM para un clúster de 6 nodos).

---

## 2. Pila Tecnológica y Arquitectura

* **Lenguaje:** Go 1.22+ estándar puro (`net/http`, `encoding/json`, `sync`, `context`). Cero frameworks externos pesados.
* **Transporte:** HTTP REST + Server-Sent Events (SSE) para streaming continuo de pensamientos (`thought`), llamadas a herramientas (`tool_call`) y resultados.
* **Resiliencia y Persistencia:** Append-only logs en formato **JSONL** (`/var/log/gentle-mesh/tasks/{id}.jsonl`). Permite reconexión histórica instantánea vía el header estándar `Last-Event-ID` con consumo de RAM constante $O(1)$.
* **Supervisión Autónoma:** Detección de inactividad, loop detection (bloqueo tras 3 fallas idénticas de herramientas) y auto-commit de seguridad (WIP) sin popups interactivos.

```text
gentle-mesh/
├── cmd/
│   └── gentle-mesh/          # CLI principal: server, worker, nodes, radar, run
├── pkg/
│   ├── protocol/             # Tipos canónicos, eventos SSE, serialización y colisiones
│   ├── server/
│   │   ├── http/             # Servidor REST, middleware Bearer y streaming SSE
│   │   ├── registry/         # Catálogo de nodos, branch locking e idempotencia
│   │   ├── task/             # Gestor de tareas, state machine y logger JSONL
│   │   └── runner/           # Abstracción Runner y SimulatedRunner realista
├── docs/
│   ├── rfcs/                 # RFC 001: Especificación técnica canónica
│   └── EVALUATION_GUIDE_FOR_LLMS.md # Guía para revisión externa (Claude / GPT)
└── docker-compose.test.yml   # Topología multi-nodo de prueba (1 coordinator + 5 workers)
```

---

## 3. Demostración Rápida en Local (Entorno Seguro)

### Requisitos
* Go 1.22+ o Docker / Docker Compose.

### Ejecutar todas las pruebas con detector de carreras
```bash
go test -v -race ./...
```
*(Todos los paquetes cuentan con cobertura unitaria y 0 race conditions).*

### Levantar el clúster de prueba de 6 nodos en Docker
El repositorio incluye una topología lista para probar en una red bridge aislada (`gentle-mesh-net`):
```bash
docker compose -f docker-compose.test.yml up -d
```

### Consultar los nodos registrados en la malla
```bash
go run ./cmd/gentle-mesh nodes -coordinator http://localhost:8080
```
Salida esperada:
```text
NODE ID         ENDPOINT                    STATUS  CONCURRENCY  AGENTS   TAGS
worker-alpha    http://worker-alpha:8081    online  0/2          worker   go,fast
worker-beta     http://worker-beta:8081     online  0/4          worker   heavy,docker
worker-gamma    http://worker-gamma:8081    online  0/2          explore  research
worker-delta    http://worker-delta:8081    online  0/1          worker   gpu,ml
worker-epsilon  http://worker-epsilon:8081  online  0/2          verify   ci,testing
```

### Consultar el Radar de Ámbitos y Actividad en Vivo
```bash
go run ./cmd/gentle-mesh radar -coordinator http://localhost:8080
```

### Despachar una tarea con ámbito acotado
```bash
go run ./cmd/gentle-mesh run -coordinator http://localhost:8080 \
  -task "Refactorizar validación de JWT" \
  -domain "auth" \
  -blast-radius "isolated-branch" \
  -surfaces "pkg/auth/jwt.go"
```

---

## 4. Documentación y Revisión Técnica

* **Especificación Técnica Completa:** [RFC 001 — Remote Agent Transport & M2M Federation](docs/rfcs/001-remote-agent-transport.md).
* **Guía de Evaluación Externa para LLMs:** [docs/EVALUATION_GUIDE_FOR_LLMS.md](docs/EVALUATION_GUIDE_FOR_LLMS.md) contiene el prompt estructurado para someter este código a revisión con Claude 3.5 Sonnet o GPT-4o.
* **Convenciones del Proyecto:** [AGENTS.md](AGENTS.md).

---

## 5. Built with Gentle-AI

<div align="center">

<a href="https://github.com/Gentleman-Programming/gentle-ai">
  <img width="220" src="https://raw.githubusercontent.com/Gentleman-Programming/gentle-ai/main/docs/assets/brand/built-with-gentle-ai.png" alt="Built with Gentle-AI" />
</a>

<p><sub>Construido y disciplinado con <strong><a href="https://github.com/Gentleman-Programming/gentle-ai">Gentle-AI</a></strong> — Memory, Workflows & Evidence.</sub></p>

</div>

---

## 6. Licencia

Código abierto bajo licencia MIT (o la que determine la gobernanza comunitaria de Gentleman Programming).
