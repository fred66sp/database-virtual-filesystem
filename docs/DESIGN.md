# db-ai — Technical Design

**Versión:** 0.2.0 · **Estado:** línea base aprobada por el PO el 2026-11-19 · **Requisitos:** [PRD.md](PRD.md) v0.2.0

## 1. Principios y arquitectura

```text
CLI / stdin / stdout
        |
Command parser + dispatcher
        |
Application services ---- Session / Mount registry
        |
Path resolver + catalog + policy
        |
Query / Mutation planner
        |
Storage port ---- SQLite adapter
        |
Existing SQLite database
```

El núcleo no ejecuta lenguaje natural. Separar parsing, resolución, autorización, planificación, ejecución y presentación. La base es la fuente de verdad de los datos y su catálogo. No usar FUSE ni simular el directorio actual del shell padre.

## 2. Estructura propuesta del repositorio

```text
cmd/db-ai/main.go
internal/cli/
internal/app/
internal/vfs/
internal/catalog/
internal/policy/
internal/query/
internal/storage/
internal/storage/sqlite/
internal/state/
internal/audit/
internal/output/
testdata/
docs/
```

Preferir biblioteca estándar y `database/sql` cuando sea adecuado. Evaluar un driver SQLite pure Go (por ejemplo modernc.org/sqlite) frente a CGO mediante pruebas de bloqueo, transacciones, interrupción y empaquetado. No asumir que las opciones de DSN son portables. Fijar versiones tras verificar el entorno.

## 3. Contrato CLI (FR-01–17)

```text
db-ai version
db-ai mount FILE /ALIAS [--mode ro|rw]
db-ai umount /ALIAS
db-ai mounts
db-ai ls [PATH] [--limit N] [--cursor TOKEN]
db-ai tree [PATH] [--depth N] [--limit N]
db-ai stat PATH
db-ai describe PATH
db-ai cat PATH
db-ai --session ID cd PATH
db-ai --session ID pwd
db-ai find PATH [--where EXPR]... [--limit N] [--cursor TOKEN]
db-ai grep PATH TEXT [--limit N] [--cursor TOKEN]
db-ai touch PATH --data JSON|-
db-ai write ROW_OR_FIELD --data JSON|- --if-match ETAG
db-ai cp ROW DEST_ROW --if-match ETAG
db-ai mv ROW DEST_ROW --if-match ETAG --apply
db-ai rm ROW --if-match ETAG --apply
db-ai batch --data JSON|-
db-ai export PATH [--where EXPR]... [--format json|jsonl] [--limit N] [--cursor TOKEN]
```

Flags globales `--json`, `--session`, `--timeout`, `--actor`; `--dry-run` para mutaciones, incompatible con `--apply`. `describe`, `find`, `grep`, `touch` y `export` reciben una ruta VFS (`/alias/table`), no un nombre de tabla. `--apply` es obligatorio solo en `mv` y `rm`. `--json` se acepta sin efecto: JSON es el formato por defecto y único del MVP. `-` lee stdin una sola vez. JSON malformado o claves duplicadas se rechazan. `--apply` es confirmación no interactiva de las operaciones destructivas, no permiso. La raíz es `/`; sin sesión, las rutas relativas se rechazan; `ls` sin argumento lista raíz o cwd de sesión. Flags desconocidos producen error. `--help` no abre bases. La forma preferida del filtro de ejemplo del PRD es `--where 'status=pending'`; los espacios exteriores del valor no son significativos. `version` es un subcomando, no un flag global: no abre bases y emite en `data` `{version, commit, state}` —versión funcional, revisión Git inyectada al compilar con `-ldflags -X` (`unknown` si no se inyectó) y estado de build `dev` o `release`.

## 4. Identidad y rutas (FR-02–04, NFR-02,07)

Tipos: Root, Mount, Table, Row, Field. Rutas absolutas `/alias/table/key/field`. Resolver segmentos antes de decodificar; `.` y `..` solo en su forma literal; no permitir ascender sobre raíz. Codificación porcentual UTF-8 sobre bytes, incluso slash, porcentaje y puntos cuando el segmento completo es `.` o `..`. Rechazar escapes no canónicos, NUL y segmentos vacíos. No usar `filepath.Clean` para rutas virtuales.

