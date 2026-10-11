// Nodes, the home page (index.html): one
// sentence first (whether DNS is answering, or what needs attention), Copy DNS servers and
// + Add node at the top right, a firmware update offered as versions, the fleet's facts, one
// row per node (its LED, state and reason, its address to copy, its load, and its MAC to copy
// while it is on DHCP), the nodes found on the network and not added yet (with their MACs to
// copy, for a DHCP reservation or a static address), and the last hour's charts. A node
// opens its page (node.html, node.js); a found node its setup panel (addnode.js: what it
// needs next, a click away). A later build of the version a node runs is said so in its row,
// with no banner (internal/updates, Rebuild). A node's keys are said in its row when they
// aren't the controller's: "Needs keys" (setup mode), "Old keys (rotation pending)" (not the
// release key the controller signs with, or not the new keys of a rotation under way). Read
// from /api/nodes and /api/nodes/found every 5 s, /api/dashboard every 10 s, /api/updates and
// /api/key every 30 s.
import { shell, h, arr, obj, str, hostOnly, led, addr, mac, copyText, toast, refreshChanges, onChanges,
  getJSON, session, nodeText, answering, inService } from "./shell.js";
import { onNodes, every, releaseKey } from "./common.js";
import { addNode, blocker, usbButton } from "./addnode.js";
import { stateOf, upFor, linkWord, readBoards, mini, fmtNum, pct, andList } from "./nodeview.js";
import { openUpdate as updatePanel } from "./update.js";
import { setupCheck } from "./setupcheck.js";

shell("index.html");
setupCheck(document.getElementById("update"));

const $ = id => document.getElementById(id);
const data = { nodes: null, found: [], updates: null, dash: null, boards: new Map(), sess: {}, key: null };

// ---- a node in words --------------------------------------------------------------------

// boardName is a board's title from the catalog (/api/boards), else its name.
const boardName = b => data.boards.get(str(b)) || str(b);
const nodeKey = n => n.id || n.addr;

// updateOf is a node's firmware update in versions (/api/updates), or "": a later build of
// the version it runs said as that (its why), never "0.0.1 → 0.0.1".
const updateFor = n => arr(obj(data.updates).nodes).find(x => x.host === n.addr);
function updateOf(n) {
  const u = updateFor(n);
  if (!u) return "";
  const from = str(obj(u.runs).version), to = str(obj(u.newest).version);
  if (u.pending) return u.rebuild ? `A newer build of ${to} waiting to apply` : `Update waiting to apply: ${from} → ${to}`;
  if (u.state === "available") return u.rebuild ? str(u.why) : `Update available: ${from} → ${to}`;
  return "";
}

const listed = () => arr(data.nodes).filter(n => n.source === "settings");
// serving are the nodes in service: a node out of service is shown, muted, but isn't counted
// as up or down, nor given to the router.
const serving = () => arr(data.nodes).filter(inService);
const names = ns => ns.map(n => nodeText(n));
const both = ns => (ns.length === 2 ? "both nodes" : `all ${ns.length} nodes`);

// ---- the head: the sentence, Copy DNS servers, + Add node --------------------------------

function renderHead() {
  const ns = serving();
  const sentence = $("sentence"), sub = $("sentence-sub");
  const copy = $("copy-dns");
  const ips = ns.map(n => hostOnly(n.addr));
  copy.disabled = !ips.length;
  copy.title = ips.join(", ");
  if (data.nodes === null) return;
  const up = ns.filter(answering);
  const attention = ns.map(n => [n, stateOf(n)]).filter(([, s]) => s.tint);
  if (!ns.length && listed().length) {
    sentence.textContent = "No node in service";
    sub.textContent = "Every node is out of service: return one to service from its page.";
  } else if (!ns.length) {
    sentence.textContent = "No node yet";
    sub.textContent = "Add a node to start answering DNS on your network.";
  } else if (!up.length) {
    sentence.textContent = "DNS is not answering on your espDNS nodes";
    sub.textContent = `Check power and network cables on ${andList(names(ns))}.`;
  } else if (attention.length) {
    const [n, s] = attention[0];
    sentence.textContent = `DNS is answering · ${andList(attention.map(([x]) => nodeText(x)))} need${attention.length === 1 ? "s" : ""} attention`;
    sub.textContent = `${nodeText(n)}: ${s.why || s.word}. Clients still get answers from ${up.length === ns.length ? both(ns) : andList(names(up))}.`;
  } else if (ns.length === 1) {
    sentence.textContent = `DNS is answering on ${nodeText(ns[0])}`;
    sub.textContent = "With one node, DNS stops while it restarts: add a second node to keep it answering.";
  } else {
    sentence.textContent = `DNS is answering on ${both(ns)}`;
    sub.textContent = "Every node is healthy.";
  }
}

