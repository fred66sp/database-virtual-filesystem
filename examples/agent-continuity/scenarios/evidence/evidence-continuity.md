# Evidencia — escenario de continuidad A → B (ACM-05)

- **Fecha:** 2026-11-19 (Europe/Madrid, UTC+01:00).
- **Alias usado:** `mem`.
- **Base:** `examples/agent-continuity/.agent-memory/memory.db`, recreada desde cero (borrado de
  `.agent-memory/` y `bash init.sh`) para partir del seed de demostración.
- **db-ai:** binario local construido del repo (sin comando version, C-ACM-01).
  (`db-ai version` y `db-ai --version` devuelven `E_USAGE` con exit 2; ver «Límites».)
- **Agentes simulados:** A = `agent-alpha` (escribe sus filas); B = `agent-beta`
  (solo lee; no crea filas, por diseño del runbook).
- **Método:** comandos ejecutados desde `examples/agent-continuity/` en un shell por agente.
  La latencia se midió con `date +%s%N` alrededor de cada invocación (incluye arranque del
  proceso); el consumo se midió con `wc -c` sobre el stdout de cada invocación (stderr vacío).
- **Transcripción:** los `--data` largos se abrevian con `…` o `{ ... }` para que se lean;
  los campos omitidos y los valores son los del runbook y los que aparecen en la salida.
  La forma exacta y copiable de cada comando está en `scenario-continuity.md`.

## Preparación: base limpia y montaje rw

```bash
rm -rf .agent-memory
bash init.sh
db-ai mounts
```

Salida observada (recortada):

```text
esquema: aplicado en .../examples/agent-continuity/.agent-memory/memory.db
seed: aplicado (base vacía)
identidad: project_key generado (01a1f49a-b991-70f9-8bfc-257c0bdff401)
montaje: alias 'mem' -> .../examples/agent-continuity/.agent-memory/memory.db (rw, política de DESIGN §6)
verificación: db-ai mounts muestra 'mem'
verificación: db-ai ls /mem responde ok:true
verificación: db-ai cat /mem/meta/01a1f308-c280-7b1c-82d3-e4f5a6b7c8d9 responde ok:true
listo: memoria de ACM en .../examples/agent-continuity/.agent-memory/memory.db montada como mem

{"version":1,"ok":true,"data":{"mounts":[{"alias":"mem","file":".../examples/agent-continuity/.agent-memory/memory.db","mode":"rw"}]},"meta":{"truncated":false,...}}
```

**Veredicto: SUPERADO.** Base nueva con seed y alias `mem` en modo `rw`.

## A abre sesión con agent-alpha

```bash
SESSION=01a1f500-1111-7081-9000-0000000000a1
db-ai touch /mem/sessions --data '{ ... "id":"$SESSION", "agent_id":"agent-alpha",
  "source":"agent", "goal":"Demostrar el flujo A a B", "status":"open",
  "started_at":"2026-11-19T15:00:00Z", ... }'
```

Salida observada (recortada):

```json
{"version":1,"ok":true,"data":{"path":"/mem/sessions/01a1f500-1111-7081-9000-0000000000a1",
 "fields":{"agent_id":{"kind":"text","data":"agent-alpha"},"goal":{"kind":"text","data":"Demostrar el flujo A a B"},
 "status":{"kind":"text","data":"open"}},
 "etag":"sha256:c1595b6f8fe21f3f1617dbb639d88e09cfc2292aa4d05c50f09cfd8b433f143c"}}
```

**Veredicto: SUPERADO.** Sesión creada con `agent_id = agent-alpha` y ETag inicial.

## A descubre antes de escribir

```bash
db-ai describe /mem/observations
db-ai find /mem/preferences --where 'status=active' --limit 20
db-ai find /mem/observations --where 'kind=decision' --where 'status=active' --limit 10
```

Salida observada (recortada):

```text
describe: "table":"observations", 21 columnas, "primary_key":["id"],
          "capabilities":{"readable":true,"writable":true,"navigable":true}

preferences (3 filas):
  "pref_key":"commit/approval"    "value":"Presentar un informe de preparación del commit y esperar confirmación explícita antes de commitear."
  "pref_key":"docs/language"      "value":"Documentos con nombres de fichero en inglés y contenido en español (castellano)."
  "pref_key":"tools/no-playwright" "value":"No usar Playwright en las validaciones; la revisión es manual."

decisiones activas (1 fila):
  "topic_key":{"kind":"text","data":"storage/db-per-project"},"status":{"kind":"text","data":"active"}
  "title":"Una base por proyecto para conservar la continuidad"
```

**Veredicto: SUPERADO.** La tabla es navegable; hay tres preferencias activas y una única
decisión activa (`storage/db-per-project`), sin la sustituida `storage/db-per-session`.

