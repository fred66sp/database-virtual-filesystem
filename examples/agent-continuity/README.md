# Agent Continuity Memory (ACM)

Memoria persistente de continuidad para agentes IA que trabajan en un proyecto:
sesiones, hallazgos, decisiones con justificación, checkpoints de reanudación y
preferencias operativas en una base SQLite local por proyecto, montada con
db-ai como el alias `mem`.

ACM no añade un motor de memoria, una CLI, un servidor MCP ni una dependencia
LLM. Compone tres piezas: SQLite con un esquema propio, db-ai como interfaz
determinista y una skill de comportamiento. Los requisitos están en
[docs/PRD.md](docs/PRD.md) y el contrato técnico, incluido el DDL canónico, en
[docs/DESIGN.md](docs/DESIGN.md).

## Requisitos

- `db-ai` en `PATH`, con el contrato de db-ai v0.2.0.
- `sqlite3` para aplicar el esquema (y el fixture solo con `--demo`).
- `python3` para mezclar la entrada del montaje en la configuración de db-ai.
- `flock` (util-linux) para compartir con db-ai el lock de `config.json`.
- Un proyecto anfitrión que excluya `.agent-memory/` de Git. Este repositorio lo
  hace en su `.gitignore` raíz.

## Inicializar

Desde esta carpeta:

```bash
bash init.sh          # memoria limpia: esquema y metadatos reales del proyecto
bash init.sh --demo   # añade los datos ficticios de seed.sql
```

`init.sh` es idempotente y se puede ejecutar desde cualquier directorio:

1. Toma el mismo `flock` exclusivo que db-ai sobre
   `$XDG_CONFIG_HOME/db-ai/config.lock`.
2. Crea `.agent-memory/` y `memory.db` si faltan.
3. Aplica [schema.sql](schema.sql) y las filas de `meta` del proyecto
   (`schema_version`, `project_key` UUIDv7, `project_name` derivado de la carpeta
   y `created_at` UTC) en una sola transacción con `-bail`. Si algo falla, la
   transacción se revierte y no se publica una base a medias.
4. Con `--demo`, aplica además [seed.sql](seed.sql) (sesiones, observaciones,
   checkpoints y preferencias ficticios) en la misma transacción, solo en una
   base nueva.
5. Registra el montaje `mem` en `$XDG_CONFIG_HOME/db-ai/config.json` (o
   `~/.config/db-ai/config.json`) bajo el lock, conservando los demás alias,
   políticas y `cursor_key`.
6. Verifica con `db-ai mounts`, `db-ai ls /mem` y un `db-ai cat` de `meta`.

Sin `--demo`, un proyecto nuevo no contiene sesiones, observaciones,
preferencias ni checkpoints ficticios. Sobre una base existente, `init.sh` no
re-aplica el DDL ni toca los recuerdos: solo asegura el montaje. Una base
parcial (menos de 5 tablas) se rechaza con error claro en vez de completarla a
ciegas; elimínala o recupérala antes de reintentar.

Variables de entorno:

| Variable | Efecto | Defecto |
|---|---|---|
| `ACM_HOME` | Raíz del proyecto cuya memoria se inicializa | Carpeta de `init.sh` |
| `ACM_ALIAS` | Alias de db-ai del montaje | `mem` |
| `XDG_CONFIG_HOME` | Ubicación de la configuración de db-ai | `~/.config` |

El alias es único por configuración de usuario: si `mem` ya apunta a otra base,
`init.sh` se detiene en lugar de pisarla; no hay flag de sobrescritura. Usa
`ACM_ALIAS` para convivir con otro proyecto.

### Por qué el montaje no lo hace `db-ai mount`

`db-ai mount --mode rw` exige una política de escritura explícita y la CLI no
tiene un flag para escribirla: devuelve `E_DENIED`. La política es territorio
del operador y vive en `config.json`. Por eso `init.sh` registra el alias y su
política con una mezcla atómica del fichero, bajo el `flock` de
`config.lock` que db-ai usa en `internal/state`, conservando otros alias.

[config.example.json](config.example.json) reproduce esa entrada con la política
de DESIGN §6: lectura en `/mem` y `touch`/`write` en `sessions`, `observations`,
`checkpoints` y `preferences`. Ninguna escritura en `/mem/meta`, y ningún `cp`,
`mv` ni `rm`. Sustituye el marcador de ruta por la ruta absoluta de la base. Es
una convención del ejemplo, no un contrato adicional: la fuente de la política
es el DESIGN.

## Operar con db-ai

La skill `db-ai` del paquete local `SKILLS-AGP/src/db-ai/` posee las formas de
los comandos, los cursores, los ETags y los códigos de error; este ejemplo no
las repite. El flujo es el de siempre: descubrir con `mounts`, `ls` y
`describe`; leer con `cat`, `find` y `grep`; escribir con `touch` y `write` con
`if_match`; agrupar con `batch`.

