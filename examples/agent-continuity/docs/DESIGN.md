# Agent Continuity Memory (ACM) — Technical Design

**Versión:** 0.1.0 · **Estado:** en revisión del PO con el PRD actualizado (§18) · **Fecha:** 2026-11-19 · **Requisitos:** [PRD.md](PRD.md) v0.1.0
**Base:** db-ai v0.2.0 — [PRD de db-ai](../../../docs/PRD.md) y [DESIGN de db-ai](../../../docs/DESIGN.md).
**Skill de referencia:** paquete local `db-ai` v0.1.0 en `SKILLS-AGP/src/db-ai/`: `SKILL.md` y `references/comandos.md`, `references/escritura.md`, `references/errores.md`.

## 1. Principios y composición

ACM no añade un motor de memoria, una CLI, un servidor MCP ni una dependencia de proveedor LLM. Compone tres piezas que ya existen:

1. SQLite como almacén local, con un esquema propio del proyecto.
2. db-ai como interfaz determinista de rutas, lectura, escritura, ETag, política, transacciones y auditoría.
3. Una skill de comportamiento que decide qué capturar, cómo consolidar y cómo recuperar.

```text
Agente IA
   |
   |  convenciones: capturar, consolidar, recuperar, reconciliar
   v
Skill agent-continuity (ACM-04)
   |
   |  mounts, ls, cat, find, grep, export, touch, write, batch
   v
CLI db-ai v0.2.0
   |
   |  rutas /mem/tabla/uuid, ETag, política, transacción
   v
SQLite .agent-memory/memory.db  (alias mem)
```

La separación entre garantías y convenciones es explícita:

| Capa | Aporta | No aporta |
|---|---|---|
| SQLite (esquema de ACM) | PK única, FK, `CHECK`, `UNIQUE`, orden de clave | semántica de consolidación |
| db-ai v0.2.0 | rutas virtuales, JSON versionado, ETag, política por prefijo, transacciones, auditoría, límites | DDL, triggers, búsqueda semántica, orden descendente, proyección de columnas |
| skill agent-continuity | cuándo capturar, elección de tipo y certeza, canonicalización del hash, `topic_key`, revisión, presupuesto de contexto | garantías del motor; no puede impedir que un proceso externo salte la política |

**Regla de composición:** ACM usa db-ai tal como está documentado. No modifica `cmd/db-ai/` ni `internal/`; no crea comandos, flags ni operaciones nuevas. Una carencia de db-ai se documenta en §14 como propuesta independiente, nunca como ampliación del alcance de ACM.

El paquete de la skill agent-continuity (ACM-04) se desarrolla en `examples/agent-continuity`; estos dos documentos viven en `examples/agent-continuity/docs/`. Este DESIGN fija el contrato de la memoria y no replica la estructura interna de la skill `db-ai` ni sus referencias.

## 2. Arquitectura de composición

| Componente | Responsabilidad | Fuera de alcance |
|---|---|---|
| Base SQLite del proyecto | persistencia local y aislada | sincronización remota, servidor |
| db-ai | acceso por rutas, precondiciones, transacciones, auditoría | DDL, migraciones, triggers, búsqueda semántica |
| skill agent-continuity | protocolo de captura, consolidación y recuperación | motor de ejecución o daemon |
| operador | inicializa el esquema, monta el alias y edita la política | identidad autenticada |
| repositorio anfitrión | excluye `.agent-memory/` de Git | copia de seguridad automática |

No hay proceso en segundo plano, hook de sesión ni planificador. La captura ocurre cuando el agente invoca la skill y decide consolidar. La auditoría de las operaciones la produce db-ai en su formato local (DESIGN de db-ai §11); ACM no duplica ese registro. La trazabilidad de conocimiento vive en las columnas de procedencia de cada fila.

## 3. Aislamiento, ruta y bootstrap

### 3.1 Ruta y alias

- Una base por proyecto: `.agent-memory/memory.db`, relativa a la raíz del proyecto que usa la memoria.
- El directorio `.agent-memory/` queda excluido por `.gitignore` del proyecto anfitrión. La memoria no se versiona con el código.
- El operador monta el fichero como alias `mem`, con `--mode rw` y la política de §6. Un observador de solo lectura puede montarlo en `ro`; ACM no necesita escritura para recuperar.
- `mount` y `umount` son operaciones del operador, no del agente (DESIGN de db-ai §9 y skill `db-ai`).

### 3.2 Identidad del proyecto

`meta` es una tabla clave/valor con PK UUIDv7. Cada fila tiene un `id` UUIDv7 y un `name` único; el valor se consulta con `find` por `name` o con `cat` por el `id` devuelto. Las filas iniciales son:

| `name` | `value` | Uso |
|---|---|---|
| `schema_version` | `1` | versión del esquema para migraciones |
| `project_key` | UUIDv7 generado al inicializar | identidad estable del proyecto |
| `project_name` | etiqueta legible del proyecto | comprobación de que la base corresponde al proyecto |
| `created_at` | marca temporal de inicialización | procedencia de la base |

La skill guarda el `project_key` esperado en su configuración de proyecto, fuera de la base, y lo compara con el valor de `meta`. Si no coincide, se detiene y no escribe: una base copiada desde otro proyecto no debe contaminar la memoria. `project_key` sobrevive a un cambio de nombre del directorio o del repositorio; `project_name` es la etiqueta humana y puede usarse como comprobación secundaria, pero no como identidad.

`meta` es de solo lectura a través de db-ai: la política no concede escritura sobre `/mem/meta`. La inicialización y una migración futura se ejecutan fuera de db-ai, con SQLite y revisión del operador.

### 3.3 Bootstrap

`init.sh` implementa este bootstrap; no es una nueva CLI de ACM. Por defecto crea una memoria limpia y `--demo` añade el fixture de `seed.sql`.

1. Crear `.agent-memory/` y excluirlo en `.gitignore`.
2. Tomar el mismo `flock` exclusivo que db-ai sobre `$XDG_CONFIG_HOME/db-ai/config.lock`, para que la mezcla de `config.json` no pierda montajes escritos por otro proceso.
3. Aplicar el DDL de §4.8 y las filas de `meta` de §3.2 en una sola transacción con `-bail` y `PRAGMA foreign_keys = ON`; si algo falla, la transacción se revierte y no se publica una base a medias.
4. Con `--demo`, aplicar `seed.sql` en la misma transacción y solo sobre una base nueva.
5. Registrar el montaje `mem` en `rw` con la política de §6, conservando los demás alias, políticas y `cursor_key`.
6. Verificar `mounts`, `describe /mem/observations` y la lectura de `meta` antes de capturar.

Sobre una base existente, `init.sh` no re-aplica el DDL ni toca los recuerdos: solo asegura el montaje. Una base parcial (menos de cinco tablas) se rechaza con error en vez de completarse a ciegas. db-ai no crea tablas ni índices (C-ACM-07); el bootstrap sigue siendo una dependencia explícita del operador. No se fija `journal_mode` en esta versión: se deja el valor por defecto de SQLite y la contención se resuelve con las transacciones y los tiempos límite de db-ai. Activar WAL es una decisión futura sujeta a prueba.

## 4. Modelo de datos

### 4.1 Convenciones de representación

