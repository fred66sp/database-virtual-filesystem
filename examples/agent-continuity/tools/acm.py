#!/usr/bin/env python3
"""Adaptador ACM: eventos AIF sobre db-ai.

Contrato (una sola línea JSON compacta por invocación):
  éxito  {"ok":true,...}
  error  {"ok":false,"error":"<código db-ai>","message":"..."}

Eventos AIF: el agente invoca el evento y recibe un formulario con solo lo que
solo él sabe; lo devuelve relleno por `submit --data -` (stdin).
  start [--goal G]                   resuelve directo: abre o restaura la sesión
  decision | learning | discovery | bugfix | preference | recall | checkpoint |
  close [--session ID]               sin datos, devuelve su formulario
  submit [--data JSON|-] [--session ID]  ejecuta un formulario rellenado

Comandos convencionales, ocultos en la ayuda y sin cambios de contrato:
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
    "E_USAGE": 2, "E_VALIDATION": 2, "E_NOT_FOUND": 3, "E_STALE_SESSION": 3,
    "E_DENIED": 4, "E_CONFLICT": 5, "E_CONSTRAINT": 6, "E_BUSY": 7,
    "E_TIMEOUT": 7, "E_UNSUPPORTED": 8, "E_LIMIT": 8, "E_INTERNAL": 9,
    "E_COMMIT_UNKNOWN": 10,
}

INSTRUCTION = (
    "Completa el formulario conservando event, form_id y session; reemplaza "
    "cada value por tu respuesta y devuelve TODO el JSON mediante: "
    "acm submit --data - (por stdin).")
CORRECTION = "corrige los campos indicados y reenvía"
FORM_ID_RE = re.compile(r"[A-Za-z0-9-]{1,64}")

# What each AIF event must ask: only what the agent knows or decides. "start"
# has no form (cmd_start resolves it), and every "allowed" is a closed set.
EVENT_FORMS = {
    "start": {},
    "decision": {
        "topic": {"required": True},
        "title": {"required": True},
        "decision": {"required": True},
        "rationale": {"required": True},
        "alternatives": {"required": False},
        "consequences": {"required": False},
        "confidence": {"required": False, "allowed": CONFIDENCES},
    },
    "learning": {
        "title": {"required": True},
        "body": {"required": True},
        "topic": {"required": False},
        "confidence": {"required": False, "allowed": CONFIDENCES},
    },
    "discovery": {
        "title": {"required": True},
        "body": {"required": True},
        "topic": {"required": False},
        "confidence": {"required": False, "allowed": CONFIDENCES},
    },
    "bugfix": {
        "title": {"required": True},
        "body": {"required": True},
        "rationale": {"required": False},
        "topic": {"required": False},
        "confidence": {"required": False, "allowed": CONFIDENCES},
    },
    "preference": {
        "pref_key": {"required": True},
        "value": {"required": True},
        # Never credit the user with an inferred preference.
        "origin": {"required": True, "allowed": ("human", "agent"),
                   "hint": "human SOLO si el usuario manifestó la preferencia "
                           "expresamente; agent si la inferiste"},
        "rationale": {"required": False},
    },
    "recall": {
        "query": {"required": True,
                  "hint": "todas las palabras deben aparecer en el recuerdo; "
                          "usa palabras clave del dominio, no la pregunta completa"},
        "limit": {"required": False},
    },
    "checkpoint": {
        "state": {"required": True},
        "next_steps": {"required": True},
        "files": {"required": False},
        "open_questions": {"required": False},
    },
    "close": {
        "summary": {"required": True},
    },
}


class AcmError(Exception):
    """Error de contrato: se serializa tal cual y nunca deja traza."""

    def __init__(self, code: str, message: str, payload: dict | None = None):
        super().__init__(message)
        self.code = code
        self.message = message
        self.payload = payload or {}


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


def set_preference(sid: str, pref_key: str, value: str, rationale: str,
                   origin: str) -> dict:
    """Una preferencia activa por clave; UNIQUE(content_hash) arbitra el duplicado."""
    if origin not in ("human", "agent"):
        raise AcmError("E_USAGE",
                       "origin debe ser human (dicho por el usuario) o agent")
    open_session(sid)
    pref_key = require_text(pref_key, "pref_key")
    value = require_text(value, "value")
    rationale = (rationale or "").strip()
    digest = content_hash("preferences", {"pref_key": pref_key, "value": value,
                                          "rationale": rationale})
    found = rows(f"/{ALIAS}/preferences", f"content_hash={digest}", limit=1)
    if found:
        return {"ok": True, "result": "existing",
                "id": fields(found[0])["id"],
                "status": fields(found[0])["status"],
                "content_hash": digest}

    timestamp = now()
    preference_id = uuid7(dt.datetime.now(dt.timezone.utc))
    operations = [{"op": "touch", "path": f"/{ALIAS}/preferences", "data": {
        "id": preference_id, "session_id": sid, "agent_id": AGENT_ID,
        # An "origin" already uses the vocabulary of "source": human or agent.
        "source": origin, "pref_key": pref_key, "value": value,
        "rationale": rationale or None, "status": "active",
        "superseded_by": None, "content_hash": digest,
        "created_at": timestamp, "updated_at": timestamp}}]
    active = rows(f"/{ALIAS}/preferences", f"pref_key={pref_key}",
                  "status=active", order="created_at:desc", limit=1)
    if active:
        operations.append({
            "op": "write",
            "path": f"/{ALIAS}/preferences/{fields(active[0])['id']}",
            "if_match": active[0]["etag"],
            "data": {"status": "superseded", "superseded_by": preference_id,
                     "updated_at": timestamp}})
    try:
        db_ai("batch", "--data", json.dumps(operations, ensure_ascii=False))
    except AcmError as exc:
        if exc.code != "E_CONSTRAINT":
            raise
        # Another writer won the same hash: it is the same content, not a retry.
        found = rows(f"/{ALIAS}/preferences", f"content_hash={digest}", limit=1)
        if not found:
            raise
        return {"ok": True, "result": "existing",
                "id": fields(found[0])["id"], "content_hash": digest}
    return {"ok": True, "result": "created", "id": preference_id,
            "superseded": len(operations) - 1, "content_hash": digest}


# --- AIF: formularios ---------------------------------------------------------

def build_form(event: str, sid: str, form_id: str,
               submitted: dict | None) -> tuple[dict, dict]:
    """Formulario del evento y, si hay datos, los problemas de validación.

    submitted=None emite el formulario en blanco: no es una validación.
    """
    form = {"event": event, "form_id": form_id, "session": sid}
    invalid = {}
    for name, rule in EVENT_FORMS[event].items():
        raw = None if submitted is None else submitted.get(name)
        # The instruction says to replace each "value": echoing the form back
        # with the nested {"required":..,"value":..} objects must also work.
        if isinstance(raw, dict) and "value" in raw:
            raw = raw["value"]
        if name == "limit" and isinstance(raw, int) and not isinstance(raw, bool):
            raw = str(raw)
        text = raw.strip() if isinstance(raw, str) else ""
        if submitted is not None:
            if raw is not None and not isinstance(raw, str):
                invalid[name] = "debe ser texto"
            elif rule["required"] and not text:
                invalid[name] = "obligatorio"
            elif text and "allowed" in rule and text not in rule["allowed"]:
                invalid[name] = ("valores permitidos: "
                                 + ", ".join(rule["allowed"]))
        field = {"required": rule["required"], "value": text}
        if "allowed" in rule:
            field["allowed"] = list(rule["allowed"])
        if "hint" in rule:
            field["hint"] = rule["hint"]
        form[name] = field
    return form, invalid


def forms_dir() -> Path:
    """Marcadores de reenvío junto a la base: /meta es de solo lectura."""
    for mount in db_ai("mounts")["mounts"]:
        if mount["alias"] == ALIAS:
            return Path(mount["file"]).parent / "acm-forms"
    raise AcmError("E_INTERNAL", f"el montaje '{ALIAS}' no está en db-ai mounts")


def submitted_id(form_id: str) -> str | None:
    try:
        return (forms_dir() / form_id).read_text(encoding="utf-8").strip() or None
    except FileNotFoundError:
        return None


def mark_submitted(form_id: str, record: str) -> None:
    """Reclama el form_id; quien pierda no re-captura, ya hay un id registrado.

    Accepted race: two concurrent resends can both capture once; the loser
    consolidates on content_hash, so the row is unique either way.
    shortcut: one marker file per form and no pruning; each is a few bytes.
    """
    directory = forms_dir()
    directory.mkdir(parents=True, exist_ok=True)
    try:
        with (directory / form_id).open("x", encoding="utf-8") as handle:
            handle.write(record)
    except FileExistsError:
        pass


def reject(invalid: dict, form: dict | None = None) -> AcmError:
    payload = {"invalid": invalid}
    if form is not None:
        payload["form"] = form
    payload["instruction"] = CORRECTION
    return AcmError("E_VALIDATION", "el formulario tiene campos inválidos",
                    payload)


def submit_event(event: str, sid: str, values: dict) -> dict:
    """Despacha al comando ya validado: la persistencia no se duplica."""
    if event == "preference":
        return set_preference(sid, values["pref_key"], values["value"],
                              values["rationale"], values["origin"])
    if event == "recall":
        limit = int(values["limit"]) if values["limit"] else 10
        return cmd_search(argparse.Namespace(session=sid,
                                             query=values["query"],
                                             limit=limit))
    if event == "checkpoint":
        return cmd_checkpoint(argparse.Namespace(
            session=sid, state=values["state"],
            next_steps=values["next_steps"], files=values["files"],
            open_questions=values["open_questions"]))
    if event == "close":
        return cmd_close(argparse.Namespace(
            session=sid, summary=values["summary"], state=None,
            next_steps=None, files=None, open_questions=None))
    return cmd_remember(argparse.Namespace(
        session=sid, kind=event, title=values["title"],
        body=values["decision"] if event == "decision" else values["body"],
        rationale=values.get("rationale", ""),
        alternatives=values.get("alternatives", ""),
        consequences=values.get("consequences", ""),
        topic_key=values.get("topic", ""),
        confidence=values.get("confidence") or None))


def cmd_event(args: argparse.Namespace) -> dict:
    """Evento sin datos: entrega el formulario que el agente debe rellenar."""
    sid = session_id(args)
    form, _ = build_form(args.command, sid,
                         uuid7(dt.datetime.now(dt.timezone.utc)), None)
    return {"ok": True, "result": "form", "form": form,
            "instruction": INSTRUCTION}


def cmd_submit(args: argparse.Namespace) -> dict:
    sid = session_id(args)
    raw = sys.stdin.read() if args.data == "-" else args.data
    try:
        data = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise reject({"data": f"JSON inválido: {exc.msg}"}) from exc
    if not isinstance(data, dict):
        raise reject({"data": "se esperaba un objeto JSON"})
    event = data.get("event")
    if event == "start":
        raise reject({"event": "start se resuelve con 'acm start', no con submit"})
    if event not in EVENT_FORMS:
        valid = ", ".join(name for name in EVENT_FORMS if name != "start")
        raise reject({"event": f"evento desconocido; válidos: {valid}"})
    # form_id names the idempotency marker file, so it is constrained here.
    form_id = data.get("form_id")
    if not (isinstance(form_id, str) and FORM_ID_RE.fullmatch(form_id)):
        form_id = uuid7(dt.datetime.now(dt.timezone.utc))
    form, invalid = build_form(event, sid, form_id, data)
    if data.get("session") != sid:
        invalid["session"] = f"debe coincidir con la sesión efectiva '{sid}'"
    if event == "recall":
        limit = form["limit"]["value"]
        if limit and not (limit.isdigit() and 1 <= int(limit) <= 100):
            invalid["limit"] = "entero entre 1 y 100"
    if invalid:
        raise reject(invalid, form)

    previous = submitted_id(form_id)
    if previous:
        return {"ok": True, "result": "already_submitted", "id": previous}
    values = {name: form[name]["value"] for name in EVENT_FORMS[event]}
    result = submit_event(event, sid, values)
    record = result.get("id") or result.get("checkpoint")
    if record:
        mark_submitted(form_id, record)
    return result


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


def cmd_checkpoint_or_form(args: argparse.Namespace) -> dict:
    """Sin datos convencionales es el evento AIF; con ellos, el comando de siempre."""
    if any((args.state, args.next_steps, args.files, args.open_questions)):
        return cmd_checkpoint(args)
    return cmd_event(args)


def cmd_close_or_form(args: argparse.Namespace) -> dict:
    if any((args.summary, args.state, args.next_steps, args.files,
            args.open_questions)):
        return cmd_close(args)
    return cmd_event(args)


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
        description="Adaptador ACM sobre db-ai (eventos AIF, JSON compacto).")
    sub = parser.add_subparsers(dest="command", required=True,
                                 metavar="COMANDO")

    def add(name: str, handler, help_text: str | None):
        # No help means absent from the help table: conventional commands stay.
        cmd = (sub.add_parser(name) if help_text is None
               else sub.add_parser(name, help=help_text))
        cmd.add_argument("--session", help="identificador de sesión explícito")
        cmd.set_defaults(handler=handler)
        return cmd

    start = add("start", cmd_start, "abre o restaura una sesión")
    start.add_argument("--goal", help="objetivo de la sesión")

    for name in EVENT_FORMS:
        if name not in ("start", "checkpoint", "close"):
            add(name, cmd_event, "evento AIF: devuelve su formulario")

    submit = add("submit", cmd_submit, "ejecuta un formulario AIF rellenado")
    submit.add_argument("--data", default="-",
                        help="JSON del formulario; '-' lo lee de stdin")

    checkpoint = add("checkpoint", cmd_checkpoint_or_form,
                     "evento AIF: checkpoint de reanudación")
    checkpoint.add_argument("--state")
    checkpoint.add_argument("--next-steps", dest="next_steps")
    checkpoint.add_argument("--files")
    checkpoint.add_argument("--open-questions", dest="open_questions")

    close = add("close", cmd_close_or_form, "evento AIF: cierra la sesión")
    close.add_argument("--summary")
    close.add_argument("--state")
    close.add_argument("--next-steps", dest="next_steps")
    close.add_argument("--files")
    close.add_argument("--open-questions", dest="open_questions")

    remember = add("remember", cmd_remember, None)
    remember.add_argument("--kind", help=f"uno de: {', '.join(KINDS)}")
    remember.add_argument("--title")
    remember.add_argument("--body")
    remember.add_argument("--rationale")
    remember.add_argument("--alternatives")
    remember.add_argument("--consequences")
    remember.add_argument("--topic-key", dest="topic_key")
    remember.add_argument("--confidence", help="por defecto confirmed")

    search = add("search", cmd_search, None)
    search.add_argument("--query")
    search.add_argument("--limit", type=int, default=10)
    return parser


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        result = args.handler(args)
    except AcmError as exc:
        print(json.dumps({"ok": False, "error": exc.code,
                          "message": exc.message, **exc.payload},
                         ensure_ascii=False, separators=(",", ":")))
        return EXIT_CODES.get(exc.code, 1)
    print(json.dumps(result, ensure_ascii=False, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
