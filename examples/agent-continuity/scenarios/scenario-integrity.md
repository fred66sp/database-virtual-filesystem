# Escenario de integridad

Runbook ejecutable de las garantías deterministas que ACM hereda de SQLite y
db-ai: aislamiento entre proyectos, conflicto de ETag entre dos escritores,
deduplicación exacta y denegación de escritura fuera de la política.

Trabaja sobre bases temporales para no ensuciar `.agent-memory/memory.db`. La
configuración de db-ai también es temporal: `XDG_CONFIG_HOME` apunta al
directorio del escenario.

```bash
TMP=$(mktemp -d)
export XDG_CONFIG_HOME=$TMP/config

ACM_HOME=$TMP/proyecto-a ACM_ALIAS=mem-a bash init.sh
ACM_HOME=$TMP/proyecto-b ACM_ALIAS=mem-b bash init.sh
```

Esperado: dos inicializaciones con `ok` en su verificación final, con bases y
alias distintos.

## 1. Aislamiento por proyecto

Cada base tiene su propio `project_key` y solo ve sus filas:

```bash
KEY_A=$(db-ai find /mem-a/meta --where 'name=project_key' --limit 1 | sed -n 's/.*"value":{"kind":"text","data":"\([^"]*\)".*/\1/p')
KEY_B=$(db-ai find /mem-b/meta --where 'name=project_key' --limit 1 | sed -n 's/.*"value":{"kind":"text","data":"\([^"]*\)".*/\1/p')
printf 'A=%s\nB=%s\n' "$KEY_A" "$KEY_B"
test "$KEY_A" != "$KEY_B" && echo "aislamiento ok"

db-ai find /mem-a/sessions --limit 10
db-ai find /mem-b/sessions --limit 10
```

Esperado: las dos claves difieren; cada base devuelve sus propias sesiones.
Una escritura en `mem-a` no aparece en `mem-b`.

## 2. Conflicto de ETag entre dos escritores

Dos agentes leen la misma fila y conservan el mismo ETag. El primero escribe; el
segundo recibe `E_CONFLICT` y debe releer antes de decidir.

```bash
ROW=/mem-a/observations/01a1f42d-ba80-76a7-8b8c-9d0e1f2a3bac
ETAG=$(db-ai cat "$ROW" | sed -n 's/.*"etag":"\([^"]*\)".*/\1/p')

# Agente 1
db-ai write "$ROW" --data '{"status":{"kind":"text","data":"needs_review"}}' --if-match "$ETAG"

# Agente 2, con el ETag ya obsoleto
db-ai write "$ROW" --data '{"status":{"kind":"text","data":"obsolete"}}' --if-match "$ETAG"
echo "exit=$?"
```

Esperado: el primer `write` responde `ok:true`; el segundo responde
`{"ok":false,"error":{"code":"E_CONFLICT",...}}` con código de salida 5. La
relectura muestra el valor del primero, no una actualización perdida.

## 3. Deduplicación exacta

Una segunda captura con el mismo `content_hash` no crea fila:

```bash
HASH=$(printf '%s' '["discovery","Duplicado exacto de prueba","Mismo contenido, mismo hash; el esquema debe impedir la segunda fila.","","","",""]' | sha256sum | cut -d' ' -f1)

db-ai touch /mem-a/observations --data "{
  \"id\":{\"kind\":\"text\",\"data\":\"01a1f600-2222-7081-9000-000000000001\"},
  \"session_id\":{\"kind\":\"text\",\"data\":\"01a1f313-bf00-70a1-8b2c-3d4e5f6a7b8c\"},
  \"agent_id\":{\"kind\":\"text\",\"data\":\"agent-demo\"},
  \"source\":{\"kind\":\"text\",\"data\":\"agent\"},
  \"kind\":{\"kind\":\"text\",\"data\":\"discovery\"},
  \"title\":{\"kind\":\"text\",\"data\":\"Duplicado exacto de prueba\"},
  \"body\":{\"kind\":\"text\",\"data\":\"Mismo contenido, mismo hash; el esquema debe impedir la segunda fila.\"},
  \"confidence\":{\"kind\":\"text\",\"data\":\"confirmed\"},
  \"last_seen_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T16:00:00Z\"},
  \"content_hash\":{\"kind\":\"text\",\"data\":\"sha256:$HASH\"},
  \"created_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T16:00:00Z\"},
  \"updated_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T16:00:00Z\"}
}" >/dev/null

# Segunda captura: distinto id, mismo hash
db-ai touch /mem-a/observations --data "{
  \"id\":{\"kind\":\"text\",\"data\":\"01a1f600-2222-7081-9000-000000000002\"},
  \"session_id\":{\"kind\":\"text\",\"data\":\"01a1f313-bf00-70a1-8b2c-3d4e5f6a7b8c\"},
  \"agent_id\":{\"kind\":\"text\",\"data\":\"agent-demo\"},
  \"source\":{\"kind\":\"text\",\"data\":\"agent\"},
  \"kind\":{\"kind\":\"text\",\"data\":\"discovery\"},
  \"title\":{\"kind\":\"text\",\"data\":\"Duplicado exacto de prueba\"},
  \"body\":{\"kind\":\"text\",\"data\":\"Mismo contenido, mismo hash; el esquema debe impedir la segunda fila.\"},
  \"confidence\":{\"kind\":\"text\",\"data\":\"confirmed\"},
  \"last_seen_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T16:01:00Z\"},
  \"content_hash\":{\"kind\":\"text\",\"data\":\"sha256:$HASH\"},
  \"created_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T16:01:00Z\"},
  \"updated_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T16:01:00Z\"}
}"; echo "exit=$?"
```

Esperado: el primer `touch` responde `ok:true`; el segundo devuelve
`E_CONSTRAINT` con código 6 y no crea fila. La recuperación convencional del
duplicado (buscar por hash y actualizar `duplicate_count` y `last_seen_at`) es
convención de la skill, no del motor.

## 4. Política: escritura denegada en metadatos

```bash
db-ai touch /mem-a/meta --data '{
  "id":{"kind":"text","data":"01a1f600-2222-7081-9000-000000000003"},
  "name":{"kind":"text","data":"intruso"},
  "value":{"kind":"text","data":"x"},
  "updated_at":{"kind":"text","data":"2026-11-19T16:00:00Z"}
}'; echo "exit=$?"
```

Esperado: `{"ok":false,"error":{"code":"E_DENIED",...}}` con código de salida 4.
La política de DESIGN §6 no concede ninguna escritura sobre `/mem/meta`; el
metadato solo se cambia en bootstrap o migración revisada, fuera de db-ai.

## 5. Operaciones que no existen en la política

```bash
db-ai rm /mem-a/observations/01a1f42d-ba80-76a7-8b8c-9d0e1f2a3bac \
  --if-match "$(db-ai cat /mem-a/observations/01a1f42d-ba80-76a7-8b8c-9d0e1f2a3bac | sed -n 's/.*"etag":"\([^"]*\)".*/\1/p')" \
  --apply
echo "exit=$?"
```

Esperado: `E_DENIED` con código 4. La obsolescencia se expresa con `status =
'obsolete'`, no borrando. `cp` y `mv` tampoco están autorizados por la política.

## Cierre

```bash
rm -rf "$TMP"
```

El borrado es del operador sobre una copia temporal, fuera de db-ai; el
escenario no usa `rm` contra una base real.
