# Registro de Gobernanza del Repositorio

> **Repositorio:** `Rafaeldelinares/gentle-mesh`  
> **Fecha:** 28 de septiembre de 2026  
> **Responsable:** Alan Buscaglia / Rafael de Linares  
> **Propósito:** Documentar la configuración de ramas protegidas, retiro de ramas legadas y reglas permanentes de operación para agentes y administradores.

---

## 1. Comprobaciones Previas

Antes de proceder a la reconfiguración y retiro de ramas legadas, se verificaron los siguientes invariantes:

1. **Etiqueta inmutable de respaldo:**
   - La etiqueta `archive/rfc002-standalone` existe en el remoto `origin`:
     ```text
     e5b296af974f5690e717fc0b8968e1a3b5d48cac    refs/tags/archive/rfc002-standalone
     ```
2. **Auditoría de commits en `origin/master`:**
   - Comando ejecutado:
     ```bash
     git log origin/feat/rfc-002-settlement..origin/master --oneline
     ```
   - Salida observada:
     ```text
     e5b296a chore(tls): refresh TLS certificates for integration tests
     0430826 fix(settlement): restore executor signature verification and fix test routing bug
     1ea9155 docs(rfc-002): add objectives, non-goals, and decision principles specification
     80dbc0e test(tls): regenerate TLS certificates for integration tests
     ```
   - Justificación y conciliación de cada commit:
     - `e5b296a` y `80dbc0e`: Commits antiguos que regeneraban certificados efímeros en `integration/certs-generated/`. Descartados deliberadamente porque en `feat/rfc-002-settlement` la Fase 0 eliminó el tracking de certificados y claves privadas del repositorio (`.gitignore` estricto y generación dinámica en CI/tests).
     - `0430826`: Equivalente funcional ya presente en `origin/feat/rfc-002-settlement` como commit `65a7003` ("fix(settlement): restore executor signature verification and fix test routing bug").
     - `1ea9155`: Equivalente funcional ya presente en `origin/feat/rfc-002-settlement` como commit `473bb2c` ("docs(rfc-002): add objectives, non-goals, and decision principles specification").
     - **Conclusión:** No se pierde ningún trabajo o contenido funcional.
3. **Nombres exactos de checks de CI:**
   - `Lint & Security`
   - `Unit Tests`
   - `Integration Tests (Docker)`
4. **Rama por defecto:**
   - `main` verificada como rama por defecto (`gh repo view --json defaultBranchRef` devolvió `"name": "main"`).

---

## 2. Acciones Ejecutadas

### 2.1 Borrado de la rama legada `master`
```bash
git push origin --delete master
```
*Resultado:* `master` eliminada exitosamente del remoto.

### 2.2 Protección de `feat/rfc-002-settlement`
```bash
gh api -X PUT repos/Rafaeldelinares/gentle-mesh/branches/feat/rfc-002-settlement/protection --input /tmp/protection-settlement.json
```
Parámetros aplicados:
- **Required status checks:** `Lint & Security`, `Unit Tests`, `Integration Tests (Docker)`.
- **Strict status checks:** `true` (requiere estar al día con la rama base antes de mergear).
- **Enforce admins:** `true` (las reglas aplican también a administradores).
- **Pull request reviews:** `required_approving_review_count: 0`, `require_code_owner_reviews: false`.
- **Force pushes:** `false` (bloqueados).
- **Deletions:** `false` (bloqueadas).

### 2.3 Protección de `main`
```bash
gh api -X PUT repos/Rafaeldelinares/gentle-mesh/branches/main/protection --input /tmp/protection-main.json
```
Parámetros aplicados:
- **Required status checks:** `null` (main aún no tiene el workflow de CI de `.github/workflows/ci.yml`; exigir checks bloquearía cualquier PR hacia main. Se añadirá en cuanto el CI llegue a main).
- **Enforce admins:** `true`.
- **Pull request reviews:** `required_approving_review_count: 0`, `require_code_owner_reviews: false`.
- **Force pushes:** `false` (bloqueados).
- **Deletions:** `false` (bloqueadas).

---

## 3. Salida JSON Actual de las Protecciones de Rama

### 3.1 `feat/rfc-002-settlement`
```json
{
  "url": "https://api.github.com/repos/Rafaeldelinares/gentle-mesh/branches/feat/rfc-002-settlement/protection",
  "required_status_checks": {
    "url": "https://api.github.com/repos/Rafaeldelinares/gentle-mesh/branches/feat/rfc-002-settlement/protection/required_status_checks",
    "strict": true,
    "contexts": [
      "Lint & Security",
      "Unit Tests",
      "Integration Tests (Docker)"
    ],
    "contexts_url": "https://api.github.com/repos/Rafaeldelinares/gentle-mesh/branches/feat/rfc-002-settlement/protection/required_status_checks/contexts",
    "checks": [
      {
        "context": "Lint & Security",
        "app_id": 15368
      },
      {
        "context": "Unit Tests",
        "app_id": 15368
      },
      {
        "context": "Integration Tests (Docker)",
        "app_id": 15368
      }
    ]
  },
  "required_pull_request_reviews": {
    "url": "https://api.github.com/repos/Rafaeldelinares/gentle-mesh/branches/feat/rfc-002-settlement/protection/required_pull_request_reviews",
    "dismiss_stale_reviews": false,
    "require_code_owner_reviews": false,
    "require_last_push_approval": false,
    "required_approving_review_count": 0
  },
  "required_signatures": {
    "url": "https://api.github.com/repos/Rafaeldelinares/gentle-mesh/branches/feat/rfc-002-settlement/protection/required_signatures",
    "enabled": false
  },
  "enforce_admins": {
    "url": "https://api.github.com/repos/Rafaeldelinares/gentle-mesh/branches/feat/rfc-002-settlement/protection/enforce_admins",
    "enabled": true
  },
  "required_linear_history": {
    "enabled": false
  },
  "allow_force_pushes": {
    "enabled": false
  },
  "allow_deletions": {
    "enabled": false
  },
  "block_creations": {
    "enabled": false
  },
  "required_conversation_resolution": {
    "enabled": false
  },
  "lock_branch": {
    "enabled": false
  },
  "allow_fork_syncing": {
    "enabled": false
  }
}
```

