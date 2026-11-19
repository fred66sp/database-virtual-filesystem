#!/usr/bin/env python3
"""Arnés de medición de M-ACM-01 a M-ACM-04 sobre una base ACM grande.

Genera (o reutiliza) un fixture de 10 000+ observaciones con tools/gen-fixture.py,
lo monta como alias de db-ai en una configuración aislada y mide:

  - guion completo anterior (ventana de sesiones, checkpoint activo, historial de
    la sesión, todas las decisiones activas y todas las preferencias activas) con
    N repeticiones: se conserva como historia de la medición;
  - guion acotado por topic_key (decisión del PO, 2026-11-19): decisiones activas
    solo del tema en curso y preferencias solo por pref_key aplicable; en frío y
    con session_id conocido, más el caso sin topic (página mínima con recorte);
  - conjunto mínimo anterior con sesión conocida (4 find), como historia;
  - paginación (meta.truncated) de cada página;
  - recuperación del checkpoint reciente con el procedimiento de P3;
  - captura consolidada con verificación posterior (M-ACM-04);
  - latencia de grep literal y find filtrado sobre las 10k filas.

Uso:
  python3 tests/benchmark.py [--db-ai /ruta/db-ai] [--workdir /tmp/acm-bench]
                             [--reps 100] [--seed 20261119]

Salida: informe Markdown por stdout. Sale != 0 si una invocación falla o una
comprobación de integridad no se cumple.
"""
import argparse
import datetime as dt
import json
import math
import os
import platform
import re
import shutil
import sqlite3
import subprocess
import sys
import time
from pathlib import Path

HERE = Path(__file__).resolve().parent
EXAMPLE = HERE.parent
GEN = EXAMPLE / "tools" / "gen-fixture.py"
HASH_TOOL = EXAMPLE / "tools" / "content-hash.py"
SCHEMA = EXAMPLE / "schema.sql"
SEED = EXAMPLE / "seed.sql"

OBJECTIVE_M1_MAX = 6
OBJECTIVE_M2_P95_MS = 150
OBJECTIVE_M3_MAX_TOKENS = 2000
OBJECTIVE_M4_MAX_INVOCATIONS = 3

# Claves aplicables a la tarea de referencia (no se commiteará nada): el guion
# acotado pide una preferencia por clave, no la tabla de activas.
PREF_KEYS = ("commit/approval", "release/no-commit")
TOPIC_IN_GOAL = re.compile(r"avanzar en (\S+) y dejar checkpoint")
NEW_OPS = ("find_decisions_topic", "find_decisions_min",
           "find_preferences_key", "find_preferences_min")


def iso(moment):
    return moment.strftime("%Y-%m-%dT%H:%M:%SZ")


def uuid7(moment):
    ms = int(moment.timestamp() * 1000)
    rnd = int.from_bytes(os.urandom(8), "big")
    return "{:08x}-{:04x}-7{:03x}-{:04x}-{:012x}".format(
        ms >> 16, ms & 0xFFFF, (rnd >> 52) & 0xFFF,
        (0b10 << 14) | ((rnd >> 38) & 0x3FFF), rnd & 0xFFFFFFFFFFFF)


def percentile(values, p):
    ordered = sorted(values)
    k = max(0, min(len(ordered) - 1, math.ceil(p / 100 * len(ordered)) - 1))
    return ordered[k]


def stats(values):
    return {
        "n": len(values),
        "min": min(values),
        "p50": percentile(values, 50),
        "p95": percentile(values, 95),
        "p99": percentile(values, 99),
        "max": max(values),
    }


def ms(value):
    return f"{value:.2f}"


