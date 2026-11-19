#!/usr/bin/env bash
# Inicializa la memoria de ACM para un proyecto: esquema, seed, montaje y
# verificación. Es idempotente: ejecutarlo dos veces deja el mismo estado.
#
#   bash init.sh                 # proyecto actual = directorio de este script
#   ACM_HOME=/ruta bash init.sh  # otra raíz (la memoria vive en $ACM_HOME/.agent-memory)
#   ACM_ALIAS=memtest bash init.sh
#
# Variables de entorno:
#   ACM_HOME   Raíz del proyecto cuya memoria se inicializa. Por defecto, la
#              carpeta de este script.
#   ACM_ALIAS  Alias de db-ai para la base. Por defecto, mem.
#   XDG_CONFIG_HOME  Respeta la configuración de db-ai (pruebas y CI).
#
# Dependencias: sqlite3 (DDL y seed), db-ai (montaje y verificación) y python3
# (mezcla atómica de $XDG_CONFIG_HOME/db-ai/config.json). db-ai rechaza
# `mount --mode rw` sin política y no tiene flag para escribirla, así que el
# operador la escribe en config.json; este script hace esa parte por ti.

set -uo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P) || exit 1
acm_home=${ACM_HOME:-$script_dir}
alias_name=${ACM_ALIAS:-mem}

case "$alias_name" in
  *[!A-Za-z0-9_-]* | '') echo "init.sh: ACM_ALIAS '$alias_name' no cumple [A-Za-z0-9_-]+" >&2; exit 1 ;;
esac

memory_dir="$acm_home/.agent-memory"
schema="$script_dir/schema.sql"
seed="$script_dir/seed.sql"

fail() { printf 'init.sh: %s\n' "$*" >&2; exit 1; }
note() { printf '%s\n' "$*"; }

command -v sqlite3 >/dev/null 2>&1 || fail "falta sqlite3 en PATH"
command -v db-ai >/dev/null 2>&1 || fail "falta db-ai en PATH"
command -v python3 >/dev/null 2>&1 || fail "falta python3 en PATH (mezcla de config.json de db-ai)"
[ -f "$schema" ] || fail "no existe $schema"
[ -f "$seed" ] || fail "no existe $seed"

mkdir -p "$memory_dir" || fail "no se pudo crear $memory_dir"
memory_dir=$(cd -- "$memory_dir" && pwd -P) || fail "no se pudo resolver $memory_dir"
db="$memory_dir/memory.db"

# --- Esquema: solo si la base no existe o le faltan tablas -------------------
if [ ! -f "$db" ]; then
  sqlite3 "$db" <"$schema" || fail "schema.sql falló sobre una base nueva; no se continúa"
  note "esquema: aplicado en $db"
