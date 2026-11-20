# Agent Continuity Memory (ACM)

Memoria de continuidad para agentes IA que trabajan en un proyecto: sesiones,
decisiones, aprendizajes, hallazgos, correcciones, preferencias operativas y
checkpoints de reanudación en una base SQLite local por proyecto, montada sobre
db-ai con el alias `mem`.

ACM no añade un motor de memoria, un servicio ni una dependencia LLM. Compone
SQLite con un esquema propio, db-ai como interfaz determinista y un adaptador de
una sola pieza, [tools/acm.py](tools/acm.py), que aplica las garantías del
esquema: deduplicación por `content_hash`, control de concurrencia por ETag y
mutaciones agrupadas en una transacción de db-ai. Los requisitos están en
[docs/PRD.md](docs/PRD.md) y el contrato técnico, incluido el DDL canónico, en
[docs/DESIGN.md](docs/DESIGN.md).

## Interfaz AIF: eventos con formulario

AIF (Agent Interaction Forms) es la forma de uso para agentes. El agente invoca
un evento **sin argumentos** y la herramienta devuelve un formulario JSON con
los campos que debe rellenar y una `instruction` con el siguiente paso. El
agente rellena el formulario y lo devuelve completo por `submit`.

```text
acm start | decision | learning | discovery | bugfix | preference | recall | checkpoint | close
acm submit --data -      # ejecuta un formulario rellenado, leído de stdin
```

Cada formulario lleva tres campos pre-rellenos que no se tocan: `event`,
`form_id` (UUIDv7) y `session`. Cada campo de datos es
`{"required": bool, "value": "", ...}`; cuando el valor está acotado, aparece
`allowed` con el conjunto cerrado, y cuando hay una pista útil, `hint`.

### Ejemplo de interacción

```console
$ acm start --goal "migrar el parser de fechas"
{"ok":true,"result":"created","session":{"id":"ses-123","agent_id":"opencode","goal":"migrar el parser de fechas","status":"open","started_at":"2026-11-20T10:00:00Z","ended_at":null,"summary":null}}
```

Una segunda invocación de `acm start` sobre la misma sesión devuelve
`"result":"restored"` y, si existe, el checkpoint activo. `start` no usa
formulario: abre o restaura la sesión.

```console
$ acm learning
{"ok":true,"result":"form","form":{"event":"learning","form_id":"019a1f0c-7c3a-7b2e-9a10-3c5d7e9f1a2b","session":"ses-123","title":{"required":true,"value":""},"body":{"required":true,"value":""},"topic":{"required":false,"value":""},"confidence":{"required":false,"value":"","allowed":["confirmed","inference","hypothesis","proposal","unknown"]}},"instruction":"Completa el formulario conservando event, form_id y session; reemplaza cada value por tu respuesta y devuelve TODO el JSON mediante: acm submit --data - (por stdin)."}
```

El agente rellena los `value` y reenvía el formulario entero por stdin:

```console
$ acm submit --data - <<'JSON'
{"event":"learning","form_id":"019a1f0c-7c3a-7b2e-9a10-3c5d7e9f1a2b","session":"ses-123","title":"El parser exige UTC","body":"Las fechas sin zona se interpretan como UTC; el test lo fija.","topic":"parser-fechas","confidence":"confirmed"}
JSON
{"ok":true,"result":"created","id":"019a1f0d-1a2b-7c3d-8e4f-5a6b7c8d9e0f","content_hash":"sha256:9f2c…","duplicate_count":1}
```

Reenviar el **mismo `form_id`** no vuelve a capturar: la ejecución es
idempotente y devuelve el registro ya creado.

```json
{"ok":true,"result":"already_submitted","id":"019a1f0d-1a2b-7c3d-8e4f-5a6b7c8d9e0f"}
```

El mismo contenido con un `form_id` **nuevo** no duplica la fila: la
deduplicación es por contenido y consolida el recuerdo existente.

```json
{"ok":true,"result":"consolidated","id":"019a1f0d-1a2b-7c3d-8e4f-5a6b7c8d9e0f","duplicate_count":2}
```

### Validación

Un formulario incompleto o inválido no escribe nada y devuelve los campos
problema, el formulario con los valores válidos conservados y la corrección a
aplicar; el código de salida es `2` (`E_VALIDATION`).

```json
{"ok":false,"error":"E_VALIDATION","message":"el formulario tiene campos inválidos","invalid":{"title":"obligatorio"},"form":{"event":"learning","form_id":"019a1f0c-7c3a-7b2e-9a10-3c5d7e9f1a2b","session":"ses-123","title":{"required":true,"value":""},"body":{"required":true,"value":"texto conservado"},"topic":{"required":false,"value":"parser-fechas"},"confidence":{"required":false,"value":"","allowed":["confirmed","inference","hypothesis","proposal","unknown"]}},"instruction":"corrige los campos indicados y reenvía"}
```

Un valor fuera del conjunto cerrado se rechaza con los permitidos en el
mensaje, por ejemplo `"invalid":{"confidence":"valores permitidos: confirmed, inference, hypothesis, proposal, unknown"}`.

### Consulta, checkpoint y cierre

- `acm recall` devuelve un formulario con `query` (y `limit` opcional) y, al
  enviarlo, los recuerdos activos que contienen **todas** las palabras de la
  consulta. El `hint` del campo pide palabras clave del dominio, no la pregunta
  completa.
