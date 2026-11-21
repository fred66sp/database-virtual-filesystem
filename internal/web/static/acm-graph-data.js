/**
 * Adaptador de datos del Grafo ACM para el Visual Explorer.
 *
 * Solo lee de la API GET de db-ai (`ls`, `describe` y `find`): no conoce ni
 * invoca ninguna operación de escritura. Detecta la memoria ACM por estructura
 * (tablas, columnas, tipos y clave primaria), no por el alias del montaje, y
 * devuelve una transformación pura en nodos y aristas.
 *
 * Uso desde Vue (script type="module"):
 *   import { loadAcmGraph, expandAcmGraph } from "./acm-graph-data.js";
 *   const graph = await loadAcmGraph({ pageSize: 200 });
 *   graph.alias; graph.nodes; graph.edges; graph.unresolved;
 *   graph.tables.sessions.truncated; graph.tables.sessions.nextCursor;
 *   const more = await expandAcmGraph(graph, "observations");
 *
 * `pageSize` es explícito y acotado (1..1000, 200 por defecto): una página por
 * tabla. `pageCounts` restaura un número explícito de páginas por tabla (máximo
 * MAX_PAGES_PER_TABLE) al actualizar. Las tablas se leen con
 * `created_at:desc,id:desc`: las filas nuevas quedan en la primera página y el
 * cursor conserva ese orden. La expansión siempre lleva el cursor de la propia
 * tabla. `source` es estado opaco para `expandAcmGraph`; Vue no necesita leerlo.
 */

export const ACM_TABLES = Object.freeze([
  "sessions",
  "observations",
  "checkpoints",
  "preferences",
]);

export const DEFAULT_PAGE_SIZE = 200;
export const MAX_PAGE_SIZE = 1000;
export const MAX_PAGES_PER_TABLE = 10;

// Orden estable y descendente por fecha para que las observaciones nuevas
// aparezcan en la primera página. `id:desc` desempata por UUIDv7 (mayor = más
// reciente) sin depender del orden nativo. El cursor queda ligado a este orden.
export const ACM_ORDERS = Object.freeze({
  sessions: "created_at:desc,id:desc",
  observations: "created_at:desc,id:desc",
  checkpoints: "created_at:desc,id:desc",
  preferences: "created_at:desc,id:desc",
});

// Límite del propio `ls` de db-ai (DESIGN §10). La detección no lo oculta: si
// trunca, se rechaza con un error explícito en lugar de afirmar que no hay ACM.
const DETECTION_LIST_LIMIT = 1000;

const READ_ENDPOINTS = new Set(["ls", "describe", "find"]);

// Estructura mínima de ACM: columna -> tipo declarado. Las columnas extra se
// toleran; las que faltan o cambian de tipo hacen que el montaje no sea ACM.
const ACM_COLUMNS = Object.freeze({
  sessions: Object.freeze({
    id: "TEXT",
    agent_id: "TEXT",
    source: "TEXT",
    goal: "TEXT",
    status: "TEXT",
    started_at: "TEXT",
    created_at: "TEXT",
    updated_at: "TEXT",
  }),
  observations: Object.freeze({
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
  }),
  checkpoints: Object.freeze({
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
  }),
  preferences: Object.freeze({
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
  }),
});

export class AcmGraphError extends Error {
  constructor(code, message, options = {}) {
    super(message, { cause: options.cause });
    this.name = "AcmGraphError";
    this.code = code;
    if (options.details !== undefined) this.details = options.details;
  }
}

/**
 * Cliente mínimo de lectura. `api(endpoint, params)` devuelve el sobre completo
 * de db-ai ({version, ok, data, meta}) y lanza AcmGraphError si no es correcto.
 */