- **Identidad:** UUIDv7 de RFC 9562, textual, minúsculas y con guiones. Lo genera el productor (agente u operador); la base no lo inventa. La representación textual de 36 caracteres es ordenable lexicográficamente por tiempo y es un segmento seguro de ruta db-ai.
- **PK textual no nula:** toda PK se declara `TEXT NOT NULL PRIMARY KEY`. Un `TEXT PRIMARY KEY` sin `NOT NULL` admite NULL en SQLite; db-ai lo detecta como limitación (`primary key columns may contain NULL`) y marca la tabla como no navegable. La comprobación empírica de §15 confirma que con `NOT NULL` la tabla es navegable y escribible.
- **Marcas temporales:** texto ISO-8601 en UTC, formato `YYYY-MM-DDTHH:MM:SSZ`. La misma forma se usa en `review_after`. UUIDv7 da el orden; las marcas temporales dan la semántica y permiten filtros de rango.
- **Enumeraciones:** valores en inglés, estables y sin acentos. `source` usa `agent`, `human`, `import`; `kind` usa `discovery`, `decision`, `pattern`, `bugfix`, `architecture`, `learning`; el ciclo de vida usa `active`, `superseded`, `obsolete`, `needs_review`; las sesiones usan `open`, `closed`, `abandoned`.
- **Certeza:** `confirmed`, `inference`, `hypothesis`, `proposal`, `unknown`. Equivalen a hecho, inferencia, hipótesis, propuesta y desconocido del PRD §10.
- **Campos JSON:** `alternatives`, `consequences`, `files` y `open_questions` son texto con JSON compacto y UTF-8. `files` es un array de rutas relativas al repositorio, nunca rutas de una máquina concreta.
- **Sin `STRICT`, `WITHOUT ROWID`, columnas generadas ni triggers:** no se han verificado con db-ai y los triggers de escritura sobre la tabla objetivo se rechazan (DESIGN de db-ai §8). La integridad se obtiene con PK, FK, `CHECK` y `UNIQUE`; el hash se calcula en el productor.
- **`content_hash`:** SHA-256 textual con prefijo `sha256:`. La base impone el formato y la unicidad; el productor calcula el valor con el algoritmo de §5.2.

### 4.2 `meta`

Tabla clave/valor de infraestructura, no de conocimiento. `id` es UUIDv7 y `name` es único. Se consulta por `find` sobre `name` o por `cat` sobre el `id`. Es de solo lectura a través de db-ai y se modifica solo en bootstrap o migración revisada.

### 4.3 `sessions`

Registra la sesión de trabajo de ACM. `id` UUIDv7, `agent_id`, `source`, `goal`, `status`, `started_at`, `ended_at`, `summary` y marcas temporales. La sesión de ACM es una fila de memoria; no es la sesión `--session` de db-ai, que solo resuelve `cd` y `pwd`. Cada agente escribe en su propia sesión y no modifica la de otro; la recuperación puede leer checkpoints de sesiones anteriores del proyecto, aunque pertenezcan a otro agente, porque la continuidad es del proyecto y la procedencia queda registrada.

La sesión se abre antes de escribir conocimiento y se cierra al terminar. Un cierre escribe `status`, `ended_at` y `summary` con ETag. La política permite escribir en la tabla; la skill limita esos campos por convención (§6.3).

### 4.4 `observations`

Es la única superficie de conocimiento general. Incluye descubrimientos, patrones, correcciones, arquitectura y aprendizajes, y también las decisiones (`kind = 'decision'`). Se eligió una tabla única frente a una tabla `decisions` separada (D-ACM-05) para tener una sola búsqueda literal, una sola deduplicación y un solo ciclo de vida. Las decisiones conservan su forma mediante columnas específicas y `CHECK`:

- `body` contiene el enunciado de la decisión cuando `kind = 'decision'`; en los demás tipos contiene el conocimiento.
- `rationale` es obligatorio y no vacío para `kind = 'decision'`; `alternatives` y `consequences` son opcionales.
- `topic_key` es obligatorio para `kind = 'decision'` y sigue la convención `familia/descripcion` en minúsculas y kebab-case, con dos niveles como máximo.
- `confidence` expresa el grado de certeza; `status` expresa el ciclo de vida. No se mezclan.
- `duplicate_count` y `last_seen_at` registran repeticiones exactas detectadas; `content_hash` es la clave de deduplicación.
- `origin_ref` es una referencia opcional a la evidencia (documento, ruta relativa, identificador de tarea). No sustituye a la procedencia estructurada.

### 4.5 `checkpoints`

Captura el estado de reanudación de una sesión: `goal`, `state`, `next_steps`, `files`, `open_questions`. `session_id` es obligatorio. Un checkpoint se sustituye por otro de la misma sesión y conserva el anterior. No tiene `review_after`: su vigencia la determina la existencia de un checkpoint posterior, no el paso del tiempo.

### 4.6 `preferences`

Preferencias operativas del proyecto por clave: `pref_key`, `value`, `rationale`, procedencia y ciclo de vida. `session_id` puede ser nulo para una preferencia creada por una persona sin sesión. La clave sigue la convención de `topic_key`; por ejemplo `commit/approval`, `tools/no-playwright`, `docs/language`. Las preferencias son del proyecto, no globales ni por agente en esta versión (decidido por el PO en PRD §14, 2026-11-19).

### 4.7 Índices

| Índice | Columnas | Para qué |
|---|---|---|
| PK de cada tabla | `id` | identidad y ruta db-ai |
| `UNIQUE(content_hash)` | `content_hash` | deduplicación exacta determinista |
| `UNIQUE(name)` en `meta` | `name` | una fila por clave de metadatos |
| `ix_sessions_agent_status` | `agent_id`, `status`, `started_at` | sesiones abiertas de un agente |
| `ix_sessions_status_started` | `status`, `started_at` | sesiones abiertas o cerradas |
| `ix_observations_session` | `session_id`, `created_at` | historial de una sesión |
| `ix_observations_kind_status` | `kind`, `status`, `created_at` | decisiones y descubrimientos activos |
| `ix_observations_topic` | `topic_key`, `status` | decisión activa por tema |
| `ix_checkpoints_session` | `session_id`, `created_at` | checkpoints de una sesión |
| `ix_checkpoints_status` | `status`, `created_at` | checkpoint activo |
| `ix_preferences_key` | `pref_key`, `status` | preferencia activa por clave |
| `ix_preferences_status` | `status`, `created_at` | preferencias vigentes |

`grep` es una subcadena literal y no usa un índice de texto: su coste crece con las filas. Los índices anteriores aceleran los filtros de `find`; no prometen latencia de `grep` (§10 y §14).

### 4.8 DDL completo

El `PRAGMA` inicial es de conexión; db-ai activa las claves foráneas en cada conexión (DESIGN de db-ai §8). El bootstrap debe ejecutarlo en la misma conexión que crea el esquema.

