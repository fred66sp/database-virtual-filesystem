// Pruebas de integración estática y de la lógica pura de la vista «Tabla | Grafo ACM».
// Ejecutar: node --test internal/web/graph-view.test.mjs
// No hay navegador: se captura la Options API de Vue con un stub y se comprueba
// lo que no necesita DOM (filtros, escape de tooltips y liberación de recursos).

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { createHash } from "node:crypto";
import vm from "node:vm";

const appSource = readFileSync(new URL("./static/app.js", import.meta.url), "utf8");
const indexSource = readFileSync(new URL("./static/index.html", import.meta.url), "utf8");
const vueSource = readFileSync(new URL("./static/vendor/vue.global.prod.js", import.meta.url), "utf8");
const vendorSource = readFileSync(new URL("./static/vendor/force-graph.min.js", import.meta.url), "utf8");
const licenseSource = readFileSync(new URL("./static/vendor/force-graph.LICENSE.txt", import.meta.url), "utf8");
const vendorReadme = readFileSync(new URL("./static/vendor/force-graph.README.md", import.meta.url), "utf8");

function appTemplate() {
  const start = indexSource.indexOf('<div id="app"');
  assert.notEqual(start, -1, "index.html no contiene #app");
  let depth = 0;
  let end = -1;
  const re = /<\/?div\b[^>]*>/g;
  re.lastIndex = start;
  let match;
  while ((match = re.exec(indexSource))) {
    depth += match[0].startsWith("</") ? -1 : 1;
    if (depth === 0) {
      end = match.index + match[0].length;
      break;
    }
  }
  assert.notEqual(end, -1, "no se pudo delimitar #app");
  return indexSource.slice(start, end);
}

