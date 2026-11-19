# Validación del driver SQLite — WP-01 (D-19, R-2)

**Fecha:** 2026-11-19 · **Tarea:** tk-6 · WP-01 · **Trazabilidad:** PRD §10, DESIGN §2 · **Estado:** informe completo, pendiente de adopción por el PO

## 1. Objetivo y método

DESIGN §2 exige evaluar un driver SQLite pure Go frente a CGO «mediante pruebas de bloqueo, transacciones, interrupción y empaquetado» antes de fijarlo, y PRD §10 sitúa esa validación «contra el motor embebido del driver, no contra el CLI». Esta validación se ejecutó como sonda aislada (fuera del código del producto) sobre un módulo Go desechable con dos variantes del mismo programa de prueba, cada una con fixture SQLite creada por el propio programa:

- **modernc.org/sqlite v1.62.0** (pure Go)
- **github.com/mattn/go-sqlite3 v1.14.53** (CGO)

15 experimentos por variante, ejecutados con Go 1.27.3 linux/amd64. Resultado: **15 OK / 0 FAIL en ambas variantes**. Artefactos de la sonda (fuera del repo): `/tmp/opencode/wp01-driver-probe/` con `README.md`, `out-modernc.txt`, `out-mattn.txt`, binarios y fuentes. Las salidas crudas y el README de la sonda están archivados en [`2026-11-19-driver-sqlite-validation.evidence/`](2026-11-19-driver-sqlite-validation.evidence/) para que la evidencia de esta puerta no dependa de /tmp.

## 2. Resultados observados

### 2.1 Motor embebido y empaquetado

| Dato | modernc v1.62.0 | mattn v1.14.53 |
|---|---|---|
| Motor embebido | **SQLite 3.54.1** | **SQLite 3.54.1** |
| Opciones de compilación | 57 (incluye `ENABLE_FTS5`, `MAX_VARIABLE_NUMBER=32766`, `THREADSAFE=1`) | 43 (sin `ENABLE_FTS5`) |
| Build | `CGO_ENABLED=0`, estático, 10.5 MiB (6.8 MiB con `-s -w`), 8 dependencias | Requiere `CGO_ENABLED=1`, dinámico (glibc), 7.5 MiB (4.0 MiB con `-s -w`), 1 dependencia |
| `CGO_ENABLED=0` con mattn | — | **Compila un stub que falla en ejecución** («go-sqlite3 requires cgo to work»): una CI que solo compile no lo detecta |
| mattn estático (`-extldflags -static`) | — | 8.6 MiB, arranca con aviso `dlopen` de glibc; sin validar en otras distribuciones |

**Versión de motor embebida verificada: SQLite 3.54.1 en ambos drivers.**

### 2.2 Bloqueo y concurrencia

| Experimento | modernc | mattn |
|---|---|---|
| `busy_timeout` por DSN | `_pragma=busy_timeout(500)` aplicado (readback 500, BUSY a 503 ms) | `_busy_timeout=500` aplicado (BUSY a 502 ms) |
| Formas DSN cruzadas | `_busy_timeout=700` (forma mattn) respetado | `_pragma=busy_timeout(700)` **ignorado en silencio** (queda 5000) |
| `busy_timeout` por defecto | **0** (BUSY inmediato) | **5000** (5 s de espera silenciosa) |
| `foreign_keys` por DSN | `_pragma=foreign_keys(1)` → readback 1 | `_pragma` no existe en su parser → readback 0 |
| WAL: lector concurrente con escritor | No se bloquea; segundo escritor BUSY a 402 ms | Idéntico |
| Detección de BUSY | Error `SQLITE_BUSY` con texto distinto por driver: la detección no puede depender del texto | |

### 2.3 Transacciones y patrón seguro

Ambos drivers, comportamiento idéntico:

