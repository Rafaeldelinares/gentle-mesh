# Guía de contribución

Gracias por tu interés en **Gentle Mesh** — transporte distribuido federado, ejecución remota de subagentes y consciencia situacional para el ecosistema Pi & Gentle AI.

Lee esta guía completa antes de empezar: el proyecto sigue un flujo estructurado.

---

## Cómo proponer cambios

Como convención, abre primero un issue: describe el problema o la propuesta con el mayor detalle posible y espera confirmación antes de escribir código. Así evitas trabajo duplicado y permites contrastar el enfoque antes de invertir esfuerzo.

Excepciones, que no necesitan issue:

- Erratas y cambios de formato.
- PRs de documentación o de mantenimiento abiertos por el mantenedor.

Para el resto de cambios, abre el PR contra `main` y referencia el issue en la descripción.

Si dudas por dónde empezar, revisa los issues abiertos con etiquetas de ayuda; los que están en discusión conviene resolverlos antes de implementar.

### Ramas

Usa minúsculas y separadores con guiones: `feat/…`, `fix/…`, `docs/…`, `chore/…`, `ci/…`, `test/…`, `refactor/…`.

Ejemplos: `feat/territory-lock`, `fix/sse-reconnect`, `docs/contributing-guide`.

### Commits

Seguimos [Conventional Commits](https://www.conventionalcommits.org/):

```
<tipo>(<alcance opcional>): <descripción>
```

Tipos habituales: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `ci`.

### Antes de abrir un PR

- [ ] Hay un issue vinculado (salvo las excepciones indicadas).
- [ ] El PR tiene **un único propósito** y es revisable. Apunta a ~400 líneas cambiadas; si el cambio es mayor, divídelo en PRs encadenados.
- [ ] Los tests pasan.
- [ ] Las compuertas de seguridad pasan.
- [ ] Has declarado la asistencia de IA (ver más abajo).
- [ ] Has revisado cada línea y puedes explicar y defender el diseño y sus consecuencias.

---

## Contribuciones asistidas por IA

La asistencia de IA está permitida, pero **la responsabilidad es enteramente humana**: quien firma el PR debe entender, revisar, validar y poder defender todo lo que envía.

- **Declara** en la descripción del PR el uso material de IA: herramienta o modelo (si lo conoces), alcance de la asistencia y qué verificación has realizado.
- No hace falta declarar correcciones de tipeo, formato, autocompletado trivial ni transformaciones mecánicas menores.
- La IA **no** recibe atribución humana: no añadas `Co-Authored-By`, `Reviewed-by`, `Tested-by` ni equivalentes apuntando a una herramienta.
- No envíes salidas que no hayas revisado, ni afirmes resultados que no hayan ocurrido.

---

## Tests y compuertas locales

```bash
# Suite completa con detector de carreras
go test -race ./...

# Compuertas de seguridad del repositorio
./scripts/security-gates.sh
```

Ambos comandos deben pasar antes de abrir un PR.

---

## Licencia

Al enviar una contribución aceptas que se licencie bajo la **licencia MIT** de este proyecto (**entrada igual a salida**, *inbound = outbound*). Ver [`LICENSE`](LICENSE).

No se requiere firmar ningún CLA.

---

## Seguridad

**No abras issues públicos para vulnerabilidades de seguridad.**

Repórtalas por el canal privado:

- **GitHub Security Advisories** (preferido):
  <https://github.com/Rafaeldelinares/gentle-mesh/security/advisories/new>
- Si GitHub no está disponible, contacta por el perfil de GitHub del mantenedor.

Ver [`SECURITY.md`](SECURITY.md) para el alcance.
