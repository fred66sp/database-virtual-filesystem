# Refinamiento de db-ai y ACM — diseño técnico y plan de paquetes

Fecha: 2026-11-20. Estado: **aprobado por el PO el 2026-11-20**, con las precisiones de §8.
Ámbito: las cinco
mejoras aceptadas en el DESIGN del ACM §14 como trabajo nuevo o corrección documental
(C-ACM-03, C-ACM-04 y C-ACM-06), agrupadas en REF-01…REF-05. No se reabren C-ACM-02 (FTS),
C-ACM-05 (vistas, D-20), C-ACM-07 (DDL, rechazada) ni C-ACM-08 (aplicaciones de ejemplo).

Este documento no modifica contratos ni CHANGELOG: es el análisis previo. Cuando el PO
apruebe las decisiones de §8, los cambios se implementan paquete a paquete y solo entonces
se actualizan `docs/DESIGN.md`, `CHANGELOG.md` y las skills.

## 1. Estado de partida y auditoría

Lo verificado en el árbol de trabajo, sin cambios de código en este ciclo.

| Área | Estado actual | Referencia |
|---|---|---|
| Superficie de comandos | 22 comandos: `version`, `mount`, `umount`, `mounts`, `ls`, `tree`, `stat`, `describe`, `cat`, `cd`, `pwd`, `find`, `grep`, `touch`, `write`, `cp`, `mv`, `rm`, `batch`, `export` | `internal/cli/commands.go:82-238` |
| Flags globales de consulta y mutación | `--where`, `--limit`, `--cursor`, `--format`, `--data`, `--if-match`, `--apply`; no existen `--fields`, `--order` ni `--count` | `internal/cli/commands.go:24-41` |
| `cat` sobre tabla | Rechazado con `E_UNSUPPORTED`: el comando solo lee filas | `internal/cli/handlers.go:615` |
| `cat` sobre fila | Devuelve la fila completa, sin proyección | `internal/cli/handlers.go:651` |
| `find` | `SELECT <todas las columnas> FROM t WHERE … ORDER BY <DefaultOrder> LIMIT n+1`; el `+1` detecta truncado con límite por defecto 1 000 | `internal/storage/sqlite/sqlite.go:283-288`, `internal/app/query.go:195` |
| Render de resultados | Serializa la fila completa tal como llega del almacén | `internal/app/query.go:288-298` |
| Orden | Solo `DefaultOrder`: PK ascendente o todas las columnas si no hay PK | `internal/storage/storage.go:95-107`, `internal/storage/sqlite/sqlite.go:234-239` |
| Keyset | El predicado admite direcciones mixtas y `orderClause` ya valida que la columna exista | `internal/storage/sqlite/sqlite.go:367-393`, `sqlite.go:539-541` |
| Tablas sin identidad estable | Solo se entrega la primera página; decisión D-12 | `internal/app/query.go:200` |
| Cursor | Payload `{v,mount,table,filters,order,search,last_key}` firmado con HMAC y vinculado a la consulta | `docs/DESIGN.md` §7, `internal/app/query.go:252-269` |
| ETag | SHA-256 de tabla + `schema_version` + clave + **todos** los campos del catálogo; `List` rechaza proyección a propósito (ADR-006) | `internal/storage/sqlite/sqlite.go:574-593`, `sqlite.go:229-231` |
| Políticas | `allowMatches` casa por segmentos de prefijo del nombre; `fieldAllowed` resuelve el grant de **tabla**, no hay grants por columna; `--where` ya pasa por `fieldAllowed` | `internal/app/policy.go:33-51`, `policy.go:58-63`, `internal/app/query.go:150-152` |
| Catálogo de errores | Cerrado, códigos `E_*`; hoy una columna desconocida sale como `E_USAGE` con mensaje `unknown column` | `internal/cli/errors.go:18-31`, `internal/app/where.go:68-69`, `internal/storage/sqlite/sqlite.go:318-319` |
| Mutaciones | `touch` con política, lectura previa del catálogo, dry-run y auditoría; `INSERT`/`touch` en almacén; duplicado → `E_CONSTRAINT` | `internal/app/mutation.go:49-96`, `internal/storage/sqlite/mutation.go:248-293` |
| ACM: esquema | `meta`, `sessions`, `observations` con `UNIQUE(content_hash)`, `checkpoints`, `preferences` | `examples/agent-continuity/schema.sql:11,18,34,59,66,89` |
| ACM: compensaciones | Ventana de `started_at` de 14 días, orden local por UUIDv7 mayor, filas completas truncadas, ausencia de conteos, consolidación `touch`→`E_CONSTRAINT`→`write` | `examples/agent-continuity/skill/references/protocolos.md:51,54,118,125-141,148-155` |
| Benchmark | Fixture de 10 240 observaciones, `tokens = bytes/4`, objetivo ≤2 000 tokens; presupuesto desbordado en la recuperación en frío | `examples/agent-continuity/tests/benchmark.py:259,536,544,696`; evidencia previa en `scenarios/evidence/evidence-benchmark.md` (db-ai 0.2.0) |
| Skills publicadas | `SKILLS-AGP/src/db-ai/SKILL.md` y `SKILLS-AGP/src/agent-continuity/SKILL.md` + `references/protocolos.md` (copia casi literal del ejemplo ACM), versionadas por frontmatter y `CHANGELOG.md` del repo, con routers en `.skills/<skill>/SKILL.md` e índices en `llms.txt` y `docs/index.html` | `/home/alfredo/PROJECTS/SKILLS-AGP` |

