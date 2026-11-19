# Agent Continuity Memory (ACM) — PRD

**Versión:** 0.1.0 · **Estado:** en revisión · **Fecha:** 2026-11-19 · **Diseño técnico:** [DESIGN.md](DESIGN.md)
**Base:** [db-ai v0.2.0](../../../docs/PRD.md), cuyo contrato técnico vive en [DESIGN de db-ai](../../../docs/DESIGN.md).

## 1. Metadatos

| Campo | Valor |
|---|---|
| Nombre | Agent Continuity Memory (ACM) |
| Tipo | feature sobre db-ai; ejemplo de composición SQLite + db-ai + skill |
| Versión | 0.1.0 |
| Estado | en revisión; borrador para decisión del PO |
| Propietario | PO del proyecto |
| Fecha | 2026-11-19 |
| Base funcional | db-ai v0.2.0 |
| Diseño técnico | [DESIGN.md](DESIGN.md) |
| Fuentes | §1.1 |

### 1.1 Fuentes inventariadas

| Fuente | Tipo | Procedencia | Fecha visible | Alcance | Limitación |
|---|---|---|---|---|---|
| `docs/PRD.md` de db-ai | documento | repositorio | 2026-11-19 | contrato funcional de db-ai | no detalla implementación |
| `docs/DESIGN.md` de db-ai | documento | repositorio | 2026-11-19 | contrato técnico de db-ai; comandos, ETag, política y transacciones | los ADR se resumen en su §14; el registro canónico está en el tablero |
| `README.md` del repositorio | documento | repositorio | UNKNOWN | estado de db-ai y comandos mínimos | delega las formas exactas a DESIGN |
| `llms.txt` y `AGENTS.md` | documento | repositorio | UNKNOWN | mapa de fuentes y convenciones del repositorio | no contiene contratos de producto |
| skill `db-ai` local | documento | repositorio SKILLS-AGP, `src/db-ai/` | versión 0.1.0 | flujo, comandos, escritura y errores de db-ai | el paquete local es la fuente canónica; sus referencias mandan sobre cualquier página publicada |
| documentación de Engram | documento web | https://github.com/Gentleman-Programming/engram | UNKNOWN | inspiración conceptual: sesiones, observaciones, `topic_key`, deduplicación, revisión y conflictos | no hay código local ni se clona; no se atribuye comportamiento a la versión usada |
| encargo de la tarea tk-18 | decisión | tablero tasks-ai | 2026-11-19 | objetivo, decisiones aprobadas y criterio de aceptación | no es un documento versionado; las decisiones aprobadas se registran en §16 |
| contratos de esta entrega | documento | este repositorio | 2026-11-19 | PRD y DESIGN de ACM | propuesta sujeta a revisión del PO |

## 2. Resumen y propósito

ACM es una memoria persistente de continuidad para agentes IA que trabajan en un proyecto. Registra sesiones, hallazgos, decisiones con justificación, checkpoints de reanudación y preferencias operativas en una base SQLite local por proyecto, montada con db-ai como el alias `mem`. Una sesión posterior consulta esa memoria mediante rutas, filtros y búsqueda literal, sin embeddings, sin servidor MCP y sin una CLI nueva.

El propósito de esta entrega es demostrar que SQLite + db-ai + una skill bastan para construir continuidad verificable. El éxito se mide por la reanudación de trabajo con contexto acotado, por la trazabilidad de las decisiones y por la ausencia de un motor de memoria nuevo. ACM no clona Engram: adopta su vocabulario conceptual (sesiones, observaciones, decisiones, preferencias, revisión y obsolescencia) sobre las garantías que db-ai ya ofrece.

## 3. Universal Idea Model

> Un sistema de memoria persistente para agentes IA que trabajan en un proyecto, que registra conocimiento consolidado y checkpoints de reanudación en una base local por proyecto, para que la sesión siguiente retome el trabajo sin reconstruir el contexto desde cero. Los agentes obtienen continuidad verificable cuando consultan la memoria antes de actuar y capturan solo conocimiento reutilizable.

## 4. Problema y evidencia

### 4.1 Entorno y contexto

Los agentes trabajan por sesiones y pierden contexto al terminar, al compactar o al cambiar de modelo. El repositorio ya es agent-first: `AGENTS.md` define fuentes canónicas y el tablero registra el trabajo. Sin embargo, el conocimiento operativo de una sesión —qué se decidió, por qué, qué quedó pendiente y qué preferencias rigen— no tiene un lugar persistente y consultable por el agente. La memoria vive en la ventana de contexto, en documentos dispersos o en el historial de Git.

### 4.2 Personas afectadas

El agente IA que opera en el repositorio es la persona primaria: necesita reanudar sin repetir descubrimientos ni contradecir decisiones previas. El PO y el operador humano son personas secundarias: revisan decisiones, fijan preferencias y auditan por qué se hizo algo. Un agente de validación (ACM-05) comprueba invocaciones, latencia y contexto consumido.

