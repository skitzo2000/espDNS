// The Query log page (querylog.html):
// what the devices asked, newest first, from every node. One box filters by site or device
// (a device by its address or by its name from the hosted zones' records), the chips show
// everything, the blocked or the failed, and narrow it to one node or one device. Each row
// says why it was answered as it was (the cache, a zone, forwarded, or blocked, with Allow,
// which waits in the pending changes until Apply). What each node keeps about a device (its
// privacy setting) is said above the rows. The rows follow live (/api/querylog every 3 s,
// what came after the last read) unless paused. Under them, the sites and devices asked
// most (/api/querylog/top every 30 s) and the last hour's charts (/api/dashboard every
// 10 s). Names and addresses come from the nodes, so hostile: text only, never markup.
import { shell, h, arr, obj, str, toast, panel, refreshChanges, getJSON, post, session, nodeText } from "./shell.js";
import { every } from "./common.js";

shell("querylog.html");

const SHOW = 300;     // rows shown at most
const EVERY = 3000;   // ms between follows
const SHOWS = { all: "", blocked: "blocked,overridden", failed: "refused,servfail,error,dropped" };
const ALLOWF = "overrides-allow.txt"; // the always-allowed sites (internal/blocking OverridesAllow)
const SVG = "http://www.w3.org/2000/svg";

const $ = id => document.getElementById(id);
const fmt = n => (typeof n === "number" && isFinite(n) ? n.toLocaleString() : "–");
const st = {
  q: "", show: "all", node: "", device: "", paused: false,
  rows: [], last: 0, run: "", matched: 0, held: 0, cap: 0, oldest: null, newest: null,
  names: {}, nodes: [], clients: "", gen: 0, loaded: false, error: "", sess: null,
};

// ---- the filters, kept in the page's address ---------------------------------------------
// A search result links here as ?name= or ?client= (or #query-log?client=, as the GUI
// names the page): a name goes in the box, an address is that device.
{
  const hash = location.hash.includes("?") ? location.hash.slice(location.hash.indexOf("?") + 1) : "";
  for (const p of [new URLSearchParams(location.search), new URLSearchParams(hash)]) {
    if (p.get("q") || p.get("name")) st.q = p.get("q") || p.get("name");
    if (p.get("device") || p.get("client")) st.device = p.get("device") || p.get("client");
    if (p.get("node")) st.node = p.get("node");
    if (Object.hasOwn(SHOWS, p.get("show") || "")) st.show = p.get("show");
  }
  $("qlq").value = st.q;
}

function keep() {
  const p = new URLSearchParams();
  if (st.q) p.set("q", st.q);
  if (st.show !== "all") p.set("show", st.show);
  if (st.node) p.set("node", st.node);
  if (st.device) p.set("device", st.device);
  const s = p.toString();
  history.replaceState(null, "", s ? `?${s}` : location.pathname);
}

function query(after) {
  const p = new URLSearchParams();
  if (st.q) p.set("q", st.q);
  if (SHOWS[st.show]) p.set("result", SHOWS[st.show]);
  if (st.node) p.set("node", st.node);
  if (st.device) p.set("device", st.device);
  if (after) { p.set("after", String(after)); p.set("run", st.run); }
  p.set("limit", String(after ? 1000 : SHOW));
  return `api/querylog?${p}`;
}

// ---- a row --------------------------------------------------------------------------------

const clock = t => new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });
function when(e) {
  const d = new Date(e.at);
  if (Number.isNaN(d.getTime())) return "–";
  const t = clock(d);
  return (e.estimated ? "~" : "") + (d.toDateString() === new Date().toDateString() ? t : `${d.toLocaleDateString()} ${t}`);
}
const took = us => (typeof us === "number" ? `${(us / 1000).toFixed(us < 10000 ? 1 : 0)} ms` : "–");
const logNode = key => st.nodes.find(n => n.key === key);
const nodeWord = key => { const n = logNode(key); return n ? nodeText(n.addr || n.key, n.name || n.addr) : str(key) || "–"; };
// A device's name is its record's first label (the full name on hover), else its address.
const devName = a => str(st.names[a]);
const devShort = a => (devName(a) ? devName(a).split(".")[0] : a);
const RCODE = { NXDOMAIN: "No such name", SERVFAIL: "SERVFAIL", REFUSED: "Refused", FORMERR: "Malformed query", NOTIMP: "Not supported" };
const rcodeWord = r => (r && r !== "NOERROR" ? RCODE[r] || str(r) : "");
const blockedMark = () => h("span", { class: "state bad" }, h("span", { class: "dot" }), "Blocked");