```sql
PRAGMA foreign_keys = ON;

CREATE TABLE meta (
  id          TEXT NOT NULL PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE CHECK (length(trim(name)) > 0),
  value       TEXT NOT NULL CHECK (length(trim(value)) > 0),
  updated_at  TEXT NOT NULL
);

CREATE TABLE sessions (
  id          TEXT NOT NULL PRIMARY KEY,
  agent_id    TEXT NOT NULL,
  source      TEXT NOT NULL CHECK (source IN ('agent','human','import')),
  goal        TEXT NOT NULL CHECK (length(trim(goal)) > 0),
  status      TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','closed','abandoned')),
  started_at  TEXT NOT NULL,
  ended_at    TEXT,
  summary     TEXT,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

CREATE INDEX ix_sessions_agent_status ON sessions (agent_id, status, started_at);
CREATE INDEX ix_sessions_status_started ON sessions (status, started_at);

CREATE TABLE observations (
  id              TEXT NOT NULL PRIMARY KEY,
  session_id      TEXT NOT NULL REFERENCES sessions(id),
  agent_id        TEXT NOT NULL,
  source          TEXT NOT NULL CHECK (source IN ('agent','human','import')),
  kind            TEXT NOT NULL CHECK (kind IN ('discovery','decision','pattern','bugfix','architecture','learning')),
  title           TEXT NOT NULL CHECK (length(trim(title)) > 0),
  body            TEXT NOT NULL CHECK (length(trim(body)) > 0),
  rationale       TEXT,
  alternatives    TEXT,
  consequences    TEXT,
  topic_key       TEXT,
  confidence      TEXT NOT NULL CHECK (confidence IN ('confirmed','inference','hypothesis','proposal','unknown')),
  status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','superseded','obsolete','needs_review')),
  review_after    TEXT,
  superseded_by   TEXT REFERENCES observations(id),
  duplicate_count INTEGER NOT NULL DEFAULT 1 CHECK (duplicate_count >= 1),
  last_seen_at    TEXT NOT NULL,
  content_hash    TEXT NOT NULL CHECK (content_hash LIKE 'sha256:%'),
  origin_ref      TEXT,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  CHECK (kind <> 'decision' OR (topic_key IS NOT NULL AND rationale IS NOT NULL AND length(trim(rationale)) > 0)),
  CHECK (status <> 'superseded' OR superseded_by IS NOT NULL),
  CHECK (status <> 'active' OR superseded_by IS NULL),
  UNIQUE (content_hash)
);

CREATE INDEX ix_observations_session ON observations (session_id, created_at);
CREATE INDEX ix_observations_kind_status ON observations (kind, status, created_at);
CREATE INDEX ix_observations_topic ON observations (topic_key, status);

CREATE TABLE checkpoints (
  id              TEXT NOT NULL PRIMARY KEY,
  session_id      TEXT NOT NULL REFERENCES sessions(id),
  agent_id        TEXT NOT NULL,
  source          TEXT NOT NULL CHECK (source IN ('agent','human','import')),
  goal            TEXT NOT NULL CHECK (length(trim(goal)) > 0),
  state           TEXT NOT NULL CHECK (length(trim(state)) > 0),
  next_steps      TEXT NOT NULL CHECK (length(trim(next_steps)) > 0),
  files           TEXT,
  open_questions  TEXT,
  status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','superseded','obsolete','needs_review')),
  superseded_by   TEXT REFERENCES checkpoints(id),
  content_hash    TEXT NOT NULL CHECK (content_hash LIKE 'sha256:%'),
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  CHECK (status <> 'superseded' OR superseded_by IS NOT NULL),
  CHECK (status <> 'active' OR superseded_by IS NULL),
  UNIQUE (content_hash)
);

CREATE INDEX ix_checkpoints_session ON checkpoints (session_id, created_at);
CREATE INDEX ix_checkpoints_status ON checkpoints (status, created_at);

CREATE TABLE preferences (
  id              TEXT NOT NULL PRIMARY KEY,
  session_id      TEXT REFERENCES sessions(id),
  agent_id        TEXT NOT NULL,
  source          TEXT NOT NULL CHECK (source IN ('agent','human','import')),
  pref_key        TEXT NOT NULL CHECK (length(trim(pref_key)) > 0),
  value           TEXT NOT NULL CHECK (length(trim(value)) > 0),
  rationale       TEXT,
  status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','superseded','obsolete','needs_review')),
  review_after    TEXT,
  superseded_by   TEXT REFERENCES preferences(id),
  content_hash    TEXT NOT NULL CHECK (content_hash LIKE 'sha256:%'),
  origin_ref      TEXT,
  created_at      TEXT NOT NULL,
  updated_at      TEXT NOT NULL,
  CHECK (status <> 'superseded' OR superseded_by IS NOT NULL),
  CHECK (status <> 'active' OR superseded_by IS NULL),
  UNIQUE (content_hash)
);

CREATE INDEX ix_preferences_key ON preferences (pref_key, status);
CREATE INDEX ix_preferences_status ON preferences (status, created_at);
```

## 5. Integridad, deduplicación y restricciones

### 5.1 Restricciones y su efecto

| Restricción | Efecto observable | Origen |
|---|---|---|
| `TEXT NOT NULL PRIMARY KEY` | identidad única y navegable; sin NULL en la clave | SQLite + db-ai |
| FK `session_id → sessions(id)` | no se crea conocimiento huérfano; sesión inexistente devuelve `E_CONSTRAINT` | SQLite + db-ai |
| FK `superseded_by` hacia el `id` de la misma tabla | la sustitución apunta a una fila existente | SQLite |
| `UNIQUE(content_hash)` | dos capturas exactamente iguales no crean dos filas; la segunda devuelve `E_CONSTRAINT` | SQLite + db-ai |
| `UNIQUE(name)` en `meta` | una sola fila por clave de metadatos | SQLite |
| `CHECK` de enumeraciones | `source`, `kind`, `status`, `confidence` y estado de sesión quedan en valores conocidos | SQLite |
| `CHECK` de decisión | `kind = 'decision'` exige `topic_key` y `rationale` no vacío | SQLite |
| `CHECK` de sustitución | `status = 'superseded'` exige `superseded_by`; `status = 'active'` lo prohíbe | SQLite |
| `CHECK` de no vacío | título, cuerpo, objetivo, estado, próximos pasos, clave y valor no pueden ser cadenas vacías | SQLite |
| FK sin cascadas | `rm` no propaga borrados; la política lo deniega de todos modos | SQLite + db-ai |

No hay `ON DELETE CASCADE` ni `SET NULL`: una fila sustituida o un checkpoint no debe desaparecer por borrar otra fila. Como `rm` está denegado por política, la integridad referencial se mantiene por diseño y por el orden de las operaciones.

### 5.2 Algoritmo de `content_hash`

El hash es una convención de la skill, pero su unicidad es una garantía del esquema. Todos los productores deben calcularlo igual:

1. Normalizar cada campo de texto: Unicode NFC, recortar espacios al inicio y al final, colapsar whitespace interno a un espacio.
2. Serializar un array JSON canónico con los campos semánticos, en orden fijo y sin espacios. Los campos ausentes se representan como cadena vacía. Los campos JSON se guardan ya compactos.
3. Calcular SHA-256 sobre los bytes UTF-8 del array.
4. Escribir `sha256:` seguido del hexadecimal en minúsculas.

Campos por tabla:

| Tabla | Campos del array |
|---|---|
| `observations` | `kind`, `title`, `body`, `rationale`, `alternatives`, `consequences`, `topic_key` |
| `checkpoints` | `goal`, `state`, `next_steps`, `files`, `open_questions` |
| `preferences` | `pref_key`, `value`, `rationale` |

`meta` y `sessions` no usan hash: sus filas se identifican por UUID y no se deduplican por contenido. Un cambio de contenido en una fila existente debe actualizar `content_hash` en la misma operación de `write`; si no, la garantía de unicidad deja de representar el contenido.

### 5.3 Deduplicación exacta y duplicados semánticos

- **Exacta (determinista):** el `UNIQUE(content_hash)` impide la segunda fila. La skill interpreta `E_CONSTRAINT` como duplicado conocido, busca la fila por `content_hash` y, en `observations`, actualiza `duplicate_count` y `last_seen_at` con ETag. En `checkpoints` y `preferences`, el duplicado es un no-op. No se reintenta la inserción. Consecuencia: el mismo contenido exacto no se inserta dos veces, ni siquiera después de una sustitución; una revalidación idéntica se documenta sobre la fila existente o se captura como variante con contexto nuevo.
- **Semántica (convención):** no hay embeddings ni ranking. La skill usa `find` por `topic_key`, `kind` y `status`, y `grep` literal, para detectar variantes. Si no puede decidir, marca `needs_review` y no crea una segunda decisión activa. No se inventa una relación estructural nueva: la nota de revisión va en `body` o en `rationale`.

### 5.4 Protocolo de sustitución

Para sustituir una decisión, un checkpoint o una preferencia:

1. Leer la fila anterior y guardar su ETag.
2. Preparar un `batch` con dos operaciones: `touch` de la fila nueva y `write` de la anterior con `status = 'superseded'`, `superseded_by` igual al id de la fila nueva y `updated_at`.
3. Ordenar el lote: primero la inserción y después la actualización. La actualización referencia una fila preexistente; no se encadena sobre una fila creada en el mismo lote.
4. Ejecutar el lote; si falla, revierte completo.
5. Verificar con una lectura: exactamente una fila activa para el tema, la sesión o la clave.

El `if_match` de la fila anterior es el punto de serialización. Dos agentes que intenten sustituir la misma fila compiten por el mismo ETag: el primero confirma y cambia el ETag; el segundo recibe `E_CONFLICT` y revierte su inserción. Así no quedan dos sustituciones confirmadas.

**Límite conocido:** si no existe fila activa previa (primera captura de un tema, de una sesión o de una clave), dos agentes concurrentes pueden insertar dos filas activas. No hay una restricción única parcial que lo impida sin romper el lote: un índice único parcial sobre “activo” evaluaría la inserción antes de la actualización de la fila anterior y haría fallar el lote, y db-ai no permite encadenar ni diferir la restricción. La skill verifica después de cada captura y, si encuentra dos activas, las marca `needs_review` y registra una decisión de reconciliación.

Por la misma razón no se usan índices únicos parciales para “una activa por tema, sesión o clave”. La invariante se mantiene por el protocolo transaccional y la verificación, no por el esquema.

