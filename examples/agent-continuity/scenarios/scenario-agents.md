# Escenario de reanudación con dos agentes reales

Runbook ejecutable de **M-ACM-05** (PRD §7): dos agentes LLM en sesiones
separadas, sin conversación compartida, con una memoria ACM como único canal de
continuidad. A diferencia de [scenario-continuity.md](scenario-continuity.md),
aquí el agente B es un agente real que decide qué consultar y cómo interpretarlo:
no es una transcripción de comandos elegidos por la persona.

Objetivo: 5 de 5 preguntas respondidas correctamente. Fracaso: menos de 4 de 5.

## Qué se mide y qué no

- **Se mide:** si B reconstruye objetivo, decisiones, siguiente paso,
  preferencias y descubrimientos usando solo la memoria, sin historial de A y
  sin Git, y si continúa el trabajo desde el checkpoint.
- **No se mide:** latencia ni tokens (M-ACM-01/M-ACM-03 tienen su propio
  runbook) ni la calidad del código producido.
- El evaluador debe contrastar cada respuesta con el estado real de la base
  (`sqlite3`) y del proyecto. El veredicto por pregunta es
  `correcta`, `parcial` o `incorrecta`, con la fila o fichero que lo sustenta.

## Entorno aislado

No se toca la configuración real de db-ai ni la base del ejemplo.

```bash
WORK=/tmp/opencode
SANDBOX=$WORK/sandbox-csv
XDG=$WORK/xdg
ACM_HOME=$WORK/acm-home
BIN=$WORK/acm-bin
mkdir -p "$SANDBOX" "$SANDBOX/data" "$SANDBOX/tests" "$XDG" "$ACM_HOME" "$BIN" "$WORK/logs"

# db-ai del árbol, con `version`
cd /ruta/a/database-virtual-filesystem
go build -o "$WORK/db-ai" ./cmd/db-ai
"$WORK/db-ai" version

# Memoria limpia, aislada, alias mem
cd examples/agent-continuity
PATH="$WORK:$PATH" XDG_CONFIG_HOME="$XDG" ACM_HOME="$ACM_HOME" bash init.sh

# Envoltorio: fija la configuración aislada y registra cada invocación
cat > "$BIN/db-ai" <<'SH'
#!/usr/bin/env bash
export XDG_CONFIG_HOME=/tmp/opencode/acm-xdg
log=${ACM_DB_AI_LOG:-/dev/null}
{ printf '### %s cwd=%s\n' "$(date -u +%FT%TZ)" "$PWD"; printf 'argv:'; printf ' %q' "$@"; printf '\n'; } >>"$log"
out=$(/tmp/opencode/db-ai "$@" 2>&1); rc=$?
printf '%s\nrc: %s\n' "$out" "$rc" >>"$log"
printf '%s\n' "$out"; exit "$rc"
SH
chmod +x "$BIN/db-ai"
```

Para que cualquier agente que solo invoque `db-ai` use esa configuración, pon en
`$BIN` un envoltorio que exporte `XDG_CONFIG_HOME=$XDG` y registre cada
invocación (argv, salida, código de salida) en un fichero por agente. Así la
transcripción de comandos es literal y no depende de lo que el agente recuerde
contar.

El envoltorio fija las rutas absolutas de este runbook (`WORK=/tmp/opencode`); si
cambias `WORK`, ajústalas.

## Proyecto sandbox

Fixture mínimo, realista y con una trampa verificable (BOM UTF-8, CRLF y un
campo entrecomillado con salto de línea). El agente A debe topársela al
implementar, no se le anuncia.

