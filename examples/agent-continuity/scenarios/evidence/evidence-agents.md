# Evidencia — reanudación con dos agentes reales (M-ACM-05)

- **Fecha:** 2026-11-19 (Europe/Madrid, UTC+01:00). Ejecución: 20:41–20:49 UTC.
- **Qué se prueba:** M-ACM-05 del PRD §7 — cinco preguntas de reanudación
  respondidas con **solo la memoria**, sin conversación compartida ni Git.
  Objetivo 5 de 5; fracaso por debajo de 4 de 5.
- **Guion:** [`scenarios/scenario-agents.md`](../scenario-agents.md). Este
  fichero es la evidencia de una ejecución, no el runbook.
- **Ejecutor:** OpenCode 2.3.2 con la CLI `opencode run --standalone --agent
  general`, **dos procesos y dos sesiones separadas**. El runtime de esta sesión
  no expone la herramienta `subagent`; se usó el agente `general` por CLI, que
  crea una sesión nueva sin historial.
- **Modelo de A y de B:** `nan/deepseek-v4-flash#high`, el valor por defecto del
  agente `general` en la configuración de OpenCode. La exportación de sesión de
  `opencode session export` no registra el modelo por sesión: el valor es el
  configurado, no una lectura del metadato de ejecución. Son dos procesos y dos
  sesiones independientes.
  Sesiones de OpenCode: A `ses_e0a737b74ffe3uvqQuNnkjKMbC`, B
  `ses_e0a70caccffej97nqMxzxTbaKO`.
- **db-ai:** construido del árbol (`go build -o /tmp/opencode/db-ai ./cmd/db-ai`).
  `db-ai version` → `{"version":1,"ok":true,"data":{"version":"0.2.0","commit":"unknown","state":"dev"}}`.
  El `db-ai` instalado en el sistema no tiene `version`; el del árbol sí
  (C-ACM-01 resuelto en el árbol).
- **Base:** `/tmp/opencode/acm-home/.agent-memory/memory.db`, creada limpia con
  `init.sh` (4 filas de `meta`, 0 de contenido) y configuración aislada en
  `/tmp/opencode/acm-xdg`. `sha256` del fichero final:
  `780bf1989ded8b2cbf0dfd0c2c405306221739ab528531e56525134e29a5966b`;
  `PRAGMA integrity_check` → `ok`.
- **Proyecto sandbox:** `/tmp/opencode/sandbox-csv` (validador CSV en Python,
  fixture con BOM UTF-8, CRLF y un campo entrecomillado con salto de línea).
- **Independencia:** A y B son procesos distintos; el prompt de B no contiene
  ningún id, texto ni resumen de A; B no leyó el sandbox hasta después de
  responder (verificado en la transcripción, más abajo).

## Diseño de la prueba

1. **A** recibe un encargo de ingeniería real y acotado en el sandbox:
   implementar `validate.py` y sus pruebas sin el flag `--strict`. Durante el
   trabajo debe capturar en la memoria ACM: sesión propia, 1 `discovery`, 1
   `decision` con `topic_key` y `rationale`, 1 preferencia con `pref_key` y
   `rationale`, 1 checkpoint con el siguiente paso y cierre de sesión.
2. **B** arranca sin contexto. Se le pide recuperar la continuidad con la skill
   y responder, **antes de leer el sandbox**, a estas cinco preguntas literales:

   1. ¿Cuál era el objetivo del trabajo?
   2. ¿Qué decisiones están vigentes y por qué?
   3. ¿Cuál es el siguiente paso pendiente?
   4. ¿Qué preferencias operativas rigen?
   5. ¿Qué descubrimientos previos no debes repetir?

   Después debía abrir su propia sesión y continuar desde el checkpoint.
3. El evaluador contrasta las respuestas con la base real (`sqlite3`) y con el
   sandbox, y cuenta las correctas.

La prueba mide comportamiento, no motor: las garantías deterministas se separan
al final.

## Agente A: invocaciones literales

