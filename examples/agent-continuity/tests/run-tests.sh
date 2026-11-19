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

# --- Caso 1: init sobre base nueva -----------------------------------------
out=$(bash "$init" 2>&1); rc=$?
if [ "$rc" -eq 0 ] && [ -f "$db" ]; then
  ok "init crea la base y sale 0"
else
  ko "init crea la base y sale 0" "rc=$rc salida: $out"
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

# --- Resultado -------------------------------------------------------------
printf '\n%d ok, %d fallos\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
