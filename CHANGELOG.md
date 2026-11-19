# Changelog

Todos los cambios notables de este proyecto se documentan en este fichero.

El formato está basado en [Keep a Changelog](https://keepachangelog.com/es-ES/1.1.0/),
y este proyecto se adhiere al [Versionado Semántico](https://semver.org/lang/es/).

## [Sin publicar]

### Añadido

- Contratos funcionales y técnicos iniciales: `docs/PRD.md` (requisitos y alcance) y
  `docs/DESIGN.md` (arquitectura y decisiones técnicas).
- Sistema de trabajo: tablero tasks-ai (`.tasks-ai/`), mapa de fuentes para agentes
  (`AGENTS.md`), política Git (`CONTRIBUTING.md`), catálogo enlazable (`llms.txt`) y
  configuración de plugins (`opencode.jsonc`).
- Informe de revisión de los contratos iniciales (`docs/reviews/2026-11-19-prd-design-review.md`).

### Modificado

- Contratos PRD y DESIGN revisados a v0.2.0 con las 21 decisiones aprobadas por el PO
  (D-1…D-20, E-1): dirección por ruta VFS, JSON único por defecto, esquema de error y
  cursor definidos, política de lectura de campo en M1, límites como default y máximo,
  salida 10 para commit incierto.