// answer says why the query was answered as it was: the words, and a line under them.
function answer(e) {
  const r = str(e.result), rc = rcodeWord(e.rcode);
  const sub = t => (t ? h("div", { class: "sub" }, t) : null);
  const plain = (w, s) => [h("span", {}, w), sub(s)];
  const failed = (w, s) => [h("span", { class: "state warn" }, h("span", { class: "dot" }), w), sub(s)];
  switch (r) {
    case "cache": return plain("Cache", rc);
    case "forwarded": return plain("Forwarded", [rc, e.rule === "allow" ? "Always allowed by you" : ""].filter(Boolean).join(" · "));
    case "hosted": return plain("Your zone", rc);
    case "secondary": return plain("Copied zone", rc);
    case "notify": return plain("Zone notice", "From your zone primary");
    case "overridden": return [blockedMark(), sub("Always blocked by you")];
    case "blocked": {
      if (e.rule === "cname") return [blockedMark(), sub("It points to a blocked site")];
      const site = str(e.qname).toLowerCase().replace(/\.$/, "");
      const s = sub("By a blocklist");
      if (!e.truncated && site && st.sess && st.sess.logged_in) {
        const b = h("button", { class: "btn link small", type: "button", title: `Always allow ${site} (waits in changes to apply)` }, "Allow");
        b.addEventListener("click", () => allow(site, b));
        s.append(" · ", b);
      }
      return [blockedMark(), s];
    }
    case "refused": return failed("Refused", "Not allowed to ask this node");
    case "servfail": return failed("Failed", "No answer found (SERVFAIL)");
    case "error": return failed("Bad query", rc || "The node couldn't read it");
    case "dropped": return failed("Dropped", "Not answered");
    default: return plain(r || "?", rc);
  }
}

function row(e) {
  const t = h("div", { class: "num" }, h("div", { class: "tm", title: e.estimated ? "The node's clock wasn't set: estimated from its uptime." : null }, when(e)),
    h("div", { class: "sub" }, nodeWord(e.node)));
  let dev;
  if (typeof e.client === "string") {
    const full = devName(e.client);
    dev = h("div", {}, h("button", { class: full ? "btn link dev" : "btn link dev mono", type: "button", title: `${full ? `${full} · ` : ""}Show only this device`, onclick: () => setDevice(e.client) }, devShort(e.client)),
      full ? h("div", { class: "sub mono" }, e.client) : null);
  } else {
    dev = h("div", { class: "muted", title: "This node keeps nothing about devices (its query log setting)." }, "Not kept");
  }
  const site = h("div", { class: "mono full-n site" }, str(e.qname));
  if (e.truncated) site.append(" ", h("span", { class: "hint", title: "The name was longer than the node keeps: its last labels." }, "(cut)"));
  const r = h("div", { class: "r", "data-q": "" }, t, dev, site, h("div", { class: "hide-n sub" }, str(e.qtype)), h("div", { class: "ans" }, answer(e)),
    h("div", { class: "hide-n num sub took" }, took(e.latency_us)));
  r.addEventListener("click", ev => { if (!ev.target.closest("button, a, input, label")) openQuery(e); });
  return r;
}

