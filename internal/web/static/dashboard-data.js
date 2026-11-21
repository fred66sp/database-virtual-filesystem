/**
 * Datos de solo lectura del dashboard del Visual Explorer.
 *
 * Solo usa las rutas GET de db-ai (`ls`, `describe`, `find`, `count`): no hay
 * ninguna operación de escritura en esta capa. Reutiliza la detección ACM del
 * grafo (`detectAcmMemory`) para que ambos paneles declaren la misma memoria.
 *
 * Uso desde el navegador:
 *   import { createDashboardApi, loadGeneralStats, detectAcmMounts, loadAcmStats }
 *     from "./dashboard-data.js";
 *   const api = createDashboardApi();
 *   const general = await loadGeneralStats(api);
 *   const acm = await detectAcmMounts(api);
 *   if (acm.aliases.length > 0) { ... await loadAcmStats(api, { alias: acm.aliases[0] }); }
 */

import { ACM_TABLES, AcmGraphError, detectAcmMemory } from "./acm-graph-data.js";

export const DASHBOARD_ENDPOINTS = Object.freeze(["count", "describe", "find", "ls"]);

const ALLOWED_ENDPOINTS = new Set(DASHBOARD_ENDPOINTS);

// Límite del propio `ls` de db-ai (DESIGN §10).
const LIST_LIMIT = 1000;

const STORES = Object.freeze([
  { key: "sessions", label: "Sesiones" },
  { key: "observations", label: "Observaciones" },
  { key: "checkpoints", label: "Checkpoints" },
  { key: "preferences", label: "Preferencias" },
]);

// El conocimiento comparte modelo de estados; las sesiones quedan fuera.
const KNOWLEDGE_TABLES = Object.freeze(["observations", "checkpoints", "preferences"]);

const KNOWLEDGE_STATES = Object.freeze([
  { key: "active", label: "Activo" },
  { key: "superseded", label: "Sustituido" },
  { key: "obsolete", label: "Obsoleto" },
  { key: "needs_review", label: "Pendiente de revisión" },
]);

const SESSION_STATES = Object.freeze([
  { key: "open", label: "Abiertas" },
  { key: "closed", label: "Cerradas" },
  { key: "abandoned", label: "Abandonadas" },
]);

const OBSERVATION_KINDS = Object.freeze([
  "discovery",
  "decision",
  "pattern",
  "bugfix",
  "architecture",
  "learning",
]);

const TIMELINE_ORDER = "created_at:asc,id:asc";
const RECENT_SESSIONS_LIMIT = 8;
const GRANULARITIES = Object.freeze(["day", "week", "month"]);

// Un instante sin zona horaria no se puede agrupar en UTC sin inventarla: solo
// se aceptan fecha sola o instante con Z/offset explícito.
const ISO_DATE = /^\d{4}-\d{2}-\d{2}(?:T\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:\d{2}))?$/;

// La detección ACM distingue E_DENIED/E_NOT_FOUND por instanceof AcmGraphError:
// heredar de ella mantiene ese comportamiento sin duplicar la clase.
export class DashboardError extends AcmGraphError {
  constructor(code, message, options = {}) {
    super(code, message, options);
    this.name = "DashboardError";
  }
}

