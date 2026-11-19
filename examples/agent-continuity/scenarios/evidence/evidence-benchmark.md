# Evidencia — benchmark de métricas ACM con fixture de 10 000 observaciones

- **Fecha de ejecución:** 2026-11-19T20:26:22Z (UTC).
- **Binario medido:** `db-ai` compilado del árbol de trabajo con
  `go build -o /tmp/opencode/db-ai ./cmd/db-ai`.
  `db-ai version` →
  `{"version":"0.2.0","commit":"unknown","state":"dev"}`.
- **Hardware:** AMD Ryzen 7 5825U with Radeon Graphics, 16 vCPU visibles,
  `MemTotal: 7 573 160 kB` (~7,2 GiB), kernel
  `6.18.33.2-microsoft-standard-WSL2` (x86_64).
- **Alcance:** M-ACM-01 a M-ACM-04 medidos. M-ACM-05 no se mide aquí: es la
  prueba con agentes reales de P6 y queda **PENDIENTE**.
- **Objetivos:** los del PRD §7, sin cambios. M-ACM-03 sigue en 2 000 tokens; el
  guion por defecto queda acotado por `topic_key` y `pref_key` (decisión del PO,
  2026-11-19) y se re-mide en la sección 8.

> Las secciones 1–7 conservan como historia la medición anterior con el guion
> completo. La sección 8 re-mide M-ACM-03 con el guion acotado y la tabla final
> muestra ambos guiones con su veredicto.

## Método

1. **Fixture reproducible.** `tools/gen-fixture.py` aplica `schema.sql` y crea
   la base con un generador determinista (`--seed 20261119`). Cada fila usa el
   algoritmo de `content_hash` de DESIGN §5.2 importando el helper canónico
   `tools/content-hash.py`; el propio generador recalcula 25 hashes al final y
   aborta si no coinciden. El instante final de la ventana lo fija `--now`
   (por defecto, el momento de generar); la pareja `(seed, now)` reproduce la
   base. UUIDv7 textuales con la marca de tiempo en los 48 bits altos.
2. **Arnés.** `tests/benchmark.py` monta la base como alias `mem-bench` en una
   configuración `XDG_CONFIG_HOME` aislada y ejecuta el guion de reanudación de
   `protocolos.md` §1.4 y §3.6 con `db-ai` real. La medición de esta sección usa
   el guion completo anterior; la sección 8 usa el guion acotado que lo
   sustituye por defecto:
   ventana de sesiones de 14 días (límite 50) → checkpoint activo de la sesión
   más reciente (`session_id` + `status=active`, límite 5) → observaciones de esa
   sesión (límite 50) → decisiones activas (límite 10) → preferencias activas
   (límite 20). Se cuentan las invocaciones y se suma el stdout; el detalle
   redundante con `cat` no forma parte del guion.
3. **Tiempo de pared.** `time.perf_counter()` alrededor de cada proceso `db-ai`;
   incluye el arranque del proceso. 100 repeticiones medidas tras 3 de
   calentamiento excluidas. Percentil por rango más cercano.
4. **Tokens.** Estimación declarada `bytes/4` sobre el stdout UTF-8; stderr
   excluido. No es un conteo del tokenizador de un modelo.
5. **M-ACM-04.** Diez capturas consolidadas en una **copia** de la base
   (`capture.db`), para no alterar el fixture de reanudación: por captura, un
   `touch` y una verificación posterior con `cat`, comprobando que el título
   leído coincide con el capturado. `content_hash` calculado con el helper
   canónico. Las invocaciones del helper y de la apertura de sesión no cuentan.
6. **grep y find filtrado.** 20 repeticiones de cada uno sobre las 10 240 filas.

## 1. Fixture

Cuenta exacta leída con `sqlite3` sobre
`/tmp/opencode/acm-bench/memory.db`:

| Tabla | Filas |
|---|---|
| `meta` | 4 |
| `sessions` | 1 024 |
| `observations` | **10 240** |
| `checkpoints` | 1 024 (todos activos) |
| `preferences` | 12 activas |

- Reparto de `kind`: discovery 3 084, learning 2 109, pattern 1 529,
  architecture 1 162, bugfix 1 129, decision 1 227 (40 temas con una única
  decisión activa; las anteriores quedan `superseded`).
- Estados de `observations`: 8 133 `active`, 1 187 `superseded`, 549
  `needs_review`, 371 `obsolete`.
- Ventana de 14 días: **39 sesiones** (cabe en el límite 50 sin truncar).
- `PRAGMA foreign_key_check` = 0 filas; `PRAGMA integrity_check` = `ok`.
- Fichero: 11 268 096 bytes.

