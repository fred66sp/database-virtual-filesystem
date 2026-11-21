// Pruebas de la capa de datos del dashboard:
//   node --test internal/web/dashboard-data.test.mjs
// Los dobles imitan el sobre real de db-ai ({version, ok, data, meta}) y sus
// rutas /ALIAS/TABLA/ID. Ninguna prueba escribe: solo count, describe, find y ls.

import { test } from "node:test";
import assert from "node:assert/strict";

import {
  DASHBOARD_ENDPOINTS,
  DashboardError,
  bucketCounts,
  createDashboardApi,
  detectAcmMounts,
  loadAcmStats,
  loadGeneralStats,
  percentages,
  regroup,
} from "./static/dashboard-data.js";

const ACM_TABLES = ["sessions", "observations", "checkpoints", "preferences"];

const COLUMNS = {
  sessions: {
    id: "TEXT",
    agent_id: "TEXT",
    source: "TEXT",
    goal: "TEXT",
    status: "TEXT",
    started_at: "TEXT",
    ended_at: "TEXT",
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
    confidence: "TEXT",
    status: "TEXT",
    superseded_by: "TEXT",
    duplicate_count: "INTEGER",
    content_hash: "TEXT",
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
    status: "TEXT",
    superseded_by: "TEXT",
    content_hash: "TEXT",
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

function row(kind, id, extra = {}) {
  const base = {
    id,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
    status: "active",
  };
  if (kind === "sessions") {
    Object.assign(base, {
      agent_id: "agent",
      source: "agent",
      goal: `Objetivo ${id}`,
      status: "open",
      started_at: "2026-01-01T00:00:00Z",
      ended_at: null,
    });
  } else if (kind === "observations") {
    Object.assign(base, {
      session_id: "s1",
      agent_id: "agent",
      source: "agent",
      kind: "decision",
      title: `Título ${id}`,
      body: `Cuerpo ${id}`,
      confidence: "confirmed",
      superseded_by: null,
      duplicate_count: 1,
      content_hash: `sha256:${id}`,
    });
  } else if (kind === "checkpoints") {
    Object.assign(base, {
      session_id: "s1",
      agent_id: "agent",
      source: "agent",
      goal: "Objetivo",
      state: `Estado ${id}`,
      next_steps: "[]",
      superseded_by: null,
      content_hash: `sha256:${id}`,
    });
  } else if (kind === "preferences") {
    Object.assign(base, {
      session_id: "s1",
      agent_id: "agent",
      source: "agent",
      pref_key: `clave/${id}`,
      value: `valor ${id}`,
      superseded_by: null,
      content_hash: `sha256:${id}`,
    });
  }
  Object.assign(base, extra);
  return base;
}

function acmMount(alias = "mem", rows = {}) {
  return {
    alias,
    tables: ACM_TABLES.map((kind) => ({
      name: kind,
      kind,
      rows: rows[kind] ?? [row(kind, `${kind[0]}1`)],
    })),
  };
}

function describeFor(mount, table) {
  return {
    path: `/${mount.alias}/${table.name}`,
    table: table.name,
    columns: Object.entries(COLUMNS[table.kind]).map(([name, declared_type]) => ({
      name,
      declared_type,
      primary_key: name === "id" ? 1 : 0,
      nullable: name !== "id",
      generated: false,
    })),
    primary_key: ["id"],
    indexes: [],
    foreign_keys: [],
    incoming_references: [],
    triggers: [],
    capabilities: { readable: true, writable: true, navigable: true },
    limitations: [],
  };
}

function ok(data, meta = {}) {
  return { version: 1, ok: true, data, meta: { truncated: false, request_id: "test", ...meta } };
}

function matches(item, expr) {
  const match = /^([a-z_]+)\s*=\s*(.*)$/.exec(expr);
  if (!match) throw new Error(`where inesperado: ${expr}`);
  const [, column, raw] = match;
  const value = raw.startsWith('"') ? JSON.parse(raw) : raw;
  return String(item[column] ?? "") === value;
}

function sortRows(rows, order) {
  const specs = order.split(",").map((term) => {
    const [column, direction] = term.split(":");
    return { column, desc: direction === "desc" };
  });
  return rows.slice().sort((a, b) => {
    for (const spec of specs) {
      const av = String(a[spec.column] ?? "");
      const bv = String(b[spec.column] ?? "");
      const cmp = av < bv ? -1 : av > bv ? 1 : 0;
      if (cmp !== 0) return spec.desc ? -cmp : cmp;
    }
    return 0;
  });
}

function project(item, fields) {
  const names = fields ? fields.split(",") : Object.keys(item);
  const out = {};
  for (const name of names) {
    if (!(name in item)) continue;
    const value = item[name];
    out[name] = value === null ? n() : typeof value === "number" ? i(value) : t(value);
  }
  return out;
}

function fakeApi({
  mounts = [acmMount()],
  countFailures = {},
  findFailures = {},
  listFailures = {},
  rootTruncated = false,
  mountTruncated = {},
} = {}) {
  const calls = [];

  const resolve = (path) => {
    const [, alias, name] = path.split("/");
    const mount = mounts.find((item) => item.alias === alias);
    const table = mount?.tables.find((item) => item.name === name);
    return { mount, table };
  };

  const api = async (endpoint, params = {}) => {
    calls.push({ endpoint, params });

    if (endpoint === "ls") {
      if (params.path === "/") {
        return ok(
          {
            path: "/",
            entries: mounts.map((mount) => ({
              name: mount.alias,
              type: "mount",
              path: `/${mount.alias}`,
            })),
          },
          rootTruncated ? { truncated: true } : {},
        );
      }
      if (listFailures[params.path]) {
        throw new DashboardError(listFailures[params.path].code, listFailures[params.path].message);
      }
      const mount = mounts.find((item) => `/${item.alias}` === params.path);
      if (!mount) throw new DashboardError("E_NOT_FOUND", `mount ${params.path}`);
      return ok(
        {
          path: params.path,
          entries: mount.tables.map((table) => ({
            name: table.name,
            type: "table",
            path: `/${mount.alias}/${table.name}`,
          })),
        },
        mountTruncated[mount.alias] ? { truncated: true } : {},
      );
    }

    if (endpoint === "describe" || endpoint === "count" || endpoint === "find") {
      const { mount, table } = resolve(params.path);
      if (!mount || !table) throw new DashboardError("E_NOT_FOUND", `table ${params.path}`);
      const key = `${mount.alias}/${table.name}`;

      if (endpoint === "describe") return ok(describeFor(mount, table));
      if (endpoint === "count" && countFailures[key]) {
        throw new DashboardError(countFailures[key].code, countFailures[key].message);
      }
      if (endpoint === "find" && findFailures[key]) {
        throw new DashboardError(findFailures[key].code, findFailures[key].message);
      }

      const where = params.where === undefined ? [] : [].concat(params.where);
      let rows = table.rows.filter((item) => where.every((expr) => matches(item, expr)));
      if (endpoint === "count") return ok({ count: rows.length });

      if (params.order) rows = sortRows(rows, params.order);
      const limit = Number(params.limit ?? 1000);
      const start = params.cursor ? Number(params.cursor) : 0;
      const end = Math.min(start + limit, rows.length);
      const truncated = end < rows.length;
      const page = rows.slice(start, end).map((item) => ({
        path: `/${mount.alias}/${table.name}/${item.id}`,
        fields: project(item, params.fields),
        etag: `sha256:${item.id}`,
      }));
      return ok(
        { path: params.path, rows: page },
        truncated ? { truncated: true, next_cursor: String(end) } : {},
      );
    }

    throw new Error(`endpoint inesperado: ${endpoint}`);
  };

  api.calls = calls;
  return api;
}

test("percentages suma exactamente 100 y con total 0 deja todo a cero", () => {
  const thirds = percentages([{ key: "a", count: 1 }, { key: "b", count: 1 }, { key: "c", count: 1 }]);
  assert.deepEqual(thirds.map((item) => item.pct), [34, 33, 33]);
  assert.equal(thirds.reduce((sum, item) => sum + item.pct, 0), 100);

  const mixed = percentages([{ key: "a", count: 1 }, { key: "b", count: 2 }]);
  assert.deepEqual(mixed.map((item) => item.pct), [33, 67]);
  assert.equal(mixed.reduce((sum, item) => sum + item.pct, 0), 100);

  const zero = percentages([{ key: "a", count: 0 }, { key: "b", count: 0 }]);
  assert.deepEqual(zero.map((item) => item.pct), [0, 0]);
  assert.deepEqual(percentages([]), []);
});

test("percentages deja null en los counts desconocidos y reparte 100 entre los conocidos", () => {
  const partial = percentages([{ count: 2 }, { count: null }, { count: 1 }]);
  assert.deepEqual(partial.map((item) => item.pct), [67, null, 33]);
  assert.equal(partial[0].pct + partial[2].pct, 100);

  const unknown = percentages([{ count: null }]);
  assert.deepEqual(unknown.map((item) => item.pct), [null]);
  assert.deepEqual(unknown.map((item) => item.count), [null]);
});

test("bucketCounts rellena solo los huecos del intervalo verificado, en UTC", () => {
  const buckets = bucketCounts(
    ["2026-01-01T10:00:00Z", "2026-01-03T23:59:00Z", "2026-01-03T00:00:00Z"],
    { granularity: "day" },
  );
  assert.deepEqual(buckets, [
    { start: "2026-01-01", count: 1 },
    { start: "2026-01-02", count: 0 },
    { start: "2026-01-03", count: 2 },
  ]);
  assert.deepEqual(bucketCounts([]), []);
  // Sin zona horaria no hay día UTC verificable; se descarta como inválida.
  assert.deepEqual(
    bucketCounts(["no-es-fecha", "", "2026-01-02T10:00:00", "2026-01-01T00:00:00Z"]),
    [{ start: "2026-01-01", count: 1 }],
  );
});

test("regroup mantiene el día, agrupa la semana ISO desde el lunes y conserva huecos", () => {
  const daily = [
    { start: "2026-01-01", count: 2 },
    { start: "2026-01-02", count: 0 },
  ];
  assert.deepEqual(regroup(daily, "day"), daily);

  const weeks = regroup(
    [
      { start: "2025-12-28", count: 1 },
      { start: "2026-01-12", count: 4 },
    ],
    "week",
  );
  assert.deepEqual(weeks, [
    { start: "2025-12-22", count: 1 },
    { start: "2025-12-29", count: 0 },
    { start: "2026-01-05", count: 0 },
    { start: "2026-01-12", count: 4 },
  ]);
});

test("regroup por mes rellena con cero y no extrapola el rango pedido", () => {
  const months = regroup(
    [
      { start: "2026-01-31", count: 2 },
      { start: "2026-03-01", count: 3 },
    ],
    "month",
  );
  assert.deepEqual(months, [
    { start: "2026-01", count: 2 },
    { start: "2026-02", count: 0 },
    { start: "2026-03", count: 3 },
  ]);
});

test("regroup conserva el total diario y rechaza granularidades o buckets inválidos", () => {
  const daily = [
    { start: "2026-01-28", count: 3 },
    { start: "2026-01-29", count: 0 },
    { start: "2026-01-30", count: 5 },
    { start: "2026-01-31", count: 1 },
    { start: "2026-02-01", count: 2 },
    { start: "2026-02-02", count: 4 },
  ];
  const sum = (items) => items.reduce((total, item) => total + item.count, 0);
  assert.equal(sum(daily), 15);
  assert.equal(sum(regroup(daily, "week")), 15);
  assert.equal(sum(regroup(daily, "month")), 15);
  assert.deepEqual(regroup([], "week"), []);

  assert.throws(() => regroup(daily, "year"), (error) => error.code === "E_OPTION");
  assert.throws(
    () => regroup([{ start: "2026-13-01", count: 0 }], "day"),
    (error) => error instanceof DashboardError && error.code === "E_OPTION",
  );
});

test("createDashboardApi rechaza endpoints de escritura antes de la petición", async () => {
  const calls = [];
  const api = createDashboardApi({
    fetchImpl: async (url, init) => {
      calls.push({ url, method: init.method });
      return { status: 200, json: async () => ok({ count: 3 }) };
    },
  });

  await assert.rejects(
    () => api("insert", { path: "/mem/sessions" }),
    (error) => error instanceof DashboardError && error.code === "E_UNSUPPORTED",
  );
  await assert.rejects(() => api("delete", { path: "/mem/sessions" }), /no permitida/);
  assert.equal(calls.length, 0);
  assert.deepEqual([...DASHBOARD_ENDPOINTS].sort(), ["count", "describe", "find", "ls"]);
});

test("createDashboardApi emite GET y repite las claves de los arrays", async () => {
  const calls = [];
  const api = createDashboardApi({
    fetchImpl: async (url, init) => {
      calls.push({ url, method: init.method });
      return { status: 200, json: async () => ok({ count: 3 }) };
    },
  });

  const body = await api("count", { path: "/mem/observations", where: ["status = active", "kind = decision"] });
  assert.equal(body.data.count, 3);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].method, "GET");
  const url = new URL(calls[0].url, "http://localhost");
  assert.equal(url.pathname, "/api/count");
  assert.deepEqual(url.searchParams.getAll("where"), ["status = active", "kind = decision"]);
});