/** Cliente de lectura del dashboard: sobre completo de db-ai, siempre GET. */
export function createDashboardApi({ fetchImpl, signal, basePath = "/api" } = {}) {
  const doFetch = fetchImpl ?? globalThis.fetch;
  if (typeof doFetch !== "function") {
    throw new DashboardError("E_ENV", "Este entorno no ofrece fetch para leer db-ai.");
  }

  return async function api(endpoint, params = {}) {
    if (!ALLOWED_ENDPOINTS.has(endpoint)) {
      throw new DashboardError(
        "E_UNSUPPORTED",
        `Operación no permitida por el dashboard: ${endpoint}. Solo se lee con ${DASHBOARD_ENDPOINTS.join(", ")}.`,
      );
    }

    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(params)) {
      if (Array.isArray(value)) {
        for (const item of value) {
          if (item !== undefined && item !== null && item !== "") query.append(key, String(item));
        }
      } else if (value !== undefined && value !== null && value !== "") {
        query.append(key, String(value));
      }
    }
    const qs = query.toString();
    const url = `${basePath}/${endpoint}${qs ? `?${qs}` : ""}`;

    let response;
    try {
      response = await doFetch(url, {
        method: "GET",
        headers: { Accept: "application/json" },
        signal,
      });
    } catch (cause) {
      if (cause?.name === "AbortError" || signal?.aborted) {
        throw new DashboardError("E_ABORTED", "Lectura del dashboard cancelada.", { cause });
      }
      throw new DashboardError("E_NETWORK", "No se pudo contactar con db-ai.", { cause });
    }

    let body;
    try {
      body = await response.json();
    } catch (cause) {
      throw new DashboardError(
        "E_HTTP",
        `db-ai devolvió una respuesta no JSON (HTTP ${response.status}) al leer ${endpoint}.`,
        { cause },
      );
    }

    if (!body || body.ok !== true) {
      const code = body?.error?.code ?? `E_HTTP_${response.status}`;
      const detail = body?.error?.message ?? `HTTP ${response.status}`;
      throw new DashboardError(code, `db-ai rechazó la lectura: ${detail}`, {
        details: { endpoint, params },
      });
    }
    return body;
  };
}

/**
 * Recuento por montaje y tabla. Los counts se piden sin `where` y sin filas.
 * Un count fallido queda en null: el total de registros solo se declara cuando
 * se conocen todas las tablas.
 */
export async function loadGeneralStats(api) {
  const errors = [];
  let partial = false;

  const root = await listEntries(api, "/", "los montajes");
  if (root.truncated) {
    partial = true;
    errors.push({
      scope: "mounts",
      code: "E_TRUNCATED",
      message: "El listado de montajes quedó truncado; faltan montajes por contabilizar.",
    });
  }

  const mounts = [];
  let tableCount = 0;
  let recordCount = 0;
  let countFailures = 0;

  for (const entry of root.entries.filter((item) => item.type === "mount")) {
    const mount = { alias: entry.name, path: entry.path, tables: [], error: null, truncated: false };

    let listing;
    try {
      listing = await listEntries(api, entry.path, `las tablas de ${entry.path}`);
    } catch (cause) {
      if (isAborted(cause)) throw cause;
      mount.error = errorInfo(cause);
      partial = true;
      errors.push({ scope: `mount:${entry.name}`, ...mount.error });
      mounts.push(mount);
      continue;
    }

    mount.truncated = listing.truncated;
    if (listing.truncated) {
      partial = true;
      errors.push({
        scope: `tables:${entry.name}`,
        code: "E_TRUNCATED",
        message: `El listado de tablas de ${entry.path} quedó truncado.`,
      });
    }

    for (const item of listing.entries.filter((candidate) => candidate.type === "table")) {
      const table = { name: item.name, path: item.path, count: null, error: null };
      try {
        table.count = await countRows(api, item.path);
        recordCount += table.count;
      } catch (cause) {
        if (isAborted(cause)) throw cause;
        table.error = errorInfo(cause);
        countFailures += 1;
        partial = true;
        errors.push({ scope: `count:${item.path}`, ...table.error });
      }
      mount.tables.push(table);
      tableCount += 1;
    }

    mounts.push(mount);
  }

  const inaccessibleMounts = mounts.filter((mount) => mount.error !== null).length;
  const unknown =
    countFailures > 0 || inaccessibleMounts > 0 || root.truncated || mounts.some((mount) => mount.truncated);

  return {
    mounts,
    totals: {
      mounts: mounts.length,
      tables: tableCount,
      records: unknown ? null : recordCount,
      accessibleMounts: mounts.length - inaccessibleMounts,
      inaccessibleMounts,
    },
    partial,
    errors,
  };
}