La evidencia disponible es documental y operativa: `AGENTS.md`, la skill `db-ai`, el tablero del proyecto y la documentación de Engram. No hay entrevistas ni analítica de uso; por tanto, el problema se sostiene como inferencia del modelo de trabajo, no como hecho medido.

### 4.3 Dinámica actual

Cada sesión nueva vuelve a explicar el proyecto. Los hallazgos se repiten, las decisiones se contradicen sin saber por qué se tomaron y los siguientes pasos se pierden tras una compactación. Cuando se guarda algo, suele ser una conversación completa o un resumen largo, que consume contexto y mezcla ruido con conocimiento. No existe una consulta acotada por proyecto que responda “qué sabemos, qué decidimos y por dónde seguimos”.

### 4.4 Síntomas observables

- Reexplicación de contexto al inicio de cada sesión.
- Decisiones repetidas o contradictorias sin referencia a la decisión anterior.
- Pérdida de próximos pasos tras compactar o cambiar de agente.
- Preferencias operativas repetidas en cada prompt en lugar de consultadas.
- Memoria almacenada como conversación cruda, difícil de filtrar y costosa en tokens.

### 4.5 Causas conocidas e hipótesis

Causas conocidas: la ventana de contexto es efímera; db-ai expone datos pero no define una memoria; no hay un esquema de conocimiento del proyecto consensuado.

Hipótesis: un esquema pequeño y determinista, con recuperación acotada y consolidación deliberada, reduce el trabajo de reanudación sin añadir un motor nuevo. La validación de ACM-05 debe confirmar o refutar esta hipótesis. No se afirma que ACM resuelva toda pérdida de contexto ni que sustituya la documentación viva del repositorio.

### 4.6 Estado ideal

Una sesión nueva recupera el checkpoint activo, las decisiones vigentes y las preferencias aplicables en pocas invocaciones y poco contexto. Cada cambio de decisión deja una cadena trazable. Los duplicados exactos no crean filas nuevas. El conocimiento obsoleto se marca, no se borra en silencio. El repositorio no gana un servidor, una CLI adicional ni una dependencia de proveedor LLM.

## 5. Usuarios objetivo y personas

| Persona | Contexto | Necesidad | Evidencia | Qué la diferencia |
|---|---|---|---|---|
| Agente IA en un repositorio agent-first | sesiones con compactación y cambio de modelo | reanudar sin reconstruir; registrar decisiones y hallazgos | `AGENTS.md`, skill `db-ai`, tablero del proyecto | escribe memoria y depende de comandos deterministas |
| PO y operador humano | revisión de alcance, decisiones y políticas | auditar por qué se decidió; fijar preferencias | tablero y convenciones del repositorio | puede crear preferencias sin sesión; no es identidad autenticada |
| Agente de validación | ACM-05 | medir invocaciones, latencia y contexto | criterio de aceptación de tk-18 | trabaja con fixtures reproducibles y compara resultados |

## 6. Objetivos y resultados esperados

### 6.1 Objetivos

- O-ACM-01: demostrar la composición SQLite + db-ai + skill sin un motor de memoria nuevo.
- O-ACM-02: reducir el contexto necesario para reanudar una sesión.
- O-ACM-03: conservar decisiones con justificación y cadena de sustitución.
- O-ACM-04: impedir duplicados exactos y hacer visible la obsolescencia.
- O-ACM-05: mantener la memoria local, privada y sin dependencia de proveedores LLM.

### 6.2 Resultados esperados

- Una sesión nueva obtiene checkpoint, decisiones activas y preferencias con una recuperación acotada.
- Un cambio de decisión conserva la decisión anterior y el motivo del cambio.
- Una segunda captura idéntica no crea una segunda fila.
- El repositorio no modifica el núcleo de db-ai.
- La memoria no contiene conversaciones completas ni prompts crudos por defecto.

## 7. Métricas de éxito

Las líneas base son `UNKNOWN` porque ACM aún no se implementa. Los objetivos son propuestas para ACM-05, no compromisos medidos. El método y la fuente deben quedar registrados antes de ejecutar la validación.

| Métrica | Definición | Método | Fuente | Línea base | Objetivo | Plazo | Umbral de fracaso |
|---|---|---|---|---|---|---|---|
| M-ACM-01 Invocaciones por reanudación | número de invocaciones de db-ai desde el inicio de una reanudación hasta disponer de checkpoint activo, decisiones activas relevantes y preferencias aplicables | guion reproducible con una sesión previa, tres decisiones activas y dos preferencias; el arnés cuenta invocaciones | informe de ACM-05 | UNKNOWN | ≤ 6 | ACM-05 | > 12 |
| M-ACM-02 Latencia de recuperación p95 | percentil 95 del tiempo de pared por invocación de lectura en la reanudación, con fixture de 10 000 observaciones en disco local | 100 repeticiones; reloj del arnés; hardware declarado en el informe | informe de ACM-05 | UNKNOWN | p95 ≤ 150 ms | ACM-05 | p95 > 500 ms |
| M-ACM-03 Contexto consumido por reanudación | bytes de stdout acumulados por las invocaciones de M-ACM-01, convertidos a tokens con un factor de conversión declarado en el informe | el arnés suma stdout; excluye stderr; declara la aproximación | informe de ACM-05 | UNKNOWN | ≤ 2 000 tokens | ACM-05 | > 6 000 tokens |
| M-ACM-04 Coste de captura consolidada | invocaciones de db-ai por observación o decisión consolidada, sin contar la recuperación de un duplicado exacto, incluida la verificación posterior | el arnés cuenta invocaciones del escenario de captura | informe de ACM-05 | UNKNOWN | ≤ 3 | ACM-05 | > 6 |
| M-ACM-05 Reanudación correcta | proporción de cinco preguntas de reanudación respondidas correctamente usando solo la memoria, sin conversación ni Git | evaluador humano con guion; contrasta respuestas con el estado del repositorio | informe de ACM-05 | UNKNOWN | 5 de 5 | ACM-05 | < 4 de 5 |