export function createAcmApi({ fetchImpl, signal, basePath = "/api" } = {}) {
  const doFetch = fetchImpl ?? globalThis.fetch;
  if (typeof doFetch !== "function") {
    throw new AcmGraphError("E_ENV", "Este entorno no ofrece fetch para leer db-ai.");
  }

  return async function api(endpoint, params = {}) {
    if (!READ_ENDPOINTS.has(endpoint)) {
      throw new AcmGraphError(
        "E_UNSUPPORTED",
        `Operación no permitida por el adaptador ACM: ${endpoint}. Solo se lee con ls, describe y find.`,
      );
    }

    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(params)) {
      if (value !== undefined && value !== null && value !== "") {
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
        throw new AcmGraphError("E_ABORTED", "Lectura de la memoria ACM cancelada.", { cause });
      }
      throw new AcmGraphError(
        "E_NETWORK",
        "No se pudo contactar con db-ai para leer la memoria ACM.",
        { cause },
      );
    }

    let body;
    try {
      body = await response.json();
    } catch (cause) {
      throw new AcmGraphError(
        "E_HTTP",
        `db-ai devolvió una respuesta no JSON (HTTP ${response.status}) al leer ${endpoint}.`,
        { cause },
      );
    }

    if (!body || body.ok !== true) {
      const code = body?.error?.code ?? `E_HTTP_${response.status}`;
      const detail = body?.error?.message ?? `HTTP ${response.status}`;
      throw new AcmGraphError(code, `db-ai rechazó la lectura: ${detail}`, {
        details: { endpoint, params },
      });
    }
    return body;
  };
}

/** Valida y normaliza el tamaño de página: nunca hay un límite implícito. */
export function normalizePageSize(value) {
  if (value === undefined || value === null) return DEFAULT_PAGE_SIZE;
  const size = Number(value);
  if (!Number.isInteger(size) || size < 1 || size > MAX_PAGE_SIZE) {
    throw new AcmGraphError(
      "E_OPTION",
      `pageSize debe ser un entero entre 1 y ${MAX_PAGE_SIZE}; recibido: ${String(value)}.`,
    );
  }
  return size;
}

/** Páginas por tabla: explícito, acotado y con una sola consulta por página. */
export function normalizePageCount(value, kind = "") {
  if (value === undefined || value === null) return 1;
  const count = Number(value);
  if (!Number.isInteger(count) || count < 1 || count > MAX_PAGES_PER_TABLE) {
    throw new AcmGraphError(
      "E_OPTION",
      `pageCount de «${kind || "tabla"}» debe ser un entero entre 1 y ${MAX_PAGES_PER_TABLE}; ` +
        `recibido: ${String(value)}.`,
    );
  }
  return count;
}

/**
 * Localiza la memoria ACM entre los montajes de db-ai y valida su estructura.
 * `api` es el cliente de createAcmApi; `alias` solo es necesario si hay más de
 * una memoria ACM montada.
 */