else
  tables=$(sqlite3 "$db" \
    "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN
     ('meta','sessions','observations','checkpoints','preferences');" 2>/dev/null) \
    || fail "no se pudo leer el catálogo de $db"
  if [ "$tables" -lt 5 ]; then
    note "esquema: faltan tablas ($tables de 5); aplicando schema.sql"
    sqlite3 "$db" <"$schema" \
      || fail "schema.sql no se pudo aplicar sobre una base parcial; revísala antes de reintentar"
  else
    note "esquema: presente ($tables de 5 tablas)"
  fi
fi

# --- Seed: solo si no hay datos de memoria en ninguna tabla -----------------
rows=$(sqlite3 "$db" \
  "SELECT (SELECT count(*) FROM meta)+(SELECT count(*) FROM sessions)+
          (SELECT count(*) FROM observations)+(SELECT count(*) FROM checkpoints)+
          (SELECT count(*) FROM preferences);" 2>/dev/null) \
  || fail "no se pudieron contar las filas de $db"
if [ "$rows" -eq 0 ]; then
  { printf 'PRAGMA foreign_keys = ON;\nBEGIN;\n'; cat "$seed"; printf 'COMMIT;\n'; } \
    | sqlite3 "$db" || fail "seed.sql falló; la transacción se revirtió"
  note "seed: aplicado (base vacía)"

  # El seed trae un project_key de demostración. La identidad del proyecto es
  # de la base, no del fixture: se genera una por inicialización (DESIGN §3.2).
  project_key=$(python3 - <<'PY'
import os, time
ms = int(time.time() * 1000)
r = os.urandom(9).hex()
t = f"{ms:012x}"
print(f"{t[:8]}-{t[8:12]}-7{r[:3]}-8{r[3:6]}-{r[6:18]}")
PY
  ) || fail "no se pudo generar project_key"
  sqlite3 "$db" \
    "UPDATE meta SET value='$project_key', updated_at=strftime('%Y-%m-%dT%H:%M:%SZ','now')
     WHERE name='project_key';" || fail "no se pudo fijar project_key en meta"
  note "identidad: project_key generado ($project_key)"
else
  note "seed: omitido ($rows filas existentes; la base no está vacía)"
fi

# --- Montaje: config.json de db-ai (alias rw con la política de DESIGN §6) --
if [ -n "${XDG_CONFIG_HOME:-}" ]; then
  config_dir="$XDG_CONFIG_HOME/db-ai"
else
  config_dir="$HOME/.config/db-ai"
fi
mkdir -p "$config_dir" || fail "no se pudo crear $config_dir"
chmod 700 "$config_dir" || fail "no se pudieron fijar los permisos de $config_dir"

python3 - "$config_dir" "$db" "$alias_name" <<'PY' || exit 1
import json, os, sys, tempfile

config_dir, db, alias = sys.argv[1:4]
path = os.path.join(config_dir, "config.json")
read_ops = ["ls", "tree", "cat", "describe", "stat", "find", "grep", "export"]
mount = {
    "file": os.path.realpath(db),
    "mode": "rw",
    "allow": [
        {"ops": read_ops, "prefix": f"/{alias}"},
        {"ops": ["touch", "write"], "prefix": f"/{alias}/sessions"},
        {"ops": ["touch", "write"], "prefix": f"/{alias}/observations"},
        {"ops": ["touch", "write"], "prefix": f"/{alias}/checkpoints"},
        {"ops": ["touch", "write"], "prefix": f"/{alias}/preferences"},
    ],
}

cfg = {"version": 1, "mounts": {}}
if os.path.exists(path):
    try:
        with open(path, encoding="utf-8") as fh:
            cfg = json.load(fh)
    except (OSError, ValueError) as exc:
        print(f"init.sh: config.json ilegible en {path}: {exc}", file=sys.stderr)
        sys.exit(1)

if cfg.get("version") != 1:
    print(f"init.sh: config.json con version {cfg.get('version')!r}, se esperaba 1", file=sys.stderr)
    sys.exit(1)

mounts = cfg.setdefault("mounts", {})
existing = mounts.get(alias)
if existing and os.path.realpath(existing.get("file", "")) != mount["file"]:
    print(
        f"init.sh: el alias '{alias}' ya apunta a {existing.get('file')}; "
        f"usa ACM_ALIAS=otro para no pisar ese montaje",
        file=sys.stderr,
    )
    sys.exit(1)

mounts[alias] = mount
fd, tmp = tempfile.mkstemp(dir=config_dir, prefix=".config-", suffix=".json")
try:
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        json.dump(cfg, fh, ensure_ascii=False, indent=2)
        fh.write("\n")
    os.chmod(tmp, 0o600)
    os.replace(tmp, path)
finally:
    if os.path.exists(tmp):
        os.unlink(tmp)
PY

note "montaje: alias '$alias_name' -> $db (rw, política de DESIGN §6)"

# --- Verificación con db-ai -------------------------------------------------
mounts_json=$(db-ai mounts) || fail "db-ai mounts falló: $mounts_json"
printf '%s' "$mounts_json" | grep -Fq "\"alias\":\"$alias_name\"" \
  || fail "db-ai mounts no muestra el alias '$alias_name': $mounts_json"
note "verificación: db-ai mounts muestra '$alias_name'"

ls_json=$(db-ai ls "/$alias_name") || fail "db-ai ls /$alias_name falló: $ls_json"
printf '%s' "$ls_json" | grep -Fq '"ok":true' \
  || fail "db-ai ls /$alias_name no respondió ok: $ls_json"
note "verificación: db-ai ls /$alias_name responde ok:true"

project_row=$(sqlite3 "$db" "SELECT id FROM meta WHERE name='project_key';") \
  || fail "no se pudo leer project_key de meta"
[ -n "$project_row" ] || fail "meta no contiene project_key; la base no es de ACM"
cat_json=$(db-ai cat "/$alias_name/meta/$project_row") || fail "db-ai cat de meta falló: $cat_json"
printf '%s' "$cat_json" | grep -Fq '"ok":true' \
  || fail "db-ai cat de meta no respondió ok: $cat_json"
note "verificación: db-ai cat /$alias_name/meta/$project_row responde ok:true"

printf 'listo: memoria de ACM en %s montada como %s\n' "$db" "$alias_name"
