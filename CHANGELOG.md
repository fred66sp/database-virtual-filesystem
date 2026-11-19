# Changelog

Todos los cambios notables de este proyecto se documentan en este fichero.

El formato está basado en [Keep a Changelog](https://keepachangelog.com/es-ES/1.1.0/),
y este proyecto se adhiere al [Versionado Semántico](https://semver.org/lang/es/).

## [0.3.0] - 2026-11-19

### Añadido

- Comando `db-ai version`: responde con el sobre JSON estándar e identifica la versión
  funcional, la revisión Git inyectada al compilar con `-ldflags -X` (`unknown` sin
  inyección) y el estado de build `dev`/`release`. Sin dependencias nuevas.
- `init.sh` de ACM con modo demostración explícito (`--demo`) y arnés de benchmark con
  fixture reproducible de 10 000+ observaciones (`tools/gen-fixture.py`,
  `tests/benchmark.py`) con medición de M-ACM-01 a M-ACM-04.
- Evidencia de continuidad con dos agentes reales en sesiones independientes
  (`scenarios/scenario-agents.md`, `scenarios/evidence/evidence-agents.md`), con guion
  reproducible de las cinco preguntas de reanudación.

### Modificado

- Convención de reanudación: el checkpoint vigente se recupera por `session_id` cuando
  la sesión es conocida y, si no, por la sesión más reciente de una ventana acotada de
  `started_at` con corrección del corte si la página viene truncada; el guion por
  defecto acota decisiones por `topic_key` y preferencias por `pref_key`.

### Corregido

- El seed de demostración se aplicaba a cualquier memoria nueva, incluida la de un
  proyecto real: ahora `init.sh` crea memoria limpia y los datos ficticios exigen
  `--demo`.
- Dos `init.sh` concurrentes perdían montajes de `config.json`: ahora comparten el
  mismo bloqueo `flock` que db-ai (`config.lock`).

## [0.2.0] - 2026-11-19

### Añadido

- Contratos funcionales y técnicos iniciales: `docs/PRD.md` (requisitos y alcance) y
  `docs/DESIGN.md` (arquitectura y decisiones técnicas).
- Sistema de trabajo: tablero tasks-ai (`.tasks-ai/`), mapa de fuentes para agentes
  (`AGENTS.md`), política Git (`CONTRIBUTING.md`), catálogo enlazable (`llms.txt`) y
  configuración de plugins (`opencode.jsonc`).
- Informe de revisión de los contratos iniciales (`docs/reviews/2026-11-19-prd-design-review.md`).
- CLI `db-ai` con contrato JSON versionado, esquema único de errores con códigos `E_*` y
  codec reversible de rutas VFS (WP-01).
- Registro de montajes, catálogo SQLite de solo lectura y comandos de lectura (`ls`,
  `tree`, `stat`, `describe`, `cat`) sobre bases existentes (WP-02).
- Sesiones explícitas (`cd`, `pwd`), consultas `find` con filtros tipados, cursores keyset
  firmados y exportación JSON/JSONL por stdout (WP-03).
- Política de escritura deny-by-default, log de auditoría JSONL redactado, transacciones
  `BEGIN IMMEDIATE`, precondiciones ETag y `--dry-run` (WP-04).
- Mutaciones individuales `cp`, `mv` y `rm` con pruebas adversariales (WP-05).
- Comando `batch` (FR-12): array de operaciones `{op, path, …}` con máximo de 100 (D-8),
  un solo montaje y una transacción con rollback completo, auditoría por operación y
  `--dry-run`; pruebas de concurrencia multiproceso (WP-06).
- Aplicación de referencia `examples/agent-continuity/`: memoria de continuidad para
  agentes compuesta con SQLite + db-ai + una skill — contratos PRD/DESIGN propios
  (17 requisitos trazados), esquema con PKs UUIDv7, `init.sh` idempotente, escenarios
  ejecutables (continuidad A→B e integridad), batería de 13 pruebas y evidencias de
  validación con métricas de recuperación.
- Skill `agent-continuity` (paquete en `examples/agent-continuity/skill/`): siete
  protocolos de comportamiento sobre la skill `db-ai`, con la memoria declarada como
  contexto y nunca como instrucciones con autoridad sobre el usuario.

### Modificado

- Contratos PRD y DESIGN revisados a v0.2.0 con las 21 decisiones aprobadas por el PO
  (D-1…D-20, E-1): dirección por ruta VFS, JSON único por defecto, esquema de error y
  cursor definidos, política de lectura de campo en M1, límites como default y máximo,
  salida 10 para commit incierto.