## 2. Reanudación (M-ACM-01 y M-ACM-03) — 100 repeticiones

Tiempos de pared por invocación, en ms:

| # | Operación | Llamadas | p50 | p95 | p99 | máx | Filas | truncated | Bytes/página |
|---|---|---|---|---|---|---|---|---|---|
| 1 | `find /mem-bench/sessions --where 'started_at>=<corte 14d>' --limit 50` | 100 | 7,39 | 8,74 | 9,68 | 9,87 | 39 | false | 30 458 |
| 2 | `find /mem-bench/checkpoints --where 'session_id=…' --where 'status=active' --limit 5` | 100 | 7,11 | 8,33 | 9,01 | 9,07 | 1 | false | 1 331 |
| 3 | `find /mem-bench/observations --where 'session_id=…' --limit 50` | 100 | 6,16 | 7,25 | 8,06 | 8,12 | 10 | false | 15 035 |
| 4 | `find /mem-bench/observations --where 'kind=decision' --where 'status=active' --limit 10` | 100 | 6,63 | 8,07 | 8,98 | 9,20 | 10 | **true** | 18 519 |
| 5 | `find /mem-bench/preferences --where 'status=active' --limit 20` | 100 | 6,19 | 7,67 | 8,31 | 8,33 | 12 | false | 13 295 |
| | **Total por reanudación** | 100 | **34,12** | **37,29** | **38,68** | **39,44** | | | **78 638** |

- **Invocaciones por reanudación: 5.** El conjunto mínimo con sesión conocida
  (sin la ventana de sesiones) son **4** `find`.
- **stdout por reanudación: 78 638 bytes** (idéntico en las 100 repeticiones;
  rango 78 638–78 638). Estimación declarada: **19 660 tokens** (`bytes/4`).
- **Conjunto mínimo de 4 `find`: 48 180 bytes ≈ 12 045 tokens.**
- La página de decisiones activas llega a 10 filas con 40 decisiones activas en
  la base: `meta.truncated = true`. Las demás páginas no truncan.
- Base pequeña (seed de demostración, 2 sesiones / 6 observaciones / 3
  preferencias): el mismo guion cuesta **6 invocaciones** (la sesión más
  reciente no tiene checkpoint y hay que probar la siguiente) y **14 823 bytes
  ≈ 3 706 tokens**. La evidencia de continuidad midió el conjunto mínimo de la
  base pequeña en 7 729 B ≈ 1 933 tokens.

**M-ACM-01: SUPERADO.** 5 ≤ 6 invocaciones.

**M-ACM-03: FALLIDO contra el objetivo de 2 000 tokens.** 19 660 tokens
estimados en el guion completo y 12 045 en el conjunto mínimo: ambos superan el
objetivo y también el umbral de fracaso (> 6 000 tokens). La causa medida es la
misma que en la base pequeña, agravada por el tamaño: `find` devuelve filas
completas y db-ai las envuelve campo a campo. El desglose por página es 30 458 B de ventana de sesiones,
1 331 B de checkpoint, 15 035 B de historial, 18 519 B de decisiones y
13 295 B de preferencias. El objetivo no se toca: el resultado es FALLIDO.
Nota de comparabilidad: el método de M-ACM-01 fija un escenario de tres
decisiones activas y dos preferencias; este fixture tiene 40 y 12, así que la
cifra mide el coste real de las consultas del protocolo sobre 10k, no el
escenario mínimo del PRD. El conjunto mínimo sigue fallando con esa salvedad.

## 3. M-ACM-02 — latencia p95 por invocación

Sobre las 500 invocaciones de lectura del guion (5 por repetición × 100):

| Estadístico | ms |
|---|---|
| p50 | 6,77 |
| **p95** | **8,35** |
| p99 | 9,07 |
| máximo | 9,87 |

**M-ACM-02: SUPERADO.** p95 8,35 ms ≤ 150 ms (el peor p95 por operación fue
8,74 ms en la ventana de sesiones).

## 4. M-ACM-04 — captura consolidada con verificación posterior

- Guion por captura: `touch` de la observación (1 invocación) + `cat` de la fila
  para verificar el título (1 invocación).
- Invocaciones por captura en 10 capturas: `[2, 2, 2, 2, 2, 2, 2, 2, 2, 2]`;
  **máximo 2**. Verificadas correctamente 10/10.
- No se cuenta la recuperación de un duplicado exacto, conforme a la definición
  del PRD §7.

**M-ACM-04: SUPERADO.** 2 ≤ 3 invocaciones e incluye la verificación posterior
que exige la definición (la evidencia de continuidad solo registraba la
escritura, sin verificación).