### 5.5 Triggers y reglas no ejecutables

No se usan triggers: db-ai rechaza triggers de escritura sobre la tabla objetivo (DESIGN de db-ai §8). En consecuencia, no hay generación automática de hash, promoción automática de estados ni caducidad automática. Todo eso es convención de la skill, verificable después de escribir, no una garantía del motor.

## 6. Política de acceso db-ai

### 6.1 Política concreta

El montaje `mem` se configura en `rw` para captura y recuperación, y en `ro` para observadores. La política materializa el requisito FR-13 del PRD de db-ai y se expresa con la forma de DESIGN de db-ai §9. Autoriza por prefijo, comparando segmentos:

| Prefijo | Operaciones permitidas | Uso |
|---|---|---|
| `/mem` | `ls`, `tree`, `cat`, `describe`, `stat`, `find`, `grep`, `export` | descubrir, leer y buscar todas las tablas |
| `/mem/sessions` | `touch`, `write` | abrir y cerrar sesión |
| `/mem/observations` | `touch`, `write` | capturar, actualizar estado y metadatos |
| `/mem/checkpoints` | `touch`, `write` | capturar y sustituir checkpoints |
| `/mem/preferences` | `touch`, `write` | crear y sustituir preferencias |
| `/mem/meta` | ninguna escritura | metadatos solo por bootstrap o migración revisada |

No se conceden `cp`, `mv` ni `rm` en ningún prefijo. `batch` no es una operación de política separada: cada elemento se comprueba con su propia operación (DESIGN de db-ai §8; skill `db-ai`, fase de lote). Un lote con `rm` o con una ruta fuera de los prefijos anteriores se deniega y revierte.

Las operaciones que aparecen en `ops` son nombres de comando documentados en DESIGN de db-ai §3 (`touch`, `write`, `cp`, `mv`, `rm`) y su semántica de mutación está en §8; la forma de la política y la comparación por segmentos están en §9. El vocabulario completo de `ops` no está enumerado en §9 (C-ACM-06), pero ACM usa solo nombres documentados y no inventa operaciones.

La forma exacta de la configuración y su fichero pertenecen a DESIGN de db-ai §9; este documento fija la política, no la sintaxis del fichero. `mount` y `umount` siguen siendo del operador.

### 6.2 Consecuencias de la política

- **No hay borrado ni renombrado por db-ai.** La obsolescencia es un cambio de estado; la identidad UUID permanece estable como ruta. Si el operador necesita podar físicamente, lo hace fuera de db-ai, con copia previa y revisión; la skill no lo hace.
- **La política no es una frontera de seguridad.** Un proceso con acceso al fichero o a la configuración puede saltarla. La separación real exige permisos de sistema operativo (PRD de db-ai §8).
- **La política es por ruta, no por columna ni por tiempo.** Permitir `write` en `/mem/observations` permite escribir cualquier campo de cualquier fila de esa tabla. La inmutabilidad de `agent_id`, `session_id`, `created_at` y demás campos de procedencia es una convención de la skill, no una garantía de db-ai; `content_hash` solo cambia cuando cambia el contenido, en la misma operación.

### 6.3 Sesiones: lectura después de abrir

La política no puede expresar “escribir solo al abrir y al cerrar” porque no distingue momentos. Se permite `touch` y `write` en `/mem/sessions`; la skill aplica la disciplina:

- `touch` al abrir, con `goal`, `agent_id`, `source`, `started_at`.
- lecturas durante el trabajo.
- `write` al cerrar, limitado a `status`, `ended_at` y `summary`.

No se usa la sesión `--session` de db-ai para la memoria: esa sesión solo mantiene el `cwd` de `cd`/`pwd`. Las rutas de memoria son absolutas.

## 7. Mapeo de operaciones de memoria a db-ai

| Operación de memoria | Operación db-ai | Garantía usada | Convención de la skill |
|---|---|---|---|
| Descubrir tablas y columnas | `mounts`, `ls`, `describe`, `stat` | catálogo y capacidades | parar cuando la tabla está identificada |
| Leer una fila o un campo | `cat` sobre la ruta | identidad y ETag | leer solo lo necesario |
| Filtrar por columnas | `find` con `--where` y límite | operadores documentados, orden por PK | elegir filtros selectivos y página corta |
| Buscar texto | `grep` literal con límite | subcadena sensible a mayúsculas | usar términos distintivos; no esperar ranking |
| Exportar para análisis | `export` por stdout | mismos filtros y límites | no volcar tablas al contexto del agente |
| Insertar una fila | `touch` | INSERT sin precondición | calcular `id` y `content_hash` antes |
| Actualizar estado o metadatos | `write` con `if_match` | UPDATE con ETag | no cambiar PK ni procedencia; actualizar hash si cambia contenido |
| Varias filas en una transacción | `batch` | una transacción, rollback total, máximo 100 | insertar antes de sustituir; no encadenar |
| Eliminar | `rm` | DELETE con ETag y `--apply` | denegado por política; usar estado `obsolete` |
| Renombrar | `mv` | PK simple y `--apply` | denegado; los UUID son estables |
| Copiar | `cp` | copia dentro de la tabla | denegado; una repetición se deduplica o se captura como nueva |
| Contexto de sesión de ACM | no usar `--session` | sesiones de db-ai solo para `cd`/`pwd` | la sesión de ACM es una fila de `sessions` |
| Auditoría | auditoría local de db-ai | operación, actor, recurso y resultado | no duplicar el log; usar procedencia de las filas |

## 8. Flujos operativos

### 8.1 Abrir y cerrar sesión

1. Generar un UUIDv7 para la sesión.
2. `touch` en `/mem/sessions` con `agent_id`, `source`, `goal`, `started_at`, `status = 'open'` y marcas temporales.
3. Verificar con `cat` y conservar el ETag si se cerrará después.
4. Al terminar, `write` sobre la sesión con `status = 'closed'`, `ended_at` y `summary`, usando el ETag vigente.
5. Si la sesión se abandona, `status = 'abandoned'` y un checkpoint que explique el estado.

### 8.2 Capturar una observación

1. Decidir si el hallazgo es durable y reutilizable; si no, no capturar.
2. Elegir `kind` y `confidence`; calcular `content_hash` (§5.2).
3. `touch` en `/mem/observations` con `id`, `session_id`, procedencia, título, cuerpo y metadatos.
4. Si devuelve `E_CONSTRAINT` por `content_hash`, buscar la fila existente con `find --where 'content_hash=...'` y actualizar `duplicate_count` y `last_seen_at` con ETag. No reintentar la inserción.
5. Verificar con `cat` que la fila y el ETag son los esperados.

### 8.3 Capturar una decisión y sustituir la anterior

1. Buscar la decisión activa del tema: `find` por `kind = 'decision'`, `topic_key` y `status = 'active'`.
2. Si existe, leerla y guardar su ETag.
3. Calcular el hash de la nueva decisión; incluir `topic_key` y `rationale` no vacío. Si el hash coincide con una fila existente, no crear otra: es la misma decisión o falta contexto que la distinga; resolverlo antes de preparar el lote.
4. Preparar el `batch` de §5.4: `touch` de la nueva decisión y `write` de la anterior a `superseded` con `superseded_by`.
5. Ejecutar y verificar: una sola activa para el tema, la anterior conservada con su rationale y su enlace.
6. Si no existía una activa, insertar la nueva y verificar que no apareció otra activa en paralelo; si apareció, reconciliar (§5.4).

### 8.4 Capturar un checkpoint

1. Generar UUIDv7 y calcular el hash con `goal`, `state`, `next_steps`, `files` y `open_questions`. Si el hash coincide con un checkpoint existente, no crear otro: documentar la revalidación o ajustar el estado para que refleje el cambio.
2. Buscar el checkpoint activo de la sesión.
3. Si existe, `batch` con `touch` del nuevo y `write` del anterior a `superseded`.
4. Verificar que solo uno queda activo.
5. Los `files` son rutas relativas al repositorio; no se guardan rutas absolutas de la máquina.

### 8.5 Crear o sustituir una preferencia

