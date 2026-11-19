# Escenario de continuidad A → B

Runbook ejecutable del flujo que justifica ACM: un agente A trabaja sin memoria
previa, captura conocimiento y deja un checkpoint; un agente B recupera el
estado y continúa sin leer la conversación de A.

Ejecuta los pasos desde `examples/agent-continuity/`. Los comandos son la forma
real de db-ai v0.2.0; el flujo de operaciones y los códigos de error están en la
skill `db-ai` de SKILLS-AGP (`SKILL.md` y `references/`). Este escenario no
reproduce esa skill: la usa.

Para repetirlo desde cero, elimina `.agent-memory/` y vuelve a ejecutar
`bash init.sh --demo`. El borrado es una operación del operador fuera de db-ai;
la política deniega `rm`.

## Preparación (una vez)

```bash
bash init.sh --demo
db-ai mounts
```

Esperado: `mounts` incluye `{"alias":"mem", ... "mode":"rw"}` y la base contiene
el fixture de demostración que aporta `--demo`.

## Agente A: abrir sesión

Sustituye el UUID por uno nuevo si repites el escenario: la clave primaria es
única y un segundo `touch` idéntico devuelve `E_CONSTRAINT`.

```bash
export SESSION=01a1f500-1111-7081-9000-0000000000a1

db-ai touch /mem/sessions --data "{
  \"id\":{\"kind\":\"text\",\"data\":\"$SESSION\"},
  \"agent_id\":{\"kind\":\"text\",\"data\":\"agent-a-demo\"},
  \"source\":{\"kind\":\"text\",\"data\":\"agent\"},
  \"goal\":{\"kind\":\"text\",\"data\":\"Demostrar el flujo A a B\"},
  \"status\":{\"kind\":\"text\",\"data\":\"open\"},
  \"started_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T15:00:00Z\"},
  \"created_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T15:00:00Z\"},
  \"updated_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T15:00:00Z\"}
}"
```

Esperado: `ok:true` y la ruta `/mem/sessions/<uuid>`.

## Agente A: descubrir antes de escribir

```bash
db-ai describe /mem/observations
db-ai find /mem/preferences --where 'status=active' --limit 20
db-ai find /mem/observations --where 'kind=decision' --where 'status=active' --limit 10
```

Esperado: columnas y capacidades de la tabla; tres preferencias activas; la
decisión activa del seed con `topic_key = storage/db-per-project`.

## Agente A: capturar un hallazgo

El hash sigue DESIGN §5.2: array JSON compacto de los campos semánticos
(`kind`, `title`, `body`, `rationale`, `alternatives`, `consequences`,
`topic_key`) y SHA-256 sobre sus bytes UTF-8.

```bash
HASH_JSON='["discovery","El alias mem exige política rw explícita","db-ai deniega la escritura sin una entrada allow para el prefijo; mounts confirma el modo antes de mutar.","","","",""]'
HASH="sha256:$(printf '%s' "$HASH_JSON" | sha256sum | cut -d' ' -f1)"

db-ai touch /mem/observations --data "{
  \"id\":{\"kind\":\"text\",\"data\":\"01a1f500-1111-7081-9000-0000000000a2\"},
  \"session_id\":{\"kind\":\"text\",\"data\":\"$SESSION\"},
  \"agent_id\":{\"kind\":\"text\",\"data\":\"agent-a-demo\"},
  \"source\":{\"kind\":\"text\",\"data\":\"agent\"},
  \"kind\":{\"kind\":\"text\",\"data\":\"discovery\"},
  \"title\":{\"kind\":\"text\",\"data\":\"El alias mem exige política rw explícita\"},
  \"body\":{\"kind\":\"text\",\"data\":\"db-ai deniega la escritura sin una entrada allow para el prefijo; mounts confirma el modo antes de mutar.\"},
  \"confidence\":{\"kind\":\"text\",\"data\":\"confirmed\"},
  \"last_seen_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T15:01:00Z\"},
  \"content_hash\":{\"kind\":\"text\",\"data\":\"$HASH\"},
  \"created_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T15:01:00Z\"},
  \"updated_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T15:01:00Z\"}
}"
```