## 5. Paginación

| Operación | Límite | Filas | `meta.truncated` |
|---|---|---|---|
| Ventana de sesiones (14 días) | 50 | 39 | false |
| Checkpoint activo por sesión | 5 | 1 | false |
| Observaciones de la sesión | 50 | 10 | false |
| Decisiones activas | 10 | 10 | **true** |
| Preferencias activas | 20 | 12 | false |
| `grep` literal "serializa" | 20 | 20 | **true** (1 684 coincidencias en total) |
| `find` filtrado `kind=decision status=active` | 20 | 20 | **true** |

La página de decisiones se trunca con 40 decisiones activas; el agente debe
acotar por `topic_key` o paginar. La ventana de 14 días cabe en 50 con este
fixture; con más sesiones por día truncaría y el protocolo P3 exige acortar el
corte y repetir.

## 6. Recuperación del checkpoint reciente (procedimiento de P3)

- Sesión elegida por el guion (UUIDv7 mayor de la ventana con checkpoint):
  `01a1f3a7-21f5-77eb-b5fa-d8164070402b`.
- Sesión esperada por consulta directa a SQLite (la más reciente con checkpoint
  activo): `01a1f3a7-21f5-77eb-b5fa-d8164070402b`.
- Checkpoint recuperado: `01a1f3d0-54d6-76c7-b024-5d85ece01291`; esperado:
  `01a1f3d0-54d6-76c7-b024-5d85ece01291`.
- **Coincidencia: SÍ**, con 2 `find` acotados y sin recorrer páginas.

## 7. grep literal y find filtrado sobre 10 240 filas

| Operación | Llamadas | p50 (ms) | p95 (ms) | p99 (ms) |
|---|---|---|---|---|
| `grep /mem-bench/observations "serializa" --limit 20` | 20 | 7,07 | 8,26 | 8,50 |
| `find /mem-bench/observations --where 'kind=decision' --where 'status=active' --limit 20` | 20 | 7,25 | 8,95 | 9,30 |

Ambas por debajo de 10 ms de pared, incluido el arranque del proceso. `grep` es
lineal sobre 10 240 filas y aun así no domina el coste frente al proceso.

## 8. Re-medición M-ACM-03 (guion acotado por topic_key)

La decisión del PO del 2026-11-19 cambia el guion por defecto: decisiones activas
solo del `topic_key` de la tarea en curso y preferencias solo por las claves
aplicables, en lugar de todas las activas. Sesiones y checkpoint se mantienen
(ventana de 14 días y `session_id`). El objetivo de M-ACM-03 **no cambia**: sigue
en 2 000 tokens.

### Método

1. **Fixture idéntico.** Se regeneró con `tools/gen-fixture.py --seed 20261119
   --now 2026-11-19T20:26:16Z`, la misma pareja `(seed, now)` de la sección 1:
   10 240 observaciones, 1 024 sesiones, 1 024 checkpoints, 12 preferencias y 39
   sesiones en la ventana de 14 días. El fichero mide 11 268 096 bytes; el
   arnés lo reutilizó con `--reuse-fixture`.
2. **Guion acotado.** `tests/benchmark.py` ejecuta, con `db-ai` real:
   ventana de sesiones (14 días, límite 50) → checkpoint activo por `session_id`
   (límite 5) → decisiones `kind=decision`, `status=active` y `topic_key` de la
   tarea, límite 5 → una invocación por `pref_key` aplicable
   (`commit/approval`, `release/no-commit`), `status=active`, límite 2. El
   historial de la sesión no forma parte del guion por defecto: se pide solo si
   no hay checkpoint o si la pregunta lo exige.
3. **Tarea de referencia.** `bootstrap/schema-ddl`, derivada del `goal` de la
   sesión elegida por el propio guion; no se consulta fuera de la memoria.
4. **Variantes medidas.** (a) en frío, sin sesión conocida: ventana + checkpoint;
   (b) handoff, con `session_id` conocido: sin ventana; (c) sin `topic_key`:
   decisiones en página mínima (límite 3) con aviso de `truncated`; (d) sin
   claves de preferencia: página mínima (límite 3).
5. **Tiempo y tokens.** 100 repeticiones medidas tras 3 de calentamiento por
   variante; pared por invocación con `time.perf_counter()`, incluido el arranque
   del proceso. Tokens = `bytes/4` del stdout UTF-8, stderr excluido; misma
   aproximación declarada que en la sección 2.

### Resultados

Ejecución del 2026-11-19T20:35:18Z con el binario `0.2.0` (`commit unknown`,
`state dev`) y el mismo hardware de la sección 1.

