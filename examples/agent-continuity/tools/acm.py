#!/usr/bin/env python3
"""Adaptador ACM: cinco comandos agent-first sobre db-ai.

Contrato (una sola línea JSON compacta por invocación):
  éxito  {"ok":true,...}
  error  {"ok":false,"error":"<código db-ai>","message":"..."}

Comandos:
  start --session ID [--goal G]      abre sesión; restaura la abierta con ese ID
  remember --session ID --kind K --title T --body B [...]
  search --session ID --query T [--limit N]
  checkpoint --session ID --state S --next-steps N [...]
  close --session ID [--summary S] [...]

Identidad: --session, o OPENCODE_SESSION_ID si falta. El alias de montaje es
ACM_ALIAS (por defecto "mem") y el agente ACM_AGENT_ID (por defecto "opencode").
db-ai debe estar en PATH y el montaje debe existir (init.sh).
"""
import argparse
import datetime as dt
import json
import os
import re
import subprocess
import sys
import unicodedata
from pathlib import Path

HERE = Path(__file__).resolve().parent
HASH_TOOL = HERE / "content-hash.py"
ALIAS = os.environ.get("ACM_ALIAS", "mem")
AGENT_ID = os.environ.get("ACM_AGENT_ID", "opencode")
SESSION_RE = re.compile(r"[A-Za-z0-9._-]{1,128}")
KINDS = ("discovery", "decision", "pattern", "bugfix", "architecture", "learning")
CONFIDENCES = ("confirmed", "inference", "hypothesis", "proposal", "unknown")
EXIT_CODES = {
    "E_USAGE": 2, "E_NOT_FOUND": 3, "E_STALE_SESSION": 3, "E_DENIED": 4,
    "E_CONFLICT": 5, "E_CONSTRAINT": 6, "E_BUSY": 7, "E_TIMEOUT": 7,
    "E_UNSUPPORTED": 8, "E_LIMIT": 8, "E_INTERNAL": 9, "E_COMMIT_UNKNOWN": 10,
}


class AcmError(Exception):
    """Error de contrato: se serializa tal cual y nunca deja traza."""

    def __init__(self, code: str, message: str):
        super().__init__(message)
        self.code = code
        self.message = message


# --- utilidades --------------------------------------------------------------

def iso(moment: dt.datetime) -> str:
    return moment.strftime("%Y-%m-%dT%H:%M:%SZ")


def now() -> str:
    return iso(dt.datetime.now(dt.timezone.utc))


def uuid7(moment: dt.datetime) -> str:
    ms = int(moment.timestamp() * 1000)
    rnd = int.from_bytes(os.urandom(8), "big")
    return "{:08x}-{:04x}-7{:03x}-{:04x}-{:012x}".format(
        ms >> 16, ms & 0xFFFF, (rnd >> 52) & 0xFFF,
        (0b10 << 14) | ((rnd >> 38) & 0x3FFF), rnd & 0xFFFFFFFFFFFF)


def db_ai(*args: str) -> dict:
    """Una invocación a db-ai; cualquier fallo se traduce a AcmError."""
    proc = subprocess.run(["db-ai", *args], capture_output=True,
                          encoding="utf-8", text=True)
    try:
        payload = json.loads(proc.stdout)
    except json.JSONDecodeError:
        raise AcmError("E_INTERNAL",
                       f"db-ai devolvió salida no JSON (exit {proc.returncode})")
    if not payload.get("ok"):
        err = payload.get("error") or {}
        raise AcmError(err.get("code", "E_INTERNAL"),
                       err.get("message", "db-ai falló sin mensaje"))
    return payload["data"]


def rows(path: str, *wheres: str, order: str | None = None,
         limit: int = 10) -> list[dict]:
    args = ["find", path, "--limit", str(limit)]
    for where in wheres:
        args += ["--where", where]
    if order:
        args += ["--order", order]
    return db_ai(*args)["rows"]


def fields(row: dict) -> dict:
    return {name: value["data"] for name, value in row["fields"].items()}


def content_hash(table: str, data: dict) -> str:
    # El algoritmo normativo vive en content-hash.py; aquí solo se invoca.
    proc = subprocess.run(
        [sys.executable, str(HASH_TOOL), table],
        input=json.dumps(data, ensure_ascii=False),
        capture_output=True, encoding="utf-8", text=True)
    if proc.returncode != 0:
        raise AcmError("E_INTERNAL",
                       f"content-hash.py falló: {proc.stderr.strip()}")
    return proc.stdout.strip()


