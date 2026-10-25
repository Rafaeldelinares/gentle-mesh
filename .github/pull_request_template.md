## Resumen

<!-- Descripción concisa del objetivo, contexto y alcance del cambio -->

---

## Mapeo de Objetivos y Tests

| ID Objetivo | Cambio Implementado | Test Asociado |
|---|---|---|
| <!-- Ej: S1 / 1.2 --> | <!-- Descripción del cambio --> | <!-- Test unitario / integración / adversarial --> |

---

## Hallazgos y Consideraciones de Seguridad

<!-- Análisis de seguridad, higiene y controles (STRIDE, gates, certificados, validaciones) -->

---

## Cómo intentaría romper esto

<!-- Perspectiva de Red Team / Adversarial: qué vectores de ataque o casos límite podrían comprometer esta implementación -->

---

## Diferido

<!-- Tareas o mejoras identificadas que quedan explícitamente fuera de este PR y su tracking issue correspondiente -->

---

## Checklist

- [ ] Límite de diff respetado (<400 líneas, sin contar generados).
- [ ] TDD aplicado: tests adversarios/funcionales fallaron primero (rojo) antes del fix (verde).
- [ ] `go test -race ./...` pasa localmente.
- [ ] `./scripts/security-gates.sh` reporta 0 violaciones.
- [ ] `staticcheck ./...` pasa con 0 advertencias (sin supresiones en código ni excepciones en allowlist).
- [ ] `govulncheck ./...` y `gosec` pasan limpiamente.
- [ ] Revisión RDD completada y aprobada (o excepción explícita documentada).

---

## Declaración de asistencia de IA

<!-- Completa esta sección si usaste herramientas o modelos de IA para asistir en este PR -->

**Herramienta o modelo:** <!-- ej: Claude (Anthropic), Copilot, GPT-4, Gentle-AI, otro -->

**Alcance de la asistencia:** <!-- Marca las que apliquen -->
- [ ] Investigación y planificación
- [ ] Escritura de código o tests
- [ ] Revisión y verificación
- [ ] Documentación
- [ ] Corrección ortográfica, formato o transformaciones mecánicas menores (no hace falta declarar)

**Verificación realizada:** <!-- Describe brevemente qué hiciste para verificar que el resultado es correcto -->

**Responsable:** Quien suscribe este PR confirma que entiende, ha revisado y puede defender todo el contenido enviado. La IA no reemplaza la responsabilidad humana.

---

[![Built with Gentle-AI](https://raw.githubusercontent.com/Gentleman-Programming/gentle-ai/main/docs/assets/brand/built-with-gentle-ai.png)](https://github.com/Gentleman-Programming/gentle-ai)