/**
 * Montajes con memoria ACM. La ausencia de ACM es un resultado, no un error:
 * aliases vacío significa «no hay ACM», y error queda solo para fallos reales
 * (permisos, detección truncada).
 */
export async function detectAcmMounts(api) {
  try {
    const detected = await detectAcmMemory(api);
    return { aliases: [detected.alias], error: null };
  } catch (cause) {
    if (cause instanceof AcmGraphError && cause.code === "E_ACM_AMBIGUOUS") {
      return { aliases: cause.details?.aliases ?? [], error: null };
    }
    if (cause instanceof AcmGraphError && cause.code === "E_ACM_NOT_FOUND") {
      return { aliases: [], error: null };
    }
    return { aliases: [], error: errorInfo(cause) };
  }
}

/** Agregados de la memoria ACM. Cada cifra procede de un count o find reales. */
export async function loadAcmStats(api, { alias, pageSize = 1000, maxPages = 4 } = {}) {
  const size = normalizePageSize(pageSize);
  const pages = normalizeMaxPages(maxPages);
  const detected = await detectAcmMemory(api, { alias });

  const errors = [];
  let partial = false;

  const countSafe = async (scope, path, where) => {
    try {
      return await countRows(api, path, where);
    } catch (cause) {
      if (isAborted(cause)) throw cause;
      const info = errorInfo(cause);
      errors.push({ scope, ...info });
      partial = true;
      return null;
    }
  };

  const kpis = {};
  for (const kind of ACM_TABLES) {
    kpis[kind] = await countSafe(`count:${kind}`, detected.tables[kind].path);
  }

  const distribution = STORES.map(({ key, label }) => ({ key, label, count: kpis[key] }));
  const distributionTotal = sumOrNull(distribution);

  const knowledge = [];
  for (const { key, label } of KNOWLEDGE_STATES) {
    const byTable = {};
    for (const table of KNOWLEDGE_TABLES) {
      byTable[table] = await countSafe(
        `count:knowledge:${key}:${table}`,
        detected.tables[table].path,
        `status = ${key}`,
      );
    }
    const counts = Object.values(byTable);
    // Un estado solo se declara cuando sus tres tablas respondieron.
    const count = counts.includes(null) ? null : counts.reduce((sum, value) => sum + value, 0);
    knowledge.push({ key, label, count, byTable });
  }
  const knowledgeTotal = sumOrNull(knowledge);

  const sessionStates = [];
  for (const { key, label } of SESSION_STATES) {
    const count = await countSafe(
      `count:state:${key}`,
      detected.tables.sessions.path,
      `status = ${key}`,
    );
    sessionStates.push({ key, label, count });
  }
  const sessionStatesTotal = sumOrNull(sessionStates);

  const observationKinds = [];
  for (const kind of OBSERVATION_KINDS) {
    const count = await countSafe(
      `count:kind:${kind}`,
      detected.tables.observations.path,
      `kind = ${kind}`,
    );
    observationKinds.push({ key: kind, count });
  }
  // Descendente; los fallidos (null) van al final y los empates conservan el
  // orden canónico de los seis kinds.
  observationKinds.sort((a, b) => (b.count ?? -1) - (a.count ?? -1));
  const observationKindsTotal = sumOrNull(observationKinds);

  const dates = [];
  let invalidDates = 0;
  let timelineTruncated = false;
  // Las cuatro tablas ACM, incluidas las sesiones: el donut de distribución usa el mismo alcance.
  for (const kind of ACM_TABLES) {
    try {
      const result = await collectCreatedDates(api, detected.tables[kind].path, size, pages);
      dates.push(...result.dates);
      invalidDates += result.invalidDates;
      if (result.truncated) timelineTruncated = true;
    } catch (cause) {
      if (isAborted(cause)) throw cause;
      errors.push({ scope: `find:timeline:${kind}`, ...errorInfo(cause) });
      partial = true;
    }
  }
  if (timelineTruncated) partial = true;

  const buckets = bucketCounts(dates, { granularity: "day" });
  const timeline = {
    buckets,
    granularity: "day",
    truncated: timelineTruncated,
    from: buckets.length > 0 ? buckets[0].start : null,
    to: buckets.length > 0 ? buckets[buckets.length - 1].start : null,
    timezone: "UTC",
    invalidDates,
  };

  let recentSessions = [];
  try {
    const body = await api("find", {
      path: detected.tables.sessions.path,
      order: "started_at:desc,id:desc",
      limit: RECENT_SESSIONS_LIMIT,
      fields: "id,goal,agent_id,status,started_at,ended_at",
    });
    const rows = body.data?.rows;
    if (!Array.isArray(rows)) {
      throw new DashboardError(
        "E_DATA",
        `db-ai no devolvió filas para ${detected.tables.sessions.path}.`,
      );
    }
    recentSessions = rows.map((row) => {
      const fields = row?.fields ?? {};
      return {
        id: textValue(fields.id),
        path: typeof row?.path === "string" ? row.path : "",
        goal: textValue(fields.goal),
        agentId: textValue(fields.agent_id),
        status: textValue(fields.status),
        startedAt: textValue(fields.started_at),
        endedAt: textValue(fields.ended_at) || null,
      };
    });
  } catch (cause) {
    if (isAborted(cause)) throw cause;
    errors.push({ scope: "find:recentSessions", ...errorInfo(cause) });
    partial = true;
  }

  return {
    alias: detected.alias,
    kpis,
    distribution,
    distributionTotal,
    knowledge,
    knowledgeTotal,
    sessionStates,
    sessionStatesTotal,
    observationKinds,
    observationKindsTotal,
    timeline,
    recentSessions,
    partial,
    errors,
  };
}

