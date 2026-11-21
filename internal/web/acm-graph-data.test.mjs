// Pruebas del adaptador ACM: node --test internal/web/acm-graph-data.test.mjs
// Los dobles imitan el sobre real de db-ai ({version, ok, data, meta}) y el
// contrato de rutas /ALIAS/TABLA/ID verificado contra la base ACM del proyecto.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  ACM_TABLES,
  ACM_ORDERS,
  DEFAULT_PAGE_SIZE,
  MAX_PAGES_PER_TABLE,
  MAX_PAGE_SIZE,
  AcmGraphError,
  buildAcmGraph,
  createAcmApi,
  detectAcmMemory,
  expandAcmGraph,
  loadAcmGraph,
  loadAcmPage,
  normalizePageCount,
  normalizePageSize,
} from "./static/acm-graph-data.js";

const COLUMNS = {
  sessions: {
    id: "TEXT",
    agent_id: "TEXT",
    source: "TEXT",
    goal: "TEXT",
    status: "TEXT",
    started_at: "TEXT",
    ended_at: "TEXT",
    summary: "TEXT",
    created_at: "TEXT",
    updated_at: "TEXT",
  },
  observations: {
    id: "TEXT",
    session_id: "TEXT",
    agent_id: "TEXT",
    source: "TEXT",
    kind: "TEXT",
    title: "TEXT",
    body: "TEXT",
    rationale: "TEXT",
    alternatives: "TEXT",
    consequences: "TEXT",
    topic_key: "TEXT",
    confidence: "TEXT",
    status: "TEXT",
    review_after: "TEXT",
    superseded_by: "TEXT",
    duplicate_count: "INTEGER",
    last_seen_at: "TEXT",
    content_hash: "TEXT",
    origin_ref: "TEXT",
    created_at: "TEXT",
    updated_at: "TEXT",
  },
  checkpoints: {
    id: "TEXT",
    session_id: "TEXT",
    agent_id: "TEXT",
    source: "TEXT",
    goal: "TEXT",
    state: "TEXT",
    next_steps: "TEXT",
    files: "TEXT",
    open_questions: "TEXT",
    status: "TEXT",
    superseded_by: "TEXT",
    content_hash: "TEXT",
    created_at: "TEXT",
    updated_at: "TEXT",
  },
  preferences: {
    id: "TEXT",
    session_id: "TEXT",
    agent_id: "TEXT",
    source: "TEXT",
    pref_key: "TEXT",
    value: "TEXT",
    rationale: "TEXT",
    status: "TEXT",
    review_after: "TEXT",
    superseded_by: "TEXT",
    content_hash: "TEXT",
    origin_ref: "TEXT",
    created_at: "TEXT",
    updated_at: "TEXT",
  },
};

function t(data) {
  return { kind: "text", data };
}
function i(data) {
  return { kind: "integer", data: String(data) };
}
function n() {
  return { kind: "null", data: "" };
}

function record(kind, id, extra = {}) {
  const base = {
    id: t(id),
    created_at: t("2026-01-01T00:00:00Z"),
    updated_at: t("2026-01-01T00:00:00Z"),
    status: t("active"),
  };
  if (kind === "sessions") {
    Object.assign(base, {
      agent_id: t("agent"),
      source: t("agent"),
      goal: t(`Objetivo ${id}`),
      started_at: t("2026-01-01T00:00:00Z"),
    });
  } else if (kind === "observations") {
    Object.assign(base, {
      session_id: t("s1"),
      agent_id: t("agent"),
      source: t("agent"),
      kind: t("decision"),
      title: t(`Título ${id}`),
      body: t(`Cuerpo ${id}`),
      confidence: t("confirmed"),
      superseded_by: n(),
      duplicate_count: i(1),
      content_hash: t(`sha256:${id}`),
      topic_key: n(),
    });
  } else if (kind === "checkpoints") {
    Object.assign(base, {
      session_id: t("s1"),
      agent_id: t("agent"),
      source: t("agent"),
      goal: t("Objetivo"),
      state: t(`Estado ${id}`),
      next_steps: t("[]"),
      superseded_by: n(),
      content_hash: t(`sha256:${id}`),
    });
  } else if (kind === "preferences") {
    Object.assign(base, {
      session_id: t("s1"),
      agent_id: t("agent"),
      source: t("agent"),
      pref_key: t(`clave/${id}`),
      value: t(`valor ${id}`),
      superseded_by: n(),
      content_hash: t(`sha256:${id}`),
    });
  }
  Object.assign(base, extra);
  return {
    path: `/mem/${kind}/${id}`,
    fields: base,
    etag: `sha256:${id}`,
  };
}

