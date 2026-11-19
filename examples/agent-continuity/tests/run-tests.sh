#!/usr/bin/env bash
# Pruebas ejecutables del ejemplo ACM sobre una base temporal.
# No tocan .agent-memory/ real ni la configuración real de db-ai.
# Dependencias: bash, sqlite3, db-ai, coreutils.

set -uo pipefail

tests_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P) || exit 1
example_dir=$(cd -- "$tests_dir/.." && pwd -P) || exit 1
init="$example_dir/init.sh"

tmp=$(mktemp -d) || { echo "no se pudo crear el directorio temporal" >&2; exit 1; }
trap 'rm -rf "$tmp"' EXIT

export XDG_CONFIG_HOME="$tmp/config"
export XDG_STATE_HOME="$tmp/state"
export ACM_HOME="$tmp/proyecto"
export ACM_ALIAS="mem-test"

db="$ACM_HOME/.agent-memory/memory.db"
alias="/$ACM_ALIAS"

passed=0
failed=0

ok() { printf 'ok     %s\n' "$1"; passed=$((passed + 1)); }
ko() { printf 'FALLO  %s\n       %s\n' "$1" "$2"; failed=$((failed + 1)); }

# Imprime el valor del primer error.code o cadena vacía.
error_code() { printf '%s' "$1" | sed -n 's/.*"code":"\([^"]*\)".*/\1/p'; }
# Imprime el etag de una respuesta de db-ai.
etag_of() { printf '%s' "$1" | sed -n 's/.*"etag":"\([^"]*\)".*/\1/p'; }
# Imprime el data de un campo leído con cat FIELD.
field_of() {
  printf '%s' "$1" | sed -n 's/.*"data":"\([^"]*\)".*/\1/p' | head -n 1
}
# Cuenta filas por tabla en una sola línea "meta/sessions/observations/checkpoints/preferences".
counts() {
  sqlite3 "$db" "SELECT (SELECT count(*) FROM meta)||'/'||
                        (SELECT count(*) FROM sessions)||'/'||
                        (SELECT count(*) FROM observations)||'/'||
                        (SELECT count(*) FROM checkpoints)||'/'||
                        (SELECT count(*) FROM preferences);"
}

# --- Caso 1: init sobre base nueva (memoria limpia, sin --demo) -------------
out=$(bash "$init" 2>&1); rc=$?
if [ "$rc" -eq 0 ] && [ -f "$db" ]; then
  ok "init crea la base y sale 0"
else
  ko "init crea la base y sale 0" "rc=$rc salida: $out"
fi

inicial=$(counts)
meta_rows=$(sqlite3 "$db" "SELECT count(*) FROM meta;")
pname=$(sqlite3 "$db" "SELECT value FROM meta WHERE name='project_name';")
pkey=$(sqlite3 "$db" "SELECT value FROM meta WHERE name='project_key';")
created=$(sqlite3 "$db" "SELECT value FROM meta WHERE name='created_at';")
if [ "$inicial" = "4/0/0/0/0" ] && [ "$meta_rows" = "4" ] \
  && [ "$pname" = "proyecto" ] \
  && printf '%s' "$pkey" | grep -Eq '^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$' \
  && printf '%s' "$created" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$'; then
  ok "init por defecto: 0 filas de contenido y 4 meta reales ($inicial, $pkey)"
else
  ko "init por defecto: memoria limpia y meta reales" "filas=$inicial meta=$meta_rows nombre=$pname clave=$pkey creada=$created"
fi

out=$(bash "$init" 2>&1); rc=$?
repetido=$(counts)
pkey2=$(sqlite3 "$db" "SELECT value FROM meta WHERE name='project_key';")
if [ "$rc" -eq 0 ] && [ "$repetido" = "$inicial" ] && [ "$pkey2" = "$pkey" ]; then
  ok "dos init limpios seguidos dejan el mismo estado ($repetido)"
else
  ko "dos init limpios seguidos dejan el mismo estado" "rc=$rc antes=$inicial despues=$repetido clave_antes=$pkey clave_despues=$pkey2 salida: $out"
fi

