# Feature: RFC-003 — Protocolo de unión y adaptador A2A

> Mover el conocimiento de la malla del coordinador a un log de eventos firmado y encadenado por hash, y exponer un adaptador A2A (Agent Card firmada) hacia fuera.

## Scope & Intent
Que el conocimiento de la malla deje de vivir solo en el coordinador y pase a un **log de eventos firmado y encadenado por hash**: el coordinador pasa a ser un lector más del log, y un agente que se une **nace situado** (snapshot verificable + stream en vivo) en vez de ciego. En paralelo, exponer una **Agent Card** A2A firmada (JWS sobre forma canónica JCS) para que un orquestador externo pueda descubrir y delegar en un worker sin conocer el protocolo interno de gentle-mesh. Los objetivos, no-objetivos, perfil mínimo y tabla de conformidad viven en el documento técnico: [`docs/rfcs/003-join-protocol-and-a2a-adapter.md`](../../docs/rfcs/003-join-protocol-and-a2a-adapter.md).

## Decisiones Clave Resueltas

- **Serialización del log resuelta: Opción C (híbrido).** (RFC-003 §7, issue #166). `BEGIN IMMEDIATE` sobre conexión dedicada más mutex en proceso como camino feliz sin fricción ni re-firmas, más `UNIQUE(prev_hash)` en SQLite (atrapando tanto bifurcaciones intermedias como doble génesis `prev_hash = ""`) como salvaguarda dura de defensa en profundidad.
- **El log es la fuente de verdad.** Cualquier nodo reconstruye el estado completo leyéndolo desde cero (J6). El SQLite queda **derivado** del log, no al revés.
- **Sin CRDTs, sin Raft/Paxos, sin event-sourcing completo.** Es un log de eventos de alto nivel (tarea creada, territorio declarado, colisión detectada, tarea terminada), no un sistema de reconstrucción universal.
- **Réplicas y consenso: fuera de alcance, pero sin cerrar la puerta.** El formato del evento y el esquema de encadenado **no pueden asumir implícitamente un único coordinador posible**; cada decisión que encarecería réplicas más adelante queda anotada en §6 del RFC.
- **JCS (RFC 8785) + Ed25519**, reusando las primitivas que ya existen en `pkg/jcs`, `pkg/envelope`, `pkg/signing` y `pkg/receipt`.
- **A2A es fachada hacia afuera, no transporte interno** (N4). El tráfico entre nodos sigue hablando el protocolo propio.
- **El modelo de capacidades en dos capas se documenta, no se implementa** en esta RFC: capa AGENTE (modelo, skills, presupuestos) y capa NODO/WORKER (hardware, sandbox, toolchains, red, identidad), con Tier 1 verificable al instante vía el `POST /preflight` que ya existe y Tier 2 como confianza por observación.

## Decisiones Pendientes (a resolver en implementación)

- [ ] **Superficie del snapshot** (J3): qué se firma y cómo se define el digest (sobre la cadena de hashes, no sobre el estado derivado).
- [ ] **Formato de envelope firmado completo para eventos de agentes** (reutilización de `pkg/signing` con Ed25519 sobre JCS).

## Arquitectura de Paquetes (prevista)

```
gentle-mesh/
├── pkg/
│   ├── joinlog/                # Log de eventos de la malla: formato, hash, append, verificación de cadena
│   ├── jcs/                    # RFC 8785 canonicalization (ya existe, se reusa)
│   ├── envelope/               # CognitiveTaskEnvelope + hashing (ya existe, se reusa)
│   ├── signing/                # Ed25519 (ya existe, se reusa)
│   └── receipt/                # Cadena de recibos: precedente del encadenado por hash
└── cmd/
    └── gentle-mesh/            # Endpoints de snapshot/stream y el comando de la Agent Card
```

## Tasks

- [x] Task 0: Documento técnico de RFC-003 (objetivos, no-objetivos, perfil mínimo, restricción de diseño y tabla de conformidad) más este tracking. (Commit: `abc8f4c`, documentación; PR [#167](https://github.com/Rafaeldelinares/gentle-mesh/pull/167))
- [x] Task 1: Formato del evento firmado + test de la carrera de escritura. Resuelto con Opción C (`BEGIN IMMEDIATE` + `UNIQUE(prev_hash)` en SQLite). 4 tests pasando con race detector: linealidad bajo 16 escritores concurrentes, rechazo de doble génesis `prev_hash = ""`, rechazo de bifurcación intermedia y serialización multi-handle. (Issue: [#166](https://github.com/Rafaeldelinares/gentle-mesh/issues/166))
- [x] Task 2: Cada cambio de estado como evento; el SQLite como proyección derivada del log, no al revés (J6). Cero inferencias del reloj de pared: expiración por reaper emitida explícitamente (`reason: "lease_expired"`). Suite completa de equivalencia campo por campo verificada bajo `-race` (`TestProjector_RebuildFromScratchProducesIdenticalState`). (Issue: [#169](https://github.com/Rafaeldelinares/gentle-mesh/issues/169))
- [ ] Task 3: Endpoint de snapshot con digest verificable + suscripción al stream en vivo, con cursor reanudable.
- [ ] Task 4: Verificación oportunista del territorio declarado contra la actividad observada (ficheros tocados, operaciones git), sin reemplazar la declaración.
- [ ] Task 5: Adaptador A2A: Agent Card en `/.well-known/agent-card.json`, firmada JWS sobre JCS con las primitivas Ed25519 existentes.
- [ ] Task 6: Documentar (no implementar) el modelo de capacidades en dos capas, con Tier 1 (verificable al instante) y Tier 2 (confianza por observación).

## Referencias

- Documento técnico: [`docs/rfcs/003-join-protocol-and-a2a-adapter.md`](../../docs/rfcs/003-join-protocol-and-a2a-adapter.md)
- Riesgo y decisión de serialización: issue [#166](https://github.com/Rafaeldelinares/gentle-mesh/issues/166), RFC-003 §7
- Posicionamiento previo sobre A2A: [`docs/rfcs/002-a2a-positioning.md`](../../docs/rfcs/002-a2a-positioning.md)
- Criterios de decisión y perfil mínimo de RFC-002: [`docs/rfcs/002-objectives-and-non-goals.md`](../../docs/rfcs/002-objectives-and-non-goals.md)