def session_id(args: argparse.Namespace) -> str:
    sid = args.session or os.environ.get("OPENCODE_SESSION_ID")
    if not sid:
        raise AcmError("E_USAGE",
                       "falta --session (o la variable OPENCODE_SESSION_ID)")
    if not SESSION_RE.fullmatch(sid):
        raise AcmError("E_USAGE",
                       "el identificador de sesión solo admite [A-Za-z0-9._-]")
    return sid


def load_session(sid: str) -> tuple[dict, str]:
    found = rows(f"/{ALIAS}/sessions", f"id={sid}", limit=1)
    if not found:
        raise AcmError("E_NOT_FOUND", f"la sesión '{sid}' no existe")
    return fields(found[0]), found[0]["etag"]


def open_session(sid: str) -> tuple[dict, str]:
    session, etag = load_session(sid)
    if session["status"] != "open":
        raise AcmError("E_CONSTRAINT",
                       f"la sesión '{sid}' está {session['status']}, no abierta")
    return session, etag


def require_text(value: str | None, name: str) -> str:
    text = (value or "").strip()
    if not text:
        raise AcmError("E_USAGE", f"falta {name}")
    return text


def normalize(text: str) -> str:
    """Clave de comparación: sin mayúsculas ni acentos; sin stemming."""
    return "".join(char for char in unicodedata.normalize("NFD", text.casefold())
                   if not unicodedata.combining(char))


def session_view(session: dict) -> dict:
    # db-ai renders SQL NULL as "", so the JSON contract keeps null here.
    return {"id": session.get("id"), "agent_id": session.get("agent_id"),
            "goal": session.get("goal"), "status": session.get("status"),
            "started_at": session.get("started_at"),
            "ended_at": session.get("ended_at") or None,
            "summary": session.get("summary") or None}


def consolidate(observation_id: str, row: dict, timestamp: str) -> dict:
    """Suma una repetición; ante E_CONFLICT relee y reintenta (máx 3)."""
    for attempt in range(3):
        count = int(fields(row)["duplicate_count"]) + 1
        try:
            db_ai("write", f"/{ALIAS}/observations/{observation_id}",
                  "--if-match", row["etag"], "--data", json.dumps(
                      {"duplicate_count": count, "last_seen_at": timestamp,
                       "updated_at": timestamp}))
        except AcmError as exc:
            if exc.code != "E_CONFLICT" or attempt == 2:
                raise
            # Otro proceso consolidó antes: el ETag es obsoleto, releer la fila.
            found = rows(f"/{ALIAS}/observations",
                         f"content_hash={fields(row)['content_hash']}", limit=1)
            if not found:
                raise
            row = found[0]
            continue
        return {"result": "consolidated", "id": observation_id,
                "duplicate_count": count}


# --- comandos ----------------------------------------------------------------

def cmd_start(args: argparse.Namespace) -> dict:
    sid = session_id(args)
    goal = (args.goal or f"sesión {sid}").strip()
    found = rows(f"/{ALIAS}/sessions", f"id={sid}", limit=1)
    if found:
        session = fields(found[0])
        if session["status"] != "open":
            raise AcmError(
                "E_CONSTRAINT",
                f"la sesión '{sid}' está {session['status']}; usa otro identificador")
        active = rows(f"/{ALIAS}/checkpoints", f"session_id={sid}",
                      "status=active", order="created_at:desc", limit=1)
        result = {"ok": True, "result": "restored",
                  "session": session_view(session)}
        if active:
            result["checkpoint"] = fields(active[0])
        return result

    timestamp = now()
    session = {
        "id": sid, "agent_id": AGENT_ID, "source": "agent", "goal": goal,
        "status": "open", "started_at": timestamp, "ended_at": None,
        "summary": None, "created_at": timestamp, "updated_at": timestamp,
    }
    db_ai("touch", f"/{ALIAS}/sessions",
          "--data", json.dumps(session, ensure_ascii=False))
    return {"ok": True, "result": "created", "session": session_view(session)}


