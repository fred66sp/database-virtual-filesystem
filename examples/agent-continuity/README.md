# Agent Continuity Memory (ACM)

Memoria de continuidad para agentes IA que trabajan en un proyecto: sesiones,
decisiones, aprendizajes, hallazgos, correcciones, preferencias operativas y
checkpoints de reanudación en una base SQLite local por proyecto, montada sobre
db-ai con el alias `mem`.

ACM no añade un motor de memoria, un servicio ni una dependencia LLM. Compone
SQLite con un esquema propio, db-ai como interfaz determinista y un binario Go de
una sola pieza, [go/](go/), que aplica las garantías del esquema: deduplicación
por `content_hash`, control de concurrencia por ETag y mutaciones agrupadas en una
transacción de db-ai. Los requisitos están en [docs/PRD.md](docs/PRD.md) y el
contrato técnico, incluido el DDL canónico, en [docs/DESIGN.md](docs/DESIGN.md).
El adaptador Python `tools/acm.py` se retiró el 2026-11-27; el
binario Go es la única implementación de ACM. La referencia normativa del
`content_hash` que produjo los goldens se conserva en
[tools/content-hash.py](tools/content-hash.py).

## Interfaz: `start`, `aif` y `--version`

El adaptador es un binario Go independiente en [go/](go/): módulo propio, AIF 0.2
como biblioteca nativa y db-ai por CLI. Tiene tres comandos y ninguna otra
superficie: no hay formularios, ni `submit`, ni comandos convencionales.

```text
acm start [--goal G] [--session S]   abre o restaura la sesión y devuelve el contexto
acm aif                              sirve una petición AIF 0.2 por la entrada estándar
acm --version                        muestra la versión
```

### `acm start`

Abre la sesión o la restaura y responde en Markdown compacto con lo que hay en la
base para retomar el trabajo: el objetivo, el checkpoint activo de la misma
sesión, la última sesión cerrada con su checkpoint, las decisiones vigentes, el
conocimiento reciente y las preferencias activas. Cada sección selecciona
recuerdos completos —nada cortado a mitad de frase— y omite identificadores,
sellos de tiempo y otros metadatos administrativos; cuando una sección tiene más
entradas que las cinco que caben, cierra con «_hay más_». Las secciones sin
contenido no aparecen. Una segunda invocación sobre la misma sesión responde
«Sesión restaurada.» y añade, si existe, «Estado de esta sesión».

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

`acm start` sobre una sesión ya cerrada no la reabre: falla con
`{"ok":false,"error":"E_CONSTRAINT",…}` y código de salida `6`; hay que usar otro
identificador. La identidad sale de `--session` o de `OPENCODE_SESSION_ID`.

### `acm aif`

Sirve el perfil CLI de AIF 0.2: lee **una** petición de `stdin`, escribe **un**
objeto JSON en `stdout` y deja los diagnósticos en `stderr`. Sin `operation`
responde el discovery; con `operation` sirve una de las diez operaciones de
memoria. La sesión es un miembro opcional de `input` (`session`); si falta, se
usa `OPENCODE_SESSION_ID`.

```console
$ acm aif <<'JSON'
{"aif":"0.2","operation":"learning","input":{"title":"El parser exige UTC","body":"Las fechas sin zona se interpretan como UTC; el test lo fija.","topic":"parser-fechas"}}
JSON
{"aif":"0.2","operation":"learning","result":"executed","outcome":{"result":"created","session":"ses-123"}}
```

Las diez operaciones son `decision`, `learning`, `discovery`, `bugfix`,
`pattern`, `architecture`, `preference`, `recall`, `checkpoint` y `close`. Cada
tipo de recuerdo tiene su operación, así que el condicional de `decision` —tema y
motivo obligatorios— vive en la operación, no en un hook. La respuesta de
escritura es mínima: `result` (`created`, `consolidated`, `existing` o `closed`)
y `session`; `checkpoint` y `ended_at` cuando aplican. Los detalles internos
(`id`, `content_hash`, `duplicate_count`) no salen de la herramienta.

`echo '{"aif":"0.2"}' | acm aif` devuelve el discovery con las diez operaciones,
sin `result`. Los códigos de salida son los del perfil CLI de AIF: `0` para
`executed` y discovery, `2` para `input_needed` e `invalid`, `3` para
`unknown_operation`, `unsupported_version` y `malformed_request`, `4` para
`denied` y `1` para `failed`. Un `close` o un `checkpoint` sobre una sesión
cerrada produce `denied`; una sesión inexistente produce `invalid` sobre el campo
`session`.

