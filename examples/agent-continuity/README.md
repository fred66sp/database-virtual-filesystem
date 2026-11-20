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

## Interfaz AIF: un comando por evento

AIF (Agent Interaction Forms) es la forma de uso para agentes: cada evento
resuelve la necesidad completa en una sola invocación. Los datos van como flags
y la herramienta hace la parte determinista (identidad, hash, validación,
transacción). Solo cuando falta un campo obligatorio devuelve un formulario JSON
con lo que falta y una `instruction` con el siguiente paso; el agente lo rellena
y lo reenvía por `submit` (stdin). No hace falta consultar la ayuda ni reformular
la petición en pasos sucesivos.

```text
acm start [--goal G]
acm decision --topic T --title T --decision D --rationale R [--confidence C]
acm learning | discovery --title T --body B [--topic K] [--confidence C]
acm bugfix --title T --body B [--rationale R] [--topic K] [--confidence C]
acm preference --pref-key K --value V --origin human|agent [--rationale R]
acm recall --query Q [--limit N]
acm checkpoint --state S --next-steps N [--files F] [--open-questions Q]
acm close --summary S [--state S] [--next-steps N] [--files F] [--open-questions Q]
acm submit --data -      # ejecuta un formulario rellenado, leído de stdin
```

`decision` admite además `--alternatives` y `--consequences`. Cada formulario
lleva tres campos pre-rellenos que no se tocan: `event`, `form_id` (UUIDv7) y
`session`. Cada campo de datos es `{"required": bool, "value": "", ...}`; cuando
el valor está acotado, aparece `allowed` con el conjunto cerrado, y cuando hay
una pista útil, `hint`.

### Ejemplo de interacción

```console
$ acm start
Sesión restaurada.
Objetivo: migrar el parser de fechas

## Sesión anterior

Cierre del ciclo anterior: parser de fechas con UTC verificado en-suite
Siguientes pasos: revisar el impacto en los consumidores del parser

## Decisiones vigentes

- **(decision) Las fechas se procesan en UTC** (parser-fechas): ...
```

Una segunda invocación de `acm start` sobre la misma sesión restaura la sesión
y, si existe, añade la sección «Estado de esta sesión» con su checkpoint
activo. La respuesta es Markdown compacto con lo que hay en la base para
retomar el trabajo: la última sesión cerrada con su checkpoint, las decisiones
vigentes, el conocimiento reciente y las preferencias activas. Cada sección
selecciona recuerdos completos — nada cortado a mitad de frase — y omite
identificadores, sellos de tiempo y otros metadatos administrativos; cuando una
sección tiene más entradas que las cinco que caben, cierra con «_hay más_».
Las secciones sin contenido no aparecen.

Los eventos se resuelven directamente con sus flags:

```console
$ acm learning --title "El parser exige UTC" --body "Las fechas sin zona se interpretan como UTC; el test lo fija." --topic parser-fechas
{"ok":true,"result":"created"}
```

Sin flags, el mismo evento devuelve su formulario:

```console
$ acm learning
{"ok":true,"result":"form","form":{"event":"learning","form_id":"019a1f0c-7c3a-7b2e-9a10-3c5d7e9f1a2b","session":"ses-123","title":{"required":true,"value":""},"body":{"required":true,"value":""},"topic":{"required":false,"value":""},"confidence":{"required":false,"value":"","allowed":["confirmed","inference","hypothesis","proposal","unknown"]}},"instruction":"Completa los campos que faltan conservando event, form_id y session; devuelve TODO el JSON por `acm submit --data -` (stdin)."}
```

El agente rellena los `value` y reenvía el formulario entero por stdin:

```console
$ acm submit --data - <<'JSON'
{"event":"learning","form_id":"019a1f0c-7c3a-7b2e-9a10-3c5d7e9f1a2b","session":"ses-123","title":"El parser exige UTC","body":"Las fechas sin zona se interpretan como UTC; el test lo fija.","topic":"parser-fechas","confidence":"confirmed"}
JSON
{"ok":true,"result":"created","id":"019a1f0d-1a2b-7c3d-8e4f-5a6b7c8d9e0f","content_hash":"sha256:9f2c…","duplicate_count":1}
```

