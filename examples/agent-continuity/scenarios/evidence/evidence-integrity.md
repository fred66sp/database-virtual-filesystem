# Evidencia — escenario de integridad (ACM-05)

- **Fecha:** 2026-11-19 (Europe/Madrid, UTC+01:00).
- **Alias usados:** `mem-a` y `mem-b` sobre bases temporales independientes.
- **Bases:** `/tmp/opencode/acm-integrity/proyecto-{a,b}/.agent-memory/memory.db`.
  Configuración de db-ai también temporal (`XDG_CONFIG_HOME=/tmp/opencode/acm-integrity/config`);
  la configuración real del usuario no se tocó.
- **db-ai:** binario local construido del repo (sin comando version, C-ACM-01).
  (`db-ai version` y `db-ai --version` devuelven `E_USAGE` con exit 2.)
- **Transcripción:** los `--data` largos se abrevian con `…` o `{ ... }`; los campos omitidos
  son los del runbook y los que aparecen en la salida. La forma exacta está en
  `scenario-integrity.md`.

## Dos proyectos, dos bases, dos alias

```bash
TMP=/tmp/opencode/acm-integrity
export XDG_CONFIG_HOME=$TMP/config
ACM_HOME=$TMP/proyecto-a ACM_ALIAS=mem-a bash init.sh
ACM_HOME=$TMP/proyecto-b ACM_ALIAS=mem-b bash init.sh
db-ai mounts
```

Salida observada (recortada):

```text
esquema: aplicado en /tmp/opencode/acm-integrity/proyecto-a/.agent-memory/memory.db
seed: aplicado (base vacía)
identidad: project_key generado (01a1f49b-cd64-7660-8238-62aaf76702f8)
montaje: alias 'mem-a' -> /tmp/opencode/acm-integrity/proyecto-a/.agent-memory/memory.db (rw, ...)
...
esquema: aplicado en /tmp/opencode/acm-integrity/proyecto-b/.agent-memory/memory.db
seed: aplicado (base vacía)
identidad: project_key generado (01a1f49b-cdc8-7145-8642-e57c770ac59e)
montaje: alias 'mem-b' -> /tmp/opencode/acm-integrity/proyecto-b/.agent-memory/memory.db (rw, ...)

{"version":1,"ok":true,"data":{"mounts":[
 {"alias":"mem-a","file":".../proyecto-a/.agent-memory/memory.db","mode":"rw"},
 {"alias":"mem-b","file":".../proyecto-b/.agent-memory/memory.db","mode":"rw"}]}}
```

**Veredicto: SUPERADO.** Dos inicializaciones completas con alias y ficheros distintos.

## Aislamiento por project_key

```bash
KEY_A=$(db-ai find /mem-a/meta --where 'name=project_key' --limit 1 | sed -n 's/.*"value":{"kind":"text","data":"\([^"]*\)".*/\1/p')
KEY_B=$(db-ai find /mem-b/meta --where 'name=project_key' --limit 1 | sed -n 's/.*"value":{"kind":"text","data":"\([^"]*\)".*/\1/p')
printf 'A=%s\nB=%s\n' "$KEY_A" "$KEY_B"
test "$KEY_A" != "$KEY_B" && echo "aislamiento ok"
```

Salida observada:

```text
A=01a1f49b-cd64-7660-8238-62aaf76702f8
B=01a1f49b-cdc8-7145-8642-e57c770ac59e
aislamiento ok
```

**Veredicto: SUPERADO.** Identidades de proyecto distintas generadas por `init.sh`.

## Aislamiento de escritura: mem-a no aparece en mem-b

```bash
db-ai touch /mem-a/sessions --data '{ "id":"01a1f700-3333-7081-9000-00000000000a",
  "agent_id":"agent-alpha", "goal":"Fila solo del proyecto A", "status":"open", ... }'
db-ai find /mem-b/sessions --where 'id=01a1f700-3333-7081-9000-00000000000a' --limit 10
```

Salida observada:

```text
write_exit=0
{"version":1,"ok":true,"data":{"path":"/mem-b/sessions","rows":[]},"meta":{"truncated":false,...}}
```