| Variante | Invocaciones | Bytes | Tokens (bytes/4) | p95 reanudación (ms) | Veredicto M-ACM-03 |
|---|---|---|---|---|---|
| En frío: ventana + checkpoint | 5 | 36 233 | **9 058** | 36,80 | **FALLIDO** |
| Handoff: `session_id` conocido | 4 | 5 775 | **1 444** | 29,80 | **SUPERADO** |
| Sin `topic_key`: decisiones mínimas | 5 | 40 294 | **10 074** | 38,53 | **FALLIDO** |
| Sin claves: preferencias mínimas | 4 | 37 548 | **9 387** | 30,21 | **FALLIDO** |

Desglose literal del guion acotado en frío: ventana de sesiones 30 458 B,
checkpoint 1 331 B, decisión del `topic_key` 1 933 B (1 fila) y dos preferencias
por clave 2 511 B (1 260 + 1 251). Total 36 233 B. En handoff se descuenta la
ventana: 5 775 B. Sin `topic_key`, la página mínima de decisiones devuelve 3
filas con `truncated=true` y cuesta 5 994 B; la página mínima de preferencias
devuelve 3 filas con `truncated=true` y cuesta 3 826 B.

Operaciones nuevas, 100 repeticiones por variante:

| Operación | Llamadas | p50 (ms) | p95 (ms) | p99 (ms) | Filas | truncated | Bytes/página |
|---|---|---|---|---|---|---|---|
| `find_decisions_topic` (`topic_key`, límite 5) | 200 | 6,74 | 7,78 | 9,11 | 1 | false | 1 933 |
| `find_decisions_min` (límite 3) | 100 | 6,45 | 7,84 | 9,49 | 3 | **true** | 5 994 |
| `find_preferences_key` (`pref_key`, límite 2) | 400 | 5,81 | 6,94 | 8,66 | 1 | false | 1 260 |
| `find_preferences_min` (límite 3) | 100 | 6,09 | 6,92 | 7,51 | 3 | **true** | 3 826 |
| **Conjunto de operaciones nuevas** | 800 | 6,21 | **7,51** | 8,91 | | | |

**M-ACM-01: SUPERADO.** 5 invocaciones en frío y 4 con `session_id` conocido,
ambas ≤ 6.

**M-ACM-02: SUPERADO.** p95 de 7,51 ms en las operaciones nuevas (n=800) y 8,08 ms
por invocación del guion acotado completo, muy por debajo de 150 ms.

**M-ACM-03: el veredicto depende del guion, y se declara sin suavizarlo.**

- **Guion acotado con `session_id` conocido (handoff): SUPERADO.** 1 444 tokens
  (5 775 B) ≤ 2 000. Es la reanudación por defecto cuando la persona o el
  handoff entregan la sesión, y el conjunto mínimo de cuatro `find`.
- **Guion acotado en frío, sin sesión conocida: FALLIDO.** 9 058 tokens
  (36 233 B) > 2 000. La causa no son las decisiones ni las preferencias
  (4 444 B combinadas), sino la ventana de sesiones: 30 458 B, el 84 % del
  coste, porque `find` devuelve 39 filas completas y no hay proyección
  (C-ACM-03). Acotar las decisiones por `topic_key` baja esa página de 18 519 B
  a 1 933 B y las preferencias por clave de 13 295 B a 2 511 B (5,3× menos),
  pero no compensa el listado de sesiones.
- **Sin `topic_key` ni claves: FALLIDO.** 10 074 y 9 387 tokens. La página
  mínima con aviso de recorte es honesta (no presenta 3 filas como todas), pero
  no cumple el objetivo; el aviso no es una exención.
- **Guion completo anterior: FALLIDO**, 19 660 tokens (sección 2). Se conserva
  como historia y confirma que el guion acotado reduce el coste, aunque en frío
  no lo suficiente.

El objetivo no se toca. El guion que cumple es el acotado con sesión conocida;
el que no cumple es el mismo guion en frío, y también cualquier variante que
pida todas las decisiones o todas las preferencias. Cerrar la brecha del frío
exige reducir o proyectar la página de sesiones, no relajar M-ACM-03.

## Tabla de objetivos, resultados y veredictos

