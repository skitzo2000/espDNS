// Nodes, the home page (index.html): one
// sentence first (whether DNS is answering, or what needs attention), Copy DNS servers and
// + Add node at the top right (and Update all while two or more nodes have an update), the
// fleet's facts, one row per node on one line (nodeRow), the nodes found on the network and
// not added yet, and the last hour's charts. A node
// opens its page (node.html, node.js); a found node its setup panel (addnode.js: what it
// needs next, a click away). A node's keys are said in its row when they
// aren't the controller's: "Needs keys" (setup mode), "Old keys (rotation pending)" (not the
// release key the controller signs with, or not the new keys of a rotation under way). Read
// from /api/nodes and /api/nodes/found every 5 s, /api/dashboard every 10 s, /api/updates and
// /api/key every 30 s.
import { shell, h, arr, obj, str, hostOnly, led, addr, copyText, toast, refreshChanges, onChanges,
  getJSON, session, nodeText, answering, inService } from "./shell.js";
import { onNodes, every, releaseKey } from "./common.js";
import { addNode, blocker, usbButton } from "./addnode.js";
import { stateOf, upFor, readBoards, mini, fmtNum, pct, andList } from "./nodeview.js";
import { openUpdate as updatePanel } from "./update.js";
import { setupCheck } from "./setupcheck.js";

shell("index.html");
setupCheck(document.querySelector("main > section"));

const $ = id => document.getElementById(id);
const data = { nodes: null, found: [], updates: null, dash: null, boards: new Map(), sess: {}, key: null };

// ---- a node in words --------------------------------------------------------------------

// boardName is a board's title from the catalog (/api/boards), else its name.
const boardName = b => data.boards.get(str(b)) || str(b);
const nodeKey = n => n.id || n.addr;

// offered are the updates offered (/api/updates): a listed node in service, nothing waiting.
const offered = () => arr(obj(data.updates).nodes).filter(u => u.state === "available" && u.listed && !u.out_of_service && !u.pending);

// updateOf is a node's firmware update (/api/updates), waiting to apply or offered: its
// entry and versions; null for none.
function updateOf(n) {
  const u = arr(obj(data.updates).nodes).find(x => x.host === n.addr);
  if (!u || !(u.pending || offered().includes(u))) return null;
  return { pending: !!u.pending, from: str(obj(u.runs).version), to: str(obj(u.newest).version) };
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
    sub.textContent = "Add a node to answer DNS on your network.";
  } else if (!up.length) {
    sentence.textContent = "DNS is not answering on your espDNS nodes";
    sub.textContent = `Check power and network cables on ${andList(names(ns))}.`;
  } else if (attention.length) {
    const [n, s] = attention[0];
    sentence.textContent = `DNS is answering · ${andList(attention.map(([x]) => nodeText(x)))} need${attention.length === 1 ? "s" : ""} attention`;
    sub.textContent = `${nodeText(n)}: ${s.why || s.word}. Devices still get answers from ${up.length === ns.length ? both(ns) : andList(names(up))}.`;
  } else if (ns.length === 1) {
    sentence.textContent = `DNS is answering on ${nodeText(ns[0])}`;
    sub.textContent = "With one node, DNS stops while it restarts. Add a second to keep it answering.";
  } else {
    sentence.textContent = `DNS is answering on ${both(ns)}`;
    sub.textContent = "Every node is healthy.";
  }
}