**Veredicto: SUPERADO.** La fila creada en `mem-a` no existe en la base del proyecto B.

## Concurrencia: dos escritores con el mismo if_match

Dos procesos `db-ai write` lanzados en paralelo, ambos con el ETag leído antes de escribir:

```bash
ROW=/mem-a/observations/01a1f42d-ba80-76a7-8b8c-9d0e1f2a3bac
ETAG=$(db-ai cat "$ROW" | sed -n 's/.*"etag":"\([^"]*\)".*/\1/p')
db-ai write "$ROW" --data '{"status":{"kind":"text","data":"needs_review"}}' --if-match "$ETAG" >w1.out 2>&1 & p1=$!
db-ai write "$ROW" --data '{"status":{"kind":"text","data":"obsolete"}}'     --if-match "$ETAG" >w2.out 2>&1 & p2=$!
wait $p1; rc1=$?; wait $p2; rc2=$?
db-ai cat "$ROW/status"
```

Salida observada:

```text
etag_leido=sha256:1545a26809990ba39305e24092597d114c0c46b453a8630bde94cca011c88e8b
escritor_1 rc=5: {"version":1,"ok":false,"error":{"code":"E_CONFLICT","message":"conflict: ETag \"sha256:1545a268...\" no longer matches table \"observations\""}}
escritor_2 rc=0: {"version":1,"ok":true,"data":{"path":"/mem-a/observations/01a1f42d-ba80-76a7-8b8c-9d0e1f2a3bac","fields":{...,"status":{"kind":"text","data":"obsolete"}},"etag":"sha256:c9c82f4f..."}}

-- relectura:
{"version":1,"ok":true,"data":{"kind":"text","data":"obsolete"}}
```

**Veredicto: SUPERADO.** Exactamente una escritura `ok:true` y una `E_CONFLICT` (exit 5);
la relectura muestra el valor del ganador, sin actualización perdida. En paralelo el ganador
no está determinado (aquí el escritor 2); el runbook secuencial obtiene el mismo reparto con
el primero como ganador.

## Deduplicación exacta: segundo touch con el mismo content_hash

```bash
HASH=$(printf '%s' '["discovery","Duplicado exacto de prueba","Mismo contenido, mismo hash; el esquema debe impedir la segunda fila.","","","",""]' | sha256sum | cut -d' ' -f1)
# primera captura: id ...0001
db-ai touch /mem-a/observations --data '{ ... "id":"01a1f600-2222-7081-9000-000000000001", "content_hash":"sha256:$HASH", ... }'
# segunda captura: id ...0002, mismo content_hash
db-ai touch /mem-a/observations --data '{ ... "id":"01a1f600-2222-7081-9000-000000000002", "content_hash":"sha256:$HASH", ... }'
sqlite3 .../memory.db "SELECT count(*) FROM observations WHERE content_hash='sha256:$HASH';"
```

Salida observada:

```text
primera_captura_exit=0
{"version":1,"ok":false,"error":{"code":"E_CONSTRAINT","message":"constraint: constraint failed: UNIQUE constraint failed: observations.content_hash (2067)"}}
segunda_captura_exit=6
filas_con_ese_hash=1
```

**Veredicto: SUPERADO.** `E_CONSTRAINT` (exit 6) y una sola fila con ese hash.

## Política: escritura denegada en /meta

```bash
db-ai touch /mem-a/meta --data '{ "id":"01a1f600-2222-7081-9000-000000000003",
  "name":"intruso", "value":"x", "updated_at":"2026-11-19T16:00:00Z" }'
```

Salida observada:

```text
{"version":1,"ok":false,"error":{"code":"E_DENIED","message":"denied: policy does not allow touch on \"meta\" in /mem-a/meta"}}
exit=4
```

**Veredicto: SUPERADO.** `E_DENIED` (exit 4); `/meta` es de solo lectura.

## Política: rm denegado

```bash
db-ai rm /mem-a/observations/01a1f42d-ba80-76a7-8b8c-9d0e1f2a3bac \
  --if-match "$(db-ai cat /mem-a/observations/01a1f42d-ba80-76a7-8b8c-9d0e1f2a3bac | sed -n 's/.*"etag":"\([^"]*\)".*/\1/p')" \
  --apply
```