test("createDashboardApi convierte un sobre ok:false y un fallo de red en DashboardError", async () => {
  const denied = createDashboardApi({
    fetchImpl: async () => ({
      status: 403,
      json: async () => ({ version: 1, ok: false, error: { code: "E_DENIED", message: "sin permiso" } }),
    }),
  });
  await assert.rejects(
    () => denied("count", { path: "/mem/observations" }),
    (error) => error instanceof DashboardError && error.code === "E_DENIED",
  );

  const offline = createDashboardApi({
    fetchImpl: async () => {
      throw new Error("offline");
    },
  });
  await assert.rejects(
    () => offline("ls", { path: "/" }),
    (error) => error instanceof DashboardError && error.code === "E_NETWORK",
  );
});

test("detectAcmMounts distingue ACM de no ACM y declara la ambigüedad", async () => {
  const acm = await detectAcmMounts(fakeApi());
  assert.deepEqual(acm, { aliases: ["mem"], error: null });

  const plain = await detectAcmMounts(
    fakeApi({ mounts: [{ alias: "docs", tables: [{ name: "notes", kind: "notes", rows: [] }] }] }),
  );
  assert.deepEqual(plain, { aliases: [], error: null });

  const two = await detectAcmMounts(fakeApi({ mounts: [acmMount("mem"), acmMount("memoria")] }));
  assert.deepEqual(two.aliases.sort(), ["mem", "memoria"]);
  assert.equal(two.error, null);
});