Alias ASCII `[A-Za-z0-9_-]+`. Tabla y columna deben existir en el catálogo. Clave simple TEXT usa segmento escapado; cadena vacía usa marcador reservado `~e`; escapar `~` literal como `%7E`. INTEGER usa decimal canónico. Claves compuestas usan `~k` seguido de base64url sin padding del array JSON canónico de envoltorios tipados que reutilizan `Value{Kind, Data}`, en orden de PK: `[{"kind":"text","data":"a"},{"kind":"integer","data":"42"}]`. La serialización es compacta UTF-8; la codificación, base64url sin padding. BLOB, REAL o NULL en la identidad se consideran no navegables en MVP. Comprobar unicidad real y no nulidad de PK; si una tabla puede contener claves NULL, limitarla a consulta tabular. No utilizar rowid implícito como identidad. Colaciones no binarias o afinidades ambiguas deshabilitan mutaciones hasta disponer de prueba de identidad inequívoca.

Ejemplo: `/work/tasks/TK-001/status`. Una clave textual `a/b` se representa `a%2Fb`. Nunca interpolar segmentos de ruta como SQL. Nombres de catálogo se cotizan con comillas dobles escapadas; valores siempre se enlazan como parámetros.

## 5. Catálogo y capacidades

Consultar `sqlite_schema`, `PRAGMA table_xinfo`, `foreign_key_list`, `index_list`, `index_xinfo` y `table_list` donde estén disponibles. Detectar PK, columnas ocultas/generadas, tipos declarados, restricciones, índices, triggers y referencias entrantes. Exponer limitaciones en describe. No analizar expresiones SQL de restricciones mediante heurísticas inseguras: el motor valida; si una capacidad no se puede demostrar, se deniega. Excluir tablas internas y virtuales de mutación; las vistas quedan fuera del alcance del MVP (no se exponen ni para lectura), conforme a la decisión D-20 aprobada por el PO. Invalidar catálogo cuando cambie schema_version y revalidar dentro de transacciones.

## 6. Interfaces Go orientativas

```go
package storage

import "context"

type Value struct {
    Kind string // null, integer, real, text, blob
    Data string // representación canónica
}
type Key []Value
type Field struct {
    Name  string
    Value Value
}
type Row struct {
    Fields []Field // orden de catálogo; el mapa no es determinista en Go
    Key    Key
    ETag   string
}
type Column struct {
    Name, DeclaredType string
    PKOrder int
    Generated, Nullable bool
}
type Table struct {
    Name string
    Columns []Column
    Readable, Writable, Navigable bool
}
type Filter struct { Column, Op string; Value Value }
type OrderSpec struct {
    Column string
    Desc   bool
}
type Query struct {
    Table      string
    Filters    []Filter
    Projection []string
    Order      []OrderSpec
    Limit      int
    Cursor     string
}
type Mutation struct {
    Kind, Table, IfMatch string
    Key, Destination Key
    Fields map[string]Value
}
type Reader interface {
    Catalog(ctx context.Context) ([]Table, error)
    Get(ctx context.Context, table string, key Key) (Row, error)
    List(ctx context.Context, q Query) ([]Row, string, error)
}
type Tx interface {
    Reader
    Apply(ctx context.Context, m Mutation) (Row, error)
}
type Store interface {
    Read(ctx context.Context, fn func(Reader) error) error
    Write(ctx context.Context, fn func(Tx) error) error
    Close() error
}
```

Son contratos de partida, no código completo. Los servicios verifican políticas antes de acceder al adaptador; Writer revalida catálogo y precondiciones dentro de la transacción. Los resultados no pueden emitirse como éxito antes de commit. El adaptador no depende de CLI ni de sesiones.

## 7. Consultas (FR-02,03,05,16)

`find`: operadores `=`, `!=`, `<`, `<=`, `>`, `>=`, `is-null`, `not-null`; múltiples filtros AND. Seleccionar columnas autorizadas y rechazar filtros sobre columnas prohibidas. Parsear operador más largo primero; el valor se interpreta primero como JSON y, si no lo es, como texto literal: `1` → entero, `"1"` → texto, `null`/`true`/`false` → literales JSON, palabra no JSON → texto literal. Nunca SQL. `grep` busca subcadena literal con `instr` en columnas TEXT autorizadas; sensible a mayúsculas; sin FTS automático. `ls` enumera hijos directos; `tree` agrega solo hasta profundidad y presupuesto declarados. No recorrer filas sin límite.

