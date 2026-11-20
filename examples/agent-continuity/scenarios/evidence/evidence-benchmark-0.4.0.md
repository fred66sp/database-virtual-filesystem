# Evidencia P10 — benchmark del guion refinado (db-ai 0.4.0)

Fecha: 2026-11-20. Fixture determinista regenerado con `tools/gen-fixture.py` (seed 20261119):
1.024 sesiones, 10.240 observaciones, 1.024 checkpoints, 12 preferencias (6 del humano,
6 del agente). La medición anterior (db-ai 0.2.0, `evidence-benchmark.md`) se conserva
intacta como historia; esta es una medición nueva, no su sustitución.

## Guion medido (protocolo 1.3 de la skill agent-continuity 0.2.0)

1. `find /mem/sessions --order "started_at:desc" --limit 5 --fields "id,status"`
2. `find /mem/checkpoints --where "session_id=<id>" --where "status=active" --order "created_at:desc" --limit 1 --fields "state,next_steps"` (paso repetido solo si la sesión no tiene checkpoint activo)
3. `find /mem/observations --where "session_id=<id>" --order "created_at:desc" --fields "kind,topic_key,title" --limit 5`
4. `find /mem/preferences --where "source=human" --where "status=active" --fields "pref_key,value" --limit 20`

Selección de preferencias por `source=human`: en frío se recuperan las preferencias
de la persona; las del agente o importadas se piden por `pref_key` cuando el paso las
necesite (divulgación progresiva). Tokens estimados como `bytes/4`, el mismo criterio
del arnés original.

## Resultados (10 repeticiones por variante)

| Variante | Invocaciones (máx) | Tokens estimados (máx) | p95 por invocación (máx) |
|---|---:|---:|---:|
| Recuperación en frío | **3–4** (objetivo ≤6) | **1.909** (objetivo ≤2.000) | 12,5 ms (objetivo ≤150) |
| Sesión conocida | 3 | 1.434 | 11,6 ms |

Desglose de la rep 1 en frío: sesiones 1.899 B, checkpoint 574 B, observaciones
2.763 B, preferencias del PO 2.401 B.

Captura con verificación: la consolidación por `upsert` reduce el coste del patrón
`touch`→`E_CONSTRAINT`→`write`: una invocación con resultado explícito más la
verificación de lectura posterior se sitúan por debajo del presupuesto de ≤3
invocaciones (el arnés original midió 2 con el patrón antiguo; conservado como
historia).

Veredicto: **criterios M-ACM-03 cumplidos** con el guion simplificado sobre 10.240
observaciones, sin reducir el fixture ni recortar información necesaria para
reanudar (el detalle bajo demanda viaja por `cat`/`find` acotados del protocolo).

Limitaciones: estimación por `bytes/4`, no medición real de tokens; medición local
con binario 0.4.0 (compilado del árbol sin commit); la validación con agentes reales
y modelos distintos se traslada a la prueba de campo por decisión del PO (2026-11-20).