test("un montaje denegado durante la detección se declara E_ACM_PERMISSION", async () => {
  const api = fakeApi({ listFailures: { "/mem": { code: "E_DENIED", message: "sin permiso" } } });
  const detected = await detectAcmMounts(api);

  assert.deepEqual(detected.aliases, []);
  assert.equal(detected.error.code, "E_ACM_PERMISSION");
});

test("loadGeneralStats pide count sin where y cuadra los totales con el doble", async () => {
  const rows = {
    observations: [row("observations", "o1"), row("observations", "o2")],
  };
  const api = fakeApi({ mounts: [acmMount("mem", rows)] });
  const stats = await loadGeneralStats(api);

  const mount = stats.mounts.find((item) => item.alias === "mem");
  assert.equal(mount.error, null);
  assert.equal(mount.truncated, false);
  const observations = mount.tables.find((item) => item.name === "observations");
  assert.equal(observations.count, 2);
  assert.equal(stats.totals.mounts, 1);
  assert.equal(stats.totals.tables, 4);
  assert.equal(stats.totals.records, 1 + 2 + 1 + 1);
  assert.equal(stats.totals.accessibleMounts, 1);
  assert.equal(stats.totals.inaccessibleMounts, 0);
  assert.equal(stats.partial, false);

  const counts = api.calls.filter((call) => call.endpoint === "count");
  assert.equal(counts.length, 4);
  assert.ok(counts.every((call) => call.params.where === undefined));
});