function describeFor(alias, kind, table, options = {}) {
  const columns = Object.entries(COLUMNS[kind]).map(([name, declared_type]) => ({
    name,
    declared_type,
    primary_key: name === "id" ? 1 : 0,
    nullable: name !== "id",
    generated: false,
  }));
  return {
    path: `/${alias}/${table}`,
    table,
    columns: options.columns ?? columns,
    primary_key: options.primary_key ?? ["id"],
    indexes: [],
    foreign_keys: [],
    incoming_references: [],
    triggers: [],
    capabilities: {
      readable: options.readable ?? true,
      writable: options.writable ?? true,
      navigable: options.navigable ?? true,
    },
    limitations: [],
  };
}

function memory({ alias = "mem", rows = {}, tables = {}, descriptions = {} } = {}) {
  const data = {
    sessions: rows.sessions ?? [record("sessions", "s1")],
    observations: rows.observations ?? [record("observations", "o1")],
    checkpoints: rows.checkpoints ?? [record("checkpoints", "c1")],
    preferences: rows.preferences ?? [record("preferences", "p1")],
  };
  return {
    alias,
    tables: Object.fromEntries(
      ACM_TABLES.map((kind) => [
        kind,
        {
          name: tables[kind] ?? kind,
          rows: data[kind].map((row) => ({
            ...row,
            path: row.path.replace(/^\/mem\//, `/${alias}/`),
          })),
          description: descriptions[kind] ?? {},
        },
      ]),
    ),
  };
}

function ok(data, meta = {}) {
  return {
    version: 1,
    ok: true,
    data,
    meta: { truncated: false, request_id: "test", ...meta },
  };
}

// La arista se identifica con JSON de sus tres componentes, nunca concatenando.
function edgeId(type, from, to) {
  return JSON.stringify([type, from, to]);
}

// Simula el orden keyset real para probar el refresco: el servidor acepta
// `columna[:asc|desc]` y la PK desempata. Suficiente para created_at/id TEXT.
function sortByOrder(rows, order) {
  const specs = order.split(",").map((term) => {
    const [column, direction] = term.split(":");
    return { column, desc: direction === "desc" };
  });
  return rows.slice().sort((a, b) => {
    for (const spec of specs) {
      const av = a.fields[spec.column]?.data ?? "";
      const bv = b.fields[spec.column]?.data ?? "";
      let cmp = av < bv ? -1 : av > bv ? 1 : 0;
      if (spec.desc) cmp = -cmp;
      if (cmp !== 0) return cmp;
    }
    return 0;
  });
}

function fakeApi({
  memories = [memory()],
  listTruncatedPaths = new Set(),
  listFailures = {},
  findFailures = {},
  describeFailures = {},
  omitCursor = false,
  respectOrder = false,
} = {}) {
  const byAlias = new Map(memories.map((item) => [item.alias, item]));
  const calls = [];

  const api = async (endpoint, params = {}) => {
    calls.push({ endpoint, params });
    if (endpoint === "ls") {
      const path = params.path;
      if (path === "/") {
        return ok(
          {
            path: "/",
            entries: memories.map((item) => ({
              name: item.alias,
              type: "mount",
              path: `/${item.alias}`,
            })),
          },
          listTruncatedPaths.has("/") ? { truncated: true } : {},
        );
      }
      const alias = path.slice(1);
      const item = byAlias.get(alias);
      if (!item) throw new AcmGraphError("E_NOT_FOUND", `mount ${alias}`);
      if (listFailures[path]) {
        throw new AcmGraphError(listFailures[path].code, listFailures[path].message);
      }
      return ok(
        {
          path,
          entries: ACM_TABLES.map((kind) => ({
            name: item.tables[kind].name,
            type: "table",
            path: `/${alias}/${item.tables[kind].name}`,
          })),
        },
        listTruncatedPaths.has(path) ? { truncated: true } : {},
      );
    }

    if (endpoint === "describe" || endpoint === "find") {
      const [, alias, tableName] = params.path.split("/");
      const item = byAlias.get(alias);
      const kind = ACM_TABLES.find((name) => item?.tables[name].name === tableName);
      if (!item || !kind) throw new AcmGraphError("E_NOT_FOUND", `table ${params.path}`);
      const key = `${alias}/${tableName}`;

      if (endpoint === "describe") {
        if (describeFailures[key]) {
          throw new AcmGraphError(describeFailures[key].code, describeFailures[key].message);
        }
        return ok(
          describeFor(
            alias,
            kind,
            tableName,
            item.tables[kind].description,
          ),
        );
      }

      if (findFailures[key]) {
        throw new AcmGraphError(findFailures[key].code, findFailures[key].message);
      }
      const rows = respectOrder && params.order
        ? sortByOrder(item.tables[kind].rows, params.order)
        : item.tables[kind].rows;
      const limit = Number(params.limit ?? MAX_PAGE_SIZE);
      const start = params.cursor ? Number(params.cursor) : 0;
      const end = Math.min(start + limit, rows.length);
      const truncated = end < rows.length;
      const meta = { truncated };
      if (truncated && !omitCursor) meta.next_cursor = String(end);
      return ok({ path: params.path, rows: rows.slice(start, end) }, meta);
    }

    throw new Error(`endpoint inesperado: ${endpoint}`);
  };
  api.calls = calls;
  return api;
}

test("detecta ACM por estructura en cualquier alias y transforma nodos con rutas reales", async () => {
  const api = fakeApi({ memories: [memory({ alias: "memoria-x" })] });
  const detection = await detectAcmMemory(api);
  assert.equal(detection.alias, "memoria-x");
  for (const kind of ACM_TABLES) {
    assert.equal(detection.tables[kind].table, kind);
    assert.equal(detection.tables[kind].path, `/memoria-x/${kind}`);
  }

  const graph = await loadAcmGraph({ api, pageSize: 10 });
  assert.equal(graph.complete, true);
  assert.equal(graph.nodes.length, 4);
  assert.equal(graph.edges.length, 3);

  const session = graph.nodes.find((node) => node.id === "sessions:s1");
  assert.equal(session.path, "/memoria-x/sessions/s1");
  assert.equal(session.label, "Objetivo s1");
  assert.equal(session.table, "sessions");

  const observation = graph.nodes.find((node) => node.id === "observations:o1");
  assert.equal(observation.path, "/memoria-x/observations/o1");
  assert.equal(observation.kind, "decision");
  assert.equal(observation.topic, null);
  assert.equal(observation.fields.duplicate_count, 1);

  assert.deepEqual(
    graph.edges.map((edge) => edge.type).sort(),
    ["session_id", "session_id", "session_id"],
  );
  assert.ok(graph.edges.every((edge) => edge.resolved === true));
  assert.ok(graph.edges.every((edge) => edge.from === "sessions:s1"));
  assert.ok(graph.edges.every((edge) => edge.sourcePath === "/memoria-x/sessions/s1"));
  assert.ok(graph.edges.every((edge) => edge.targetPath?.startsWith("/memoria-x/")));
});

test("solo genera aristas session_id y superseded_by, y omite los NULL", async () => {
  const api = fakeApi({
    memories: [
      memory({
        rows: {
          sessions: [record("sessions", "s1"), record("sessions", "s2")],
          observations: [
            record("observations", "o1", { session_id: t("s1"), topic_key: t("tema/real") }),
            record("observations", "o2", { session_id: t("s1"), superseded_by: t("o1") }),
            record("observations", "o3", { session_id: t("s2") }),
          ],
          checkpoints: [
            record("checkpoints", "c1", { session_id: t("s1") }),
            record("checkpoints", "c2", { session_id: t("s1"), superseded_by: t("c1") }),
          ],
          preferences: [
            record("preferences", "p1", { session_id: t("s1") }),
            record("preferences", "p2", { session_id: n() }),
            record("preferences", "p3", { session_id: t("s2"), superseded_by: t("p1") }),
          ],
        },
      }),
    ],
  });
  const graph = await loadAcmGraph({ api, pageSize: 50 });

  assert.equal(graph.nodes.length, 10);
  assert.equal(graph.edges.length, 10);
  assert.deepEqual([...new Set(graph.edges.map((edge) => edge.type))].sort(), [
    "session_id",
    "superseded_by",
  ]);

  const preference = graph.nodes.find((node) => node.id === "preferences:p2");
  assert.equal(preference.sessionId, null);
  assert.equal(
    graph.edges.some((edge) => edge.from === "preferences:p2" && edge.type === "session_id"),
    false,
  );

  const sessionEdge = graph.edges.find(
    (edge) => edge.id === edgeId("session_id", "sessions:s1", "observations:o1"),
  );
  assert.equal(sessionEdge.from, "sessions:s1");
  assert.equal(sessionEdge.to, "observations:o1");
  assert.equal(sessionEdge.sourcePath, "/mem/sessions/s1");
  assert.equal(sessionEdge.targetPath, "/mem/observations/o1");
  assert.equal(
    graph.edges.some((edge) => edge.type === "session_id" && edge.from === "observations:o1"),
    false,
  );

  const superseded = graph.edges.find(
    (edge) => edge.id === edgeId("superseded_by", "observations:o2", "observations:o1"),
  );
  assert.equal(superseded.sourcePath, "/mem/observations/o2");
  assert.equal(superseded.targetPath, "/mem/observations/o1");

  const topic = graph.nodes.find((node) => node.id === "observations:o1");
  assert.equal(topic.topic, "tema/real");
  assert.equal(graph.nodes.find((node) => node.id === "sessions:s1").topic, null);
});

test("rechaza con mensaje en español cuando no hay memoria ACM", async () => {
  const api = fakeApi({ memories: [] });
  await assert.rejects(
    () => detectAcmMemory(api),
    (error) =>
      error instanceof AcmGraphError &&
      error.code === "E_ACM_NOT_FOUND" &&
      /No se encontró la memoria ACM/.test(error.message),
  );
});

test("rechaza un montaje con tablas ACM pero estructura incompatible", async () => {
  const broken = memory({
    descriptions: {
      sessions: {
        columns: Object.entries(COLUMNS.sessions).map(([name, declared_type]) => ({
          name,
          declared_type: name === "id" ? "INTEGER" : declared_type,
          primary_key: name === "id" ? 1 : 0,
          nullable: name !== "id",
          generated: false,
        })),
      },
    },
  });
  const api = fakeApi({ memories: [broken] });
  await assert.rejects(
    () => detectAcmMemory(api),
    (error) =>
      error.code === "E_ACM_NOT_FOUND" &&
      /No se encontró una memoria ACM válida/.test(error.message) &&
      error.details.problems.some((problem) => problem.includes("id")),
  );
});

test("un montaje sin permiso de lectura se distingue de una memoria inexistente", async () => {
  const api = fakeApi({ memories: [memory({ descriptions: { observations: { readable: false } } })] });
  await assert.rejects(
    () => detectAcmMemory(api),
    (error) =>
      error.code === "E_ACM_PERMISSION" &&
      /no es legible/.test(error.message) &&
      error.details.table === "observations",
  );
});

test("varias memorias ACM exigen alias explícito y respetan el elegido", async () => {
  const api = fakeApi({
    memories: [memory({ alias: "mem-a" }), memory({ alias: "mem-b" })],
  });
  await assert.rejects(
    () => detectAcmMemory(api),
    (error) => error.code === "E_ACM_AMBIGUOUS" && /mem-a, mem-b/.test(error.message),
  );
  const picked = await detectAcmMemory(api, { alias: "mem-b" });
  assert.equal(picked.alias, "mem-b");
});

test("la detección truncada no afirma que la memoria no exista", async () => {
  const api = fakeApi({ listTruncatedPaths: new Set(["/"]) });
  await assert.rejects(
    () => detectAcmMemory(api),
    (error) => error.code === "E_ACM_DETECTION_LIMIT" && /truncada/.test(error.message),
  );
});

test("un montaje ajeno truncado o ilegible no oculta la memoria ACM", async () => {
  const truncated = fakeApi({
    memories: [memory({ alias: "mem-a" }), memory({ alias: "mem-b" })],
    listTruncatedPaths: new Set(["/mem-a"]),
  });
  assert.equal((await detectAcmMemory(truncated)).alias, "mem-b");

  const denied = fakeApi({
    memories: [memory({ alias: "mem-a" }), memory({ alias: "mem-b" })],
    listFailures: { "/mem-a": { code: "E_DENIED", message: "denegado" } },
  });
  assert.equal((await detectAcmMemory(denied)).alias, "mem-b");

  const onlyDenied = fakeApi({
    memories: [memory({ alias: "mem-a" })],
    listFailures: { "/mem-a": { code: "E_DENIED", message: "denegado" } },
  });
  await assert.rejects(
    () => detectAcmMemory(onlyDenied),
    (error) => error.code === "E_ACM_PERMISSION" && /deniega la lectura/.test(error.message),
  );
});

test("una página por tabla, truncación declarada y expansión explícita con cursor", async () => {
  const api = fakeApi({
    memories: [
      memory({
        rows: {
          sessions: [
            record("sessions", "s1"),
            record("sessions", "s2"),
            record("sessions", "s3"),
          ],
          observations: [record("observations", "o1", { session_id: t("s3") })],
          checkpoints: [],
          preferences: [],
        },
      }),
    ],
  });

  const first = await loadAcmGraph({ api, pageSize: 2 });
  assert.equal(first.limits.pageSize, 2);
  assert.equal(first.tables.sessions.rowCount, 2);
  assert.equal(first.tables.sessions.truncated, true);
  assert.equal(first.tables.sessions.hasMore, true);
  assert.equal(first.tables.sessions.nextCursor, "2");
  assert.equal(first.complete, false);
  assert.equal(first.nodes.some((node) => node.id === "sessions:s3"), false);

  // La referencia a una sesión aún no cargada no se convierte en arista.
  assert.equal(first.edges.length, 0);
  assert.deepEqual(first.unresolved, [
    {
      table: "sessions",
      id: "s3",
      reason: "pending",
      count: 1,
      sources: [
        { type: "session_id", table: "observations", id: "o1", path: "/mem/observations/o1" },
      ],
    },
  ]);
  assert.equal(first.counts.unresolvedReferences, 1);
  assert.equal(first.counts.unresolvedTargets, 1);
  assert.equal(first.counts.references, 1);
  assert.equal(first.counts.edges, 0);

  const second = await expandAcmGraph(first, "sessions", { api });
  assert.equal(second.tables.sessions.rowCount, 3);
  assert.equal(second.tables.sessions.truncated, false);
  assert.equal(second.tables.sessions.nextCursor, "");
  assert.equal(second.complete, true);
  assert.equal(second.unresolved.length, 0);
  const resolved = second.edges.find((edge) => edge.type === "session_id");
  assert.equal(resolved.from, "sessions:s3");
  assert.equal(resolved.to, "observations:o1");
  assert.equal(resolved.sourcePath, "/mem/sessions/s3");
  assert.equal(resolved.targetPath, "/mem/observations/o1");
  assert.equal(second.counts.edges, 1);
  assert.equal(second.counts.references, 1);

  await assert.rejects(
    () => expandAcmGraph(second, "sessions", { api }),
    (error) => error.code === "E_NO_MORE",
  );
});

test("actualizar restaura páginas y la observación nueva entra en la primera", async () => {
  const memoryItem = memory({
    rows: {
      observations: [
        record("observations", "o1", { created_at: t("2026-01-01T00:00:00Z") }),
        record("observations", "o2", { created_at: t("2026-02-01T00:00:00Z") }),
        record("observations", "o3", { created_at: t("2026-03-01T00:00:00Z") }),
      ],
    },
  });
  const api = fakeApi({ memories: [memoryItem], respectOrder: true });

  const first = await loadAcmGraph({ api, pageSize: 1 });
  assert.equal(first.tables.observations.pageCount, 1);
  assert.equal(first.tables.observations.order, ACM_ORDERS.observations);
  assert.equal(first.nodes.some((node) => node.id === "observations:o3"), true);
  assert.equal(first.nodes.some((node) => node.id === "observations:o2"), false);

  const expanded = await expandAcmGraph(first, "observations", { api });
  assert.equal(expanded.tables.observations.pageCount, 2);
  assert.equal(expanded.nodes.some((node) => node.id === "observations:o2"), true);
  assert.equal(expanded.nodes.some((node) => node.id === "observations:o1"), false);

  memoryItem.tables.observations.rows.unshift(
    record("observations", "o4", { created_at: t("2026-04-01T00:00:00Z") }),
  );
  const refreshed = await loadAcmGraph({
    api,
    pageSize: 1,
    pageCounts: { observations: 2 },
  });
  assert.equal(refreshed.tables.observations.pageCount, 2);
  assert.equal(refreshed.nodes.some((node) => node.id === "observations:o4"), true);
  assert.equal(refreshed.nodes.some((node) => node.id === "observations:o3"), true);
  assert.equal(refreshed.nodes.some((node) => node.id === "observations:o2"), false);

  const requests = api.calls.filter(
    (call) => call.endpoint === "find" && call.params.path.endsWith("/observations"),
  );
  assert.equal(requests.length, 4);
  for (const request of requests) {
    assert.equal(request.params.order, "created_at:desc,id:desc");
  }
});

test("el empate de created_at se desempata por id descendente", async () => {
  const api = fakeApi({
    memories: [
      memory({
        rows: {
          observations: [
            record("observations", "o1", { created_at: t("2026-01-01T00:00:00Z") }),
            record("observations", "o2", { created_at: t("2026-01-01T00:00:00Z") }),
            record("observations", "o3", { created_at: t("2026-01-01T00:00:00Z") }),
          ],
        },
      }),
    ],
    respectOrder: true,
  });
  const graph = await loadAcmGraph({ api, pageSize: 1 });
  assert.equal(graph.tables.observations.rowCount, 1);
  assert.ok(graph.nodes.some((node) => node.id === "observations:o3"));
  assert.equal(graph.nodes.some((node) => node.id === "observations:o2"), false);
});

test("el número de páginas pedido está acotado", async () => {
  assert.equal(normalizePageCount(undefined, "observations"), 1);
  assert.equal(normalizePageCount(MAX_PAGES_PER_TABLE, "observations"), MAX_PAGES_PER_TABLE);
  for (const value of [0, MAX_PAGES_PER_TABLE + 1, 1.5, "dos"]) {
    assert.throws(
      () => normalizePageCount(value, "observations"),
      (error) => error instanceof AcmGraphError && error.code === "E_OPTION",
    );
  }
  const api = fakeApi();
  await assert.rejects(
    () => loadAcmGraph({ api, pageSize: 1, pageCounts: { observations: MAX_PAGES_PER_TABLE + 1 } }),
    (error) => error instanceof AcmGraphError && error.code === "E_OPTION",
  );
});

test("una referencia ausente solo se afirma con la tabla de destino completa", async () => {
  const api = fakeApi({
    memories: [
      memory({
        rows: {
          observations: [
            record("observations", "o1", { session_id: t("s9") }),
            record("observations", "o2", { session_id: t("s9") }),
          ],
        },
      }),
    ],
  });
  const graph = await loadAcmGraph({ api, pageSize: 50 });
  assert.equal(graph.complete, true);
  assert.deepEqual(graph.unresolved, [
    {
      table: "sessions",
      id: "s9",
      reason: "absent",
      count: 2,
      sources: [
        { type: "session_id", table: "observations", id: "o1", path: "/mem/observations/o1" },
        { type: "session_id", table: "observations", id: "o2", path: "/mem/observations/o2" },
      ],
    },
  ]);
  assert.equal(graph.edges.some((edge) => edge.to === "sessions:s9"), false);
  assert.equal(graph.counts.unresolvedReferences, 2);
  assert.equal(graph.counts.references, graph.edges.length + 2);
});

test("deduplica nodos repetidos entre páginas y recuenta referencias no resueltas", () => {
  const repeated = record("observations", "o1", { session_id: t("s1") });
  const source = {
    alias: "mem",
    limits: { pageSize: 2, maxPageSize: MAX_PAGE_SIZE, detectionListLimit: 1000 },
    tables: Object.fromEntries(
      ACM_TABLES.map((kind) => [
        kind,
        {
          kind,
          table: kind,
          path: `/mem/${kind}`,
          describe: describeFor("mem", kind, kind),
          pages:
            kind === "observations"
              ? [{ rows: [repeated, repeated], truncated: false, nextCursor: "" }]
              : [{ rows: [], truncated: false, nextCursor: "" }],
          error: null,
        },
      ]),
    ),
  };
  const graph = buildAcmGraph(source);
  assert.equal(graph.counts.duplicateNodes, 1);
  assert.equal(graph.nodes.length, 1);
  assert.equal(graph.edges.length, 0);
  assert.deepEqual(graph.unresolved, [
    {
      table: "sessions",
      id: "s1",
      reason: "absent",
      count: 1,
      sources: [
        { type: "session_id", table: "observations", id: "o1", path: "/mem/observations/o1" },
      ],
    },
  ]);
});

test("acepta una base ACM vacía sin inventar nodos ni relaciones", async () => {
  const api = fakeApi({
    memories: [
      memory({
        rows: { sessions: [], observations: [], checkpoints: [], preferences: [] },
      }),
    ],
  });
  const graph = await loadAcmGraph({ api, pageSize: 50 });
  assert.equal(graph.complete, true);
  assert.deepEqual(graph.nodes, []);
  assert.deepEqual(graph.edges, []);
  assert.deepEqual(graph.unresolved, []);
  assert.equal(graph.counts.nodes, 0);
});

test("un error de lectura queda declarado en su tabla y no finge una memoria completa", async () => {
  const api = fakeApi({
    memories: [
      memory({
        rows: {
          observations: [record("observations", "o1", { session_id: t("s1") })],
        },
      }),
    ],
    findFailures: { "mem/sessions": { code: "E_DENIED", message: "no permitido" } },
  });
  const graph = await loadAcmGraph({ api, pageSize: 50 });
  assert.equal(graph.tables.sessions.error.code, "E_DENIED");
  assert.equal(graph.tables.sessions.loaded, false);
  assert.equal(graph.complete, false);
  assert.equal(graph.edges.length, 0);
  assert.equal(graph.unresolved[0].reason, "pending");
  assert.equal(graph.unresolved[0].count, 3);
  assert.equal(graph.counts.unresolvedReferences, 3);
  assert.equal(graph.counts.edges, 0);
  assert.equal(graph.counts.references, 3);
});

test("una tabla en el máximo de páginas se declara y no expande más", async () => {
  const rows = Array.from({ length: MAX_PAGES_PER_TABLE + 1 }, (_, index) =>
    record("observations", `o${String(index).padStart(2, "0")}`, { session_id: t("s1") }),
  );
  const api = fakeApi({ memories: [memory({ rows: { observations: rows } })] });
  const graph = await loadAcmGraph({
    api,
    pageSize: 1,
    pageCounts: { observations: MAX_PAGES_PER_TABLE },
  });
  assert.equal(graph.tables.observations.pageCount, MAX_PAGES_PER_TABLE);
  assert.equal(graph.tables.observations.hasMore, false);
  assert.equal(graph.tables.observations.atPageLimit, true);
  assert.ok(graph.warnings.some((warning) => /máximo de 10 páginas/.test(warning)));
  await assert.rejects(
    () => expandAcmGraph(graph, "observations", { api }),
    (error) => error.code === "E_PAGE_LIMIT",
  );
});

test("una tabla truncada sin cursor se declara y no se puede expandir", async () => {
  const api = fakeApi({
    memories: [
      memory({
        rows: {
          sessions: [record("sessions", "s1"), record("sessions", "s2")],
        },
      }),
    ],
    omitCursor: true,
  });
  const graph = await loadAcmGraph({ api, pageSize: 1 });
  assert.equal(graph.tables.sessions.truncated, true);
  assert.equal(graph.tables.sessions.hasMore, false);
  assert.match(graph.warnings[0], /sin cursor de continuación/);
  await assert.rejects(
    () => expandAcmGraph(graph, "sessions", { api }),
    (error) => error.code === "E_NO_MORE",
  );
});

test("un registro con ruta o id inválidos detiene la transformación", () => {
  const badPath = record("sessions", "s1");
  badPath.path = "/mem/sessions";
  const badId = record("sessions", "s2", { id: n() });
  const emptyIdWithoutMarker = record("sessions", "");
  emptyIdWithoutMarker.path = "/mem/sessions/sin-marcador";

  const sourceWith = (row) => ({
    alias: "mem",
    tables: Object.fromEntries(
      ACM_TABLES.map((kind) => [
        kind,
        {
          kind,
          table: kind,
          path: `/mem/${kind}`,
          describe: describeFor("mem", kind, kind),
          pages: [{ rows: kind === "sessions" ? [row] : [], truncated: false, nextCursor: "" }],
          error: null,
        },
      ]),
    ),
  });

  assert.throws(() => buildAcmGraph(sourceWith(badPath)), (error) => error.code === "E_DATA");
  assert.throws(() => buildAcmGraph(sourceWith(badId)), (error) => error.code === "E_DATA");
  assert.throws(
    () => buildAcmGraph(sourceWith(emptyIdWithoutMarker)),
    (error) => error.code === "E_DATA",
  );
});

test("acepta un id TEXT vacío con su ruta ~e y conserva la arista de sesión", async () => {
  const emptySession = record("sessions", "");
  emptySession.path = "/mem/sessions/~e";
  const api = fakeApi({
    memories: [
      memory({
        rows: {
          sessions: [emptySession],
          observations: [record("observations", "o1", { session_id: t("") })],
          checkpoints: [],
          preferences: [],
        },
      }),
    ],
  });

  const graph = await loadAcmGraph({ api, pageSize: 10 });
  const session = graph.nodes.find((node) => node.id === "sessions:");
  assert.ok(session);
  assert.equal(session.recordId, "");
  assert.equal(session.path, "/mem/sessions/~e");

  const edge = graph.edges.find((item) => item.type === "session_id");
  assert.ok(edge);
  assert.equal(edge.from, "sessions:");
  assert.equal(edge.to, "observations:o1");
  assert.equal(edge.id, edgeId("session_id", "sessions:", "observations:o1"));
  assert.deepEqual(graph.unresolved, []);
  assert.equal(graph.counts.edges, 1);
});

test("la identidad de arista no colisiona con ids que contienen ->", async () => {
  const longSession = record("sessions", "s->observations:x");
  longSession.path = "/mem/sessions/s-%3Eobservations%3Ax";
  const embeddedId = record("observations", "x->observations:y", { session_id: t("s") });
  embeddedId.path = "/mem/observations/x-%3Eobservations%3Ay";

  const api = fakeApi({
    memories: [
      memory({
        rows: {
          sessions: [longSession, record("sessions", "s")],
          observations: [
            record("observations", "y", { session_id: t("s->observations:x") }),
            embeddedId,
          ],
          checkpoints: [],
          preferences: [],
        },
      }),
    ],
  });

  const graph = await loadAcmGraph({ api, pageSize: 10 });
  const longEdge = graph.edges.find(
    (item) => item.from === "sessions:s->observations:x" && item.to === "observations:y",
  );
  const embeddedEdge = graph.edges.find(
    (item) => item.from === "sessions:s" && item.to === "observations:x->observations:y",
  );

  assert.ok(longEdge);
  assert.ok(embeddedEdge);
  assert.notEqual(longEdge.id, embeddedEdge.id);
  assert.equal(
    longEdge.id,
    edgeId("session_id", "sessions:s->observations:x", "observations:y"),
  );
  assert.equal(
    embeddedEdge.id,
    edgeId("session_id", "sessions:s", "observations:x->observations:y"),
  );
  assert.equal(graph.edges.length, 2);
  assert.equal(graph.counts.duplicateEdges, 0);
  assert.deepEqual(graph.unresolved, []);
});

test("el cliente solo emite GET de lectura y conserva los parámetros reales", async () => {
  let seen;
  const fetchImpl = async (url, init) => {
    seen = { url, init };
    return new Response(JSON.stringify(ok({ path: "/mem/sessions", rows: [] })), {
      status: 200,
      headers: { "content-type": "application/json" },
    });
  };
  const api = createAcmApi({ fetchImpl });

  await assert.rejects(
    () => api("touch", { path: "/mem/sessions" }),
    (error) => error.code === "E_UNSUPPORTED" && /Solo se lee/.test(error.message),
  );

  await api("find", { path: "/mem/sessions", limit: 2, cursor: "" });
  assert.equal(seen.init.method, "GET");
  assert.equal(seen.url, "/api/find?path=%2Fmem%2Fsessions&limit=2");

  await loadAcmPage(api, { path: "/mem/sessions", cursor: "abc", limit: 2 });
  assert.match(seen.url, /cursor=abc/);
});

test("el cliente traduce red, respuesta no JSON y sobre de error", async () => {
  const network = createAcmApi({
    fetchImpl: async () => {
      throw new TypeError("fetch failed");
    },
  });
  await assert.rejects(
    () => network("ls", { path: "/" }),
    (error) => error.code === "E_NETWORK" && /No se pudo contactar con db-ai/.test(error.message),
  );

  const noJson = createAcmApi({
    fetchImpl: async () => new Response("no es json", { status: 502 }),
  });
  await assert.rejects(
    () => noJson("ls", { path: "/" }),
    (error) => error.code === "E_HTTP" && /HTTP 502/.test(error.message),
  );

  const denied = createAcmApi({
    fetchImpl: async () =>
      new Response(
        JSON.stringify({
          version: 1,
          ok: false,
          error: { code: "E_DENIED", message: "read denied" },
        }),
        { status: 403, headers: { "content-type": "application/json" } },
      ),
  });
  await assert.rejects(
    () => denied("find", { path: "/mem/sessions" }),
    (error) =>
      error.code === "E_DENIED" &&
      /db-ai rechazó la lectura: read denied/.test(error.message),
  );
});

test("pageSize está acotado y declarado, nunca implícito", () => {
  assert.equal(normalizePageSize(undefined), DEFAULT_PAGE_SIZE);
  assert.equal(normalizePageSize(1000), MAX_PAGE_SIZE);
  for (const value of [0, -1, 1.5, 1001, "muchas"]) {
    assert.throws(() => normalizePageSize(value), (error) => error.code === "E_OPTION");
  }
});
