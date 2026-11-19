# db-ai — Product Requirements Document

**Versión:** 0.2.0 · **Estado:** línea base aprobada por el PO el 2026-11-19 · **Diseño técnico:** [DESIGN.md](DESIGN.md) v0.2.0

## 1. Visión

Proporcionar una interfaz universal de acceso a datos estructurados basada en la semántica de los sistemas de archivos. Un humano o agente puede explorar y modificar una base existente mediante rutas y comandos familiares sin generar SQL. La IA es consumidora, no dependencia del producto. La universalidad es visión, no requisito del MVP: el alcance aprobado rechaza esquemas sin identidad estable (claves sin PK estable, BLOB/REAL/NULL en la identidad), vistas y tablas virtuales.

## 2. Objetivos y límites

- Reducir la documentación necesaria para que un agente descubra y utilice una base de datos.
- Mantener operaciones deterministas, verificables y componibles mediante CLI, stdin, stdout y códigos de salida.
- Conservar las restricciones del motor relacional y evitar modificaciones implícitas.
- MVP: Go, SQLite existente, CLI, sesiones aisladas opcionales, lectura y escritura controladas. Sin servidor, FUSE, LLM, embeddings ni SQL arbitrario.
- Futuro: shell interactivo, PostgreSQL, otros adaptadores, vistas relacionales navegables y autorización multiusuario. No son requisitos del MVP.

## 3. Usuarios y escenarios

Agente explorador: descubre tablas, esquemas y registros sin conocer previamente el dominio. Agente constructor: modifica registros autorizados y verifica resultados. Operador humano: monta bases existentes y controla permisos. Automatización: consume JSON y encadena operaciones sin interacción.

## 4. Modelo de recursos

```text
/
└── work/                     # montaje SQLite
    ├── tasks/                # tabla
    │   └── TK-001/           # registro identificado por clave
    │       ├── title         # columna
    │       └── status
    └── users/
```

No se crean archivos por cada registro. Las rutas son identificadores virtuales. Los nombres y claves se codifican de forma reversible. Las claves compuestas requieren un selector canónico; las tablas sin clave primaria estable se exponen únicamente para consultas tabulares. No se añaden columnas ni tablas auxiliares a la base montada.

## 5. Requisitos funcionales

| ID | Requisito | Criterio de aceptación |
|---|---|---|
| FR-01 | Montar SQLite existente con alias único y modo read-only por defecto | Montar dos bases y listar ambas; un fichero inexistente no se crea |
| FR-02 | `ls`, `tree`, `stat`, `describe` | Descubrir tablas, columnas, tipos, claves, restricciones y capacidades; recorrido acotado |
| FR-03 | `cat` | Leer registro JSON o campo, conservando NULL, tipo y precisión |
| FR-04 | `cd`, `pwd` | Contexto opcional identificado por sesión explícita; dos sesiones no interfieren; sin sesión se usan rutas absolutas |
| FR-05 | `find`, `grep` | Filtrar con operadores documentados y búsqueda textual literal; límites y orden determinista; sin SQL libre |
| FR-06 | `touch`, `write` | Insertar registro mediante JSON sin precondición de concurrencia; actualizar un campo o registro con validación y precondición de concurrencia (ETag) |
| FR-07 | `cp` | Copiar un registro dentro de su tabla con nueva clave explícita; nunca sobrescribir ni copiar relaciones implícitamente |
| FR-08 | `rm` | Eliminar un registro identificado; exigir autorización y precondición; no borrar tablas ni subárboles |
| FR-09 | `mv` | Renombrar clave primaria simple dentro de su tabla; rechazar referencias entrantes, claves generadas y destinos existentes |
| FR-10 | `--dry-run` | Presentar plan de mutación sin ejecutar cambios ni triggers; advertir que no garantiza éxito posterior |
| FR-11 | Respuesta JSON por defecto y ayuda | Todas las operaciones disponen de respuesta JSON versionada por defecto y única en el MVP; `--json` se acepta como flag explícito sin efecto; errores legibles por máquina; sin prompts en modo automatizado |
| FR-12 | `batch` | Ejecutar varias mutaciones sobre un solo montaje en una transacción; ante fallo, rollback completo |
| FR-13 | Política de acceso | Denegar escrituras por defecto; permitir operaciones/rutas explícitas; no permitir consultas que revelen campos prohibidos |
| FR-14 | Auditoría | Registrar operación, actor declarado, recurso, resultado e identificador de solicitud fuera de la base de usuario; no incluir valores sensibles |
| FR-15 | `mount`, `umount`, `mounts` | Gestionar configuración local sin alterar ni eliminar los ficheros SQLite |
| FR-16 | `export` | Exportar selección acotada con los mismos filtros y límites que `find` (`--where`), a JSON o JSONL por stdout; no escribir archivos automáticamente |

