# database-virtual-filesystem (db-ai)

> Herramienta CLI agent-first que proporciona acceso a bases de datos mediante la
> semántica operativa de los sistemas de archivos.

La definición funcional y el alcance vigentes viven en el contrato:
[`docs/PRD.md`](docs/PRD.md). Las decisiones técnicas, en [`docs/DESIGN.md`](docs/DESIGN.md).
Este README no duplica su contenido: enlaza.

## Documentación

- [PRD — contrato funcional](docs/PRD.md)
- [DESIGN — contrato técnico](docs/DESIGN.md)
- [Política Git y contribución](CONTRIBUTING.md)
- [Registro de cambios](CHANGELOG.md)
- [Mapa del repositorio para agentes](AGENTS.md)
- [Documentación web (HTML)](docs/web/index.html) — contratos navegables de db-ai y ACM

La documentación web se previsualiza en local con [`serve-web.sh`](serve-web.sh):
`./serve-web.sh` sirve `docs/web/` en `http://127.0.0.1:8891`; `./serve-web.sh
status` y `./serve-web.sh stop` lo gestionan. No es un despliegue, solo
previsualización.

## Estado

CLI `db-ai` implementada y verificada contra los contratos (WP-01…WP-06, Gate M3):
montajes, catálogo y lectura; sesiones explícitas (`cd`, `pwd`), consultas `find` con
filtros tipados, cursores keyset firmados y `export` JSON/JSONL; política de escritura
deny-by-default, auditoría JSONL, transacciones, ETag y `--dry-run`; mutaciones `cp`,
`mv` y `rm`; y `batch` (FR-12). Las formas de cada comando viven en
[`docs/DESIGN.md`](docs/DESIGN.md) §3 y §8; este README no las duplica.

Ejemplo mínimo:

```bash
db-ai mount ./tasks.db /work            # montaje de solo lectura por defecto
db-ai ls /work
db-ai cat /work/tasks/TK-001
db-ai batch --data '[{"op":"write","path":"/work/tasks/TK-001/status","if_match":"sha256:<ETag de cat>","data":{"kind":"text","data":"done"}}]'
```

El `batch` muestra la forma del array: las mutaciones exigen además montaje en
`--mode rw` y una política que autorice la ruta ([DESIGN §8-9](docs/DESIGN.md)).
Las operaciones de un `batch` no pueden encadenarse sobre filas creadas o
modificadas por operaciones anteriores del mismo lote: el `if_match` describe el
estado previo al lote, así que el encadenamiento devuelve `E_CONFLICT` y revierte
todo; divide el trabajo en dos invocaciones.

El trabajo se gestiona en el tablero tasks-ai del repositorio (`.tasks-ai/`); su
catálogo vivo, con `tasks-ai tags`.

## Explorar las bases

`db-ai serve` sirve el Visual Explorer: un servidor HTTP local de solo lectura
que reutiliza el mismo núcleo que la CLI (catálogo, consultas y políticas) y no
ejecuta mutaciones. Escucha en localhost y se cierra con Ctrl+C; la interfaz
recorre montajes, tablas y filas con árbol lateral, vista tabular ordenable y
paginada, inspector de registro y búsqueda, sin conocer el esquema de antemano.
El diseño está en [DESIGN §15](docs/DESIGN.md).

```bash
db-ai serve               # http://127.0.0.1:8080
db-ai serve --port 8099 --open
```

Para dejarlo en segundo plano está [`serve-explorer.sh`](serve-explorer.sh), con
el mismo patrón que `serve-web.sh`: `./serve-explorer.sh` lo arranca en
`http://127.0.0.1:8893`, y `./serve-explorer.sh status` y `./serve-explorer.sh
stop` lo gestionan. Es una herramienta local de operación, no un despliegue.

## Compilar y probar

```bash
go build ./cmd/db-ai
go test ./...
go test -race ./...
```

Para que el binario declare el commit del que se compiló, inyéctalo con
`-ldflags`; sin inyección, `db-ai version` informa `commit: "unknown"` y
`state: "dev"`:

```bash
go build -ldflags "-X dbvfs/internal/cli.commit=$(git rev-parse --short HEAD)" -o db-ai ./cmd/db-ai
./db-ai version
```

`db-ai version` no toca configuración, estado ni bases: emite en `data` la
versión funcional, el commit y el estado de build ([DESIGN §3](docs/DESIGN.md)).

El binario no se versiona en el repositorio: se construye desde el código
(`go build -o db-ai ./cmd/db-ai`) y se instala en tu PATH local (p. ej.
`~/.local/bin`). En cada release semver se publica como asset adjunto a la
release de GitHub, nunca dentro del árbol del repo.
