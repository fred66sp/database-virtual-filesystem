// Integración real del adaptador del Grafo ACM contra una memoria SQLite
// aislada, sin navegador. Ejecutar a mano:
//
//   TK77_ACM_INTEGRATION=1 node --test internal/web/acm-graph-integration.test.mjs
//
// captura registros con los eventos normales de ACM (start más las operaciones
// AIF del binario: decision, learning, checkpoint, preference y close), levanta
// `db-ai serve` con
// XDG_CONFIG_HOME y XDG_STATE_HOME aislados y llama al adaptador real
// (static/acm-graph-data.js) sobre la API HTTP real. Nunca toca la memoria del
// proyecto ni el puerto 8893; sin la variable de entorno no hace nada.
//
// Las operaciones AIF no exponen un evento para sustituir decisiones. El
// procedimiento
// oficial de ACM es el batch de DESIGN §5.4/§8.3: touch de la fila nueva y
// write de la anterior con ETag, status=superseded y superseded_by. Se usa
// `db-ai batch`, la misma vía que ACM emplea para checkpoints y
// preferencias; no hay ninguna escritura SQL directa.

import { test } from "node:test";
import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import { createHash, randomBytes } from "node:crypto";
import { mkdtemp, mkdir, readFile, readdir, realpath, rm } from "node:fs/promises";
import { once } from "node:events";
import net from "node:net";
import path from "node:path";
import { fileURLToPath } from "node:url";

import { createAcmApi, loadAcmGraph } from "./static/acm-graph-data.js";

const exec = promisify(execFile);
const HERE = path.dirname(fileURLToPath(import.meta.url));
const REPO = path.resolve(HERE, "../..");
const INIT = path.join(REPO, "examples/agent-continuity/init.sh");
const ACM_GO = path.join(REPO, "examples/agent-continuity/go");
const HASH_TOOL = path.join(REPO, "examples/agent-continuity/tools/content-hash.py");
const DB_AI = process.env.DB_AI ?? "db-ai";
const ALIAS = "memtk77";
const SESSION = "tk77-integracion";
const CLOSED_SESSION = "tk77-cerrada";
const TABLES = ["sessions", "observations", "checkpoints", "preferences"];
const ENABLED = process.env.TK77_ACM_INTEGRATION === "1";

const sha = (value) => createHash("sha256").update(value).digest("hex");

async function run(cmd, args, env) {
  const { stdout, stderr } = await exec(cmd, args, { env, maxBuffer: 4 << 20 });
  return { stdout, stderr };
}

// El binario ACM se construye al vuelo en /tmp/opencode, como hace la suite
// bash de examples/agent-continuity; no depende de artefactos fuera de Git.
const builtAcm = [];
async function buildAcm(env) {
  if (buildAcm.cache) return buildAcm.cache;
  if ((await exec("go", ["version"], { env }).catch(() => null)) === null) {
    throw new Error("falta go en PATH, necesario para construir el binario ACM");
  }
  const bin = path.join(await realpath("/tmp/opencode"), `acm-tk77-${randomBytes(4).toString("hex")}`);
  await exec("go", ["build", "-o", bin, "./cmd/acm"], { env, cwd: ACM_GO });
  builtAcm.push(bin);
  buildAcm.cache = bin;
  return bin;
}

// Una operación AIF 0.2 del binario: la entrada por stdin y el resultado
// executed con su outcome; cualquier otra cosa es fallo.
async function acm(env, operation, input = {}) {
  const bin = await buildAcm(env);
  const request = JSON.stringify({ aif: "0.2", operation, input });
  const { stdout } = await runWithInput(bin, ["aif"], env, request);
  const body = JSON.parse(stdout);
  assert.equal(body.result, "executed", `acm aif ${operation} falló: ${stdout}`);
  return body;
}

async function sqliteJson(db, sql) {
  const { stdout } = await run("sqlite3", ["-json", db, sql], process.env);
  const text = stdout.trim();
  return text === "" ? [] : JSON.parse(text);
}

async function rowsOf(db, table) {
  return sqliteJson(db, `SELECT * FROM ${table} ORDER BY id;`);
}