Ordenar por clave primaria con orden explícito y paginación keyset. Cursor `base64url` de `{v, montaje, tabla, filtros, orden, última_clave}` firmado con HMAC y una clave de la configuración; un cursor falsificado o inválido devuelve `E_USAGE`. No es snapshot entre invocaciones; cambios concurrentes pueden afectar recorrido. Tablas sin identidad estable: ordenar por todas las columnas autorizadas con tipos y colación explícitos; limitar a primera página y marcar truncamiento, sin cursor si no hay orden total. Si la columna de orden no está autorizada por política, la paginación más allá de la página 1 devuelve `E_DENIED`. En tablas no navegables, `ls` y `cat` devuelven `E_UNSUPPORTED`, `tree` las muestra como contenedores vacíos, `stat` informa `navigable:false`, y `find`, `grep` y `export` se permiten solo en la primera página. Exportación aplica los mismos permisos y límites que find.

## 8. Mutaciones (FR-06–10,12; NFR-03,04)

- touch: INSERT con columnas explícitas; no inventar valores obligatorios; omisiones utilizan defaults nativos. Devolver clave efectiva; rechazar tabla no navegable.
- write: UPDATE de campos autorizados; no cambiar PK ni columnas generadas. Un campo recibe un valor JSON; registro recibe mapa parcial, no reemplazo implícito.
- cp: INSERT de campos ordinarios de origen con PK destino explícita; `--if-match` referencia la fila origen; no copiar columnas generadas ni relaciones. Restricciones UNIQUE pueden impedir copia.
- mv: UPDATE de PK simple; denegar referencias entrantes, PK generada, clave compuesta y destino existente.
- rm: DELETE de una fila; no recursivo; bloquear si existen referencias entrantes con potencial efecto indirecto.

Una operación que requiere leer fila completa (ETag, copia) se deniega si el actor no tiene acceso completo a esa fila. Triggers de escritura sobre la tabla objetivo se rechazan en MVP, incluso si parecen inocuos. Rechazar FK con efectos de cascada o SET NULL/DEFAULT para la operación afectada, de forma conservadora. Activar foreign_keys por conexión y verificarlo. No usar INSERT OR REPLACE ni desactivar restricciones.

Secuencia: autorizar → abrir conexión dedicada → BEGIN IMMEDIATE → verificar catálogo, permisos, existencia y ETag → ejecutar SQL parametrizado → comprobar filas afectadas y resultado → COMMIT → emitir éxito. ROLLBACK ante cualquier error previo a commit. Usar API de driver que garantice BEGIN IMMEDIATE o ejecutar explícitamente en conexión dedicada; no mezclar `sql.Tx` y transacciones manuales. Timeout y busy_timeout limitados. Si commit tiene resultado incierto, devolver error de estado incierto y exigir lectura de reconciliación.

ETag: SHA-256 de representación canónica de tabla, clave, schema_version y valores tipados completos en orden de catálogo; representa contenido, no versión monotónica, por tanto no detecta ABA. `--if-match` compara dentro de la transacción. dry-run solo planifica y consulta, no ejecuta triggers ni promete superar constraints. El plan no es autorización futura.

Batch: array de operaciones explícitas, máximo 100 como valor por defecto y como máximo, un montaje, una transacción, sin referencias dinámicas a resultados previos en MVP. Cada elemento es un envoltorio `{op, path, ...args de la operación individual}`, por ejemplo `[{"op":"write","path":"/work/tasks/TK-001/status","data":{"kind":"text","data":"done"}}]`. Cada operación cumple sus propios permisos y precondiciones. Un fallo revierte todas. No soportar atomicidad entre bases.

## 9. Estado, montaje y sesiones (FR-01,04,13,15)

`$XDG_CONFIG_HOME/db-ai/config.json` (fallback `~/.config/db-ai`); estado en `$XDG_STATE_HOME/db-ai` (fallback `~/.local/state/db-ai`). Configuración contiene versión, alias, ruta absoluta canonicalizada, modo, política. No resolver rutas de archivos a partir de rutas VFS. Montaje valida archivo regular existente y cabecera SQLite; abrir con mode=ro o mode=rw, nunca create. `mount --mode rw` exige política explícita del montaje; sin ella se rechaza. `mount` y `umount` se reservan al operador por convención documentada: no hay identidad autenticada que lo haga cumplir (PRD §8). Evitar `immutable=1` sobre bases activas. Umount elimina referencia, no fichero. Configuración de ejemplo:

```json
{"version":1,"mounts":{"work":{"file":"/home/user/tasks.db","mode":"rw","allow":[{"ops":["ls","cat","find","describe","stat","tree","grep","export"],"prefix":"/work/tasks"},{"ops":["write"],"prefix":"/work/tasks/TK-001/status"}]}}}
```