Antes de escribir, comprueba `db-ai mounts`: un montaje `ro` o una operación
fuera de la política devuelve `E_DENIED`. Una captura repetida devuelve
`E_CONSTRAINT` por `UNIQUE(content_hash)`; no se reintenta la inserción.

La identidad de cada fila es un UUIDv7 textual que genera el productor. El
`content_hash` es SHA-256 con prefijo `sha256:` y se calcula con el algoritmo de
DESIGN §5.2; el esquema garantiza el formato y la unicidad, no el algoritmo.

## Scenarios

Runbooks ejecutables, no prosa:

- [scenarios/scenario-continuity.md](scenarios/scenario-continuity.md): agente A
  trabaja, captura y cierra con checkpoint; agente B recupera y continúa.
- [scenarios/scenario-integrity.md](scenarios/scenario-integrity.md): aislamiento
  entre proyectos, conflicto de ETag, deduplicación exacta y `E_DENIED` en
  prefijos sin escritura.
- [scenarios/evidence/evidence-benchmark.md](scenarios/evidence/evidence-benchmark.md):
  benchmark de M-ACM-01 a M-ACM-04 sobre el fixture determinista de 10 000
  observaciones, con el guion acotado y su veredicto por variante.
- [scenarios/scenario-agents.md](scenarios/scenario-agents.md): dos agentes LLM
  reales en sesiones separadas; M-ACM-05 con las cinco preguntas de reanudación
  y evaluación contra la base. Su evidencia está en
  [scenarios/evidence/evidence-agents.md](scenarios/evidence/evidence-agents.md).

Trabajan sobre bases temporales cuando la prueba modifica datos; el escenario de
agentes usa una base aislada y el flujo de continuidad usa la base del ejemplo,
que se repite borrando `.agent-memory/` y volviendo a ejecutar `init.sh --demo`.

## Tests

```bash
bash tests/run-tests.sh
```

El helper canónico `tools/content-hash.py` calcula el `content_hash` según el
algoritmo de DESIGN §5.2 (NFC, array JSON canónico, SHA-256): úsalo en vez de
reimplementarlo, porque la deduplicación exacta depende de que todos los
productores calculen igual.

Ejercen sobre una base temporal y una configuración temporal de db-ai: init
limpio, `--demo`, idempotencia, lectura, escritura con ETag, conflicto,
deduplicación, política y concurrencia de init con db-ai sobre `config.json`.
Cada caso imprime `ok` o `FALLO`; el código de salida es 0 solo si todos pasan.

## Garantías y convenciones

| Aspecto | Garantía determinista | Convención |
|---|---|---|
| Duplicado exacto | `UNIQUE(content_hash)`; segunda inserción `E_CONSTRAINT` | cómo recuperar la fila y actualizar `duplicate_count` y `last_seen_at` |
| Sustitución | `CHECK` del enlace y lote transaccional de db-ai | cuándo sustituir y verificar una sola activa |
| Ciclo de vida | valores permitidos de `status` y `review_after` | cuándo marcar `needs_review` u `obsolete` |
| Una decisión activa por tema | protocolo de lote con ETag; no hay índice único parcial | reconciliación de dos primeras capturas concurrentes |
| Procedencia | columnas obligatorias y FK de sesión | no reescribir `agent_id`, `session_id` ni `created_at` |
| Acceso | política por prefijo de db-ai | no es una frontera de seguridad frente al fichero |

Las convenciones de comportamiento —qué capturar, cómo consolidar, cuándo
revisar y cómo reconciliar— son responsabilidad de la skill
`agent-continuity` ([skill/](skill/)); los scenarios muestran el flujo mínimo
que un agente debe seguir. Las mediciones y la práctica con agentes reales están
en [scenarios/evidence/](scenarios/evidence/):
[evidence-benchmark.md](scenarios/evidence/evidence-benchmark.md) mide M-ACM-01 a
M-ACM-04 sobre un fixture de 10 000 observaciones y
[evidence-agents.md](scenarios/evidence/evidence-agents.md) recoge M-ACM-05 con
dos agentes.

## Límites conocidos

- Sin embeddings, FTS ni ranking: `grep` es una subcadena literal.
- `find` devuelve filas completas y ordena por clave ascendente; no hay
  proyección ni orden descendente.
- La política de db-ai no impide que un proceso con acceso al fichero o a
  `config.json` la salte; la separación real exige permisos del sistema
  operativo.
- La primera captura concurrente de un tema puede dejar dos filas activas; hay
  que verificarla y reconciliarla.
- La base no se versiona y no tiene copia automática: la copia es del operador.