Esperado: `ok:true` y `etag`. Repetir el mismo `touch` devuelve `E_CONSTRAINT`
por `UNIQUE(content_hash)`; no se reintenta la inserción (DESIGN §5.3).

## Agente A: checkpoint y cierre

```bash
db-ai touch /mem/checkpoints --data "{
  \"id\":{\"kind\":\"text\",\"data\":\"01a1f500-1111-7081-9000-0000000000a3\"},
  \"session_id\":{\"kind\":\"text\",\"data\":\"$SESSION\"},
  \"agent_id\":{\"kind\":\"text\",\"data\":\"agent-a-demo\"},
  \"source\":{\"kind\":\"text\",\"data\":\"agent\"},
  \"goal\":{\"kind\":\"text\",\"data\":\"Demostrar el flujo A a B\"},
  \"state\":{\"kind\":\"text\",\"data\":\"Sesión abierta y observación capturada.\"},
  \"next_steps\":{\"kind\":\"text\",\"data\":\"Reanudar en una sesión B y verificar la recuperación.\"},
  \"files\":{\"kind\":\"text\",\"data\":\"[\\\"examples/agent-continuity/init.sh\\\"]\"},
  \"open_questions\":{\"kind\":\"text\",\"data\":\"[]\"},
  \"content_hash\":{\"kind\":\"text\",\"data\":\"sha256:$(printf '%s' '["Demostrar el flujo A a B","Sesión abierta y observación capturada.","Reanudar en una sesión B y verificar la recuperación.","[\"examples/agent-continuity/init.sh\"]","[]"]' | sha256sum | cut -d' ' -f1)\"},
  \"created_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T15:02:00Z\"},
  \"updated_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T15:02:00Z\"}
}"
```

Cerrar la sesión exige el ETag vigente (se lee de la fila, no se inventa):

```bash
ROW=/mem/sessions/$SESSION
ETAG=$(db-ai cat "$ROW" | sed -n 's/.*"etag":"\([^"]*\)".*/\1/p')

db-ai write "$ROW" --data '{
  "status":{"kind":"text","data":"closed"},
  "ended_at":{"kind":"text","data":"2026-11-19T15:10:00Z"},
  "summary":{"kind":"text","data":"Flujo A completado; reanudación pendiente en B."},
  "updated_at":{"kind":"text","data":"2026-11-19T15:10:00Z"}
}' --if-match "$ETAG"
```

Esperado: `ok:true` con un ETag nuevo. Un `write` con el ETag anterior devuelve
`E_CONFLICT`.

## Agente B: reanudar sin la conversación

```bash
db-ai find /mem/sessions --where 'status=open' --limit 10
db-ai find /mem/checkpoints --where "session_id=$SESSION" --where 'status=active' --limit 5
db-ai find /mem/observations --where "session_id=$SESSION" --limit 50
db-ai find /mem/observations --where 'kind=decision' --where 'status=active' --limit 10
db-ai find /mem/preferences --where 'status=active' --limit 20
```

Esperado:

- La sesión de A queda cerrada y no aparece entre las abiertas; sí aparecen las
  abiertas del seed o de B.
- El checkpoint de A devuelve objetivo, estado, próximos pasos y ficheros.
- La observación de A aparece en su historial de sesión.
- La decisión activa `storage/db-per-project` aparece una sola vez; la
  sustituida `storage/db-per-session` no se devuelve como activa.
- Las tres preferencias activas aparecen con su valor.

B ya puede continuar: lee el checkpoint, aplica las preferencias y no repite los
hallazgos de A. Si necesita el detalle de una fila, la lee por su ruta:

```bash
db-ai cat /mem/observations/01a1f344-44e0-75a6-8b7c-8d9e0f1a2bac
```

## Límite del escenario

La deduplicación exacta y la sustitución son garantías del esquema y del
protocolo de lote; no detectan duplicados semánticos ni impiden dos primeras
capturas concurrentes del mismo tema (DESIGN §5.4). La reconciliación de esos
casos es convención de la skill `agent-continuity` (ACM-04).
