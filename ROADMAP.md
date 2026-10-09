# Roadmap

Roadmap público de Gentle Mesh. Esta página es una **propuesta** y un resumen; los milestones reales dependen del feedback de la comunidad y de los issues abiertos. **No hay fechas de entrega** comprometidas.

> El proyecto es impulsado por la comunidad. Los plazos y prioridades son aproximaciones que se ajustan según quienes se ofrecen a empujar trabajo. Para proponer cambios, abrir un issue o un PR contra `ROADMAP.md`.

---

## Milestones anunciados

### v1.0.4 — Siguiente parche

Estado: planificado. Alcance previsto:

- Flags TLS en los clientes de la CLI (`nodes`, `radar`, `run`, `rpc`).
- Respuesta de error explícita a los comandos RPC no soportados.
- Tests de enrollment de extremo a extremo.
- Tests de webhooks.


### RFC-002 v2 Fase 2 — Liquidación entre agentes

Estado: experimental, en la rama `feat/rfc-002-settlement`.

- Perfil de conformidad: el repositorio declara 12 requisitos. El estado de conformidad documenta cuántos están demostrados por tests automatizados hoy; la rama `feat/rfc-002-settlement` trabaja hacia un nivel de conformidad más alto antes de integrar el protocolo en el binario principal.
- Issue de seguimiento: [#70](https://github.com/Rafaeldelinares/gentle-mesh/issues/70).
- Aviso: el protocolo es experimental y no se ofrece como API estable.

### v1.1.0 — Configuración por defecto segura

Estado: planificado. Alcance previsto:

- La configuración por defecto del coordinador debe ser segura (issue [#57](https://github.com/Rafaeldelinares/gentle-mesh/issues/57)).
- Revocación de certificados (issue [#25](https://github.com/Rafaeldelinares/gentle-mesh/issues/25)).

---

## Lo que no haremos

Esta lista es deliberada. Se publica para que la comunidad pueda expresar disconformidad antes de que se asuma lo contrario.

- **No haremos de Gentle Mesh un orquestador de sesiones remotas de pi.** El proyecto resuelve territorios, reparto de tareas y seguridad de malla. Las sesiones remotas de pi (`@earendil-works/pi-protocol`, `@earendil-works/pi-client`, `@earendil-works/pi-server`, `@earendil-works/pi-coding-agent`) son una pieza distinta; la integración es posible pero no es una línea actual de trabajo.
- **No añadiremos parseo de AST ni análisis estático del código del proyecto para detectar colisiones.** El coordinador solo conoce lo que cada tarea declara como superficie. Los bloqueos se basan en lo declarado, no en lo inferido.
- **No prometeremos compatibilidad binaria entre releases** hasta que el alcance del proyecto sea estable.
- **No ofreceremos SLA, soporte pagado ni garantías de tiempo de respuesta** para issues. El canal es la comunidad.

---

## Cómo participar

- Abrir un issue describiendo el cambio propuesto.
- Marcar el issue con la etiqueta del área correspondiente si existe.
- Si quieres empujar un item del roadmap, comentar en el issue enlazado y sincronizar con quien mantiene el área.

## Cómo leer este roadmap

- "Planificado" significa que el item aparece descrito en el README, en un issue abierto, o en una rama de trabajo. No hay fecha.
- "Experimental" significa que existe código de trabajo pero la integración al binario principal está pendiente de un nivel mínimo de conformidad.
- "Cerrado" (no usado todavía) se reservará para items ya entregados.

---

*Este roadmap es vivo. Si una entrada está desactualizada, abrir un PR contra `ROADMAP.md`.*
