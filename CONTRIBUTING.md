# Guía de Contribución

Gracias por tu interés en **Gentle Mesh** — transporte distribuido federado, ejecución remota de subagentes y consciencia situacional para el ecosistema Pi & Gentle AI.

Leé esta guía completa antes de empezar: el proyecto sigue un flujo estructurado.

---

## Cómo proponer cambios

1. **Abrí un issue primero.** Describí el problema o la propuesta con el mayor detalle posible y esperá confirmación antes de escribir código. Un PR sin issue de referencia puede ser rechazado.
2. **Comentá el issue** para avisar que lo estás trabajando y evitar trabajo duplicado.
3. **Abrí el PR** contra `main`, referenciando el issue en la descripción.

Si dudás por dónde empezar, revisá los issues abiertos con etiquetas de ayuda; los que están en discusión conviene resolverlos antes de implementar.

### Ramas

Usá minúsculas y separadores con guiones: `feat/…`, `fix/…`, `docs/…`, `chore/…`, `ci/…`, `test/…`, `refactor/…`.

Ejemplos: `feat/territory-lock`, `fix/sse-reconnect`, `docs/contributing-guide`.

### Commits

Seguimos [Conventional Commits](https://www.conventionalcommits.org/):

```
<tipo>(<alcance opcional>): <descripción>
```

Tipos habituales: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `ci`.

### Antes de abrir un PR

- [ ] Hay un issue vinculado.
- [ ] El PR tiene **un único propósito** y es revisable. Apuntá a ~400 líneas cambiadas; si el cambio es mayor, partilo en PRs encadenados.
- [ ] Los tests pasan.
- [ ] Las compuertas de seguridad pasan.
- [ ] Declaraste la asistencia de IA (ver más abajo).
- [ ] Revisaste cada línea y podés explicar y defender el diseño y sus consecuencias.

---

## Contribuciones asistidas por IA

La asistencia de IA está permitida, pero **la responsabilidad es enteramente humana**: quien firma el PR debe entender, revisar, validar y poder defender todo lo que envía.

- **Declará** en la descripción del PR el uso material de IA: herramienta o modelo (si lo conocés), alcance de la asistencia y qué verificación realizaste.
- No hace falta declarar correcciones de tipeo, formato, autocompletado trivial ni transformaciones mecánicas menores.
- La IA **no** recibe atribución humana: no agregues `Co-Authored-By`, `Reviewed-by`, `Tested-by` ni equivalentes apuntando a una herramienta.
- No envíes salidas que no hayas revisado, ni afirmes resultados que no ocurrieron.

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

Al enviar una contribución aceptás que se licencie bajo la **licencia MIT** de este proyecto (**entrada igual a salida**, *inbound = outbound*). Ver [`LICENSE`](LICENSE).

No se requiere firmar ningún CLA.

---

## Seguridad

**No abras issues públicos para vulnerabilidades de seguridad.**

Reportalas por el canal privado:

- **GitHub Security Advisories** (preferido):
  <https://github.com/Rafaeldelinares/gentle-mesh/security/advisories/new>
- **Email**: si GitHub no está disponible, contactá a través del perfil de GitHub del mantenedor.

Ver [`SECURITY.md`](SECURITY.md) para el alcance, los plazos de respuesta y las limitaciones conocidas.