## A captura un hallazgo con hash canónico

```bash
HASH_JSON='["discovery","El alias mem exige política rw explícita","db-ai deniega la escritura sin una entrada allow para el prefijo; mounts confirma el modo antes de mutar.","","","",""]'
HASH="sha256:$(printf '%s' "$HASH_JSON" | sha256sum | cut -d' ' -f1)"
db-ai touch /mem/observations --data '{ ... "id":"01a1f500-1111-7081-9000-0000000000a2",
  "session_id":"$SESSION", "agent_id":"agent-alpha", "kind":"discovery",
  "content_hash":"$HASH" ... }'
```

Salida observada (recortada):

```text
hash_runbook=sha256:a996e22f2d6e6fabfb0c9d6e8e40c7653ccf8bbf05ac35ff39f15cf3eefb4847
hash_tool   =sha256:a996e22f2d6e6fabfb0c9d6e8e40c7653ccf8bbf05ac35ff39f15cf3eefb4847

{"version":1,"ok":true,"data":{"path":"/mem/observations/01a1f500-1111-7081-9000-0000000000a2",
 "fields":{"agent_id":{"kind":"text","data":"agent-alpha"},"kind":{"kind":"text","data":"discovery"},
 "session_id":{"kind":"text","data":"01a1f500-1111-7081-9000-0000000000a1"},
 "content_hash":{"kind":"text","data":"sha256:a996e22f..."}},
 "etag":"sha256:8843dd87ae665d331ce14f31cb97453bfa8aebdca073957cae63f1dec1ef3190"}}
```

**Veredicto: SUPERADO.** Captura creada por `agent-alpha`; el hash del runbook coincide con
`tools/content-hash.py` (DESIGN §5.2), así que el runbook no está desviado.

## A deja checkpoint de reanudación

```bash
db-ai touch /mem/checkpoints --data '{
  "id":"01a1f500-1111-7081-9000-0000000000a3", "session_id":"$SESSION",
  "agent_id":"agent-alpha", "goal":"Demostrar el flujo A a B",
  "state":"Sesión abierta y observación capturada.",
  "next_steps":"Reanudar en una sesión B y verificar la recuperación.",
  "files":"[\"examples/agent-continuity/init.sh\"]", "open_questions":"[]",
  "content_hash":"sha256:0fc3735a2c3f4a2a11094410027bd2781ded0d60d7c2480b680939c1d8e8c1a1" }'
```

Salida observada (recortada):

```json
{"version":1,"ok":true,"data":{"path":"/mem/checkpoints/01a1f500-1111-7081-9000-0000000000a3",
 "fields":{"agent_id":{"kind":"text","data":"agent-alpha"},
 "goal":{"kind":"text","data":"Demostrar el flujo A a B"},
 "state":{"kind":"text","data":"Sesión abierta y observación capturada."},
 "next_steps":{"kind":"text","data":"Reanudar en una sesión B y verificar la recuperación."},
 "files":{"kind":"text","data":"[\"examples/agent-continuity/init.sh\"]"},
 "status":{"kind":"text","data":"active"}},
 "etag":"sha256:b838d5f84c700b6b368e82241f73337ab60bf85740ba569b28473e521e8a63ce"}}
```

El `content_hash` se calculó con `tools/content-hash.py checkpoints`
(`sha256:0fc3735a2c3f4a2a11094410027bd2781ded0d60d7c2480b680939c1d8e8c1a1`).

**Veredicto: SUPERADO.** Checkpoint activo con próximo paso explícito y ficheros.

## A cierra la sesión y un write con el ETag anterior da E_CONFLICT

```bash
ROW=/mem/sessions/$SESSION
ETAG=$(db-ai cat "$ROW" | sed -n 's/.*"etag":"\([^"]*\)".*/\1/p')
db-ai write "$ROW" --data '{"status":...,"ended_at":...,"summary":...}' --if-match "$ETAG"
db-ai write "$ROW" --data '{"summary":{"kind":"text","data":"reintento"}}' --if-match "$ETAG"
```

Salida observada (recortada):

```text
etag_leido=sha256:c1595b6f8fe21f3f1617dbb639d88e09cfc2292aa4d05c50f09cfd8b433f143c
{"ok":true,"data":{"fields":{"agent_id":"agent-alpha","ended_at":{"kind":"text","data":"2026-11-19T15:10:00Z"},
 "status":{"kind":"text","data":"closed"},
 "summary":{"kind":"text","data":"Flujo A completado; reanudación pendiente en B."}},
 "etag":"sha256:9158de8b0e414233eb144a20bbe696c2e009d56834c336d5bd1e32e2d949d761"}}

{"version":1,"ok":false,"error":{"code":"E_CONFLICT","message":"conflict: ETag \"sha256:c1595b6f...\" no longer matches table \"sessions\""}}
exit=5
```