/**
 * Porcentajes enteros por mayor resto: los counts conocidos suman 100. Un
 * `count: null` es dato desconocido y conserva `pct: null` sin entrar en el
 * denominador; si no queda ninguno conocido, todos los porcentajes son null.
 */
export function percentages(items) {
  if (!Array.isArray(items)) {
    throw new DashboardError("E_DATA", "percentages espera una lista de elementos.");
  }
  const counts = items.map((item) => {
    const count = item?.count;
    if (count === null) return null;
    if (!Number.isFinite(count) || count < 0) {
      throw new DashboardError("E_DATA", `percentages recibió un count inválido: ${String(count)}.`);
    }
    return count;
  });
  const known = counts
    .map((count, index) => ({ index, count }))
    .filter((entry) => entry.count !== null);
  const pct = counts.map(() => null);

  const total = known.reduce((sum, entry) => sum + entry.count, 0);
  if (total === 0) {
    for (const { index } of known) pct[index] = 0;
    return items.map((item, index) => ({ ...item, pct: pct[index] }));
  }

  for (const { index, count } of known) {
    pct[index] = Math.floor((count * 100) / total);
  }
  const left = 100 - known.reduce((sum, entry) => sum + pct[entry.index], 0);
  const byRemainder = known
    .map((entry) => ({ index: entry.index, rest: (entry.count * 100) % total }))
    .sort((a, b) => b.rest - a.rest || a.index - b.index);
  for (let i = 0; i < left; i += 1) {
    pct[byRemainder[i].index] += 1;
  }
  return items.map((item, index) => ({ ...item, pct: pct[index] }));
}

/**
 * Buckets contiguos por día UTC entre la fecha menor y la mayor verificadas.
 * Las fechas inválidas no generan buckets; con `from`/`to` explícitos tampoco
 * se extrapola fuera del intervalo pedido.
 */