Configuración y políticas editables por operador; cambios bajo bloqueo y reemplazo atómico en mismo directorio, permisos 0600 y directorios 0700 en Linux. Sin política, montaje ro con lectura solo de tablas ordinarias permitidas por defecto; rw no concede escritura por sí solo. Política explícita reemplaza defaults; deny por defecto, comparación por segmentos, nunca prefijos string. Ancestros se pueden mostrar solo como contenedores de recursos autorizados. No exponer columnas, conteos, filtros ni errores de otros recursos prohibidos. Esquema, claves y dependencias que no puedan exponerse sin fuga implican denegación.

Sesión: ID explícito ASCII seguro, cwd persistido en `$XDG_STATE_HOME/db-ai/sessions/<id>.json` (fallback `~/.local/state/db-ai`), actualización bajo bloqueo. Cada agente usa ID propio; no sesión global. `cd` valida destino contenedor y permiso; `pwd` revalida cwd; alias desmontado devuelve E_STALE_SESSION. `cd` y `pwd` sin `--session` devuelven E_USAGE; el resto de operaciones son stateless sin sesión. Shell interactivo futuro reutiliza dispatcher y mantiene cwd en memoria.

## 10. Salida y errores (FR-11, NFR-01,05)

```json
{"version":1,"ok":true,"data":{"path":"/work/tasks/TK-001","fields":{"id":{"kind":"text","data":"TK-001"},"status":{"kind":"text","data":"pending"}},"etag":"sha256:..."},"meta":{"truncated":false,"next_cursor":"...","request_id":"..."}}
{"version":1,"ok":false,"error":{"code":"E_…","message":"…"}}
```

Esquema de error único: `{"version":1,"ok":false,"error":{"code":"E_…","message":"…"}}` en stdout con código de salida no cero. `error.code` es la fuente de verdad para el agente; los códigos de salida son grupos gruesos.

Formas de salida: un único objeto fila `{path, fields, etag}` compartido por `cat` de registro, `find` y `export`; `cat` de campo emite el envoltorio tipado `{kind, data}`; `stat` emite `{type, path, columns|keys}`; `grep` emite fila, columna y posición. El cursor de paginación viaja en `meta.next_cursor`.

`request_id`: flag opcional `--request-id` del cliente; si falta, lo genera el servidor; siempre en `meta.request_id`.

Representación tipada conserva enteros de 64 bits y BLOB en base64 estándar con padding (RFC 4648); REAL decimal reversible y no finitos explícitamente no soportados: un REAL no finito al leer devuelve `E_UNSUPPORTED`; NULL usa kind null y data vacío. El objeto `fields` de la salida emite sus claves en orden byte-wise ascendente: la serialización JSON de un mapa en Go ordena las claves y es determinista; la estructura interna `Row.Fields` conserva el orden de catálogo. Entrada admite valores JSON naturales o envoltorios tipados; números se parsean sin pasar por float64 si enteros. JSONL exporta una fila por línea; errores a stderr y código no cero si flujo parcial. El resto de comandos JSON emite exactamente un objeto a stdout incluso ante error; logs y diagnósticos a stderr. Orden canónico; no timestamps en lecturas. Presupuestos de salida, páginas y tiempo se aplican antes de emitir éxito; error si una fila individual excede límite. Valores aprobados (NFR-05, por defecto y máximo): 1000 registros por página (`--limit` > 1000 → `E_LIMIT`), 16 MiB de salida, profundidad máxima de `tree` 4 y `--timeout` entre 1 y 60 s con defecto 5 s.

Auditoría (§11): la redacción del recurso sigue la política del actor; la PK autorizada se registra completa y la PK no autorizada se registra como SHA-256 truncado más la tabla.

| Exit | Código | Significado |
|---|---|---|
| 0 | OK | Éxito |
| 2 | E_USAGE | Sintaxis o valor inválido |
| 3 | E_NOT_FOUND / E_STALE_SESSION | Recurso ausente o contexto inválido |
| 4 | E_DENIED | Política o modo de acceso |
| 5 | E_CONFLICT | ETag o destino en conflicto |
| 6 | E_CONSTRAINT | Restricción del motor |
| 7 | E_BUSY / E_TIMEOUT | Contención o plazo |
| 8 | E_UNSUPPORTED / E_LIMIT | Capacidad o presupuesto |
| 9 | E_INTERNAL | Fallo interno |
| 10 | E_COMMIT_UNKNOWN | Resultado de commit incierto |

