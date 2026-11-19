-- Agent Continuity Memory — datos de demostración.
-- Fuente del modelo: docs/DESIGN.md §3.2, §4 y §5. Los UUIDv7 y los
-- content_hash son fijos: el hash sigue el algoritmo de DESIGN §5.2 sobre los
-- campos semánticos tal como aparecen aquí, incluida la normalización de
-- espacios. Al editar un campo semántico de una fila, recalcula su hash; el
-- UNIQUE(content_hash) es una garantía del esquema y no admite excusas.
--
-- Contenido: dos sesiones (una cerrada con summary y otra abierta), seis
-- observaciones con una decisión sustituida y una fila needs_review, un
-- checkpoint de la sesión cerrada y tres preferencias del proyecto. La
-- preferencia commit/approval nace de una persona sin sesión (session_id NULL).
--
-- El project_key de meta es un marcador de demostración: init.sh genera uno
-- nuevo por base al aplicar el seed, para que dos proyectos inicializados con
-- este fixture no compartan identidad. Aplicar seed.sql a mano deja el marcador.

INSERT INTO meta (id, name, value, updated_at) VALUES
  ('01a1f308-c280-7a1b-82c3-d4e5f6a7b8c9', 'schema_version', '1', '2026-11-19T09:00:00Z'),
  ('01a1f308-c280-7b1c-82d3-e4f5a6b7c8d9', 'project_key', '01a1f308-c280-7b1c-82d3-e4f5a6b7c8d9', '2026-11-19T09:00:00Z'),
  ('01a1f308-c280-7c1d-82e3-f4a5b6c7d8e9', 'project_name', 'agent-continuity-example', '2026-11-19T09:00:00Z'),
  ('01a1f308-c280-7d1e-82f3-a4b5c6d7e8f9', 'created_at', '2026-11-19T09:00:00Z', '2026-11-19T09:00:00Z');

INSERT INTO sessions (id, agent_id, source, goal, status, started_at, ended_at, summary, created_at, updated_at) VALUES
  ('01a1f313-bf00-70a1-8b2c-3d4e5f6a7b8c', 'claude-code', 'agent',
   'Implementar el ejemplo ACM-03 sobre db-ai', 'closed',
   '2026-11-19T09:12:00Z', '2026-11-19T11:40:00Z',
   'Bootstrap idempotente, esquema aplicado y flujo de reanudación documentado; queda la skill agent-continuity (ACM-04).',
   '2026-11-19T09:12:00Z', '2026-11-19T11:40:00Z'),
  ('01a1f41f-fee0-71a2-8b3c-4d5e6f7a8b9c', 'opencode', 'agent',
   'Reanudar ACM-03 y validar la continuidad entre agentes', 'open',
   '2026-11-19T14:05:00Z', NULL, NULL,
   '2026-11-19T14:05:00Z', '2026-11-19T14:05:00Z');

