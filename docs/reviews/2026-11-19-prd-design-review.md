# Revisión de contratos iniciales — PRD.md y DESIGN.md

Fecha: 2026-11-19. Estado: revisión cerrada — decisiones aprobadas por el PO el 2026-11-19 e incorporadas en los contratos v0.2.0. Método: prd-cartographer +
contraste técnico verificado en el entorno (Go 1.27.3, SQLite CLI 3.46.1, git 2.53.0,
gcc 13.2, CGO=1, WSL2).

**Veredicto de preparación: PARCIAL.** Los contratos no superan la puerta como línea base
sin resolver las decisiones D-1…D-20 y los huecos de trazabilidad de §0.

## 0. Huecos de método del PRD

- [HUECO-TRAZABILIDAD] PRD §1–§3: no hay problema enunciado con evidencia ni fuentes
  inventariadas; la visión describe la solución antes que la necesidad.
- [HUECO-TRAZABILIDAD] PRD §3: los cuatro «usuarios» son roles definidos por la propia
  solución, sin evidencia ni marca de hipótesis.
- [HUECO-TRAZABILIDAD] PRD §2 y §7: no hay ninguna métrica de resultado con definición,
  método, línea base, objetivo y plazo; los NFR son restricciones, no métricas.
- [HUECO-TRAZABILIDAD] PRD §5: los FR no están vinculados a persona, objetivo o riesgo;
  la tabla no tiene columna de trazabilidad.
- [HUECO-TRAZABILIDAD] PRD: faltan propietario, changelog y condiciones de revisión.

## 1. Consistencia interna

- [RIESGO] PRD §5 FR-13 vs §9: la cláusula de lectura de FR-13 se asigna a M2, no a M1;
  M1 entrega lectura sin política de campo.
- [AMBIGUEDAD] PRD §2 vs §5 FR-04: «sesiones aisladas opcionales» no distingue función
  opcional del producto de uso opcional por el usuario.
- [AMBIGUEDAD] PRD §5 FR-06: la precondición de concurrencia no tiene sentido en INSERT.
- [AMBIGUEDAD] PRD §5 FR-16 vs DESIGN §3: «selección acotada» sin mecanismo en `export`.
- [RIESGO] PRD §8 FR-08 vs DESIGN §3: «autorización» se usa para la política de FR-13 y
  para `--apply`; un agente puede confundir confirmación con control de acceso.
- [AMBIGUEDAD] PRD §7 NFR-07 vs DESIGN §4: «probar» BLOB/REAL/NULL no distingue soportar
  de rechazar con error claro.
- [RIESGO] DESIGN §5: «no analizar SQL de restricciones» contradice autorizar vistas
  (demostrar dependencias exige analizar su SQL).
- [RIESGO] DESIGN §8 vs §10: el ETag de fila completa contradice `cat` con `etag` sobre
  filas parciales; emitirlo sobre filas parciales sería un oráculo de valores ocultos.
- [RIESGO] DESIGN §3: dos modelos de dirección (ruta VFS y nombre de tabla) sin regla de
  precedencia.
- [HUECO-TRAZABILIDAD] DESIGN §3 vs §7: `--cursor` en tablas sin orden total no definido.
- [HUECO-TRAZABILIDAD] DESIGN §6 vs §7: `Query` no puede expresar orden, colación ni
  proyección que el propio documento exige.
- [RIESGO] DESIGN §6: `Row.Fields map[string]Value` — el recorrido de un mapa en Go no
  es determinista; el tipo invita a romper NFR-01.
- [RIESGO] PRD §10 y DESIGN §14: dos registros de decisiones pendientes sin identificadores
  ni referencia cruzada; la trazabilidad de decisiones no es auditable.

## 2. Trazabilidad PRD→DESIGN

