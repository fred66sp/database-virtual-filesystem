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

## Compilar y probar

```bash
go build ./cmd/db-ai
go test ./...
go test -race ./...
```