INSERT INTO observations (id, session_id, agent_id, source, kind, title, body, rationale, alternatives, consequences, topic_key, confidence, status, review_after, superseded_by, duplicate_count, last_seen_at, content_hash, origin_ref, created_at, updated_at) VALUES
  ('01a1f31f-a5e0-72a3-8b4c-5d6e7f8a9bac', '01a1f313-bf00-70a1-8b2c-3d4e5f6a7b8c', 'claude-code', 'agent', 'discovery',
   'db-ai mount --mode rw exige una política explícita',
   'La CLI rechaza con E_DENIED un montaje en rw sin política; el operador registra el alias mem en config.json con allow por prefijo. Una sesión que necesite escribir debe verificar mounts antes de la primera mutación.',
   NULL, NULL, NULL, NULL, 'confirmed', 'active', '2027-11-19T00:00:00Z', NULL, 1, '2026-11-19T09:25:00Z',
   'sha256:29def3e8414db7cbc7dbf3170a15216ada1d306408f9353bd61d33d2e70801b5',
   'docs/DESIGN.md §9', '2026-11-19T09:25:00Z', '2026-11-19T09:25:00Z'),
  ('01a1f328-cda0-73a4-8b5c-6d7e8f9a0bac', '01a1f313-bf00-70a1-8b2c-3d4e5f6a7b8c', 'claude-code', 'agent', 'architecture',
   'Una base SQLite por proyecto montada como alias mem',
   'Cada proyecto guarda su memoria en .agent-memory/memory.db y la monta como mem. El aislamiento se comprueba con project_key en meta; una base copiada de otro proyecto no se escribe.',
   NULL, NULL, NULL, NULL, 'confirmed', 'active', '2027-11-19T00:00:00Z', NULL, 1, '2026-11-19T09:35:00Z',
   'sha256:c66ce899a5cfe8030d83e245ef57d3839b33847619eee9a0140f3fa72236df24',
   'docs/DESIGN.md §3', '2026-11-19T09:35:00Z', '2026-11-19T09:35:00Z'),
  ('01a1f344-44e0-75a6-8b7c-8d9e0f1a2bac', '01a1f313-bf00-70a1-8b2c-3d4e5f6a7b8c', 'claude-code', 'agent', 'decision',
   'Una base por proyecto para conservar la continuidad',
   'La memoria vive en una sola base por proyecto y la sesión es una fila, no un fichero.',
   'Una base por proyecto conserva la continuidad y el aislamiento sin sincronización entre ficheros.',
   '["db por sesión","base global"]',
   '["La contención se resuelve con ETag y lotes","El operador monta el alias una vez"]',
   'storage/db-per-project', 'confirmed', 'active', '2027-11-19T00:00:00Z', NULL, 1, '2026-11-19T10:05:00Z',
   'sha256:d9bb2cdc4230e7ab18c2dae7064bcfe1a3a7bb2066a91525c3aa2e09c1e04486',
   'docs/DESIGN.md D-ACM-01', '2026-11-19T10:05:00Z', '2026-11-19T10:05:00Z'),
  ('01a1f32d-6180-74a5-8b6c-7d8e9f0a1bac', '01a1f313-bf00-70a1-8b2c-3d4e5f6a7b8c', 'claude-code', 'agent', 'decision',
   'Una base por sesión de agente',
   'Se propuso una base distinta por sesión para aislar escrituras concurrentes.',
   'Aislar la memoria de cada ejecución y evitar escrituras cruzadas entre agentes.',
   '["db por agente","base global"]',
   '["Fragmenta la continuidad y multiplica los montajes"]',
   'storage/db-per-session', 'hypothesis', 'superseded', NULL,
   '01a1f344-44e0-75a6-8b7c-8d9e0f1a2bac', 1, '2026-11-19T10:05:00Z',
   'sha256:bb4cf8499bc0a46430eb94b6bd0b0ed8713c6491894d90605711e3725fe5e597',
   'docs/DESIGN.md D-ACM-01', '2026-11-19T09:40:00Z', '2026-11-19T10:05:00Z'),
  ('01a1f42d-ba80-76a7-8b8c-9d0e1f2a3bac', '01a1f41f-fee0-71a2-8b3c-4d5e6f7a8b9c', 'opencode', 'agent', 'discovery',
   'El checkpoint activo responde dónde quedamos',
   'El checkpoint de la sesión cerrada contiene objetivo, estado, próximos pasos y ficheros; una sesión nueva lo recupera sin leer la conversación anterior.',
   NULL, NULL, NULL, NULL, 'confirmed', 'active', '2027-11-19T00:00:00Z', NULL, 1, '2026-11-19T14:20:00Z',
   'sha256:973951e35f9590e75a0a764c7994292216d794d5b919731ba0af912a5ea63649',
   NULL, '2026-11-19T14:20:00Z', '2026-11-19T14:20:00Z'),
  ('01a1f43b-7620-77a8-8b9c-0d1e2f3a4bac', '01a1f41f-fee0-71a2-8b3c-4d5e6f7a8b9c', 'opencode', 'agent', 'learning',
   'La deduplicación exacta evita reescribir la misma decisión',
   'Una segunda captura con el mismo content_hash devuelve E_CONSTRAINT y no crea fila; queda por confirmar el coste de recuperar la fila existente.',
   NULL, NULL, NULL, NULL, 'unknown', 'needs_review', '2027-05-19T00:00:00Z', NULL, 1, '2026-11-19T14:35:00Z',
   'sha256:fa73ccaf427846244f29fb5f234b778035b0e9d7119f84c567288f8ef53a215c',
   NULL, '2026-11-19T14:35:00Z', '2026-11-19T14:35:00Z');