**Veredicto: SUPERADO.** Cierre con ETag vigente; reintento con ETag obsoleto rechazado
(`E_CONFLICT`, exit 5) sin actualización perdida.

## B reanuda sin la conversación: identidad y comandos

B se ejecutó en un shell nuevo con la identidad declarada `agent-beta`; su único insumo
fueron las salidas de db-ai de la tabla siguiente. No recibió texto de la sesión de A.

```bash
export AGENT=agent-beta   # B no escribe filas
db-ai find /mem/sessions --where 'status=open' --limit 10
db-ai find /mem/checkpoints --where "session_id=$SESSION" --where 'status=active' --limit 5
db-ai find /mem/observations --where "session_id=$SESSION" --limit 50
db-ai find /mem/observations --where 'kind=decision' --where 'status=active' --limit 10
db-ai find /mem/preferences --where 'status=active' --limit 20
db-ai cat /mem/observations/01a1f500-1111-7081-9000-0000000000a2   # detalle, opcional
```

### 1. Sesiones abiertas: la de A no aparece

```text
{"ok":true,"data":{"path":"/mem/sessions","rows":[
 {"path":"/mem/sessions/01a1f41f-fee0-71a2-8b3c-4d5e6f7a8b9c",
  "fields":{"status":{"kind":"text","data":"open"},"goal":{"kind":"text","data":"Reanudar ACM-03 y validar la continuidad entre agentes"}}}]}}
```

`01a1f500-1111-7081-9000-0000000000a1` (sesión de A) no está en `rows`: quedó cerrada.

**Veredicto: SUPERADO.** Solo la sesión abierta del seed.

### 2. Checkpoint activo de A

```text
{"ok":true,"data":{"path":"/mem/checkpoints","rows":[
 {"path":"/mem/checkpoints/01a1f500-1111-7081-9000-0000000000a3",
  "fields":{"agent_id":{"kind":"text","data":"agent-alpha"},
   "goal":{"kind":"text","data":"Demostrar el flujo A a B"},
   "state":{"kind":"text","data":"Sesión abierta y observación capturada."},
   "next_steps":{"kind":"text","data":"Reanudar en una sesión B y verificar la recuperación."},
   "files":{"kind":"text","data":"[\"examples/agent-continuity/init.sh\"]"},
   "status":{"kind":"text","data":"active"}}}]}}
```

**Veredicto: SUPERADO.** Objetivo, estado, próximos pasos y ficheros recuperados.

### 3. Historial de la sesión de A

```text
{"ok":true,"data":{"path":"/mem/observations","rows":[
 {"path":"/mem/observations/01a1f500-1111-7081-9000-0000000000a2",
  "fields":{"agent_id":{"kind":"text","data":"agent-alpha"},
   "kind":{"kind":"text","data":"discovery"},
   "title":{"kind":"text","data":"El alias mem exige política rw explícita"},
   "body":{"kind":"text","data":"db-ai deniega la escritura sin una entrada allow para el prefijo; mounts confirma el modo antes de mutar."}}}]}}
```

**Veredicto: SUPERADO.** El hallazgo de A llega a B sin la conversación.

### 4. Decisiones activas: una sola, la vigente

```text
{"ok":true,"data":{"path":"/mem/observations","rows":[
 {"fields":{"topic_key":{"kind":"text","data":"storage/db-per-project"},
  "status":{"kind":"text","data":"active"},
  "title":{"kind":"text","data":"Una base por proyecto para conservar la continuidad"}}}]}}
```

**Veredicto: SUPERADO.** Una única decisión activa; la sustituida no se devuelve.

### 5. Preferencias activas: las tres

```text
rows[0]: "pref_key":"commit/approval"     "value":"Presentar un informe de preparación del commit y esperar confirmación explícita antes de commitear."
rows[1]: "pref_key":"docs/language"       "value":"Documentos con nombres de fichero en inglés y contenido en español (castellano)."
rows[2]: "pref_key":"tools/no-playwright" "value":"No usar Playwright en las validaciones; la revisión es manual."
```

**Veredicto: SUPERADO.** Tres preferencias activas con su valor.

### 6. Detalle de una fila por su ruta

```text
{"ok":true,"data":{"path":"/mem/observations/01a1f500-1111-7081-9000-0000000000a2","fields":{...}}}
```

**Veredicto: SUPERADO (redundante).** `find` ya devuelve la fila completa; esta lectura no
aporta datos nuevos y por eso se cuenta aparte en las métricas.

## B declara el siguiente paso y coincide con el checkpoint de A

```bash
NEXT=$(db-ai find /mem/checkpoints --where "session_id=$SESSION" --where 'status=active' --limit 5 \
       | sed -n 's/.*"next_steps":{"kind":"text","data":"\([^"]*\)".*/\1/p')
DECLARADO="Reanudar en una sesión B y verificar la recuperación."
[ "$NEXT" = "$DECLARADO" ] && echo "coincide=SI"
```