export function bucketCounts(dates, { granularity = "day", from, to } = {}) {
  if (granularity !== "day") {
    throw new DashboardError(
      "E_OPTION",
      `Granularidad no soportada: ${String(granularity)}; esperaba "day".`,
    );
  }
  if (!Array.isArray(dates)) {
    throw new DashboardError("E_DATA", "bucketCounts espera una lista de fechas.");
  }

  const counts = new Map();
  for (const value of dates) {
    const day = dayOf(value);
    if (day === null) continue;
    counts.set(day, (counts.get(day) ?? 0) + 1);
  }

  const explicitFrom = from === undefined || from === null ? null : dayOf(from);
  const explicitTo = to === undefined || to === null ? null : dayOf(to);
  if ((from !== undefined && from !== null && explicitFrom === null) ||
      (to !== undefined && to !== null && explicitTo === null)) {
    throw new DashboardError("E_OPTION", "El intervalo del bucket no es una fecha ISO 8601 válida.");
  }

  const days = [...counts.keys()].sort();
  const first = explicitFrom ?? days[0];
  const last = explicitTo ?? days[days.length - 1];
  if (first === undefined || last === undefined) return [];
  if (first > last) {
    throw new DashboardError("E_OPTION", `El intervalo del bucket está invertido: ${first} > ${last}.`);
  }

  const buckets = [];
  for (let time = utcDay(first); time <= utcDay(last); time += 86_400_000) {
    const start = new Date(time).toISOString().slice(0, 10);
    buckets.push({ start, count: counts.get(start) ?? 0 });
  }
  return buckets;
}

/**
 * Reagrupa una serie diaria contigua en día, semana ISO (lunes) o mes. El
 * intervalo va del primer al último bucket de entrada: los ceros dentro del
 * rango se conservan y no se extrapola fuera de él.
 */
export function regroup(buckets, granularity) {
  if (!GRANULARITIES.includes(granularity)) {
    throw new DashboardError(
      "E_OPTION",
      `Granularidad no soportada: ${String(granularity)}; esperaba day, week o month.`,
    );
  }
  if (!Array.isArray(buckets)) {
    throw new DashboardError("E_OPTION", "regroup espera una lista de buckets diarios.");
  }
  if (buckets.length === 0) return [];

  const daily = buckets.map((bucket) => {
    const day = dayOf(bucket?.start);
    const count = bucket?.count;
    if (day === null || day !== bucket?.start || !Number.isInteger(count) || count < 0) {
      throw new DashboardError("E_OPTION", `Bucket diario inválido: ${JSON.stringify(bucket)}.`);
    }
    return { day, count };
  });

  if (granularity === "day") return daily.map(({ day, count }) => ({ start: day, count }));

  const counts = new Map();
  for (const { day, count } of daily) {
    const start = bucketStart(day, granularity);
    counts.set(start, (counts.get(start) ?? 0) + count);
  }

  const first = bucketStart(daily[0].day, granularity);
  const last = bucketStart(daily[daily.length - 1].day, granularity);
  const out = [];

  if (granularity === "week") {
    const end = utcDay(last);
    for (let time = utcDay(first); time <= end; time += 7 * 86_400_000) {
      const start = new Date(time).toISOString().slice(0, 10);
      out.push({ start, count: counts.get(start) ?? 0 });
    }
    return out;
  }

  let year = Number(first.slice(0, 4));
  let month = Number(first.slice(5, 7));
  for (;;) {
    const start = `${String(year).padStart(4, "0")}-${String(month).padStart(2, "0")}`;
    if (start > last) return out;
    out.push({ start, count: counts.get(start) ?? 0 });
    month += 1;
    if (month > 12) {
      month = 1;
      year += 1;
    }
  }
}

/** Primer día (lunes ISO) o primer mes del bucket que contiene `day`. */
function bucketStart(day, granularity) {
  if (granularity === "month") return day.slice(0, 7);
  const time = utcDay(day);
  const offset = (new Date(time).getUTCDay() + 6) % 7; // lunes = 0
  return new Date(time - offset * 86_400_000).toISOString().slice(0, 10);
}

async function listEntries(api, path, what) {
  const body = await api("ls", { path, limit: LIST_LIMIT });
  const entries = body.data?.entries;
  if (!Array.isArray(entries)) {
    throw new DashboardError("E_DATA", `db-ai no devolvió una lista para ${what}.`);
  }
  return { entries, truncated: body.meta?.truncated === true };
}