**Diagnóstico.** Lo que falta no es capacidad de acceso, sino **forma de la respuesta** y
**una operación atómica**:

1. Toda lectura devuelve filas completas; el coste en tokens lo fija el ancho de la tabla, no
   la pregunta (`handlers.go:651`, `query.go:288-298`).
2. El orden es siempre el interno; no hay forma de pedir «lo último primero» ni de alinear
   orden con cursor desde el cliente (`sqlite.go:234-239`).
3. No hay conteo: quien necesita «cuántos» debe paginar (`protocolos.md:125-141`).
4. Consolidar una observación exige leer, chocar con `E_CONSTRAINT` y reintentar con `write`
   (`protocolos.md:148-155`), una carrera de dos escrituras.
5. El vocabulario de `ops` de §9 no incluye ninguna de las operaciones anteriores y las
   skills copian la prosa del ejemplo, de modo que cualquier cambio se desincroniza.

Alcance de este ciclo: REF-01 a REF-05, sin FTS, sin vistas y sin DDL.

## 2. REF-01 Proyección de columnas (`--fields`)

**Problema.** `find` y `cat <fila>` devuelven todas las columnas del registro
(`sqlite.go:283-288`, `handlers.go:651`) y el render no recorta (`query.go:288-298`). En el
ACM, `observations` es la tabla ancha y el presupuesto de 2 000 tokens se consume en campos
que la reanudación no lee.

**Propuesta.**

- Flag `--fields` en `find` y `cat <mount>.<tabla>.<fila>`. Gramática: lista de nombres de
  columna separados por coma, sin espacios garantizados, sin expresiones, alias, comodines ni
  funciones. Ejemplo: `--fields "id,content_hash,title"`.
- **La proyección se aplica solo en el render** (`query.go:288-298`). El almacén sigue
  leyendo la fila completa y `List` sigue rechazando proyección (`sqlite.go:229-231`): no se
  toca el camino que produce el ETag. Es una **optimización del volumen de salida y del
  contexto del agente**, no una proyección ejecutada por SQLite: la fila llega íntegra del
  almacén y el recorte se hace al serializar (decisión del PO, 2026-11-20).
- **El ETag no depende de `--fields`** (ADR-006, `sqlite.go:574-593`): dos lecturas de la
  misma fila con distinta proyección comparten ETag y `--if-match` sigue siendo válido.
  Consecuencia documentada: `--fields` no debilita la precondición de escritura.