| Métrica | Objetivo (PRD §7) | Resultado medido | Veredicto |
|---|---|---|---|
| M-ACM-01 Invocaciones por reanudación | ≤ 6 | 5 en frío; 4 con `session_id` (guion acotado) | **SUPERADO** |
| M-ACM-02 Latencia p95 por invocación | ≤ 150 ms | 8,08 ms (guion acotado, n=500); operaciones nuevas 7,51 ms (n=800) | **SUPERADO** |
| M-ACM-03 Contexto por reanudación (guion acotado, `session_id` conocido) | ≤ 2 000 tokens | 1 444 tokens (5 775 B) | **SUPERADO** |
| M-ACM-03 Contexto por reanudación (guion acotado, en frío sin sesión) | ≤ 2 000 tokens | 9 058 tokens (36 233 B) | **FALLIDO** |
| M-ACM-03 Contexto por reanudación (guion completo anterior, histórico) | ≤ 2 000 tokens | 19 660 tokens (78 638 B); conjunto mínimo anterior 12 045 tokens (48 180 B) | **FALLIDO** |
| M-ACM-04 Coste de captura consolidada | ≤ 3 invocaciones | 2 por captura, con verificación posterior (10/10) | **SUPERADO** |
| M-ACM-05 Reanudación correcta | 5 de 5 | No medido en esta ronda | **PENDIENTE (P6)** |

## Límites de la medición y de la aproximación

- `bytes/4` es una aproximación declarada, no un conteo del tokenizador de un
  modelo. El stdout de db-ai incluye el envoltorio JSON de cada campo, de modo
  que el byte real por fila es mayor que el texto visible; cualquier objetivo
  expresado en tokens depende de este factor.
- Solo se mide stdout; stderr queda excluido. El tiempo incluye el arranque del
  proceso `db-ai` en cada invocación (es una CLI local, no una API caliente).
- La medición se hizo en WSL2 sobre un VHD local, con 16 vCPU visibles y sin
  fijación de CPU ni de frecuencia; los valores absolutos pueden variar entre
  ejecuciones y máquinas. La comparación válida es entre métricas y objetivos,
  no entre milisegundos de dos máquinas.
- El fixture es sintético y determinista: contenidos realistas pero repetidos
  por plantilla. No incluye conversaciones, prompts ni secretos.
- El escenario de M-ACM-01 del PRD (tres decisiones activas y dos
  preferencias) no coincide con el fixture de 10k (40 y 12); se han registrado
  ambas cifras y el veredicto no se suaviza. El guion acotado reduce el efecto
  de esa diferencia, pero el fixture sigue midiendo el coste de la ventana de
  sesiones, no el escenario mínimo del PRD.
- La página de sesiones domina el coste en frío: 30 458 B de 36 233 B. `find` no
  proyecta columnas (C-ACM-03) y devuelve la fila entera, así que el objetivo de
  2 000 tokens no se alcanza en la base de 10k sin sesión conocida. La medición
  con `session_id` conocido sí lo cumple; ambas cifras quedan declaradas.
- M-ACM-05 no se ha medido: requiere la prueba con agentes reales de P6 y un
  evaluador humano. No se declara superado.
- `db-ai version` existe en el árbol de trabajo, pero el binario instalado en
  `~/.local/bin` aún no lo soporta; la versión declarada es la del binario
  compilado para esta medición (`0.2.0`, `commit unknown`, `state dev`).

## Reproducción

```bash
cd /home/alfredo/PROJECTS/database-virtual-filesystem
go build -o /tmp/opencode/db-ai ./cmd/db-ai

cd examples/agent-continuity
python3 tests/benchmark.py --db-ai /tmp/opencode/db-ai \
  --workdir /tmp/opencode/acm-bench --reps 100

# El arnés regenera el fixture. Para congelar el de esta evidencia y reutilizarlo:
python3 tools/gen-fixture.py --db /tmp/opencode/acm-bench/memory.db \
  --seed 20261119 --now 2026-11-19T20:26:16Z
python3 tests/benchmark.py --db-ai /tmp/opencode/db-ai \
  --workdir /tmp/opencode/acm-bench --reps 100 --reuse-fixture

# Comprobación de la cuenta del fixture:
sqlite3 /tmp/opencode/acm-bench/memory.db \
  "SELECT (SELECT count(*) FROM sessions), (SELECT count(*) FROM observations),
          (SELECT count(*) FROM checkpoints), (SELECT count(*) FROM preferences);"
```

- El generador usa por defecto `--now` = instante de ejecución; el fixture de
  esta evidencia se creó en `2026-11-19T20:26:16Z`. Con la misma pareja
  `(seed, now)` la base es idéntica (comprobado: dos generaciones consecutivas
  producen el mismo volcado y el mismo fichero).
- El arnés regenera el fixture en cada ejecución (ventana temporal fresca); con
  `--reuse-fixture` reutiliza el que exista.
- El arnés escribe el informe Markdown por stdout; la ejecución de esta
  evidencia se guardó en `/tmp/opencode/benchmark-report.md` (fuera del
  repositorio).