async function countRows(api, path, where) {
  const body = await api("count", where === undefined ? { path } : { path, where });
  const count = body.data?.count;
  if (!Number.isInteger(count) || count < 0) {
    throw new DashboardError("E_DATA", `db-ai no devolvió un count válido para ${path}.`);
  }
  return count;
}

async function collectCreatedDates(api, path, pageSize, maxPages) {
  const dates = [];
  let invalidDates = 0;
  let cursor = "";

  for (let page = 1; page <= maxPages; page += 1) {
    const params = {
      path,
      fields: "created_at",
      order: TIMELINE_ORDER,
      limit: pageSize,
    };
    if (cursor !== "") params.cursor = cursor;
    const body = await api("find", params);
    const rows = body.data?.rows;
    if (!Array.isArray(rows)) {
      throw new DashboardError("E_DATA", `db-ai no devolvió filas para ${path}.`);
    }
    for (const row of rows) {
      const value = textValue(row?.fields?.created_at);
      if (dayOf(value) === null) invalidDates += 1;
      else dates.push(value);
    }

    const truncated = body.meta?.truncated === true;
    if (!truncated) return { dates, invalidDates, truncated: false };
    const next = typeof body.meta?.next_cursor === "string" ? body.meta.next_cursor : "";
    // Sin cursor de continuación la tabla no se puede completar: es una
    // truncación declarada, no una lectura completa.
    if (page >= maxPages || next === "") return { dates, invalidDates, truncated: true };
    cursor = next;
  }

  return { dates, invalidDates, truncated: false };
}

function normalizePageSize(value) {
  const size = Number(value);
  if (!Number.isInteger(size) || size < 1 || size > 1000) {
    throw new DashboardError(
      "E_OPTION",
      `pageSize debe ser un entero entre 1 y 1000; recibido: ${String(value)}.`,
    );
  }
  return size;
}

function normalizeMaxPages(value) {
  const pages = Number(value);
  if (!Number.isInteger(pages) || pages < 1) {
    throw new DashboardError("E_OPTION", `maxPages debe ser un entero positivo; recibido: ${String(value)}.`);
  }
  return pages;
}

function sumOrNull(items) {
  let total = 0;
  for (const item of items) {
    if (item.count === null) return null;
    total += item.count;
  }
  return total;
}

function textValue(value) {
  if (typeof value === "string") return value;
  if (value && typeof value === "object" && typeof value.data === "string") return value.data;
  return "";
}

function utcDay(day) {
  const [year, month, date] = day.split("-").map(Number);
  return Date.UTC(year, month - 1, date);
}

/** Día UTC de una fecha ISO 8601 válida; null si es inválida o vacía. */
function dayOf(value) {
  if (value instanceof Date) {
    return Number.isNaN(value.getTime()) ? null : value.toISOString().slice(0, 10);
  }
  if (typeof value !== "string" || !ISO_DATE.test(value)) return null;
  const [year, month, date] = value.slice(0, 10).split("-").map(Number);
  // Date.parse acepta 2026-02-30 rodando al mes siguiente: se comprueba el
  // calendario antes de confiar en él.
  const calendar = new Date(Date.UTC(year, month - 1, date));
  if (
    calendar.getUTCFullYear() !== year ||
    calendar.getUTCMonth() !== month - 1 ||
    calendar.getUTCDate() !== date
  ) {
    return null;
  }
  const time = Date.parse(value);
  if (Number.isNaN(time)) return null;
  return new Date(time).toISOString().slice(0, 10);
}

function errorInfo(cause) {
  if (cause instanceof AcmGraphError) {
    return { code: cause.code, message: cause.message };
  }
  return {
    code: "E_UNKNOWN",
    message: `Error inesperado del dashboard: ${String(cause?.message ?? cause)}`,
  };
}

function isAborted(cause) {
  return cause instanceof DashboardError && cause.code === "E_ABORTED";
}