- **Identidad preservada.** Las columnas de la clave (PK; si no hay PK, ninguna) se emiten
  siempre aunque no se pidan, porque el cursor siguiente se construye a partir de
  `last_key` (`query.go:252-269`). El recorte afecta al payload de presentación, no al
  registro leído. En una tabla sin PK no hay cursor que conservar (D-12), así que el render
  proyecta libremente.
- **Validación contra el catálogo** con `HasColumn`, la misma comprobación que usan `where`
  y `order`. Lista vacía, nombre repetido o columna inexistente → **`E_USAGE`** con mensaje
  `unknown column: <nombre>`, coherente con `where.go:68-69` y `sqlite.go:318-319`. No se
  añade código nuevo al catálogo cerrado (`errors.go:18-31`).
- **Políticas.** Proyectar no amplía acceso: el grant es por tabla (`policy.go:58-63`), de
  modo que la lectura sigue exigiendo el mismo permiso y devuelve el mismo error de política
  o montaje que hoy. No hay grants por columna y este ciclo no los introduce.
- **Cursor.** `--fields` entra en el payload y, por tanto, en el HMAC y en la vinculación
  (`query.go:252-269`). Un cursor emitido con una proyección y reutilizado con otra (o sin
  ella) falla con el error de vinculación ya definido en §10; no se sirve una página con
  forma distinta a la prometida.
- **Sin proyección implícita.** Sin `--fields` la salida debe ser **idéntica byte a byte** a
  la de hoy; ninguna heurística añade ni quita columnas.
- `cat` sobre tabla sigue en `E_UNSUPPORTED` (`handlers.go:615`): fuera de alcance.

**Contrato.** El sobre no cambia; cada fila es un objeto JSON con el subconjunto pedido más
la clave. Orden de claves irrelevante para el consumidor; se emiten en el orden del catálogo.

**Pruebas mínimas.** (i) golden de `find` sin `--fields` sin diferencias; (ii) `--fields`
válido recorta y conserva la PK; (iii) columna inexistente → `E_USAGE`; (iv) tabla denegada
por política → mismo error con y sin `--fields`; (v) cursor con proyección distinta → error
de vinculación; (vi) tabla sin PK proyecta sin prometer paginación.

## 3. REF-02 Ordenación (`--order`)

**Problema.** El orden está cableado a `DefaultOrder` (`storage.go:95-107`,
`sqlite.go:234-239`). El ACM necesita «últimas observaciones primero» y hoy lo resuelve
ordenando en local lo que ya trajo (`protocolos.md:54,118`), lo que rompe con páginas
truncadas.

**Gramática.** `--order "col:asc,col2:desc"`:

- lista separada por comas; cada término es `columna:dirección`;
- dirección opcional, por defecto `asc`;
- solo identificadores del catálogo y dos direcciones; **nada de SQL, expresiones,
  funciones, `COLLATE`, `NULLS` ni índices por posición**;
- columna o dirección inválida → `E_USAGE` (misma decisión que en REF-01).

**Semántica del orden.**

- Las columnas del usuario van primero, en el orden pedido.
- Desempate determinista: el resto de la PK en su orden (o el `DefaultOrder` completo si no
  hay PK). Con PK el orden resultante es **total**, requisito del keyset.
- El predicado keyset ya soporta direcciones mixtas (`sqlite.go:367-393`) y `orderClause`
  valida la columna (`sqlite.go:539-541`); el trabajo es componer la lista normalizada y
  usarla tanto en el `ORDER BY` como en el predicado.
- **Sin PK no hay orden total**: se mantiene la decisión D-12 y se entrega solo la primera
  página (`query.go:200`), sin cursor. `--order` no convierte en paginable una tabla que no
  lo era.
- **Nulos.** Decisión del PO (2026-11-20): **`NULLS LAST` en ambas direcciones** — una fila
  sin fecha no desplaza a las que tienen información de recencia, ni en ascendente ni en
  descendente; sin opciones configurables. Si la versión de SQLite enlazada soporta
  `NULLS LAST`/`FIRST` (≥ 3.30) se usa directamente; en caso contrario, la expresión
  equivalente. **El predicado keyset debe replicar exactamente el mismo criterio de nulos en
  ambos lados de la comparación**: las filas con NULL y un `last_key` con NULL deben
  ordenarse de forma coincidente, o la paginación salta o repite filas. Interacción
  NULL-cursor cubierta expresamente en la batería adversarial (P7, tk-42).
