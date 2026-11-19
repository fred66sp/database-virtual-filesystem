#!/usr/bin/env bash
# Inicializa la memoria de ACM para un proyecto: esquema, identidad, montaje y
# verificación. Es idempotente: ejecutarlo dos veces deja el mismo estado.
#
#   bash init.sh                 # memoria vacía: esquema + metadatos del proyecto
#   bash init.sh --demo          # añade los datos ficticios de seed.sql
#   ACM_HOME=/ruta bash init.sh  # otra raíz (la memoria vive en $ACM_HOME/.agent-memory)
#   ACM_ALIAS=memtest bash init.sh
#
# Variables de entorno:
#   ACM_HOME   Raíz del proyecto cuya memoria se inicializa. Por defecto, la
#              carpeta de este script.
#   ACM_ALIAS  Alias de db-ai para la base. Por defecto, mem.
#   XDG_CONFIG_HOME  Respeta la configuración de db-ai (pruebas y CI).
#
# Dependencias: sqlite3 (DDL), db-ai (montaje y verificación), python3 (mezcla
# atómica de $XDG_CONFIG_HOME/db-ai/config.json) y flock(1) (exclusión mutua con
# db-ai sobre config.lock). db-ai rechaza `mount --mode rw` sin política y no
# tiene flag para escribirla, así que el operador la escribe en config.json;
# este script hace esa parte por ti.

set -uo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P) || exit 1
acm_home=${ACM_HOME:-$script_dir}
alias_name=${ACM_ALIAS:-mem}
demo=0

for arg in "$@"; do
  case "$arg" in
    --demo) demo=1 ;;
    *) printf "init.sh: argumento no reconocido '%s' (usa --demo)\n" "$arg" >&2; exit 1 ;;
  esac
done

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
command -v flock >/dev/null 2>&1 || fail "falta flock(1) en PATH: sin él no se puede compartir el lock de db-ai"
[ -f "$schema" ] || fail "no existe $schema"
if [ "$demo" -eq 1 ]; then
  [ -f "$seed" ] || fail "no existe $seed"
fi

# --- Lock de db-ai ----------------------------------------------------------
# Se toma antes de crear o tocar la base y se mantiene hasta publicar la entrada
# en config.json. Así dos init.sh (o un init.sh y un `db-ai mount`) se serializan
# sobre el mismo flock exclusivo que usa db-ai (internal/state: config.lock
# junto a config.json). Un fallo posterior libera el lock al salir el proceso.
if [ -n "${XDG_CONFIG_HOME:-}" ]; then
  config_dir="$XDG_CONFIG_HOME/db-ai"
else
  config_dir="$HOME/.config/db-ai"
fi
mkdir -p "$config_dir" || fail "no se pudo crear $config_dir"
chmod 700 "$config_dir" || fail "no se pudieron fijar los permisos de $config_dir"

lock_file="$config_dir/config.lock"
old_umask=$(umask)
umask 177
exec 9>"$lock_file" || { umask "$old_umask"; fail "no se pudo abrir el lock $lock_file"; }
umask "$old_umask"
flock -x 9 || fail "no se pudo tomar el lock exclusivo $lock_file"

mkdir -p "$memory_dir" || fail "no se pudo crear $memory_dir"
memory_dir=$(cd -- "$memory_dir" && pwd -P) || fail "no se pudo resolver $memory_dir"
db="$memory_dir/memory.db"