14 invocaciones de `db-ai`, todas con salida 0. Registradas por un envoltorio de
`db-ai` que anota argv, salida y código de salida. Los `--data` largos se
abrevian con `{...}`; forma literal de los cimientos en
[`scenario-agents.md`](../scenario-agents.md).

```text
01  20:43:18  db-ai mounts
02  20:43:43  db-ai find /mem/meta --where name=project_key --limit 2
03  20:43:49  db-ai touch /mem/sessions --data '{id, agent_id=agent-a-csv, source=agent,
                goal="Implementar validate.py y sus pruebas para data/contacts.csv
                segun README, sin el flag --strict", status=open, started_at,
                created_at, updated_at=2026-11-19T20:43:43Z}'
04  20:45:16  db-ai touch /mem/observations --data -        # discovery
05  20:45:16  db-ai touch /mem/observations --data -        # decision
06  20:45:17  db-ai touch /mem/preferences --data -
07  20:45:17  db-ai touch /mem/checkpoints --data -
08  20:45:20  db-ai cat /mem/observations/01a1f58d-09b5-7000-8029-f888a3470f94
09  20:45:20  db-ai cat /mem/observations/01a1f58d-09b5-7000-805e-dfbfd732e217
10  20:45:20  db-ai cat /mem/preferences/01a1f58d-09b5-7000-80cf-15b217d5c511
11  20:45:20  db-ai cat /mem/checkpoints/01a1f58d-09b5-7000-8055-5ba00dd6f5b8
12  20:45:26  db-ai cat /mem/sessions/01a1f58d-09b4-7000-802b-376f14e1c55e
13  20:45:26  db-ai write /mem/sessions/01a1f58d-09b4-7000-802b-376f14e1c55e
                --data - --if-match sha256:d4137682…573de3b5
14  20:45:31  db-ai cat /mem/sessions/01a1f58d-09b4-7000-802b-376f14e1c55e
```

**Resultado técnico de A:** `validate.py` con cabecera exacta, campos no vacíos,
forma de email, duplicados, línea física y exit 0/1; `tests/test_validate.py`
con 9 pruebas en verde; exit 1 sobre el fichero real (duplicado en línea 4,
fila vacía en línea 5). Sin `--strict`, como pedía el encargo.

**Filas que A dejó en la base** (textos de `sqlite3`, no del informe de A):

| Tabla | id | Contenido |
|---|---|---|
| `sessions` | `01a1f58d-09b4-7000-802b-376f14e1c55e` | `agent-a-csv`, `closed`, goal del encargo, `20:43:43Z`–`20:45:26Z` |
| `observations` | `01a1f58d-09b5-7000-8029-f888a3470f94` | `discovery` / «contacts.csv trae BOM UTF-8 y CRLF: sin utf-8-sig la cabecera exacta email,name falla» |
| `observations` | `01a1f58d-09b5-7000-805e-dfbfd732e217` | `decision`, `topic_key=csv/numeracion-lineas` / «Numerar cada registro CSV por su primera línea física, no por la última de csv.reader» |
| `preferences` | `01a1f58d-09b5-7000-80cf-15b217d5c511` | `testing/unittest-biblioteca-estandar`: unittest de la biblioteca estándar, cero dependencias |
| `checkpoints` | `01a1f58d-09b5-7000-8055-5ba00dd6f5b8` | `active`; `next_steps` = implementar `--strict` y añadir pruebas |

El `next_steps` literal del checkpoint:

> 1. Implementar el flag --strict según el README: rechazar nombres con espacios
> al principio o al final y emitir {"rows":N,"errors":M} en stdout cuando el
> fichero sea válido. 2. Añadir pruebas del flag en tests/.

Pregunta abierta del checkpoint: si los emails duplicados deben compararse sin
distinguir mayúsculas.

**Anomalía de captura de A (hecho):** las filas de `observations`,
`preferences` y `checkpoints` llevan `created_at=2026-11-19T20:50:00Z`, posterior
al cierre de la propia sesión de A (`20:45:26Z`) y al reloj real de la
ejecución (`20:43:18Z`–`20:45:31Z`). El motor aceptó esas marcas: son un dato
del productor y no hay `CHECK` que las contraste. B lo detectó y lo declaró así
en su informe final:

> Reloj: las filas sembradas por el agente A tienen marcas
> `2026-11-19T20:50:00Z`, posteriores al reloj del sistema (20:47–20:49). La
> recencia por UUIDv7 es coherente (la mía es mayor), pero las marcas no lo son;
> no lo toqué.

No se corrigió en vuelo.

## Agente B: invocaciones literales

Sesión nueva. **Fase 1, recuperación**, 14 invocaciones en este orden (las
declaró B y coinciden con el registro del envoltorio):

```text
01  db-ai mounts
02  db-ai ls /mem
03  db-ai find /mem/meta --where 'name=project_key' --limit 2
04  db-ai find /mem/sessions --where 'started_at>=2026-11-05T00:00:00Z' --limit 50
05  db-ai find /mem/preferences --where 'status=active' --limit 3
06  db-ai find /mem/checkpoints --where 'session_id=01a1f58d-09b4-7000-802b-376f14e1c55e' \
        --where 'status=active' --limit 5
07  db-ai find /mem/observations --where 'kind=decision' --where 'status=active' --limit 3
08  db-ai find /mem/observations --where 'kind=discovery' --where 'status=active' --limit 5
09  db-ai find /mem/observations --where 'kind=learning' --where 'status=active' --limit 5
10  db-ai find /mem/observations --where 'status=needs_review' --limit 5
11  db-ai find /mem/observations --where 'kind=bugfix' --where 'status=active' --limit 5
12  db-ai find /mem/observations --where 'kind=pattern' --where 'status=active' --limit 5
13  db-ai grep /mem/preferences strict --limit 5
14  db-ai grep /mem/observations strict --limit 5
```

B no conocía el `topic_key`, así que enumeró por `kind` en vez de filtrar por
tema: **14 invocaciones frente al presupuesto de ~6 de `protocolos.md` §3.6**
(M-ACM-01). B lo declaró por su cuenta en su respuesta. Ninguna invocación
exportó tablas.

**Fase 2:** 16 invocaciones más (30 en total, 27 con salida 0 y 3 `E_USAGE`
propios, recuperados sin pérdida de datos). Abrió su sesión, implementó
`--strict`, añadió 4 pruebas, actualizó el README, capturó 2 observaciones y 1
checkpoint, y cerró su sesión. El detalle:

```text
15–18  db-ai describe /mem/{sessions,observations,checkpoints,preferences}
19     db-ai touch /mem/sessions --data -        → E_USAGE (stdin vacío)
20     db-ai touch /mem/sessions --data - < session.json
21–22  db-ai touch /mem/observations --data - < obs{0,1}.json
       → E_USAGE ×2: duplicate_count debe ser cadena en el envoltorio JSON
23     db-ai touch /mem/checkpoints --data - < ckpt.json
24–25  db-ai touch /mem/observations --data - < obs{0,1}.json   (reintento corregido)
26     db-ai cat /mem/sessions/01a1f590-5047-765c-80b1-52e473f6f93f
27     db-ai write /mem/sessions/01a1f590-5047-765c-80b1-52e473f6f93f \
       --data - --if-match sha256:c6531d31…a589237f
28     db-ai find /mem/checkpoints --where 'session_id=01a1f590-…' --where 'status=active' --limit 5
29     db-ai find /mem/observations --where 'topic_key=csv/flag-strict' --where 'status=active' --limit 5
30     db-ai cat /mem/sessions/01a1f590-5047-765c-80b1-52e473f6f93f/status
```

Los 3 errores son de construcción de comandos por B, no del motor: `E_USAGE` es
la respuesta determinista a una forma inválida y B releyó antes de reintentar.