- **Tipos mixtos.** SQLite admite clases de almacenamiento distintas en una columna. Se
  acepta el orden nativo de SQLite (`NULL` < `INTEGER`/`REAL` < `TEXT` < `BLOB`) sin
  coerción: `--order` no castea, y la prueba documenta el resultado observado.

**Cursor.** La lista de orden **resuelta** (usuario + desempate, con direcciones) se guarda
en el payload firmado, no la cadena tecleada. Así un cursor en vuelo no cambia de semántica
si mañana cambia el `DefaultOrder` de la tabla, y reutilizarlo con otro `--order` (u omitirlo)
produce el error de vinculación de §7/§10. `--order` y `--fields` son independientes y ambos
quedan ligados al cursor.

**Pruebas mínimas.** Orden total verificable con PK; truncado con `LIMIT n+1` intacto;
direcciones mixtas; nulos en ambos sentidos; tabla sin PK → primera página; cursor con orden
distinto → error; columna inexistente → `E_USAGE`.

## 4. REF-03 Recuento (`--count`)

**Problema.** No existe forma de saber cuántas filas cumplen un filtro sin paginar; el ACM lo
declara como limitación (`protocolos.md:125-141`) y la recuperación en frío paga el coste.

**Propuesta.**

- `--count` en `find`: mismo `--where` (incluidos los filtros de `grep` cuando aplique) y
  **las mismas políticas** que la lectura de esa tabla.
- Implementación: `COUNT(*)` con el mismo constructor de predicados, **sin materializar
  filas**; el plan de consulta puede usar índice si existe, pero no se exige.
- Salida compacta dentro del sobre estándar: `{"count": N}`, sin array de filas ni cursores.
- **Incompatible con `--cursor`**: contar una página concreta no tiene sentido y se rechaza
  con `E_USAGE`. **`--limit` se ignora** a efectos de recuento (no es error): `count` es
  siempre el total del conjunto filtrado. Ambas cosas son decisión (c) de §8.
- Los recuentos bajo políticas restrictivas devuelven exactamente el conjunto que devolvería
  `find`: si la tabla está denegada, error; si el filtro recorta, cuenta lo recortado.
- `--count` no altera el ETag ni interactúa con `--fields`.

**Pruebas mínimas.** `count` igual a `len(find)` sobre fixture pequeño, con y sin `grep`;
política restrictiva; `--count --cursor` → `E_USAGE`; `--count --limit 5` devuelve el total;
rendimiento aceptable sobre el fixture de 10 240 filas.

## 5. REF-04 Upsert (C-ACM-04)

**Problema.** Consolidar exige `touch`, recibir `E_CONSTRAINT` y reintentar con `write`
(`protocolos.md:148-155`): dos escrituras, una carrera y un resultado ambiguo para el agente
(no sabe si creó o actualizó).

**Propuesta.**

- Nuevo subcomando `upsert <mount>.<tabla> --data '{…}' [--if-match <etag>] [--apply]`.
- **Resultado explícito** en el sobre: `{"result": "created" | "updated"}` más el ETag de la
  fila resultante. El agente deja de inferir por código de error.
- Mecanismo: `INSERT … ON CONFLICT(<destino>) DO UPDATE SET …` en una sola sentencia, con
  `BEGIN IMMEDIATE` para serializar escritores concurrentes.
- **Destino de conflicto**: la PK si existe; si no, una `UNIQUE` declarada y única en la
  tabla. No se acepta destino arbitrario por flags.
- **Conflicto incidental** en otra `UNIQUE` no declarada como destino → `E_CONSTRAINT` y
  rollback: nunca se actualiza una fila por una coincidencia que el cliente no pidió.
  Varias `UNIQUE` conviven, pero el upsert solo resuelve una; el coste (una sentencia, sin
  lectura previa) se declara en el diseño.