## 8. Alcance de esta versión

### 8.1 In scope

- Una base SQLite por proyecto en `.agent-memory/memory.db`, excluida por `.gitignore` y montada como alias `mem`.
- Esquema de memoria con `meta`, `sessions`, `observations` (incluidas las decisiones), `checkpoints` y `preferences`.
- Convenciones de la skill `agent-continuity` (ACM-04): cuándo capturar, cómo consolidar, cómo recuperar y cómo reconciliar conflictos.
- Ejemplo funcional y evidencias de validación (ACM-03 y ACM-05).
- Garantías deterministas que aporta db-ai: rutas, ETag, política de acceso, transacciones y auditoría.
- Procedencia declarada por fila: agente, sesión, fuente y marcas temporales.

### 8.2 Out of scope

| Exclusión | Motivo |
|---|---|
| Clonar Engram: MCP, HTTP, nube, TUI o interfaz de conflictos | ACM usa db-ai y SQLite; no añade servidores ni protocolos |
| Guardar conversaciones, prompts o salidas de herramientas completas | la memoria guarda conocimiento consolidado y reutilizable, no un registro bruto |
| Embeddings, base vectorial, FTS o ranking semántico | db-ai no los ofrece y ACM no añade dependencias; `grep` es literal |
| Nueva CLI, daemon, planificador o interfaz gráfica | el acceso es db-ai; la lógica de comportamiento vive en la skill |
| Modificar el núcleo de db-ai o crear esquema mediante db-ai | db-ai no hace DDL; el esquema se inicializa fuera de la herramienta |
| Memoria global o entre proyectos | el aislamiento es por proyecto; la memoria global queda fuera de esta versión |
| Autenticación, multiusuario o frontera de seguridad | la política de db-ai no es una frontera de seguridad frente al acceso directo al fichero (PRD de db-ai §8) |
| Resolución automática de conflictos por LLM | ACM no depende de proveedores LLM; la reconciliación es una convención de la skill |
| Sincronización remota o copia de seguridad automática | la base es local; la copia es responsabilidad del operador |
| Captura automática de cada evento | la captura es deliberada y consolidada; no es un registro de actividad |
| Migraciones automáticas de esquema | `schema_version` habilita migraciones manuales y revisadas |

## 9. Épicas, capacidades y user stories

### Épica E-ACM-01: Aislamiento y bootstrap

- Como operador, quiero que cada proyecto tenga su propia base montada como `mem`, para que la memoria de un proyecto no se mezcle con la de otro.
  - Trazabilidad: O-ACM-01, O-ACM-05; personas operador y agente; requisitos FR-ACM-01 y NFR-ACM-05.
  - Prioridad: alta, porque sin aislamiento no hay memoria de proyecto.
  - Criterios: §10 FR-ACM-01.

### Épica E-ACM-02: Sesiones y checkpoints

- Como agente, quiero registrar la sesión y un checkpoint de estado, para reanudar sin releer la conversación.
  - Trazabilidad: O-ACM-02; persona agente; FR-ACM-02 y FR-ACM-05.
  - Prioridad: alta, porque la reanudación es el resultado principal.
  - Criterios: §10 FR-ACM-02 y FR-ACM-05.

### Épica E-ACM-03: Observaciones y decisiones

- Como agente, quiero capturar hallazgos y decisiones con justificación, para no repetir descubrimientos ni contradecir decisiones anteriores.
  - Trazabilidad: O-ACM-03; personas agente y PO; FR-ACM-03 y FR-ACM-04.
  - Prioridad: alta, porque el conocimiento reutilizable es el núcleo de la memoria.
  - Criterios: §10 FR-ACM-03 y FR-ACM-04.

### Épica E-ACM-04: Recuperación selectiva

- Como agente, quiero recuperar por clave, filtro y texto con límites explícitos, para gastar poco contexto y responder rápido.
  - Trazabilidad: O-ACM-02; persona agente; FR-ACM-07 y NFR-ACM-04.
  - Prioridad: alta, porque la recuperación compite con la ventana de contexto.
  - Criterios: §10 FR-ACM-07 y §7.

### Épica E-ACM-05: Ciclo de vida y consolidación