test("un count fallido deja null, no 0, y anula el total", async () => {
  const api = fakeApi({
    countFailures: { "mem/observations": { code: "E_DENIED", message: "sin permiso" } },
  });
  const stats = await loadGeneralStats(api);

  const table = stats.mounts[0].tables.find((item) => item.name === "observations");
  assert.equal(table.count, null);
  assert.notEqual(table.count, 0);
  assert.deepEqual(table.error, { code: "E_DENIED", message: "sin permiso" });
  assert.equal(stats.totals.records, null);
  assert.equal(stats.partial, true);
  assert.ok(stats.errors.some((item) => item.scope === "count:/mem/observations"));
});

test("loadAcmStats calcula cada KPI con su count y excluye sesiones del conocimiento", async () => {
  const rows = {
    sessions: [row("sessions", "s1", { status: "open" }), row("sessions", "s2", { status: "closed" })],
    observations: [
      row("observations", "o1", { kind: "decision", status: "active" }),
      row("observations", "o2", { kind: "decision", status: "active" }),
      row("observations", "o3", { kind: "bugfix", status: "superseded" }),
      row("observations", "o4", { kind: "learning", status: "active" }),
    ],
    checkpoints: [row("checkpoints", "c1", { status: "active" })],
    preferences: [row("preferences", "p1", { status: "active" }), row("preferences", "p2", { status: "obsolete" })],
  };
  const api = fakeApi({ mounts: [acmMount("mem", rows)] });
  const stats = await loadAcmStats(api, { alias: "mem" });

  for (const kind of ACM_TABLES) {
    assert.equal(stats.kpis[kind], rows[kind].length);
  }
  assert.equal(stats.distribution.length, 4);
  assert.ok(stats.distribution.every((item) => item.label !== ""));
  assert.equal(stats.distributionTotal, 9);
  assert.deepEqual(stats.knowledge.map((item) => item.key), [
    "active",
    "superseded",
    "obsolete",
    "needs_review",
  ]);
  assert.deepEqual(stats.knowledge.map((item) => item.label), [
    "Activo",
    "Sustituido",
    "Obsoleto",
    "Pendiente de revisión",
  ]);
  assert.deepEqual(stats.knowledge.map((item) => item.count), [5, 1, 1, 0]);
  assert.deepEqual(stats.knowledge[0].byTable, { observations: 3, checkpoints: 1, preferences: 1 });
  assert.deepEqual(stats.knowledge[1].byTable, { observations: 1, checkpoints: 0, preferences: 0 });
  assert.deepEqual(stats.knowledge[2].byTable, { observations: 0, checkpoints: 0, preferences: 1 });
  assert.deepEqual(stats.knowledge[3].byTable, { observations: 0, checkpoints: 0, preferences: 0 });
  assert.equal(stats.knowledgeTotal, 7);

  const stateWheres = new Set(["active", "superseded", "obsolete", "needs_review"].map((state) => `status = ${state}`));
  const knowledgeCalls = api.calls.filter(
    (call) => call.endpoint === "count" && stateWheres.has(call.params.where),
  );
  assert.equal(knowledgeCalls.length, 12);
  assert.deepEqual(
    [...new Set(knowledgeCalls.map((call) => call.params.path))].sort(),
    ["/mem/checkpoints", "/mem/observations", "/mem/preferences"],
  );
  assert.deepEqual(stats.sessionStates.map((item) => item.key), ["open", "closed", "abandoned"]);
  assert.deepEqual(stats.sessionStates.map((item) => item.count), [1, 1, 0]);
  assert.equal(stats.sessionStatesTotal, 2);
  assert.equal(stats.partial, false);
  assert.equal(stats.recentSessions[0].id, "s2");
  assert.equal(stats.recentSessions[0].agentId, "agent");
  assert.equal(stats.recentSessions[0].endedAt, null);
  assert.equal(stats.recentSessions[0].path, "/mem/sessions/s2");
});

