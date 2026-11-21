(() => {
"use strict";

const LS_KEY = "dbai.ve.v1";
const PAGE_SIZES = [25, 50, 100, 250, 1000];
const OPS = ["=", "!=", "<", "<=", ">", ">=", "is-null", "not-null"];

const fmtInt = new Intl.NumberFormat("es-ES");
const fmtReal = new Intl.NumberFormat("es-ES", { maximumFractionDigits: 6 });

function normErr(e) {
  if (e && e.code) return { code: String(e.code), message: String(e.message || "") };
  return { code: "E_UNKNOWN", message: String((e && e.message) || e || "error desconocido") };
}

function clamp(n, min, max) { return Math.min(max, Math.max(min, Math.round(n))); }

function jsonTokens(src) {
  const out = [];
  const re = /("(?:\\.|[^"\\])*")(\s*:)?|\b(true|false)\b|\bnull\b|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)/g;
  let last = 0;
  let m;
  const push = (text, cls) => { if (text) out.push({ text, cls }); };
  while ((m = re.exec(src))) {
    if (m.index > last) push(src.slice(last, m.index), "");
    if (m[1] !== undefined) {
      if (m[2] !== undefined) { push(m[1], "j-key"); push(m[2], "j-punc"); }
      else push(m[1], "j-str");
    } else if (m[3] !== undefined) {
      push(m[3], "j-bool");
    } else if (m[0] === "null") {
      push("null", "j-null");
    } else {
      push(m[0], "j-num");
    }
    last = re.lastIndex;
  }
  if (last < src.length) push(src.slice(last), "");
  return out;
}

/* ---------- tooltips: single delegated manager ---------- */
const TIPS = Object.freeze({
  mounts: "Una base de datos montada: el punto de entrada para explorar sus tablas y registros.",
  tree: "Explora los montajes, sus tablas y las primeras filas. La tabla que elijas se abre en el panel principal.",
  path: "Dirección de un recurso dentro del sistema de archivos virtual: montaje, tabla, registro y campo. Puedes copiarla para usarla en la CLI.",
  readOnly: "Puedes consultar y explorar los datos, pero esta interfaz no permite modificarlos.",
  grep: "Busca el texto tal cual, con distinción de mayúsculas, en las columnas de texto. No interpreta comodines ni expresiones.",
  filters: "Combina columna, operador y valor. Varios filtros se suman con AND. El valor se interpreta como JSON y, si no lo es, como texto.",
  sort: "Ordena por la columna elegida: el primer clic asciende, el segundo desciende y el tercero quita el orden. Los NULL van al final.",
  columns: "Muestra u oculta columnas de la tabla. La preferencia se guarda por tabla.",
  paging: "Avanza o retrocede por bloques de registros mediante un cursor firmado. No es una foto fija: los cambios concurrentes pueden afectar al recorrido.",
  inspector: "Muestra todos los campos del registro seleccionado, con su tipo y su valor completo.",
  noPk: "La tabla puede consultarse, pero sus registros no tienen una identidad compatible con la navegación individual por rutas; la paginación se limita a la primera página.",
  etag: "Identificador de una versión concreta del contenido de una fila. Permite detectar si ha cambiado entre consultas.",
  nullValue: "Valor ausente o desconocido. No es lo mismo que una cadena vacía ni que cero.",
  blob: "Datos binarios. La interfaz muestra su tamaño y permite copiarlos en base64.",
  copyPath: "Copia la ruta virtual del recurso seleccionado, lista para usar en la CLI.",
  refresh: "Vuelve a consultar los datos de la vista actual.",
  help: "Abre la ayuda: cómo empezar, navegación básica, búsqueda, filtros, inspector y conceptos.",
});

const tipState = {
  el: null,
  anchor: null,
  describedBy: null,
  origin: null,
  show(anchor, origin) {
    const text = anchor.getAttribute("data-tip");
    if (!text || !this.el) return;
    if (this.anchor === anchor && this.el.classList.contains("show")) {
      if (origin) this.origin = origin;
      return;
    }
    this.hide();
    this.anchor = anchor;
    this.origin = origin || "hover";
    this.describedBy = anchor.getAttribute("aria-describedby");
    anchor.setAttribute("aria-describedby", "dbai-tip");
    this.el.textContent = text;
    this.el.removeAttribute("aria-hidden");
    this.el.classList.add("show");
    this.place();
  },
  place() {
    if (!this.anchor || !this.el) return;
    const a = this.anchor.getBoundingClientRect();
    const w = this.el.offsetWidth;
    const h = this.el.offsetHeight;
    const vw = document.documentElement.clientWidth;
    const vh = document.documentElement.clientHeight;
    let top = a.top - h - 6;
    if (top < 8) top = a.bottom + 6;
    top = Math.max(8, Math.min(top, vh - h - 8));
    let left = a.left + (a.width - w) / 2;
    left = Math.max(8, Math.min(left, vw - w - 8));
    this.el.style.top = Math.round(top) + "px";
    this.el.style.left = Math.round(left) + "px";
  },
  hide() {
    if (!this.anchor) return;
    if (this.describedBy === null) this.anchor.removeAttribute("aria-describedby");
    else this.anchor.setAttribute("aria-describedby", this.describedBy);
    this.anchor = null;
    this.describedBy = null;
    this.origin = null;
    this.el.classList.remove("show");
    this.el.setAttribute("aria-hidden", "true");
  },
};

function setTip(el, value) {
  const text = value == null ? "" : String(value).trim();
  if (text) el.setAttribute("data-tip", text);
  else el.removeAttribute("data-tip");
}

const tipDirective = {
  mounted(el, binding) { setTip(el, binding.value); },
  updated(el, binding) {
    if (binding.value === binding.oldValue) return;
    setTip(el, binding.value);
    if (tipState.anchor === el && tipState.el && tipState.el.classList.contains("show")) {
      tipState.el.textContent = binding.value == null ? "" : String(binding.value);
      tipState.place();
    }
  },
  beforeUnmount(el) { el.removeAttribute("data-tip"); },
};

function initTooltips() {
  tipState.el = document.getElementById("dbai-tip");
  if (!tipState.el) return;
  const anchorOf = (target) => (target && target.closest ? target.closest("[data-tip]") : null);

  document.addEventListener("pointerover", (event) => {
    if (event.pointerType !== "mouse") return;
    const anchor = anchorOf(event.target);
    if (anchor) tipState.show(anchor, "hover");
  });

  document.addEventListener("pointerout", (event) => {
    if (event.pointerType !== "mouse") return;
    const anchor = anchorOf(event.target);
    if (!anchor || tipState.anchor !== anchor) return;
    if (event.relatedTarget && anchor.contains(event.relatedTarget)) return;
    tipState.hide();
  });

  document.addEventListener("focusin", (event) => {
    const anchor = anchorOf(event.target);
    if (anchor) tipState.show(anchor, "focus");
  });

  document.addEventListener("focusout", (event) => {
    const anchor = anchorOf(event.target);
    if (!anchor || tipState.anchor !== anchor) return;
    if (event.relatedTarget && anchor.contains(event.relatedTarget)) return;
    tipState.hide();
  });

  document.addEventListener("pointerdown", (event) => {
    const anchor = anchorOf(event.target);
    if (!anchor) { tipState.hide(); return; }
    if (event.pointerType !== "touch" && event.pointerType !== "pen") return;
    if (tipState.anchor === anchor) tipState.hide();
    else tipState.show(anchor, "touch");
  });

  /* Escape hides the tip. A focus-only hint must not swallow the Escape that
     closes a drawer, popover or the help panel. */
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape" || !tipState.anchor) return;
    const byFocus = tipState.origin === "focus";
    tipState.hide();
    if (!byFocus) event.stopImmediatePropagation();
  });

  document.addEventListener("scroll", () => tipState.hide(), { capture: true, passive: true });
  window.addEventListener("resize", () => tipState.hide());
}