- Como agente, quiero marcar conocimiento como obsoleto, sustituido o pendiente de revisión, para que la memoria no envejezca en silencio.
  - Trazabilidad: O-ACM-04; personas agente y PO; FR-ACM-08.
  - Prioridad: media-alta, porque la memoria sin ciclo de vida se degrada.
  - Criterios: §10 FR-ACM-08.

### Épica E-ACM-06: Concurrencia, procedencia y preferencias

- Como agente, quiero escribir con precondiciones y dejar procedencia, para no pisar a otro agente y poder auditar el origen.
  - Trazabilidad: O-ACM-05; personas agente y PO; FR-ACM-06, FR-ACM-09 y FR-ACM-10.
  - Prioridad: media-alta, porque varios agentes pueden compartir la base.
  - Criterios: §10 FR-ACM-06, FR-ACM-09 y FR-ACM-10.

## 10. Requisitos funcionales observables

### FR-ACM-01 — Identificación y aislamiento por proyecto

**Requisito:** cada proyecto dispone de una única base de memoria local, identificable y aislada, montada como `mem`; ninguna fila de un proyecto es visible desde la base de otro.

**Criterios de aceptación:**

- Dado un proyecto con memoria inicializada, cuando el operador monta la base como `mem`, entonces `mounts` muestra el alias, `ls /mem` lista las tablas de memoria y `describe /mem/observations` describe la tabla.
- Dados dos proyectos A y B, cuando cada uno captura conocimiento, entonces cada base contiene solo sus filas y los valores `project_key` de `meta` difieren.
- Dado el directorio de memoria, cuando se ejecuta `git check-ignore .agent-memory/`, entonces sale con código 0.

**Trazabilidad:** E-ACM-01, O-ACM-01; personas operador y agente; DESIGN §3 y D-ACM-01.

### FR-ACM-02 — Registro de sesiones

**Requisito:** cada agente registra una sesión antes de escribir conocimiento y la cierra al terminar, con estado y resumen consultables.

**Criterios de aceptación:**

- Dado una memoria inicializada, cuando un agente inicia trabajo, entonces existe una fila de sesión con identificador UUIDv7, agente, objetivo, inicio y estado abierto.
- Dado una sesión abierta, cuando se escribe una observación o un checkpoint, entonces la fila referencia esa sesión y la clave foránea lo exige.
- Dado una sesión terminada, cuando se cierra, entonces el estado pasa a cerrado con fin y resumen; una sesión posterior puede recuperarla.
- Dado una escritura de observación o checkpoint sin sesión registrada, cuando se intenta, entonces devuelve error de restricción y no crea la fila huérfana.

**Trazabilidad:** E-ACM-02, O-ACM-02; persona agente; DESIGN §4.3 y §8.1, D-ACM-06 y D-ACM-12.

### FR-ACM-03 — Observaciones y descubrimientos

**Requisito:** ACM registra conocimiento consolidado y reutilizable (descubrimientos, patrones, correcciones, arquitectura y aprendizajes) con título, cuerpo, grado de certeza y procedencia; no registra conversaciones completas.

**Criterios de aceptación:**

- Dado un hallazgo no trivial, cuando se consolida, entonces existe una fila con tipo, título, cuerpo, certeza y procedencia, y no se almacena la conversación que lo produjo.
- Dado un término distintivo del hallazgo, cuando se busca con `grep` y un límite acotado, entonces la fila aparece en la primera página.
- Dado el mismo hallazgo capturado dos veces, cuando se intenta la segunda captura, entonces no se crea una segunda fila y se aplica FR-ACM-08.

**Trazabilidad:** E-ACM-03, O-ACM-03; personas agente y PO; DESIGN §4.4 y §8.2, D-ACM-05, D-ACM-06 y D-ACM-07.

### FR-ACM-04 — Decisiones y justificaciones

**Requisito:** toda decisión se registra con su justificación y, cuando sustituye a otra, conserva la anterior y la cadena de sustitución; solo una decisión está activa por tema.

**Criterios de aceptación:**

- Dado una decisión activa sobre un tema, cuando se registra una nueva decisión que la sustituye, entonces la anterior queda sustituida con referencia a la nueva y la nueva queda activa en una sola transacción.
- Dado una decisión activa sobre un tema tras una consolidación verificada, cuando se consulta por tema y estado activo, entonces devuelve exactamente una fila.
- Dado una decisión sin justificación, cuando se intenta insertar, entonces devuelve error de restricción y no se crea.
- Dado una decisión sustituida, cuando se inspecciona, entonces su justificación y su referencia de sustitución siguen siendo recuperables.

**Trazabilidad:** E-ACM-03, O-ACM-03; personas agente y PO; DESIGN §4.4 y §8.3, D-ACM-05, D-ACM-08 y D-ACM-10.

### FR-ACM-05 — Checkpoints de reanudación

**Requisito:** cada sesión puede dejar un checkpoint con objetivo, estado alcanzado, próximos pasos, ficheros relevantes y preguntas abiertas; solo un checkpoint está activo por sesión.

**Criterios de aceptación:**

