#!/usr/bin/env python3
"""Adaptador ACM: eventos AIF sobre db-ai.

Contrato (una sola línea JSON compacta por invocación):
  éxito  {"ok":true,...}
  error  {"ok":false,"error":"<código db-ai>","message":"..."}

Un comando por evento: los datos van como flags y el evento se resuelve en la
misma invocación. Solo si falta un campo obligatorio devuelve el formulario AIF
para rellenarlo por `submit` (stdin). `start` responde en Markdown compacto con
el contexto de continuidad; el resto de eventos, en JSON.
  start [--goal G]                    abre la sesión; restaura con su contexto
  decision --topic T --title T --decision D --rationale R [...]
  learning | discovery --title T --body B [--topic K] [--confidence C]
  bugfix --title T --body B [--rationale R] [--topic K] [--confidence C]
  preference --pref-key K --value V --origin human|agent [--rationale R]
  recall --query Q [--limit N]        busca recuerdos activos
  checkpoint --state S --next-steps N [--files F] [--open-questions Q]
  close --summary S [...]             cierra con su checkpoint final
  submit [--data JSON|-]              ejecuta un formulario AIF rellenado

Comandos convencionales, ocultos en la ayuda y sin cambios de contrato:
  remember --kind K --title T --body B [...]   captura directa; `pattern` y
               `architecture` solo son capturables por esta vía
  search --query T [--limit N]                 búsqueda directa

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

# Continuity sections: complete memories each, no mid-entry cuts.
BLOCK = 5
RECALL_FIELDS = ("id", "kind", "title", "body", "topic_key", "session_id",
                 "created_at")

# Function words dropped only when the exact recall finds nothing.
STOPWORDS = frozenset("""
de la el los las un una unos unas que y o en con por para del al es son se su
sus lo le les no mas pero si como este esta estos estas cuando donde quien cual
sobre entre hasta desde sin ya muy tambien hay ser fue han ha
the a an of to in on for and or is are be with how what when where why this
that these those it its as at by from not do does did can could should would
will we you i my our your
""".split())

INSTRUCTION = (
    "Completa los campos que faltan conservando event, form_id y session; "
    "devuelve TODO el JSON por `acm submit --data -` (stdin).")
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
                  "hint": "palabras clave del dominio, no la pregunta completa"},
        "limit": {"required": False, "range": (1, 100),
                  "hint": "entero entre 1 y 100; por defecto 10"},
    },
    "checkpoint": {
        "state": {"required": True},
        "next_steps": {"required": True},
        "files": {"required": False},
        "open_questions": {"required": False},
    },
    "close": {
        "summary": {"required": True},
        "state": {"required": False,
                  "hint": "estado al cerrar; por defecto, el summary"},
        "next_steps": {"required": False,
                       "hint": "siguiente paso; por defecto, ninguno"},
        "files": {"required": False},
        "open_questions": {"required": False},
    },
}

EVENT_HELP = {
    "decision": "registra una decisión con su motivo",
    "learning": "registra un aprendizaje reutilizable",
    "discovery": "registra un hallazgo",
    "bugfix": "registra un defecto resuelto",
    "preference": "registra una preferencia operativa",
    "recall": "busca recuerdos activos por palabras clave",
    "checkpoint": "guarda un checkpoint de reanudación",
    "close": "cierra la sesión con su checkpoint final",
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


def new_id() -> str:
    return uuid7(dt.datetime.now(dt.timezone.utc))


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
                       "el identificador de sesión solo admite "
                       "[A-Za-z0-9._-]; p. ej. --session tarea-parser")
    return sid


def load_session(sid: str) -> tuple[dict, str]:
    found = rows(f"/{ALIAS}/sessions", f"id={sid}", limit=1)
    if not found:
        raise AcmError("E_NOT_FOUND",
                       f"la sesión '{sid}' no existe; ábrela con "
                       f"`acm start --session {sid}`")
    return fields(found[0]), found[0]["etag"]


def open_session(sid: str) -> tuple[dict, str]:
    session, etag = load_session(sid)
    if session["status"] != "open":
        raise AcmError("E_CONSTRAINT",
                       f"la sesión '{sid}' está {session['status']}, no abierta; "
                       "usa otro identificador con `acm start`")
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


def checkpoint_md(title: str, checkpoint: dict) -> str:
    """Un checkpoint completo en Markdown: campos vacíos fuera, nada cortado."""
    lines = [f"## {title}\n"]
    state = (checkpoint.get("state") or "").strip()
    if state:
        lines.append(f"Estado: {state}\n")
    steps = (checkpoint.get("next_steps") or "").strip()
    if steps and steps != "ninguno":
        lines.append(f"Siguientes pasos: {steps}\n")
    files = (checkpoint.get("files") or "").strip()
    if files:
        lines.append(f"Ficheros: {files}\n")
    questions = (checkpoint.get("open_questions") or "").strip()
    if questions:
        lines.append(f"Preguntas abiertas: {questions}\n")
    return "\n".join(lines).rstrip()


def recall_view(observation: dict) -> dict:
    return {key: observation.get(key) for key in RECALL_FIELDS}


def confirm(result: dict) -> dict:
    """Respuesta mínima de escritura: el agente solo necesita saber si se hizo.

    Los detalles de persistencia (id, content_hash, duplicate_count) quedan
    dentro de la herramienta; la deduplicación y el marcador de idempotencia
    se resuelven igual antes de recortar.
    """
    return {"ok": True, "result": result.get("result")}


def continuity() -> str:
    """Contexto de continuidad: Markdown compacto con recuerdos completos."""
    sections = []

    closed = rows(f"/{ALIAS}/sessions", "status=closed",
                  order="started_at:desc,id:desc", limit=1)
    if closed:
        session = fields(closed[0])
        summary = (session.get("summary") or session.get("goal") or "").strip()
        final = rows(f"/{ALIAS}/checkpoints", f"session_id={session['id']}",
                     order="created_at:desc,id:desc", limit=1)
        if final:
            checkpoint = fields(final[0])
            lines = [f"## Sesión anterior\n\n{summary}"]
            state = (checkpoint.get("state") or "").strip()
            if state and state != summary:
                lines.append(f"Estado: {state}")
            steps = (checkpoint.get("next_steps") or "").strip()
            if steps and steps != "ninguno":
                lines.append(f"Siguientes pasos: {steps}")
            files = (checkpoint.get("files") or "").strip()
            if files:
                lines.append(f"Ficheros: {files}")
            questions = (checkpoint.get("open_questions") or "").strip()
            if questions:
                lines.append(f"Preguntas abiertas: {questions}")
            sections.append("\n\n".join(lines))

    def block(title: str, *wheres: str) -> None:
        found = rows(f"/{ALIAS}/observations", "status=active", *wheres,
                     order="created_at:desc,id:desc", limit=BLOCK + 1)
        if not found:
            return
        lines = []
        for row in found[:BLOCK]:
            observation = fields(row)
            entry = f"- **({observation.get('kind')}) {observation.get('title')}**"
            topic = (observation.get("topic_key") or "").strip()
            if topic:
                entry += f" ({topic})"
            body = (observation.get("body") or "").strip()
            if body and body != observation.get("title"):
                entry += f": {body}"
            lines.append(entry)
        if len(found) > BLOCK:
            lines.append("- _hay más_")
        sections.append(f"## {title}\n\n" + "\n".join(lines))

    block("Decisiones vigentes", "kind=decision")
    block("Conocimiento reciente", "kind!=decision")

    preferences = rows(f"/{ALIAS}/preferences", "status=active",
                       order="created_at:desc,id:desc", limit=BLOCK + 1)
    if preferences:
        lines = []
        for row in preferences[:BLOCK]:
            preference = fields(row)
            key = (preference.get("pref_key") or "").strip()
            origin = (preference.get("source") or "").strip()
            value = (preference.get("value") or "").strip()
            item = f"- **{key}**"
            if origin == "human":
                item += " (del usuario)"
            if value:
                item += f": {value}"
            lines.append(item)
        if len(preferences) > BLOCK:
            lines.append("- _hay más_")
        sections.append("## Preferencias activas\n\n" + "\n".join(lines))

    return "\n\n".join(sections)


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
    pref_key = require_text(pref_key, "--pref-key")
    value = require_text(value, "--value")
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
    preference_id = new_id()
    operations = [{"op": "touch", "path": f"/{ALIAS}/preferences", "data": {
        "id": preference_id, "session_id": sid, "agent_id": AGENT_ID,
        # An "origin" already uses the vocabulary of "source": human or agent.
        "source": origin, "pref_key": pref_key, "value": value,
        "rationale": rationale or None, "status": "active",
        "superseded_by": None, "content_hash": digest,
        "created_at": timestamp, "updated_at": timestamp}}]
    active = rows(f"/{ALIAS}/preferences", f"pref_key={pref_key}",
                  "status=active", order="created_at:desc,id:desc", limit=1)
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
            elif text and "range" in rule:
                low, high = rule["range"]
                if not (text.isdigit() and low <= int(text) <= high):
                    invalid[name] = f"entero entre {low} y {high}"
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
    raise AcmError("E_INTERNAL",
                   f"el montaje '{ALIAS}' no está en db-ai mounts; "
                   "ejecuta init.sh en la carpeta del ejemplo")


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


def resolve_event(event: str, sid: str, values: dict) -> dict:
    """Resuelve con los flags; si faltan obligatorios, entrega el formulario."""
    form, invalid = build_form(event, sid, new_id(), values or None)
    if any(reason != "obligatorio" for reason in invalid.values()):
        raise reject(invalid, form)
    if not values or invalid:
        return {"ok": True, "result": "form", "form": form,
                "instruction": INSTRUCTION}
    return confirm(submit_event(event, sid,
                                {name: form[name]["value"]
                                 for name in EVENT_FORMS[event]}))


def submit_event(event: str, sid: str, values: dict) -> dict:
    """Despacha al comando ya validado: la persistencia no se duplica."""
    if event == "preference":
        return set_preference(sid, values["pref_key"], values["value"],
                              values.get("rationale", ""), values["origin"])
    if event == "recall":
        return recall(sid, values["query"],
                      int(values["limit"]) if values.get("limit") else 10)
    if event == "checkpoint":
        return write_checkpoint(sid, values["state"], values["next_steps"],
                                values.get("files", ""),
                                values.get("open_questions", ""))
    if event == "close":
        return close_session(sid, values["summary"], values.get("state"),
                             values.get("next_steps"), values.get("files"),
                             values.get("open_questions"))
    return record_observation(sid, event, values)


def cmd_event(args: argparse.Namespace) -> dict:
    """Datos por flags; sin los obligatorios, devuelve el formulario."""
    sid = session_id(args)
    values = {}
    for name in EVENT_FORMS[args.event]:
        value = getattr(args, name, None)
        if value is not None:
            values[name] = value
    return resolve_event(args.event, sid, values)


def cmd_remember(args: argparse.Namespace) -> dict:
    """Captura convencional: única vía de `pattern` y `architecture`."""
    kind = require_text(args.kind, "--kind")
    if kind not in KINDS:
        raise AcmError("E_USAGE", f"--kind debe ser uno de: {', '.join(KINDS)}")
    body = require_text(args.body, "--body")
    values = {"body": body, "title": args.title or "",
              "rationale": (args.rationale or "").strip(),
              "alternatives": (args.alternatives or "").strip(),
              "consequences": (args.consequences or "").strip(),
              "topic": (args.topic_key or "").strip(),
              "confidence": args.confidence}
    if kind == "decision":
        values["decision"] = values.pop("body")
        if not (values["topic"] and values["rationale"]):
            raise AcmError("E_USAGE",
                           "una decisión exige --topic-key y --rationale")
    return confirm(record_observation(session_id(args), kind, values))


def cmd_search(args: argparse.Namespace) -> dict:
    """Búsqueda convencional: mismos términos y relajación que `recall`."""
    return recall(session_id(args), (args.query or "").strip(), args.limit)


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
        form_id = new_id()
    form, invalid = build_form(event, sid, form_id, data)
    if data.get("session") != sid:
        invalid["session"] = (f"debe coincidir con la sesión efectiva '{sid}'; "
                              f"añade --session {sid} al comando")
    if invalid:
        raise reject(invalid, form)

    previous = submitted_id(form_id)
    if previous:
        return {"ok": True, "result": "already_submitted"}
    values = {name: form[name]["value"] for name in EVENT_FORMS[event]}
    result = submit_event(event, sid, values)
    record = result.get("id") or result.get("checkpoint")
    if record:
        mark_submitted(form_id, record)
    return confirm(result)


# --- comandos ----------------------------------------------------------------

def cmd_start(args: argparse.Namespace) -> str:
    """Markdown compacto: cabecera, checkpoint activo y contexto de continuidad."""
    sid = session_id(args)
    found = rows(f"/{ALIAS}/sessions", f"id={sid}", limit=1)
    if found:
        session = fields(found[0])
        if session["status"] != "open":
            raise AcmError(
                "E_CONSTRAINT",
                f"la sesión '{sid}' está {session['status']}; usa otro identificador")
        stored_goal = (session.get("goal") or "").strip()
        parts = ["Sesión restaurada."]
        if stored_goal and stored_goal != f"sesión {sid}":
            parts.append(f"Objetivo: {stored_goal}")
        active = rows(f"/{ALIAS}/checkpoints", f"session_id={sid}",
                      "status=active", order="created_at:desc,id:desc", limit=1)
        if active:
            parts.append(checkpoint_md("Estado de esta sesión",
                                       fields(active[0])))
        context = continuity()
        if context:
            parts.append(context)
        return "\n\n".join(parts)

    goal = (args.goal or f"sesión {sid}").strip()
    timestamp = now()
    session = {
        "id": sid, "agent_id": AGENT_ID, "source": "agent", "goal": goal,
        "status": "open", "started_at": timestamp, "ended_at": None,
        "summary": None, "created_at": timestamp, "updated_at": timestamp,
    }
    db_ai("touch", f"/{ALIAS}/sessions",
          "--data", json.dumps(session, ensure_ascii=False))
    parts = ["Sesión iniciada."]
    if goal != f"sesión {sid}":
        parts.append(f"Objetivo: {goal}")
    context = continuity()
    if context:
        parts.append(context)
    return "\n\n".join(parts)


def record_observation(sid: str, kind: str, values: dict) -> dict:
    open_session(sid)
    title = require_text(values.get("title"), "--title")
    body_name = "decision" if kind == "decision" else "body"
    body = require_text(values.get(body_name), f"--{body_name}")
    content = {
        "kind": kind, "title": title, "body": body,
        "rationale": (values.get("rationale") or "").strip(),
        "alternatives": (values.get("alternatives") or "").strip(),
        "consequences": (values.get("consequences") or "").strip(),
        "topic_key": (values.get("topic") or "").strip(),
    }
    digest = content_hash("observations", content)
    timestamp = now()
    found = rows(f"/{ALIAS}/observations", f"content_hash={digest}", limit=1)
    if found:
        return {"ok": True, **consolidate(fields(found[0])["id"], found[0],
                                          timestamp), "content_hash": digest}

    observation = {
        **content, "id": new_id(),
        "session_id": sid, "agent_id": AGENT_ID, "source": "agent",
        "confidence": values.get("confidence") or "confirmed",
        "status": "active", "duplicate_count": 1,
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
    checkpoint_id = new_id()
    operations = [{"op": "touch", "path": f"/{ALIAS}/checkpoints", "data": {
        "id": checkpoint_id, "session_id": sid, "agent_id": AGENT_ID,
        "source": "agent", "goal": goal, "state": state,
        "next_steps": next_steps, "files": files or None,
        "open_questions": open_questions or None, "status": "active",
        "content_hash": digest, "created_at": timestamp,
        "updated_at": timestamp}}]
    active = rows(f"/{ALIAS}/checkpoints", f"session_id={sid}",
                  "status=active", order="created_at:desc,id:desc", limit=1)
    if active:
        operations.append({
            "op": "write",
            "path": f"/{ALIAS}/checkpoints/{fields(active[0])['id']}",
            "if_match": active[0]["etag"],
            "data": {"status": "superseded", "superseded_by": checkpoint_id,
                     "updated_at": timestamp}})
    return operations, checkpoint_id


def write_checkpoint(sid: str, state: str, next_steps: str, files: str = "",
                     open_questions: str = "") -> dict:
    session, _ = open_session(sid)
    state = require_text(state, "--state")
    next_steps = require_text(next_steps, "--next-steps")
    operations, checkpoint_id = checkpoint_batch(
        sid, session["goal"], state, next_steps,
        (files or "").strip(), (open_questions or "").strip())
    if operations is None:
        return {"ok": True, "result": "existing", "checkpoint": checkpoint_id}
    db_ai("batch", "--data", json.dumps(operations, ensure_ascii=False))
    return {"ok": True, "result": "created", "checkpoint": checkpoint_id,
            "superseded": len(operations) - 1}


def close_session(sid: str, summary: str, state: str | None = None,
                  next_steps: str | None = None, files: str | None = None,
                  open_questions: str | None = None) -> dict:
    session, etag = open_session(sid)
    summary = require_text(summary, "--summary")
    operations, checkpoint_id = checkpoint_batch(
        sid, session["goal"], (state or summary).strip(),
        (next_steps or "ninguno").strip(),
        (files or "").strip(), (open_questions or "").strip())
    operations = operations or []
    timestamp = now()
    operations.append({
        "op": "write", "path": f"/{ALIAS}/sessions/{sid}", "if_match": etag,
        "data": {"status": "closed", "ended_at": timestamp,
                 "summary": summary, "updated_at": timestamp}})
    db_ai("batch", "--data", json.dumps(operations, ensure_ascii=False))
    return {"ok": True, "result": "closed", "session": sid,
            "checkpoint": checkpoint_id, "ended_at": timestamp}


def recall(sid: str, query: str, limit: int) -> dict:
    load_session(sid)
    terms = re.findall(r"\w+", normalize(query))
    if not terms:
        raise AcmError("E_USAGE",
                       "la consulta no tiene palabras clave; usa términos del "
                       "dominio (p. ej. 'parser fechas')")
    limit = max(1, min(limit, 100))
    # status=active va en el filtro, antes del límite; 1000 es el máximo de --limit.
    # shortcut: no hay paginación, más de 1000 observaciones activas se truncan.
    candidates = []
    for row in rows(f"/{ALIAS}/observations", "status=active", limit=1000):
        observation = fields(row)
        haystack = normalize(" ".join(str(observation.get(key) or "") for key in
                                      ("title", "body", "topic_key")))
        candidates.append((observation, haystack))

    def hits(stage: list[str]) -> list[dict]:
        results = []
        for observation, haystack in candidates:
            if all(term in haystack for term in stage):
                results.append(recall_view(observation))
                if len(results) == limit:
                    break
        return results

    significant = [term for term in terms
                   if len(term) > 2 and term not in STOPWORDS]
    stages = ([terms] if not significant or significant == terms
              else [terms, significant])
    for index, stage in enumerate(stages):
        results = hits(stage)
        if results:
            payload = {"ok": True, "query": query, "count": len(results),
                       "results": results}
            if index:
                payload["relaxed"] = True
            return payload

    # Relajación por palabras más significativas: con tres o más términos, basta
    # con que fallen uno; se ordena por número de coincidencias.
    if len(significant) >= 3:
        threshold = max(2, len(significant) - 1)
        ranked = []
        for observation, haystack in candidates:
            matched = sum(term in haystack for term in significant)
            if matched >= threshold:
                ranked.append((matched, observation))
        if ranked:
            ranked.sort(key=lambda pair: -pair[0])
            results = [recall_view(observation)
                       for _, observation in ranked[:limit]]
            return {"ok": True, "query": query, "count": len(results),
                    "results": results, "relaxed": True}
    return {"ok": True, "query": query, "count": 0, "results": [],
            "message": "sin resultados; prueba otras palabras clave"}


# --- entrada -----------------------------------------------------------------

def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Adaptador ACM sobre db-ai (un comando por evento, JSON "
                    "compacto).")
    sub = parser.add_subparsers(dest="command", required=True,
                                 metavar="COMANDO")

    def add(name: str, handler, help_text: str | None) -> argparse.ArgumentParser:
        # No help means absent from the help table: conventional commands stay.
        cmd = (sub.add_parser(name) if help_text is None
               else sub.add_parser(name, help=help_text))
        cmd.add_argument("--session",
                         help="identificador de sesión (o OPENCODE_SESSION_ID)")
        cmd.set_defaults(handler=handler)
        return cmd

    start = add("start", cmd_start,
                "abre la sesión y devuelve el contexto para continuar")
    start.add_argument("--goal", help="objetivo de la sesión")

    for event, rules in EVENT_FORMS.items():
        if event == "start":
            continue
        event_parser = add(event, cmd_event, EVENT_HELP[event])
        event_parser.set_defaults(event=event)
        for name, rule in rules.items():
            event_parser.add_argument(
                "--" + name.replace("_", "-"), dest=name,
                help=rule.get("hint") or ("obligatorio"
                                          if rule["required"] else None))

    submit = add("submit", cmd_submit, "ejecuta un formulario AIF rellenado")
    submit.add_argument("--data", default="-",
                        help="JSON del formulario; '-' lo lee de stdin")

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
    if isinstance(result, str):
        print(result)
    else:
        print(json.dumps(result, ensure_ascii=False, separators=(",", ":")))
    return 0


if __name__ == "__main__":
    sys.exit(main())
