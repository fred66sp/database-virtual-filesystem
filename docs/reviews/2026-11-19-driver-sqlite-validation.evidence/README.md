# Sonda de validación del driver SQLite (D-19 / R-2)

Material de trabajo, desechable. Verifica empíricamente modernc.org/sqlite (pure Go)
frente a github.com/mattn/go-sqlite3 (CGO) antes de fijar el driver de `db-ai`.
No usa bases reales: cada experimento crea su propio fichero en un directorio temporal.

Fecha: 2026-11-19 · Entorno: linux/amd64 · go1.27.3 · gcc presente (`/usr/bin/gcc`) · red disponible.

## Versiones fijadas

| Driver | Versión | Motor embebido | Compilación embebida |
|---|---|---|---|
| modernc.org/sqlite | v1.62.0 | SQLite **3.54.1** | `journal_mode` por defecto `delete`, `busy_timeout` por defecto **0**, 57 compile options (incluye `ENABLE_FTS5`, `MAX_VARIABLE_NUMBER=32766`, `THREADSAFE=1`) |
| github.com/mattn/go-sqlite3 | v1.14.53 | SQLite **3.54.1** | `journal_mode` por defecto `delete`, `busy_timeout` por defecto **5000**, 43 compile options (`MAX_VARIABLE_NUMBER=32766`, `THREADSAFE=1`, **sin `ENABLE_FTS5`**) |

Ambos drivers empaquetan exactamente el mismo motor (3.54.1); lo que cambia es cómo se
compiló y qué opciones de DSN acepta.

## Construcción

```bash
CGO_ENABLED=1 go build ./...                      # exit 0
CGO_ENABLED=0 go build -o bin/probe-modernc ./cmd/modernc
CGO_ENABLED=1 go build -o bin/probe-mattn   ./cmd/mattn
```

| Artefacto | Tamaño | Enlazado | `go list -deps` |
|---|---|---|---|
| `bin/probe-modernc` (CGO_ENABLED=0) | 10.531.068 B (10,0 MiB) | estático | 146 |
| `bin/probe-modernc-stripped` (`-s -w`) | 6.852.768 B (6,5 MiB) | estático | 146 |
| `bin/probe-mattn` (CGO_ENABLED=1) | 7.565.344 B (7,2 MiB) | dinámico (glibc) | 117 |
| `bin/probe-mattn-stripped` (`-s -w`) | 4.034.864 B (3,8 MiB) | dinámico (glibc) | 117 |
| mattn con `-linkmode external -extldflags "-static"` | 8.612.368 B | estático (aviso `dlopen` de glibc) | — |

Trampa de empaquetado medida: `CGO_ENABLED=0 go build ./cmd/mattn` **compila** (3.549.688 B)
porque mattn incluye un stub `!cgo`, pero el binario falla en ejecución con
`Binary was compiled with 'CGO_ENABLED=0', go-sqlite3 requires cgo to work. This is a stub`
(13 de 15 experimentos FAIL, exit 1). El fallo es de ejecución, no de compilación: una
matriz de CI que solo compile no lo detecta.

## Resultados por experimento

Salidas completas: `out-modernc.txt`, `out-mattn.txt`.