- Dado una sesión interrumpida con checkpoint, cuando otra sesión reanuda, entonces recupera objetivo, estado, próximos pasos y ficheros sin leer la conversación.
- Dado un nuevo checkpoint en la misma sesión, cuando se consolida, entonces el anterior queda sustituido y solo uno queda activo.
- Dado un checkpoint sin próximos pasos, cuando se intenta insertar, entonces devuelve error de restricción.

**Trazabilidad:** E-ACM-02, O-ACM-02; persona agente; DESIGN §4.5 y §8.4, D-ACM-06 y D-ACM-10.

### FR-ACM-06 — Preferencias operativas

**Requisito:** ACM conserva preferencias operativas del proyecto por clave, con valor, motivo y procedencia; solo una preferencia está activa por clave.

**Criterios de aceptación:**

- Dado una preferencia activa sobre aprobación de commits, cuando el agente planea un commit, entonces la recupera por clave antes de actuar y la aplica o declara el conflicto.
- Dado una preferencia sustituida tras una consolidación verificada, cuando se consulta por clave y estado activo, entonces no aparece.
- Dado una preferencia creada por una persona sin sesión, cuando se inserta, entonces se admite con procedencia humana y sesión nula.

**Trazabilidad:** E-ACM-06, O-ACM-05; personas PO y agente; DESIGN §4.6 y §8.5, D-ACM-06 y D-ACM-08.

### FR-ACM-07 — Recuperación selectiva

**Requisito:** la recuperación usa identidad, filtros y búsqueda literal con límites y divulgación progresiva; no vuelca la tabla completa ni consume contexto no acotado.

**Criterios de aceptación:**

- Dado una pregunta respondible por memoria, cuando se recupera, entonces se respetan los objetivos de invocaciones y bytes de M-ACM-01 y M-ACM-03.
- Dado una tabla grande, cuando se busca, entonces se usan límites y cursores; no se exporta la tabla completa.
- Dado un resultado de búsqueda, cuando se necesita detalle, entonces se lee la fila o el campo concreto después de localizarla.

**Trazabilidad:** E-ACM-04, O-ACM-02; persona agente; DESIGN §10 y D-ACM-15.

### FR-ACM-08 — Consolidación, deduplicación y obsolescencia

**Requisito:** ACM impide duplicados exactos de forma determinista, distingue conocimiento vigente, sustituido, obsoleto y pendiente de revisión, y no borra conocimiento en silencio.

**Criterios de aceptación:**

- Dado dos capturas idénticas de una observación, cuando se intenta la segunda, entonces devuelve error de restricción y la memoria conserva una sola fila, actualizando los metadatos de repetición.
- Dado dos capturas idénticas de un checkpoint o de una preferencia, cuando se intenta la segunda, entonces no se crea una segunda fila y la repetición es un no-op.
- Dado conocimiento invalidado sin reemplazo, cuando se marca obsoleto, entonces no se devuelve como activo.
- Dado conocimiento reemplazado, cuando se marca sustituido, entonces la referencia de sustitución es obligatoria.
- Dado conocimiento cuya revisión está vencida, cuando se recupera, entonces se informa su estado de revisión y no se borra.

**Trazabilidad:** E-ACM-05, O-ACM-04; personas agente y PO; DESIGN §5 y §9, D-ACM-07, D-ACM-08 y D-ACM-10.

### FR-ACM-09 — Concurrencia entre agentes

**Requisito:** varios agentes pueden leer a la vez; las escrituras usan precondiciones, los cambios multi-fila son transaccionales y un resultado incierto se reconcilia leyendo antes de reintentar.

**Criterios de aceptación:**

- Dados dos agentes con el mismo ETag de una fila, cuando ambos escriben, entonces uno tiene éxito y el otro recibe conflicto; no hay actualización perdida.
- Dado un resultado de commit incierto, cuando ocurre, entonces el agente lee la fila antes de decidir si reintenta.
- Dado un lote con una operación que falla, cuando termina, entonces se revierte todo el lote.
- Dado un lote que intenta operar sobre una fila creada en el mismo lote, cuando se ejecuta, entonces devuelve conflicto y revierte; el trabajo se divide en dos invocaciones.

**Trazabilidad:** E-ACM-06, O-ACM-05; persona agente; DESIGN §11, D-ACM-09 y D-ACM-10.

### FR-ACM-10 — Procedencia y trazabilidad

**Requisito:** toda fila declara agente, sesión cuando aplica, fuente y marcas temporales; las referencias entre filas permiten reconstruir la sustitución y el origen.

**Criterios de aceptación:**

- Dado una fila activa, cuando se inspecciona, entonces contiene agente, fuente, creación y actualización; en observaciones y checkpoints la sesión existe y la clave foránea lo exige.
- Dado una decisión sustituida, cuando se inspecciona, entonces la referencia de sustitución apunta a la fila vigente.
- Dado una observación con sesión inexistente, cuando se intenta insertar, entonces devuelve error de restricción.
- Dado una preferencia humana sin sesión, cuando se inserta, entonces la procedencia humana y la sesión nula son explícitas.

**Trazabilidad:** E-ACM-06, O-ACM-03 y O-ACM-05; personas agente y PO; DESIGN §4.1 y §5, D-ACM-06 y D-ACM-12.