class Harness:
    def __init__(self, dbai, workdir):
        self.dbai = dbai
        self.workdir = workdir
        self.config_home = workdir / "config"
        self.state_home = workdir / "state"
        self.env = os.environ.copy()
        self.env["XDG_CONFIG_HOME"] = str(self.config_home)
        self.env["XDG_STATE_HOME"] = str(self.state_home)
        (self.config_home / "db-ai").mkdir(parents=True, exist_ok=True)
        os.chmod(self.config_home / "db-ai", 0o700)
        self.state_home.mkdir(parents=True, exist_ok=True)

    # --- configuración de montajes ------------------------------------------
    def register(self, alias, db, rw=True):
        path = self.config_home / "db-ai" / "config.json"
        cfg = {"version": 1, "mounts": {}}
        if path.exists():
            cfg = json.loads(path.read_text(encoding="utf-8"))
        read_ops = ["ls", "tree", "cat", "describe", "stat", "find", "grep", "export"]
        allow = [{"ops": read_ops, "prefix": f"/{alias}"}]
        if rw:
            allow += [{"ops": ["touch", "write"], "prefix": f"/{alias}/{t}"}
                      for t in ("sessions", "observations", "checkpoints", "preferences")]
        cfg.setdefault("mounts", {})[alias] = {
            "file": str(Path(db).resolve()), "mode": "rw" if rw else "ro",
            "allow": allow,
        }
        tmp = path.with_suffix(".tmp")
        tmp.write_text(json.dumps(cfg, ensure_ascii=False, indent=2) + "\n",
                       encoding="utf-8")
        os.chmod(tmp, 0o600)
        os.replace(tmp, path)

    # --- invocación medida ---------------------------------------------------
    def call(self, op, *args):
        inicio = time.perf_counter()
        proc = subprocess.run([self.dbai, *args], capture_output=True, env=self.env)
        pared = (time.perf_counter() - inicio) * 1000
        if proc.returncode != 0:
            raise RuntimeError(
                f"{op}: exit {proc.returncode}: {proc.stderr.decode(errors='replace')}")
        payload = json.loads(proc.stdout.decode("utf-8"))
        if not payload.get("ok"):
            raise RuntimeError(f"{op}: {payload.get('error')}")
        return {
            "op": op,
            "ms": pared,
            "bytes": len(proc.stdout),
            "truncated": payload["meta"]["truncated"],
            "rows": payload["data"].get("rows", []),
            "entries": payload["data"].get("entries", []),
            "fields": payload["data"].get("fields", {}),
        }

    @staticmethod
    def row_id(row):
        return row["fields"]["id"]["data"]

    # --- guion de reanudación ------------------------------------------------
    def locate(self, alias, known_session=None):
        """Sesión y checkpoint del protocolo 1.4; devuelve la ventana leída."""
        calls = []
        sesiones = None
        if known_session is None:
            corte = iso(dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=14))
            sesiones = self.call(
                "find_sessions_window", "find", f"/{alias}/sessions",
                "--where", f"started_at>={corte}", "--limit", "50")
            calls.append(sesiones)
            candidatas = sorted(
                (self.row_id(r) for r in sesiones["rows"]), reverse=True)
            if not candidatas:
                raise RuntimeError("la ventana de 14 días no devolvió sesiones")
        else:
            candidatas = [known_session]

        elegida = None
        checkpoint = None
        for sid in candidatas:
            busqueda = self.call(
                "find_checkpoint_active", "find", f"/{alias}/checkpoints",
                "--where", f"session_id={sid}", "--where", "status=active",
                "--limit", "5")
            calls.append(busqueda)
            if busqueda["rows"]:
                elegida = sid
                checkpoint = max(self.row_id(r) for r in busqueda["rows"])
                break
        if elegida is None:
            raise RuntimeError("ninguna sesión de la ventana tiene checkpoint activo")
        return {"calls": calls, "session": elegida, "checkpoint": checkpoint,
                "rows": (sesiones or {}).get("rows", [])}

    def resume(self, alias, known_session=None):
        """Guion completo anterior; se conserva como historia de la medición."""
        located = self.locate(alias, known_session)
        calls = located["calls"]
        calls.append(self.call(
            "find_observations_session", "find", f"/{alias}/observations",
            "--where", f"session_id={located['session']}", "--limit", "50"))
        calls.append(self.call(
            "find_decisions_active", "find", f"/{alias}/observations",
            "--where", "kind=decision", "--where", "status=active", "--limit", "10"))
        calls.append(self.call(
            "find_preferences_active", "find", f"/{alias}/preferences",
            "--where", "status=active", "--limit", "20"))
        return {"calls": calls, "session": located["session"],
                "checkpoint": located["checkpoint"], "rows": located["rows"]}

    def resume_bounded(self, alias, topic, pref_keys=PREF_KEYS,
                       known_session=None):
        """Guion acotado por topic_key (decisión del PO, 2026-11-19).

        Decisiones activas solo del tema en curso (límite 5). Sin topic, página
        mínima (límite 3) con aviso de recorte, nunca todas. Preferencias solo
        por pref_key aplicable (límite 2); sin claves, página mínima.
        """
        located = self.locate(alias, known_session)
        calls = located["calls"]
        if topic:
            calls.append(self.call(
                "find_decisions_topic", "find", f"/{alias}/observations",
                "--where", "kind=decision", "--where", "status=active",
                "--where", f"topic_key={topic}", "--limit", "5"))
        else:
            calls.append(self.call(
                "find_decisions_min", "find", f"/{alias}/observations",
                "--where", "kind=decision", "--where", "status=active",
                "--limit", "3"))
        if pref_keys:
            for key in pref_keys:
                calls.append(self.call(
                    "find_preferences_key", "find", f"/{alias}/preferences",
                    "--where", f"pref_key={key}", "--where", "status=active",
                    "--limit", "2"))
        else:
            calls.append(self.call(
                "find_preferences_min", "find", f"/{alias}/preferences",
                "--where", "status=active", "--limit", "3"))
        return {"calls": calls, "session": located["session"],
                "checkpoint": located["checkpoint"], "rows": located["rows"]}


