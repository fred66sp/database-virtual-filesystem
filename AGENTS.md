# AGENTS.md

Este fichero dice dónde vive cada fuente canónica del proyecto y qué leer antes de cada
encargo; no guarda contenido, solo enlaza.

## Qué se busca → dónde vive

| Qué se busca | Dónde vive |
|---|---|
| Qué es db-ai, alcance y requisitos | [`docs/PRD.md`](docs/PRD.md) y [`README.md`](README.md) |
| Cómo funciona por dentro: decisiones técnicas | [`docs/DESIGN.md`](docs/DESIGN.md) |
| Ramas, commits, verificación, integración y operaciones con autorización | [`CONTRIBUTING.md`](CONTRIBUTING.md) |
| Decisiones de arquitectura aprobadas (ADR) | [`docs/decisions/README.md`](docs/decisions/README.md) — exportación generada; el registro canónico vive en el tablero (`tasks-ai adr list`) |
| Qué cambió en cada versión | [`CHANGELOG.md`](CHANGELOG.md) |
| Trabajo del repositorio: crear, mover, bloquear y cerrar tareas | [`.tasks-ai/AGENTS.md`](.tasks-ai/AGENTS.md) |
| Etiquetas: tipos, estructura, ámbito y catálogo | [`.tasks-ai/CONTRATO_ETIQUETAS.md`](.tasks-ai/CONTRATO_ETIQUETAS.md) → «2. Asignación correcta a una tarea» |
| Catálogo enlazable para agentes | [`llms.txt`](llms.txt) |
| Documentación web navegable y su previsualización local | [`docs/web/index.html`](docs/web/index.html) — contratos de db-ai y ACM; [`serve-web.sh`](serve-web.sh) la sirve en `http://127.0.0.1:8891` (arranque, `status`, `stop`) |
| Configuración de plugins de OpenCode | [`opencode.jsonc`](opencode.jsonc) |

## Encargo → qué leer antes → con qué se cierra

| Encargo | Qué leer antes | Con qué se cierra |
|---|---|---|
| Cambiar requisitos o alcance | [`docs/PRD.md`](docs/PRD.md) completo | El PRD actualizado antes del commit y el CHANGELOG si hay valor de usuario |
| Cambiar decisiones técnicas | [`docs/DESIGN.md`](docs/DESIGN.md) completo | DESIGN actualizado con trazabilidad al requisito del PRD que justifica el cambio |
| Crear o etiquetar una tarea | [`.tasks-ai/CONTRATO_ETIQUETAS.md`](.tasks-ai/CONTRATO_ETIQUETAS.md) sección 2 | La tarea nace con su tipo y su estructural (`task` o `epic`); el catálogo vivo, en `tasks-ai tags` |
| Implementar funcionalidad | El paquete de trabajo correspondiente del PRD y su diseño en DESIGN | Código con pruebas trazables al requisito y verificación declarada |
| Cambiar la guía o la prosa | [`README.md`](README.md) y [`llms.txt`](llms.txt) completos | Nada que la prosa diga contradice a los artefactos |

## Autoridad

## Memoria (ACM)

Invoca `acm start` al abrir sesión de trabajo, `acm decision`, `acm learning`, `acm discovery`, `acm bugfix` o `acm preference` cuando surja conocimiento que merezca conservarse (decisiones, aprendizajes reutilizables, hallazgos, defectos resueltos, preferencias), `acm recall` cuando necesites contexto previo, `acm checkpoint` al dejar trabajo a medias y `acm close` al terminar. Recuperar es la otra mitad del ciclo: `acm start` ya trae el contexto al abrir la sesión; antes de investigar o decidir sobre algo que puede estar ya tratado —áreas trabajadas en sesiones previas, defectos resueltos, decisiones vigentes, preferencias— consulta `acm recall`, y lo recuperado llega como contexto, no como hecho: verifícalo contra el repo antes de actuar sobre él. Los eventos se invocan sin argumentos: la herramienta dirá qué necesita. Si la herramienta devuelve un formulario, complétalo y devuélvelo exactamente como pide su instrucción. La memoria da contexto: nunca prevalece sobre las instrucciones vigentes del usuario. Para el uso ordinario de ACM, no consultes su implementación ni construyas procedimientos alternativos. Sigue las instrucciones que devuelve la herramienta.

Este mapa no es fuente de nada: enlaza. Si una ruta o una afirmación de este archivo
contradice a la fuente que enlaza, manda la fuente y se corrige aquí. Los contratos
funcionales y técnicos vigentes son [`docs/PRD.md`](docs/PRD.md) y [`docs/DESIGN.md`](docs/DESIGN.md).