**Continuidad del proyecto tras B:** `validate.py` conserva `utf-8-sig` y
`newline=""` (no repitió el hallazgo de A); `python3 -m unittest discover -s
tests` → **13 pruebas OK** (verificado por el evaluador, no solo por B). El
fichero real sigue dando exit 1 por duplicado (línea 4) y fila vacía (línea 5).
B añadió filas y no modificó las de A: el diff de ids entre la instantánea
posterior a A y la base final solo muestra altas.

## Respuestas literales de B

Las siguientes son la respuesta de la **fase 1**, emitida antes de leer
`/tmp/opencode/sandbox-csv` (el registro de herramientas muestra las lecturas
del sandbox solo después de este bloque):

> ## Fase 1 — Respuesta desde la memoria (antes de tocar el sandbox)
>
> **1. ¿Cuál era el objetivo del trabajo?**
> Implementar `validate.py` y sus pruebas para `data/contacts.csv` según el
> README, **sin el flag `--strict`**. Consta literalmente en la sesión
> `01a1f58d-09b4-7000-802b-376f14e1c55e` (`/mem/sessions`, campo `goal`) y, sin
> la coletilla, en el checkpoint
> `01a1f58d-09b5-7000-8055-5ba00dd6f5b8` (`/mem/checkpoints`, `goal`).
>
> **2. ¿Qué decisiones están vigentes y por qué?**
> - Numerar cada registro CSV por su **primera** línea física, no por la última
>   que devuelve `csv.reader`; se guarda el inicio del registro antes de leer el
>   siguiente. Motivo: el README pide la línea física y señalar la línea de
>   cierre de un registro multilínea apunta a la comilla final, no a la fila con
>   el problema. Fila `01a1f58d-09b5-7000-805e-dfbfd732e217`,
>   `/mem/observations`, `kind=decision`, `topic_key=csv/numeracion-lineas`,
>   `status=active`.
> - No hay más decisiones activas en memoria: la consulta por
>   `kind=decision, status=active` devolvió una sola fila sin `truncated`.
>
> **3. ¿Cuál es el siguiente paso pendiente?**
> Checkpoint `01a1f58d-09b5-7000-8055-5ba00dd6f5b8` (`next_steps`), literal:
> «1. Implementar el flag `--strict` según el README: rechazar nombres con
> espacios al principio o al final y emitir `{"rows":N,"errors":M}` en stdout
> cuando el fichero sea válido. 2. Añadir pruebas del flag en `tests/`.» Queda
> además una pregunta abierta en ese checkpoint: si los emails duplicados deben
> compararse sin distinguir mayúsculas; el README no lo especifica y hoy la
> comparación es exacta.
>
> **4. ¿Qué preferencias operativas rigen?**
> Las pruebas se escriben con `unittest` de la biblioteca estándar y se ejecutan
> con `python3 -m unittest discover -s tests`; motivo: el README exige cero
> dependencias. Fila `01a1f58d-09b5-7000-80cf-15b217d5c511`, `/mem/preferences`,
> `pref_key=testing/unittest-biblioteca-estandar`, `status=active`. Es la única
> preferencia activa que devolvió la página (sin `truncated`); no hay
> preferencia registrada sobre `--strict`.
>
> **5. ¿Qué descubrimientos previos no debes repetir?**
> `contacts.csv` trae BOM UTF-8 (`EF BB BF`) y CRLF: sin `utf-8-sig` la cabecera
> exacta `email,name` falla, y un `split(',')` por líneas rompe el campo
> entrecomillado con salto de línea (líneas 6-7) en dos filas falsas; verificado
> con `xxd`, volcado de `csv.reader` y las pruebas `test_fichero_real` y
> `test_linea_fisica_tras_registro_multilinea`. Fila
> `01a1f58d-09b5-7000-8029-f888a3470f94`, `/mem/observations`,
> `kind=discovery`, `confidence=confirmed`. No hay filas `learning`, `bugfix` ni
> `pattern`, ni nada en `needs_review`.
>
> **Contradicción declarada:** el `goal` de la sesión excluye `--strict`, pero el
> `next_steps` del checkpoint pide implementarlo. La memoria no contiene ninguna
> decisión ni preferencia que resuelva esa contradicción; la trataré como el
> trabajo pendiente (el checkpoint es más reciente y concreto) y lo reportaré al
> final.
>
> [… invocaciones 1–14, reproducidas arriba …]
>
> (Nota final de B, tras esa lista: «Me excedí del presupuesto de ~6
> invocaciones del §3.6: las 8-14 son comprobaciones acotadas por `kind`/texto
> para responder la pregunta 5 y la cola `needs_review`; ninguna exporta
> tablas.»)