def cmd_remember(args: argparse.Namespace) -> dict:
    sid = session_id(args)
    open_session(sid)
    kind = require_text(args.kind, "--kind")
    if kind not in KINDS:
        raise AcmError("E_USAGE", f"--kind debe ser uno de: {', '.join(KINDS)}")
    confidence = args.confidence or "confirmed"
    if confidence not in CONFIDENCES:
        raise AcmError("E_USAGE",
                       f"--confidence debe ser uno de: {', '.join(CONFIDENCES)}")
    title = require_text(args.title, "--title")
    body = require_text(args.body, "--body")
    content = {
        "kind": kind, "title": title, "body": body,
        "rationale": (args.rationale or "").strip(),
        "alternatives": (args.alternatives or "").strip(),
        "consequences": (args.consequences or "").strip(),
        "topic_key": (args.topic_key or "").strip(),
    }
    if kind == "decision" and not (content["topic_key"] and content["rationale"]):
        raise AcmError("E_USAGE",
                       "una decisión exige --topic-key y --rationale")
    digest = content_hash("observations", content)
    timestamp = now()
    found = rows(f"/{ALIAS}/observations", f"content_hash={digest}", limit=1)
    if found:
        return {"ok": True, **consolidate(fields(found[0])["id"], found[0],
                                          timestamp), "content_hash": digest}

    observation = {
        **content, "id": uuid7(dt.datetime.now(dt.timezone.utc)),
        "session_id": sid, "agent_id": AGENT_ID, "source": "agent",
        "confidence": confidence, "status": "active", "duplicate_count": 1,
        "last_seen_at": timestamp, "content_hash": digest,
        "created_at": timestamp, "updated_at": timestamp,
    }
    try:
        db_ai("touch", f"/{ALIAS}/observations",
              "--data", json.dumps(observation, ensure_ascii=False))
    except AcmError as exc:
        if exc.code != "E_CONSTRAINT":
            raise
        # Otro escritor ganó la carrera del hash: consolidar, no reintentar.
        found = rows(f"/{ALIAS}/observations", f"content_hash={digest}", limit=1)
        if not found:
            raise
        return {"ok": True, **consolidate(fields(found[0])["id"], found[0],
                                          timestamp), "content_hash": digest}
    return {"ok": True, "result": "created", "id": observation["id"],
            "content_hash": digest, "duplicate_count": 1}


def checkpoint_batch(sid: str, goal: str, state: str, next_steps: str,
                     files: str, open_questions: str) -> tuple[list[dict] | None, str]:
    """Operaciones de un checkpoint nuevo que sustituye al activo anterior.

    Un duplicado exacto de content_hash es un no-op (DESIGN §5.2): devuelve
    None y el id de la fila existente, porque UNIQUE(content_hash) lo impide.
    """
    timestamp = now()
    digest = content_hash("checkpoints", {
        "goal": goal, "state": state, "next_steps": next_steps,
        "files": files, "open_questions": open_questions})
    existing = rows(f"/{ALIAS}/checkpoints", f"content_hash={digest}", limit=1)
    if existing:
        return None, fields(existing[0])["id"]
    checkpoint_id = uuid7(dt.datetime.now(dt.timezone.utc))
    operations = [{"op": "touch", "path": f"/{ALIAS}/checkpoints", "data": {
        "id": checkpoint_id, "session_id": sid, "agent_id": AGENT_ID,
        "source": "agent", "goal": goal, "state": state,
        "next_steps": next_steps, "files": files or None,
        "open_questions": open_questions or None, "status": "active",
        "content_hash": digest, "created_at": timestamp,
        "updated_at": timestamp}}]
    active = rows(f"/{ALIAS}/checkpoints", f"session_id={sid}",
                  "status=active", order="created_at:desc", limit=1)
    if active:
        operations.append({
            "op": "write",
            "path": f"/{ALIAS}/checkpoints/{fields(active[0])['id']}",
            "if_match": active[0]["etag"],
            "data": {"status": "superseded", "superseded_by": checkpoint_id,
                     "updated_at": timestamp}})
    return operations, checkpoint_id


def cmd_checkpoint(args: argparse.Namespace) -> dict:
    sid = session_id(args)
    session, _ = open_session(sid)
    state = require_text(args.state, "--state")
    next_steps = require_text(args.next_steps, "--next-steps")
    operations, checkpoint_id = checkpoint_batch(
        sid, session["goal"], state, next_steps,
        (args.files or "").strip(), (args.open_questions or "").strip())
    if operations is None:
        return {"ok": True, "result": "existing", "checkpoint": checkpoint_id}
    db_ai("batch", "--data", json.dumps(operations, ensure_ascii=False))
    return {"ok": True, "result": "created", "checkpoint": checkpoint_id,
            "superseded": len(operations) - 1}