INSERT INTO checkpoints (id, session_id, agent_id, source, goal, state, next_steps, files, open_questions, status, superseded_by, content_hash, created_at, updated_at) VALUES
  ('01a1f392-16c0-78a9-8b0c-1d2e3f4a5bac', '01a1f313-bf00-70a1-8b2c-3d4e5f6a7b8c', 'claude-code', 'agent',
   'Cerrar el bootstrap y documentar el flujo de reanudación',
   'init.sh aplica esquema y seed de forma idempotente; el esquema coincide con DESIGN §4.8.',
   '1. Implementar la skill agent-continuity (ACM-04). 2. Ejecutar la validación ACM-05 con dos proyectos.',
   '["examples/agent-continuity/init.sh","examples/agent-continuity/schema.sql","examples/agent-continuity/docs/DESIGN.md"]',
   '["¿Qué cadencia de revisión fija el PO para needs_review?"]',
   'active', NULL,
   'sha256:a9eb6ef548b9506f79b326c7b086a6af76ae3b7606732702739d0e1f0e82b22f',
   '2026-11-19T11:30:00Z', '2026-11-19T11:30:00Z');

INSERT INTO preferences (id, session_id, agent_id, source, pref_key, value, rationale, status, review_after, superseded_by, content_hash, origin_ref, created_at, updated_at) VALUES
  ('01a1f309-ace0-79aa-8b0c-1d2e3f4a5bac', NULL, 'alfredo', 'human', 'commit/approval',
   'Presentar un informe de preparación del commit y esperar confirmación explícita antes de commitear.',
   'La convención del repositorio exige revisión humana antes de cada commit.',
   'active', '2027-02-19T00:00:00Z', NULL,
   'sha256:5b47ceae4215d8717cdb845b3c8d0f84c7e853ed9396b3507c164a05569cf2c7',
   'AGENTS.md', '2026-11-19T09:01:00Z', '2026-11-19T09:01:00Z'),
  ('01a1f30a-9740-7aab-8b0c-1d2e3f4a5bac', NULL, 'alfredo', 'human', 'docs/language',
   'Documentos con nombres de fichero en inglés y contenido en español (castellano).',
   'Convención local del repositorio; prevalece salvo petición explícita.',
   'active', '2027-02-19T00:00:00Z', NULL,
   'sha256:86862a3ad898077bc53c806b5c1d7cafe02d8a34da1b0a50ea2d91505580dd87',
   'AGENTS.md', '2026-11-19T09:02:00Z', '2026-11-19T09:02:00Z'),
  ('01a1f30b-81a0-7bbc-8c0c-1d2e3f4a5bac', NULL, 'alfredo', 'human', 'tools/no-playwright',
   'No usar Playwright en las validaciones; la revisión es manual.',
   'El PO pidió revisión manual y sin automatización de navegador.',
   'active', '2027-02-19T00:00:00Z', NULL,
   'sha256:a34b0fe4c03a463851df50d1568a6303f62fff54d8219beec1f5d56a9047a6aa',
   NULL, '2026-11-19T09:03:00Z', '2026-11-19T09:03:00Z');