# --- Caso 1b: --demo incorpora el fixture ----------------------------------
rm -rf "$ACM_HOME/.agent-memory"
out=$(bash "$init" --demo 2>&1); rc=$?
con_demo=$(counts)
if [ "$rc" -eq 0 ] && [ "$con_demo" = "4/2/6/1/3" ]; then
  ok "--demo aplica el fixture completo ($con_demo)"
else
  ko "--demo aplica el fixture completo" "rc=$rc filas=$con_demo salida: $out"
fi

# --- Caso 2: lectura con db-ai ---------------------------------------------
out=$(db-ai mounts 2>&1)
if printf '%s' "$out" | grep -Fq "\"alias\":\"$ACM_ALIAS\"" && printf '%s' "$out" | grep -Fq '"ok":true'; then
  ok "db-ai mounts muestra el alias $ACM_ALIAS"
else
  ko "db-ai mounts muestra el alias $ACM_ALIAS" "$out"
fi

out=$(db-ai ls "$alias" 2>&1)
if printf '%s' "$out" | grep -Fq '"name":"observations"' && printf '%s' "$out" | grep -Fq '"ok":true'; then
  ok "db-ai ls lista las tablas"
else
  ko "db-ai ls lista las tablas" "$out"
fi

out=$(db-ai find "$alias/meta" --where 'name=project_key' --limit 1 2>&1)
if printf '%s' "$out" | grep -Fq '"ok":true' && printf '%s' "$out" | grep -Fq '"path":"/mem-test/meta/'; then
  ok "db-ai find recupera project_key de meta"
else
  ko "db-ai find recupera project_key de meta" "$out"
fi

# --- Caso 3: el seed deja active, superseded y needs_review ----------------
statuses=$(sqlite3 "$db" "SELECT group_concat(status||':'||n, ',') FROM
  (SELECT status, count(*) AS n FROM observations GROUP BY status ORDER BY status);")
case "$statuses" in
  *active:*superseded:*needs_review:* | *active:*needs_review:*superseded:* | \
  *superseded:*active:*needs_review:* | *superseded:*needs_review:*active:* | \
  *needs_review:*active:*superseded:* | *needs_review:*superseded:*active:*)
    ok "seed: observations tiene active, superseded y needs_review ($statuses)" ;;
  *) ko "seed: observations tiene active, superseded y needs_review" "$statuses" ;;
esac

decisiones=$(sqlite3 "$db" "SELECT count(*) FROM observations WHERE kind='decision' AND status='active' AND topic_key='storage/db-per-project';")
superseded_by=$(sqlite3 "$db" "SELECT superseded_by FROM observations WHERE topic_key='storage/db-per-session';")
active_target=$(sqlite3 "$db" "SELECT status FROM observations WHERE id='$superseded_by';")
if [ "$decisiones" = "1" ] && [ "$active_target" = "active" ]; then
  ok "seed: una decisión activa por tema y superseded_by apunta a la activa"
else
  ko "seed: decisión activa y cadena de sustitución" "activas=$decisiones destino=$active_target"
fi

prefs=$(sqlite3 "$db" "SELECT count(*) FROM preferences WHERE status='active';")
if [ "$prefs" = "3" ]; then
  ok "seed: tres preferencias activas"
else
  ko "seed: tres preferencias activas" "encontradas=$prefs"
fi

# --- Caso 4: escritura con ETag y relectura --------------------------------
row="$alias/observations/01a1f42d-ba80-76a7-8b8c-9d0e1f2a3bac"
out=$(db-ai cat "$row" 2>&1)
etag=$(etag_of "$out")
if [ -n "$etag" ]; then
  out=$(db-ai write "$row" --data '{"status":{"kind":"text","data":"needs_review"}}' --if-match "$etag" 2>&1); rc=$?
  out2=$(db-ai cat "$row/status" 2>&1)
  value=$(field_of "$out2")
  if [ "$rc" -eq 0 ] && [ "$value" = "needs_review" ]; then
    ok "write con ETag actualiza y la relectura muestra needs_review"
  else
    ko "write con ETag actualiza y la relectura muestra needs_review" "rc=$rc valor='$value' salida: $out"
  fi