def cmd_close(args: argparse.Namespace) -> dict:
    sid = session_id(args)
    session, etag = open_session(sid)
    summary = (args.summary or "sesión cerrada").strip()
    state = (args.state or args.summary or "sesión cerrada").strip()
    next_steps = (args.next_steps or "ninguno").strip()
    operations, checkpoint_id = checkpoint_batch(
        sid, session["goal"], state, next_steps,
        (args.files or "").strip(), (args.open_questions or "").strip())
    operations = operations or []
    timestamp = now()
    operations.append({
        "op": "write", "path": f"/{ALIAS}/sessions/{sid}", "if_match": etag,
        "data": {"status": "closed", "ended_at": timestamp,
                 "summary": summary, "updated_at": timestamp}})
    db_ai("batch", "--data", json.dumps(operations, ensure_ascii=False))
    return {"ok": True, "result": "closed", "session": sid,
            "checkpoint": checkpoint_id, "ended_at": timestamp}


def cmd_search(args: argparse.Namespace) -> dict:
    sid = session_id(args)
    load_session(sid)
    query = (args.query or "").strip()
    terms = re.findall(r"\w+", normalize(query))
    if not terms:
        raise AcmError("E_USAGE", "query vacía")
    limit = max(1, min(args.limit, 100))
    # status=active va en el filtro, antes del límite; 1000 es el máximo de --limit.
    # shortcut: no hay paginación, más de 1000 observaciones activas se truncan.
    candidates = rows(f"/{ALIAS}/observations", "status=active", limit=1000)
    results = []
    for row in candidates:
        observation = fields(row)
        haystack = normalize(" ".join(str(observation.get(key) or "") for key in
                                      ("title", "body", "topic_key")))
        if all(term in haystack for term in terms):
            results.append({k: observation.get(k) for k in (
                "id", "kind", "title", "body", "topic_key", "session_id",
                "created_at")})
            if len(results) == limit:
                break
    return {"ok": True, "query": query, "count": len(results),
            "results": results}


# --- entrada -----------------------------------------------------------------

def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Adaptador ACM sobre db-ai (5 comandos, JSON compacto).")
    sub = parser.add_subparsers(dest="command", required=True)

    def add(name: str, handler, help_text: str):
        cmd = sub.add_parser(name, help=help_text)
        cmd.add_argument("--session", help="identificador de sesión explícito")
        cmd.set_defaults(handler=handler)
        return cmd

    start = add("start", cmd_start, "abre o restaura una sesión")
    start.add_argument("--goal", help="objetivo de la sesión")

    remember = add("remember", cmd_remember, "captura una observación")
    remember.add_argument("--kind", help=f"uno de: {', '.join(KINDS)}")
    remember.add_argument("--title")
    remember.add_argument("--body")
    remember.add_argument("--rationale")
    remember.add_argument("--alternatives")
    remember.add_argument("--consequences")
    remember.add_argument("--topic-key", dest="topic_key")
    remember.add_argument("--confidence", help="por defecto confirmed")

    search = add("search", cmd_search, "busca conocimiento persistido")
    search.add_argument("--query")
    search.add_argument("--limit", type=int, default=10)

    checkpoint = add("checkpoint", cmd_checkpoint, "guarda un checkpoint")
    checkpoint.add_argument("--state")
    checkpoint.add_argument("--next-steps", dest="next_steps")
    checkpoint.add_argument("--files")
    checkpoint.add_argument("--open-questions", dest="open_questions")

    close = add("close", cmd_close, "cierra sesión con checkpoint final")
    close.add_argument("--summary")
    close.add_argument("--state")
    close.add_argument("--next-steps", dest="next_steps")
    close.add_argument("--files")
    close.add_argument("--open-questions", dest="open_questions")
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        result = args.handler(args)
    except AcmError as exc:
        print(json.dumps({"ok": False, "error": exc.code,
                          "message": exc.message},
                         ensure_ascii=False, separators=(",", ":")))
        return EXIT_CODES.get(exc.code, 1)
    print(json.dumps(result, ensure_ascii=False, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
