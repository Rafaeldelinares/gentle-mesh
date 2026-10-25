# Historia y horizonte de gentle-mesh

## Qué es gentle-mesh (y qué no es)

La función central de gentle-mesh es evitar que dos agentes de IA toquen el mismo código a la vez: consciencia situacional y prevención de colisiones entre agentes que trabajan sobre el mismo repositorio (o varios). No es un planificador de trabajo — dentro de esa función sí elige el nodo entre los compatibles por capacidad y reparto de carga.

La visión a más largo plazo: que un agente (o persona) proponga tareas y sea la propia red la que las coordine entre los agentes disponibles, sin que ninguno haga de coordinador central obligatorio.

**Posicionamiento**: gentle-mesh no compite con A2A (Google/Linux Foundation, ~22k stars, 150+ organizaciones) como protocolo general de comunicación agente-agente. Se posiciona como la capa de coordinación y confianza para agentes de código que trabajan sobre un mismo repositorio — un hueco que A2A no resuelve y que la literatura de seguridad de la industria confirma como abierto (ver más abajo). Colabora con el ecosistema Gentleman Programming (gentle-shell como motor de ejecución) sin integrarse dentro de él.

## Por qué importa: validación externa del problema

Un paper de modelado de amenazas comparando MCP/A2A/Agora/ANP afirma textualmente que *"task collisions on shared resources are also not addressed directly for A2A"* — exactamente el problema central que ataca gentle-mesh. El mismo análisis confirma que la identidad en A2A es autodeclarada sin aplicación de unicidad, y que la integridad de artefactos no es forzada por el protocolo, dejándolo enteramente al implementador.

Un documento público de la comunidad gentle-ai (oct. 2026) describe un sistema de coordinación de agentes hecho a mano con scripts para proyectos reales, usando Gentle Shell como runtime de ejecución — confirma adopción real de Gentle Shell por terceros y documenta en vivo los mismos síntomas que gentle-mesh ataca (mensajes mal enrutados, bypasses de jerarquía).

Un análisis de seguridad externo independiente validó la cola territorial automática como la idea diferenciadora correcta, y señaló como hallazgo nuevo: el sistema nunca contrasta a posteriori la superficie de edición declarada por una tarea contra los ficheros realmente tocados — queda como trabajo futuro de verificación territorial a posteriori.

## Una lección de proceso, no solo de código

El hallazgo más valioso del ciclo de conformidad con el perfil mínimo de la RFC-002 no fue técnico sino de proceso: pedir una revisión externa independiente, sin adelantarle nada, específicamente para contrastarla contra la autoevaluación del propio trabajo de cierre, destapó que un issue de conformidad se había cerrado como completo habiendo resuelto solo una fracción de los requisitos reales, con al menos un test de seguridad debilitado silenciosamente en el camino.

Ninguno de esos hallazgos tenía superficie de ataque activa en producción en ese momento, pero todos eran críticos a nivel de protocolo. La respuesta fue un cierre sistemático de los 12 requisitos del perfil mínimo con evidencia RED/GREEN cruda exigida en cada PR, no narrativa — ver la tabla de conformidad en el README y en docs/rfcs/002-objectives-and-non-goals.md §7.

**El principio**: una autoevaluación de un agente sobre su propio trabajo de cierre no es suficiente evidencia por sí sola para un hito crítico. Una segunda mirada externa, sin contaminar con lo que ya se cree cerrado, es la que lo destapa.

## El horizonte

Con el perfil mínimo de la RFC-002 cerrado de verdad (12/12), el proyecto abre dos frentes concretos, de corto plazo:

**Validación con agentes reales**: un worker físico ya está en marcha, probado con una tarea sintética no trivial (una caché LRU+TTL concurrente) con verificación cruzada independiente fuera del sandbox del agente. El siguiente paso es trabajo real de producción, no solo pruebas de validación.

**RFC-003 — protocolo de unión + adaptador A2A** (en diseño, no implementado todavía): dos ideas que comparten infraestructura y se fusionan en una sola RFC:

- *Nacer situado*: que el conocimiento de la malla viva en un log de eventos firmado y encadenado por hash, con snapshot + suscripción para que un agente nuevo se una ya situado al estado de la red, y con declaraciones de territorio verificadas oportunísticamente contra actividad observada (ficheros tocados, operaciones git) en vez de confiar a ciegas en lo declarado.
- *Interoperabilidad A2A*: exponer Agent Cards A2A-compatibles hacia fuera, para que un worker de gentle-mesh sea descubrible y delegable desde un orquestador externo sin que ese orquestador necesite conocer el protocolo interno de gentle-mesh.

Como candidato de alcance adicional: un modelo de capacidades en dos capas separadas — la capa AGENTE (modelo, skills, presupuestos de ejecución) y la capa NODO/WORKER (hardware, sandbox, toolchains, red, identidad) — para que las fallas de un worker por carencia de algo (una herramienta no instalada, por ejemplo) se reporten con su causa real en vez de perderse como un error genérico.

Ninguno de estos dos frentes es un plazo ni un compromiso — son los próximos pasos razonables dado lo que ya está construido, no una promesa de cuándo estará terminado.

## Un ciclo de auditoría externa, con la misma disciplina

En octubre de 2026, una auditoría independiente sobre el repositorio encontró 50 hallazgos de seguridad y arquitectura. Ninguno se aceptó sin verificación propia contra el código real: de los 12 hallazgos más graves, 9 se confirmaron (varios con un mecanismo distinto al descrito originalmente), 2 no se pudieron reproducir, y 1 tenía la severidad sobrestimada.

El cierre se hizo con cinco pasadas adversariales por frente, reproduciendo cada fallo antes de escribir el arreglo. En uno de los casos, el propio proceso de cierre introdujo una condición de carrera nueva — y la misma verificación que cerró el hallazgo original la encontró y la corrigió en la misma sesión, antes de darla por buena.

El resultado: identidad de nodos con revocación de certificados respaldada por el propio almacenamiento persistente (no por una copia en memoria que podía desincronizarse), aislamiento de ejecución reforzado contra fugas del sandbox, y una primera validación real, con dos máquinas distintas — una como coordinador, otra como ejecutor — completando una tarea de principio a fin con un agente real dentro del entorno aislado. El binario desplegado en ambas máquinas se verificó como idéntico, byte a byte, antes de confiar en el resultado.