$("copy-dns").addEventListener("click", async () => {
  const ips = serving().map(n => hostOnly(n.addr));
  if (!ips.length) return;
  const t = ips.join(", ");
  toast(await copyText(t) ? `Copied ${t}: set ${ips.length === 2 ? "both" : ips.length === 1 ? "it" : "them all"} as your router's DNS servers`
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

// The Update panel (update.js): adds the updates to the changes to apply, the one node's
// (only) chosen, else every one offered. Update all, by + Add node, while two or more are.
function openUpdate(only) {
  updatePanel({ updates: data.updates, listed: serving(), canWrite: !!data.sess.logged_in, only, done: readUpdates });
}
$("update-all").addEventListener("click", () => openUpdate());

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

// A node's row, on one line: its name (its board on hovering it); its dot, green while it
// serves, amber for something to do, red when it doesn't work, with a flag beside one not
// green (a word, or an update's button) and the state and reason on hovering it; its link,
// address and copy button (its MAC on hovering the address); its uptime and its load.
function nodeRow(n) {
  const s = obj(n.status), net = obj(s.net);
  const st = stateOf(n);
  const keys = keysOf(n);
  // Setup mode is said once: its health reason "no keys" is this.
  const stWhy = keys.startsWith("Needs") ? st.why.split(", ").filter(w => w !== "No keys").join(", ") : st.why;
  const u = st.out ? null : updateOf(n);
  const v = u ? `${u.from} → ${u.to}` : "";
  const off = st.out ? "Out of service" : !n.online ? "Offline" : "";
  const flag = off || [st.tint || st.led !== "healthy" ? stWhy || st.word : "", keys, u && u.pending ? `Update waiting to apply: ${v}` : ""].filter(Boolean).join(" · ");
  const dot = off || st.tint === "bad" ? "fault" : st.tint === "warn" ? "degraded" : u || keys || st.led === "unknown" ? "todo" : st.led;
  const tip = [st.word, stWhy, keys, u && `Update ${u.pending ? "waiting to apply" : "available"}: ${v}`].filter(Boolean).join(" · ");
  const btn = u && !u.pending ? h("button", { class: "btn small upd", type: "button", "aria-label": `Update ${nodeText(n)} from ${u.from} to ${u.to}`,
    onclick: e => { e.stopPropagation(); openUpdate(n.addr); } }, `Update ${v}`) : null;
  const href = `node.html#${encodeURIComponent(nodeKey(n))}`;
  return h("div", { class: `r${st.tint ? ` r-${st.tint}` : ""}${st.out ? " r-out" : ""}`, "data-href": href },
    h("div", {}, h("a", { class: "name", href, title: boardName(s.board) || null }, nodeText(n))),
    h("div", { class: "full-n" }, h("span", { class: "state" }, led(dot, tip), flag ? h("span", { class: "flag", title: flag }, flag) : null, btn)),
    h("div", { class: "full-n" }, addr(hostOnly(n.addr), net.kind, n.mac)),
    h("div", { class: "hide-n num" }, n.online ? upFor(s.uptime_s) || "–" : "–"),
    h("div", { class: "hide-n num" }, n.online && typeof n.qps === "number" ? n.qps.toFixed(1) : "–"),
    h("span", { class: "chev", "aria-hidden": "true" }, "›"));
}

// A node found on the network (mDNS, or looked up) and not added: a light-blue row, its dot
// amber. A click on it opens its setup panel; its button does the next step itself.
function foundRow(f) {
  const link = f.net === "ethernet" ? "Wired" : f.net === "wifi" ? "Wi-Fi" : "";
  const flag = f.next ? "Not added yet" : blocker(f);
  const tip = !f.next ? flag
    : `Found ${f.source === "lookup" ? "at its address" : "on your network"}${link ? ` over ${link}` : ""}${f.version ? `, firmware ${f.version}` : ""}`;
  // The next step's button: Add in the Load column; Update over USB beside its flag.
  const act = f.next ? h("button", { class: "btn small primary", type: "button", onclick: () => adder.open(f) }, "Add")
    : f.install === "usb" ? null : h("button", { class: "btn small", type: "button", onclick: () => adder.setup(f) }, "Set up");
  const usb = f.install === "usb" ? usbButton(f, "btn small primary", boardName(f.board) || str(f.hostname)) : null;
  return h("div", { class: "r r-new", "data-found": "", onclick: e => { if (!e.target.closest("button, a, input, label")) adder.setup(f); } },
    h("div", {}, h("span", { class: "name", title: [boardName(f.board), str(f.hostname)].filter(Boolean).join(" · ") || null },
      f.adopted && f.name ? str(f.name) : "New device found")),
    h("div", { class: "full-n" }, h("span", { class: "state" }, led("todo", tip), h("span", { class: "flag", title: tip }, flag), usb)),
    h("div", { class: "full-n" }, addr(hostOnly(f.host), f.net, f.mac)),
    h("div", { class: "hide-n" }),
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
    h("p", {}, "No node yet. + Add node finds nodes on your network, or installs a new board over USB.")));
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
  $("update-all").hidden = offered().length < 2;
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
  else toast("That node isn't on the network now");
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