- Sin PK ni `UNIQUE` → sin identidad, no hay upsert (`E_UNSUPPORTED`).
- **`--if-match` obligatorio en la rama de actualización** (decisión del PO, 2026-11-20):
  una actualización de un registro existente exige un ETag válido; **la ausencia de ETag no
  autoriza una actualización incondicional** y se rechaza con el error de precondición. Un
  ETag obsoleto produce `E_CONFLICT`. Si se proporciona ETag para actualizar y el registro
  ya no existe, **no se recrea silenciosamente**: error de precondición, sin inserción.
- **Permisos por rama** (decisión del PO): la rama de inserción exige los permisos de
  `touch` y la de actualización los de `write`; misma política, mismo grant y **ninguna vía
  indirecta** para eludir las precondiciones de concurrencia ni las políticas de acceso.
- **Resultado explícito y honesto con dry-run**: el sobre distingue la simulación de la
  operación aplicada (`applied: false` en dry-run frente a `applied: true` al aplicar) junto
  al resultado de rama (`created`/`updated`) y el ETag de la fila resultante.
- **Dry-run** (`--apply` ausente) informa del resultado previsto y del plan sin escribir.
- **Auditoría** con la operación efectiva (`created`/`updated`), tabla, clave y actor; no se
  registra «upsert» genérico.
- **No elude garantías**: mismo grant de mutación que `touch`/`write`, misma validación de
  esquema y tipos, mismo dry-run y misma auditoría (`mutation.go:49-96`,
  `sqlite/mutation.go:248-293`).
- Coste declarado: una comprobación de catálogo y una sentencia; el índice del destino manda.

**Pruebas mínimas.** Insert crea (result `created`); segunda llamada actualiza (`updated`);
conflicto en `UNIQUE` no destino → `E_CONSTRAINT`; `--if-match` caducado → error de
precondición y sin escritura; `--if-match` con fila inexistente → error; dry-run no escribe;
auditoría refleja la rama real; política denegada → mismo error que `touch`.

**Nota de alcance.** No se toca `protocolos.md` hasta que el comando exista y esté
versionado; la skill mantiene hoy la secuencia `touch`→`E_CONSTRAINT`→`write`.

## 6. REF-05 Vocabulario `ops` y contratos (C-ACM-06)

Corrección documental aceptada en §14. Lista completa que debe figurar en §9:

| Categoría | Ops |
|---|---|
| Lectura | `cat`, `find`, `grep`, `export`, `ls`, `tree`, `stat`, `describe` |
| Mutación | `touch`, `write`, `cp`, `mv`, `rm`, `upsert` |
| Compuesto | `batch`, con la política evaluada **por elemento** |

Notas que acompañan la tabla en §9:

- `--fields` y `--count` **no añaden superficie de acceso**: son modificadores de `find` y
  `cat`, con las políticas por tabla ya vigentes (`policy.go:33-63`). El vocabulario de `ops`
  no crece con ellos.
- `upsert` es mutación y hereda las garantías de `touch`/`write` (§5).
- **Fuente canónica única**: `docs/DESIGN.md` (§3 superficie, §7 consultas y cursores, §8
  mutaciones y ETag, §9 políticas y `ops`, §10 errores). `CHANGELOG.md` refleja solo lo
  visible para el usuario. Las skills de `SKILLS-AGP` son **copias** y se regeneran o
  sincronizan desde DESIGN; hoy `src/agent-continuity/SKILL.md` y su `protocolos.md` son
  copia casi literal del ejemplo ACM, incluida la prosa que dice que no hay orden descendente
  (`protocolos.md:118-119`), que quedará falsa en cuanto se implemente REF-02.

## 7. Plan de paquetes y evidencias

Mapeo de tareas verificado por CLI contra el tablero (`tasks-ai show tk-35`): las 13
subtareas existen con su criterio observable; la correspondencia P↔tk de esta tabla es la
del tablero, no una propuesta.