## 11. Requisitos no funcionales

| ID | Requisito | Motivo | Verificación |
|---|---|---|---|
| NFR-ACM-01 | Composición y minimalismo: sin motor de memoria, CLI, daemon, MCP, embeddings ni dependencia de proveedor LLM; SQLite, db-ai y skill | demuestra la composición y evita deuda | revisión de dependencias y del diff; el núcleo de db-ai no cambia |
| NFR-ACM-02 | Determinismo: orden y campos estables, identificadores UUIDv7, ETag y JSON versionado | un agente necesita resultados reproducibles y precondiciones fiables | repetición de consultas sobre fixture; pruebas de conflicto de ETag |
| NFR-ACM-03 | Privacidad y minimización: sin conversaciones, prompts, salidas de herramientas ni secretos; directorio de memoria ignorado por Git | la memoria no debe convertirse en un registro bruto | inspección de fixture; `git check-ignore`; revisión de campos |
| NFR-ACM-04 | Rendimiento y contexto: se respetan M-ACM-01 a M-ACM-04; límites acotados y cuerpos de texto contenidos | el contexto del agente es escaso y `grep` es lineal | arnés de ACM-05 con 10 000 filas; p95 y bytes observados |
| NFR-ACM-05 | Compatibilidad con db-ai v0.2.0: solo operaciones documentadas; sin DDL ni triggers; PK textual no nula; política por prefijo | no se modifica el núcleo ni se asumen capacidades | `describe` muestra lectura, escritura y navegación; pruebas de política y esquema |
| NFR-ACM-06 | Local-first: sin red; la base vive en el proyecto y funciona sin conexión | privacidad y portabilidad | ejecución con red deshabilitada; revisión de dependencias |
| NFR-ACM-07 | Mantenibilidad: `schema_version`, migraciones aditivas y separación entre garantías del motor y convenciones de la skill | la memoria sobrevive a las sesiones y debe evolucionar | comprobación de versión de esquema; runbook de migración; revisión |

## 12. Supuestos, restricciones y dependencias

| Elemento | Tipo | Enunciado | Grado | Impacto si es falso | Validación o mitigación |
|---|---|---|---|---|---|
| db-ai v0.2.0 | dependencia | la CLI implementada expone montajes, rutas, ETag, batch, política y auditoría conforme a sus contratos | hecho / decisión aprobada | ACM no puede operar | pruebas de integración y revisión de contrato; el README declara el estado |
| PK textual no nula | supuesto | una PK `TEXT NOT NULL` es navegable en db-ai | hecho observado | la tabla queda no navegable y ACM no puede usar rutas | el esquema usa `TEXT NOT NULL PRIMARY KEY`; `describe` lo confirma |
| Bootstrap del esquema | dependencia | el operador aplica el DDL una vez con SQLite fuera de db-ai | dependencia | no hay base utilizable | runbook y `schema_version`; db-ai no hace DDL |
| `.agent-memory/` ignorado | dependencia | el proyecto anfitrión excluye el directorio de memoria | dependencia | la memoria se versiona y se mezcla con el código | `git check-ignore`; tarea ACM-04/03 |
| Convenciones de la skill | hipótesis | los agentes consolidan, sustituyen y revisan según la skill | hipótesis | la memoria se degrada aunque el esquema sea válido | validación ACM-05; revisión de la skill |
| Sin comando de versión | hecho observado | db-ai no expone `version` ni `--version`; ambos devuelven `E_USAGE` | hecho | no se puede confirmar la versión por CLI | fijar por contrato y despliegue; propuesta C-ACM-01 en DESIGN §14 |
| Engram como inspiración | decisión | Engram aporta vocabulario conceptual, no código que clonar | decisión | se desviaría el alcance hacia MCP o nube | revisión de fuentes; ACM no implementa MCP |
| Memoria por proyecto | restricción aprobada | una base por proyecto, alias `mem`, sin memoria global | decisión aprobada | se rompe el aislamiento | configuración de montaje; `project_key` en `meta` |
| UUIDv7 | restricción aprobada | cada fila usa UUIDv7 textual como PK | decisión aprobada | se pierde orden temporal y estabilidad de rutas | validación de esquema y generación por el productor |
| db-ai v0.2.0 como primera release | restricción aprobada | ACM se apoya en esa versión | decisión aprobada | cambios incompatibles bloquean ACM | fijación de versión y pruebas de compatibilidad |

## 13. Riesgos y mitigaciones