## 11. Auditoría y amenazas (FR-13,14)

JSONL local con request_id, timestamp UTC, actor declarado, operación, alias, identificador de recurso redactado, estado y código. El request_id lo aporta el cliente con `--request-id` o lo genera el servidor, y viaja siempre en `meta.request_id`. La redacción del recurso sigue la política del actor: la PK autorizada se registra completa; la PK no autorizada se registra como SHA-256 truncado más la tabla. No guardar valores, SQL crudo, credenciales ni claves sensibles. Bloquear escritura concurrente del log. Antes de mutar, registrar intento durable y abortar si falla. Tras commit registrar resultado; si falla esta segunda escritura, devolver señal explícita de commit confirmado sin auditoría final y no recomendar reintento ciego. Un crash entre commit y log deja intento de resultado desconocido; reconciliar leyendo base. No se promete atomicidad entre log externo y SQLite. Auditoría no es resistente a manipulación por el mismo usuario de SO. No ejecutar comandos, extensiones ni SQL suministrados por agente. Proteger acceso directo al fichero mediante SO cuando sea necesario.

## 12. Verificación y trazabilidad

| Requisitos | Pruebas mínimas |
|---|---|
| FR-01,15 | Mount ro/rw, alias duplicado, archivo ausente, hash de fichero intacto en lectura, umount no borra |
| FR-02,03 | Catálogo, NULL, BLOB, int64, rutas escapadas, claves compuestas, tablas sin PK |
| FR-04 | Sesiones paralelas sin contaminación, cwd obsoleto, navegación relativa |
| FR-05,16 | Filtros parametrizados, inyección, paginación, export y truncamiento |
| FR-06–09 | INSERT/UPDATE/cp/mv/rm, ETag viejo, constraints, FK, triggers, destino existente |
| FR-10 | Dry-run no modifica archivo ni ejecuta efectos de escritura |
| FR-11 | Golden JSON, códigos de salida, stderr separado, ayuda |
| FR-12 | Rollback de lote ante segundo fallo, rechazo multibase |
| FR-13,14 | Acceso denegado por ruta y campo, bypass por filtros, log redactado, fallo auditoría antes/después de commit |
| NFR-01–08 | Fuzz parser/rutas, race detector, dos procesos competidores, timeout, límites, build WSL |

Usar fixtures temporales por prueba, nunca bases reales del usuario. Probar transacciones con dos conexiones independientes y dos procesos CLI. Verificar foreign_keys en cada conexión. Medir rendimiento con 100k filas de fixture, registrar hardware y baseline antes de fijar SLO. No inventar resultados de benchmark.

## 13. Plan de construcción para agentes

1. WP-01: contrato CLI, tipos, errores, codec rutas, pruebas golden/fuzz.
2. WP-02: registro de montajes, SQLite ro, catálogo y lectura.
3. WP-03: sesiones, query planner, paginación, export. Gate M1.
4. WP-04: política, auditoría, transacciones, ETags y dry-run.
5. WP-05: mutaciones individuales y pruebas adversariales. Gate M2.
6. WP-06: batch, concurrencia multiproceso, documentación, build y pruebas finales. Gate M3.

Cada WP referencia requisitos y tests, declara alcance, archivos tocados y evidencia. No introducir funcionalidades fuera de alcance sin aprobación del PO; no hacer commit ni push sin autorización. Mantener PRD como contrato funcional y DESIGN como contrato técnico; discrepancias se elevan antes de implementar. No declarar verificado lo que no se ejecutó.

## 14. Decisiones y riesgos

Los siete ADR aprobados por el PO el 2026-11-19. El registro canónico es el registro de decisiones del tablero tasks-ai, exportado a `docs/decisions/`; el resumen de la aprobación está en [PRD.md](PRD.md) §10.

- CLI virtual frente a FUSE.
- SQLite primero.
- Política ro por defecto.
- Claves estables frente a rowid.
- Sesiones explícitas frente a cwd global.
- ETag frente a columnas de versión invasivas.
- Rechazo conservador de efectos indirectos.

Riesgos: semántica relacional no equivalente a archivos, esquemas exóticos, colaciones, concurrencia, auditoría no atómica y permisos del SO. Las firmas Go son propuestas; el driver SQLite se valida en WP-01 contra el motor embebido del driver, no contra el CLI (PRD §10). No se ha inspeccionado ninguna base real ni ejecutado código de implementación.