test("percentages sobre knowledge reparte 100 con el total conocido", async () => {
  const rows = {
    observations: [
      row("observations", "o1", { status: "active" }),
      row("observations", "o2", { status: "active" }),
      row("observations", "o3", { status: "active" }),
      row("observations", "o4", { status: "active" }),
      row("observations", "o5", { status: "active" }),
      row("observations", "o6", { status: "superseded" }),
    ],
    checkpoints: [row("checkpoints", "c1", { status: "active" })],
    preferences: [
      row("preferences", "p1", { status: "obsolete" }),
      row("preferences", "p2", { status: "needs_review" }),
    ],
  };
  const stats = await loadAcmStats(fakeApi({ mounts: [acmMount("mem", rows)] }), { alias: "mem" });

  assert.deepEqual(stats.knowledge.map((item) => item.count), [6, 1, 1, 1]);
  assert.equal(stats.knowledgeTotal, 9);
  const pcts = percentages(stats.knowledge);
  assert.deepEqual(pcts.map((item) => item.pct), [67, 11, 11, 11]);
  assert.equal(pcts.reduce((sum, item) => sum + item.pct, 0), 100);
});

test("observationKinds va en orden descendente y su total suma los seis kinds", async () => {
  const rows = {
    observations: [
      row("observations", "o1", { kind: "decision" }),
      row("observations", "o2", { kind: "decision" }),
      row("observations", "o3", { kind: "bugfix" }),
      row("observations", "o4", { kind: "learning" }),
      row("observations", "o5", { kind: "learning" }),
      row("observations", "o6", { kind: "learning" }),
      row("observations", "o7", { kind: "discovery" }),
    ],
  };
  const stats = await loadAcmStats(fakeApi({ mounts: [acmMount("mem", rows)] }), { alias: "mem" });

  assert.deepEqual(stats.observationKinds.map((item) => item.key), [
    "learning",
    "decision",
    "discovery",
    "bugfix",
    "pattern",
    "architecture",
  ]);
  assert.deepEqual(stats.observationKinds.map((item) => item.count), [3, 2, 1, 1, 0, 0]);
  assert.equal(stats.observationKindsTotal, 7);
});