const app = Vue.createApp({
  data() {
    return {
      mounts: [],
      rootLoading: false,
      rootTruncated: false,
      treeError: null,

      tablePath: "",
      tableName: "",
      mountAlias: "",
      describe: null,
      columns: [],
      rows: [],
      count: 0,
      truncated: false,
      nextCursor: "",
      isLoading: false,
      error: null,
      etagNotice: "",
      lastETag: "",
      lastPageTag: "",

      sort: { column: "", dir: "" },
      filters: [],
      filterOpen: false,
      filterError: null,
      draftFilter: { column: "", op: "=", value: "" },
      ops: OPS,

      pageSize: 25,
      pageSizes: PAGE_SIZES,
      cursors: [""],
      pageIndex: 0,

      hiddenByTable: {},
      colMenuOpen: false,

      selectedPath: "",
      selectedFields: null,
      selectedETag: "",
      inspectorLoading: false,
      inspectorError: null,
      highlightField: "",

      searchText: "",
      searchOpen: false,
      searchLoading: false,
      searchEntries: [],
      searchTruncated: false,
      searchCount: null,
      searchError: null,

      inspOpen: true,
      treeWidth: 200,
      inspWidth: 288,
      resize: null,

      compact: false,
      mobile: false,
      drawer: "",
      drawerOpener: null,
      helpOpen: false,
      helpOpener: null,
      tips: TIPS,

      toast: "",
      toastTimer: null,

      reqId: 0,
      rowReq: 0,
    };
  },

  computed: {
    visibleColumns() {
      if (!this.columns.length) return [];
      const hidden = this.hiddenByTable[this.tablePath] || [];
      const shown = this.columns.filter((c) => hidden.indexOf(c.name) === -1);
      return shown.length ? shown : this.columns;
    },
    tableMinWidth() {
      return this.visibleColumns.reduce((sum, c) => sum + this.colWidthPx(c), 0);
    },
    workspaceStyle() {
      return {
        "--tree-w": this.treeWidth + "px",
        "--insp-w": (this.inspOpen ? this.inspWidth : 34) + "px",
      };
    },
    selectedKey() {
      const parts = this.selectedPath.split("/");
      return parts[parts.length - 1] || "";
    },
    shortKey() {
      const key = this.selectedKey;
      return key.length > 20 ? key.slice(0, 8) + "…" + key.slice(-6) : key;
    },
    displayPath() {
      return this.selectedPath || this.tablePath || "/";
    },
    rangeLabel() {
      const start = this.pageIndex * this.pageSize + 1;
      const end = this.pageIndex * this.pageSize + this.rows.length;
      if (!this.rows.length) return "0 de " + this.num(this.count);
      return this.num(start) + "–" + this.num(end) + " de " + this.num(this.count);
    },
    canPrev() { return this.pageIndex > 0; },
    canNext() { return Boolean(this.nextCursor); },
    isNullOp() { return this.isNullOpFor(this.draftFilter.op); },
    searchPlaceholder() {
      return this.tablePath ? "Buscar literalmente en " + this.tablePath : "Selecciona una tabla";
    },
    inspectorColumns() {
      if (!this.selectedFields) return [];
      if (this.columns.length) return this.columns;
      return Object.keys(this.selectedFields).sort().map((name) => ({ name: name, declared_type: "" }));
    },
    readable() { return Boolean(this.describe && this.describe.capabilities && this.describe.capabilities.readable); },
    navigable() { return Boolean(this.describe && this.describe.capabilities && this.describe.capabilities.navigable); },
    etagTip() {
      return this.lastETag ? TIPS.etag + " Valor completo: " + this.lastETag : TIPS.etag;
    },
  },

  methods: {
    /* ---------- prefs ---------- */
    loadPrefs() {
      try {
        const p = JSON.parse(localStorage.getItem(LS_KEY) || "{}");
        if (p && typeof p === "object") {
          if (PAGE_SIZES.indexOf(p.pageSize) !== -1) this.pageSize = p.pageSize;
          if (p.hidden && typeof p.hidden === "object" && !Array.isArray(p.hidden)
              && Object.keys(p.hidden).every((k) => Array.isArray(p.hidden[k]) && p.hidden[k].every((v) => typeof v === "string"))) {
            this.hiddenByTable = p.hidden;
          }
          if (typeof p.inspOpen === "boolean") this.inspOpen = p.inspOpen;
          if (typeof p.treeWidth === "number") this.treeWidth = clamp(p.treeWidth, 168, 320);
          if (typeof p.inspWidth === "number") this.inspWidth = clamp(p.inspWidth, 248, 460);
        }
      } catch (e) { /* prefs are best-effort */ }
    },
    savePrefs() {
      try {
        localStorage.setItem(LS_KEY, JSON.stringify({
          pageSize: this.pageSize,
          hidden: this.hiddenByTable,
          inspOpen: this.inspOpen,
          treeWidth: this.treeWidth,
          inspWidth: this.inspWidth,
        }));
      } catch (e) { /* prefs are best-effort */ }
    },

    /* ---------- API ---------- */
    async api(endpoint, pairs) {
      const qs = new URLSearchParams();
      (pairs || []).forEach((pair) => qs.append(pair[0], pair[1]));
      const url = "/api/" + endpoint + (qs.toString() ? "?" + qs.toString() : "");
      let resp;
      try {
        resp = await fetch(url, { headers: { Accept: "application/json" } });
      } catch (e) {
        throw { code: "E_NETWORK", message: "no se pudo contactar con db-ai serve" };
      }
      let body;
      try {
        body = await resp.json();
      } catch (e) {
        throw { code: "E_HTTP_" + resp.status, message: "respuesta no JSON del servidor" };
      }
      if (!body || body.ok !== true) {
        const err = (body && body.error) || {};
        throw {
          code: err.code || "E_HTTP_" + resp.status,
          message: err.message || ("HTTP " + resp.status),
        };
      }
      return body;
    },
    handleError(e) { this.error = normErr(e); },
    normErr,

    /* ---------- tree ---------- */
    async loadMounts() {
      this.rootLoading = true;
      this.treeError = null;
      try {
        const body = await this.api("ls", [["path", "/"], ["limit", "1000"]]);
        this.rootTruncated = Boolean(body.meta && body.meta.truncated);
        this.mounts = (body.data.entries || []).map((e) => ({
          name: e.name, path: e.path,
          expanded: false, loaded: false, loading: false, tables: [], error: null,
        }));
      } catch (e) {
        this.mounts = [];
        this.treeError = normErr(e);
      } finally {
        this.rootLoading = false;
      }
    },
    toggleMount(m) {
      m.expanded = !m.expanded;
      if (m.expanded && !m.loaded && !m.loading) this.loadTables(m);
    },
    async loadTables(m) {
      m.loading = true;
      m.error = null;
      try {
        const body = await this.api("ls", [["path", m.path], ["limit", "1000"]]);
        m.tables = (body.data.entries || []).map((e) => ({
          name: e.name, path: e.path, navigable: null,
          expanded: false, loaded: false, loading: false, rows: [], truncated: false, error: null,
        }));
        m.loaded = true;
      } catch (e) {
        m.error = normErr(e);
      } finally {
        m.loading = false;
      }
    },
    toggleRows(t) {
      if (t.navigable === false) return;
      t.expanded = !t.expanded;
      if (t.expanded && !t.loaded && !t.loading) this.loadRows(t);
    },
    async loadRows(t) {
      if (t.navigable === false) return;
      t.loading = true;
      t.error = null;
      try {
        const body = await this.api("ls", [["path", t.path], ["limit", "100"]]);
        t.rows = (body.data.entries || []).map((e) => ({ name: e.name, path: e.path }));
        t.truncated = Boolean(body.meta && body.meta.truncated);
        t.loaded = true;
      } catch (e) {
        t.error = normErr(e);
      } finally {
        t.loading = false;
      }
    },
    async activateRow(rowPath) {
      const parts = rowPath.split("/").filter(Boolean);
      const tablePath = "/" + parts.slice(0, 2).join("/");
      if (this.tablePath !== tablePath) await this.openTable(tablePath, { autoSelect: false });
      this.openRowPath(rowPath, "");
    },
    treeTableNode(path) {
      for (const m of this.mounts) {
        const t = m.tables.find((x) => x.path === path);
        if (t) return t;
      }
      return null;
    },

    /* ---------- table ---------- */
    clearTable() {
      this.reqId++;
      this.tablePath = "";
      this.tableName = "";
      this.mountAlias = "";
      this.describe = null;
      this.columns = [];
      this.rows = [];
      this.count = 0;
      this.truncated = false;
      this.nextCursor = "";
      this.selectedPath = "";
      this.selectedFields = null;
      this.selectedETag = "";
      this.highlightField = "";
      this.filters = [];
      this.filterError = null;
      this.filterOpen = false;
      this.searchText = "";
      this.searchOpen = false;
      this.searchEntries = [];
      this.searchCount = null;
      this.searchError = null;
      this.error = null;
      this.etagNotice = "";
      this.lastETag = "";
      this.lastPageTag = "";
      this.isLoading = false;
      this.sort = { column: "", dir: "" };
      this.resetPage();
    },
    async openTable(path, options) {
      if (this.drawer === "tree") this.closeDrawer();
      if (!path || this.tablePath === path) return;
      options = options || {};
      this.reqId++;
      const my = this.reqId;

      this.tablePath = path;
      const parts = path.split("/").filter(Boolean);
      this.mountAlias = parts[0] || "";
      this.tableName = parts[1] || "";
      this.describe = null;
      this.columns = [];
      this.rows = [];
      this.count = 0;
      this.truncated = false;
      this.nextCursor = "";
      this.selectedPath = "";
      this.selectedFields = null;
      this.selectedETag = "";
      this.highlightField = "";
      this.filters = [];
      this.filterError = null;
      this.filterOpen = false;
      this.searchText = "";
      this.searchOpen = false;
      this.searchEntries = [];
      this.searchCount = null;
      this.searchError = null;
      this.error = null;
      this.etagNotice = "";
      this.lastETag = "";
      this.lastPageTag = "";
      this.sort = { column: "", dir: "" };
      this.resetPage();
      this.isLoading = true;

      try {
        const body = await this.api("describe", [["path", path]]);
        if (my !== this.reqId) return;
        this.describe = body.data;
        this.columns = body.data.columns || [];
        const node = this.treeTableNode(path);
        if (node) {
          node.navigable = Boolean(body.data.capabilities.navigable);
          if (!node.navigable) node.expanded = false;
        }

        const previous = this.hiddenByTable[path] || [];
        const valid = previous.filter((name) => this.columns.some((c) => c.name === name));
        if (valid.length >= this.columns.length) valid.length = 0;
        this.hiddenByTable[path] = valid;
        this.savePrefs();

        this.draftFilter.column = this.columns.length ? this.columns[0].name : "";
        if (!body.data.capabilities.readable) {
          this.error = { code: "E_DENIED", message: "la tabla no es legible con la política de este montaje" };
          return;
        }
        await Promise.all([this.reloadPage(), this.loadCount()]);
        if (my !== this.reqId) return;
        if (options.autoSelect !== false && this.rows.length && this.describe.capabilities.navigable) {
          this.openRowPath(this.rows[0].path, "");
        }
      } catch (e) {
        if (my === this.reqId) this.handleError(e);
      } finally {
        if (my === this.reqId) this.isLoading = false;
      }
    },
    findParams(cursor) {
      const pairs = [["path", this.tablePath], ["limit", String(this.pageSize)]];
      if (this.sort.column) {
        pairs.push(["order", this.sort.column + (this.sort.dir === "desc" ? ":desc" : ":asc")]);
      }
      this.filters.forEach((f) => pairs.push(["where", this.filterExpr(f)]));
      if (cursor) pairs.push(["cursor", cursor]);
      return pairs;
    },
    async reloadPage(options) {
      options = options || {};
      const my = this.reqId;
      this.isLoading = true;
      this.error = null;
      try {
        const body = await this.api("find", this.findParams(this.cursors[this.pageIndex]));
        if (my !== this.reqId) return;
        this.rows = body.data.rows || [];
        this.truncated = Boolean(body.meta && body.meta.truncated);
        this.nextCursor = (body.meta && body.meta.next_cursor) || "";
        const tag = this.rows.map((row) => row.etag).join("|");
        if (options.compare) {
          if (this.lastPageTag && tag && tag !== this.lastPageTag) {
            this.etagNotice = "ETag de la tabla cambió en esta página";
          } else {
            this.etagNotice = "";
          }
        } else {
          this.etagNotice = "";
        }
        this.lastPageTag = tag;
        if (this.rows.length) this.lastETag = this.rows[0].etag;
      } catch (e) {
        if (my === this.reqId) this.handleError(e);
      } finally {
        if (my === this.reqId) this.isLoading = false;
      }
    },
    async loadCount() {
      const my = this.reqId;
      try {
        const pairs = [["path", this.tablePath]];
        this.filters.forEach((f) => pairs.push(["where", this.filterExpr(f)]));
        const body = await this.api("count", pairs);
        if (my !== this.reqId) return;
        this.count = body.data.count;
        this.filterError = null;
      } catch (e) {
        if (my === this.reqId) this.filterError = normErr(e);
      }
    },
    async refresh() {
      if (!this.tablePath) {
        await this.loadMounts();
        this.toastMsg("Montajes recargados");
        return;
      }
      if (this.pageIndex > 0 && this.error && this.error.code === "E_USAGE") this.resetPage();
      await Promise.all([this.reloadPage({ compare: true }), this.loadCount()]);
      if (!this.error) {
        this.toastMsg(this.etagNotice ? "Datos cambiados · ETag distinto" : "Vista actualizada");
      }
      if (this.selectedPath) this.openRowPath(this.selectedPath, this.highlightField);
    },
    async applyQueryChange() {
      this.resetPage();
      await Promise.all([this.reloadPage(), this.loadCount()]);
    },
    resetPage() {
      this.cursors = [""];
      this.pageIndex = 0;
    },
    sortBy(col) {
      if (!this.readable) return;
      if (this.sort.column !== col.name) {
        this.sort = { column: col.name, dir: "asc" };
      } else if (this.sort.dir === "asc") {
        this.sort = { column: col.name, dir: "desc" };
      } else {
        this.sort = { column: "", dir: "" };
      }
      this.applyQueryChange();
    },
    sortMark(col) {
      if (this.sort.column !== col.name) return "↕";
      return this.sort.dir === "asc" ? "↑" : "↓";
    },
    ariaSort(col) {
      if (this.sort.column !== col.name) return "none";
      return this.sort.dir === "asc" ? "ascending" : "descending";
    },
    changePageSize() {
      this.savePrefs();
      this.applyQueryChange();
    },
    prevPage() {
      if (this.isLoading || !this.canPrev) return;
      this.pageIndex--;
      this.reloadPage();
    },
    nextPage() {
      if (this.isLoading || !this.nextCursor) return;
      this.cursors = this.cursors.slice(0, this.pageIndex + 1).concat([this.nextCursor]);
      this.pageIndex = this.cursors.length - 1;
      this.reloadPage();
    },

    /* ---------- columns ---------- */
    toggleColMenu() { this.colMenuOpen = !this.colMenuOpen; },
    isColumnVisible(col) {
      const hidden = this.hiddenByTable[this.tablePath] || [];
      return hidden.indexOf(col.name) === -1;
    },
    toggleColumn(col, event) {
      const checked = event.target.checked;
      let hidden = (this.hiddenByTable[this.tablePath] || []).slice();
      if (checked) hidden = hidden.filter((name) => name !== col.name);
      else if (hidden.indexOf(col.name) === -1) hidden.push(col.name);
      if (this.columns.length - hidden.length < 1) {
        event.target.checked = true;
        this.toastMsg("Debe quedar al menos una columna visible");
        return;
      }
      this.hiddenByTable = Object.assign({}, this.hiddenByTable, { [this.tablePath]: hidden });
      this.savePrefs();
    },
    showAllColumns() {
      this.hiddenByTable = Object.assign({}, this.hiddenByTable, { [this.tablePath]: [] });
      this.savePrefs();
    },
    colWidthPx(col) {
      const t = (col.declared_type || "").toUpperCase();
      if (/INT|REAL|FLOA|DOUB|NUMERIC|DEC/.test(t)) return 120;
      if (/BLOB/.test(t)) return 140;
      return 220;
    },
    isBoolColumn(col) { return /BOOL/i.test(col.declared_type || ""); },
    cell(row, col) { return (row.fields && row.fields[col.name]) || null; },
    cellTitle(value) {
      /* Full value only where it is truncated; NULL and BLOB have their own tooltip. */
      if (!value || value.kind === "null" || value.kind === "blob") return null;
      return String(value.data);
    },
    /* `kind` y `status` se pintan como etiquetas; el tono es estable por valor.
       `status` usa tonos convencionales; el resto, un hash sin signo (>>> 0:
       `& 0xffffffff` en JS da enteros con signo y restos negativos). */
    isTagCell(col, value) {
      if (!value || value.kind !== "text") return false;
      const name = String(col.name || "").toLowerCase();
      if (name !== "kind" && name !== "status") return false;
      const text = String(value.data).trim();
      return text.length > 0 && text.length <= 32;
    },
    tagTone(col, value) {
      const name = String(col.name || "").toLowerCase();
      if (name === "status") {
        const state = String(value.data).trim().toLowerCase();
        if (/^(active|ok|done|closed|valid)$/.test(state)) return "is-ok";
        if (/^(pending|open|in_progress|running|queued)$/.test(state)) return "is-pending";
        if (/^(blocked|failed|error|invalid)$/.test(state)) return "is-blocked";
        if (/^(superseded|archived|stale|inactive)$/.test(state)) return "is-muted";
      }
      return "tone-" + this.toneOf(value.data);
    },
    toneOf(value) {
      const s = String(value);
      let h = 5381;
      for (let i = 0; i < s.length; i += 1) h = ((h * 33) + s.charCodeAt(i)) >>> 0;
      return h % 7;
    },

    /* ---------- filters ---------- */
    toggleFilters() { this.filterOpen = !this.filterOpen; },
    isNullOpFor(op) { return op === "is-null" || op === "not-null"; },
    filterExpr(f) {
      if (this.isNullOpFor(f.op)) return f.column + " " + f.op;
      return f.column + " " + f.op + " " + f.value;
    },
    addFilter() {
      if (!this.draftFilter.column) return;
      this.filters = this.filters.concat([{
        column: this.draftFilter.column,
        op: this.draftFilter.op,
        value: this.draftFilter.value,
      }]);
      this.draftFilter.value = "";
      this.filterOpen = false;
      this.applyQueryChange();
    },
    removeFilter(index) {
      this.filters = this.filters.filter((_, i) => i !== index);
      this.applyQueryChange();
    },
    clearFilters() {
      this.filters = [];
      this.applyQueryChange();
    },

    /* ---------- search ---------- */
    async runGrep() {
      const text = this.searchText.trim();
      if (!this.tablePath) return;
      if (!text) {
        this.searchOpen = false;
        this.searchEntries = [];
        this.searchCount = null;
        return;
      }
      this.searchOpen = true;
      this.searchLoading = true;
      this.searchError = null;
      this.searchEntries = [];
      this.searchTruncated = false;
      this.searchCount = null;
      const my = this.reqId;
      try {
        const [grep, count] = await Promise.all([
          this.api("grep", [["path", this.tablePath], ["text", text], ["limit", "200"]]),
          this.api("count", [["path", this.tablePath], ["text", text]]).catch(() => null),
        ]);
        if (my !== this.reqId) return;
        this.searchEntries = (grep.data.entries || []).filter((e) => e.row);
        this.searchTruncated = Boolean(grep.meta && grep.meta.truncated);
        this.searchCount = count ? count.data.count : null;
      } catch (e) {
        if (my === this.reqId) this.searchError = normErr(e);
      } finally {
        if (my === this.reqId) this.searchLoading = false;
      }
    },
    jumpTo(entry) {
      if (!this.navigable) {
        this.toastMsg("Esta tabla no permite abrir filas por ruta");
        return;
      }
      this.searchOpen = false;
      const searchInput = document.getElementById("q");
      if (searchInput) searchInput.focus();
      const row = this.rows.find((r) => r.path === entry.row);
      if (row) this.selectRow(row, entry.column);
      else this.openRowPath(entry.row, entry.column);
    },

    /* ---------- rows and inspector ---------- */
    selectRow(row, highlight, event) {
      if (!this.navigable) return;
      this.openRowPath(row.path, highlight || "");
      if (this.mobile && this.drawer !== "insp") {
        this.openDrawer("insp", event ? event.currentTarget : null);
      }
    },
    async openRowPath(path, highlight) {
      if (!path) return;
      this.rowReq++;
      const my = this.rowReq;
      this.selectedPath = path;
      this.selectedFields = null;
      this.inspectorError = null;
      this.inspectorLoading = true;
      this.highlightField = highlight || "";
      try {
        const body = await this.api("cat", [["path", path]]);
        if (my !== this.rowReq) return;
        this.selectedFields = body.data.fields || {};
        this.selectedETag = body.data.etag || "";
      } catch (e) {
        if (my === this.rowReq) this.inspectorError = normErr(e);
      } finally {
        if (my === this.rowReq) this.inspectorLoading = false;
      }
    },
    inspectorValue(name) {
      if (!this.selectedFields) return null;
      return this.selectedFields[name] || null;
    },
    setInspector(open) {
      this.inspOpen = open;
      this.savePrefs();
    },
    collapseInspector() {
      if (this.compact) this.closeDrawer();
      else this.setInspector(false);
    },
    goRoot() { this.clearTable(); },
    goMount() {
      const alias = this.mountAlias;
      this.clearTable();
      const m = this.mounts.find((x) => x.name === alias);
      if (m) {
        m.expanded = true;
        if (!m.loaded && !m.loading) this.loadTables(m);
      }
    },
    goTable() { this.selectedPath = ""; this.selectedFields = null; this.highlightField = ""; this.inspectorError = null; },

    /* ---------- value rendering ---------- */
    num(value) {
      if (typeof value === "number") return fmtInt.format(value);
      const raw = String(value);
      try {
        if (/^-?\d+$/.test(raw)) return fmtInt.format(BigInt(raw));
      } catch (e) { /* falls back to the raw string */ }
      return raw;
    },
    real(value) {
      const n = Number(value);
      return Number.isFinite(n) ? fmtReal.format(n) : String(value);
    },
    blobBytes(base64) {
      const raw = String(base64).replace(/[=\s]/g, "");
      return Math.floor(raw.length * 3 / 4);
    },
    jsonTokensOf(value) {
      if (!value || value.kind !== "text") return null;
      const text = String(value.data).trim();
      if (!text.startsWith("{") && !text.startsWith("[")) return null;
      try { JSON.parse(text); } catch (e) { return null; }
      return jsonTokens(text);
    },
    valueText(value) {
      if (!value || value.kind === "null") return "NULL";
      return String(value.data);
    },

    /* ---------- clipboard and toast ---------- */
    async copyText(text, message) {
      let ok = true;
      try {
        await navigator.clipboard.writeText(text);
      } catch (e) { ok = false; }
      this.toastMsg(ok ? (message || "Copiado") : "No se pudo copiar al portapapeles");
      return ok;
    },
    copyPath() { this.copyText(this.displayPath, "Ruta copiada"); },
    copyCell(value) { this.copyText(this.valueText(value), "Valor copiado"); },
    copyFieldValue(col) { this.copyText(this.valueText(this.selectedFields[col.name]), col.name + " copiado"); },
    toastMsg(message) {
      clearTimeout(this.toastTimer);
      this.toast = message;
      this.toastTimer = setTimeout(() => { this.toast = ""; }, 1700);
    },

    /* ---------- splitter ---------- */
    startResize(kind, event) {
      if (event.button !== 0) return;
      this.resize = {
        kind,
        startX: event.clientX,
        startW: kind === "tree" ? this.treeWidth : this.inspWidth,
        pointerId: event.pointerId,
      };
      event.currentTarget.setPointerCapture(event.pointerId);
      document.body.classList.add("resizing");
      event.preventDefault();
    },
    moveResize(event) {
      if (!this.resize || event.pointerId !== this.resize.pointerId) return;
      const dx = event.clientX - this.resize.startX;
      if (this.resize.kind === "tree") {
        this.treeWidth = clamp(this.resize.startW + dx, 168, 320);
      } else {
        this.inspWidth = clamp(this.resize.startW - dx, 248, 460);
      }
    },
    endResize(event) {
      if (!this.resize) return;
      if (event && event.pointerId === this.resize.pointerId) {
        const el = event.currentTarget;
        if (el.hasPointerCapture(event.pointerId)) el.releasePointerCapture(event.pointerId);
      }
      this.resize = null;
      document.body.classList.remove("resizing");
      this.savePrefs();
    },
    resizeKey(kind, event) {
      let d = 0;
      if (event.key === "ArrowLeft") d = -8;
      if (event.key === "ArrowRight") d = 8;
      if (!d) return;
      event.preventDefault();
      if (kind === "tree") this.treeWidth = clamp(this.treeWidth + d, 168, 320);
      else this.inspWidth = clamp(this.inspWidth - d, 248, 460);
      this.savePrefs();
    },

    /* ---------- drawers and focus ---------- */
    drawerRoot() {
      if (this.drawer === "tree") return this.$refs.tree;
      if (this.drawer === "insp") return this.$refs.insp;
      return null;
    },
    focusables(root) {
      if (!root) return [];
      const nodes = root.querySelectorAll(
        'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])'
      );
      return Array.prototype.filter.call(nodes, (el) => el.getClientRects().length > 0);
    },
    trapTab(event, root) {
      const items = this.focusables(root);
      if (!items.length) {
        event.preventDefault();
        return;
      }
      const first = items[0];
      const last = items[items.length - 1];
      const active = document.activeElement;
      if (event.shiftKey && (active === first || !root.contains(active))) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && (active === last || !root.contains(active))) {
        event.preventDefault();
        first.focus();
      }
    },
    openDrawer(kind, opener) {
      if (!this.compact) return;
      tipState.hide();
      this.drawerOpener = opener || document.activeElement;
      this.drawer = kind;
      this.$nextTick(() => {
        const root = this.drawerRoot();
        if (!root) return;
        const first = this.focusables(root)[0];
        (first || root).focus();
      });
    },
    closeDrawer() {
      if (!this.drawer) return;
      const opener = this.drawerOpener;
      this.drawer = "";
      this.drawerOpener = null;
      this.$nextTick(() => {
        if (opener && document.contains(opener) && opener.getClientRects().length) { opener.focus(); tipState.hide(); }
      });
    },
    toggleDrawer(kind, event) {
      if (this.drawer === kind) this.closeDrawer();
      else this.openDrawer(kind, event.currentTarget);
    },
    openHelp(event) {
      tipState.hide();
      this.helpOpener = event ? event.currentTarget : document.activeElement;
      this.helpOpen = true;
      this.$nextTick(() => {
        const root = this.$refs.help;
        if (!root) return;
        const first = this.focusables(root)[0];
        (first || root).focus();
      });
    },
    closeHelp() {
      if (!this.helpOpen) return;
      const opener = this.helpOpener;
      this.helpOpen = false;
      this.helpOpener = null;
      this.$nextTick(() => {
        if (opener && document.contains(opener) && opener.getClientRects().length) { opener.focus(); tipState.hide(); }
      });
    },
    onBreakpointChange() {
      this.compact = this.mqCompact.matches;
      this.mobile = this.mqMobile.matches;
      if (!this.compact) this.closeDrawer();
    },

    /* ---------- global input ---------- */
    onDocClick(event) {
      if (this.colMenuOpen && !event.target.closest("#col-menu") && !event.target.closest("#columns-btn")) {
        this.colMenuOpen = false;
      }
      if (this.filterOpen && !event.target.closest("#filter-pop") && !event.target.closest("#filter-btn")) {
        this.filterOpen = false;
      }
      if (this.searchOpen && !event.target.closest(".search-wrap")) {
        this.searchOpen = false;
      }
    },
    onKey(event) {
      if (this.helpOpen) {
        if (event.key === "Escape") {
          event.preventDefault();
          this.closeHelp();
          return;
        }
        if (event.key === "Tab") {
          this.trapTab(event, this.$refs.help);
          return;
        }
        return;
      }
      if (this.drawer) {
        if (event.key === "Escape") {
          event.preventDefault();
          this.closeDrawer();
          return;
        }
        if (event.key === "Tab") {
          this.trapTab(event, this.drawerRoot());
        }
        return;
      }
      if (event.key === "Escape") {
        if (this.searchOpen) this.searchOpen = false;
        if (this.colMenuOpen) this.colMenuOpen = false;
        if (this.filterOpen) this.filterOpen = false;
      }
      if (event.key === "/" && !/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName)) {
        const input = document.getElementById("q");
        if (input && !input.disabled) {
          event.preventDefault();
          input.focus();
        }
      }
    },
  },

  mounted() {
    this.loadPrefs();
    this.loadMounts();
    this.mqCompact = window.matchMedia("(max-width:1199.98px)");
    this.mqMobile = window.matchMedia("(max-width:767.98px)");
    this.onBreakpointChange();
    this.mqCompact.addEventListener("change", this.onBreakpointChange);
    this.mqMobile.addEventListener("change", this.onBreakpointChange);
    document.addEventListener("click", this.onDocClick);
    document.addEventListener("keydown", this.onKey);
  },
  beforeUnmount() {
    this.mqCompact.removeEventListener("change", this.onBreakpointChange);
    this.mqMobile.removeEventListener("change", this.onBreakpointChange);
    document.removeEventListener("click", this.onDocClick);
    document.removeEventListener("keydown", this.onKey);
    clearTimeout(this.toastTimer);
  },
});

app.directive("tip", tipDirective);
initTooltips();
app.mount("#app");
})();