```bash
python3 - "$SANDBOX" <<'PY'
import sys, pathlib
root = pathlib.Path(sys.argv[1])
(root / "data").mkdir(parents=True, exist_ok=True)
(root / "tests").mkdir(exist_ok=True)
rows = [
    "email,name",
    "ana@example.com,Ana",
    "luis@example.com,Luis",
    "ana@example.com,Ana Duplicada",
    ",",
    '"rota@example.com","Nombre\ncon salto"',
]
(root / "data" / "contacts.csv").write_bytes(
    b"\xef\xbb\xbf" + ("\r\n".join(rows) + "\r\n").encode("utf-8")
)
PY

cat > "$SANDBOX/README.md" <<'MD'
# validador-csv

Utilidad pequeña para validar el fichero de contactos `data/contacts.csv`.
Sin dependencias: solo la biblioteca estándar de Python 3.

## Alcance

`validate.py` lee `data/contacts.csv` y comprueba:

1. Cabecera exacta: `email,name`, en ese orden.
2. Cada fila tiene `email` y `name` no vacíos.
3. `email` tiene forma `usuario@dominio` (sin espacios).
4. No hay emails duplicados.

Salida: una línea por problema en stderr con el número de línea física del
fichero; exit 1 si hay algún problema, exit 0 si el fichero es válido.

## Pendiente (siguiente paso del proyecto)

- Flag `--strict`: además de lo anterior, trata como error los nombres con
  espacios al principio o al final, y emite un resumen JSON
  (`{"rows":N,"errors":M}`) en stdout cuando el fichero es válido.
- Pruebas del flag en `tests/`.

## Uso

```bash
python3 validate.py
python3 validate.py --strict   # pendiente
```
MD
```

El pendiente del README es el siguiente paso que B debe ejecutar.

## Agente A — trabajar, capturar y dejar checkpoint

Lánzalo en su propia sesión, con el directorio de trabajo en `$SANDBOX` y sin
historial previo. Materializa el prompt:

```bash
cat > "$WORK/logs/prompt-agent-a.md" <<'PROMPT'
Eres el AGENTE A de una prueba de continuidad entre agentes. Trabajas solo, en un proyecto sandbox, y tu rastro debe quedar en una memoria ACM.

## Entorno
- Directorio de trabajo: `/tmp/opencode/sandbox-csv`. Es tu proyecto. Trabaja solo ahí.
- PROHIBIDO escribir en `/home/alfredo/PROJECTS/database-virtual-filesystem`: solo puedes LEER los ficheros de skill que se indican abajo. Nada de git, commits ni tasks-ai.
- Tienes la CLI `db-ai` en PATH, ya configurada. La memoria ACM está montada como alias `mem` sobre una base limpia. Compruébalo con `db-ai mounts`.
- `sqlite3` está disponible por si lo necesitas para inspeccionar, pero la memoria se escribe SIEMPRE con `db-ai`.

## Protocolo obligatorio (léelo antes de actuar)
1. Lee completo `/home/alfredo/PROJECTS/database-virtual-filesystem/examples/agent-continuity/skill/SKILL.md`.
2. Lee completo `/home/alfredo/PROJECTS/database-virtual-filesystem/examples/agent-continuity/skill/references/protocolos.md`.
3. Lee completo `/home/alfredo/PROJECTS/SKILLS-AGP/src/db-ai/SKILL.md` (formas exactas de los comandos). Sus `references/` si dudas de un flag.
4. El `content_hash` se calcula con el helper canónico, no a mano:
   `python3 /home/alfredo/PROJECTS/database-virtual-filesystem/examples/agent-continuity/tools/content-hash.py observations` leyendo el JSON por stdin (también acepta `checkpoints` y `preferences`).
5. Los UUID que uses son UUIDv7 textuales; genera los tuyos (p. ej. con python3 y `uuid.uuid7()` si existe, o construyéndolos como en init.sh).

## Tu encargo técnico
Lee `/tmp/opencode/sandbox-csv/README.md` y su fichero `data/contacts.csv`. Implementa `validate.py` exactamente con el alcance descrito en el README (cabecera, campos no vacíos, forma de email, duplicados, número de línea física, exit 0/1) y crea pruebas en `tests/` que ejecutes. NO implementes el flag `--strict`: es el siguiente paso pendiente del proyecto y debe quedar así.

Durante el trabajo aparecerá al menos un problema no trivial con el fichero real (no lo evites ni lo rodees simplificando el alcance: diagnostícalo y resuélvelo en el código). No inventes hallazgos: captura lo que realmente te ocurra y cómo lo comprobaste.

## Tu encargo de memoria (mínimo obligatorio, con db-ai)
Sigue los protocolos de la skill ACM. En concreto:
1. Abre TU sesión en `/mem/sessions` con `agent_id` = `agent-a-csv`, `source` = `agent`, objetivo del encargo, `status` = `open` y marcas ISO-8601 UTC. No escribas nada más antes de abrir la sesión.
2. Captura en `/mem/observations`, vinculadas a tu sesión:
   - 1 observación `kind=discovery` con el hallazgo no trivial (título distinctive, cuerpo con qué pasó y cómo se comprobó, `confidence=confirmed`).
   - 1 observación `kind=decision` con `topic_key` (formato `familia/descripcion`) y `rationale` no vacío: una decisión de diseño que hayas tomado y por qué, con la alternativa descartada.
   Puedes añadir más si son durables, pero esas dos son obligatorias.
3. Captura en `/mem/preferences` UNA preferencia operativa con `pref_key` estable, `value`, `rationale` y `status=active` (por ejemplo sobre dependencias o estilo de pruebas). Justifícala.
4. Crea UN checkpoint en `/mem/checkpoints` para tu sesión con `goal`, `state`, `next_steps` (la primera acción debe ser implementar el flag `--strict` según el README), `files` (rutas relativas al proyecto, p. ej. `["validate.py","tests/"]`) y `open_questions` (`[]` si no hay).
5. Verifica con `cat` lo escrito.
6. Cierra tu sesión con `write`: `status=closed`, `ended_at`, `summary`, `updated_at`, usando el ETag vigente leído de la fila. El checkpoint debe seguir `active`.

No captures conversación ni actividad rutinaria. Al terminar, responde con un informe breve que incluya: `session_id`, `agent_id`, los `id` de las dos observaciones, de la preferencia y del checkpoint, el texto del hallazgo y de la decisión, el `next_steps` del checkpoint, el resultado de las pruebas, y cualquier error o duda. Indica también el número total de invocaciones de `db-ai` que hiciste.
PROMPT
```