$("copy-dns").addEventListener("click", async () => {
  const ips = serving().map(n => hostOnly(n.addr));
  if (!ips.length) return;
  const t = ips.join(", ");
  toast(await copyText(t) ? `Copied ${t}: give ${ips.length === 2 ? "both" : ips.length === 1 ? "it" : "them all"} to your router as its DNS servers`
    : `Couldn't copy: your DNS servers are ${t}`);
});

const adder = addNode($("add-node"), {
  found: () => data.found,
  nodes: () => listed(),
  boardName,
  nodeKey,
  // What a USB install of a chip image gives: its exported image's version.
  imageVersion: image => str(obj(obj(data.updates).builds && arr(data.updates.builds).find(b => b.source === `images/${image}` && !b.error)).version),
  done: () => { readFound(); refreshChanges(); },
});

// ---- the update -------------------------------------------------------------------------

function renderUpdate() {
  const box = $("update");
  const rep = obj(data.updates);
  // A new version only: a later build of the one a node runs is said in its row.
  const offered = arr(rep.nodes).filter(u => u.state === "available" && u.listed && !u.out_of_service && !u.pending && !u.rebuild);
  if (!offered.length) { box.hidden = true; box.replaceChildren(); return; }
  const others = serving().filter(n => !offered.some(u => u.host === n.addr));
  const nm = u => nodeText(u.host);
  const b = h("button", { class: "btn primary", type: "button", onclick: () => openUpdate() }, offered.length === 1 ? `Update ${nm(offered[0])}` : "Update all");
  let line;
  if (offered.length === 1) {
    const u = offered[0];
    line = h("div", {}, h("b", {}, `Firmware ${str(obj(u.newest).version)} for ${nm(u)}`), " ",
      h("span", { class: "muted" }, `(now ${str(obj(u.runs).version)}). Applies with a restart${others.length ? `; ${andList(names(others))} keep${others.length === 1 ? "s" : ""} answering meanwhile` : ""}.`));
  } else {
    line = h("div", {}, h("b", {}, `New firmware for ${andList(offered.map(nm))}`), " ",
      h("span", { class: "muted" }, "Applies with a restart each, one node at a time; the others keep answering."));
  }
  box.className = "panel pad row spread accent";
  box.replaceChildren(line, b);
  box.hidden = false;
}

// The Update panel (update.js): adds the updates to the changes to apply. only, a host, is
// the node whose row opened it: only it is chosen; else the new versions are (a later build
// of the one a node runs is not).
function openUpdate(only) {
  updatePanel({ updates: data.updates, listed: serving(), canWrite: !!data.sess.logged_in, only, done: readUpdates });
}

// ---- the facts and the rows -------------------------------------------------------------


function renderFacts() {
  const ns = serving();
  const f = obj(obj(data.dash).fleet), now = obj(f.now), hour = obj(f.hour);
  const failed = typeof hour.servfail === "number" ? hour.servfail + (hour.dropped || 0) : null;
  const fact = (b, t) => h("span", {}, h("b", {}, b), ` ${t}`);
  $("facts").replaceChildren(
    fact(`${ns.filter(answering).length} of ${ns.length}`, `node${ns.length === 1 ? "" : "s"} answering`),
    fact(fmtNum(now.qps), "queries a second"),
    fact(failed === null ? "–" : failed.toLocaleString(), "failed answers in the last hour"),
    fact(pct(hour.blocked_share), "of queries blocked"));
}

// macLine is a node's MAC to copy, on its own line under its address; null for none.
function macLine(m) {
  const c = mac(m);
  return c ? h("div", { class: "sub" }, c) : null;
}

