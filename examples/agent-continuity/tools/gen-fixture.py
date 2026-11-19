#!/usr/bin/env python3
"""Genera una base ACM grande y determinista para el benchmark M-ACM-02.

Aplica schema.sql y rellena sesiones, observaciones, checkpoints y preferencias
con el mismo algoritmo de content_hash que tools/content-hash.py (DESIGN §5.2).
El seed fija el contenido; `--now` fija el instante final de la ventana temporal,
de modo que la misma pareja (seed, now) reproduce la base byte a byte.

Uso:
  python3 tools/gen-fixture.py --db /tmp/acm/memory.db [--observations 10240]
                               [--sessions 1024] [--seed 20261119] [--now ISO]

Salida: resumen JSON con las cuentas reales de cada tabla y la verificación de
integridad. Pensado para el arnés tests/benchmark.py, no para la base real.
"""
import argparse
import datetime as dt
import hashlib
import importlib.util
import json
import os
import random
import sqlite3
import sys
from pathlib import Path

HERE = Path(__file__).resolve().parent
SCHEMA = HERE.parent / "schema.sql"
HASH_TOOL = HERE / "content-hash.py"

TOPICS = [
    "storage/db-per-project", "storage/schema-version", "storage/backup",
    "retrieval/find-limits", "retrieval/grep-literal", "retrieval/window-14d",
    "capture/dedupe-hash", "capture/consolidation", "capture/session-open",
    "commit/approval", "docs/language", "tools/no-playwright",
    "testing/manual-review", "concurrency/etag", "concurrency/batch",
    "concurrency/commit-unknown", "checkpoint/session-scope",
    "checkpoint/next-steps", "checkpoint/window", "lifecycle/needs-review",
    "lifecycle/supersede", "lifecycle/obsolete", "bootstrap/init-idempotent",
    "bootstrap/schema-ddl", "policy/allow-prefix", "metrics/m-acm-01",
    "metrics/m-acm-02", "metrics/m-acm-03", "metrics/m-acm-04",
    "skill/protocols", "skill/capture-rules", "schema/uuid7",
    "schema/fk-no-cascade", "audit/evidence", "audit/reproducibility",
    "performance/p95", "performance/tokens", "quality/gate", "release/version",
    "retrieval/progressive",
]

KINDS = ["discovery", "learning", "pattern", "architecture", "bugfix", "decision"]
KIND_WEIGHTS = [30, 20, 15, 12, 11, 12]
CONFIDENCES = ["confirmed", "inference", "hypothesis", "proposal", "unknown"]
CONF_WEIGHTS = [50, 20, 15, 10, 5]

TITLES = {
    "discovery": "Hallazgo sobre {topic} en la sesión {n}",
    "learning": "Aprendizaje operativo sobre {topic} (caso {n})",
    "pattern": "Patrón reutilizable para {topic} (caso {n})",
    "architecture": "Componente de arquitectura para {topic} (caso {n})",
    "bugfix": "Corrección aplicada en {topic} (caso {n})",
    "decision": "Decisión sobre {topic}: variante {n}",
}

BODIES = [
    "Se observó que {topic} se comporta de forma estable bajo las condiciones del "
    "proyecto. El registro se conserva para evitar repetir el análisis en sesiones "
    "posteriores.",
    "La consulta filtrada sobre {topic} respeta el límite y devuelve una página "
    "acotada; el detalle se pide después solo del elemento que se va a usar.",
    "El procedimiento documentado para {topic} incluye verificación posterior y una "
    "marca temporal en UTC; no se guarda la conversación que lo produjo.",
    "La evidencia recogida sobre {topic} se apoya en el binario local y en SQLite; "
    "no añade dependencias externas ni un motor de memoria nuevo.",
    "Un cambio en {topic} exige recalcular el hash de contenido y actualizar la fila "
    "con el ETag vigente; el ETag serializa la escritura entre agentes.",
    "Se descartó la alternativa de {topic} porque duplicaba mecanismos ya presentes "
    "en SQLite y db-ai, y habría añadido deuda de mantenimiento.",
]