## Evaluación pregunta por pregunta

| # | Pregunta | Veredicto | Evidencia |
|---|---|---|---|
| 1 | Objetivo | **Correcta** | `sessions.goal` de A, literal: «Implementar validate.py y sus pruebas para data/contacts.csv segun README, sin el flag --strict». B lo cita con el id correcto. |
| 2 | Decisiones vigentes y por qué | **Correcta** | Única `decision` activa en la instantánea post-A: `csv/numeracion-lineas`; B reproduce el motivo (línea física de inicio, no `reader.line_num`) y la alternativa descartada. |
| 3 | Siguiente paso | **Correcta** | `checkpoints.next_steps` de A, literal; B añade la pregunta abierta del mismo checkpoint. |
| 4 | Preferencias operativas | **Correcta** | `preferences` activa `testing/unittest-biblioteca-estandar`; B reproduce valor y motivo (cero dependencias). |
| 5 | Descubrimientos que no repetir | **Correcta** | `discovery` de A: BOM `EF BB BF`, CRLF, cabecera con `\ufeff`, campo entrecomillado multilínea y las dos pruebas que lo cubren. |

**M-ACM-05 = 5 de 5.** Umbral de fracaso (<4 de 5) no alcanzado.

Matices de la evaluación:

- **El evaluador es un agente, no una persona.** El PRD pide «evaluador humano
  con guion». Esta ejecución cumple el guion y el contraste con la base, pero la
  ratificación humana queda pendiente. El 5 de 5 es del evaluador delegado.
- La pregunta 2 se respondió con una consulta de **todas** las decisiones
  activas (`kind=decision`, `status=active`, límite 3), no filtrada por
  `topic_key` como pide `protocolos.md` §1.5. Con una sola decisión el resultado
  es correcto, pero el guion no se siguió al pie de la letra y con más filas
  habría sido una página truncada.
- B declaró la contradicción entre el `goal` de la sesión de A («sin --strict»)
  y el `next_steps` del checkpoint («implementar --strict»), eligió el
  checkpoint y la dejó como pregunta abierta en su propio checkpoint. Es una
  decisión razonable, pero la ambigüedad la introdujo la captura de A.

## Cumplimiento del protocolo por B

| Comprobación | Resultado | Evidencia |
|---|---|---|
| ¿Comprobó el montaje y el `project_key`? | Sí | `mounts`, `ls /mem` y `find /mem/meta` antes de escribir. |
| ¿Abrió su propia sesión antes de escribir? | Sí | `01a1f590-5047-765c-80b1-52e473f6f93f`, `agent_id=agent-b-csv`, `closed`. |
| ¿Usó el checkpoint vigente? | Sí | `find` por `session_id` y `status=active`; `next_steps` citado. |
| ¿Respondió antes de leer el proyecto? | Sí | En la transcripción, el bloque de respuestas precede a `ls`/`read` del sandbox. |
| ¿Evitó Git y `sqlite3`? | Sí | Sin comandos `git`; toda la memoria se leyó con `db-ai`. |
| ¿Respetó el presupuesto de recuperación? | **No** | 14 invocaciones de fase 1 frente a ~6; B lo declaró. |
| ¿Repitió el descubrimiento de A? | No | Conservó `utf-8-sig`; sus filas nuevas no duplican el hallazgo. |
| ¿Dejó el checkpoint de A intacto? | Sí | Sigue `active`, mismo `agent_id` y `etag`; B creó uno propio. |
| ¿Resolvió sus errores sin corromper datos? | Sí | 3 `E_USAGE`, relee y corrige; ninguna escritura duplicada. |