// Huella lógica y física: contenido, esquema, integridad y ficheros auxiliares
// de SQLite. Una lectura no debe cambiar ninguna de las cuatro.
async function snapshot(db) {
  const [file, dump, schema, integrity, siblings] = await Promise.all([
    readFile(db),
    run("sqlite3", [db, ".dump"], process.env),
    run("sqlite3", [db, ".schema"], process.env),
    run("sqlite3", [db, "PRAGMA integrity_check; PRAGMA schema_version;"], process.env),
    readdir(path.dirname(db)),
  ]);
  return {
    fileBytes: sha(file),
    content: sha(dump.stdout),
    schema: sha(schema.stdout),
    integrity: integrity.stdout.trim(),
    sidecars: siblings.filter((name) => /-(wal|journal|shm)$/.test(name)).sort(),
  };
}

function freePort() {
  return new Promise((resolve, reject) => {
    const probe = net.createServer();
    probe.once("error", reject);
    probe.listen(0, "127.0.0.1", () => {
      const { port } = probe.address();
      probe.close((error) => (error ? reject(error) : resolve(port)));
    });
  });
}

async function startServer(env, port) {
  const child = spawn(DB_AI, ["serve", "--port", String(port)], {
    env,
    stdio: ["ignore", "pipe", "pipe"],
  });
  let log = "";
  const append = (chunk) => {
    log += chunk.toString();
  };
  child.stdout.on("data", append);
  child.stderr.on("data", append);

  const deadline = Date.now() + 10_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`db-ai serve terminó:\n${log}`);
    if (log.includes("listening on")) return { child, log: () => log };
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  child.kill("SIGKILL");
  throw new Error(`db-ai serve no anunció su dirección:\n${log}`);
}

async function stopServer(server) {
  if (!server || server.child.exitCode !== null) return;
  server.child.kill("SIGTERM");
  await Promise.race([once(server.child, "exit"), new Promise((resolve) => setTimeout(resolve, 3000))]);
  if (server.child.exitCode === null) {
    server.child.kill("SIGKILL");
    await Promise.race([once(server.child, "exit"), new Promise((resolve) => setTimeout(resolve, 1000))]);
  }
}

async function getJson(base, route) {
  const response = await fetch(base + route, { headers: { Accept: "application/json" } });
  const body = await response.json();
  assert.equal(body.ok, true, `${route} devolvió ${response.status}: ${JSON.stringify(body)}`);
  return body;
}

function splitNodeId(id) {
  const at = id.indexOf(":");
  return [id.slice(0, at), id.slice(at + 1)];
}

// Comprueba nodos, aristas y recuentos del grafo contra las filas SQLite que
// acaba de leer el adaptador. Devuelve los mapas por id para las comprobaciones
// específicas de cada sustitución.
function verifyGraphAgainstOrigin(graph, origin) {
  const byId = Object.fromEntries(
    TABLES.map((table) => [table, new Map(origin[table].map((row) => [row.id, row]))]),
  );
  const expectedNodes = TABLES.flatMap((table) => origin[table].map((row) => `${table}:${row.id}`));
  assert.deepEqual(graph.nodes.map((node) => node.id).sort(), expectedNodes.sort());

  for (const node of graph.nodes) {
    const [table, id] = splitNodeId(node.id);
    const row = byId[table].get(id);
    assert.ok(row, `nodo sin origen: ${node.id}`);
    assert.equal(node.path, `/${ALIAS}/${table}/${id === "" ? "~e" : id}`);
    assert.equal(node.status, row.status ?? "");
  }

  let expectedSessionEdges = 0;
  let expectedSupersededEdges = 0;
  for (const table of TABLES) {
    for (const row of origin[table]) {
      if (row.session_id != null) expectedSessionEdges += 1;
      if (row.superseded_by != null) expectedSupersededEdges += 1;
    }
  }
  assert.equal(graph.edges.length, expectedSessionEdges + expectedSupersededEdges);
  assert.deepEqual(graph.unresolved, []);
  assert.equal(graph.counts.unresolvedReferences, 0);
  assert.equal(graph.complete, true);

  for (const edge of graph.edges) {
    const [fromTable, fromId] = splitNodeId(edge.from);
    const [toTable, toId] = splitNodeId(edge.to);
    const from = byId[fromTable].get(fromId);
    const to = byId[toTable].get(toId);
    assert.ok(from, `arista sin origen: ${edge.id}`);
    assert.ok(to, `arista sin destino: ${edge.id}`);
    if (edge.type === "session_id") {
      assert.equal(fromTable, "sessions");
      assert.equal(to.session_id, from.id);
    } else if (edge.type === "superseded_by") {
      assert.equal(fromTable, toTable);
      assert.equal(from.superseded_by, to.id);
      assert.equal(from.status, "superseded");
    } else {
      assert.fail(`tipo de arista inesperado: ${edge.type}`);
    }
  }
  return { byId, expectedSessionEdges, expectedSupersededEdges };
}