La respuesta de confirmación es mínima: el agente sabe si la operación se hizo;
el `id`, el `content_hash` y el recuento de duplicados quedan dentro de la
herramienta.

Reenviar el **mismo `form_id`** no vuelve a capturar: la ejecución es
idempotente y no crea un registro nuevo.

```json
{"ok":true,"result":"already_submitted"}
```

El mismo contenido con un `form_id` **nuevo** no duplica la fila: la
deduplicación es por contenido y consolida el recuerdo existente.

```json
{"ok":true,"result":"consolidated"}
```

### Validación

En `submit`, un formulario incompleto o inválido no escribe nada y devuelve los
campos problema, el formulario con los valores válidos conservados y la
corrección a aplicar; el código de salida es `2` (`E_VALIDATION`). En la
invocación directa, un valor fuera del conjunto cerrado produce el mismo error;
si solo faltan campos obligatorios, devuelve el formulario sin error.

```json
{"ok":false,"error":"E_VALIDATION","message":"el formulario tiene campos inválidos","invalid":{"title":"obligatorio"},"form":{"event":"learning","form_id":"019a1f0c-7c3a-7b2e-9a10-3c5d7e9f1a2b","session":"ses-123","title":{"required":true,"value":""},"body":{"required":true,"value":"texto conservado"},"topic":{"required":false,"value":"parser-fechas"},"confidence":{"required":false,"value":"","allowed":["confirmed","inference","hypothesis","proposal","unknown"]}},"instruction":"corrige los campos indicados y reenvía"}
```

Un valor fuera del conjunto cerrado se rechaza con los permitidos en el
mensaje, por ejemplo `"invalid":{"confidence":"valores permitidos: confirmed, inference, hypothesis, proposal, unknown"}`.

### Consulta, checkpoint y cierre

- `acm recall --query Q [--limit N]` busca en los recuerdos activos. Primero
  exige que aparezcan **todas** las palabras de la consulta; si no hay
  resultados, descarta palabras funcionales y, con tres o más términos, acepta
  que falte uno y ordena por número de coincidencias. Cuando la relajación
  cambia la búsqueda, la respuesta lleva `"relaxed":true`. Si aun así no hay
  nada, devuelve `"count":0` y `"message":"sin resultados; prueba otras palabras
  clave"` en vez de un error. El `limit` va de 1 a 100 (por defecto 10).
- `acm checkpoint --state S --next-steps N [--files F] [--open-questions Q]`
  escribe el checkpoint nuevo y pasa el anterior a `superseded` en un solo
  `batch` de db-ai, con ETag: la cadena `superseded`/`active` nunca queda a
  medias.
- `acm close --summary S [--state S] [--next-steps N] [--files F]
  [--open-questions Q]` escribe, en la **misma transacción**, el checkpoint
  final y cierra la sesión (`status":"closed"`). La respuesta incluye `session`,
  `checkpoint` y `ended_at`.

`acm start` sobre una sesión ya cerrada no la reabre: falla con
`{"ok":false,"error":"E_CONSTRAINT",…}` y código de salida `6`; hay que usar
otro identificador.

### Preferencias

`acm preference --pref-key K --value V --origin human|agent [--rationale R]`
distingue el origen: `origin` admite `human` o `agent`, y el `hint` lo exige
explícito. Una preferencia inferida por el agente nunca se atribuye al usuario:
se registra como `agent`. `human` solo si la persona la manifestó. `--pref-key` y
`--value` son obligatorios; si falta `origin`, el formulario lo pide.

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

## Formularios y automatización

`submit` es la única vía de formulario y se conserva para cuando la herramienta
devuelve uno: no es una segunda interfaz ni un modo distinto. Los comandos
convencionales `remember` y `search` siguen ocultos en la ayuda para
automatización; `remember` es además la única vía de capturar los `kind`
`pattern` y `architecture`, que no tienen evento AIF propio. Un script
automatiza con los mismos flags que un agente, sin pasar por formularios.

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

- `recall` busca por términos: no hay embeddings, FTS ni ranking. La primera
  pasada exige todas las palabras en el `title`, el `body` o el `topic_key`; la
  relajación interna descarta palabras funcionales y, con tres o más términos,
  tolera uno ausente y ordena por coincidencias. Si aun así no hay resultados,
  lo dice con `count:0` y una guía, no con un error.
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