export async function detectAcmMemory(api, { alias } = {}) {
  const root = await listEntries(api, "/", "los montajes");
  if (root.truncated) throw detectionTruncated("los montajes");

  const mounts = root.entries.filter((entry) => entry.type === "mount");
  const candidates = [];
  const skipped = [];

  for (const mount of mounts) {
    let listing;
    try {
      listing = await listEntries(api, mount.path, `las tablas de ${mount.path}`);
    } catch (cause) {
      if (cause instanceof AcmGraphError && cause.code === "E_DENIED") {
        skipped.push({ path: mount.path, reason: "denied" });
        continue;
      }
      if (cause instanceof AcmGraphError && cause.code === "E_NOT_FOUND") {
        skipped.push({ path: mount.path, reason: "not-found" });
        continue;
      }
      throw cause;
    }
    if (listing.truncated) {
      skipped.push({ path: mount.path, reason: "truncated" });
      continue;
    }

    const tables = new Map(
      listing.entries
        .filter((entry) => entry.type === "table")
        .map((entry) => [entry.name.toLowerCase(), entry]),
    );
    if (ACM_TABLES.every((kind) => tables.has(kind))) {
      candidates.push({ alias: mount.name, tables });
    }
  }

  if (candidates.length === 0) {
    if (skipped.some((item) => item.reason === "denied")) {
      throw new AcmGraphError(
        "E_ACM_PERMISSION",
        "No se pudo comprobar si existe la memoria ACM: algún montaje de db-ai deniega la lectura.",
        { details: { alias: alias ?? null, skipped } },
      );
    }
    if (skipped.some((item) => item.reason === "truncated")) {
      throw new AcmGraphError(
        "E_ACM_DETECTION_LIMIT",
        `La detección de ACM quedó truncada al listar montajes o tablas (límite ${DETECTION_LIST_LIMIT}); ` +
          "no se puede afirmar que la memoria no exista.",
        { details: { alias: alias ?? null, skipped } },
      );
    }
    throw new AcmGraphError(
      "E_ACM_NOT_FOUND",
      "No se encontró la memoria ACM: ningún montaje de db-ai contiene las tablas " +
        "sessions, observations, checkpoints y preferences con la estructura esperada.",
      { details: { alias: alias ?? null } },
    );
  }
  if (candidates.length > 1 && alias === undefined) {
    throw new AcmGraphError(
      "E_ACM_AMBIGUOUS",
      `Hay más de una memoria ACM montada (${candidates.map((c) => c.alias).join(", ")}); ` +
        "indica el alias que se quiere leer.",
      { details: { aliases: candidates.map((c) => c.alias) } },
    );
  }

  const candidate =
    alias === undefined ? candidates[0] : candidates.find((c) => c.alias === alias);
  if (!candidate) {
    throw new AcmGraphError(
      "E_ACM_NOT_FOUND",
      `No se encontró la memoria ACM en el montaje «${alias}»: no contiene las cuatro tablas ` +
        "con la estructura esperada.",
      { details: { alias } },
    );
  }

  const tables = {};
  const problems = [];
  for (const kind of ACM_TABLES) {
    const entry = candidate.tables.get(kind);
    let described;
    try {
      const body = await api("describe", { path: entry.path });
      described = body.data;
    } catch (cause) {
      if (cause instanceof AcmGraphError && cause.code === "E_NOT_FOUND") {
        problems.push(`la tabla «${entry.name}» desapareció al describirla`);
        continue;
      }
      if (cause instanceof AcmGraphError && cause.code === "E_DENIED") {
        throw new AcmGraphError(
          "E_ACM_PERMISSION",
          `La memoria ACM del montaje «${candidate.alias}» no es legible: ` +
            `la tabla «${entry.name}» deniega la lectura.`,
          { cause },
        );
      }
      throw cause;
    }
    problems.push(...structureProblems(kind, described));
    tables[kind] = { kind, table: entry.name, path: entry.path, describe: described };
  }

  if (problems.length > 0) {
    throw new AcmGraphError(
      "E_ACM_NOT_FOUND",
      "No se encontró una memoria ACM válida: las tablas localizadas no tienen la " +
        "estructura esperada de ACM.",
      { details: { alias: candidate.alias, problems } },
    );
  }

  for (const kind of ACM_TABLES) {
    if (tables[kind].describe?.capabilities?.readable !== true) {
      throw new AcmGraphError(
        "E_ACM_PERMISSION",
        `La memoria ACM del montaje «${candidate.alias}» no es legible: ` +
          `la tabla «${tables[kind].table}» no concede permiso de lectura.`,
        { details: { alias: candidate.alias, table: tables[kind].table } },
      );
    }
  }

  return { alias: candidate.alias, tables };
}

/**
 * Lee una sola página de una tabla. Sin cursor lee la primera; con cursor pide
 * exactamente la siguiente, sin recorrer la tabla en silencio.
 */
export async function loadAcmPage(api, { path, cursor = "", limit = DEFAULT_PAGE_SIZE, order = "" } = {}) {
  const pageSize = normalizePageSize(limit);
  const params = {
    path,
    limit: pageSize,
    cursor: cursor === "" ? undefined : cursor,
  };
  if (order !== "") params.order = order;
  const body = await api("find", params);

  const rows = body.data?.rows;
  if (!Array.isArray(rows)) {
    throw new AcmGraphError("E_DATA", `db-ai no devolvió filas para ${path}.`);
  }
  const meta = body.meta ?? {};
  return {
    cursor,
    rows,
    truncated: meta.truncated === true,
    nextCursor: typeof meta.next_cursor === "string" ? meta.next_cursor : "",
  };
}

/**
 * Carga inicial: detecta ACM, lee las páginas pedidas por tabla y construye el
 * grafo. `pageCounts` restaura el número de páginas ya cargadas tras actualizar
 * (1 por defecto, máximo MAX_PAGES_PER_TABLE por tabla). Los errores de lectura
 * quedan en `tables[kind].error`; las páginas ya leídas no se descartan.
 */