- `acm checkpoint` pide `state` y `next_steps` (con `files` y `open_questions`
  opcionales) y escribe el checkpoint nuevo y el paso del anterior a
  `superseded` en un solo `batch` de db-ai, con ETag: la cadena
  `superseded`/`active` nunca queda a medias.
- `acm close` pide `summary` y, en la **misma transacción**, escribe el
  checkpoint final y cierra la sesión (`status":"closed"`). La respuesta incluye
  `session`, `checkpoint` y `ended_at`.

`acm start` sobre una sesión ya cerrada no la reabre: falla con
`{"ok":false,"error":"E_CONSTRAINT",…}` y código de salida `6`; hay que usar
otro identificador.

### Preferencias

`acm preference` distingue el origen: `origin` admite `human` o `agent`, y el
`hint` lo exige explícito. Una preferencia inferida por el agente nunca se
atribuye al usuario: se registra como `agent`. `human` solo si la persona la
manifestó.

## Instalación

```bash
ln -s <repo>/examples/agent-continuity/tools/acm.py ~/.local/bin/acm
```

El enlace deja un comando estable `acm` que apunta al adaptador versionado en el
repositorio. La identidad de sesión sale de `OPENCODE_SESSION_ID` o de
`--session`; el montaje es `ACM_ALIAS` (por defecto `mem`) y el agente
`ACM_AGENT_ID` (por defecto `opencode`).

Requisitos: `db-ai` en `PATH` con el contrato v0.2.0, `sqlite3`, `python3` y
`flock`. Para crear la base y registrar el montaje, desde esta carpeta:

```bash
bash init.sh          # esquema, metadatos y montaje
bash init.sh --demo   # además, datos ficticios de seed.sql
```

`init.sh` es idempotente, toma el lock de `config.json` que usa db-ai y no toca
los recuerdos de una base existente. La carpeta `.agent-memory/` no se versiona
y no tiene copia automática: la copia es del operador.

## Comandos convencionales

`remember` y `search` se conservan para automatización y no aparecen en
`--help`; `checkpoint` y `close` aceptan además sus flags de siempre
(`--state`, `--next-steps`, `--summary`), de modo que un script existente sigue
funcionando sin pasar por formularios. Son una vía de compatibilidad, no la
interfaz recomendada para un agente: el formulario ya dice qué falta y qué
valores admite.

## Escenarios y pruebas

- [scenarios/scenario-continuity.md](scenarios/scenario-continuity.md): agente A
  captura y cierra con checkpoint; agente B recupera y continúa.
- [scenarios/scenario-integrity.md](scenarios/scenario-integrity.md): aislamiento
  entre proyectos, conflicto de ETag, deduplicación exacta y `E_DENIED`.
- [scenarios/scenario-agents.md](scenarios/scenario-agents.md): dos agentes LLM
  reales en sesiones separadas, con su
  [evidencia](scenarios/evidence/evidence-agents.md).

```bash
bash tests/run-tests.sh
```

El helper `tools/content-hash.py` calcula el `content_hash` de DESIGN §5.2
(NFC, array JSON canónico, SHA-256): úsalo en vez de reimplementarlo, porque la
deduplicación exacta depende de que todos los productores calculen igual.

## Métricas

Comparación de una prueba puntual con el mismo modelo y una tarea equivalente;
el adaptador AIF se midió antes de retirar la skill.

| Interfaz | Invocaciones | Fallos | Coste | Tokens |
|---|---|---|---|---|
| AIF (eventos y formularios) | 21 en 50 s | 0 de `submit` | $0.0186 | 22.9k |
| Convencional (`remember`/`search`) | 17 | — | $0.0140 | 12.7k |
| Sin adaptador (db-ai directo) | 31 | 6 errores | $0.0615 | 106k |

Es `n=1`: una sola ejecución por variante. Sirve para justificar la interfaz,
no para generalizar ni para prometer un ahorro. Con db-ai directo el agente
pagó los errores de forma y de contrato; con AIF el formulario los evita, a
cambio de más tokens que la vía convencional.

## Límites declarados

- `recall` busca por términos: todas las palabras deben aparecer en el
  `title`, el `body` o el `topic_key`; no hay embeddings, FTS ni ranking. El
  formulario lo recuerda con un `hint`.
- Los marcadores de idempotencia son un fichero por `form_id` junto a la base
  (`acm-forms/`) y **no se podan**. Cada uno ocupa unos bytes; la limpieza es
  manual.
- La deduplicación de checkpoints es global por `content_hash`: dos sesiones con
  el mismo objetivo, estado y pasos producen el mismo checkpoint y la segunda
  recibe el existente (`"result":"existing"`), no una fila nueva. Es una
  decisión del esquema, no un fallo.
- La política de db-ai no impide que un proceso con acceso al fichero o a
  `config.json` la salte; la separación real exige permisos del sistema
  operativo.

## Qué quedó retirado

Se retiraron la skill `agent-continuity` del paquete local `SKILLS-AGP` y sus
copias, y la carpeta `skill/` de este ejemplo con su referencia de protocolos,
que contenía el defecto del upsert en batch. Todo es recuperable por el
historial de Git; no se mantiene en paralelo. Las instrucciones operativas
vigentes viven en el bloque «Memoria (ACM)» del [AGENTS.md](../../AGENTS.md) del
repositorio.