// openQuery is a query's panel: what was asked, by which device, of which node, the answer,
// and where to go from it: the site or the device alone here, the site on Blocking, the node.
function openQuery(e) {
  const site = str(e.qname).toLowerCase().replace(/\.$/, "");
  const p = panel(site || "A query");
  const kv = [];
  const pair = (k, ...v) => kv.push(h("dt", {}, k), h("dd", {}, ...v));
  pair("When", when(e), e.estimated ? " (estimated)" : "");
  pair("Device", typeof e.client === "string" ? [devName(e.client) || e.client, devName(e.client) ? h("span", { class: "sub mono" }, ` ${e.client}`) : null] : "Not kept");
  pair("Asked", h("span", { class: "mono" }, str(e.qname)), ` (${str(e.qtype) || "?"})`);
  pair("Answer", ...answer(e));
  pair("Took", took(e.latency_us));
  pair("Node", nodeLink({ key: e.node }));
  const go = (label, f) => h("button", { class: "btn small", type: "button", onclick: () => { p.close(); f(); } }, label);
  p.body.append(h("dl", { class: "kv ql-kv" }, kv),
    h("div", { class: "row" },
      site && !e.truncated ? go("Only this site", () => { st.q = site; $("qlq").value = site; changed(); }) : null,
      typeof e.client === "string" ? go("Only this device", () => setDevice(e.client)) : null,
      site && !e.truncated ? h("a", { class: "btn small", href: `blocklists.html#check=${encodeURIComponent(site)}` }, "Check it on Blocking") : null,
      e.node ? h("a", { class: "btn small", href: `node.html#${encodeURIComponent(str(e.node))}` }, "Open the node") : null));
  p.footer.append(h("button", { class: "btn", type: "button", onclick: p.close }, "Close"));
}

// ---- allowing a blocked site ---------------------------------------------------------------
// Allow adds the site to the always-allowed sites (the overrides' allow file) as a pending
// change: the header's chip has it until Apply sends it to the nodes.
async function allow(site, btn) {
  btn.disabled = true;
  try {
    const f = await getJSON(`api/changes/file?kind=source&name=${encodeURIComponent(ALLOWF)}`);
    if (f.partial) throw new Error("the always-allowed list is too long to add to here: add it on the Blocking page");
    const text = str(f.text);
    if (text.split("\n").some(l => l.trim().toLowerCase() === site)) { toast(`${site} is already always allowed`); return; }
    const next = (text && !text.endsWith("\n") ? `${text}\n` : text) + `${site}\n`;
    await post("api/changes", { kind: "source", name: ALLOWF, text: next, hash: str(f.hash), summary: `Always allow ${site}` });
    await refreshChanges();
    toast(`Always allow ${site}: waiting to apply`);
  } catch (e) {
    toast(`Couldn't allow ${site}: ${e.message}`);
    btn.disabled = false;
  }
}

// ---- the page ------------------------------------------------------------------------------

function empty(text, cls, action) {
  return h("div", { class: "empty" }, h("p", { class: cls || null }, text), action || null);
}

function renderRows() {
  const box = $("qlrows"), head = box.firstElementChild;
  let body;
  if (st.sess && !st.sess.logged_in) body = [empty("Log in to see the query log.")];
  else if (st.error && !st.loaded) body = [empty(`Couldn't read the query log: ${st.error}`, "bad")];
  else if (!st.loaded) body = [empty("Reading the query log…")];
  else if (st.rows.length) body = st.rows.map(row);
  else if (!st.held) body = [empty("No queries yet; the nodes' logs are read every few seconds.")];
  else if (st.show === "failed" && !st.q && !st.device && !st.node) body = [empty("No failed queries.")];
  else body = [empty("No queries match.", null, h("button", { class: "btn small", type: "button", onclick: clearAll }, "Show everything"))];
  box.replaceChildren(head, ...body);
  const span = st.oldest && st.newest ? `, from ${clock(st.oldest)} to ${clock(st.newest)}` : "";
  $("qlmore").textContent = st.loaded && st.matched > st.rows.length
    ? `Showing the newest ${fmt(st.rows.length)} of ${fmt(st.matched)} matches${span}. Narrow the filter to see older ones.` : "";
}