export async function loadAcmGraph({
  api,
  pageSize,
  alias,
  fetchImpl,
  signal,
  pageCounts,
} = {}) {
  const client = api ?? createAcmApi({ fetchImpl, signal });
  const size = normalizePageSize(pageSize);
  const detected = await detectAcmMemory(client, { alias });
  const tables = {};

  for (const kind of ACM_TABLES) {
    const source = detected.tables[kind];
    const target = normalizePageCount(pageCounts?.[kind], kind);
    const order = ACM_ORDERS[kind] ?? "";
    let pages = [];
    let error = null;
    try {
      pages = [await loadAcmPage(client, { path: source.path, limit: size, order })];
      while (pages.length < target) {
        const last = pages[pages.length - 1];
        if (!last.truncated || !last.nextCursor) break;
        pages.push(
          await loadAcmPage(client, {
            path: source.path,
            cursor: last.nextCursor,
            limit: size,
            order,
          }),
        );
      }
    } catch (cause) {
      if (cause instanceof AcmGraphError && cause.code === "E_ABORTED") throw cause;
      error = errorInfo(cause);
    }
    tables[kind] = { ...source, pages, error, order };
  }

  return buildAcmGraph({
    alias: detected.alias,
    limits: {
      pageSize: size,
      maxPageSize: MAX_PAGE_SIZE,
      maxPagesPerTable: MAX_PAGES_PER_TABLE,
      detectionListLimit: DETECTION_LIST_LIMIT,
    },
    tables,
  });
}

/**
 * Transformación pura. Recibe las páginas crudas ya leídas y devuelve:
 *  - nodes: un nodo por registro real, id `tabla:ID`, ruta db-ai del propio row.
 *  - edges: solo relaciones con ambos nodos cargados. `session_id` va de la
 *    sesión al registro que la referencia; `superseded_by`, del antiguo al nuevo.
 *  - unresolved: referencias sin arista (no entran en el force-graph), agrupadas
 *    por destino, con recuento, `pending` (aún no cargado) frente a `absent`
 *    (la tabla está completa y el destino no existe) y las fuentes afectadas.
 *  - tables: estado de paginación, truncación, error y permisos de cada tabla.
 * No inventa temas: `topic` solo procede de `topic_key` del registro.
 */
