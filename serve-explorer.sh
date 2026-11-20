#!/usr/bin/env bash
#
# Deja el Visual Explorer (db-ai serve) en segundo plano: arranca el binario
# instalado en ~/.local/bin y deja su registro y su pid en .run/.
#
#   ./serve-explorer.sh          arranca el servidor si no está ya en marcha
#   ./serve-explorer.sh status   dice si está en marcha y dónde escucha
#   ./serve-explorer.sh stop     lo para
#
# Puerto fijo 8893, distinto del de la documentación web (8891) y del tablero
# (8890). El servidor se reconoce por el puerto en el que escucha, no por el pid
# que guardó este script: si se arrancó a mano con `db-ai serve --port 8893`,
# también se ve y se para desde aquí. Un proceso ajeno en ese puerto no se toca.
#
# Arrancar dos veces no duplica nada: la segunda invocación informa del servidor
# ya en marcha y no escribe ni el pid ni el registro.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$root"

binary="${DB_AI:-$HOME/.local/bin/db-ai}"
if [[ ! -x "$binary" ]] && command -v db-ai >/dev/null 2>&1; then
	binary="$(command -v db-ai)"
fi
state="$root/.run"
pidfile="$state/serve-explorer.pid"
logfile="$state/serve-explorer.log"
port=8893
address="http://127.0.0.1:$port"

# listening devuelve el pid que escucha en el puerto del explorador, o nada.
listening() {
	local out=""
	if command -v ss >/dev/null 2>&1; then
		out="$(ss -lptnH "sport = :$port" 2>/dev/null | grep -o 'pid=[0-9]*' | head -1 | cut -d= -f2 || true)"
	fi
	if [[ -z "$out" ]] && command -v lsof >/dev/null 2>&1; then
		out="$(lsof -ti "tcp:$port" -sTCP:LISTEN 2>/dev/null | head -1 || true)"
	fi
	printf '%s' "$out"
}

# ours dice si el pid es un db-ai serve y no otro proceso: el pid de un fichero
# que envejeció puede ser de cualquiera, y matarlo sería el error.
ours() {
	local pid="${1:-}"
	[[ -n "$pid" ]] || return 1
	ps -p "$pid" -o args= 2>/dev/null | grep -q '[d]b-ai serve'
}

# server devuelve el pid del servidor en marcha, lo arrancara quien lo arrancara.
server() {
	local pid
	pid="$(cat "$pidfile" 2>/dev/null || true)"
	if ours "$pid"; then
		printf '%s' "$pid"
		return 0
	fi
	pid="$(listening)"
	if ours "$pid"; then
		printf '%s' "$pid"
	fi
	return 0
}

# busy nombra al proceso que ocupa el puerto sin ser el servidor. No se toca: se dice.
busy() {
	local pid
	pid="$(listening)"
	[[ -n "$pid" ]] || return 1
	echo "El puerto $port lo ocupa otro proceso (pid $pid: $(ps -p "$pid" -o args= 2>/dev/null)): no se toca." >&2
	return 0
}

# failed cuenta por qué no arrancó y retira el pid que ya no vale.
failed() {
	echo "El servidor no arrancó. Registro:" >&2
	cat "$logfile" >&2 2>/dev/null || true
	rm -f "$pidfile"
}

start() {
	local pid
	pid="$(server)"
	if [[ -n "$pid" ]]; then
		echo "El servidor ya está en marcha (pid $pid): $address"
		return
	fi

	busy && return 1

	if [[ ! -x "$binary" ]]; then
		echo "Falta el binario db-ai: constrúyelo con 'go build -o $HOME/.local/bin/db-ai ./cmd/db-ai'." >&2
		return 1
	fi

	mkdir -p "$state"
	: >"$logfile"

	nohup "$binary" serve --port "$port" >>"$logfile" 2>&1 &
	echo $! >"$pidfile"

	# Se espera al anuncio de la dirección y a que el proceso siga vivo después:
	# el aviso sale antes de atender, así que un puerto ocupado solo se descubre
	# un momento más tarde.
	for _ in {1..50}; do
		kill -0 "$(cat "$pidfile")" 2>/dev/null || { failed; return 1; }
		grep -q 'listening on' "$logfile" && break
		sleep 0.1
	done
	sleep 0.2
	kill -0 "$(cat "$pidfile")" 2>/dev/null || { failed; return 1; }

	echo "Servidor en marcha (pid $(cat "$pidfile")): $address"
	echo "Registro: $logfile"
}

stop() {
	local pid
	pid="$(server)"
	if [[ -z "$pid" ]]; then
		rm -f "$pidfile"
		busy && return 1
		echo "El servidor no está en marcha."
		return
	fi

	kill "$pid" 2>/dev/null || true

	# El servidor atiende la señal y cierra su canal de avisos: se le da tiempo
	# antes de darlo por colgado.
	for _ in {1..50}; do
		kill -0 "$pid" 2>/dev/null || break
		sleep 0.1
	done
	kill -9 "$pid" 2>/dev/null || true

	rm -f "$pidfile"
	echo "Servidor parado."
}

status() {
	local pid
	pid="$(server)"
	if [[ -n "$pid" ]]; then
		echo "En marcha (pid $pid): $address"
	else
		echo "Parado."
	fi
}

case "${1:-start}" in
start) start ;;
stop) stop ;;
status) status ;;
*)
	echo "Uso: $0 [start|stop|status]" >&2
	exit 2
	;;
esac