function renderBar() {
  for (const b of $("show").querySelectorAll("button")) {
    const on = b.dataset.show === st.show;
    b.classList.toggle("primary", on);
    b.setAttribute("aria-pressed", String(on));
  }
  const sel = $("qlnode");
  const opts = [h("option", { value: "" }, "Every node"), ...st.nodes.map(n => h("option", { value: n.key }, nodeWord(n.key)))];
  if (st.node && !st.nodes.some(n => n.key === st.node)) opts.push(h("option", { value: st.node }, st.node));
  sel.replaceChildren(...opts);
  sel.value = st.node;
  sel.hidden = st.nodes.length < 2 && !st.node;
  $("qlchips").replaceChildren(st.device
    ? h("button", { class: "chip", type: "button", "aria-label": `Device ${devShort(st.device)}: show every device`, title: "Show every device", onclick: () => setDevice("") },
      `Device: ${devShort(st.device)}`, h("span", { class: "cx", "aria-hidden": "true" }, "×"))
    : "");
  const live = $("qllive");
  if (st.error && st.loaded) { live.textContent = `Not updated: ${st.error}`; live.classList.add("bad"); }
  else {
    live.classList.remove("bad");
    live.textContent = st.paused ? "Paused" : !st.loaded ? "" : st.held ? `Live · the last ${fmt(Math.min(st.held, st.cap || st.held))} queries` : "Live";
  }
  $("pause").textContent = st.paused ? "Resume" : "Pause";
  $("pause").hidden = !st.loaded;
}

// What each node keeps about a device (its query log's client setting), and what's wrong
// with a node's log.
const KEEPS = { full: "each device's address", subnet: "each device's network only (/24)", hidden: "nothing about each device" };
const andList = xs => (xs.length < 2 ? xs : [...xs.slice(0, -1).flatMap((x, i) => (i ? [", ", x] : [x])), " and ", xs[xs.length - 1]]);
const nodeLink = n => h("a", { href: `node.html#${encodeURIComponent(n.key)}` }, nodeWord(n.key));

function renderNodes() {
  const on = st.nodes.filter(n => n.state === "ok" && n.enabled);
  const groups = new Map();
  for (const n of on) {
    const k = Object.hasOwn(KEEPS, n.client) ? n.client : "hidden";
    if (!groups.has(k)) groups.set(k, []);
    groups.get(k).push(n);
  }
  const parts = [];
  for (const [k, ns] of groups) {
    if (parts.length) parts.push(" · ");
    parts.push(...andList(ns.map(nodeLink)), ` ${ns.length === 1 ? "keeps" : "keep"} ${KEEPS[k]}`);
  }
  $("qlkeep").replaceChildren(...(parts.length ? [...parts, ". Set in each node's settings."] : []));
  const lines = [];
  for (const n of st.nodes) {
    const who = nodeLink(n);
    const page = text => h("a", { href: `node.html#${encodeURIComponent(n.key)}` }, text);
    if (n.state === "unsupported") lines.push([who, " keeps no query log: its firmware is from before it. ", page("Update it"), "."]);
    else if (n.state === "error") lines.push([who, `'s query log couldn't be read: ${str(n.error) || "no answer"}.`]);
    else if (n.state === "ok" && !n.enabled) lines.push([who, "'s query log is off: ", page("turn it on in its settings"), "."]);
    else if (n.state === "ok" && n.lost) lines.push([who, ` answered ${fmt(n.lost)} queries faster than the controller read them: those aren't here.`]);
  }
  $("qlwarn").replaceChildren(...lines.map(l => h("p", { class: "hint" }, ...l)));
}

function render() { renderBar(); renderNodes(); renderRows(); }

// ---- reading -------------------------------------------------------------------------------

// clientsOf is each node's client setting: one made stricter reloads the rows.
const clientsOf = v => arr(v.nodes).map(n => `${n.key}=${n.client}`).sort().join(" ");
function take(v) {
  st.held = v.held || 0; st.cap = v.cap || 0; st.oldest = v.oldest || null; st.newest = v.newest || null;
  st.nodes = arr(v.nodes);
  Object.assign(st.names, obj(v.names));
}

async function load() {
  if (!st.sess || !st.sess.logged_in) { render(); return; }
  const g = ++st.gen;
  try {
    const v = await getJSON(query(0));
    if (g !== st.gen) return; // the filters changed while it was read
    take(v);
    st.rows = arr(v.entries); st.last = v.last || 0; st.run = str(v.run); st.clients = clientsOf(v);
    st.matched = v.matched || 0; st.loaded = true; st.error = "";
  } catch (e) {
    if (g !== st.gen) return;
    st.error = e.message;
  }
  render();
}