export function buildAcmGraph(source) {
  if (!source || typeof source !== "object" || !source.tables || typeof source.alias !== "string") {
    throw new AcmGraphError("E_STATE", "Falta el estado de origen del grafo ACM.");
  }

  const tables = {};
  const tableRows = {};
  const maxPages = source.limits?.maxPagesPerTable ?? MAX_PAGES_PER_TABLE;
  for (const kind of ACM_TABLES) {
    const state = source.tables[kind] ?? {};
    const pages = Array.isArray(state.pages) ? state.pages : [];
    const status = tableStatus(state, pages, maxPages);
    tables[kind] = {
      kind,
      table: state.table ?? kind,
      path: state.path ?? "",
      order: state.order ?? "",
      capabilities: permissionsOf(state.describe),
      columns: state.describe?.columns ?? [],
      rowCount: status.rowCount,
      pageCount: pages.length,
      loaded: status.loaded,
      truncated: status.truncated,
      nextCursor: status.nextCursor,
      hasMore: status.hasMore,
      atPageLimit: status.atPageLimit,
      complete: status.complete,
      error: state.error ?? null,
    };
    tableRows[kind] = pages.flatMap((page) => page.rows ?? []);
  }

  const nodesById = new Map();
  let duplicateNodes = 0;
  for (const kind of ACM_TABLES) {
    for (const row of tableRows[kind]) {
      const node = parseNode(kind, row, tables[kind].path);
      if (nodesById.has(node.id)) {
        duplicateNodes += 1;
        continue;
      }
      nodesById.set(node.id, node);
    }
  }

  const edgesById = new Map();
  const unresolvedByKey = new Map();
  let duplicateEdges = 0;

  const addEdge = (type, from, to) => {
    // JSON de los tres componentes: la identidad no se puede confundir aunque
    // los IDs reales contengan "->", ":" o cualquier otro separador.
    const id = JSON.stringify([type, from.id, to.id]);
    if (edgesById.has(id)) {
      duplicateEdges += 1;
      return;
    }
    edgesById.set(id, {
      id,
      type,
      from: from.id,
      to: to.id,
      sourceTable: from.table,
      sourceId: from.recordId,
      sourcePath: from.path,
      targetTable: to.table,
      targetId: to.recordId,
      targetPath: to.path,
      resolved: true,
    });
  };

  // Una referencia sin destino cargado no se convierte en arista: se conserva
  // aquí con su fuente para que la vista pueda mostrarla sin inventar un nodo.
  const markUnresolved = (type, targetTable, targetId, source) => {
    const key = `${targetTable}:${targetId}`;
    const reason = tables[targetTable]?.complete === true ? "absent" : "pending";
    let group = unresolvedByKey.get(key);
    if (!group) {
      group = { table: targetTable, id: targetId, reason, count: 0, sources: [] };
      unresolvedByKey.set(key, group);
    }
    group.count += 1;
    group.sources.push({ type, table: source.table, id: source.recordId, path: source.path });
  };

  for (const node of nodesById.values()) {
    if (node.sessionId !== null) {
      const target = nodesById.get(`sessions:${node.sessionId}`);
      if (target) addEdge("session_id", target, node);
      else markUnresolved("session_id", "sessions", node.sessionId, node);
    }
    if (node.supersededBy !== null) {
      const target = nodesById.get(`${node.table}:${node.supersededBy}`);
      if (target) addEdge("superseded_by", node, target);
      else markUnresolved("superseded_by", node.table, node.supersededBy, node);
    }
  }

  const nodes = [...nodesById.values()].sort(compareNodes);
  const edges = [...edgesById.values()].sort((a, b) => compareText(a.id, b.id));
  const unresolved = [...unresolvedByKey.values()].sort(
    (a, b) => compareText(a.table, b.table) || compareText(a.id, b.id),
  );
  for (const group of unresolved) {
    group.sources.sort(
      (a, b) =>
        compareText(a.type, b.type) ||
        compareText(a.table, b.table) ||
        compareText(a.id, b.id) ||
        compareText(a.path, b.path),
    );
  }
  const unresolvedReferences = unresolved.reduce((total, group) => total + group.count, 0);
  const complete = ACM_TABLES.every((kind) => tables[kind].complete);

  const warnings = [];
  for (const kind of ACM_TABLES) {
    if (tables[kind].truncated && !tables[kind].nextCursor) {
      warnings.push(
        `La tabla «${tables[kind].table}» está truncada sin cursor de continuación; no se puede expandir.`,
      );
    } else if (tables[kind].atPageLimit && tables[kind].nextCursor) {
      warnings.push(
        `La tabla «${tables[kind].table}» alcanzó el máximo de ${maxPages} páginas; ` +
          "no se consultarán más páginas.",
      );
    }
  }

  return {
    alias: source.alias,
    limits: {
      pageSize: source.limits?.pageSize ?? DEFAULT_PAGE_SIZE,
      maxPageSize: MAX_PAGE_SIZE,
      maxPagesPerTable: maxPages,
      detectionListLimit: DETECTION_LIST_LIMIT,
    },
    complete,
    tables,
    nodes,
    edges,
    unresolved,
    counts: {
      nodes: nodes.length,
      edges: edges.length,
      references: edges.length + unresolvedReferences,
      resolvedEdges: edges.length,
      unresolvedReferences,
      unresolvedTargets: unresolved.length,
      duplicateNodes,
      duplicateEdges,
    },
    warnings,
    source: {
      alias: source.alias,
      limits: {
        pageSize: source.limits?.pageSize ?? DEFAULT_PAGE_SIZE,
        maxPageSize: MAX_PAGE_SIZE,
        maxPagesPerTable: maxPages,
        detectionListLimit: DETECTION_LIST_LIMIT,
      },
      tables: Object.fromEntries(
        ACM_TABLES.map((kind) => [kind, source.tables[kind] ?? { kind, pages: [], error: null }]),
      ),
    },
  };
}

/**
 * Expansión explícita: pide la página siguiente de una tabla con su cursor y
 * reconstruye el grafo. Los nodos y aristas ya vistos se deduplican.
 */
