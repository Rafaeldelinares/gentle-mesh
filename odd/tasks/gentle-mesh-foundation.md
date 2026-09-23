# Feature: gentle-mesh-foundation

> Fundaciones del protocolo, registro de malla, runner simulado y servidor SSE para Gentle Mesh v1.

## Scope & Intent
Implementar la primera versión ejecutable y testeada de Gentle Mesh en Go, validando los contratos tipados de la API v1, el registro de nodos en malla, la persistencia append-only en JSONL, el streaming SSE con soporte de reconexión (`Last-Event-ID`), y el cliente CLI mínimo para demostración end-to-end.

## Tasks

- [x] Task 1: Especificar tipos canónicos y eventos SSE en `pkg/protocol` bajo TDD estricto. (Commit: `6fcab26`, 18 tests passing with race detector)
- [x] Task 2: Implementar el registro de nodos de malla (`pkg/server/registry`) con `/join`, `/heartbeat`, catálogo en memoria, branch locking exclusivo e idempotencia. (Commit: `9b56521`, 36 tests passing with race detector)
- [x] Task 3: Implementar el gestor de ciclo de vida de tareas y logger JSONL append-only (`pkg/server/task`). (Commit: `b574c17`, 53 tests passing with race detector across all packages)
- [x] Task 4: Diseñar la abstracción `Runner` y el `SimulatedRunner` con generación realista de eventos SSE (`pkg/server/runner`). (Commit: `a8078e0`, 67 tests passing with race detector across all packages)
- [ ] Task 5: Construir el servidor HTTP y manejador de streaming SSE con soporte para `Last-Event-ID`, `/reply` y cancelación (`pkg/server/http`).
- [ ] Task 6: Construir el punto de entrada CLI (`cmd/gentle-mesh`) con comandos `server` y `run` para validación end-to-end.
