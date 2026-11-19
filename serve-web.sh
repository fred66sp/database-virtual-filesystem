#!/usr/bin/env bash
#
# Deja el servidor de la documentación web (docs/web) en segundo plano: arranca
# python3 -m http.server contra docs/web y deja su registro y su pid en .run/.
#
#   ./serve-web.sh          arranca el servidor si no está ya en marcha
#   ./serve-web.sh status   dice si está en marcha y dónde escucha
#   ./serve-web.sh stop     lo para
#
# Puerto fijo 8891, distinto del del tablero (8890). El servidor se reconoce por
# el puerto en el que escucha, no por el pid de este script: si se arrancó a mano
# con `python3 -m http.server 8891 -d docs/web`, también se ve y se para desde
# aquí. Un proceso ajeno en ese puerto no se toca.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$root"

docroot="$root/docs/web"
state="$root/.run"
pidfile="$state/serve-web.pid"
logfile="$state/serve-web.log"
port=8891
address="http://127.0.0.1:$port"

# listening devuelve el pid que escucha en el puerto, o nada.
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

# ours dice si el pid sirve esta documentación: el pid de un fichero que envejeció
# puede ser de cualquiera, y matarlo sería el error.
ours() {
	local pid="${1:-}"
	[[ -n "$pid" ]] || return 1
	ps -p "$pid" -o args= 2>/dev/null | grep -q "http.server $port" || return 1
	ps -p "$pid" -o args= 2>/dev/null | grep -q -- "-d $docroot"
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

start() {
	local pid
	pid="$(server)"
	if [[ -n "$pid" ]]; then
		echo "El servidor ya está en marcha (pid $pid): $address"
		return
	fi

	busy && return 1

	if [[ ! -d "$docroot" ]]; then
		echo "Falta $docroot: aún no hay documentación web que servir." >&2
		return 1
	fi

	mkdir -p "$state"
	: >"$logfile"

	nohup python3 -m http.server "$port" --bind 127.0.0.1 -d "$docroot" >>"$logfile" 2>&1 &
	echo $! >"$pidfile"

	# Se espera a que el proceso siga vivo y al anuncio del arranque en el registro:
	# un puerto ocupado solo se descubre un momento más tarde.
	for _ in {1..50}; do
		kill -0 "$(cat "$pidfile")" 2>/dev/null || { failed; return 1; }
		grep -q "Serving HTTP" "$logfile" && break
		sleep 0.1
	done
	sleep 0.2
	kill -0 "$(cat "$pidfile")" 2>/dev/null || { failed; return 1; }

	echo "Servidor en marcha (pid $(cat "$pidfile")): $address"
	echo "  Índice:         $address/"
	echo "Registro: $logfile"
}

failed() {
	echo "El servidor no arrancó. Registro:" >&2
	cat "$logfile" >&2 2>/dev/null || true
	rm -f "$pidfile"
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

	# Se le da tiempo a atender la señal antes de darlo por colgado.
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