async function follow() {
  if (st.paused || !st.loaded || !st.sess || !st.sess.logged_in) return;
  const g = st.gen;
  try {
    const v = await getJSON(query(st.last));
    if (g !== st.gen || st.paused) return;
    // The controller restarted (its IDs started over), or a node's client setting changed
    // (the rows shown may hold more than it allows now): read again.
    if (str(v.run) !== st.run || clientsOf(v) !== st.clients) { load(); return; }
    st.last = Math.max(st.last, v.last || 0);
    take(v);
    const fresh = arr(v.entries);
    st.matched = Math.min(st.matched + (v.matched || 0), st.held); // the oldest leave as new ones come
    if (fresh.length) {
      st.rows = fresh.concat(st.rows);
      st.rows.sort((a, b) => (new Date(b.at) - new Date(a.at)) || (b.id - a.id));
      st.rows = st.rows.slice(0, SHOW);
    }
    st.error = "";
  } catch (e) {
    if (g !== st.gen) return;
    st.error = e.message;
  }
  render();
}

function changed() { keep(); st.loaded = false; st.error = ""; render(); load(); tops(); }
function setDevice(a) { st.device = a; changed(); }
function clearAll() { st.q = ""; $("qlq").value = ""; st.show = "all"; st.node = ""; st.device = ""; changed(); }

let typing = null;
$("qlq").addEventListener("input", () => {
  clearTimeout(typing);
  typing = setTimeout(() => { st.q = $("qlq").value.trim(); changed(); }, 300);
});
$("filters").addEventListener("submit", e => { e.preventDefault(); clearTimeout(typing); st.q = $("qlq").value.trim(); changed(); });
$("show").addEventListener("click", e => {
  const b = e.target.closest("button[data-show]");
  if (!b || b.dataset.show === st.show) return;
  st.show = b.dataset.show;
  changed();
});
$("qlnode").addEventListener("change", e => { st.node = e.target.value; changed(); });
$("pause").addEventListener("click", () => {
  st.paused = !st.paused;
  renderBar();
  if (!st.paused) follow();
});

// ---- the sites and devices asked most ------------------------------------------------------

async function tops() {
  if (!st.sess || !st.sess.logged_in) return;
  let t;
  try { t = await getJSON(`api/querylog/top${st.node ? `?node=${encodeURIComponent(st.node)}` : ""}`); } catch { return; }
  Object.assign(st.names, obj(t.names));
  const line = (label, count, onclick, title) => h("div", { class: "row spread topr" },
    h("button", { class: "btn link", type: "button", onclick, title }, label), h("span", { class: "num small muted" }, fmt(count)));
  const sites = arr(t.queried).slice(0, 8), devs = arr(t.clients).slice(0, 8);
  $("topsites").replaceChildren(h("h3", {}, "Sites"), ...(sites.length
    ? sites.map(s => line(h("span", { class: "mono small" }, str(s.name)), s.count, () => { st.q = str(s.name); $("qlq").value = st.q; changed(); }, `Show only ${str(s.name)}`))
    : [h("p", { class: "hint" }, "Nothing yet.")]));
  const named = devs.some(d => devName(d.name));
  $("topdevices").replaceChildren(h("h3", {}, "Devices"), ...(devs.length
    ? devs.map(d => line(h("span", { class: "small" }, devName(d.name) ? [devShort(d.name), " ", h("span", { class: "mono muted" }, str(d.name))] : h("span", { class: "mono" }, str(d.name))),
      d.count, () => setDevice(str(d.name)), devName(d.name) || `Show only ${str(d.name)}`))
    : [h("p", { class: "hint" }, "No device addresses kept.")]),
  ...(named ? [h("p", { class: "hint" }, "Named from the records in your zones.")] : []));
  $("most").hidden = false;
  renderBar();
  renderRows(); // the names may be new
}

// ---- the last hour -------------------------------------------------------------------------