// El compilador de Vue usa document.createElement("div") para decodificar
// entidades HTML. Este doble basta para compilar el template sin navegador.
function decodeEntities(value) {
  return String(value)
    .replace(/&#x([0-9a-f]+);/gi, (_, hex) => String.fromCodePoint(Number.parseInt(hex, 16)))
    .replace(/&#(\d+);/g, (_, dec) => String.fromCodePoint(Number(dec)))
    .replace(/&quot;/g, '"')
    .replace(/&#39;/g, "'")
    .replace(/&lt;/g, "<")
    .replace(/&gt;/g, ">")
    .replace(/&amp;/g, "&");
}

function compileAppTemplate(template = appTemplate()) {
  const sandbox = {
    console,
    setTimeout,
    clearTimeout,
    document: {
      createElement() {
        const el = {
          _attr: "",
          _text: "",
          children: [],
          set innerHTML(value) {
            const html = String(value);
            this._html = html;
            const attribute = /^<div foo="([\s\S]*)">$/.exec(html);
            this._text = decodeEntities(attribute ? attribute[1] : html);
            this._attr = decodeEntities(attribute ? attribute[1] : "");
          },
          get textContent() { return this._text; },
          set textContent(value) { this._text = String(value); },
        };
        el.children = [{ getAttribute() { return el._attr; } }];
        return el;
      },
      querySelector() { return null; },
      addEventListener() {},
    },
    navigator: { userAgent: "" },
    window: {},
  };
  sandbox.globalThis = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(vueSource, sandbox, { filename: "vue.global.prod.js" });
  return sandbox.Vue.compile(template, {
    onError(error) { throw error; },
  });
}

// `v-tip` no interviene en el render: se retira para poder ejecutar el render
// con un contexto plano, sin registrar la directiva ni montar un DOM real.
function renderAppTemplate() {
  return compileAppTemplate(appTemplate().replace(/\s+v-tip="[^"]*"/g, ""));
}

function createAppHarness() {
  let spec = null;
  const scripts = [];
  const sandbox = {
    Vue: {
      createApp(candidate) {
        spec = candidate;
        return { directive() {}, mount() {}, config: { globalProperties: {} } };
      },
    },
    document: {
      getElementById() { return null; },
      addEventListener() {},
      removeEventListener() {},
      createElement(tag) {
        if (tag === "script") {
          const script = {};
          scripts.push(script);
          return script;
        }
        return {};
      },
      head: { appendChild() {} },
      body: { classList: { add() {}, remove() {} } },
      documentElement: { clientWidth: 1280, clientHeight: 800 },
      activeElement: { tagName: "BODY" },
    },
    window: {
      matchMedia() { return { matches: false, addEventListener() {}, removeEventListener() {} }; },
      addEventListener() {},
      devicePixelRatio: 1,
    },
    localStorage: { getItem() { return null; }, setItem() {} },
    navigator: { clipboard: { writeText: async () => {} } },
    requestAnimationFrame() { return 1; },
    cancelAnimationFrame() {},
    setTimeout,
    clearTimeout,
    Intl,
    URLSearchParams,
    console,
  };
  sandbox.globalThis = sandbox;
  sandbox.__scripts = scripts;
  vm.createContext(sandbox);
  vm.runInContext(appSource, sandbox, { filename: "app.js" });
  assert.ok(spec, "app.js no creó la aplicación Vue");
  return { spec, sandbox };
}

function createContext(spec, overrides = {}) {
  const context = { ...spec.data() };
  for (const [name, fn] of Object.entries(spec.methods)) context[name] = fn.bind(context);
  for (const [name, fn] of Object.entries(spec.computed)) {
    Object.defineProperty(context, name, { get() { return fn.call(context); }, enumerable: true });
  }
  for (const [name, value] of Object.entries(overrides)) {
    if (Object.getOwnPropertyDescriptor(context, name)?.get) {
      throw new Error(`no se puede sobrescribir el computed ${name}`);
    }
    context[name] = value;
  }
  spec.created.call(context);
  return context;
}

const { spec } = createAppHarness();
const methods = spec.methods;

const graph = {
  nodes: [
    { id: "sessions:s1", table: "sessions", recordId: "s1", path: "/mem/sessions/s1", label: "Sesión uno", sessionId: null, kind: null, status: "active", topic: null },
    { id: "observations:o1", table: "observations", recordId: "o1", path: "/mem/observations/o1", label: "Decisión", sessionId: "s1", kind: "decision", status: "active", topic: "tema/a" },
    { id: "observations:o2", table: "observations", recordId: "o2", path: "/mem/observations/o2", label: "Aprendizaje", sessionId: "s1", kind: "learning", status: "superseded", topic: "tema/b" },
    { id: "checkpoints:c1", table: "checkpoints", recordId: "c1", path: "/mem/checkpoints/c1", label: "Checkpoint", sessionId: "s1", kind: null, status: "active", topic: null },
  ],
  edges: [
    { id: "e1", type: "session_id", from: "sessions:s1", to: "observations:o1" },
    { id: "e2", type: "session_id", from: "sessions:s1", to: "observations:o2" },
    { id: "e3", type: "superseded_by", from: "observations:o2", to: "observations:o1" },
    { id: "e4", type: "session_id", from: "sessions:s1", to: "checkpoints:c1" },
  ],
};

const noFilters = { type: "", kind: "", status: "", session: null, topic: "", related: false };

function filterWith(changes, activeId = "") {
  return methods.graphFilterData.call(methods, graph, { ...noFilters, ...changes }, activeId);
}

test("sin filtros conserva todos los nodos y aristas", () => {
  const view = filterWith({});
  assert.equal(view.nodes.length, 4);
  assert.equal(view.edges.length, 4);
});

test("el filtro de tipo reduce nodos y elimina aristas colgantes", () => {
  const view = filterWith({ type: "observations" });
  assert.deepEqual(view.nodes.map((node) => node.id), ["observations:o1", "observations:o2"]);
  assert.deepEqual(view.edges.map((edge) => edge.id), ["e3"]);
});

test("kind, estado, sesión y tema filtran de forma verificable", () => {
  assert.deepEqual(filterWith({ kind: "decision" }).nodes.map((node) => node.id), ["observations:o1"]);
  assert.deepEqual(filterWith({ status: "superseded" }).nodes.map((node) => node.id), ["observations:o2"]);
  assert.equal(filterWith({ session: "s1" }).nodes.length, 4);
  assert.deepEqual(filterWith({ topic: "tema/a" }).nodes.map((node) => node.id), ["observations:o1"]);
});

test("relacionados deja la selección y sus vecinos, con sus aristas", () => {
  const view = filterWith({ related: true }, "observations:o1");
  assert.deepEqual(view.nodes.map((node) => node.id).sort(), [
    "observations:o1",
    "observations:o2",
    "sessions:s1",
  ]);
  assert.deepEqual(view.edges.map((edge) => edge.id).sort(), ["e1", "e2", "e3"]);
});

test("la sesión con id vacío es un valor de filtro real, no «Todas»", () => {
  const emptyIdGraph = {
    nodes: [
      { id: "sessions:", table: "sessions", recordId: "", path: "/mem/sessions/~e", label: "", sessionId: null, kind: null, status: "active", topic: null },
      { id: "observations:o1", table: "observations", recordId: "o1", path: "/mem/observations/o1", label: "Uno", sessionId: "", kind: "learning", status: "active", topic: null },
    ],
    edges: [{ id: "e1", type: "session_id", from: "sessions:", to: "observations:o1" }],
  };
  const view = methods.graphFilterData.call(methods, emptyIdGraph, { ...noFilters, session: "" }, "");
  assert.deepEqual(view.nodes.map((node) => node.id), ["sessions:", "observations:o1"]);
  assert.equal(view.edges.length, 1);
  assert.equal(methods.shortId(""), "(sin id)");
});

test("los valores de los filtros se deduplican y ordenan", () => {
  assert.deepEqual(Array.from(methods.graphFilterValues(graph, "kind")), ["decision", "learning"]);
  assert.deepEqual(Array.from(methods.graphFilterValues(graph, "status")), ["active", "superseded"]);
  assert.deepEqual(Array.from(methods.graphFilterValues(graph, "topic")), ["tema/a", "tema/b"]);
});

test("los tooltips escapan el dato y solo añaden marcado propio", () => {
  const escaped = methods.graphEsc('<img src=x onerror="alert(1)"> & \'');
  assert.equal(escaped, "&lt;img src=x onerror=&quot;alert(1)&quot;&gt; &amp; &#39;");
  const tip = methods.graphNodeTooltip.call(methods, {
    table: "observations",
    recordId: "o1",
    label: "<b>no html</b>",
    kind: "decision",
    status: "active",
  });
  assert.ok(tip.includes("&lt;b&gt;no html&lt;/b&gt;"));
  assert.ok(!tip.includes("<b>"));
  assert.ok(tip.includes('<br><span class="fg-tip-meta">'));
});

test("la liberación pausa, destruye, desconecta el observer y vacía el host", () => {
  const calls = [];
  const context = {
    graphResizeObserver: { disconnect() { calls.push("disconnect"); } },
    graphResizeRaf: 0,
    graph2d: {
      pauseAnimation() { calls.push("pause"); },
      _destructor() { calls.push("destructor"); },
    },
    $refs: { graphCanvas: { replaceChildren() { calls.push("clear"); } } },
    graphFramed: true,
    graphEnginePaused: false,
    graphZoomPct: 140,
  };
  methods.disposeGraph.call(context);
  assert.deepEqual(calls, ["disconnect", "pause", "destructor", "clear"]);
  assert.equal(context.graph2d, null);
  assert.equal(context.graphZoomPct, 100);
});

test("el template de la SPA compila con el Vue local", () => {
  const render = compileAppTemplate();
  assert.equal(typeof render, "function");
});

test("el render inicial con graph null no dereferencia el grafo", () => {
  const render = renderAppTemplate();
  const context = createContext(spec, {
    tablePath: "/mem/observations",
    tableName: "observations",
    mountAlias: "mem",
    describe: { capabilities: { readable: true, navigable: true } },
    columns: [],
    rows: [],
    count: 0,
  });
  assert.equal(context.graph, null);
  assert.equal(context.graphVisible.totalNodes, 0);
  assert.equal(context.graphVisible.totalEdges, 0);
  assert.doesNotThrow(() => render(context, []));
});

test("graphPageCounts conserva páginas y respeta el máximo declarado", () => {
  const counts = methods.graphPageCounts({
    limits: { maxPagesPerTable: 10 },
    tables: {
      sessions: { pageCount: 2 },
      observations: { pageCount: 12 },
      checkpoints: { pageCount: 0 },
      preferences: {},
    },
  });
  assert.deepEqual({ ...counts }, {
    sessions: 2,
    observations: 10,
    checkpoints: 1,
    preferences: 1,
  });
  assert.equal(methods.graphPageCounts(null), undefined);
});

function emptyGraphData() {
  const table = () => ({
    rowCount: 0,
    pageCount: 1,
    hasMore: false,
    truncated: false,
    error: null,
    columns: [],
  });
  return {
    alias: "mem",
    nodes: [],
    edges: [],
    unresolved: [],
    warnings: [],
    complete: true,
    limits: { pageSize: 200, maxPagesPerTable: 10 },
    tables: {
      sessions: table(),
      observations: table(),
      checkpoints: table(),
      preferences: table(),
    },
  };
}

function fakeGraphFactory(counter) {
  return () => (host) => {
    counter.created += 1;
    counter.host = host;
    const target = { paused: false, destroyed: false };
    const instance = new Proxy(target, {
      get(object, property) {
        if (property in object) return object[property];
        if (property === "graphData") {
          return (data) => {
            if (data === undefined) return { nodes: [], links: [] };
            return instance;
          };
        }
        if (property === "zoom") {
          return (...args) => (args.length === 0 ? 1 : instance);
        }
        if (property === "centerAt") {
          return (...args) => (args.length === 0 ? { x: 0, y: 0 } : instance);
        }
        if (property === "pauseAnimation") {
          return () => { object.paused = true; return instance; };
        }
        if (property === "resumeAnimation") {
          return () => { object.paused = false; return instance; };
        }
        if (property === "_destructor") {
          return () => { object.destroyed = true; };
        }
        return () => instance;
      },
    });
    return instance;
  };
}

test("dos cargas concurrentes no inicializan el grafo si la vista cambió a tabla", async () => {
  const harness = createAppHarness();
  const host = { isConnected: true, clientWidth: 320, clientHeight: 240 };
  const context = createContext(harness.spec, {
    graph: emptyGraphData(),
    viewMode: "graph",
    $refs: { graphCanvas: host },
  });
  const counter = { created: 0, host: null };

  harness.sandbox.ForceGraph = undefined;
  const first = harness.spec.methods.ensureForceGraph.call(context);
  const second = harness.spec.methods.ensureForceGraph.call(context);
  assert.equal(harness.sandbox.__scripts.length, 1, "la carga debe compartirse");

  context.viewMode = "table";
  harness.sandbox.ForceGraph = fakeGraphFactory(counter);
  harness.sandbox.__scripts[0].onload();
  await Promise.all([first, second]);

  assert.equal(counter.created, 0, "no debe crear el grafo en la tabla oculta");
  assert.equal(context.graph2d, null);
  assert.equal(context.graphPreparing, false);

  context.viewMode = "graph";
  await harness.spec.methods.ensureForceGraph.call(context);
  assert.equal(counter.created, 1);
  assert.ok(context.graph2d);
  await harness.spec.methods.ensureForceGraph.call(context);
  assert.equal(counter.created, 1, "una segunda llamada no debe duplicar la instancia");
});

test("la distribución local de force-graph carga como ForceGraph", () => {
  const style = { type: "", styleSheet: null, appendChild() {} };
  const head = { firstChild: null, appendChild() {} };
  const sandbox = {
    console,
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    requestAnimationFrame() { return 1; },
    cancelAnimationFrame() {},
    document: {
      head,
      createElement(tag) {
        if (tag === "style") return style;
        if (tag === "canvas") return { getContext() { return {}; } };
        return {};
      },
      createTextNode() { return {}; },
      getElementsByTagName() { return [head]; },
    },
    window: { devicePixelRatio: 1 },
    navigator: { userAgent: "" },
  };
  sandbox.globalThis = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(vendorSource, sandbox, { filename: "force-graph.min.js" });
  assert.equal(typeof sandbox.ForceGraph, "function");
});

test("el desmontaje de la SPA libera el grafo", () => {
  let tornDown = false;
  const context = {
    teardownGraph() { tornDown = true; },
    mqCompact: { removeEventListener() {} },
    mqMobile: { removeEventListener() {} },
    toastTimer: 0,
  };
  spec.beforeUnmount.call(context);
  assert.equal(tornDown, true);
});

test("la vista carga force-graph y el adaptador como recursos locales", () => {
  assert.ok(appSource.includes('import("./acm-graph-data.js")'));
  assert.ok(appSource.includes('"/vendor/force-graph.min.js"'));
  assert.ok(appSource.includes("preservePages: true"));
  assert.ok(appSource.includes("pageCounts"));
  assert.ok(indexSource.includes("Grafo ACM"));
  assert.ok(indexSource.includes("Ver relaciones"));
  assert.ok(indexSource.includes("maxPagesPerTable"));
  assert.ok(!indexSource.includes("force-graph.min.js\"></script>"));
  assert.ok(/<script src="\/vendor\/vue\.global\.prod\.js"><\/script>/.test(indexSource));
  assert.ok(vendorSource.startsWith("// Version 1.52.0 force-graph"));
  assert.ok(vendorSource.includes("_destructor"));
  assert.equal(
    createHash("sha256").update(vendorSource).digest("hex"),
    "f59db59ba92ff5a76ed93d7a4d77a703b176260e5d9cd29f4f8eb4f6c369a629",
  );
  assert.ok(!/https?:\/\/(cdn|unpkg|jsdelivr)/i.test(appSource + indexSource));
  assert.ok(licenseSource.includes("MIT License"));
  assert.ok(licenseSource.includes("Vasco Asturiano"));
  assert.ok(vendorReadme.includes("force-graph@1.52.0"));
  assert.ok(vendorReadme.includes("sha512-0fpxrZzW6qwm9zM7wgh54d7GbxQfQSDV71I0apo+75PVYQPpPHGVPQFLsE9WAu+hOS4VdLd53g0Hua/bxDJKSg=="));
});