## Garantías deterministas vs comportamiento observado

Esta separación es la que la evidencia puede afirmar sin mezclar capas.

### Lo que garantiza el motor (db-ai + esquema)

| Garantía | Dónde | ¿Se ejerció aquí? |
|---|---|---|
| `UNIQUE(content_hash)` impide el duplicado exacto | `schema.sql` | No se provocó; cubierto por `tests/run-tests.sh`. |
| Clave foránea `session_id` exige sesión previa | `schema.sql` | Sí de forma indirecta: B abrió sesión antes de capturar. |
| `ETag` + `--if-match` serializa la escritura y da `E_CONFLICT` | db-ai | Sí en el cierre de ambas sesiones con el ETag leído; el conflicto no se provocó. |
| `CHECK` de decisión exige `topic_key` y `rationale` | `schema.sql` | Sí: las decisiones de A y B los llevan. |
| Transacción y reversión total en `batch` | db-ai | No se usó `batch`. |
| Política de prefijos: solo `touch`/`write` en las cuatro tablas | `config.json` | Parcial: no se intentó una escritura denegada; todas cayeron dentro de la política. |
| Formato `sha256:` del hash, no su algoritmo | `CHECK` | Sí: el hash lo calculó el productor con `tools/content-hash.py`. |

### Lo que el motor no garantiza (y se observó)

| Límite | Observado |
|---|---|
| No valida marcas temporales ni el orden lógico entre filas | A escribió filas con `20:50:00Z` después de cerrar su sesión a `20:45:26Z`. El motor las aceptó. |
| No hay FTS ni ranking; `find` devuelve filas enteras y ordena por PK | B enumeró por `kind` y usó `grep` para responder la pregunta 5, y gastó 14 invocaciones. |
| No impone presupuesto de recuperación ni disciplina de captura | Nada impidió las 14 invocaciones ni las marcas de tiempo incoherentes de A. |

### Lo que solo es convención (skill) y se observó

| Convención | Observado |
|---|---|
| Capturar conocimiento durable, no actividad | A capturó 2 observaciones, 1 preferencia y 1 checkpoint; no transcribió la conversación. |
| Un checkpoint activo por sesión y siguiente paso identificable | A y B dejan uno cada uno, con `next_steps` concreto. |
| Abrir sesión antes de escribir | A y B lo hicieron. |
| Cerrar con `status`, `ended_at`, `summary` y ETag vigente | Ambas sesiones cerradas; sus resúmenes coinciden con la base. |
| Recuperación acotada a ~6 invocaciones | B usó 14; la skill lo advierte, el motor no lo impide. |
| Recencia por UUIDv7, no por marca temporal | B razonó la recencia por UUIDv7, detectó que las marcas de A no cuadraban y no las usó para ordenar. |
| No fusionar ni borrar filas dudosas | B dejó la duda de mayúsculas como pregunta abierta; no tocó filas de A. |
| Declarar conflictos en vez de resolverlos en silencio | B detectó la contradicción `goal`/checkpoint, eligió el checkpoint y la dejó abierta con confirmación humana pendiente. |

**Conclusión de la separación:** el motor mantuvo las garantías que se
ejercieron (identidad de fila, FK de sesión, ETag de escritura, `CHECK` de
decisión y formato del hash); las de permiso denegado y `batch` no se
ejercieron aquí. Y **no** garantizó lo que no promete: coherencia temporal del
productor, presupuesto de recuperación ni disciplina de captura. Lo que falló en
esta ejecución pertenece a la capa de convención; lo que funcionó en la
reanudación se apoya en filas que el motor sí hizo consistentes.

## Comprobaciones solicitadas