export async function expandAcmGraph(graph, table, { api, pageSize, fetchImpl, signal } = {}) {
  const kind = String(table).toLowerCase();
  if (!ACM_TABLES.includes(kind)) {
    throw new AcmGraphError(
      "E_OPTION",
      `Tabla ACM desconocida: ${String(table)}; esperaba ${ACM_TABLES.join(", ")}.`,
    );
  }
  if (!graph?.source?.tables?.[kind]) {
    throw new AcmGraphError(
      "E_STATE",
      "El grafo no conserva el estado de paginación; vuelve a cargarlo con loadAcmGraph.",
    );
  }

  const current = graph.source.tables[kind];
  if (current.error) {
    throw new AcmGraphError(
      "E_TABLE_ERROR",
      `La tabla «${graph.tables[kind]?.table ?? kind}» no se cargó: ${current.error.message}`,
      { details: { table: kind, error: current.error } },
    );
  }

  const maxPages = graph.limits?.maxPagesPerTable ?? MAX_PAGES_PER_TABLE;
  if ((graph.tables[kind]?.pageCount ?? 0) >= maxPages) {
    throw new AcmGraphError(
      "E_PAGE_LIMIT",
      `La tabla «${graph.tables[kind]?.table ?? kind}» alcanzó el máximo de ${maxPages} páginas.`,
      { details: { table: kind, maxPages } },
    );
  }

  const nextCursor = graph.tables[kind]?.nextCursor ?? "";
  if (!nextCursor) {
    throw new AcmGraphError(
      "E_NO_MORE",
      `La tabla «${graph.tables[kind]?.table ?? kind}» ya está cargada por completo.`,
    );
  }

  const client = api ?? createAcmApi({ fetchImpl, signal });
  const size = normalizePageSize(pageSize ?? graph.limits.pageSize);
  const order = current.order ?? "";
  const page = await loadAcmPage(client, { path: current.path, cursor: nextCursor, limit: size, order });

  const nextSource = {
    alias: graph.source.alias,
    limits: { ...graph.limits, pageSize: size, maxPagesPerTable: maxPages },
    tables: Object.fromEntries(
      ACM_TABLES.map((name) => {
        const state = graph.source.tables[name] ?? { kind: name, pages: [], error: null, order: ACM_ORDERS[name] ?? "" };
        if (name !== kind) return [name, state];
        return [name, { ...state, pages: [...(state.pages ?? []), page], order }];
      }),
    ),
  };
  return buildAcmGraph(nextSource);
}

async function listEntries(api, path, what) {
  const body = await api("ls", { path, limit: DETECTION_LIST_LIMIT });
  const entries = body.data?.entries;
  if (!Array.isArray(entries)) {
    throw new AcmGraphError("E_DATA", `db-ai no devolvió una lista para ${what}.`);
  }
  return { entries, truncated: body.meta?.truncated === true };
}

function detectionTruncated(what) {
  return new AcmGraphError(
    "E_ACM_DETECTION_LIMIT",
    `La detección de ACM quedó truncada al listar ${what} (límite ${DETECTION_LIST_LIMIT}); ` +
      "no se puede afirmar que la memoria no exista.",
  );
}

function structureProblems(kind, describe) {
  const problems = [];
  if (!describe || typeof describe !== "object") {
    return [`«${kind}» no tiene descripción`];
  }
  const columns = new Map(
    (describe.columns ?? []).map((column) => [String(column.name).toLowerCase(), column]),
  );
  for (const [name, type] of Object.entries(ACM_COLUMNS[kind])) {
    const column = columns.get(name);
    if (!column) problems.push(`«${kind}» no tiene la columna ${name}`);
    else if (String(column.declared_type).toUpperCase() !== type) {
      problems.push(`«${kind}.${name}» es ${column.declared_type}, esperaba ${type}`);
    }
  }
  const primaryKey = Array.isArray(describe.primary_key) ? describe.primary_key : [];
  if (primaryKey.length !== 1 || String(primaryKey[0]).toLowerCase() !== "id") {
    problems.push(`«${kind}» no tiene id como clave primaria única`);
  }
  if (describe.capabilities?.navigable !== true) {
    problems.push(`«${kind}» no tiene una clave navegable`);
  }
  return problems;
}

function tableStatus(state, pages, maxPages = MAX_PAGES_PER_TABLE) {
  const error = state.error ?? null;
  const last = pages.length > 0 ? pages[pages.length - 1] : null;
  const truncated = last?.truncated === true;
  const nextCursor = truncated && typeof last.nextCursor === "string" ? last.nextCursor : "";
  const rowCount = pages.reduce((total, page) => total + (page.rows?.length ?? 0), 0);
  const loaded = pages.length > 0 && error === null;
  const atPageLimit = pages.length >= maxPages && truncated;
  return {
    rowCount,
    loaded,
    truncated,
    nextCursor,
    hasMore: nextCursor !== "" && !atPageLimit && error === null,
    atPageLimit,
    complete: loaded && !truncated,
  };
}