## 6. Contrato de experiencia

Ejemplos normativos de intención (la sintaxis exacta está en DESIGN):

```bash
db-ai mount ./tasks.db /work
db-ai ls /work --json
db-ai describe /work/tasks --json
db-ai cat /work/tasks/TK-001 --json
db-ai find /work/tasks --where 'status=pending' --limit 20
```

Las operaciones mutantes requieren montaje habilitado por el operador, política de escritura y, según operación, autorización explícita y versión esperada. Los códigos de error permiten al agente corregir su propia invocación. No hay confirmaciones interactivas obligatorias ni lenguaje natural interpretado automáticamente.

## 7. Requisitos no funcionales

| ID | Requisito | Verificación |
|---|---|---|
| NFR-01 | Determinismo | Orden estable, contrato JSON versionado, sin timestamps variables en lecturas normales |
| NFR-02 | Seguridad | SQL parametrizado, identificadores resueltos desde catálogo, rutas confinadas, sin extensiones SQLite cargables |
| NFR-03 | Integridad | Foreign keys habilitadas; restricciones nativas respetadas; efectos indirectos de escritura rechazados en MVP |
| NFR-04 | Concurrencia | Precondiciones verificadas dentro de transacción de escritura; fallo explícito ante bloqueo o conflicto |
| NFR-05 | Recursos | Paginación; los límites son por defecto y máximo: `--limit` mayor que 1000 → `E_LIMIT`; salida máxima 16 MiB; `tree` profundidad máxima 4; `--timeout` configurable entre 1 y 60 s con defecto 5 s |
| NFR-06 | Portabilidad | Linux/WSL como entorno inicial; binario autónomo sin servicio; Windows nativo sujeto a pruebas posteriores |
| NFR-07 | Compatibilidad | No alterar esquema al montar; anunciar capacidades no soportadas; probar claves compuestas, NULL, BLOB, enteros de 64 bits |
| NFR-08 | Mantenibilidad | Adaptador separado de CLI, pruebas unitarias y de integración; dependencias justificadas |

## 8. Política de producto

La autorización local no constituye una frontera de seguridad frente a agentes que tengan acceso directo al fichero o a la configuración. Para aislamiento real se necesitan permisos de SO o un servicio intermediario, fuera del MVP. El actor indicado por CLI es informativo, no identidad autenticada.

No se aceptan operaciones ambiguas: crear tablas mediante mkdir, mover entre tablas, borrado recursivo, copiar grafos de relaciones, ejecución SQL libre ni edición de campos que formen parte de la identidad. Los efectos de triggers y cascadas se rechazan conservadoramente cuando no se pueden garantizar.

## 9. Entregas y aceptación

- M1 — Exploración: FR-01–05, 11, 15, 16 y FR-13 en su lado de lectura (política de campo aplicada a las consultas). Demostración con SQLite existente intacta.
- M2 — Mutaciones: FR-06–10, 13 en su lado de escritura, 14. Fixtures verifican restricciones, conflictos y ausencia de cambios ante fallo.
- M3 — Composición: FR-12, endurecimiento y documentación. Pruebas completas de concurrencia, límites y compatibilidad.

Todo requisito debe tener evidencia automatizada o una justificación explícita de bloqueo. No se declara terminado sobre la base de una demostración visual. `go test ./...`, `go test -race ./...`, `go vet ./...` y build deben pasar en el entorno objetivo.

## 10. Decisiones aprobadas

Decisiones aprobadas por el PO el 2026-11-19: línea base de este PRD v0.2.0 y los siete ADR de [DESIGN.md](DESIGN.md) §14 (registrados en el registro de decisiones del tablero tasks-ai, exportado a `docs/decisions/`). Las 21 decisiones del contrato externo (D-1…D-20, E-1) quedan incorporadas en el texto de ambos contratos. La elección de driver SQLite se valida en WP-01 contra el motor embebido del driver, no contra el CLI.