- `BEGIN IMMEDIATE` por `Exec` en `*sql.Conn` fijada: bloqueo real al inicio, fila oculta hasta COMMIT.
- `BeginTx(LevelSerializable)` con DSN por defecto: **BEGIN diferido**, no toma bloqueo al abrir. Solo `_txlock=immediate` por DSN produce BEGIN IMMEDIATE real desde `BeginTx`.
- `db.Exec("BEGIN")` sobre el pool suelta la conexión a mitad de transacción (`InUse=0`); el fallo posterior es ruidoso (BUSY o «cannot start a transaction within a transaction»), no una división silenciosa.
- **Patrón seguro confirmado en ambos:** una `*sql.Conn` fijada por sesión para BEGIN/COMMIT manuales, o `sql.Tx` en exclusiva (con `MaxOpenConns(1)` verificado). Esto valida la secuencia de DESIGN §8 (autorizar → conexión dedicada → BEGIN IMMEDIATE → …).

### 2.4 Interrupción

Consulta larga (CTE recursivo) cancelada con `context.WithTimeout` a los ~200 ms en ambos: el error llega como `context deadline exceeded`, la conexión queda reutilizable y **sin bloqueo residual** (un segundo proceso puede tomar BEGIN IMMEDIATE inmediatamente). Medido en lectura, no en escritura en curso (ver §4).

### 2.5 Portabilidad de DSN — frontera crítica para `mode=ro`

**`?mode=ro` sobre un nombre de fichero plano se ignora en ambos drivers** (la escritura pasa). Solo `file:...?mode=ro` se aplica (error 8 «readonly database»). Esta frontera es la base del requisito de montaje ro de FR-01/D-17: db-ai deberá construir siempre URI `file:` canonizadas para montajes ro, no confiar en el flag de modo con rutas desnudas.

## 3. Conclusión y recomendación

La diferencia real entre drivers no está en el motor (3.54.1 en ambos, comportamiento idéntico en bloqueo, transacciones e interrupción) sino en **empaquetado, compatibilidad y portabilidad de DSN**:

| Criterio | modernc | mattn |
|---|---|---|
| Binario autónomo sin servicio (NFR-06) | ✅ estático, sin glibc | ⚠️ requiere CGO y glibc; el build estático emite avisos |
| Leer bases existentes con tablas FTS5 (NFR-07) | ✅ módulo incluido | ❌ «no such module: fts5» en el build por defecto |
| DSN portable entre drivers | ✅ acepta formas mattn (`_busy_timeout`, `_txlock`) y `_pragma` real | ❌ descarta en silencio parámetros desconocidos |
| Fallo ruidoso ante misconfiguración | ✅ | ⚠️ silencio en DSN es un riesgo de seguridad operativo (un pragma de seguridad que no se aplica sin avisar) |
| Trap de CI | — | ❌ stub CGO_ENABLED=0 que falla solo en ejecución |

**Recomendación: modernc.org/sqlite v1.62.0** (motor embebido SQLite 3.54.1), con estas decisiones operativas para WPs posteriores:

1. Abrir siempre con URI `file:` canonizada; `mode=ro` solo vía URI, nunca sobre ruta desnuda (D-17).
2. Fijar `busy_timeout` explícitamente por Exec tras abrir (no por DSN) para no depender de defaults divergentes (0 vs 5000).
3. Transacciones de escritura: `*sql.Conn` fijada + `BEGIN IMMEDIATE` por Exec, o DSN `_txlock=immediate`; `BeginTx` por defecto es diferido y no cumple la secuencia de DESIGN §8.
4. Detección de BUSY/errores por código, nunca por texto del mensaje.
5. `foreign_keys` verificado por readback en cada conexión (NFR-03), independientemente del DSN.

**Estado:** recomendación emitida; la adopción de modernc.org/sqlite como dependencia de go.mod queda pendiente de aprobación del PO junto con el commit de WP-01 (el código de WP-01 no depende del driver: es stdlib puro).

## 4. No verificado (límites de la sonda)

- Rendimiento (throughput/latencia/memoria), corrupción y durabilidad (corte a mitad de escritura, `synchronous`, `integrity_check`).
- Concurrencia entre **dos procesos** (la sonda usó dos conexiones en un proceso); ficheros en red, `cache=shared`, `immutable`, `nolock`.
- Fixtures triviales: sin BLOB, PK compuestas ni colaciones no binarias (eso corresponde a WP-02 en adelante con bases reales de prueba).
- Interrupción medida solo en consulta de lectura; no en transacción de escritura en curso.
- Solo Linux/WSL amd64; sin Windows/macOS.