function runWithInput(cmd, args, env, input) {
  return new Promise((resolve, reject) => {
    const child = spawn(cmd, args, { env, stdio: ["pipe", "pipe", "pipe"] });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk;
    });
    child.on("error", reject);
    child.on("close", (code) => {
      if (code === 0) resolve({ stdout, stderr });
      else reject(new Error(`${cmd} salió con ${code}: ${stderr || stdout}`));
    });
    child.stdin.end(input);
  });
}

async function dbAi(env, ...args) {
  const { stdout } = await run(DB_AI, args, env);
  const body = JSON.parse(stdout);
  assert.equal(body.ok, true, `db-ai ${args[0]} falló: ${stdout}`);
  return body.data;
}

function uuid7() {
  const bytes = randomBytes(16);
  const ms = BigInt(Date.now());
  for (let i = 0; i < 6; i += 1) bytes[i] = Number((ms >> BigInt(8 * (5 - i))) & 0xffn);
  bytes[6] = (bytes[6] & 0x0f) | 0x70;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = bytes.toString("hex");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

// DESIGN §5.4/§8.3: busca la decisión activa por la API, calcula el hash con la
// herramienta oficial y ejecuta un batch atómico con la fila nueva y la
// anterior marcada como superseded.
async function supersedeDecision(env, base, { topic, title, decision, rationale }) {
  const query = new URLSearchParams([
    ["path", `/${ALIAS}/observations`],
    ["where", "kind=decision"],
    ["where", `topic_key=${topic}`],
    ["where", "status=active"],
    ["limit", "10"],
  ]);
  const found = await getJson(base, `/api/find?${query}`);
  assert.equal(
    found.data.rows.length,
    1,
    `la sustitución exige una única decisión activa en «${topic}»`,
  );
  const oldId = found.data.rows[0].fields.id.data;
  const oldEtag = found.data.rows[0].etag;
  assert.ok(oldEtag, "la decisión anterior llegó sin ETag");

  const timestamp = new Date().toISOString().replace(/\.\d{3}Z$/, "Z");
  const id = uuid7();
  const semantic = {
    kind: "decision",
    title,
    body: decision,
    rationale,
    alternatives: "",
    consequences: "",
    topic_key: topic,
  };
  const contentHash = (
    await runWithInput("python3", [HASH_TOOL, "observations"], env, JSON.stringify(semantic))
  ).stdout.trim();
  assert.match(contentHash, /^sha256:[0-9a-f]{64}$/);

  await dbAi(env, "batch", "--data", JSON.stringify([
    {
      op: "touch",
      path: `/${ALIAS}/observations`,
      data: {
        ...semantic,
        id,
        session_id: SESSION,
        agent_id: "tk77-test",
        source: "agent",
        confidence: "confirmed",
        status: "active",
        duplicate_count: 1,
        last_seen_at: timestamp,
        content_hash: contentHash,
        created_at: timestamp,
        updated_at: timestamp,
      },
    },
    {
      op: "write",
      path: `/${ALIAS}/observations/${oldId}`,
      if_match: oldEtag,
      data: { status: "superseded", superseded_by: id, updated_at: timestamp },
    },
  ]));
  return { id, oldId };
}

test(
  "el grafo ACM se construye sobre una base real y las lecturas no la escriben",
  { skip: !ENABLED && "define TK77_ACM_INTEGRATION=1 para ejecutar la integración real" },
  async (t) => {
    const root = await mkdtemp(path.join(await realpath("/tmp/opencode"), "tk77-acm-"));
    const project = path.join(root, "project");
    const env = {
      ...process.env,
      ACM_HOME: project,
      ACM_ALIAS: ALIAS,
      ACM_AGENT_ID: "tk77-test",
      OPENCODE_SESSION_ID: SESSION,
      XDG_CONFIG_HOME: path.join(root, "xdg-config"),
      XDG_STATE_HOME: path.join(root, "xdg-state"),
    };
    const db = path.join(project, ".agent-memory", "memory.db");
    let server;
    t.after(async () => {
      await stopServer(server);
      for (const bin of builtAcm) await rm(bin, { force: true });
      if (process.env.TK77_KEEP !== "1") await rm(root, { recursive: true, force: true });
      else t.diagnostic(`memoria de prueba conservada en ${root}`);
    });

    // 1. Procedimiento oficial de alta, con configuración de db-ai aislada.
    await mkdir(project, { recursive: true });
    const init = await run("bash", [INIT], env);
    assert.match(init.stdout, /listo: memoria de ACM en /, init.stdout + init.stderr);
    const config = JSON.parse(
      await readFile(path.join(env.XDG_CONFIG_HOME, "db-ai", "config.json"), "utf8"),
    );
    assert.deepEqual(Object.keys(config.mounts), [ALIAS]);
    assert.equal(config.mounts[ALIAS].file, await realpath(db));

    // 2. Registros por los mecanismos normales de ACM: sesión abierta y cerrada,
    //    decisión, aprendizajes, dos preferencias (la segunda sustituye a la
    //    primera) y checkpoints.
    await buildAcm(env);
    await run(buildAcm.cache, ["start", "--goal", "Integración real tk-77"], env);
    await acm(env, "decision", {
      topic: "tk77",
      title: "El grafo se lee de la API de solo lectura",
      decision: "El adaptador usa ls, describe y find",
      rationale: "La vista no puede escribir la memoria",
      confidence: "confirmed",
    });
    await acm(env, "decision", {
      topic: "tk77-decision",
      title: "Decisión inicial sobre el refresco",
      decision: "El grafo se recarga después de cada sustitución",
      rationale: "Comprobar la cadena de sustitución de una decisión",
      confidence: "confirmed",
    });
    await acm(env, "learning", {
      title: "Los nodos conservan la ruta de origen",
      body: "Cada nodo del grafo lleva la ruta db-ai del registro que lo produjo.",
      topic: "tk77",
      confidence: "confirmed",
    });
    await acm(env, "checkpoint", { state: "Integración en curso", next_steps: "Cargar el grafo" });
    const firstPref = await acm(env, "preference", {
      pref_key: "tk77-color",
      value: "azul",
      origin: "agent",
      rationale: "Valor inicial de la prueba",
    });
    const secondPref = await acm(env, "preference", {
      pref_key: "tk77-color",
      value: "verde",
      origin: "agent",
      rationale: "Sustituye al valor inicial",
    });
    assert.equal(firstPref.outcome.result, "created");
    assert.equal(secondPref.outcome.result, "created");

    env.OPENCODE_SESSION_ID = CLOSED_SESSION;
    await run(buildAcm.cache, ["start", "--goal", "Sesión cerrada tk-77"], env);
    await acm(env, "learning", {
      title: "La sesión cerrada sigue en el grafo",
      body: "Un registro de una sesión cerrada es un nodo más, con su arista de sesión.",
      topic: "tk77",
    });
    const closed = await acm(env, "close", { summary: "Cierre de la prueba de integración" });
    assert.equal(closed.outcome.result, "closed");
    env.OPENCODE_SESSION_ID = SESSION;

    // 3. Servidor aislado en un puerto efímero, nunca el 8893 del explorador.
    const port = await freePort();
    assert.ok(![8890, 8891, 8893].includes(port), `puerto de prueba no aislado: ${port}`);
    server = await startServer(env, port);
    const base = `http://127.0.0.1:${port}`;
    t.diagnostic(`db-ai serve aislado en ${base} sobre ${db}`);

    const api = createAcmApi({
      fetchImpl: (url, init) => fetch(base + url, init),
    });

    // 4. Estado de la base justo antes de leer; después, solo lecturas.
    const beforeRead = await snapshot(db);
    const graph = await loadAcmGraph({ api, alias: ALIAS, pageSize: 200 });

    // 5. La vista de tabla consume estos mismos endpoints (describe/find/count/
    //    cat). Se comprueba el contrato de datos, no el render: sin navegador.
    const tablePath = `/${ALIAS}/observations`;
    const described = await getJson(base, `/api/describe?path=${encodeURIComponent(tablePath)}`);
    const tablePage = await getJson(base, `/api/find?path=${encodeURIComponent(tablePath)}&limit=200`);
    const counted = await getJson(base, `/api/count?path=${encodeURIComponent(tablePath)}`);
    const firstRow = tablePage.data.rows[0];
    const catted = await getJson(
      base,
      `/api/cat?path=${encodeURIComponent(firstRow.path)}`,
    );
    assert.ok(described.data.columns.length > 0);
    assert.ok(Array.isArray(tablePage.data.rows) && tablePage.data.rows.length > 0);
    assert.equal(counted.data.count, tablePage.data.rows.length);
    assert.deepEqual(catted.data.fields.id, firstRow.fields.id);
    // La tabla y el grafo deben mostrar exactamente las mismas observaciones.
    assert.deepEqual(
      tablePage.data.rows.map((row) => row.fields.id.data).sort(),
      graph.nodes
        .filter((node) => node.table === "observations")
        .map((node) => node.recordId)
        .sort(),
      "la tabla y el grafo no muestran las mismas observaciones",
    );
    const afterRead = await snapshot(db);
    assert.deepEqual(afterRead, beforeRead, "la lectura del grafo y de la tabla escribió en la base");

    // 6. Cada nodo y cada relación del grafo se verifica en el origen SQLite.
    const origin = Object.fromEntries(await Promise.all(TABLES.map(async (table) => [table, await rowsOf(db, table)])));
    const { byId, expectedSessionEdges, expectedSupersededEdges } =
      verifyGraphAgainstOrigin(graph, origin);

    // Sustitución creada por `acm preference`, con dirección antiguo → nuevo.
    // Los ids se leen del origen: la confirmación de ACM solo publica `result`.
    const oldPref = byId.preferences.get(
      origin.preferences.find((row) => row.pref_key === "tk77-color" && row.value === "azul")?.id,
    );
    const newPref = byId.preferences.get(
      origin.preferences.find((row) => row.pref_key === "tk77-color" && row.value === "verde")?.id,
    );
    assert.ok(oldPref && newPref, "faltan las dos preferencias de la prueba");
    assert.equal(oldPref.status, "superseded");
    assert.equal(oldPref.superseded_by, newPref.id);
    assert.equal(newPref.status, "active");
    const preferenceEdge = graph.edges.find(
      (edge) => edge.type === "superseded_by" && edge.from === `preferences:${oldPref.id}`,
    );
    assert.ok(preferenceEdge, "falta la arista de sustitución de la preferencia");
    assert.equal(preferenceEdge.to, `preferences:${newPref.id}`);
    assert.ok(
      !graph.edges.some((edge) => edge.from === `preferences:${newPref.id}` && edge.type === "superseded_by"),
      "la arista de sustitución apunta en la dirección contraria",
    );

    t.diagnostic(
      `${graph.nodes.length} nodos y ${graph.edges.length} aristas verificados; ` +
        `${expectedSessionEdges} de sesión, ${expectedSupersededEdges} de sustitución`,
    );

    // Sustitución de una decisión por el procedimiento oficial de ACM
    // (DESIGN §5.4/§8.3). Las operaciones AIF no tienen evento de sustitución: el batch de
    // db-ai hace `touch` de la nueva y `write` de la anterior con ETag. No hay
    // escritura SQL directa. Primero se comprueba que la inicial está activa.
    const initialDecision = graph.nodes.find(
      (node) => node.label === "Decisión inicial sobre el refresco",
    );
    assert.ok(initialDecision, "la decisión inicial no aparece en el primer grafo");
    assert.equal(initialDecision.status, "active");
    assert.ok(
      !graph.edges.some(
        (edge) => edge.type === "superseded_by" && edge.from === initialDecision.id,
      ),
      "la decisión inicial ya tenía una sustitución",
    );

    const decisionChange = await supersedeDecision(env, base, {
      topic: "tk77-decision",
      title: "Decisión revisada sobre el refresco",
      decision: "El grafo se recarga después de sustituir una decisión",
      rationale: "La decisión anterior queda enlazada y deja de estar activa",
    });
    const beforeDecisionReload = await snapshot(db);
    const afterDecision = await loadAcmGraph({ api, alias: ALIAS, pageSize: 200 });
    const afterDecisionRead = await snapshot(db);
    assert.deepEqual(
      afterDecisionRead,
      beforeDecisionReload,
      "la recarga tras sustituir la decisión escribió en la base",
    );

    const originAfterDecision = Object.fromEntries(
      await Promise.all(TABLES.map(async (table) => [table, await rowsOf(db, table)])),
    );
    verifyGraphAgainstOrigin(afterDecision, originAfterDecision);
    const decisionRows = originAfterDecision.observations;
    const oldDecision = decisionRows.find((row) => row.id === decisionChange.oldId);
    const newDecision = decisionRows.find((row) => row.id === decisionChange.id);
    assert.ok(oldDecision && newDecision, "faltan las decisiones de la sustitución");
    assert.equal(oldDecision.title, "Decisión inicial sobre el refresco");
    assert.equal(oldDecision.status, "superseded");
    assert.equal(oldDecision.superseded_by, newDecision.id);
    assert.equal(
      oldDecision.rationale,
      "Comprobar la cadena de sustitución de una decisión",
    );
    assert.equal(newDecision.status, "active");
    assert.equal(newDecision.superseded_by, null);
    assert.equal(newDecision.session_id, SESSION);
    assert.equal(
      decisionRows.filter(
        (row) => row.topic_key === "tk77-decision" && row.status === "active",
      ).length,
      1,
      "debe quedar una sola decisión activa por tema",
    );

    const decisionEdge = afterDecision.edges.find(
      (edge) =>
        edge.type === "superseded_by" &&
        edge.from === `observations:${decisionChange.oldId}` &&
        edge.to === `observations:${decisionChange.id}`,
    );
    assert.ok(decisionEdge, "falta la arista antigua → nueva de la decisión");
    assert.equal(
      afterDecision.nodes.find((node) => node.id === decisionEdge.from)?.status,
      "superseded",
    );
    assert.equal(
      afterDecision.nodes.find((node) => node.id === decisionEdge.to)?.status,
      "active",
    );
    assert.ok(
      !afterDecision.edges.some(
        (edge) =>
          edge.type === "superseded_by" &&
          edge.from === `observations:${decisionChange.id}`,
      ),
      "la arista de la decisión apunta en la dirección contraria",
    );
    t.diagnostic(
      `tras sustituir la decisión: ${afterDecision.nodes.length} nodos y ` +
        `${afterDecision.edges.length} aristas; ${oldDecision.id} → ${newDecision.id}`,
    );

    // 7. Refresco: una observación nueva capturada por ACM aparece al recargar.
    const fresh = await acm(env, "learning", {
      title: "Observación posterior al primer grafo",
      body: "Debe aparecer como nodo al volver a cargar el grafo.",
      topic: "tk77",
    });
    assert.equal(fresh.outcome.result, "created");
    const freshRow = (await rowsOf(db, "observations")).find(
      (row) => row.title === "Observación posterior al primer grafo",
    );
    assert.ok(freshRow, "la observación nueva no está en el origen");
    assert.equal(freshRow.session_id, SESSION);
    const beforeReload = await snapshot(db);
    const refreshed = await loadAcmGraph({ api, alias: ALIAS, pageSize: 200 });
    const afterReload = await snapshot(db);
    assert.deepEqual(afterReload, beforeReload, "la recarga del grafo escribió en la base");
    assert.equal(
      refreshed.tables.observations.rowCount,
      afterDecision.tables.observations.rowCount + 1,
    );
    const freshNode = refreshed.nodes.find((node) => node.id === `observations:${freshRow.id}`);
    assert.ok(freshNode, "la observación nueva no aparece tras recargar");
    assert.equal(freshNode.label, "Observación posterior al primer grafo");
    assert.equal(byId.observations.has(freshRow.id), false, "el nodo nuevo debe ser posterior al origen leído");

    // 8. Sin cambios de esquema en toda la ejecución; el contenido final es el
    //    que dejaron las escrituras ACM intencionadas.
    assert.equal(afterReload.schema, beforeRead.schema);
    assert.deepEqual(afterReload.sidecars, []);
    assert.match(afterReload.integrity, /^ok\n\d+$/);
    t.diagnostic(`huella de lectura idéntica: ${beforeRead.content.slice(0, 16)}…`);
  },
);