1. Normalizar `pref_key` (familia/descripción, minúsculas, kebab-case).
2. Buscar la preferencia activa de la clave.
3. Si el valor cambia, `batch` con `touch` de la nueva y `write` de la anterior a `superseded`.
4. Si el valor es idéntico, no crear otra fila: el hash la deduplica.
5. Verificar por clave y estado activo.

### 8.6 Reanudar

1. Acotar la búsqueda del checkpoint antes de pedirla: `find` ordena por PK ascendente y no garantiza el más reciente en la primera página (C-ACM-03). Con `session_id` conocido (handoff o reanudación de una sesión concreta), `find` de checkpoints por `session_id` y `status = 'active'`, con límite corto; el UUIDv7 mayor de esa página es el vigente. Sin sesión conocida, `find` de sesiones por `started_at` dentro de una ventana acotada (por defecto, los últimos 14 días) y límite corto; si la respuesta trae `truncated`, acortar el intervalo y repetir, sin recorrer páginas; con la ventana en memoria, ordenar localmente por UUIDv7 mayor y tomar la primera sesión con checkpoint activo.
2. `find` del checkpoint activo de esa sesión.
3. `find` de decisiones activas solo del `topic_key` de la tarea en curso, con límite 5. Sin `topic_key` conocido, página mínima (límite 3) y declarar el recorte de `truncated`.
4. `find` de preferencias por `pref_key` aplicable, límite 2 por clave y como máximo tres claves; sin claves conocidas, página mínima (límite 3).
5. Leer solo el detalle que la pregunta exige; no exportar tablas. El historial de la sesión no forma parte del guion: se pide si no hay checkpoint o si la pregunta lo requiere.
6. Respetar los presupuestos de §10.

Exigir la continuidad del mismo agente solo cuando la persona o el encargo lo pidan: añadir `agent_id` a los filtros de sesión y checkpoint y, si no hay checkpoint de ese agente, declararlo en vez de adoptar el de otro en silencio.

### 8.7 Revisar y marcar obsolescencia

1. Detectar filas `needs_review`, `review_after` vencido o conocimiento contradicho.
2. Releer la fila y su ETag.
3. Decidir: sigue vigente (volver a `active` con nueva `review_after`), fue sustituida (`superseded` + enlace) o ya no es válida (`obsolete`).
4. `write` del estado con `if_match`; si cambia contenido, actualizar `content_hash`.
5. Verificar con `cat` y no borrar la fila.

## 9. Consolidación y obsolescencia

### 9.1 Qué se captura

| Se captura | No se captura |
|---|---|
| conocimiento verificado y reutilizable | conversaciones, prompts o salidas de herramientas completas |
| decisiones con justificación y alternativas | cada paso rutinario o cada invocación |
| checkpoints con estado y próximos pasos | historial de actividad sin valor de reanudación |
| preferencias operativas con motivo | secretos, credenciales o datos personales |
| descubrimientos con grado de certeza | especulación presentada como hecho |

La captura es deliberada y ocurre al abrir, al cerrar, al cambiar una decisión, al alcanzar un hito o al detectar una preferencia. No es un registro automático de eventos.

### 9.2 Estados y transiciones

| Estado | Significado | Transiciones permitidas |
|---|---|---|
| `active` | vigente y reutilizable | a `superseded` si hay reemplazo; a `obsolete` si deja de ser válido; a `needs_review` si es dudoso o vencido |
| `superseded` | reemplazado por otra fila | terminal; conserva `superseded_by` |
| `obsolete` | ya no válido, sin reemplazo | terminal; se conserva por auditoría |
| `needs_review` | dudoso, conflictivo o pendiente de revisión | a `active` si se confirma; a `superseded` u `obsolete` si se resuelve |

Una fila `superseded` exige `superseded_by`; una `active` lo prohíbe. Una fila obsoleta o sustituida no se reactiva en su sitio. Si el conocimiento vuelve a ser válido con contexto o justificación nuevos, se captura una fila nueva con UUID nuevo y un hash distinto, y se enlaza la historia. Si el contenido es exactamente idéntico, el `UNIQUE(content_hash)` impide duplicarlo: la revalidación se documenta sobre la fila existente o se deja constancia de que no hay una nueva decisión activa. Esto preserva la trazabilidad y evita reescribir el pasado.

`review_after` es una convención, no una caducidad automática. Propuesta inicial, inspirada en la revisión por tipo de Engram pero no copiada: preferencias a tres meses; `discovery`, `pattern` y `decision` a seis meses; `architecture` y `bugfix` a doce meses; `learning` sin plazo por defecto. La skill informa la fecha y puede marcar `needs_review`; no borra ni cambia el estado por el mero paso del tiempo. Los valores concretos son una pregunta abierta del PRD §14.

### 9.3 Determinista frente a convención

| Aspecto | Determinista | Convención de la skill |
|---|---|---|
| Duplicado exacto | `UNIQUE(content_hash)`; segunda inserción `E_CONSTRAINT` | cómo recuperar y actualizar metadatos |
| Duplicado semántico | no hay detección automática | comparar por `topic_key`, `kind` y texto; marcar `needs_review` |
| Sustitución | lote atómico, ETag de la fila anterior, `CHECK` del enlace | elegir el momento y verificar una sola activa |
| Una activa por tema, sesión o clave | protocolo de §5.4 y verificación posterior | primera captura concurrente se reconcilia |
| Obsolescencia | `CHECK` y FK; no hay borrado por db-ai | decidir `obsolete` o `needs_review` |
| Revisión temporal | `review_after` es un dato | cadencia, umbral y acción |
| Hash | formato y unicidad | canonicalización y campos |
| Procedencia | FK y columnas obligatorias | no reescribir campos de procedencia |

## 10. Recuperación selectiva

### 10.1 Patrones de consulta

| Pregunta | Consulta | Límite |
|---|---|---|
| ¿Qué sesiones están abiertas? | `find` en `sessions` por `agent_id` y `status` | 20 |
| ¿Cuál es la última? | elegir el UUIDv7 mayor de la ventana acotada de sesiones | no aplica |
| ¿Dónde quedamos? | `find` en `checkpoints` por `session_id` y `status = 'active'`; sin sesión conocida, la sesión más reciente de la ventana por `started_at` | 5 |
| ¿Qué decidimos sobre T? | `find` en `observations` por `kind = 'decision'`, `topic_key` y `status` | 10 |
| ¿Qué sabemos de un término? | `grep` en `observations` con término distintivo | 20 |
| ¿Qué preferencias rigen? | `find` en `preferences` por `status`, o por `pref_key` | 20 |
| ¿Qué pasó en esta sesión? | `find` en `observations` por `session_id` | 50 |
| ¿Qué hay en la base? | `describe` y `stat`; no `export` completo | no aplica |

`find` devuelve filas completas y ordena por PK ascendente. Para la recencia se usa el UUIDv7 mayor de una ventana acotada de `started_at` —con corrección del corte si `truncated`—, no una ordenación descendente que db-ai no expone. El checkpoint se busca por `session_id`; solo sin sesión conocida se busca primero la sesión en la ventana. `grep` devuelve la ruta de la fila, la columna y la posición; después se lee el campo con `cat`.

El guion por defecto está acotado (protocolos de la skill, §1.4 y §3.6): decisiones solo del `topic_key` de la tarea en curso y preferencias solo por las `pref_key` aplicables. Con `session_id` conocido son cuatro `find`; en frío, la ventana de sesiones añade el quinto. La medición está en [evidence-benchmark.md](../scenarios/evidence/evidence-benchmark.md) §8.

### 10.2 Divulgación progresiva

1. Localizar con `find` o `grep` y un límite corto.
2. Elegir una fila por su ruta y su identificador.
3. Leer el campo necesario con `cat`; no releer la fila completa si basta un campo.
4. Si la respuesta no está en la primera página, usar el cursor; no pedir la tabla completa.
5. `export` se reserva para análisis fuera del contexto del agente, no para responder una pregunta.

### 10.3 Presupuesto y tamaño de texto

Objetivos de M-ACM-01 a M-ACM-04 del PRD §7. Convenciones de tamaño para que una página de resultados no desborde el contexto:

| Campo | Tope propuesto | Si se supera |
|---|---|---|
| `title` | 160 caracteres | usar un título más corto y mover detalle a `body` |
| `body` | 2 000 caracteres | resumir; el detalle externo va en `origin_ref` |
| `rationale` | 1 000 caracteres | resumir el motivo principal |
| `alternatives` y `consequences` | 1 000 caracteres cada uno | listar solo las relevantes |
| `goal` | 500 caracteres | expresar el objetivo en una frase |
| `state` | 2 000 caracteres | resumir el estado |
| `next_steps` | 1 000 caracteres | listar pasos accionables |
| `summary` | 1 000 caracteres | resumir el cierre de la sesión |
| `open_questions` | 1 000 caracteres | listar solo las preguntas abiertas |
| `value` de preferencia | 500 caracteres | expresar la regla de forma directa |

Los topes son convenciones de la skill; el esquema no los impone. No hay proyección de columnas en `find`/`export` (C-ACM-03), así que el control de contexto depende del límite de filas y del tamaño del texto.

## 11. Concurrencia y recuperación

| Situación | Garantía | Acción de la skill |
|---|---|---|
| ETag desactualizado | db-ai compara `if_match` dentro de la transacción y devuelve `E_CONFLICT` | releer la fila; decidir con el estado nuevo; no reutilizar el ETag |
| Varias filas en una transacción | `batch` revierte todo si una operación falla | usar un lote; no encadenar sobre filas creadas en el mismo lote |
| Resultado de commit incierto | `E_COMMIT_UNKNOWN` no dice si el commit se aplicó | leer por `id` o buscar por `content_hash`; si el cambio está, verificar; si no, reintentar con el ETag vigente |
| Contención | `E_BUSY` o `E_TIMEOUT` | reducir alcance; un reintento tras observar; no subir el plazo a ciegas |
| Dos agentes en la misma fila | el ETag serializa la escritura | el segundo recibe conflicto; no hay actualización perdida |
| Dos primeras capturas del mismo tema | no hay restricción única parcial | verificar después; si hay dos activas, marcar `needs_review` y reconciliar |
| Sesión de db-ai frente a sesión de ACM | son cosas distintas | no usar `--session` para memoria; usar la tabla `sessions` |

`E_COMMIT_UNKNOWN` se reconcilia siempre leyendo. Con un `touch`, el `id` UUIDv7 lo conoce el productor: se lee la ruta; si existe, el commit se aplicó. Si no existe, se reintenta con el mismo `id` tras confirmar la ausencia, o se informa la incertidumbre si la lectura no es concluyente. No se reintenta a ciegas (skill `db-ai`, `references/errores.md`).

La auditoría de db-ai registra intento y resultado. ACM no promete atomicidad entre la memoria y el log de auditoría: son dos sistemas, igual que en db-ai. La procedencia de las filas es la trazabilidad duradera de ACM.

No se fija WAL. La contención se resuelve con transacciones de escritura y tiempos límite; si el escenario multiproceso lo exige, se evalúa WAL en una decisión posterior con prueba de bloqueo y empaquetado.

## 12. Trazabilidad requisito ↔ diseño ↔ decisión

| Requisito | Secciones del DESIGN | Decisiones |
|---|---|---|
| FR-ACM-01 | §3, §6, §8.1 | D-ACM-01, D-ACM-11 |
| FR-ACM-02 | §4.3, §8.1 | D-ACM-06, D-ACM-12 |
| FR-ACM-03 | §4.4, §5.2, §8.2 | D-ACM-05, D-ACM-06, D-ACM-07 |
| FR-ACM-04 | §4.4, §5.4, §8.3 | D-ACM-05, D-ACM-08, D-ACM-10 |
| FR-ACM-05 | §4.5, §5.4, §8.4 | D-ACM-06, D-ACM-10 |
| FR-ACM-06 | §4.6, §5.4, §8.5 | D-ACM-06, D-ACM-08 |
| FR-ACM-07 | §10 | D-ACM-15 |
| FR-ACM-08 | §5, §9 | D-ACM-07, D-ACM-08, D-ACM-10 |
| FR-ACM-09 | §11 | D-ACM-09, D-ACM-10 |
| FR-ACM-10 | §4.1, §5.1, §6.2 | D-ACM-06, D-ACM-12 |
| NFR-ACM-01 | §1, §2, §14 | D-ACM-03, D-ACM-04, D-ACM-14 |
| NFR-ACM-02 | §4.1, §5, §11 | D-ACM-02, D-ACM-07 |
| NFR-ACM-03 | §3, §9.1 | D-ACM-01, D-ACM-13 |
| NFR-ACM-04 | §10, §15 | D-ACM-15 |
| NFR-ACM-05 | §4, §6, §14 | D-ACM-04, D-ACM-09, D-ACM-11 |
| NFR-ACM-06 | §1, §2 | D-ACM-04, D-ACM-14, D-ACM-17 |
| NFR-ACM-07 | §3.3, §13 | D-ACM-06, D-ACM-16 |

## 13. Decisiones de diseño

| ID | Estado | Decisión | Alternativas descartadas | Consecuencia | Requisitos |
|---|---|---|---|---|---|
| D-ACM-01 | aprobada | una base SQLite por proyecto en `.agent-memory/memory.db`, alias `mem` | base global o una base por sesión | aislamiento; sin memoria entre proyectos | FR-ACM-01, NFR-ACM-03 |
| D-ACM-02 | aprobada | PK UUIDv7 textual y no nula en cada fila | UUIDv4 aleatorio; entero autoincremental | orden temporal y rutas estables; el productor genera el UUID | NFR-ACM-02 |
| D-ACM-03 | aprobada | la skill se compone sobre db-ai; no hay motor ni CLI nuevos | servidor MCP, CLI de memoria, daemon | depende de las capacidades de db-ai; convenciones en la skill | NFR-ACM-01 |
| D-ACM-04 | aprobada | base db-ai v0.2.0 | versión futura o parche local | compatibilidad fijada por contrato | NFR-ACM-01, NFR-ACM-05, NFR-ACM-06 |
| D-ACM-05 | propuesta | las decisiones son `observations` con `kind = 'decision'` | tabla `decisions` separada | una búsqueda y un ciclo de vida; columnas de decisión compartidas | FR-ACM-03, FR-ACM-04 |
| D-ACM-06 | propuesta | tablas `meta`, `sessions`, `observations`, `checkpoints`, `preferences` | una tabla única para todo; tablas por tipo | modelo pequeño y consultable; recuperación por tabla | FR-ACM-02, FR-ACM-03, FR-ACM-04, FR-ACM-05, FR-ACM-06, FR-ACM-08, FR-ACM-10, NFR-ACM-07 |
| D-ACM-07 | propuesta | `content_hash` calculado por el productor y `UNIQUE` en el esquema | triggers de hash; deduplicación semántica | dedup exacta determinista; variantes manuales | FR-ACM-03, FR-ACM-08, NFR-ACM-02 |
| D-ACM-08 | propuesta | estados `active`, `superseded`, `obsolete`, `needs_review` y `review_after` | borrado lógico por fecha; bandera `deleted` | ciclo de vida explícito; la historia no se borra | FR-ACM-04, FR-ACM-06, FR-ACM-08 |
| D-ACM-09 | propuesta | sin triggers; hash, promoción y revisión son convenciones verificadas | triggers de escritura; capa de aplicación | el esquema no necesita triggers; las invariantes se verifican después | FR-ACM-09, NFR-ACM-05 |
| D-ACM-10 | propuesta | sustitución atómica en un lote; sin índices únicos parciales | índice parcial sobre “activo”; dos transacciones | el lote funciona con db-ai; la primera captura concurrente se reconcilia | FR-ACM-04, FR-ACM-05, FR-ACM-08, FR-ACM-09 |
| D-ACM-11 | propuesta | lectura total; escritura solo en sesiones, observaciones, checkpoints y preferencias; sin `cp`, `mv` ni `rm` | escritura en todas las tablas; política por columna | evita borrado y renombrado accidentales; la política no es seguridad | FR-ACM-01, NFR-ACM-05 |
| D-ACM-12 | propuesta | sesiones escribibles al abrir y cerrar; lectura durante el trabajo | prohibir `write` en sesiones; política dinámica | el cierre es posible; la disciplina temporal es convención | FR-ACM-02, FR-ACM-10 |
| D-ACM-13 | aprobada | no se almacenan conversaciones completas; solo conocimiento consolidado | registro bruto de sesión; prompts completos | privacidad y contexto; se pierde detalle no consolidado | NFR-ACM-03 |
| D-ACM-14 | aprobada | sin embeddings, MCP, CLI nueva ni dependencia LLM | búsqueda vectorial; servidor de memoria | composición mínima; recall limitado a filtros y texto | NFR-ACM-01, NFR-ACM-06 |
| D-ACM-15 | propuesta | recuperación con UUIDv7, filtros, límites y divulgación progresiva | export completo; ranking semántico | contexto acotado; recencia por UUID mayor | FR-ACM-07, NFR-ACM-04 |
| D-ACM-16 | propuesta | DDL y `schema_version` fuera de db-ai; migraciones manuales revisadas | DDL desde db-ai; migrador automático | bootstrap explícito; evolución controlada | NFR-ACM-07 |
| D-ACM-17 | propuesta | local-first, sin red; copia de seguridad del operador | sincronización nube; backup automático | privacidad y portabilidad; la copia no es automática | NFR-ACM-06 |