def topic_of(rows, session_id):
    """Topic_key de la tarea en curso, derivado del goal de la sesión elegida."""
    for row in rows:
        fields = row["fields"]
        if fields["id"]["data"] != session_id:
            continue
        match = TOPIC_IN_GOAL.search((fields.get("goal") or {}).get("data", ""))
        if match:
            return match.group(1)
    return None


def run_stats(runs):
    """Invocaciones, bytes, tokens y p95 por reanudación de una variante."""
    totales = [sum(c["ms"] for c in r["calls"]) for r in runs]
    bytes_rep = sorted(sum(c["bytes"] for c in r["calls"]) for r in runs)
    mediana = bytes_rep[len(bytes_rep) // 2]
    return {
        "invocations": sorted({len(r["calls"]) for r in runs}),
        "bytes": mediana,
        "bytes_min": bytes_rep[0],
        "bytes_max": bytes_rep[-1],
        "tokens": mediana / 4,
        "p95": stats(totales)["p95"],
        "calls": [c for r in runs for c in r["calls"]],
    }



def build_small_base(path):
    for sufijo in ("", "-journal"):
        p = Path(str(path) + sufijo)
        if p.exists():
            p.unlink()
    con = sqlite3.connect(path)
    con.execute("PRAGMA foreign_keys = ON")
    con.executescript(SCHEMA.read_text(encoding="utf-8"))
    con.executescript(SEED.read_text(encoding="utf-8"))
    con.commit()
    con.close()


def counts(db_path):
    con = sqlite3.connect(db_path)
    result = {
        tabla: con.execute(f"SELECT count(*) FROM {tabla}").fetchone()[0]
        for tabla in ("meta", "sessions", "observations", "checkpoints", "preferences")
    }
    con.close()
    return result


def hardware():
    cpu = "UNKNOWN"
    mem_kb = None
    with open("/proc/cpuinfo", encoding="utf-8") as fh:
        for line in fh:
            if line.startswith("model name"):
                cpu = line.split(":", 1)[1].strip()
                break
    with open("/proc/meminfo", encoding="utf-8") as fh:
        for line in fh:
            if line.startswith("MemTotal"):
                mem_kb = int(line.split()[1])
                break
    return {
        "cpu": cpu,
        "cores": os.cpu_count(),
        "mem_gib": round(mem_kb / 1024 / 1024, 1) if mem_kb else None,
        "kernel": platform.platform(),
    }


def apply_field_wrappers(fields):
    return {k: {"kind": "text", "data": v} for k, v in fields.items()}


def content_hash(table, fields):
    proc = subprocess.run(
        [sys.executable, str(HASH_TOOL), table],
        input=json.dumps(fields, ensure_ascii=False), capture_output=True, text=True)
    if proc.returncode != 0:
        raise RuntimeError(f"content-hash.py falló: {proc.stderr}")
    return proc.stdout.strip()


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--db-ai", default="db-ai")
    ap.add_argument("--workdir", default="/tmp/opencode/acm-bench")
    ap.add_argument("--reps", type=int, default=100)
    ap.add_argument("--seed", type=int, default=20261119)
    ap.add_argument("--skip-small", action="store_true")
    ap.add_argument("--reuse-fixture", action="store_true",
                    help="reutiliza memory.db si existe en vez de regenerarlo")
    args = ap.parse_args()

    workdir = Path(args.workdir)
    workdir.mkdir(parents=True, exist_ok=True)
    db = workdir / "memory.db"
    small_db = workdir / "small.db"
    alias = "mem-bench"
    small_alias = "mem-bench-small"

    if not args.reuse_fixture or not db.exists():
        subprocess.run([sys.executable, str(GEN), "--db", str(db),
                        "--seed", str(args.seed)], check=True)
    bench = Harness(args.db_ai, workdir)
    bench.register(alias, db)

    # Versión del binario (C-ACM-01: puede no existir en binarios antiguos).
    ver = subprocess.run([args.db_ai, "version"], capture_output=True,
                         env=bench.env)
    try:
        version = json.loads(ver.stdout.decode()).get("data", {})
    except Exception:
        version = None
    version_text = json.dumps(version, ensure_ascii=False) if version else \
        f"E_USAGE (exit {ver.returncode})"

    fixture = counts(db)
    if fixture["observations"] < 10000:
        raise SystemExit(f"fixture insuficiente: {fixture['observations']}")

    # Calentamiento fuera de la medición: caché de página del sistema.
    for _ in range(3):
        bench.resume(alias)

    # --- guion de reanudación, N repeticiones --------------------------------
    reps = []
    for _ in range(args.reps):
        rep = bench.resume(alias)
        reps.append(rep)

    invocaciones = [len(r["calls"]) for r in reps]
    if len(set(invocaciones)) != 1:
        raise SystemExit(f"invocaciones inestables por repetición: {set(invocaciones)}")
    invocaciones = invocaciones[0]

    todas = [c for r in reps for c in r["calls"]]
    por_op = {}
    for call in todas:
        por_op.setdefault(call["op"], []).append(call["ms"])
    totales_rep = [sum(c["ms"] for c in r["calls"]) for r in reps]
    bytes_rep = [sum(c["bytes"] for c in r["calls"]) for r in reps]
    bytes_por_op = {}
    truncado_por_op = {}
    filas_por_op = {}
    for call in reps[0]["calls"]:
        bytes_por_op.setdefault(call["op"], []).append(call["bytes"])
        truncado_por_op[call["op"]] = call["truncated"]
        filas_por_op[call["op"]] = len(call["rows"])

    stat_invocacion = stats([c["ms"] for c in todas])
    elegida = reps[0]["session"]
    checkpoint = reps[0]["checkpoint"]

    # --- guion acotado por topic_key (decisión del PO, 2026-11-19) -----------
    topic = topic_of(reps[0]["rows"], elegida)
    if not topic:
        raise SystemExit("no se pudo derivar el topic_key de la tarea en curso")
    for _ in range(3):
        bench.resume_bounded(alias, topic)
        bench.resume_bounded(alias, topic, known_session=elegida)
        bench.resume_bounded(alias, None)
        bench.resume_bounded(alias, topic, pref_keys=())
    bounded_runs = {
        "frio": [bench.resume_bounded(alias, topic) for _ in range(args.reps)],
        "handoff": [bench.resume_bounded(alias, topic, known_session=elegida)
                    for _ in range(args.reps)],
        "sin_topic": [bench.resume_bounded(alias, None) for _ in range(args.reps)],
        "sin_claves": [bench.resume_bounded(alias, topic, pref_keys=())
                       for _ in range(args.reps)],
    }
    bounded = {name: run_stats(runs) for name, runs in bounded_runs.items()}
    nuevas = [c for c in (bounded["frio"]["calls"] + bounded["sin_topic"]["calls"]
                          + bounded["sin_claves"]["calls"])
              if c["op"] in NEW_OPS]
    stat_nuevas = {}
    nuevas_filas = {}
    nuevas_trunc = {}
    nuevas_bytes = {}
    for call in nuevas:
        stat_nuevas.setdefault(call["op"], []).append(call["ms"])
        nuevas_filas[call["op"]] = len(call["rows"])
        nuevas_trunc[call["op"]] = call["truncated"]
        nuevas_bytes[call["op"]] = call["bytes"]
    stat_nuevas = {op: stats(vals) for op, vals in stat_nuevas.items()}
    stat_bounded_inv = stats([c["ms"] for c in bounded["frio"]["calls"]])

    # --- conjunto mínimo anterior (sesión conocida, sin ventana) -------------
    minimo_reps = [bench.resume(alias, known_session=elegida) for _ in range(20)]
    minimo_inv = {len(r["calls"]) for r in minimo_reps}
    minimo_bytes = [sum(c["bytes"] for c in r["calls"]) for r in minimo_reps]

    # --- P3: ¿el checkpoint recuperado es el más reciente? -------------------
    corte = iso(dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=14))
    con = sqlite3.connect(db)
    esperada = con.execute(
        "SELECT s.id FROM sessions s WHERE s.started_at >= ? AND EXISTS ("
        " SELECT 1 FROM checkpoints c WHERE c.session_id = s.id"
        " AND c.status = 'active') ORDER BY s.id DESC LIMIT 1", (corte,)).fetchone()
    esperada = esperada[0] if esperada else None
    esperado_cp = con.execute(
        "SELECT max(id) FROM checkpoints WHERE session_id = ? AND status = 'active'",
        (esperada,)).fetchone()[0] if esperada else None
    con.close()
    p3_ok = elegida == esperada and checkpoint == esperado_cp

    # --- M-ACM-04: captura consolidada + verificación posterior --------------
    # En una copia de la base: las filas de la captura no deben alterar el
    # fixture que mide la reanudación (la sesión nueva cambiaría la elección P3).
    capture_db = workdir / "capture.db"
    capture_alias = "mem-bench-capture"
    shutil.copyfile(db, capture_db)
    bench.register(capture_alias, capture_db)
    ahora = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
    session_id = uuid7(ahora)
    bench.call("touch_session", "touch", f"/{capture_alias}/sessions", "--data",
               json.dumps(apply_field_wrappers({
                   "id": session_id, "agent_id": "benchmark-harness",
                   "source": "agent", "goal": "Medir el coste de captura de M-ACM-04",
                   "status": "open", "started_at": iso(ahora),
                   "created_at": iso(ahora), "updated_at": iso(ahora),
               })))
    capturas = []
    for i in range(1, 11):
        momento = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
        campos = {
            "kind": "discovery",
            "title": f"Benchmark M-ACM-04: captura consolidada {i} ({iso(momento)})",
            "body": "Fila creada por el arnés para medir touch más verificación "
                    "posterior con cat; no pertenece a la memoria del proyecto.",
            "rationale": None, "alternatives": None, "consequences": None,
            "topic_key": "metrics/m-acm-04",
        }
        oid = uuid7(momento)
        datos = {
            "id": oid, "session_id": session_id, "agent_id": "benchmark-harness",
            "source": "agent", "kind": "discovery", "title": campos["title"],
            "body": campos["body"], "confidence": "confirmed",
            "last_seen_at": iso(momento),
            "content_hash": content_hash("observations", campos),
            "origin_ref": "tests/benchmark.py",
            "created_at": iso(momento), "updated_at": iso(momento),
        }
        invocaciones_captura = 0
        bench.call("touch_observation", "touch", f"/{capture_alias}/observations",
                   "--data", json.dumps(apply_field_wrappers(datos)))
        invocaciones_captura += 1
        verificado = bench.call("cat_observation", "cat",
                                f"/{capture_alias}/observations/{oid}")
        invocaciones_captura += 1
        titulo = (verificado["fields"].get("title") or {}).get("data")
        if titulo != campos["title"]:
            raise SystemExit(f"captura {i}: la verificación posterior no coincide")
        capturas.append(invocaciones_captura)

    # --- paginación y latencias de grep/find ---------------------------------
    grep_ms = []
    grep_trunc = None
    grep_rows = None
    findf_ms = []
    findf_trunc = None
    findf_rows = None
    for _ in range(20):
        g = bench.call("grep_literal", "grep", f"/{alias}/observations",
                       "serializa", "--limit", "20")
        grep_ms.append(g["ms"])
        grep_trunc, grep_rows = g["truncated"], len(g["entries"])
        f = bench.call("find_filtered", "find", f"/{alias}/observations",
                       "--where", "kind=decision", "--where", "status=active",
                       "--limit", "20")
        findf_ms.append(f["ms"])
        findf_trunc, findf_rows = f["truncated"], len(f["rows"])

    # --- base pequeña (seed de demostración) ---------------------------------
    pequena = None
    if not args.skip_small:
        build_small_base(small_db)
        bench.register(small_alias, small_db)
        try:
            p = bench.resume(small_alias)
        except RuntimeError as exc:
            pequena = {"error": str(exc)}
        else:
            pequena = {
                "counts": counts(small_db),
                "invocations": len(p["calls"]),
                "bytes": sum(c["bytes"] for c in p["calls"]),
                "ops": [(c["op"], round(c["ms"], 2), c["bytes"], c["truncated"])
                        for c in p["calls"]],
            }

    # --- informe -------------------------------------------------------------
    hw = hardware()
    stat_op = {op: stats(vals) for op, vals in por_op.items()}
    stat_total = stats(totales_rep)
    bytes_mediana = sorted(bytes_rep)[len(bytes_rep) // 2]
    tokens_mediana = bytes_mediana / 4
    minimo_bytes_mediana = sorted(minimo_bytes)[len(minimo_bytes) // 2]

    verdict_m1 = ("SUPERADO" if bounded["frio"]["invocations"][-1] <= OBJECTIVE_M1_MAX
                  else "FALLIDO")
    verdict_m2_bounded = ("SUPERADO"
                          if stat_bounded_inv["p95"] <= OBJECTIVE_M2_P95_MS
                          else "FALLIDO")
    verdict_m3 = "SUPERADO" if tokens_mediana <= OBJECTIVE_M3_MAX_TOKENS else "FALLIDO"
    verdict_m4 = ("SUPERADO" if max(capturas) <= OBJECTIVE_M4_MAX_INVOCATIONS
                  else "FALLIDO")
    verdict_bounded = {
        name: ("SUPERADO" if v["tokens"] <= OBJECTIVE_M3_MAX_TOKENS else "FALLIDO")
        for name, v in bounded.items()
    }

    print(f"# Benchmark ACM — fixture de 10 000+ observaciones\n")
    print(f"- Fecha: {iso(dt.datetime.now(dt.timezone.utc))}")
    print(f"- db-ai: `{args.db_ai}`; `db-ai version` → `{version_text}`")
    print(f"- Hardware: {hw['cpu']} ({hw['cores']} vCPU), "
          f"{hw['mem_gib']} GiB RAM, {hw['kernel']}")
    print(f"- Fixture: seed {args.seed}, {fixture['sessions']} sesiones, "
          f"{fixture['observations']} observaciones, "
          f"{fixture['checkpoints']} checkpoints, "
          f"{fixture['preferences']} preferencias")
    print(f"- Repeticiones: {args.reps} (+3 de calentamiento, excluidas)\n")

    print("## Guion completo anterior (histórico, antes de la decisión del PO)\n")
    print("| # | Operación | Llamadas | p50 (ms) | p95 (ms) | p99 (ms) | "
          "máx (ms) | Filas/página | Truncated | Bytes/página |")
    print("|---|---|---|---|---|---|---|---|---|---|")
    for i, op in enumerate(por_op, 1):
        s = stat_op[op]
        print(f"| {i} | `{op}` | {s['n']} | {ms(s['p50'])} | {ms(s['p95'])} | "
              f"{ms(s['p99'])} | {ms(s['max'])} | {filas_por_op[op]} | "
              f"{truncado_por_op[op]} | {bytes_por_op[op][0]} |")
    print(f"| | **Total por reanudación** | {args.reps} | "
          f"{ms(stat_total['p50'])} | {ms(stat_total['p95'])} | "
          f"{ms(stat_total['p99'])} | {ms(stat_total['max'])} | | | "
          f"{bytes_mediana} |\n")
    print(f"- Invocaciones por reanudación: **{invocaciones}** "
          f"(conjunto mínimo con sesión conocida: "
          f"{', '.join(str(x) for x in sorted(minimo_inv))}).")
    print(f"- Bytes de stdout por reanudación: **{bytes_mediana}** "
          f"(rango {min(bytes_rep)}–{max(bytes_rep)}); "
          f"estimación declarada bytes/4 = **{tokens_mediana:.0f} tokens**.")
    print(f"- Conjunto mínimo anterior (4 `find`, sesión conocida): "
          f"{minimo_bytes_mediana} bytes ≈ {minimo_bytes_mediana / 4:.0f} tokens.")
    print(f"- p95 por invocación (todas las lecturas del guion): "
          f"**{ms(stat_invocacion['p95'])} ms** "
          f"(p50 {ms(stat_invocacion['p50'])} ms, p99 {ms(stat_invocacion['p99'])} ms, "
          f"n={stat_invocacion['n']}).\n")

    print("## Guion acotado por topic_key (decisión del PO, 2026-11-19)\n")
    print(f"- Tarea de referencia: `{topic}` (derivada del goal de la sesión "
          f"elegida); claves de preferencia aplicables: "
          f"{', '.join(f'`{k}`' for k in PREF_KEYS)}.")
    print("- Decisiones: solo `kind=decision` + `status=active` + `topic_key` del "
          "tema, límite 5. Preferencias: una invocación por `pref_key`, límite 2. "
          "El historial de la sesión no forma parte del guion por defecto.")
    print("- Sin topic conocido: página mínima de decisiones (límite 3) con aviso "
          "de recorte; sin claves: página mínima de preferencias (límite 3).\n")
    print("| Variante | Invocaciones | Bytes | Tokens (bytes/4) | "
          "p95 reanudación (ms) | Veredicto M-ACM-03 |")
    print("|---|---|---|---|---|---|")
    etiquetas = {
        "frio": "en frío: ventana + checkpoint",
        "handoff": "handoff: `session_id` conocido",
        "sin_topic": "sin topic: decisiones mínimas",
        "sin_claves": "sin claves: preferencias mínimas",
    }
    for name in ("frio", "handoff", "sin_topic", "sin_claves"):
        v = bounded[name]
        print(f"| {etiquetas[name]} | {v['invocations'][0]} | {v['bytes']} | "
              f"{v['tokens']:.0f} | {ms(v['p95'])} | {verdict_bounded[name]} |")
    print()
    print("| Operación nueva | Llamadas | p50 (ms) | p95 (ms) | p99 (ms) | "
          "Filas | Truncated | Bytes/página |")
    print("|---|---|---|---|---|---|---|---|")
    for op in NEW_OPS:
        if op not in stat_nuevas:
            continue
        s = stat_nuevas[op]
        print(f"| `{op}` | {s['n']} | {ms(s['p50'])} | {ms(s['p95'])} | "
              f"{ms(s['p99'])} | {nuevas_filas[op]} | {nuevas_trunc[op]} | "
              f"{nuevas_bytes[op]} |")
    stat_nuevas_total = stats([c["ms"] for c in nuevas])
    print(f"\n- p95 de las operaciones nuevas: **{ms(stat_nuevas_total['p95'])} ms** "
          f"(p50 {ms(stat_nuevas_total['p50'])} ms, "
          f"p99 {ms(stat_nuevas_total['p99'])} ms, n={stat_nuevas_total['n']}).")
    print(f"- Aviso de recorte sin topic: `find_decisions_min` devuelve "
          f"{nuevas_filas.get('find_decisions_min')} filas con "
          f"`truncated={nuevas_trunc.get('find_decisions_min')}`; no son todas. "
          f"`find_preferences_min` devuelve "
          f"{nuevas_filas.get('find_preferences_min')} filas con "
          f"`truncated={nuevas_trunc.get('find_preferences_min')}` y el agente "
          "elige las claves aplicables de esa página.\n")

    print("## Paginación (primera repetición)\n")
    print("| Operación | Límite | Filas devueltas | truncated |")
    print("|---|---|---|---|")
    limites = {"find_sessions_window": 50, "find_checkpoint_active": 5,
               "find_observations_session": 50, "find_decisions_active": 10,
               "find_preferences_active": 20}
    for op in por_op:
        print(f"| `{op}` | {limites.get(op, '—')} | {filas_por_op[op]} | "
              f"{truncado_por_op[op]} |")
    print()

    print("## Recuperación del checkpoint reciente (procedimiento de P3)\n")
    print(f"- Sesión elegida por el guion: `{elegida}`")
    print(f"- Sesión esperada (más reciente con checkpoint activo): `{esperada}`")
    print(f"- Checkpoint recuperado: `{checkpoint}`; esperado: `{esperado_cp}`")
    print(f"- Coincidencia: **{'SÍ' if p3_ok else 'NO'}**\n")

    print("## M-ACM-04 — captura consolidada con verificación posterior\n")
    print(f"- Guion por captura: `touch` (1) + `cat` de verificación (1).")
    print(f"- Invocaciones por captura: {capturas} "
          f"(máx **{max(capturas)}**, objetivo ≤ {OBJECTIVE_M4_MAX_INVOCATIONS}).")
    print(f"- Capturas verificadas: {len(capturas)}/10.\n")

    print("## Búsqueda literal y find filtrado sobre 10k filas\n")
    print("| Operación | Llamadas | p50 (ms) | p95 (ms) | p99 (ms) | Filas | Truncated |")
    print("|---|---|---|---|---|---|---|")
    sg = stats(grep_ms)
    sf = stats(findf_ms)
    print(f"| `grep observations \"serializa\" --limit 20` | {sg['n']} | "
          f"{ms(sg['p50'])} | {ms(sg['p95'])} | {ms(sg['p99'])} | {grep_rows} | "
          f"{grep_trunc} |")
    print(f"| `find observations kind=decision status=active --limit 20` | "
          f"{sf['n']} | {ms(sf['p50'])} | {ms(sf['p95'])} | {ms(sf['p99'])} | "
          f"{findf_rows} | {findf_trunc} |\n")

    if pequena and "error" in pequena:
        print("## Comparación con la base pequeña (seed de demostración)\n")
        print(f"- No medible con el guion actual: {pequena['error']} "
              "(el seed tiene fechas fijas).\n")
    elif pequena:
        print("## Comparación con la base pequeña (seed de demostración)\n")
        c = pequena["counts"]
        print(f"- Filas: {c['sessions']} sesiones, {c['observations']} "
              f"observaciones, {c['checkpoints']} checkpoints, "
              f"{c['preferences']} preferencias.")
        print(f"- Mismo guion: **{pequena['invocations']} invocaciones**, "
              f"{pequena['bytes']} bytes ≈ {pequena['bytes'] / 4:.0f} tokens.")
        print("- Detalle: " + "; ".join(
            f"`{op}` {lat} ms, {b} B, truncated={t}"
            for op, lat, b, t in pequena["ops"]) + "\n")

    print("## Veredictos\n")
    print("| Métrica | Objetivo | Resultado | Veredicto |")
    print("|---|---|---|---|")
    print(f"| M-ACM-01 | ≤ {OBJECTIVE_M1_MAX} invocaciones | "
          f"{bounded['frio']['invocations'][0]} en frío; "
          f"{bounded['handoff']['invocations'][0]} con `session_id` | "
          f"{verdict_m1} |")
    print(f"| M-ACM-02 | p95 ≤ {OBJECTIVE_M2_P95_MS} ms | "
          f"{ms(stat_bounded_inv['p95'])} ms (guion acotado; guion completo "
          f"{ms(stat_invocacion['p95'])}) | {verdict_m2_bounded} |")
    print(f"| M-ACM-03 (guion acotado, con `session_id` conocido) | "
          f"≤ {OBJECTIVE_M3_MAX_TOKENS} tokens (bytes/4) | "
          f"{bounded['handoff']['tokens']:.0f} tokens "
          f"({bounded['handoff']['bytes']} B) | {verdict_bounded['handoff']} |")
    print(f"| M-ACM-03 (guion acotado, en frío sin sesión) | "
          f"≤ {OBJECTIVE_M3_MAX_TOKENS} tokens (bytes/4) | "
          f"{bounded['frio']['tokens']:.0f} tokens "
          f"({bounded['frio']['bytes']} B) | {verdict_bounded['frio']} |")
    print(f"| M-ACM-03 (guion completo anterior, histórico) | "
          f"≤ {OBJECTIVE_M3_MAX_TOKENS} tokens (bytes/4) | "
          f"{tokens_mediana:.0f} tokens ({bytes_mediana} B) | {verdict_m3} |")
    print(f"| M-ACM-04 | ≤ {OBJECTIVE_M4_MAX_INVOCATIONS} invocaciones | "
          f"{max(capturas)} | {verdict_m4} |")
    print("| M-ACM-05 | 5 de 5 | PENDIENTE (P6) | NO MEDIDO |")


if __name__ == "__main__":
    main()