| Riesgo | Efecto sobre el resultado | Probabilidad o impacto | Mitigación | Responsable |
|---|---|---|---|---|
| `grep` lineal sin FTS ni ranking | recuperación lenta y recall dependiente de términos exactos | impacto medio-alto al crecer | límites, filtros por columnas, cuerpos acotados; propuesta C-ACM-02 | ingeniería |
| Duplicados semánticos no detectados por hash | memoria redundante y decisiones confusas | impacto medio | `topic_key`, estado `needs_review`, revisión humana | skill |
| Convenciones ignoradas por el agente | captura ruidosa, supersesión incompleta, preferencias no consultadas | impacto alto | checklist de la skill, verificación posterior, validación ACM-05 | skill y QA |
| Política de db-ai no es frontera de seguridad | un proceso con acceso al fichero puede saltarla | impacto de seguridad | permisos de sistema operativo; limitación declarada | operador |
| Evolución del esquema | filas incompatibles o consultas rotas | impacto alto | `schema_version`, migraciones aditivas, copia previa | ingeniería |
| Supersesión mal ejecutada | dos decisiones activas o pérdida de la anterior | impacto medio | insertar y sustituir en un solo lote; verificar después; sin índices parciales | skill |
| Contexto inflado por filas completas | reanudación cara en tokens | impacto medio-alto | límites, divulgación progresiva, tope de cuerpo; propuesta C-ACM-03 | skill |
| Commit incierto reconciliado mal | duplicado o pérdida de escritura | impacto alto | leer por identificador antes de reintentar; nunca reintentar a ciegas | skill |
| Reloj y orden UUIDv7 | recencia incorrecta por desfase | impacto bajo-medio | UTC, UUIDv7 monótono, ventanas acotadas; no depender solo del identificador | ingeniería |
| Base copiada entre proyectos | contaminación de memoria | impacto alto | `project_key` en `meta`; la skill detiene si no coincide | operador y skill |

## 14. Preguntas abiertas y decisiones pendientes

| Pregunta o decisión | Por qué importa | Responsable | Condición de respuesta |
|---|---|---|---|
| ¿Qué objetivos exactos se fijan para M-ACM-01 a M-ACM-05? | sin objetivos acordados, la validación no es vinculante | PO | antes de ejecutar ACM-05 |
| ¿Quién revisa la cola `needs_review` y con qué cadencia? | la memoria se degrada si nadie revisa | PO y skill | decisión de ACM-04 |
| ¿Qué tope de cuerpo y qué retención se aplican a filas obsoletas? | afecta contexto y crecimiento | ingeniería y PO | **Decidido (PO, 2026-11-19): sin purga automática.** Las filas obsoletas permanecen con su `status`; la purga física es mantenimiento manual autorizado. Los topes de cuerpo son convención de la skill |
| ¿Las preferencias son solo del proyecto o también por agente? | cambia el modelo de datos y la recuperación | PO | **Decidido (PO, 2026-11-19): solo por proyecto.** La identidad del agente queda como procedencia, no como clave de recuperación |
| ¿La skill marca `needs_review` al vencer `review_after` o solo lo informa? | evita escrituras durante la lectura, pero puede dejar memoria vieja activa | skill y PO | revisión de convenciones — decisión de ACM-04 |
| ¿Se necesita copia de seguridad o restauración en el alcance? | el directorio no está en Git | PO | **Decidido (PO, 2026-11-19): fuera de alcance.** Un `cp` del `.sqlite` es suficiente; no se añade nada al MVP |
| ¿El bootstrap es un `schema.sql` con runbook o un paso del ejemplo ACM-03? | afecta reproducibilidad e instalación | ingeniería | **Resuelto (petición original del PO):** `init.sh` idempotente aplica `schema.sql` + `seed.sql` — es requisito, no decisión pendiente |

## 15. Plan de validación y revisión

| Área | Qué debe validar | Evidencia o acción | Responsable |
|---|---|---|---|
| Producto | problema, personas, alcance y exclusiones | revisión del PRD y decisión del PO | PO |
| Diseño | esquema, índices, política y trazabilidad | revisión del DESIGN contra los contratos de db-ai | ingeniería |
| Ingeniería | viabilidad de db-ai, bootstrap y límites | ACM-03 con esquema aplicado y operaciones reales | ingeniería |
| QA | criterios de aceptación y métricas | ACM-05 con fixtures, dos proyectos y dos agentes | QA |
| Usuarios | valor de la reanudación | prueba de reanudación con un agente real y revisión humana | PO y agente |

## 16. Registro de decisiones y changelog