| Paquete | Tarea | Contenido | Depende de | Evidencia exigida | Skills |
|---|---|---|---|---|---|
| P1 | tk-36 | Auditoría, diseño y plan (este documento) | — | Este documento + revisión independiente | — |
| P2 | tk-47 | `--fields` en `find` y `cat` fila; validación, cursor, pruebas | P1 | Golden sin flag, unit de `E_USAGE`, prueba de cursor y de política | — |
| P3 | tk-37 | `--order`; orden total con PK, nulos, tipos mixtos | P2 | Pruebas de orden/keyset/truncado, tabla sin PK | — |
| P4 | tk-39 | `--count` en `find` | P3 | Igualdad con `find`, `E_USAGE` con cursor | — |
| P5 | tk-46 | `upsert` con resultado explícito, `--if-match`, dry-run, auditoría | P4 | Pruebas de ramas, conflicto incidental, concurrencia | — |
| P6 | tk-38 | Vocabulario `ops` y contratos en DESIGN §9 + CHANGELOG | P1 (redacción); cierra tras P5 | DESIGN y CHANGELOG revisados | — |
| P7 | tk-42 | Pruebas adversariales del núcleo (orden con empates, asc/desc, paginación sin omisiones, cursores manipulados, proyección inválida, recuentos restrictivos, inyección SQL, upserts concurrentes, compatibilidad) | P5 | Suite adversarial + informe por caso | `software-testing` |
| P8 | tk-43 | Simplificación de los protocolos ACM (retirada de compensaciones, recuperación en frío en 6 pasos) | P2–P5 | Guion de reanudación reproducible | — |
| P9 | tk-40 | Skills db-ai y agent-continuity en `SKILLS-AGP` + routers e índices | P6–P8 | Diff de skills, `llms.txt`, `docs/index.html` | — |
| P10 | tk-44 | Benchmark sobre el fixture de 10 240 con los flags nuevos; evidencia nueva sin tocar la de 0.2.0 | P8–P9 | `tests/benchmark.py` ejecutado, informe en `scenarios/evidence/` | — |
| P11 | tk-45 | Validación de continuidad A→B con dos modelos distintos (M-ACM-05, presupuesto de contexto) | P9 | Registro A/B con modelos, condiciones y limitaciones | — |
| P12 | tk-41 | Preparación de la prueba de campo: procedimiento operativo y protocolo de observación | P10–P11 | Procedimiento operativo y protocolo del piloto | — |
| P13 | tk-48 | Gate final: informe, GO/NO-GO y propuesta de congelación funcional | P12 | Informe de gate con comandos y salidas | Revisión independiente |

Reglas del plan:

- El núcleo es P2→P3→P4→P5 y no se paraliza: comparten el payload del cursor y el render.
- P7 es adversarial y va **después** del núcleo; no sustituye a las pruebas de cada paquete.
- Las revisiones independientes se reservan a los paquetes críticos (P2, P3, P5) y al gate
  (P13), con los criterios `Risk 1+3` de la revisión de §8.
- `scenarios/evidence/evidence-benchmark.md` (db-ai 0.2.0) **no se reescribe**: la evidencia
  nueva va en fichero propio para que la comparación siga siendo posible.

## 8. Riesgos, regresiones y decisiones pendientes del PO

**Riesgos.**

1. **Cursor y estabilidad bajo `--order`.** Un orden total exige que el desempate cubra la
   PK; si se olvida una columna, el keyset repite o pierde filas en los empates. Mitigación:
   normalizar la lista en un único punto y probar empates explícitos.
2. **ETag bajo `--fields` con `--if-match`.** Si alguien «optimiza» proyectando también en el
   almacén, el ETag cambia y las escrituras fallan con `E_CONFLICT` falso. Mitigación:
   ADR-006 como prueba de regresión, no como comentario.
3. **Contratos y skills desincronizados.** El vocabulario de §9, la prosa de
   `protocolos.md:118-119` y las copias de `SKILLS-AGP` divergen si se actualizan por
   separado. Mitigación: P6 y P9 como puertas, con diff revisado.
4. **Upsert sobre varias `UNIQUE`.** El destino elegido no cubre el resto; un conflicto
   incidental debe fallar, no actualizar en silencio. Riesgo de expectativa equivocada en el
   cliente. Mitigación: contrato explícito y prueba dedicada.