Notas de las decisiones con más matiz:

- **D-ACM-05.** Una tabla `decisions` separada daría columnas más limpidas, pero multiplicaría la búsqueda (`grep` por tabla), la deduplicación y el ciclo de vida. Engram usa observaciones con tipos; ACM adopta esa forma sin copiar su implementación. Las decisiones conservan `rationale`, `alternatives` y `consequences` y un `CHECK` que exige tema y motivo.
- **D-ACM-09 y D-ACM-10.** Los triggers y los índices únicos parciales parecen la forma natural de imponer “una activa por tema”. Ambos chocan con db-ai: los triggers de escritura se rechazan y el índice parcial evalúa la inserción de la fila nueva antes de que el lote sustituya la anterior, haciendo fallar la transacción. La invariante se mantiene por el ETag de la fila anterior y la verificación posterior.
- **D-ACM-12.** La política por prefijo no distingue “después de abrir”. Se permite escribir en sesiones para poder cerrarlas; la skill limita los campos y el momento. La alternativa de prohibir `write` dejaría sesiones abiertas para siempre.

## 14. Carencias de db-ai detectadas

Estas carencias son propuestas de evolución independientes. Salvo la nota de estado de C-ACM-01, no se implementan aquí. Se documentan porque ACM las encuentra al componer sobre el contrato de db-ai v0.2.0.

### C-ACM-01 — Sin comando ni flag de versión

**Evidencia observada:** `db-ai version` devuelve `E_USAGE` con `unknown command "version"` y código de salida 2; `db-ai --version` devuelve `E_USAGE` con `unknown flag "--version"` y código de salida 2. El contrato CLI de DESIGN de db-ai §3 no define ninguno de los dos.

**Impacto en ACM:** no se puede confirmar en tiempo de ejecución que el binario instalado es v0.2.0. La versión se fija por contrato y despliegue, y `schema_version` cubre el esquema, no la CLI.

**Propuesta:** añadir `version` o `--version` a db-ai, o incluir la versión en `mounts`/`meta` de una operación existente. Es una propuesta de evolución, no una ampliación de ACM.

**Estado (2026-11-19):** aprobada por el PO e implementada en el árbol como `db-ai version`, que emite `{version, commit, state}` con el sobre JSON estándar; el contrato está en DESIGN de db-ai §3. **Actualización (2026-11-20):** cometida y publicada con la v0.3.0 (commit `9f1bcfd`); el binario de la release responde `version` con el sobre estándar, y `commit` queda `unknown` en builds sin `ldflags`.

### C-ACM-02 — Sin búsqueda difusa, FTS ni ranking

**Evidencia:** DESIGN de db-ai §7 define `grep` como subcadena literal sensible a mayúsculas, sin FTS automático; la skill local `db-ai` (`references/comandos.md`, filtros) confirma que no hay expresiones regulares ni ranking. No hay embeddings ni base vectorial en el MVP.

**Impacto en ACM:** el recall depende de términos exactos y del orden de las filas; los duplicados semánticos no se detectan solos. La recuperación usa filtros, `topic_key` y revisión manual.

**Propuesta:** un modo de búsqueda FTS5 o trigramas de solo lectura, o una operación de búsqueda con ranking, fuera del alcance de ACM.

**Estado (2026-11-20):** aplazada por el PO. La recuperación actual con filtros, `topic_key` y revisión manual es adecuada; se retomará cuando el recall observado en uso real justifique FTS o ranking. Sin fecha prevista.

### C-ACM-03 — Sin proyección, orden descendente ni recuento en la CLI

**Evidencia:** `db-ai find --help` y `db-ai export --help` exponen `--where`, `--limit`, `--cursor` y `--format`, pero no campos, orden ni recuento; DESIGN de db-ai §3 no los incluye, aunque el tipo `Query` de §6 tiene `Projection` y `Order`. `find` ordena por PK ascendente; no hay `--order` descendente.

**Impacto en ACM:** `find` devuelve filas completas, lo que consume contexto; la recencia se calcula con el UUIDv7 mayor de una ventana acotada; no hay conteos baratos. El DESIGN compensa con límites y topes de texto.

**Propuesta:** `--fields`, `--order`/`--sort` y `--count` (o un `stat` con recuento), manteniendo el orden determinista y los límites. Fuera de ACM.

**Estado (2026-11-20):** aceptada por el PO para una evolución futura de db-ai. Motivo: es la propuesta de mayor rentabilidad —ahorro de contexto en `find` y recencia más barata— con riesgo bajo, pues respeta el orden determinista y los límites existentes. Implementación pendiente como trabajo nuevo después de cerrar la épica tk-20.

### C-ACM-04 — Sin upsert ni inserción condicional

**Evidencia:** `touch` es INSERT y una clave única duplicada devuelve `E_CONSTRAINT` (DESIGN de db-ai §8; observado en §15). No hay `touch --if-absent` ni `upsert`.

**Impacto en ACM:** una repetición exacta requiere invocaciones adicionales: `touch` falla, la skill busca por hash y actualiza metadatos con `write`, y verifica. Es determinista, pero cuesta más que una inserción condicional.

**Propuesta:** una semántica de inserción condicional o de upsert por clave única, con resultado explícito de “creado” o “existente”. Fuera de ACM.

**Estado (2026-11-20):** aceptada por el PO para una evolución futura de db-ai. Motivo: el ACM compensa hoy la idempotencia con `touch` + búsqueda por hash + `write` + verificación, determinista pero costoso; el resultado explícito de “creado” o “existente” simplifica el flujo sin ambigüedad. Implementación pendiente como trabajo nuevo después de cerrar la épica tk-20.

### C-ACM-05 — Sin vistas ni búsqueda entre tablas

**Evidencia:** DESIGN de db-ai §5 excluye vistas del MVP (decisión D-20); `find` y `grep` reciben una ruta de tabla, no una raíz con varias tablas (skill local `db-ai`, `references/comandos.md`).

**Impacto en ACM:** recuperar conocimiento exige consultar `observations`, `checkpoints` y `preferences` por separado; no hay una superficie única de solo lectura. Por eso las decisiones viven con las observaciones.

**Propuesta:** vistas de solo lectura o una operación de búsqueda a nivel de alias; exigiría revisar la exclusión D-20 y un ADR propio. Fuera de ACM.

**Estado (2026-11-20):** aplazada por el PO. Motivo: entra en conflicto con una decisión de diseño aprobada (D-20) y exige un ADR propio; el coste de consulta por tabla no ha demostrado ser un problema en la práctica actual del ACM. Sin fecha prevista.

### C-ACM-06 — Vocabulario de `ops` de la política no enumerado por completo

**Evidencia:** DESIGN de db-ai §9 muestra operaciones de lectura y el ejemplo `["write"]`, pero no enumera el conjunto completo de operaciones mutantes. La implementación acepta nombres de comando (`touch`, `write`, `cp`, `mv`, `rm`) y comprueba cada elemento de un lote con su propia operación; la skill local `db-ai` describe la política por `ops` y `prefix` sin listar el vocabulario completo.