| # | Comprobación | Resultado observado |
|---|---|---|
| 1 | El guion de 5 preguntas existe y coincide | `scenarios/scenario-agents.md` contiene las 5 preguntas literales; son las mismas del prompt de B y de esta evidencia. |
| 2 | Invocaciones literales de A y B y respuestas de B | 14 de A y 30 de B (14 de recuperación) transcritas arriba; respuesta literal de B antes de leer el sandbox. |
| 3 | Evaluación por pregunta y M-ACM-05 | 5 correctas de 5, con evidencia por fila; matiz de evaluador no humano declarado. |
| 4 | Separación determinista vs observado | Sección propia, con lo ejercido y lo no ejercido. |
| 5 | La base queda en el estado descrito | `sqlite3` sobre `/tmp/opencode/acm-home/.agent-memory/memory.db`: 2 sesiones, 4 observaciones, 2 checkpoints, 1 preferencia; `integrity_check=ok`; hash arriba. |
| 6 | `bash tests/run-tests.sh` sigue en verde | Ejecutado desde `examples/agent-continuity/`: `20 ok, 0 fallos`. |

Comandos de verificación de la base:

```bash
DB=/tmp/opencode/acm-home/.agent-memory/memory.db
sqlite3 "$DB" "select 'sesiones',count(*) from sessions
 union all select 'observaciones',count(*) from observations
 union all select 'checkpoints',count(*) from checkpoints
 union all select 'preferencias',count(*) from preferences;"
sqlite3 "$DB" "select id,agent_id,status from sessions;"
sqlite3 "$DB" "select kind,topic_key,status,title from observations;"
sqlite3 "$DB" "select pref_key,status from preferences;"
sqlite3 "$DB" "select agent_id,status,next_steps from checkpoints;"
```

Salida observada (los `next_steps` largos, recortados con `…`):

```text
sesiones|2
observaciones|4
checkpoints|2
preferencias|1
01a1f58d-09b4-7000-802b-376f14e1c55e|agent-a-csv|closed
01a1f590-5047-765c-80b1-52e473f6f93f|agent-b-csv|closed
discovery||active|contacts.csv trae BOM UTF-8 y CRLF: sin utf-8-sig la cabecera exacta email,name falla
decision|csv/numeracion-lineas|active|Numerar cada registro CSV por su primera línea física, no por la última de csv.reader
decision|csv/flag-strict|active|El resumen de --strict cuenta registros de datos, no lineas fisicas, y M queda en 0
learning|csv/flag-strict|active|--strict deja stdout vacio si hay errores y JSON compacto de una linea si es valido
testing/unittest-biblioteca-estandar|active
agent-a-csv|active|1. Implementar el flag --strict …
agent-b-csv|active|1. Resolver la pregunta abierta: decidir si los emails duplicados se comparan sin distinguir mayusculas…
```

## Límites de esta evidencia

- **Evaluador no humano.** El PRD pide evaluador humano; aquí evalúa el agente
  delegado que orquestó la prueba. El guion y el contraste con la base están
  hechos; falta la ratificación de una persona.
- **Una sola ejecución y el mismo modelo** en A y B (`deepseek-v4-flash#high`).
  La independencia es de proceso, sesión y contexto —verificada—, no de modelo.
- **El evaluador preparó el fixture** con la trampa del BOM. A no recibió el
  hallazgo: lo descubrió, pero el escenario está diseñado para que aparezca.
- **Base temporal.** Vive en `/tmp/opencode/acm-home`; no se versiona. El hash y
  el volcado lógico de esta sección permiten reconstruir el estado, pero el
  fichero puede desaparecer al limpiar el entorno.
- **El registro del envoltorio se entrelaza** cuando el agente lanza lecturas
  concurrentes; el orden de la fase 1 se reconstruyó con la declaración de B y
  la transcripción de herramientas, no solo con el fichero de log.
- **La base es pequeña** (4 observaciones). El coste de recuperación de B (14
  invocaciones) no es comparable con el benchmark de 10 000 observaciones; aquí
  solo se declara el incumplimiento del presupuesto de la skill.
- **No se midió** M-ACM-02 (p95) ni M-ACM-03 (tokens) en esta ejecución.