5. **Tablas sin PK bajo `--order`.** El usuario puede esperar paginación que D-12 no permite.
   Mitigación: documentarlo en §7 y mantener la primera página sin cursor.
6. **Benchmark con evidencia fija.** Comparar contra 0.2.0 mezcla cambios de varias
   versiones. Mitigación: A/B con la misma versión de referencia y evidencia nueva separada.

**Decisiones resueltas por el PO (2026-11-20).** Aprueba el diseño y el plan con estas
precisiones:

1. (a) Gramática de `--order` (`col:asc,col:desc`, dirección por defecto `asc`) aprobada.
   **Nulos: `NULLS LAST` en ambas direcciones** (no `NULLS FIRST` en desc); sin opciones
   configurables. La interacción NULL-cursor keyset se verifica expresamente en P7.
2. (b) `--fields` en `find` y `cat` de fila, en el render; `cat` de tabla sigue en
   `E_UNSUPPORTED`. Documentada expresamente como optimización de volumen de salida y de
   contexto del agente, no como proyección ejecutada por SQLite; identidad conservada para
   navegación y paginación.
3. (c) `--count` aprobado: `COUNT(*)` sobre el conjunto autorizado y filtrado, incompatible
   con `--cursor` e independiente de `--limit`; semántica documentada claramente.
4. (d) `E_USAGE` reutilizado para columnas inválidas, sin ampliar el catálogo de errores.
5. (e) Upsert aprobado con garantías adicionales: permisos de `touch` en la inserción y de
   `write` en la actualización; actualización de registro existente siempre con ETag válido
   (sin ETag no hay actualización incondicional); ETag obsoleto → `E_CONFLICT`; ETag con
   registro desaparecido → error de precondición, sin recreación silenciosa; conflicto
   incidental en otra `UNIQUE` → `E_CONSTRAINT`; ninguna vía indirecta para eludir
   precondiciones o políticas; el resultado distingue dry-run de operación aplicada. Si el
   diseño introduce complejidad desproporcionada, se eleva al PO antes de implementar.
6. (f) Actualización coordinada de skills `db-ai` y `agent-continuity` y del ejemplo ACM,
   con fuente canónica única y verificación de divergencias.
7. (g) Versionado: incremento MINOR de db-ai y de las skills afectadas si se mantiene la
   compatibilidad; cualquier excepción se justifica conforme al versionado semántico.

Condiciones generales de ejecución: mantener el plan de 13 paquetes con trazabilidad
tasks-ai; priorizar REF-01 y REF-02; conservar las evidencias anteriores; revisión
adversarial independiente en P7; sin ampliar el alcance; objetivo de 2.000 tokens en la
recuperación en frío sin flexibilizar garantías; validación A→B con dos modelos; gate final
GO/NO-GO antes de la prueba de campo. Autorizada la implementación empezando por REF-01
(tk-47); sin commits, pushes, tags, releases ni instalación global.

Aprobado por el PO el 2026-11-20; implementación en curso por paquetes según §7, empezando
por REF-01 (tk-47).

## 9. Ajuste de alcance del PO (2026-11-20, rev. 2)

El PO redujo el ciclo para evitar sobreingeniería (KISS, YAGNI): la validación entre
modelos y las comprobaciones longitudinales pasan a la prueba de campo; la batería
adversarial monolítica de P7 se cubre con la adversarial por REF ya producida y verificada
más una pasada compacta al binario (registrada en las tareas); P12 se reduce a una nota
breve de uso integrada en la skill; el gate final queda limitado al informe breve con la
comparativa del benchmark. Se conservan las REF-01 a REF-04 ya implementadas y verificadas
en su totalidad (incluido el upsert con destino PK — el fallback UNIQUE queda fuera por
no tener direccionabilidad de fila en el VFS). MINOR de versión en db-ai (0.4.0) y en las
skills afectadas. Criterio de cierre del ciclo: versión funcional con pruebas en verde,
skills sincronizadas y ACM listo para la prueba de campo.