// keysOf is a node's keys in words when they aren't the controller's, or "": in setup mode
// (no keys, or damaged ones), or trusting another release key than the one the controller
// signs with (during a rotation: than the rotation's new keys).
function keysOf(n) {
  const s = obj(n.status), ks = arr(s.keys), src = str(s.keys_source);
  if (src === "none" || src === "corrupt") return "Needs keys (setup mode)";
  const k = obj(data.key), rot = obj(k.rotation);
  if (!n.online || typeof ks[0] !== "string" || !ks[0]) return "";
  if (rot.release) return ks[0] !== rot.release || (rot.recovery && ks[1] !== rot.recovery) ? "Old keys (rotation pending)" : "";
  return k.present && str(k.fingerprint) && ks[0] !== k.fingerprint ? "Old keys (rotation pending)" : "";
}

function nodeRow(n) {
  const s = obj(n.status), net = obj(s.net);
  const st = stateOf(n);
  const keys = keysOf(n);
  // Setup mode is said once: its health reason "no keys" is this.
  const stWhy = keys.startsWith("Needs") ? st.why.split(", ").filter(w => w !== "No keys").join(", ") : st.why;
  const why = [stWhy, keys, !st.tint && !st.out ? updateOf(n) : ""].filter(Boolean).join(" · ");
  // A later build of its version: no banner, so its row offers it.
  const u = updateFor(n);
  const rebuild = !st.tint && !st.out && u && u.rebuild && u.state === "available" && u.listed && !u.pending
    ? h("button", { class: "btn small link", type: "button", onclick: () => openUpdate(n.addr) }, "Update") : null;
  const href = `node.html#${encodeURIComponent(nodeKey(n))}`;
  const up = n.online ? upFor(s.uptime_s) : "";
  // Out of service: muted, its badge for its state.
  const word = st.out ? h("span", { class: "out-badge" }, st.word) : h("span", { class: "stw" }, st.word);
  return h("div", { class: `r${st.tint ? ` r-${st.tint}` : ""}${st.out ? " r-out" : ""}`, "data-href": href },
    h("div", {}, h("a", { class: "name", href }, nodeText(n)), h("div", { class: "sub" }, boardName(s.board) || "–")),
    h("div", { class: "full-n" }, h("span", { class: "state" }, led(st.led), word), why ? h("div", { class: "sub" }, why, rebuild ? " " : null, rebuild) : null),
    h("div", { class: "full-n" }, addr(hostOnly(n.addr)), h("div", { class: "sub" }, [linkWord(net.kind), up && `up ${up}`].filter(Boolean).join(" · ")),
      obj(s.config).address === "dhcp" ? macLine(n.mac) : null),
    h("div", { class: "hide-n num" }, n.online && typeof n.qps === "number" ? n.qps.toFixed(1) : "–", " ", h("span", { class: "sub" }, "q/s")),
    h("span", { class: "chev", "aria-hidden": "true" }, "›"));
}

// A node found on the network (mDNS, or looked up) and not added: a light-blue row. A click
// on it opens its setup panel; its button does the next step itself.
function foundRow(f) {
  const link = f.net === "ethernet" ? "Wired" : f.net === "wifi" ? "Wi-Fi" : "";
  const sub = !f.next ? blocker(f)
    : `Found ${f.source === "lookup" ? "at its address" : "on your network"}${link ? ` over ${link}` : ""}${f.version ? `, firmware ${f.version}` : ""}`;
  // The next step's button: Add in the narrow last column; Update over USB, wider, under why.
  const act = f.next ? h("button", { class: "btn small primary", type: "button", onclick: () => adder.open(f) }, "Add")
    : f.install === "usb" ? null : h("button", { class: "btn small", type: "button", onclick: () => adder.setup(f) }, "Set up");
  const usb = f.install === "usb" ? h("div", { class: "actions" }, usbButton(f, "btn small primary", boardName(f.board) || str(f.hostname))) : null;
  return h("div", { class: "r r-new", "data-found": "", onclick: e => { if (!e.target.closest("button, a, input, label")) adder.setup(f); } },
    h("div", {}, h("div", { class: "name" }, f.adopted && f.name ? str(f.name) : "New device found"),
      h("div", { class: "sub" }, [boardName(f.board), str(f.hostname)].filter(Boolean).join(" · "))),
    h("div", { class: "full-n" }, h("span", { class: "state" }, led("unknown"), h("span", { class: "stw" }, "Not added yet")), h("div", { class: "sub" }, sub), usb),
    h("div", { class: "full-n" }, addr(hostOnly(f.host)), macLine(f.mac)),
    h("div", { class: "hide-n" }, act),
    h("div", {}));
}