# --- Esquema e identidad -----------------------------------------------------
# Base nueva: DDL, filas reales de meta y, solo con --demo, el fixture. Todo en
# una transacción con -bail sobre un fichero temporal que solo se publica si el
# COMMIT llega: un fallo a mitad no deja esquema parcial ni un fichero a medias.
#
# Base existente: no se re-aplica DDL ni se tocan los recuerdos. Una base parcial
# (menos de 5 tablas) se rechaza con error claro: completarla a ciegas puede
# dejar filas de meta falsas o un esquema incoherente. Es la opción conservadora.
if [ ! -f "$db" ]; then
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ) || fail "no se pudo obtener la fecha UTC"
  ids_out=$(python3 - <<'PY'
import os, time
ms = int(time.time() * 1000)
for _ in range(4):
    r = os.urandom(9).hex()
    t = f"{ms:012x}"
    print(f"{t[:8]}-{t[8:12]}-7{r[:3]}-8{r[3:6]}-{r[6:18]}")
PY
  ) || fail "no se pudo generar project_key"
  mapfile -t ids <<<"$ids_out"
  project_key=${ids[1]}
  project_name=$(basename -- "$acm_home")
  project_name_sql=${project_name//\'/\'\'}

  tmp_db="$memory_dir/.memory.$$.db"
  {
    printf 'PRAGMA foreign_keys = ON;\nBEGIN;\n'
    cat "$schema"
    if [ "$demo" -eq 1 ]; then
      cat "$seed"
      printf "UPDATE meta SET value='%s', updated_at='%s' WHERE name='project_key';\n" \
        "$project_key" "$now"
    else
      printf 'INSERT INTO meta (id, name, value, updated_at) VALUES\n'
      printf "  ('%s', 'schema_version', '1', '%s'),\n" "${ids[0]}" "$now"
      printf "  ('%s', 'project_key', '%s', '%s'),\n" "${ids[1]}" "$project_key" "$now"
      printf "  ('%s', 'project_name', '%s', '%s'),\n" "${ids[2]}" "$project_name_sql" "$now"
      printf "  ('%s', 'created_at', '%s', '%s');\n" "${ids[3]}" "$now" "$now"
    fi
    printf 'COMMIT;\n'
  } | sqlite3 -bail "$tmp_db" \
    || { rm -f "$tmp_db" "$tmp_db-journal"; fail "esquema/identidad fallaron; la transacción se revirtió y no se publicó la base"; }
  mv -f "$tmp_db" "$db" || fail "no se pudo publicar $db"

  note "esquema: aplicado en $db"
  if [ "$demo" -eq 1 ]; then
    note "seed: aplicado (--demo)"
  else
    note "recuerdos: base limpia (sin sesiones, observaciones, checkpoints ni preferencias)"
  fi
  note "identidad: project_key generado ($project_key)"
else
  tables=$(sqlite3 "$db" \
    "SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN
     ('meta','sessions','observations','checkpoints','preferences');" 2>/dev/null) \
    || fail "no se pudo leer el catálogo de $db"
  if [ "$tables" -lt 5 ]; then
    fail "base parcial en $db ($tables de 5 tablas); init.sh no repara bases parciales: elimínala o recupérala antes de reintentar"
  fi
  note "esquema: presente ($tables de 5 tablas); recuerdos intactos"
fi

# --- Montaje: config.json de db-ai (alias rw con la política de DESIGN §6) --
# La lectura-modificación-escritura sigue bajo el flock tomado arriba; el
# fichero se reemplaza atómicamente dentro del lock y nadie pierde alias ajenos,
# cursor_key ni políticas.
python3 - "$config_dir" "$db" "$alias_name" <<'PY'
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
        f"no se sobrescribe. Usa ACM_ALIAS=otro para convivir con ese montaje",
        file=sys.stderr,
    )
    sys.exit(1)

mounts[alias] = mount
fd, tmp = tempfile.mkstemp(dir=config_dir, prefix=".config-", suffix=".json")
try:
    with os.fdopen(fd, "w", encoding="utf-8") as fh:
        json.dump(cfg, fh, ensure_ascii=False, indent=2)
        fh.write("\n")
        fh.flush()
        os.fsync(fh.fileno())
    os.chmod(tmp, 0o600)
    os.replace(tmp, path)
finally:
    if os.path.exists(tmp):
        os.unlink(tmp)
PY
config_rc=$?
exec 9>&-
[ "$config_rc" -eq 0 ] || exit 1

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