| Experimento | modernc (pure Go) | mattn (CGO) |
|---|---|---|
| `engine` | OK · 3.54.1, `journal_mode=delete`, `busy_timeout=0`, 57 opts | OK · 3.54.1, `journal_mode=delete`, `busy_timeout=5000`, 43 opts |
| `busy_dsn` (`_pragma=busy_timeout(500)` vs `_busy_timeout=500`) | OK · readback 500, segundo escritor BUSY a los 503 ms | OK · readback 500, segundo escritor BUSY a los 502 ms |
| `busy_dsn_alt` (forma del otro driver) | OK · `_busy_timeout=700` **respetado** (readback 700) | OK · `_pragma=busy_timeout(700)` **ignorado en silencio** (readback 5000) |
| `dsn_pragma_support` (`_pragma=foreign_keys(1)`) | OK · readback 1 (`_pragma` funciona) | OK · readback 0 (`_pragma` no se aplica; no es un parámetro de su DSN) |
| `busy_pragma_exec` (`PRAGMA busy_timeout=300` tras abrir) | OK · readback 300, BUSY a los 302 ms | OK · readback 300, BUSY a los 302 ms |
| `dsn_readonly_mode` (`?mode=ro`) | OK · nombre plano **no aplica** `mode=ro` (escritura pasa); `file:` URI sí (error 8) | OK · nombre plano **no aplica** `mode=ro` (escritura pasa); `file:` URI sí |
| `busy_wal_mode` | OK · lector sigue leyendo; segundo escritor BUSY a los 402 ms | OK · lector sigue leyendo; segundo escritor BUSY a los 402 ms |
| `begin_immediate_exec` (`BEGIN IMMEDIATE` por `Exec` en `sql.Conn`) | OK · fila no confirmada invisible, visible tras COMMIT | OK · ídem |
| `begin_tx_serializable_default_dsn` | OK · `BeginTx(LevelSerializable)` **no** toma bloqueo de escritura al abrir (BEGIN diferido) | OK · igual: `LevelSerializable` no da `BEGIN IMMEDIATE` |
| `begin_tx_dsn_txlock_immediate` (`_txlock=immediate`) | OK · `BeginTx` sí toma bloqueo de escritura | OK · `_txlock=immediate` sí funciona (mismo comportamiento) |
| `tx_mixing_manual_and_pooled` | OK · tras `db.Exec("BEGIN IMMEDIATE")` el pool tiene `InUse=0`; un `Exec` pooled durante una tx manual ajena falla con BUSY (ruidoso, sin división silenciosa); `tx.Exec("BEGIN")` → `SQL logic error: cannot start a transaction within a transaction (1)`; `MaxOpenConns(1)` funciona | OK · mismas observaciones; `tx.Exec("BEGIN")` → `cannot start a transaction within a transaction` |
| `interrupt_ctx_timeout` (CTE recursiva de 1e8 filas, `context.WithTimeout` 200 ms) | OK · cancelada a los 201 ms (`context deadline exceeded`), conexión reutilizable, sin bloqueo residual | OK · cancelada a los 201 ms, conexión reutilizable, sin bloqueo residual |
| `journal_mode_wal` | OK · `PRAGMA journal_mode=WAL` → `wal` sin perder `busy_timeout` | OK · ídem |
| `fts5` | OK · crear + insertar + `MATCH` devuelve 1 fila | OK · `CREATE VIRTUAL TABLE ... USING fts5` → `no such module: fts5` |
| `build` | OK · CGO_ENABLED=0, 8 deps de módulo | OK · CGO_ENABLED=1, 1 dep de módulo |

Todas las ejecuciones terminaron con exit 0 en ambas variantes (los FAIL de la variante
mattn sin CGO son de la prueba de empaquetado, no del programa principal).

## Diferencias de DSN y portabilidad (medido)

1. **`_pragma` genérico no es portable.** modernc lo ejecuta como SQL (`_pragma=foreign_keys(1)`
   → aplicado). mattn v1.14.53 **no tiene** ese parámetro: usa claves tipadas
   (`_busy_timeout`, `_txlock`, `_foreign_keys`, `_journal_mode`, …) y descarta en silencio
   las desconocidas. Verificado en la fuente del driver, no solo por comportamiento.
2. **Las claves tipadas de mattn sí funcionan en modernc** (`_busy_timeout=700` verificado).
   Es decir, la forma portable hoy es usar las claves tipadas en ambos; `_pragma` es
   exclusivo de modernc y, además, ejecuta SQL arbitrario desde el DSN (riesgo de inyección
   si el DSN no es constante: modernc ofrece `StrictPragmas(true)`).
