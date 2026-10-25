# Revisión automática (RDD): estado y excepciones

La revisión automática de gentle-ai (RDD) no ha podido ejecutarse en
el entorno aislado de este proyecto. Los PR #72, #74 y #75 (solo
documentación) se mergearon con una excepción documentada del
mantenedor.

## Motivos observados

- `immutable_review_transport_unsupported`: el runtime solo admite la
  revisión mientras la retransmite el host de Pi (gentle-pi), que es
  quien exporta `GENTLE_PI_REVIEW_RELAY_CONTRACT`. No se ha simulado
  el host de Pi.
- La herramienta `gentle_review` del host de Pi no admite una base
  explícita (`--base-ref`): con la rama ya confirmada y el árbol
  limpio devuelve `empty_candidate_base_ref_required`.
- Los activos gestionados de gentle-ai figuraban como desactualizados
  (`managed_assets_outdated`); no se ejecutó la sincronización.
- Errores de consentimiento (`consent-binding-stale` y
  `consent-binding-unknown`) sin causa determinada.

## Qué se hizo en su lugar

CI en verde sobre el head, lectura manual del diff completo,
comprobación de las afirmaciones externas contra su fuente o contra
el código, escaneo de voseo, caracteres no latinos y palabras de
cierre, y la demo ejecutada en contenedor y en local.

## Alcance de la excepción

Solo cubre cambios de documentación. Cada PR acogido a ella lo indica
en un comentario. Los cambios de código no se mergean sin revisión, o
se decide caso por caso y se documenta. Se informará del caso a
gentle-ai.