### 3.2 `main`
```json
{
  "url": "https://api.github.com/repos/Rafaeldelinares/gentle-mesh/branches/main/protection",
  "required_pull_request_reviews": {
    "url": "https://api.github.com/repos/Rafaeldelinares/gentle-mesh/branches/main/protection/required_pull_request_reviews",
    "dismiss_stale_reviews": false,
    "require_code_owner_reviews": false,
    "require_last_push_approval": false,
    "required_approving_review_count": 0
  },
  "required_signatures": {
    "url": "https://api.github.com/repos/Rafaeldelinares/gentle-mesh/branches/main/protection/required_signatures",
    "enabled": false
  },
  "enforce_admins": {
    "url": "https://api.github.com/repos/Rafaeldelinares/gentle-mesh/branches/main/protection/enforce_admins",
    "enabled": true
  },
  "required_linear_history": {
    "enabled": false
  },
  "allow_force_pushes": {
    "enabled": false
  },
  "allow_deletions": {
    "enabled": false
  },
  "block_creations": {
    "enabled": false
  },
  "required_conversation_resolution": {
    "enabled": false
  },
  "lock_branch": {
    "enabled": false
  },
  "allow_fork_syncing": {
    "enabled": false
  }
}
```

---

## 4. Decisiones Tomadas y Criterios de Endurecimiento

1. **Revisiones Requeridas (`required_approving_review_count: 0`):**
   - **Causa:** Los PRs y tokens de automatización actuales operan bajo la identidad `@Rafaeldelinares`. GitHub prohíbe explícitamente que el creador de un PR apruebe sus propios cambios.
   - **Condición de endurecimiento:** Tan pronto como se configure una identidad separada para el agente (cuenta bot o GitHub App independiente sin privilegios de administración), se elevará a:
     - `required_approving_review_count: 1`
     - `require_code_owner_reviews: true`
2. **Checks en `main`:**
   - La rama `main` aún conserva el estado de la versión v1.0.1 y no contiene el directorio `.github/workflows/ci.yml`. Exigir los checks en `main` en este momento provocaría que cualquier PR legítimo hacia `main` quedase bloqueado indefinidamente.
   - **Condición de endurecimiento:** Cuando se porte el CI a `main` (con la resolución del Issue #10: "ci: llevar el workflow de CI y los security gates a main"), se añadirán de inmediato los 3 checks requeridos con `strict: true`.

---

## 5. Regla Permanente de Operación

> ⚠️ **REGLA PERMANENTE:**
> El agente de IA **NO TIENE PERMITIDO** modificar, relajar ni eliminar las reglas de protección de rama, los archivos `CODEOWNERS` ni los flujos de CI sin una instrucción explícita e inequívoca del humano en la conversación.
> Todo cambio posterior en la gobernanza debe ser registrado obligatoriamente en este archivo (`docs/ops/REPO-GOVERNANCE.md`) mediante un PR auditable.

---

## 6. Verificación de Protección (Prueba de Rechazo)

Para verificar empíricamente que la protección de rama funciona y bloquea commits directos (incluso con permisos de administrador gracias a `enforce_admins: true`), se ejecutó la siguiente prueba:

```bash
git commit --allow-empty -m "test: protection check"
git push origin HEAD:feat/rfc-002-settlement
```

### Salida Literal del Rechazo por GitHub:
```text
remote: error: GH006: Protected branch update failed for refs/heads/feat/rfc-002-settlement.        
remote: 
remote: - Changes must be made through a pull request.        
remote: 
remote: - 3 of 3 required status checks are expected.        
To https://github.com/Rafaeldelinares/gentle-mesh.git
 ! [remote rejected] HEAD -> feat/rfc-002-settlement (protected branch hook declined)
error: falló el empuje de algunas referencias a 'https://github.com/Rafaeldelinares/gentle-mesh.git'
```
*Prueba superada: El hook de GitHub declinó el empuje directo.*

---

## 7. Pendientes Identificados

- [ ] Crear identidad bot / GitHub App dedicada para el agente (sin permisos de administración).
- [ ] Elevar `required_approving_review_count` a 1 y habilitar `require_code_owner_reviews`.
- [ ] Añadir checks obligatorios de CI a `main` tras sincronizar `.github/workflows/ci.yml`.