else
  ko "write con ETag actualiza y la relectura muestra needs_review" "sin etag: $out"
fi

# --- Caso 5: conflicto de ETag ---------------------------------------------
out=$(db-ai write "$row" --data '{"status":{"kind":"text","data":"obsolete"}}' --if-match "$etag" 2>&1); rc=$?
code=$(error_code "$out")
if [ "$rc" -eq 5 ] && [ "$code" = "E_CONFLICT" ]; then
  ok "write con ETag obsoleto devuelve E_CONFLICT"
else
  ko "write con ETag obsoleto devuelve E_CONFLICT" "rc=$rc code=$code salida: $out"
fi

# --- Caso 6: deduplicación exacta ------------------------------------------
hash="sha256:$(printf '%s' '["discovery","Duplicado exacto de prueba","Mismo contenido y mismo hash.","","","",""]' | sha256sum | cut -d' ' -f1)"
new_obs() {
  db-ai touch "$alias/observations" --data "{
    \"id\":{\"kind\":\"text\",\"data\":\"$1\"},
    \"session_id\":{\"kind\":\"text\",\"data\":\"01a1f313-bf00-70a1-8b2c-3d4e5f6a7b8c\"},
    \"agent_id\":{\"kind\":\"text\",\"data\":\"run-tests\"},
    \"source\":{\"kind\":\"text\",\"data\":\"agent\"},
    \"kind\":{\"kind\":\"text\",\"data\":\"discovery\"},
    \"title\":{\"kind\":\"text\",\"data\":\"Duplicado exacto de prueba\"},
    \"body\":{\"kind\":\"text\",\"data\":\"Mismo contenido y mismo hash.\"},
    \"confidence\":{\"kind\":\"text\",\"data\":\"confirmed\"},
    \"last_seen_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T16:00:00Z\"},
    \"content_hash\":{\"kind\":\"text\",\"data\":\"$hash\"},
    \"created_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T16:00:00Z\"},
    \"updated_at\":{\"kind\":\"text\",\"data\":\"2026-11-19T16:00:00Z\"}
  }" 2>&1
}
out=$(new_obs "01a1f600-2222-7081-9000-000000000001"); rc=$?
out2=$(new_obs "01a1f600-2222-7081-9000-000000000002"); rc2=$?
code=$(error_code "$out2")
n=$(sqlite3 "$db" "SELECT count(*) FROM observations WHERE content_hash='$hash';")
if [ "$rc" -eq 0 ] && [ "$rc2" -eq 6 ] && [ "$code" = "E_CONSTRAINT" ] && [ "$n" = "1" ]; then
  ok "segunda captura idéntica devuelve E_CONSTRAINT y deja una fila"
else
  ko "segunda captura idéntica devuelve E_CONSTRAINT y deja una fila" "rc1=$rc rc2=$rc2 code=$code filas=$n"
fi

# --- Caso 7: política de solo lectura en meta ------------------------------
out=$(db-ai touch "$alias/meta" --data '{
  "id":{"kind":"text","data":"01a1f600-2222-7081-9000-000000000003"},
  "name":{"kind":"text","data":"intruso"},
  "value":{"kind":"text","data":"x"},
  "updated_at":{"kind":"text","data":"2026-11-19T16:00:00Z"}
}' 2>&1); rc=$?
code=$(error_code "$out")
if [ "$rc" -eq 4 ] && [ "$code" = "E_DENIED" ]; then
  ok "touch en /meta devuelve E_DENIED"
else
  ko "touch en /meta devuelve E_DENIED" "rc=$rc code=$code salida: $out"
fi

# --- Caso 8: idempotencia de init ------------------------------------------
before=$(counts)
before_key=$(sqlite3 "$db" "SELECT value FROM meta WHERE name='project_key';")
out=$(bash "$init" 2>&1); rc=$?
after=$(counts)
after_key=$(sqlite3 "$db" "SELECT value FROM meta WHERE name='project_key';")
if [ "$rc" -eq 0 ] && [ "$before" = "$after" ] && [ "$before_key" = "$after_key" ]; then
  ok "segundo init sale 0, conserva las filas ($after) y el project_key"