## Agente B — reanudar sin contexto

Proceso y sesión nuevos. No se le pasa nada de A: ni conversación, ni ids, ni
resumen. Materializa el prompt:

```bash
cat > "$WORK/logs/prompt-agent-b.md" <<'PROMPT'
Eres el AGENTE B de una prueba de continuidad entre agentes. Retomas un proyecto que NO conoces. No tienes ninguna conversación previa: la única fuente de contexto es una memoria ACM montada. No se te dará nada del agente anterior.

## Entorno
- Directorio de trabajo: `/tmp/opencode/sandbox-csv`.
- PROHIBIDO escribir en `/home/alfredo/PROJECTS/database-virtual-filesystem`: solo puedes LEER los ficheros de skill que se indican abajo. Nada de git, commits ni tasks-ai.
- Tienes la CLI `db-ai` en PATH, ya configurada. La memoria ACM del proyecto está montada como alias `mem`. Compruébalo con `db-ai mounts`.
- Usa SOLO `db-ai` para leer la memoria (no abras la base con `sqlite3`, no leas `/tmp/opencode/acm-home` ni `/tmp/opencode/logs`). No busques fuera de la memoria y del proyecto.

## Protocolo obligatorio (léelo antes de actuar)
1. Lee completo `/home/alfredo/PROJECTS/database-virtual-filesystem/examples/agent-continuity/skill/SKILL.md`.
2. Lee completo `/home/alfredo/PROJECTS/database-virtual-filesystem/examples/agent-continuity/skill/references/protocolos.md`, en especial el protocolo 1 (reanudación), el 3.6 (guion acotado) y el 6.2.
3. Lee completo `/home/alfredo/PROJECTS/SKILLS-AGP/src/db-ai/SKILL.md` para las formas exactas de los comandos.
4. Recupera la continuidad con el guion acotado de la skill (ventana de sesiones, checkpoint vigente, decisiones y preferencias aplicables). No pidas tablas enteras.

## Fase 1 — responder ANTES de tocar el proyecto
No leas ningún fichero del sandbox (README, código, tests, datos) hasta haber respondido. Usa únicamente la memoria.

Responde por escrito, de forma literal y numerada, a estas cinco preguntas:
1. ¿Cuál era el objetivo del trabajo?
2. ¿Qué decisiones están vigentes y por qué?
3. ¿Cuál es el siguiente paso pendiente?
4. ¿Qué preferencias operativas rigen?
5. ¿Qué descubrimientos previos no debes repetir?

Para cada respuesta, indica la fila o ruta de memoria que la sustenta (id y tabla). Si algo no está en la memoria, dilo explícitamente en vez de inferirlo. Declara también las invocaciones de `db-ai` que hiciste, en orden y con la forma literal del comando.

## Fase 2 — continuar el trabajo desde el checkpoint
Después de responder:
1. Abre TU propia sesión en `/mem/sessions` con `agent_id` = `agent-b-csv`, `source` = `agent`, objetivo, `status` = `open` y marcas ISO-8601 UTC.
2. Ejecuta el siguiente paso pendiente que hayas recuperado, respetando las decisiones y preferencias vigentes. Al terminar, ejecuta las pruebas y comprueba el resultado.
3. Captura en la memoria lo que sea durable de tu intervención: al menos una observación `kind=learning` o `bugfix` (si procede) y un checkpoint nuevo para tu sesión con `goal`, `state`, `next_steps`, `files` y `open_questions`. Si no hay conocimiento durable nuevo, dilo y no inventes filas.
4. Cierra tu sesión con `write` usando el ETag vigente.
5. El `content_hash` se calcula con `python3 /home/alfredo/PROJECTS/database-virtual-filesystem/examples/agent-continuity/tools/content-hash.py <tabla>` leyendo el JSON por stdin.

## Informe final
Incluye: las cinco respuestas (repite el texto literal), las invocaciones de `db-ai` de la fase 1 en orden, qué hiciste en la fase 2 con el resultado de las pruebas, los ids de lo que escribiste, y cualquier error, duda o contradicción encontrada entre la memoria y el proyecto.
PROMPT
```