test("timeline rellena con cero los huecos, no extrapola y declara las fechas inválidas", async () => {
  const rows = {
    sessions: [row("sessions", "s1", { created_at: "2026-01-01T00:00:00Z" })],
    observations: [
      row("observations", "o1", { created_at: "2026-01-01T12:00:00Z" }),
      row("observations", "o2", { created_at: "2026-01-03T12:00:00Z" }),
      row("observations", "o3", { created_at: "no-es-fecha" }),
    ],
    checkpoints: [row("checkpoints", "c1", { created_at: "" })],
    preferences: [row("preferences", "p1", { created_at: "2026-01-05T00:00:00Z" })],
  };
  const stats = await loadAcmStats(fakeApi({ mounts: [acmMount("mem", rows)] }), { alias: "mem" });

  assert.deepEqual(stats.timeline.buckets, [
    { start: "2026-01-01", count: 2 },
    { start: "2026-01-02", count: 0 },
    { start: "2026-01-03", count: 1 },
    { start: "2026-01-04", count: 0 },
    { start: "2026-01-05", count: 1 },
  ]);
  assert.equal(stats.timeline.invalidDates, 2);
  assert.equal(stats.timeline.from, "2026-01-01");
  assert.equal(stats.timeline.to, "2026-01-05");
  assert.equal(stats.timeline.timezone, "UTC");
  assert.equal(stats.timeline.truncated, false);
  assert.equal(stats.partial, false);
});

test("una tabla truncada marca partial y no interpola fuera de lo leído", async () => {
  const observations = Array.from({ length: 5 }, (_, index) =>
    row("observations", `o${index + 1}`, { created_at: `2026-02-0${index + 1}T00:00:00Z` }),
  );
  const api = fakeApi({ mounts: [acmMount("mem", { observations })] });
  const stats = await loadAcmStats(api, { alias: "mem", pageSize: 2, maxPages: 2 });

  assert.equal(stats.timeline.truncated, true);
  assert.equal(stats.partial, true);
  assert.equal(stats.timeline.to, "2026-02-04");
  const pages = api.calls.filter(
    (call) =>
      call.endpoint === "find" &&
      call.params.path === "/mem/observations" &&
      call.params.fields === "created_at",
  );
  assert.equal(pages.length, 2);
});

test("un count fallido en ACM deja el KPI en null y anula los totales", async () => {
  const api = fakeApi({
    countFailures: { "mem/observations": { code: "E_DENIED", message: "sin permiso" } },
  });
  const stats = await loadAcmStats(api, { alias: "mem" });

  assert.equal(stats.kpis.observations, null);
  assert.notEqual(stats.kpis.observations, 0);
  const active = stats.knowledge.find((item) => item.key === "active");
  assert.equal(active.byTable.observations, null);
  assert.equal(active.byTable.checkpoints, 1);
  assert.equal(active.count, null);
  assert.notEqual(active.count, 0);
  assert.equal(stats.knowledgeTotal, null);
  assert.equal(stats.observationKindsTotal, null);
  assert.equal(stats.partial, true);
  assert.ok(stats.errors.some((item) => item.scope === "count:knowledge:active:observations"));
});