Cubiertos completos: FR-01…FR-10, FR-15, NFR-02, NFR-04, NFR-06, NFR-07, NFR-08.
Parciales: FR-11 (esquema de error, formato por defecto, ayuda), FR-12 (esquema batch,
límite 100 sin base), FR-13 (cláusula de lectura en M2), FR-14 (`request_id`, redacción),
FR-16 (selección), NFR-01 (orden de `ls`/`tree`/`mounts`/`describe`), NFR-03 (cláusulas
de conflicto, ver R-1), NFR-05 (cifras no replicadas en DESIGN).
Decisiones de diseño sin requisito que las justifique: límite batch 100 (§8), alias
`[A-Za-z0-9_-]+` (§4), rutas XDG y formato `config.json` con `allow[{ops,prefix}]` (§9),
numeración de códigos de salida 2–9 y `--apply` (§3), exclusión de tablas virtuales y
vistas solo lectura (§5).

## 3. Ambigüedades que bloquean al implementador (D-1…D-18)

1. **D-1** Dirección canónica: ¿ruta VFS o nombre de tabla para
   `describe`/`find`/`grep`/`touch`/`export`? (PRD §6 vs DESIGN §3)
2. **D-2** ¿`export` acepta filtros o exporta tabla completa? (FR-16)
3. **D-3** ¿Forma exacta de salida de `cat` de campo, `stat`, `find`, `grep`? Sin esto no
   hay golden test posible.
4. **D-4** ¿Nombre, ubicación y garantía de integridad del cursor? Si la PK está prohibida,
   un cursor base64 la revela.
5. **D-5** ¿Esquema JSON de error y fuente de verdad para el agente: código de salida o
   `error.code`? ¿Se separa `E_COMMIT_UNKNOWN` (comparte salida 9 con `E_INTERNAL`)?
6. **D-6** ¿ETag de origen o de destino en `cp`?
7. **D-7** ¿Qué mutaciones exigen `--apply`?
8. **D-8** ¿Esquema del array de `batch`?
9. **D-9** ¿Cómo se registra un recurso cuando la PK es dato personal (registrar, hashear u
   omitir)?
10. **D-10** ¿`request_id` lo aporta el cliente o lo devuelve el servidor?
11. **D-11** ¿Sintaxis de valores tipados en `--where`?
12. **D-12** ¿Comportamiento de `ls`/`tree`/`cat`/`stat` sobre tablas no navegables?
13. **D-13** ¿1000/16 MiB/profundidad 4/5 s son defaults, máximos o ambos?
14. **D-14** ¿JSON versionado por defecto o texto humano?
15. **D-15** ¿Forma canónica exacta de la clave compuesta?
16. **D-16** ¿Variante base64 y comportamiento ante REAL no finito?
17. **D-17** ¿Puede un agente ejecutar `mount`, o es operación reservada al operador?
18. **D-18** ¿Contrato de sesión sin `--session` y ubicación de sus ficheros?

## 4. Contradicciones y riesgos técnicos

- **R-1 (máximo)** Cláusulas `ON CONFLICT REPLACE/IGNORE` declaradas en esquema: verificada
  en SQLite 3.46.1, sustituyen filas en INSERT normal sin error; rompe en silencio
  «nunca sobrescribir» (FR-07) y NFR-03; no detectable con `table_xinfo`/`index_list`.
- **R-2** Motor del driver distinto del CLI verificado: modernc embebe SQLite 3.53.4,
  mattn 3.45.1; comprobar PRAGMAs contra el CLI no verifica el motor empaquetado.
- **R-3** Garantía de solo lectura frágil: `-shm` en WAL, recuperación de diario caliente,
  checkpoint al cerrar `rw` puede alterar el hash del fichero.
- **R-4** PK TEXT admite múltiples NULL en tablas rowid (verificado): «PK ⇒ NOT NULL» es
  falso; debe ser criterio de aceptación explícito.
- **R-5** Cursor como canal lateral: si la PK está prohibida, el cursor y el orden la
  revelan (base64 no es cifrado).
- **R-6** Código de salida 9 compartido por `E_INTERNAL` y `E_COMMIT_UNKNOWN`: un agente no
  puede decidir entre reintentar y no reintentar.
- **R-7** ETag global por `schema_version`: cualquier DDL invalida todos los ETags; ruido
  de `E_CONFLICT` para agentes.
- **R-8** «Durable» de la auditoría sin política de fsync/rotación/permisos.
- **R-9** Reutilización de PK INTEGER sin `AUTOINCREMENT`: una ruta borrada puede
  «resucitar» apuntando a otra fila.
