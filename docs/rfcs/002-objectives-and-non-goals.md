# RFC-002 — Objetivos y no-objetivos

> Estado: propuesta · Aplica a: RFC-002 (Settlement Protocol) v2
> Este documento es el criterio de decisión del protocolo. Cualquier cambio a la especificación
> o a la implementación debe poder justificarse contra estos objetivos y no contradecir ningún no-objetivo.

> **Nota de lectura.** Este documento expresa los objetivos del protocolo y el criterio de decisión.
> El estado real de cada objetivo —con su test y su incidencia— vive en la sección 7, «Estado de
> conformidad». Que un objetivo figure en esta lista no significa que esté cumplido hoy.

## 1. Propósito

gentle-mesh RFC-002 es un protocolo de **confianza y liquidación entre agentes**: permite que un agente
(emisor) encargue trabajo a otro (ejecutor) de forma que:

1. el ejecutor **solo pueda hacer lo autorizado**,
2. ambos obtengan **evidencia firmada y encadenada por recibo, con manipulación detectable en el contenido firmado** de lo que se pidió, lo que se hizo y lo que se comprobó (hoy los enlaces `previous_receipt_hash`/`seq` y la decisión del emisor no están firmados: incidencias #43 y #48),
3. cualquier intento de salirse de lo autorizado **falle y deje rastro**.

Promesa pública del protocolo:

> *gentle-mesh no impide que un agente intente salirse; hace que cada acción quede registrada
> en un recibo firmado por el ejecutor, que el emisor pueda aceptarlo o disputarlo, y que
> cualquier manipulación del contenido firmado se detecte al verificar el recibo.* (estado real en la sección 7)

## 2. Modelo de adversario (resumen)

El protocolo asume que **cualquier participante puede ser malicioso o estar comprometido**, incluidos:

- un **emisor** que pide acciones dañinas o intenta ampliar permisos mediante delegación;
- un **ejecutor** (típicamente un agente LLM) que persigue su objetivo por medios no autorizados
  (reward hacking, uso de canales laterales, coordinación con otros agentes);
- un **atacante en red** que intercepta, reordena, repite o altera mensajes;
- un **atacante con acceso al almacenamiento** que modifica estado o recibos en reposo.

El detalle completo está en `docs/architecture/THREAT-MODEL.md`.

## 3. Objetivos

Cada objetivo tiene un criterio medible. "Cumplido" significa que existe un test automatizado que lo demuestra.

### 3.1 Seguro

| ID | Objetivo | Criterio medible |
|----|----------|------------------|
| S1 | **Identidad fuerte** | Toda conexión usa mTLS; el CN/SAN del certificado coincide con el `agent_id` firmante. Una discrepancia se rechaza (403). |
| S2 | **Verificar antes de actuar** | Ningún envelope, aceptación o disputa se ejecuta ni se persiste sin haber verificado antes su firma Ed25519 contra una clave registrada y no revocada. |
| S3 | **Deny-by-default** | Un envelope sin capacidades declaradas se ejecuta con el perfil mínimo (sin red, sin exec, solo lectura del workspace). Una capacidad no concedida produce `REJECTED_CAPABILITY`. |
| S4 | **Política aplicada fuera del agente** | Los límites (red, sistema de ficheros, binarios, recursos) los impone el runtime del ejecutor o la infraestructura, no el proceso del agente. Un agente no puede modificar su propia política. |
| S5 | **Delegación atenuada** | En cualquier cadena de delegación, las capacidades del hijo ⊆ las del padre ∩ la política del ejecutor. Existe un test de propiedad que lo comprueba. |
| S6 | **Sin ejecución de datos** | Los datos recibidos solo se decodifican como JSON canónico (RFC 8785) a tipos fijos con campos desconocidos rechazados. Ningún camino de código reconstruye tipos arbitrarios ni ejecuta strings recibidas como shell. |
| S7 | **Integridad de la historia** | Cada recibo está firmado por el ejecutor (y contrafirmado por el emisor al aceptar o disputar) y encadenado por SHA-256. La cadena se valida **al escribir** y es verificable de forma independiente por terceros. |
| S8 | **Revocación** | Una clave revocada deja de ser aceptada en toda la malla en un tiempo acotado (objetivo ≤ 1 intervalo de heartbeat). Existe un kill switch que cancela todos los leases activos. |
| S9 | **Sin downgrade** | Todo mensaje lleva `protocol_version`. Una versión no soportada se rechaza; no existe modo de compatibilidad insegura. |

### 3.2 Fuerte (robusto)

| ID | Objetivo | Criterio medible |
|----|----------|------------------|
| R1 | **Idempotencia** | Un envelope con un `envelope_id` ya procesado no se ejecuta dos veces; se devuelve el recibo existente. |
| R2 | **Leases que caducan** | Todo lease tiene expiración; al vencer, el ejecutor aborta y emite un recibo `SETTLEMENT_TIMEOUT`. |
| R3 | **Reintentos seguros** | Un cliente puede reintentar cualquier operación ante un fallo de red sin efectos duplicados (se apoya en R1). |
| R4 | **Concurrencia correcta** | N escrituras concurrentes al mismo ejecutor producen una cadena válida sin enlaces rotos ni IDs duplicados (test con N ≥ 50 bajo `-race`). |
| R5 | **Estados terminales claros** | Todo contrato termina en un estado terminal definido; no existen estados colgados sin salida. |
| R6 | **Sin dependencia del reloj ajeno** | La validez de un mensaje nunca depende de que el reloj del otro nodo sea correcto; las expiraciones las evalúa quien las aplica, con su propio reloj. |
| R7 | **Firmas deterministas** | Firmar → serializar → persistir → leer → verificar da siempre el mismo resultado (test de 1000 iteraciones sin fallos). |

### 3.3 Ágil

| ID | Objetivo | Criterio medible |
|----|----------|------------------|
| A1 | **Seguro por defecto, sin configurar** | Un emisor puede encargar una tarea de bajo riesgo sin declarar capacidades ni conocer nada del ejecutor. |
| A2 | **Descubrimiento por intento** | No se exige anunciar capacidades de nodo. Un ejecutor que no puede cumplir responde con un lease `accepted: false` y el motivo estructurado. |
| A3 | **Camino rápido** | Las tareas dentro del perfil mínimo se resuelven en un único round-trip (envelope → recibo). |
| A4 | **Coste criptográfico despreciable** | La verificación y firma añaden < 1 ms por mensaje en hardware de referencia (benchmark en CI). |
| A5 | **Controles pesados solo por riesgo** | Sandbox efímero, aprobación humana y otros controles caros solo se activan cuando las capacidades pedidas superan el perfil mínimo. |
| A6 | **Seguridad invisible para el integrador** | Firmar, verificar, encadenar y comprobar la política lo hace la librería; el integrador no manipula firmas ni hashes a mano. |
| A7 | **Capas sustituibles** | Identidad/transporte, mensaje, autorización y evidencia se especifican y prueban por separado y pueden evolucionar de forma independiente. |

## 4. No-objetivos

Lo que RFC-002 **no** pretende resolver. Declararlo evita prometer lo que no se cumple.

| ID | No-objetivo | Quién lo cubre |
|----|-------------|----------------|
| N1 | **Impedir que un agente autorizado haga daño dentro de su perímetro.** Si se le concede escribir en `src/`, puede escribir mal en `src/`. | Revisión humana, aserciones de liquidación, perímetros pequeños. |
| N2 | **Garantizar el aislamiento del SO o del kernel.** Un escape de contenedor por una vulnerabilidad del kernel está fuera del protocolo. | Infraestructura (gVisor, nsjail, microVM), parches. |
| N3 | **Cerrar canales laterales fuera de la malla** (DNS, almacenes compartidos, servicios públicos). | Aislamiento de red real (S4) y configuración de la infraestructura. |
| N4 | **Proteger contra un host ejecutor comprometido.** Si el host está comprometido, lo que firma ese ejecutor pierde valor. | Claves en HSM/KMS, anclaje externo de la cadena, revocación (S8). |
| N5 | **Juzgar la calidad o intención del trabajo.** El protocolo verifica aserciones deterministas, no si la solución es buena. | Aserciones del emisor, evaluación humana. |
| N6 | **Ser un protocolo de transporte general ni de descubrimiento de agentes.** | Protocolos existentes (p. ej. A2A, MCP); gentle-mesh se sitúa como capa de confianza y liquidación. |
| N7 | **Reinventar criptografía, delegación o sandboxing.** | Primitivas y modelos estándar (Ed25519, RFC 8785, macaroons/UCAN/biscuit, logs de transparencia, gVisor/nsjail). |
| N8 | **Compatibilidad con envelopes v1.** | Migración explícita; v2 rechaza v1 (S9). |
| N9 | **Albergar cognición compartida no gobernada.** La red no es una "mente colectiva": no ofrece canales libres de memoria o coordinación entre agentes. Cualquier estado compartido futuro será un *tablón gobernado* (escritura por capacidad, entradas firmadas y encadenadas, lectura por ámbito, revocación, solo datos tipados). | Extensión futura (RFC-004) bajo este modelo. |

## 5. Perfil mínimo conforme

Un nodo puede declararse *gentle-mesh RFC-002 conforme* si implementa, como mínimo:

- S1, S2, S3, S6, S7, S9
- R1, R2, R5, R7
- A1, A2

El resto son **extensiones** (delegación atenuada, revocación en malla, sandbox por tarea,
aprobación humana, anclaje externo) que un nodo anuncia en su `health` como `extensions: [...]`.

## 6. Principios de decisión

Cuando dos objetivos entren en conflicto, se aplica este orden:

1. **Seguridad** sobre agilidad: nunca se relaja S1–S9 para ganar velocidad o comodidad.
2. **Corrección** sobre disponibilidad: ante duda, rechazar y emitir un recibo, no ejecutar.
3. **Simplicidad** sobre generalidad: una capa pequeña y verificable antes que una flexible y opaca.
4. **Estándares** sobre invención propia.
5. **Evidencia** sobre confianza: si no hay test que lo demuestre, el objetivo no está cumplido.

---

## 7. Estado de conformidad

Verificación sobre el commit `9a849a855087e144cdc02b4cb4eb24457b459d22` (rama
`feat/rfc-002-settlement`), a fecha 2026-10-06. «Cumplido» significa que existe un test
automatizado que demuestra el criterio medible.

| Objetivo | Perfil mínimo | Estado | Test relacionado | Qué falta | Incidencia |
|----------|---------------|--------|------------------------|-----------|------------|
| S1 | sí | no cumplido | `integration/agent/tls_test.go:104` (débil) | Ligar CN/SAN al `agent_id` en el servidor RFC-002: el coordinador de `main` ya liga el CN al `node_id` en `/v1/mesh/join` desde la v1.0.3, pero el servidor de la RFC-002 no liga el CN al `agent_id` | [#66](https://github.com/Rafaeldelinares/gentle-mesh/issues/66) |
| S2 | sí | no cumplido | — | Verificar la firma del emisor antes de ejecutar o persistir | [#67](https://github.com/Rafaeldelinares/gentle-mesh/issues/67), [#48](https://github.com/Rafaeldelinares/gentle-mesh/issues/48) (parcial) |
| S3 | sí | sin código | — | Capacidades *deny-by-default* | ninguna |
| S4 | no | sin código | — | Política aplicada fuera del proceso del agente | ninguna |
| S5 | no | sin código | — | Delegación atenuada en cadena | ninguna |
| S6 | sí | no cumplido | `pkg/jcs/jcs_redteam_test.go:12` (débil) | ejecución de cadenas recibidas, ver #35 | #35 |
| S7 | sí | parcial | `pkg/receipt/chain_integrity_test.go:42,58` | Firmar los enlaces y la decisión del emisor | #43, #48, #41 |
| S8 | no | no cumplido | — | Revocación efectiva y kill switch | #25 |
| S9 | sí | cumplido | `pkg/envelope/version_test.go:11`; `pkg/receipt/version_test.go:11` | — | ninguna |
| R1 | sí | no cumplido | — | Idempotencia por `envelope_id` | [#68](https://github.com/Rafaeldelinares/gentle-mesh/issues/68) |
| R2 | sí | no cumplido | — | Expiración de lease y recibo `SETTLEMENT_TIMEOUT` | [#69](https://github.com/Rafaeldelinares/gentle-mesh/issues/69), [#8](https://github.com/Rafaeldelinares/gentle-mesh/issues/8) (parcial) |
| R3 | no | no cumplido | — | Reintentos sin efectos duplicados | ninguna |
| R4 | no | cumplido | `pkg/receipt/chain_integrity_test.go:142` | — | — |
| R5 | sí | parcial | `pkg/envelope/envelope_test.go:447`; `pkg/receipt/receipt_test.go:8` | Máquina de estados de contrato con terminal garantizado | ninguna |
| R6 | no | no cumplido | — | Test de independencia del reloj ajeno | ninguna |
| R7 | sí | cumplido | `pkg/envelope/signer_test.go:241` | — | ninguna |
| A1 | sí | parcial | — | Encargo de bajo riesgo sin declarar capacidades | ninguna |
| A2 | sí | parcial | `pkg/shell/shell_test.go:17` | Motivo estructurado en un lease rechazado | #37 (parcial) |
| A3 | no | no cumplido | — | Camino de un solo round-trip | ninguna |
| A4 | no | parcial | `pkg/signing/signing_test.go:421`; `pkg/jcs/jcs_test.go:485` | Benchmark con aserción integrado en CI | ninguna |
| A5 | no | sin código | — | Controles pesados solo cuando el riesgo supera el perfil mínimo | ninguna |
| A6 | no | parcial | — | Cobertura de la política desde la librería | ninguna |
| A7 | no | parcial | — | Capa de autorización separada y probada | ninguna |

«(débil)» marca un test que existe pero no demuestra el criterio: pasaría aunque se quitara la lógica que debería comprobar.

Los objetivos sin issue propio se siguen como casillas en el paraguas [#70](https://github.com/Rafaeldelinares/gentle-mesh/issues/70).

**Lectura honesta:** a la fecha y al commit indicados, del perfil mínimo conforme solo S9 y R7
están demostrados por tests automatizados; el resto está parcialmente implementado o sin
implementar, y ningún nodo del repositorio puede declararse hoy «gentle-mesh RFC-002 conforme».

El recuento por estado es: 3 cumplidos (S9, R4, R7), 7 parciales (S7, R5, A1, A2, A4, A6, A7),
9 no cumplidos (S1, S2, S6, S8, R1, R2, R3, R6, A3) y 4 sin código (S3, S4, S5, A5).

### Método y límites de la comprobación

La comprobación se hizo sobre el commit auditado leyendo el código y los tests, y ejecutando
la suite con `go test -race`. Los tests E2E que requieren Docker se leyeron, pero no se
ejecutaron; no se consultó el estado real del CI en GitHub; y el umbral de A4 (menos de 1 ms
por mensaje) no está medido de extremo a extremo (el benchmark mide firma y verificación sin
canonicalización, y no corre en CI).