RATIONALE = (
    "Se elige esta opción porque conserva la trazabilidad de {topic} sin añadir "
    "un componente nuevo y permite verificar el resultado con una lectura posterior."
)

PREFERENCES = [
    ("commit/approval", "Presentar un informe de preparación del commit y esperar confirmación explícita antes de commitear."),
    ("docs/language", "Documentos con nombres de fichero en inglés y contenido en español (castellano)."),
    ("tools/no-playwright", "No usar Playwright en las validaciones; la revisión es manual."),
    ("testing/manual-review", "Revisar a mano las evidencias antes de darlas por buenas."),
    ("retrieval/progressive", "Pedir el detalle con cat solo del elemento que se vaya a usar."),
    ("capture/verify-after-write", "Verificar cada captura con una lectura posterior de la fila."),
    ("storage/backup", "Copiar el fichero .sqlite antes de una migración manual."),
    ("checkpoint/close-session", "Cerrar la sesión con un checkpoint que deje un siguiente paso identificable."),
    ("audit/declare-limits", "Declarar en la evidencia lo que no se ha medido y por qué."),
    ("performance/measure", "Medir con 100 repeticiones y declarar el hardware usado."),
    ("skill/capture-threshold", "Capturar solo conocimiento durable, reutilizable y no obvio."),
    ("release/no-commit", "No commitear ni empujar sin confirmación explícita de la persona."),
]


def load_hash_tool():
    spec = importlib.util.spec_from_file_location("acm_content_hash", HASH_TOOL)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def canonical_hash(mod, table, row):
    canon = []
    for campo in mod.CAMPOS[table]:
        valor = row.get(campo)
        if valor is None:
            valor = ""
        if isinstance(valor, (dict, list)):
            canon.append(json.dumps(valor, ensure_ascii=False, separators=(",", ":")))
        else:
            canon.append(mod.normaliza(str(valor)))
    arreglo = json.dumps(canon, ensure_ascii=False, separators=(",", ":"))
    return "sha256:" + hashlib.sha256(arreglo.encode("utf-8")).hexdigest()


def uuid7(rng, ts_ms):
    ms = int(ts_ms)
    return "{:08x}-{:04x}-7{:03x}-{:04x}-{:012x}".format(
        ms >> 16,
        ms & 0xFFFF,
        rng.getrandbits(12),
        (0b10 << 14) | rng.getrandbits(14),
        rng.getrandbits(48),
    )


def iso(moment):
    return moment.strftime("%Y-%m-%dT%H:%M:%SZ")


def parse_now(valor):
    if valor:
        return dt.datetime.strptime(valor, "%Y-%m-%dT%H:%M:%SZ").replace(
            tzinfo=dt.timezone.utc)
    return dt.datetime.now(dt.timezone.utc).replace(microsecond=0)