3. **`busy_timeout` por defecto no coincide: 0 (modernc) frente a 5000 (mattn).** No fijarlo
   da una latencia de bloqueo de 5 s en mattn y de 0 (BUSY inmediato) en modernc. Hay que
   fijarlo explícitamente por conexión.
4. **`?mode=ro` en un nombre plano se ignora en los dos drivers**: el fichero se abre en
   lectura/escritura y una escritura **tiene éxito**. Solo la forma `file:...?mode=ro`
   queda aplicada. Para un montaje `ro` esto es una frontera de seguridad, no un detalle.
5. **Los textos de error difieren**: modernc devuelve `database is locked (5) (SQLITE_BUSY)`
   (código incluido); mattn devuelve `database is locked` (sin código). La clasificación de
   BUSY no puede depender del texto ni de un código de error tipado del driver.
6. **`BeginTx` con `LevelSerializable` no da `BEGIN IMMEDIATE` en ninguno de los dos**:
   ambos abren en modo diferido. El único camino portable a `BEGIN IMMEDIATE` mediante
   `sql.Tx` es `_txlock=immediate` en el DSN; alternativamente, `BEGIN IMMEDIATE` por `Exec`
   sobre un `*sql.Conn` fijado.
7. **Patrón de transacción seguro (idéntico en ambos)**: o un `*sql.Conn` fijado para
   `BEGIN`/`COMMIT` manuales, o `sql.Tx` en exclusiva. `db.Exec("BEGIN")` devuelve la conexión
   al pool (`InUse=0`) y la transacción queda sin dueño. Mezclar `sql.Tx` con `BEGIN` manual
   falla de forma explicita, y `MaxOpenConns(1)` hace que la secuencia manual funcione a costa
   de serializar todo el proceso.
8. **FTS5**: modernc lo trae; el build por defecto de mattn no (`no such module: fts5`).
   Leer una base existente con tablas FTS5 funciona en modernc y falla en mattn.

## Sorpresas

- mattn ya trae `busy_timeout=5000` por defecto (comportamiento históricamente opuesto a lo
  que se suele asumir de "0 por defecto"), y `_pragma` no existe en su parser de DSN pese a
  ser la forma que muchos ejemplos usan con modernc.
- Ambas variantes empaquetan el mismo motor 3.54.1 y se comportan igual en bloqueo,
  transacciones e interrupción; la diferencia real está en el DSN, en FTS5 y en el empaquetado.

## Qué no se probó (riesgos)

- Sin pruebas de rendimiento (throughput, latencia, memoria): no se midió coste por consulta.
- Sin pruebas de corrupción/durabilidad (corte de proceso a mitad de escritura, `fsync`,
  `synchronous=FULL`), ni `PRAGMA integrity_check` tras fallo simulado.
- Sin bases reales del usuario: ficheros creados por la sonda, esquema trivial, sin BLOBs,
  sin PK compuestas, sin colaciones no binarias.
- Sin concurrencia multiproceso real (dos procesos distintos): el bloqueo se probó con dos
  conexiones en el mismo proceso.
- Sin `cache=shared`, `immutable`, `nolock`, ni bloqueo sobre NFS/SMB (`\\wsl.localhost`,
  rutas UNC) — relevante si db-ai monta ficheros en red.
- Sin Windows/macOS ni cross-compilación.
- La interrupción se midió con una única consulta de solo lectura; no se probó cancelar una
  transacción de escritura en curso.
- Con mattn, el enlazado estático produce un aviso de `dlopen` de glibc: funciona en este
  entorno, pero no se validó su portabilidad a otras distribuciones.

## Cómo reproducir

```bash
cd /tmp/opencode/wp01-driver-probe
CGO_ENABLED=0 go build -o bin/probe-modernc ./cmd/modernc && ./bin/probe-modernc
CGO_ENABLED=1 go build -o bin/probe-mattn   ./cmd/mattn   && ./bin/probe-mattn
```

Cada experimento imprime `OK <experimento>: <dato>` o `FAIL <experimento>: <detalle>` y el
programa sale con 0 si todos pasan.