- **R-10** `grep`/`find` sin índice + timeout: el resultado depende del reloj en el límite;
  «sin resultados» y «sin tiempo» deben ser distinguibles para el agente.
- **R-11** Escritura sobre filas parcialmente legibles: sin lectura completa no hay ETag;
  coherente con integridad pero no está dicho.

## 5. Contraste con el entorno

| Afirmación | Verificación | Resultado |
|---|---|---|
| Go | `go version` | go1.27.3 linux/amd64 |
| CGO para `-race` | `go env CGO_ENABLED`; `which gcc` | 1; gcc 13.2 presente |
| SQLite CLI | `sqlite3 --version` | 3.46.1 2024-08-13 |
| PRAGMAs y funciones (§5, §7, §8) | ejecución sobre fixture | Todos disponibles |
| `mode=ro` sin crear fichero / escritura denegada | URI `file:…?mode=ro` | Cumple el criterio |
| Driver pure Go | caché de módulos | modernc v1.60.1, embebe SQLite 3.53.4 |
| Driver CGO | caché de módulos | mattn v1.14.22, embebe SQLite 3.45.1 |

- [DESVIACIÓN-ENTORNO] Versión de motor no fijada: la compatibilidad con «SQLite 3.46.1»
  describe el CLI, no el producto (decisión abierta en DESIGN §2).
- [DESVIACIÓN-ENTORNO] Windows nativo sin ruta de configuración declarada (NFR-06 lo difiere).
- Ninguna herramienta mencionada falta.

## 6. Viabilidad global

El stack (Go + `database/sql` + SQLite, CLI sin servidor) es implementable con lo
instalado. La dificultad no está en el acceso a SQLite sino en: (1) fidelidad al esquema
real — riesgo alto, R-1/R-4 y clases de esquema rechazadas en el MVP; (2) atomicidad y
recuperación — riesgo medio-alto, contrato del estado incierto sin cerrar (R-6, R-8);
(3) contrato de agente incompleto — riesgo alto para el objetivo: sin formas de salida,
cursor y errores especificados, el determinismo de NFR-01 no es demostrable.

M2 concentra el riesgo real (mutaciones + política + auditoría + ETag + dry-run en un
solo WP). M1 es el más fácil pero expone el hueco de FR-13. M3 añade superficie con
límites no aprobados.

## 7. Principios obligatorios

| Principio | Hueco principal |
|---|---|
| Determinismo | Orden de `ls`/`tree`/`mounts` sin definir; `Row.Fields` mapa; timeout vs resultados |
| Minimalismo | MVP incluye sesiones, batch, política, auditoría, ETags, dry-run; límites añadidos sin PRD |
| Agent-first | Sin esquema de error, sin formato por defecto, sin descubrimiento de capacidades, `request_id` no expuesto |
| Seguridad por defecto | FR-13 de lectura en M2; `mount` sin control de actor (D-17); redacción de auditoría (D-9) |
| Integridad de datos | R-1; R-8; R-9; DEFAULT no deterministas no tratados |
| Trazabilidad | FR sin persona/objetivo; decisiones sin ID en dos registros; WPs no enumeran FR |

## 8. Decisiones del PO (consolidadas)

- **D-19** ¿Se aprueba la línea base de los contratos: propuestas de PRD §10 y ADR de
  DESIGN §14? Sin esta aprobación los contratos no son línea base.
- **D-20** ¿Se acepta que «universal» es visión y el MVP rechaza esquemas sin PK estable,
  BLOB/REAL/NULL en identidad, vistas y tablas virtuales?
- **D-1…D-18** como en §3.

## No verificado

- No existe `go.mod` ni código: `go test`, `go vet` y el build de PRD §9 no son ejecutables.
- El motor embebido de los drivers (3.53.4 / 3.45.1) no verificado; las comprobaciones
  SQLite se hicieron con el CLI 3.46.1.
- R-3 (diario caliente en `mode=ro`, `-shm`) es inferencia documentada, no prueba.
- Sin bases reales del usuario en el repositorio: R-1/R-4 verificados en fixtures.