function permissionsOf(describe) {
  return {
    readable: describe?.capabilities?.readable === true,
    writable: describe?.capabilities?.writable === true,
    navigable: describe?.capabilities?.navigable === true,
  };
}

function parseNode(kind, row, tablePath) {
  if (!row || typeof row !== "object" || typeof row.fields !== "object" || row.fields === null) {
    throw new AcmGraphError("E_DATA", `Un registro de «${kind}» llegó sin campos.`);
  }
  const prefix = tablePath.endsWith("/") ? tablePath : `${tablePath}/`;
  if (
    typeof row.path !== "string" ||
    !row.path.startsWith(prefix) ||
    row.path.length === prefix.length
  ) {
    throw new AcmGraphError(
      "E_DATA",
      `Un registro de «${kind}» no tiene una ruta db-ai propia: ${String(row.path)}.`,
    );
  }

  const recordId = requiredText(row.fields, "id", row.path);
  if ((recordId === "") !== row.path.endsWith("/~e")) {
    throw new AcmGraphError(
      "E_DATA",
      `El id y la ruta de ${row.path} no son coherentes: ACM representa el id vacío con ~e.`,
    );
  }
  const fields = Object.fromEntries(
    Object.entries(row.fields).map(([name, value]) => [name, decodeValue(value, row.path)]),
  );
  const label =
    textOf(fields, labelColumn(kind)) || textOf(fields, "title") || recordId;

  return {
    id: `${kind}:${recordId}`,
    table: kind,
    recordId,
    path: row.path,
    label,
    kind: kind === "observations" ? textOf(fields, "kind") : null,
    topic: kind === "observations" ? textOf(fields, "topic_key") : null,
    status: textOf(fields, "status"),
    createdAt: textOf(fields, "created_at"),
    updatedAt: textOf(fields, "updated_at"),
    sessionId: referenceOf(fields, "session_id", row.path),
    supersededBy: referenceOf(fields, "superseded_by", row.path),
    fields,
  };
}

function labelColumn(kind) {
  switch (kind) {
    case "sessions":
      return "goal";
    case "observations":
      return "title";
    case "checkpoints":
      return "state";
    case "preferences":
      return "pref_key";
    default:
      return "id";
  }
}

function decodeValue(value, path) {
  if (!value || typeof value !== "object" || typeof value.kind !== "string") {
    throw new AcmGraphError("E_DATA", `Valor tipado inválido en ${path}.`);
  }
  switch (value.kind) {
    case "text":
      return value.data;
    case "integer": {
      const number = Number(value.data);
      return Number.isSafeInteger(number) ? number : value.data;
    }
    case "real": {
      const number = Number(value.data);
      return Number.isFinite(number) ? number : value.data;
    }
    case "null":
      return null;
    default:
      return value.data;
  }
}

function textOf(fields, name) {
  const value = fields[name];
  return typeof value === "string" && value !== "" ? value : null;
}

function requiredText(fields, name, path) {
  const value = fields?.[name];
  // Un TEXT vacío es un id válido en ACM y db-ai lo representa como "~e" en la
  // ruta; lo que no se acepta es columna ausente, NULL u otro tipo.
  if (!value || value.kind !== "text" || typeof value.data !== "string") {
    throw new AcmGraphError("E_DATA", `El registro ${path} no tiene un ${name} TEXT válido.`);
  }
  return value.data;
}

function referenceOf(fields, name, path) {
  const value = fields?.[name];
  if (value === undefined || value === null) return null;
  if (typeof value !== "string") {
    throw new AcmGraphError("E_DATA", `La referencia ${name} de ${path} no es un TEXT válido.`);
  }
  return value;
}

function compareNodes(a, b) {
  return compareText(a.table, b.table) || compareText(a.recordId, b.recordId);
}

// Comparación por punto de código: el orden no depende del locale del motor.
function compareText(a, b) {
  if (a < b) return -1;
  if (a > b) return 1;
  return 0;
}

function errorInfo(cause) {
  if (cause instanceof AcmGraphError) {
    return { code: cause.code, message: cause.message };
  }
  return {
    code: "E_UNKNOWN",
    message: `Error inesperado al leer la memoria ACM: ${String(cause?.message ?? cause)}`,
  };
}