function renderRows() {
  const box = $("nodes");
  const head = box.firstElementChild;
  const ns = listed();
  const rows = [...ns.map(nodeRow), ...arr(data.found).map(foundRow)];
  if (data.nodes === null) rows.push(h("div", { class: "empty" }, h("p", {}, "Reading the nodes…")));
  else if (!rows.length) rows.push(h("div", { class: "empty with-mascot" },
    h("img", { class: "mascot", src: "img/artie.webp", width: 56, height: 96, alt: "", title: "Artie A. Record" }),
    h("p", {}, "No node yet. + Add node finds the ones on your network, or installs a new board over USB.")));
  head.hidden = !ns.length && !arr(data.found).length;
  box.replaceChildren(head, ...rows);
}

// A row opens its node; its buttons and links do their own thing.
$("nodes").addEventListener("click", e => {
  const r = e.target.closest(".r[data-href]");
  if (r && !e.target.closest("button, a, input, label")) location.href = r.dataset.href;
});

// ---- the last hour ----------------------------------------------------------------------

function renderHour() {
  const f = obj(obj(data.dash).fleet), hour = obj(f.hour);
  const sec = $("hour");
  if (!listed().length || !data.dash) { sec.hidden = true; return; }
  const ms = v => (typeof v === "number" ? `${Math.round(v)} ms typical` : "–");
  $("minis").replaceChildren(
    mini("Queries a second", fmtNum(hour.qps), f.qps),
    mini("Answer time", ms(hour.p50_ms), f.p95_ms, "The line: the slowest answers (95th percentile) each minute"),
    mini("Blocked", pct(hour.blocked_share), f.blocked_qps));
  sec.hidden = false;
}

function render() {
  renderHead();
  renderUpdate();
  renderFacts();
  renderRows();
  renderHour();
}

// ---- reading ----------------------------------------------------------------------------

async function readFound() {
  if (!data.sess.logged_in) return;
  try { data.found = arr(obj(await getJSON("api/nodes/found")).nodes); data.foundRead = true; } catch { /* kept */ }
  render();
  openFound();
}

// #found=<address or node ID> (a found node's page, or a search result) opens that found
// node's setup panel once the found nodes are read.
function openFound() {
  const m = location.hash.match(/^#found=(.+)$/);
  if (!m || !data.foundRead) return;
  let k = "";
  try { k = decodeURIComponent(m[1]).toLowerCase(); } catch { return; }
  const f = arr(data.found).find(x => hostOnly(x.host) === k || str(x.host) === k || str(x.id).toLowerCase() === k);
  history.replaceState(null, "", location.pathname + location.search);
  if (f) adder.setup(f);
  else toast("That node isn't found on the network now");
}
window.addEventListener("hashchange", openFound);
async function readKey() {
  data.key = await releaseKey();
  renderRows();
}
async function readUpdates() {
  try { data.updates = await getJSON("api/updates"); } catch { data.updates = null; }
  render();
}
async function readDash() {
  try { data.dash = await getJSON("api/dashboard"); } catch { data.dash = null; }
  renderFacts();
  renderHour();
}

onNodes(ns => { data.nodes = ns; render(); });
// A firmware change added or applied changes what is offered: read again then.
let changesKey = null;
onChanges(c => {
  const k = arr(c.changes).map(x => x.id).join(",");
  if (changesKey !== null && k !== changesKey) readUpdates();
  changesKey = k;
});
readBoards(data.boards).then(render);
session.then(s => {
  data.sess = s;
  every(readFound);
  every(readUpdates, 30000);
  every(readDash, 10000);
  every(readKey, 30000);
});
render();