else
  ko "segundo init sale 0, conserva las filas y el project_key" "rc=$rc antes=$before despues=$after clave_antes=$before_key clave_despues=$after_key salida: $out"
fi

# --- Caso 9: el helper canónico de content_hash (DESIGN §5.2) --------------
calc=$(sqlite3 "$db" "SELECT kind||char(9)||coalesce(title,'')||char(9)||coalesce(body,'')||char(9)||coalesce(rationale,'')||char(9)||coalesce(alternatives,'')||char(9)||coalesce(consequences,'')||char(9)||coalesce(topic_key,'')||char(9)||content_hash FROM observations LIMIT 1;" | python3 -c "
import json, sqlite3, subprocess, sys
linea = sys.stdin.readline().rstrip('\n').split('\t')
datos = dict(zip(['kind','title','body','rationale','alternatives','consequences','topic_key'], linea[:-1]))
r = subprocess.run([sys.executable, sys.argv[1], 'observations'], input=json.dumps(datos, ensure_ascii=False), capture_output=True, text=True)
print('OK' if r.stdout.strip() == linea[-1] else 'DIFF')
" "$example_dir/tools/content-hash.py")
if [ "$calc" = "OK" ]; then
  ok "tools/content-hash.py reproduce el content_hash del seed (DESIGN §5.2)"
else
  ko "tools/content-hash.py reproduce el content_hash del seed" "$calc"
fi

# --- Casos 10-12: concurrencia sobre config.json ---------------------------
# (a) dos init.sh del mismo proyecto, (b) init.sh junto a db-ai mount de otro
# alias, (c) proyectos independientes. Criterio: 0 montajes perdidos, JSON
# siempre parseable y política/cursor_key intactos.

otro_db="$tmp/otro.db"
sqlite3 "$otro_db" 'CREATE TABLE t (x TEXT);'
db-ai mount "$otro_db" /otro --mode ro >/dev/null 2>&1 \
  || ko "preparación de concurrencia: db-ai mount /otro" "no se pudo montar $otro_db"
cursor=$(python3 - <<'PY'
import base64, os
print(base64.urlsafe_b64encode(os.urandom(32)).decode().rstrip("="))
PY
)
python3 - "$XDG_CONFIG_HOME/db-ai/config.json" "$cursor" <<'PY'
import json, sys
path, cursor = sys.argv[1:3]
with open(path, encoding="utf-8") as fh:
    cfg = json.load(fh)
cfg["cursor_key"] = cursor
with open(path, "w", encoding="utf-8") as fh:
    json.dump(cfg, fh, ensure_ascii=False, indent=2)
    fh.write("\n")
PY

# Comprueba JSON válido, alias correcto con su política, montaje ajeno y cursor.
config_ok() {
  python3 - "$XDG_CONFIG_HOME/db-ai/config.json" "$1" "$2" "$3" <<'PY'
import json, os, sys
path, alias, db, cursor = sys.argv[1:5]
try:
    with open(path, encoding="utf-8") as fh:
        cfg = json.load(fh)
except Exception as exc:
    print(f"json ilegible: {exc}")
    sys.exit(1)
mounts = cfg.get("mounts", {})
m = mounts.get(alias)
if not m or os.path.realpath(m.get("file", "")) != os.path.realpath(db):
    print(f"alias {alias} ausente o mal apuntado: {m}")
    sys.exit(1)
if m.get("mode") != "rw" or len(m.get("allow", [])) != 5:
    print(f"política inesperada en {alias}: {m}")
    sys.exit(1)
if "otro" not in mounts:
    print("montaje ajeno 'otro' perdido")
    sys.exit(1)
if cfg.get("cursor_key") != cursor:
    print("cursor_key perdido")
    sys.exit(1)
PY
}

conc_home="$tmp/concurrente"
conc_db="$conc_home/.agent-memory/memory.db"
fallos_init=0
perdidas=0
invalidos=0
for i in $(seq 1 10); do
  ACM_HOME="$conc_home" ACM_ALIAS=conc bash "$init" >/dev/null 2>&1 &
  p1=$!
  ACM_HOME="$conc_home" ACM_ALIAS=conc bash "$init" >/dev/null 2>&1 &
  p2=$!
  wait "$p1" || fallos_init=$((fallos_init + 1))
  wait "$p2" || fallos_init=$((fallos_init + 1))
  python3 -m json.tool "$XDG_CONFIG_HOME/db-ai/config.json" >/dev/null 2>&1 \
    || invalidos=$((invalidos + 1))
  config_ok conc "$conc_db" "$cursor" >/dev/null || perdidas=$((perdidas + 1))
done
if [ "$fallos_init" -eq 0 ] && [ "$perdidas" -eq 0 ] && [ "$invalidos" -eq 0 ] \
  && [ -f "$conc_db" ]; then
  ok "concurrencia (a): 10 iteraciones de dos init en paralelo sin pérdidas"
else
  ko "concurrencia (a): dos init en paralelo" "fallos=$fallos_init perdidas=$perdidas json_invalidos=$invalidos db=$([ -f "$conc_db" ] && echo sí || echo no)"
fi

# (b) init.sh concurrente con db-ai mount de otro alias.
fallos_b=0
for i in $(seq 1 5); do
  nuevo_db="$tmp/nuevo-$i.db"
  sqlite3 "$nuevo_db" 'CREATE TABLE t (x TEXT);'
  ACM_HOME="$tmp/proyecto-b" ACM_ALIAS=init-b bash "$init" >/dev/null 2>&1 &
  pb=$!
  db-ai mount "$nuevo_db" "/externo$i" --mode ro >/dev/null 2>&1 &
  pm=$!
  wait "$pb" || fallos_b=$((fallos_b + 1))
  wait "$pm" || fallos_b=$((fallos_b + 1))
  python3 -m json.tool "$XDG_CONFIG_HOME/db-ai/config.json" >/dev/null 2>&1 \
    || fallos_b=$((fallos_b + 1))
  db-ai mounts 2>/dev/null | grep -Fq "\"alias\":\"externo$i\"" \
    || fallos_b=$((fallos_b + 1))
done
if [ "$fallos_b" -eq 0 ] \
  && config_ok init-b "$tmp/proyecto-b/.agent-memory/memory.db" "$cursor" >/dev/null; then
  ok "concurrencia (b): init.sh y db-ai mount no se pisan"
else
  ko "concurrencia (b): init.sh y db-ai mount no se pisan" "fallos=$fallos_b"
fi

# (c) proyectos independientes en paralelo.
ACM_HOME="$tmp/proyecto-c1" ACM_ALIAS=conc-c1 bash "$init" >/dev/null 2>&1 &
pc1=$!
ACM_HOME="$tmp/proyecto-c2" ACM_ALIAS=conc-c2 bash "$init" >/dev/null 2>&1 &
pc2=$!
wait "$pc1"; rc1=$?
wait "$pc2"; rc2=$?
if [ "$rc1" -eq 0 ] && [ "$rc2" -eq 0 ] \
  && config_ok conc-c1 "$tmp/proyecto-c1/.agent-memory/memory.db" "$cursor" >/dev/null \
  && config_ok conc-c2 "$tmp/proyecto-c2/.agent-memory/memory.db" "$cursor" >/dev/null; then
  ok "concurrencia (c): dos proyectos independientes conservan ambos montajes"
else
  ko "concurrencia (c): dos proyectos independientes" "rc1=$rc1 rc2=$rc2"
fi

# --- Caso 13: reanudación con más de una página de checkpoints activos ------
# Fixture: 12 sesiones y 12 checkpoints activos además del seed. `find` ordena
# por PK ascendente, así que con límite 5 el vigente (UUIDv7 mayor) no cabe en
# la primera página. El protocolo 1.4 corregido lo localiza por la ventana de
# sesiones con 2 `find` acotados, sin exportar la tabla ni recorrer páginas.
mapfile -t fixture < <(python3 - "$db" <<'PY'
import datetime, hashlib, json, sqlite3, sys

con = sqlite3.connect(sys.argv[1])
now = datetime.datetime.now(datetime.timezone.utc).replace(microsecond=0)
for i in range(1, 13):
    sid = f"01a2b000-0000-7000-8000-{i:012d}"
    cid = f"01a2c000-0000-7000-8000-{i:012d}"
    ts = (now - datetime.timedelta(minutes=12 - i)).strftime("%Y-%m-%dT%H:%M:%SZ")
    con.execute(
        "INSERT INTO sessions (id, agent_id, source, goal, status, started_at,"
        " created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)",
        (sid, f"agente-{i}", "agent", f"sesión {i}", "closed", ts, ts, ts),
    )
    campos = [f"objetivo {i}", f"estado {i}", f"paso {i}", "[]", "[]"]
    h = "sha256:" + hashlib.sha256(
        json.dumps(campos, ensure_ascii=False, separators=(",", ":")).encode()
    ).hexdigest()
    con.execute(
        "INSERT INTO checkpoints (id, session_id, agent_id, source, goal, state,"
        " next_steps, files, open_questions, status, content_hash, created_at,"
        " updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)",
        (cid, sid, f"agente-{i}", "agent", *campos, "active", h, ts, ts),
    )
con.commit()
print(f"01a2b000-0000-7000-8000-{12:012d}")
print(f"01a2c000-0000-7000-8000-{12:012d}")
PY
)

# Extrae los id de las filas de una respuesta de find, en el orden devuelto.
ids() {
  python3 -c 'import json, sys
try:
    print("\n".join(r["fields"]["id"]["data"] for r in json.load(sys.stdin)["data"]["rows"]))
except Exception:
    pass'
}

corte=$(date -u -d '14 days ago' +%Y-%m-%dT%H:%M:%SZ)
sesiones=$(db-ai find "$alias/sessions" --where "started_at>=$corte" --limit 50 2>&1)
elegida=$(printf '%s' "$sesiones" | ids | sort | tail -n 1)
checkpoints=$(db-ai find "$alias/checkpoints" --where "session_id=$elegida" --where 'status=active' --limit 5 2>&1)
vigente=$(printf '%s' "$checkpoints" | ids | sort | tail -n 1)
# Protocolo antiguo: `find` de checkpoints activos sin filtro de sesión. El
# vigente no está en esa página y el máximo de la página no es el vigente.
pagina=$(db-ai find "$alias/checkpoints" --where 'status=active' --limit 5 2>&1)
antiguo=$(printf '%s' "$pagina" | ids | sort | tail -n 1)
pagina_filas=$(printf '%s' "$pagina" | ids | grep -c . || true)
# meta.truncated distingue "la ventana entera está en la página" de "falta la
# parte reciente": el protocolo corrige la ventana; el antiguo no puede.
truncado() {
  python3 -c 'import json, sys
try:
    print(json.load(sys.stdin)["meta"]["truncated"])
except Exception:
    print("error")'
}
sesiones_truncada=$(printf '%s' "$sesiones" | truncado)
pagina_truncada=$(printf '%s' "$pagina" | truncado)
activos=$(sqlite3 "$db" "SELECT count(*) FROM checkpoints WHERE status='active';")

if [ "$vigente" = "${fixture[1]:-}" ] && [ "$elegida" = "${fixture[0]:-}" ] \
  && [ "$activos" -gt 5 ] && [ "$pagina_filas" -eq 5 ] \
  && [ "$sesiones_truncada" = "False" ] && [ "$pagina_truncada" = "True" ] \
  && [ "$antiguo" != "${fixture[1]:-}" ]; then
  ok "checkpoint vigente entre $activos activos con límite 5 (2 find acotados)"
else
  ko "checkpoint vigente entre $activos activos con límite 5" \
    "sesión=$elegida vigente=$vigente esperado_sesión=${fixture[0]:-} esperado_checkpoint=${fixture[1]:-} página=$pagina_filas truncada_sesiones=$sesiones_truncada truncada_página=$pagina_truncada antiguo=$antiguo activos=$activos"
fi

# --- Resultado -------------------------------------------------------------
printf '\n%d ok, %d fallos\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