Salida observada:

```text
{"version":1,"ok":false,"error":{"code":"E_DENIED","message":"denied: policy does not allow rm on \"01a1f42d-ba80-76a7-8b8c-9d0e1f2a3bac\" in /mem-a/observations/01a1f42d-ba80-76a7-8b8c-9d0e1f2a3bac"}}
exit=4
```

**Veredicto: SUPERADO.** La obsolescencia no se expresa borrando; `rm` queda fuera de la
política aunque el ETag sea válido.

## Recuperación de conocimiento pendiente de revisión (needs_review)

```bash
db-ai find /mem-a/observations --where 'status=needs_review' --limit 10
sqlite3 .../memory.db "SELECT status, count(*) FROM observations GROUP BY status ORDER BY status;"
```

Salida observada (recortada):

```text
{"version":1,"ok":true,"data":{"path":"/mem-a/observations","rows":[
 {"path":"/mem-a/observations/01a1f43b-7620-77a8-8b9c-0d1e2f3a4bac",
  "fields":{"kind":{"kind":"text","data":"learning"},
   "title":{"kind":"text","data":"La deduplicación exacta evita reescribir la misma decisión"},
   "status":{"kind":"text","data":"needs_review"},
   "review_after":{"kind":"text","data":"2027-05-19T00:00:00Z"},
   "confidence":{"kind":"text","data":"unknown"}}}]}}

active|4
needs_review|1
obsolete|1
superseded|1
```

**Veredicto: SUPERADO.** Las filas `needs_review` se recuperan con su estado visible, no se
devuelven como activas y no se borran.

## Integridad FK en las dos bases

```bash
for b in .../proyecto-a/.agent-memory/memory.db .../proyecto-b/.agent-memory/memory.db; do
  sqlite3 "$b" 'PRAGMA foreign_key_check;'
  sqlite3 "$b" 'PRAGMA integrity_check;'
done
```

Salida observada:

```text
-- /tmp/opencode/acm-integrity/proyecto-a/.agent-memory/memory.db
foreign_key_check_filas=0
ok
-- /tmp/opencode/acm-integrity/proyecto-b/.agent-memory/memory.db
foreign_key_check_filas=0
ok
```

**Veredicto: SUPERADO.** Cero violaciones de clave foránea y `integrity_check` correcto en
ambas bases tras todas las pruebas.

## Batería de pruebas

```bash
bash tests/run-tests.sh
```

Salida observada:

```text
ok     init crea la base y sale 0
ok     db-ai mounts muestra el alias mem-test
ok     db-ai ls lista las tablas
ok     db-ai find recupera project_key de meta
ok     seed: observations tiene active, superseded y needs_review (active:4,needs_review:1,superseded:1)
ok     seed: una decisión activa por tema y superseded_by apunta a la activa
ok     seed: tres preferencias activas
ok     write con ETag actualiza y la relectura muestra needs_review
ok     write con ETag obsoleto devuelve E_CONFLICT
ok     segunda captura idéntica devuelve E_CONSTRAINT y deja una fila
ok     touch en /meta devuelve E_DENIED
ok     segundo init sale 0, conserva las filas (4/2/7/1/3) y el project_key
ok     tools/content-hash.py reproduce el content_hash del seed (DESIGN §5.2)

13 ok, 0 fallos
```

**Veredicto: SUPERADO.** 13 de 13 casos en verde, exit 0.

## Límites de esta evidencia

- El escenario de integridad usa bases temporales y las deja fuera del repo; al terminar la
  sesión de validación se eliminan. Las rutas de la transcripción son reproducibles con el
  runbook.
- El conflicto de ETag se probó con dos procesos en paralelo (el reparto ok/E_CONFLICT se
  mantiene); el orden de llegada no está garantizado.
- La política de db-ai no es una frontera de seguridad frente al fichero ni frente a
  `config.json`; se validó la denegación de la CLI, no el aislamiento del sistema operativo.
- `db-ai version` no existe: la versión se declara como binario local del repo (C-ACM-01).