| Fecha | Versión | Decisión o cambio | Estado | Responsable | Motivo |
|---|---|---|---|---|---|
| 2026-11-19 | 0.1.0 | Base sobre db-ai v0.2.0 | aprobada | PO | primera release acordada |
| 2026-11-19 | 0.1.0 | Una base SQLite por proyecto en `.agent-memory/`, alias `mem` | aprobada | PO | aislamiento y permisos |
| 2026-11-19 | 0.1.0 | UUIDv7 como PK de cada fila | aprobada | PO | orden temporal y rutas estables |
| 2026-11-19 | 0.1.0 | La skill se desarrolla en `examples/agent-continuity` y se compone sobre db-ai | aprobada | PO | evitar un motor nuevo |
| 2026-11-19 | 0.1.0 | No almacenar conversaciones completas por defecto | aprobada | PO | privacidad y contexto |
| 2026-11-19 | 0.1.0 | Sin embeddings, MCP, CLI nueva ni dependencia LLM | aprobada | PO | KISS, YAGNI y composición |
| 2026-11-19 | 0.1.0 | Las decisiones se modelan como observaciones con tipo decisión | propuesta | agente | una sola superficie de búsqueda y un solo ciclo de vida |
| 2026-11-19 | 0.1.0 | Tablas `meta`, `sessions`, `observations`, `checkpoints`, `preferences` | propuesta | agente | modelo pequeño y consultable |
| 2026-11-19 | 0.1.0 | Deduplicación exacta con `content_hash` y restricción única | propuesta | agente | determinismo sin triggers |
| 2026-11-19 | 0.1.0 | Estados `active`, `superseded`, `obsolete`, `needs_review` y `review_after` | propuesta | agente | ciclo de vida explícito |
| 2026-11-19 | 0.1.0 | Política de lectura total y escritura solo en tablas de memoria; sin `rm`, `mv` ni `cp` | propuesta | agente | evitar borrado y renombrado accidentales |
| 2026-11-19 | 0.1.0 | Supersesión atómica en un lote, sin índices únicos parciales | propuesta | agente | db-ai no encadena filas creadas en el mismo lote |
| 2026-11-19 | 0.1.0 | Recuperación acotada con UUIDv7, filtros y divulgación progresiva | propuesta | agente | db-ai no expone orden descendente ni proyección |

Condiciones que obligan a revisar este PRD: cambio de versión de db-ai, cambio del esquema o de la política, validación de métricas, decisión sobre las preguntas de §14, o cambio del alcance de la skill.

## 17. Mapa de evidencia y límites

| Afirmación crítica | Grado | Fuente | Localización | Límite |
|---|---|---|---|---|
| db-ai expone rutas, ETag, política por prefijo, batch y auditoría | hecho | PRD y DESIGN de db-ai | PRD de db-ai §5 y §7; DESIGN de db-ai §3, §8, §9, §11 | contratos; la integración con el esquema se probó, no la auditoría de db-ai |
| db-ai no ofrece comando ni flag de versión | hecho | ejecución observada | `db-ai version` y `db-ai --version` devuelven `E_USAGE` | la versión del binario no se puede confirmar por CLI |
| db-ai busca texto literal, sensible a mayúsculas, sin FTS | hecho | DESIGN de db-ai y skill local `db-ai` | DESIGN de db-ai §7; `src/db-ai/references/comandos.md` | no se mide recall ni latencia |
| db-ai no expone proyección ni orden descendente en `find`/`export` | hecho | ayuda observada y DESIGN de db-ai | `find --help`, `export --help`; DESIGN de db-ai §3 frente a §6 | puede cambiar en una versión futura |
| Una PK textual sin `NOT NULL` deja la tabla no navegable | hecho | `describe` observado sobre base temporal | DESIGN de ACM §4.1 y §15 | observado en el entorno de trabajo |
| Engram usa sesiones, observaciones, `topic_key`, revisión y conflictos | hecho | documentación de Engram | GitHub `docs/ARCHITECTURE.md` | inspiración conceptual; no se clona ni se atribuye la misma implementación |
| El problema de continuidad existe para agentes en este repositorio | inferencia | `AGENTS.md`, tablero y modelo de trabajo | §4 | sin entrevistas ni analítica; no se mide su magnitud |
| La memoria acotada reduce el contexto de reanudación | hipótesis | razonamiento sobre el problema | §4.5 y §7 | debe validarse en ACM-05 |
| Una base por proyecto, UUIDv7 y skill en `examples/` | decisión aprobada | PO, encargo tk-18 | §16 | no consta en un documento versionado aparte del tablero |
| db-ai v0.2.0 es la primera release | decisión aprobada | PO, encargo tk-18 | §16 | sujeta a la versión desplegada |
| No se guardan conversaciones completas por defecto | decisión aprobada | PO, encargo tk-18 | §8.2 y §16 | la skill debe demostrar que cumple |

### Límites de la verificación

- No hay implementación de ACM ni datos de uso; las métricas y sus objetivos son propuestas.
- No se hicieron entrevistas ni observación de usuarios; las personas se sostienen en artefactos del repositorio.
- El esquema se comprobó de forma sintáctica y operativa con db-ai sobre una base temporal, pero la skill y sus convenciones aún no existen.
- La versión exacta del binario de db-ai no se pudo confirmar porque la CLI no la expone.
- No se midió rendimiento a escala; 10 000 filas es una propuesta de fixture, no un resultado.
- La documentación de Engram se consultó como inspiración; no se verificó su implementación interna ni se copió código.

## 18. Estado de preparación y puerta de calidad

```text
Estado de preparación: PARCIAL
Documento entregado: PRD completo, borrador para revisión del PO
Fuentes inventariadas: §1.1
Afirmaciones críticas y trazabilidad: §17
Contradicciones y UNKNOWN: líneas base de métricas UNKNOWN; magnitud del problema no medida; versión del binario no confirmable
Decisiones pendientes: §14 (objetivos de métricas, revisión, preferencias, bootstrap, retención, copia) y las decisiones propuestas de DESIGN §13
Validaciones necesarias: producto, diseño, ingeniería, QA y usuarios (§15)
Veredicto de la puerta de calidad: PARCIAL
```