**Impacto en ACM:** un operador puede escribir una política incompleta o creer que `batch` necesita una operación propia. La política de §6 se apoya en el comportamiento observado, no en una lista contractual.

**Propuesta:** documentar en DESIGN de db-ai §9 el vocabulario completo de `ops`, incluida la comprobación por elemento del lote. Es una corrección documental, no una capacidad nueva.

**Estado (2026-11-20):** aceptada por el PO. Motivo: es documentación pura, el coste es mínimo y hoy un operador puede escribir una política incompleta por no tener el vocabulario contractual. Implementación pendiente como corrección en DESIGN de db-ai después de cerrar la épica tk-20.

### C-ACM-07 — Sin DDL ni creación de esquema

**Evidencia:** PRD de db-ai §2 y §8 excluyen crear tablas mediante la CLI; la skill local `db-ai` lo repite. db-ai monta una base existente y no altera el esquema.

**Impacto en ACM:** el bootstrap es una dependencia del operador: `.agent-memory/` y el DDL se crean fuera de db-ai. No hay una operación de ACM que inicialice la base sola.

**Propuesta:** ninguna sobre db-ai; el bootstrap de ACM se documenta en §3.3 y está implementado en `init.sh`. Si se quisiera una inicialización integrada, sería una herramienta de despliegue, no una ampliación del núcleo.

**Estado (2026-11-20):** rechazada por el PO. Motivo: es diseño deliberado del MVP —db-ai monta una base existente y no altera el esquema—, no una carencia; el bootstrap externo de ACM (`init.sh`) cubre la necesidad.

### C-ACM-08 — `describe` no expone las expresiones `CHECK`

**Evidencia:** DESIGN de db-ai §5 anuncia detección de restricciones, pero la salida observada de `describe` incluye columnas, PK, índices, claves foráneas, referencias entrantes, triggers, capacidades y limitaciones, y no incluye las expresiones `CHECK`. El contrato también declara que db-ai no analiza expresiones de restricciones mediante heurísticas inseguras.

**Impacto en ACM:** el agente descubre las reglas de decisión, sustitución y no-vacío por `E_CONSTRAINT` o por la skill, no por `describe`. La documentación del esquema debe ser explícita para que el error no sea la primera fuente.

**Propuesta:** exponer en `describe` un resumen seguro y acotado de las restricciones `CHECK`, o publicar el DDL como referencia junto al montaje. Fuera de ACM.

**Estado (2026-11-20):** aplazada por el PO. Motivo: parsear o resumir `CHECK` con seguridad es delicado por contrato, y la alternativa barata (publicar el DDL como referencia) merece decidirse junto con la evolución del contrato de `describe`. La skill documenta mientras tanto el esquema, y el `E_CONSTRAINT` sigue siendo la señal de ejecución. Sin fecha prevista.

## 15. Verificación

Las comprobaciones siguientes se ejecutaron sobre una base SQLite temporal fuera del repositorio y una configuración XDG temporal, sin tocar la configuración del usuario ni el repositorio.

### 15.1 Esquema

| Comprobación | Resultado observado |
|---|---|
| Aplicar el DDL de §4.8 | `sqlite3` termina con código 0 |
| Listar tablas | aparecen `checkpoints`, `meta`, `observations`, `preferences`, `sessions` |
| `PRAGMA foreign_key_check` | no devuelve filas; código 0 |
| `UNIQUE(content_hash)` con una segunda inserción | falla con `UNIQUE constraint failed`; código 19 de SQLite |
| FK con `session_id` inexistente | falla con `FOREIGN KEY constraint failed`; código 19 de SQLite |
| `superseded` sin `superseded_by` | falla con `CHECK constraint failed`; código 19 de SQLite |

### 15.2 Integración con db-ai

| Comprobación | Resultado observado |
|---|---|
| Montar la base como `mem` en `rw` | `mounts` muestra el alias y el modo |
| `describe /mem/observations` | `readable: true`, `writable: true`, `navigable: true`; sin limitaciones |
| `describe /mem/meta` | `readable: true`, `writable: false`, `navigable: true` |
| `find` de decisiones activas | devuelve rutas de fila con UUIDv7 y orden ascendente |
| `grep` literal | devuelve ruta de fila, columna y posición |
| `touch` de una observación | crea la fila y devuelve ruta y ETag |
| `write` de estado con `if_match` | actualiza y devuelve el ETag nuevo |
| `batch` de sustitución | inserta la nueva, marca la anterior y deja una sola activa |
| `rm` | `E_DENIED`; la política no lo permite |
| `cp` y `mv` | `E_DENIED`; la política no los permite |
| `touch` en `/mem/meta` | `E_DENIED`; metadatos de solo lectura |
| Observación con `content_hash` repetido | `E_CONSTRAINT`; no crea segunda fila |
| Observación con sesión inexistente | `E_CONSTRAINT`; no crea fila huérfana |
| Decisión sin `rationale` | `E_CONSTRAINT`; no crea la fila |

La comprobación de navegabilidad confirma la regla de §4.1: con `id TEXT NOT NULL PRIMARY KEY`, `describe` no reporta `primary key columns may contain NULL` y la tabla es navegable y escribible.

### 15.3 Límites de esta verificación

- ACM está implementado en el árbol (`init.sh`, `skill/` y escenarios) y se ejecutó. Las pruebas de esta sección miden el esquema y la integración con db-ai, no el cumplimiento espontáneo de cada convención de la skill.
- `db-ai version` existe en el árbol y se comporta según DESIGN de db-ai §3; el binario instalado puede no exponerlo hasta la release, y sin `ldflags` el `commit` es `unknown` (C-ACM-01).
- El rendimiento y el contexto se midieron sobre un fixture determinista de 10 240 observaciones y hardware declarado en [evidence-benchmark.md](../scenarios/evidence/evidence-benchmark.md): M-ACM-01, M-ACM-02 y M-ACM-04 se superan; M-ACM-03 se supera con `session_id` conocido (1 444 tokens) y falla en frío (9 058) por C-ACM-03. M-ACM-05 se probó con dos agentes reales ([evidence-agents.md](../scenarios/evidence/evidence-agents.md)): 5 de 5 por evaluador delegado, pendiente de ratificación humana. No es una prueba de producción.
- No se probó `E_COMMIT_UNKNOWN` de forma provocada; el protocolo de §11 sigue el contrato y la skill local `db-ai`.
- La concurrencia de `init.sh` con db-ai sobre `config.json` se probó con `flock` en la batería de pruebas. La primera captura simultánea de dos filas activas del mismo tema no se provocó: §5.4 sigue siendo razonamiento sobre ETag y transacción.

## 16. Riesgos y límites del diseño

| Riesgo o límite | Efecto | Mitigación |
|---|---|---|
| `grep` lineal y sin ranking | recall y latencia dependen del volumen | filtros selectivos, límites, topes de texto; propuesta C-ACM-02 |
| `find` devuelve la fila completa | contexto inflado | límites cortos y cuerpos acotados; propuesta C-ACM-03 |
| Primera captura concurrente de un tema | dos activas posibles | verificación posterior y reconciliación; §5.4 |
| Reutilización exacta tras una sustitución | el `UNIQUE(content_hash)` impide una fila nueva idéntica | documentar la revalidación sobre la fila existente o capturar una variante con contexto nuevo; §5.3 y §9.2 |
| Convenciones no ejecutables | memoria inconsistente si el agente las ignora | checklist de la skill y verificación posterior; ACM-04/05 |
| Política no es seguridad | acceso directo al fichero la salta | permisos del sistema operativo; límite declarado |
| Evolución del esquema | filas incompatibles | `schema_version`, migración aditiva revisada y copia previa |
| Reloj y UUIDv7 | recencia incorrecta por desfase | UTC, UUIDv7 monótono y ventanas acotadas |
| Bootstrap y copia | base perdida si no se copia | runbook del operador; `.agent-memory/` fuera de Git |
| Dependencia de db-ai | un cambio de contrato rompe ACM | fijar v0.2.0 y probar la integración antes de subir versión |
| Versión desplegada | `db-ai version` existe en el árbol, pero el binario instalado puede ser anterior | verificar `version` y `commit` antes de confiar en el contrato; release con autorización del PO |