La validación también es la de AIF: si falta un dato obligatorio del formulario,
la respuesta es `input_needed` con `missing`; si un valor no está en el conjunto
cerrado, es `invalid` con el motivo; en ambos casos no se escribe nada. Un fallo
de db-ai conserva su código (por ejemplo `E_CONSTRAINT` o `E_COMMIT_UNKNOWN`) en
un resultado `failed` y **no se reintenta automáticamente**: un `E_COMMIT_UNKNOWN`
significa que el resultado de la transacción es desconocido, y repetirla podría
duplicar el efecto. Los reintentos por `E_CONFLICT` de la consolidación sí se
hacen, hasta tres, porque releer el ETag es seguro.

`recall` busca en los recuerdos activos. Primero exige que aparezcan **todas** las
palabras de la consulta; si no hay resultados, descarta palabras funcionales y,
con tres o más términos, acepta que falte uno y ordena por número de
coincidencias. Cuando la relajación cambia la búsqueda, la respuesta lleva
`"relaxed":true`. Si aun así no hay nada, devuelve `"count":0` y
`"message":"sin resultados; prueba otras palabras clave"`. El `limit` va de 1 a
100 (por defecto 10). La comparación usa casefold y NFC sin marcas combinantes.

`preference` distingue el origen: `origin` admite `human` o `agent`, y `human`
solo se usa si la persona manifestó la preferencia expresamente; una preferencia
inferida por el agente se registra como `agent`. Una preferencia nueva de la
misma clave sustituye a la activa en el mismo `batch`.

## Instalación

Compila el ejecutable desde el commit o el tag que apruebes y enlázalo como
`acm` global; la versión imprime el commit de origen:

```bash
cd examples/agent-continuity/go
go build -ldflags "-X main.commit=<commit>" -o acm ./cmd/acm
ln -sf "$(pwd)/acm" ~/.local/bin/acm
acm --version   # acm 0.1.0 (<commit>)
```

Requisitos: `db-ai` en `PATH` con el contrato v0.2.0, `sqlite3` y `flock` para
`init.sh`; Go 1.27 solo para compilar. Python 3 solo hace falta para `init.sh`
y para la herramienta de referencia `tools/content-hash.py`; el binario no lo
usa en ejecución.

Para crear la base y registrar el montaje, desde esta carpeta:

```bash
bash init.sh          # esquema, metadatos y montaje
bash init.sh --demo   # además, datos ficticios de seed.sql
```

`init.sh` es idempotente, toma el lock de `config.json` que usa db-ai y no toca
los recuerdos de una base existente. La base `.agent-memory/memory.db` se versiona
o no según decida el proyecto anfitrión; por defecto está excluida, y este
repositorio la excluye con el resto del directorio (decidido 2026-11-23). No
tiene copia automática: la copia es del operador.

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

El binario Go implementa el `content_hash` de DESIGN §5.2 (NFC, whitespace de
Python, array JSON canónico y SHA-256); `tools/content-hash.py` es la referencia
normativa y la herramienta con la que se generan los goldens de
`go/internal/memory/testdata/`. La deduplicación exacta depende de que todos los
productores calculen igual: un golden que difiera es un bloqueo, no un ajuste.

## Métricas

Comparación de una prueba puntual con el mismo modelo y una tarea equivalente;
el adaptador AIF se midió antes de retirar la skill y los formularios.

| Interfaz | Invocaciones | Fallos | Coste | Tokens |
|---|---|---|---|---|
| AIF 0.2 (formularios, retirada) | 21 en 50 s | 0 de `submit` | $0.0186 | 22.9k |
| Convencional (`remember`/`search`, retirada) | 17 | — | $0.0140 | 12.7k |
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
- `E_COMMIT_UNKNOWN` no se reintenta: el resultado de la transacción es
  desconocido y repetirla podría duplicar el efecto. Se informa como `failed`
  con su código. Los reintentos por `E_CONFLICT` de la consolidación sí se hacen,
  hasta tres, porque releer el ETag es seguro.
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
que contenía el defecto del upsert en batch. El 2026-11-27 se retiró también la
capa de formularios del adaptador Python (`form_id`, `submit`, los marcadores
`acm-forms/`, los comandos con flags y `remember`/`search`): el binario Go
conserva el núcleo y las garantías, y la interfaz pasa a `start`, `aif` y
`--version`. El 2026-11-27 se retiró también el fichero `tools/acm.py` del árbol:
el binario Go queda como única implementación. Todo es recuperable por el
historial de Git; no se mantiene en paralelo. Las instrucciones operativas
vigentes viven en el bloque «Memoria (ACM)» del
[AGENTS.md](../../AGENTS.md) del repositorio.