def distribute(total, buckets, rng):
    """Reparte `total` observaciones entre `buckets` de forma determinista."""
    if buckets <= 0:
        raise ValueError("se necesita al menos una sesión")
    media = total / buckets
    counts = [max(1, int(round(media * rng.uniform(0.5, 1.5)))) for _ in range(buckets)]
    cursor = 0
    while sum(counts) != total:
        paso = 1 if sum(counts) < total else -1
        i = cursor % buckets
        if paso > 0 or counts[i] > 1:
            counts[i] += paso
        cursor += 1
        if cursor > buckets * (total + buckets):
            raise RuntimeError("no se pudo repartir el total de observaciones")
    return counts


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--db", required=True, help="ruta de la base a crear")
    ap.add_argument("--observations", type=int, default=10240)
    ap.add_argument("--sessions", type=int, default=1024)
    ap.add_argument("--seed", type=int, default=20261119)
    ap.add_argument("--now", default=None,
                    help="instante final UTC (YYYY-MM-DDTHH:MM:SSZ); por defecto, ahora")
    args = ap.parse_args()

    if args.observations < 10000:
        sys.exit("gen-fixture: --observations debe ser >= 10000")
    if args.sessions < 1:
        sys.exit("gen-fixture: --sessions debe ser >= 1")

    rng = random.Random(args.seed)
    now = parse_now(args.now)
    mod = load_hash_tool()
    db_path = Path(args.db)
    db_path.parent.mkdir(parents=True, exist_ok=True)
    for sufijo in ("", "-journal", "-wal", "-shm"):
        p = Path(str(db_path) + sufijo)
        if p.exists():
            p.unlink()

    con = sqlite3.connect(db_path)
    con.execute("PRAGMA foreign_keys = ON")
    con.executescript(SCHEMA.read_text(encoding="utf-8"))

    now_ms = int(now.timestamp() * 1000)
    con.executemany(
        "INSERT INTO meta (id, name, value, updated_at) VALUES (?,?,?,?)",
        [
            (uuid7(rng, now_ms - 4000), "schema_version", "1", iso(now)),
            (uuid7(rng, now_ms - 3000), "project_key", uuid7(rng, now_ms - 3500), iso(now)),
            (uuid7(rng, now_ms - 2000), "project_name", "acm-bench-10k", iso(now)),
            (uuid7(rng, now_ms - 1000), "created_at", iso(now), iso(now)),
        ],
    )

    # --- Sesiones y reparto de observaciones ---------------------------------
    span = dt.timedelta(days=365)
    step = span / args.sessions
    counts = distribute(args.observations, args.sessions, rng)
    agents = ["agent-alpha", "agent-beta", "claude-code", "opencode"]
    sesiones = []
    for i in range(args.sessions):
        started = now - span + step * i
        sid = uuid7(rng, int(started.timestamp() * 1000))
        agent = agents[i % len(agents)]
        abierta = i >= args.sessions - 2
        ended = None if abierta else started + dt.timedelta(minutes=90)
        summary = None if abierta else (
            f"Sesión {i:04d} cerrada; queda un siguiente paso identificable.")
        sesiones.append({
            "id": sid,
            "agent_id": agent,
            "source": "agent",
            "goal": f"Sesión {i:04d}: avanzar en {TOPICS[i % len(TOPICS)]} y dejar checkpoint.",
            "status": "open" if abierta else "closed",
            "started_at": iso(started),
            "ended_at": iso(ended) if ended else None,
            "summary": summary,
            "created_at": iso(started),
            "updated_at": iso(ended or started),
            "_started": started,
            "_ended": ended,
        })
    con.executemany(
        "INSERT INTO sessions (id, agent_id, source, goal, status, started_at,"
        " ended_at, summary, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)",
        [(s["id"], s["agent_id"], s["source"], s["goal"], s["status"],
          s["started_at"], s["ended_at"], s["summary"], s["created_at"],
          s["updated_at"]) for s in sesiones],
    )
    con.commit()

    # --- Observaciones --------------------------------------------------------
    observaciones = []
    decisiones_por_tema = {}
    n = 0
    for idx, s in enumerate(sesiones):
        for j in range(counts[idx]):
            n += 1
            kind = rng.choices(KINDS, KIND_WEIGHTS)[0]
            topic = TOPICS[rng.randrange(len(TOPICS))]
            created = s["_started"] + dt.timedelta(minutes=1 + 2 * j)
            oid = uuid7(rng, int(created.timestamp() * 1000))
            if kind == "decision":
                status = "active"
                rationale = RATIONALE.format(topic=topic)
                alternatives = json.dumps(
                    [f"mantener {topic}", "no registrar la decisión"],
                    ensure_ascii=False, separators=(",", ":"))
                consequences = json.dumps(
                    ["la decisión queda trazable", "requiere verificación posterior"],
                    ensure_ascii=False, separators=(",", ":"))
            else:
                status = rng.choices(
                    ["active", "needs_review", "obsolete"], [90, 6, 4])[0]
                rationale = None
                alternatives = None
                consequences = None
            fila = {
                "id": oid,
                "session_id": s["id"],
                "agent_id": s["agent_id"],
                "source": "agent",
                "kind": kind,
                "title": TITLES[kind].format(topic=topic, n=f"{n:05d}"),
                "body": BODIES[rng.randrange(len(BODIES))].format(topic=topic),
                "rationale": rationale,
                "alternatives": alternatives,
                "consequences": consequences,
                "topic_key": topic if kind == "decision" or rng.random() < 0.6 else None,
                "confidence": rng.choices(CONFIDENCES, CONF_WEIGHTS)[0],
                "status": status,
                "review_after": iso(s["_started"] + dt.timedelta(days=180))
                if rng.random() < 0.2 else None,
                "superseded_by": None,
                "duplicate_count": rng.choices([1, 2, 3], [92, 6, 2])[0],
                "last_seen_at": iso(created),
                "origin_ref": f"scenarios/evidence/evidence-benchmark.md#{n:05d}",
                "created_at": iso(created),
                "updated_at": iso(created),
            }
            fila["content_hash"] = canonical_hash(mod, "observations", fila)
            observaciones.append(fila)
            if kind == "decision":
                decisiones_por_tema.setdefault(topic, []).append(fila)

    # Una sola decisión activa por tema: las anteriores quedan superseded.
    for filas in decisiones_por_tema.values():
        filas.sort(key=lambda f: f["created_at"])
        for anterior, siguiente in zip(filas, filas[1:]):
            anterior["status"] = "superseded"
            anterior["superseded_by"] = siguiente["id"]
            anterior["updated_at"] = siguiente["created_at"]
    # La cadena de sustitución apunta a decisiones posteriores: se inserta sin
    # enlace y se enlaza en una segunda pasada (la FK es inmediata).
    enlaces = []
    for o in observaciones:
        if o["superseded_by"] is not None:
            enlaces.append((o["status"], o["superseded_by"], o["updated_at"], o["id"]))
    con.executemany(
        "INSERT INTO observations (id, session_id, agent_id, source, kind, title,"
        " body, rationale, alternatives, consequences, topic_key, confidence,"
        " status, review_after, superseded_by, duplicate_count, last_seen_at,"
        " content_hash, origin_ref, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
        [(o["id"], o["session_id"], o["agent_id"], o["source"], o["kind"],
          o["title"], o["body"], o["rationale"], o["alternatives"],
          o["consequences"], o["topic_key"], o["confidence"],
          "active" if o["superseded_by"] else o["status"],
          o["review_after"], None, o["duplicate_count"],
          o["last_seen_at"], o["content_hash"], o["origin_ref"],
          o["created_at"], o["updated_at"]) for o in observaciones],
    )
    con.executemany(
        "UPDATE observations SET status=?, superseded_by=?, updated_at=? WHERE id=?",
        enlaces,
    )
    con.commit()

    # --- Checkpoints: uno activo por sesión -----------------------------------
    checkpoints = []
    for i, s in enumerate(sesiones):
        created = s["_ended"] or (s["_started"] + dt.timedelta(minutes=45))
        files = json.dumps(
            ["examples/agent-continuity/schema.sql",
             "examples/agent-continuity/docs/DESIGN.md"],
            ensure_ascii=False, separators=(",", ":"))
        questions = json.dumps(
            [f"¿Sigue vigente el límite de la ventana en la sesión {i:04d}?"],
            ensure_ascii=False, separators=(",", ":")) if i % 7 == 0 else "[]"
        fila = {
            "id": uuid7(rng, int(created.timestamp() * 1000) + 1),
            "session_id": s["id"],
            "agent_id": s["agent_id"],
            "source": "agent",
            "goal": f"Cerrar la sesión {i:04d} con estado y siguiente paso.",
            "state": f"Trabajo de {TOPICS[i % len(TOPICS)]} consolidado; "
                     "observaciones capturadas y verificadas.",
            "next_steps": f"1. Revisar {TOPICS[i % len(TOPICS)]}. "
                          "2. Ejecutar la verificación posterior.",
            "files": files,
            "open_questions": questions,
            "status": "active",
            "superseded_by": None,
            "created_at": iso(created),
            "updated_at": iso(created),
        }
        fila["content_hash"] = canonical_hash(mod, "checkpoints", fila)
        checkpoints.append(fila)
    con.executemany(
        "INSERT INTO checkpoints (id, session_id, agent_id, source, goal, state,"
        " next_steps, files, open_questions, status, superseded_by, content_hash,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
        [(c["id"], c["session_id"], c["agent_id"], c["source"], c["goal"],
          c["state"], c["next_steps"], c["files"], c["open_questions"],
          c["status"], c["superseded_by"], c["content_hash"], c["created_at"],
          c["updated_at"]) for c in checkpoints],
    )

    # --- Preferencias activas -------------------------------------------------
    reciente = sesiones[-1]["id"]
    preferencias = []
    for i, (key, value) in enumerate(PREFERENCES):
        created = now - dt.timedelta(days=200 - i)
        humana = i % 2 == 0
        fila = {
            "id": uuid7(rng, int(created.timestamp() * 1000)),
            "session_id": None if humana else reciente,
            "agent_id": "alfredo" if humana else "agent-alpha",
            "source": "human" if humana else "agent",
            "pref_key": key,
            "value": value,
            "rationale": f"Convención del proyecto para {key}; se aplica salvo "
                         "instrucción explícita de la persona.",
            "status": "active",
            "review_after": iso(now + dt.timedelta(days=90)),
            "superseded_by": None,
            "origin_ref": "AGENTS.md",
            "created_at": iso(created),
            "updated_at": iso(created),
        }
        fila["content_hash"] = canonical_hash(mod, "preferences", fila)
        preferencias.append(fila)
    con.executemany(
        "INSERT INTO preferences (id, session_id, agent_id, source, pref_key,"
        " value, rationale, status, review_after, superseded_by, content_hash,"
        " origin_ref, created_at, updated_at)"
        " VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
        [(p["id"], p["session_id"], p["agent_id"], p["source"], p["pref_key"],
          p["value"], p["rationale"], p["status"], p["review_after"],
          p["superseded_by"], p["content_hash"], p["origin_ref"],
          p["created_at"], p["updated_at"]) for p in preferencias],
    )
    con.commit()

    # --- Autocomprobación: cuentas, integridad y muestra de hashes ------------
    cuentas = {
        tabla: con.execute(f"SELECT count(*) FROM {tabla}").fetchone()[0]
        for tabla in ("meta", "sessions", "observations", "checkpoints",
                      "preferences")
    }
    if cuentas["observations"] < args.observations:
        sys.exit(f"gen-fixture: esperaba >= {args.observations} observaciones, "
                 f"hay {cuentas['observations']}")
    muestra = con.execute(
        "SELECT kind, title, body, rationale, alternatives, consequences,"
        " topic_key, content_hash FROM observations ORDER BY id LIMIT 25"
    ).fetchall()
    campos = mod.CAMPOS["observations"]
    for fila in muestra:
        recalculado = canonical_hash(mod, "observations", dict(zip(campos, fila)))
        if recalculado != fila[-1]:
            sys.exit(f"gen-fixture: content_hash no reproduce en {fila[1]!r}")
    fk = con.execute("PRAGMA foreign_key_check").fetchall()
    integridad = con.execute("PRAGMA integrity_check").fetchone()[0]
    con.close()
    if fk or integridad != "ok":
        sys.exit(f"gen-fixture: integridad rota (fk={fk}, {integridad})")

    print(json.dumps({
        "db": str(db_path),
        "seed": args.seed,
        "now": iso(now),
        "counts": cuentas,
        "content_hash_muestra": len(muestra),
        "integrity_check": integridad,
    }, ensure_ascii=False))


if __name__ == "__main__":
    main()