Salida observada:

```text
next_steps_del_checkpoint=Reanudar en una sesión B y verificar la recuperación.
siguiente_paso_declarado_por_B=Reanudar en una sesión B y verificar la recuperación.
coincide=SI
```

**Veredicto: SUPERADO.** B continúa con el paso que dejó A.

## B no crea filas

```bash
sqlite3 .agent-memory/memory.db "SELECT (SELECT count(*) FROM sessions)||'/'||
  (SELECT count(*) FROM observations)||'/'||(SELECT count(*) FROM checkpoints)||'/'||
  (SELECT count(*) FROM preferences);"
```

Salida observada:

```text
counts_antes=3/7/2/3
counts_despues=3/7/2/3
agent_ids en la base=agent-alpha,claude-code,opencode
```

**Veredicto: SUPERADO.** La recuperación de B no altera la memoria; `agent-beta` no aparece
en ninguna fila porque no escribió.

## Integridad de la base de continuidad

```bash
sqlite3 .agent-memory/memory.db 'PRAGMA foreign_key_check;'
sqlite3 .agent-memory/memory.db 'PRAGMA integrity_check;'
```

Salida observada:

```text
foreign_key_check_filas=0
ok
```

**Veredicto: SUPERADO.** Sin violaciones de clave foránea ni corrupción.

## Métricas de la recuperación de B (M-ACM-01 y M-ACM-03)

Cada fila es una invocación real de B; el orden es el de la tabla anterior.

| # | Invocación | Latencia (ms) | stdout (bytes) |
|---|---|---|---|
| 1 | `find /mem/sessions --where status=open` | 9 | 822 |
| 2 | `find /mem/checkpoints --where session_id=… --where status=active` | 9 | 1 187 |
| 3 | `find /mem/observations --where session_id=…` | 10 | 1 496 |
| 4 | `find /mem/observations --where kind=decision --where status=active` | 11 | 1 772 |
| 5 | `find /mem/preferences --where status=active` | 10 | 3 274 |
| 6 | `cat /mem/observations/…a2` (detalle opcional y redundante) | 10 | 1 458 |
| | **Total** | **59** | **10 009** |

- **Invocaciones:** 6 (5 `find` + 1 `cat`). Sin el `cat` redundante: 5. Conjunto mínimo
  checkpoint + historial + decisiones + preferencias: 4.
- **Latencia:** 59 ms sumando los tiempos de pared individuales; 9–11 ms por invocación.
- **Contexto:** 10 009 bytes de stdout. Estimación declarada **bytes/4 ≈ 2 503 tokens**.
  Sin el `cat`: 8 551 B ≈ 2 138 tokens. Conjunto mínimo: 7 729 B ≈ 1 933 tokens.
  (Reproducido por el orquestador tras la validación: 7 729 B exactos con el
  mismo conjunto de 4 `find`.)

  **Decisión del PO (2026-11-19):** se mantiene el objetivo de 2 000 tokens y la
  convención de recuperación de la skill adopta este conjunto mínimo como
  protocolo (skill/references/protocolos.md §3.6).
  Es una estimación, no un conteo de tokens del modelo.

**Veredicto M-ACM-01: SUPERADO.** 6 invocaciones ≤ 6 (objetivo), muy por debajo del umbral
de fracaso (>12). Sin el detalle redundante son 5.

**Veredicto M-ACM-03: FALLIDO contra el objetivo.** 2 503 tokens estimados > 2 000, aunque
lejos del umbral de fracaso (>6 000). El conjunto mínimo (1 933) sí cabe: el exceso viene de
que `find` devuelve filas completas y de la lectura de detalle redundante. Un agente que
recupere solo lo necesario cumple; el runbook completo no.

**M-ACM-04 (dato observado):** la captura de A fue 1 invocación de escritura, sin
verificación posterior; 1 ≤ 3.

## Límites de esta evidencia

- B es una simulación por comandos: no hay un LLM distinto al otro lado. Lo verificado es
  que el estado de A es recuperable solo con db-ai y que no hizo falta nada más.
- `db-ai mounts` no se contó como invocación de recuperación porque el runbook parte del
  alias ya conocido; un agente que deba descubrirlo sumaría 1 (7 en total, incumpliendo
  M-ACM-01).
- M-ACM-02 (p95 con 10 000 observaciones y 100 repeticiones) y M-ACM-05 (5 preguntas con
  evaluador humano) no se midieron en esta ejecución.
- La latencia incluye el arranque del proceso db-ai; es un CLI local, no una API.
- `db-ai version` no existe: la versión se declara como binario local del repo (C-ACM-01).