### Las cinco preguntas (literales)

1. ¿Cuál era el objetivo del trabajo?
2. ¿Qué decisiones están vigentes y por qué?
3. ¿Cuál es el siguiente paso pendiente?
4. ¿Qué preferencias operativas rigen?
5. ¿Qué descubrimientos previos no debes repetir?

## Ejecución

```bash
# A
cd "$SANDBOX"
ACM_DB_AI_LOG=$WORK/logs/agent-a-dbai.log PATH="$BIN:$PATH" \
  opencode run --standalone --agent general --auto --format json \
  --title "agente A" "$(cat "$WORK/logs/prompt-agent-a.md")" \
  > "$WORK/logs/agent-a.jsonl"

# B, proceso nuevo; el prompt no contiene nada de A
cd "$SANDBOX"
ACM_DB_AI_LOG=$WORK/logs/agent-b-dbai.log PATH="$BIN:$PATH" \
  opencode run --standalone --agent general --auto --format json \
  --title "agente B" "$(cat "$WORK/logs/prompt-agent-b.md")" \
  > "$WORK/logs/agent-b.jsonl"
```

Si el entorno ofrece una herramienta `subagent`, sirve igual: la condición es
una sesión independiente por agente y un prompt de B que no transporte el
historial de A. Guarda el `sessionID` que imprime `--format json`.

## Evaluación

Para cada pregunta:

1. Extrae la respuesta literal de B (primer bloque de texto posterior a la
   recuperación, antes de leer el sandbox).
2. Contrasta con `sqlite3` sobre la base usada:
   `sessions.goal`, `checkpoints.next_steps`, `observations` activas por
   `topic_key`, `preferences` activas por `pref_key`, y los ficheros del
   sandbox para lo ejecutado en la fase 2.
3. Veredicto `correcta` / `parcial` / `incorrecta` con la evidencia. Cuenta las
   correctas: **M-ACM-05 = correctas de 5**; por debajo de 4, fallo.

Comprueba además, con la transcripción `--format json`, que B no leyó el sandbox
antes de responder (`read`/`shell` sobre `$SANDBOX`) y que no hubo Git.

## Cierre

- La base queda en `$ACM_HOME/.agent-memory/memory.db`; es la que la evidencia
  describe y se puede abrir con `sqlite3` en modo lectura.
- Los directorios de trabajo son temporales: no se versiona la base ni el
  sandbox.
- Registra el resultado en
  [`scenarios/evidence/evidence-agents.md`](evidence/evidence-agents.md).