// chart is a small chart of a series (a value a minute, null for none), with what the
// minute under the pointer had.
function chart(title, value, series, say, d, hint) {
  const vs = arr(series).map(v => (typeof v === "number" && isFinite(v) ? v : null));
  const known = vs.filter(v => v !== null);
  const box = h("div", { class: "panel mini", title: hint || null }, h("span", { class: "small muted" }, title), h("span", { class: "v num" }, value));
  if (known.length < 2) { box.append(h("span", { class: "hint chartnone" }, "The chart fills in as the nodes are read.")); return box; }
  const max = Math.max(...known) || 1, w = 200, hgt = 50, n = vs.length;
  const x = i => (i / (n - 1)) * w, y = v => hgt - 4 - (v / max) * (hgt - 10);
  const pts = vs.map((v, i) => (v === null ? null : `${x(i).toFixed(1)},${y(v).toFixed(1)}`)).filter(Boolean);
  const mk = (tag, at) => { const e = document.createElementNS(SVG, tag); for (const [k, v] of Object.entries(at)) e.setAttribute(k, v); return e; };
  const svg = mk("svg", { class: "spark", viewBox: `0 0 ${w} ${hgt}`, preserveAspectRatio: "none", role: "img", "aria-label": `${title}: ${value}` });
  const first = pts[0].split(",")[0], last = pts[pts.length - 1].split(",")[0];
  const guide = mk("line", { class: "guide", x1: 0, x2: 0, y1: 0, y2: hgt, visibility: "hidden", "vector-effect": "non-scaling-stroke" });
  svg.append(mk("polygon", { points: `${first},${hgt} ${pts.join(" ")} ${last},${hgt}` }), mk("polyline", { points: pts.join(" "), "vector-effect": "non-scaling-stroke" }), guide);
  const read = h("span", { class: "hint readout" }, "60 min ago to now");
  const show = e => {
    const r = svg.getBoundingClientRect();
    const i = Math.max(0, Math.min(n - 1, Math.round(((e.clientX - r.left) / r.width) * (n - 1))));
    guide.setAttribute("x1", x(i)); guide.setAttribute("x2", x(i)); guide.setAttribute("visibility", "visible");
    const end = new Date(new Date(d.now).getTime() - (n - 1 - i) * (d.bin_s || 60) * 1000);
    read.textContent = `${end.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hour12: false })}: ${vs[i] === null ? "no figures" : say(vs[i])}`;
  };
  svg.addEventListener("pointermove", show);
  svg.addEventListener("pointerdown", show);
  svg.addEventListener("pointerleave", () => { guide.setAttribute("visibility", "hidden"); read.textContent = "60 min ago to now"; });
  box.append(svg, read);
  return box;
}

const qps = v => (typeof v === "number" ? v.toFixed(v < 10 ? 1 : 0) : "–");
const pct = v => (typeof v === "number" ? `${(v * 100).toFixed(v > 0 && v < 0.1 ? 1 : 0)}%` : "–");
const ms = v => (typeof v === "number" ? `${v < 10 ? v.toFixed(1) : Math.round(v)} ms` : "–");

async function readDash() {
  let d;
  try { d = await getJSON("api/dashboard"); } catch { return; }
  const f = obj(d.fleet), hour = obj(f.hour);
  if (!f.nodes) { $("hour").hidden = true; return; }
  $("minis").replaceChildren(
    chart("Queries a second", qps(hour.qps), f.qps, v => `${qps(v)} a second`, d),
    chart("Blocked", pct(hour.blocked_share), f.blocked_qps, v => `${qps(v)} blocked a second`, d),
    chart("Answer time", typeof hour.p50_ms === "number" ? `${ms(hour.p50_ms)} typical` : "–", f.p95_ms, v => `the slowest ${ms(v)}`, d,
      "The line: the slowest answers (95th percentile) each minute"),
    chart("Failed answers", typeof hour.servfail === "number" ? `${fmt(hour.servfail)} in the hour` : "–", f.servfail, v => `${fmt(v)} failed`, d));
  $("hourwhat").textContent = f.reading < f.nodes ? `${fmt(f.reading)} of ${fmt(f.nodes)} nodes send their figures` : "";
  $("hour").hidden = false;
}

render();
session.then(s => {
  st.sess = s;
  render();
  load();
  every(follow, EVERY);
  every(tops, 30000);
});
every(readDash, 10000);
