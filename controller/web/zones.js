// Zones (zones.html): every zone the
// fleet answers for, in one place, whatever its source: hosted here, copied from your
// primary, or forwarded (/api/zone-inventory). All nodes serve the same zones, so a zone
// shows the fleet's state, never a node's. The zone primary is the first row: its address
// and the zones copied from it (primary.js).
// + Add zone (addzone.js; the first run mounts it too) is a box: a zone typed is looked up
// (/api/zone-lookup) and added the way it lives: hosted here, copied from your primary, or
// forwarded. A zone opens its page
// (zones.html#zone=<name>): a hosted zone's records edited with record forms (the file under
// Advanced, with its earlier versions), a copied zone's records read-only
// (/api/zone-records), a forwarded zone's server. Every edit waits as a pending change
// (POST /api/changes: a hosted zone's file, or the node configs' secondary and forward
// zones) until Apply in the header; a zone's waiting changes can be undone here.
// Everything from a node, a config or the controller is put in as text.
import { shell, h, arr, obj, str, hostOnly, addr, copyText, toast, panel, refreshChanges, onChanges, getJSON, post, session } from "./shell.js";
import { onNodes, every } from "./common.js";
import { primaryFields } from "./primary.js";
import { addZone, zoneEditor, readConfigs as readCfgs, primaryIP as primaryIPOf, IPV4, canon, fileOf, said } from "./addzone.js";

shell("zones.html");

const view = document.getElementById("view");
const enc = encodeURIComponent;
const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;
const reEsc = s => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

const data = {
  inv: null, invErr: null,     // /api/zone-inventory
  changes: [], changesRead: false,
  cfgs: null, cfgErr: null,    // the node configs as the pending changes make them
  nodes: [],                   // /api/nodes
  canWrite: false,
};
session.then(s => { data.canWrite = !!s.logged_in; render(); });

// ---- reading ----------------------------------------------------------------------------

async function readInventory() {
  try { data.inv = await getJSON("api/zone-inventory"); data.invErr = null; } catch (e) { data.invErr = said(e.message); }
  renderSoft();
}

// readConfigs reads every node config as the pending changes make it (addzone.js): the
// secondary and forward zones an apply would send, and the version a new change edits.
async function readConfigs() {
  try {
    data.cfgs = await readCfgs();
    data.cfgErr = null;
  } catch (e) { data.cfgErr = said(e.message); }
}

let changesKey = null;
onChanges(p => {
  data.changes = arr(p.changes);
  data.changesRead = true;
  const key = data.changes.map(c => c.id).join(",");
  if (key !== changesKey) {
    changesKey = key;
    readConfigs().then(() => { readInventory(); });
  }
});
onNodes(ns => { data.nodes = arr(ns); renderCopy(); });
every(readInventory, 10000);

// ---- the zones, with what waits to apply --------------------------------------------------

// changesOf are the pending changes of zone name: its file's, and the configs' that name it.
function changesOf(name) {
  const re = new RegExp(`(^|\\s)zone ${reEsc(name)}(:|\\s|$)`, "i");
  return data.changes.filter(c => (c.kind === "zone" && c.name === fileOf(name)) || (c.kind === "config" && re.test(str(c.summary))));
}

// effective is the secondary and forward zones the configs hold as the pending changes make
// them (null until read).
function effective() {
  if (!data.cfgs) return null;
  const sec = new Map(), fwd = new Map();
  for (const c of data.cfgs) {
    const s = obj(obj(c.cfg).secondary);
    for (const z of arr(s.zones)) sec.set(canon(z), str(s.primary) || sec.get(canon(z)) || "");
    for (const f of arr(obj(c.cfg).forward_zones)) fwd.set(canon(obj(f).zone), str(obj(f).forwarder));
  }
  return { sec, fwd };
}

// rows are every zone: the inventory's, each with what waits for it ("add", "edit",
// "remove"), and the zones only the pending changes add.
function rows() {
  const inv = obj(data.inv), eff = effective();
  const out = [];
  const seen = new Set();
  for (const z of arr(inv.zones)) {
    const r = { name: str(z.name), kind: str(z.kind), primary: str(z.primary), forwarders: arr(z.forwarders).map(str), records: z.records || 0,
      serial: z.serial || 0, state: str(z.state), text: str(z.text), configs: arr(z.configs).map(str), pending: null };
    seen.add(`${r.kind} ${r.name}`);
    if (r.kind === "hosted") {
      const zc = data.changes.filter(c => c.kind === "zone" && c.name === fileOf(r.name));
      if (zc.length) r.pending = zc.at(-1).delete ? "remove" : "edit";
    } else if (eff && r.configs.length) {
      const m = r.kind === "secondary" ? eff.sec : eff.fwd;
      if (!m.has(r.name)) r.pending = "remove";
      else if (r.kind === "forward" && m.get(r.name) && !r.forwarders.includes(m.get(r.name))) { r.pending = "edit"; r.to = m.get(r.name); }
    }
    out.push(r);
  }
  const add = (name, kind, extra) => {
    if (seen.has(`${kind} ${name}`)) return;
    seen.add(`${kind} ${name}`);
    out.push({ name, kind, primary: "", forwarders: [], records: 0, serial: 0, state: "", text: "", configs: [], pending: "add", ...extra });
  };
  for (const c of data.changes) {
    if (c.kind === "zone" && !c.delete && str(c.name).endsWith(".zone")) add(str(c.name).slice(0, -5), "hosted", {});
  }
  if (eff) {
    for (const [name, p] of eff.sec) add(name, "secondary", { primary: p });
    for (const [name, f] of eff.fwd) add(name, "forward", { forwarders: [f] });
  }
  return out.sort((a, b) => a.name.localeCompare(b.name) || a.kind.localeCompare(b.kind));
}

// The zone primary's address, as the page names it: settings.json's, else the one the
// configs copy from.
function primaryAddr() {
  const p = obj(obj(data.inv).primary);
  return str(p.address) || arr(obj(data.inv).primaries)[0] || "";
}

// primaryIP is the zone primary's IPv4 address, which a node config needs: settings.json's,
// else the address the configs copy from (one only).
function primaryIP() { return primaryIPOf(data.inv); }

// yours says whether a copied zone's primary is the zone primary: its address, or the
// only primary the configs copy from.
const yours = p => !p || p === primaryAddr() || p === primaryIP() || arr(obj(data.inv).primaries).length <= 1;

function comesFrom(z) {
  if (z.kind === "secondary") return yours(z.primary) ? ["Copied from your primary"] : ["Copied from ", h("span", { class: "mono" }, z.primary)];
  if (z.kind === "forward") {
    const to = z.to || z.forwarders.join(", ");
    return to ? ["Forwarded to ", h("span", { class: "mono" }, to)] : ["Forwarded"];
  }
  return ["Hosted here"];
}

// stateOf is a zone's state in words, as the fleet has it: a class for its dot, and the
// row's tint ("" normal, warn, bad).
function stateOf(z) {
  if (z.pending === "add") return { word: "Waiting to apply", cls: "off", tint: "" };
  if (z.pending === "remove") return { word: "Removed · waiting to apply", cls: "off", tint: "" };
  if (z.pending === "edit") return { word: "Edited · waiting to apply", cls: "off", tint: "" };
  switch (z.state) {
    case "ok": return { word: z.kind === "forward" ? "Forwarding" : "Up to date", cls: "", tint: "" };
    case "partial": return { word: "Not on every node yet", cls: "off", tint: "" };
    case "waiting": return { word: z.kind === "secondary" ? "Not copied yet" : "On no node yet", cls: "warn", tint: "warn" };
    case "expired": return { word: "Can't copy it", cls: "warn", tint: "warn" };
    case "error": return { word: "The file has errors", cls: "bad", tint: "bad" };
    case "no_file": return { word: "No file here", cls: "warn", tint: "warn" };
    default: return { word: "Not read yet", cls: "off", tint: "" };
  }
}
const stateMark = s => h("span", { class: `state${s.cls ? ` ${s.cls}` : ""}` }, h("span", { class: "dot" }), h("span", { class: "stw" }, s.word));

function updatedWords(z) {
  if (z.pending === "add") return "after apply";
  if (z.kind === "hosted") return "edited here";
  if (z.kind === "secondary") return z.serial ? `version ${z.serial}` : "not copied yet";
  return "–";
}

// ---- the page ---------------------------------------------------------------------------

const route = () => {
  const m = location.hash.match(/^#zone=([^&]*)/);
  return m ? { zone: canon(decodeURIComponent(m[1])) } : { zone: null };
};
let shown = null; // the zone whose page is drawn, or "" for the list
window.addEventListener("hashchange", () => render());

function render() {
  const r = route();
  if (r.zone) zonePage(r.zone);
  else listPage();
}

// renderSoft draws what changed without throwing away what is being typed or read.
function renderSoft() {
  const r = route();
  if (!r.zone) {
    if (shown !== "") { listPage(); return; }
    renderList();
    return;
  }
  if (shown !== r.zone) { zonePage(r.zone); return; }
  refreshZoneHead();
}

// ---- the list ---------------------------------------------------------------------------

let copyBtn = null;

function listPage() {
  shown = "";
  document.title = "espDNS zones";
  copyBtn = h("button", { class: "btn", type: "button", id: "copy-dns", onclick: copyDNS }, "Copy DNS servers");
  adder.mount(addBox);
  view.replaceChildren(
    h("div", { class: "head" },
      h("div", { class: "lead" }, h("h1", {}, "Zones"), h("p", { class: "muted" }, "The domains your nodes answer themselves instead of asking the internet.")),
      h("div", { class: "row" }, copyBtn, addBox)),
    h("div", { id: "zprimary" }),
    h("section", {}, h("div", { class: "facts", id: "facts" }),
      h("div", { class: "panel rows zones", id: "zones" })),
    h("p", { class: "hint" }, "Copied zones are edited on your primary and picked up within seconds. Hosted zones are edited here."));
  primaryRow(document.getElementById("zprimary"));
  renderCopy();
  renderList();
}

const listedNodes = () => data.nodes.filter(n => n.source === "settings");

function renderCopy() {
  if (!copyBtn || !copyBtn.isConnected) return;
  const ips = listedNodes().map(n => hostOnly(n.addr));
  copyBtn.disabled = !ips.length;
  copyBtn.title = ips.join(", ");
}

async function copyDNS() {
  const ips = listedNodes().map(n => hostOnly(n.addr));
  if (!ips.length) return;
  const t = ips.join(", ");
  toast(await copyText(t) ? `Copied ${t}: set ${ips.length === 1 ? "it" : "them"} as your router's DNS servers`
    : `Couldn't copy: your DNS servers are ${t}`);
}

function renderList() {
  const box = document.getElementById("zones"), facts = document.getElementById("facts");
  if (!box) return;
  if (primarySay) primarySay();
  const head = h("div", { class: "r headr" }, h("div", {}, "Zone"), h("div", {}, "Comes from"), h("div", {}, "Records"), h("div", {}, "Updated"), h("div", {}, "State"), h("div", {}));
  if (!data.inv) {
    box.replaceChildren(h("div", { class: "empty" }, h("p", { class: data.invErr ? "bad" : "" }, data.invErr ? `Can't read the zones: ${data.invErr}` : "Reading the zones…")));
    facts.replaceChildren();
    return;
  }
  const rs = rows();
  const now = rs.filter(z => z.pending !== "add");
  const n = k => now.filter(z => z.kind === k).length;
  facts.replaceChildren(
    h("span", {}, h("b", {}, String(n("secondary"))), " copied from your primary"),
    h("span", {}, h("b", {}, String(n("forward"))), " forwarded"),
    h("span", {}, h("b", {}, String(n("hosted"))), " hosted here"),
    h("span", {}, h("b", {}, String(obj(data.inv.counts).records || 0)), " records"));
  if (data.invErr) facts.append(h("span", { class: "bad" }, "The controller isn't answering, so this may be out of date."));
  if (!rs.length) {
    box.replaceChildren(h("div", { class: "empty" }, h("p", {}, "No zones yet: your nodes ask the internet for every name."),
      h("p", {}, "+ Add zone copies one from your primary, hosts one here or forwards one to another server.")));
    return;
  }
  box.replaceChildren(head, ...rs.map(z => {
    const s = stateOf(z);
    const href = `#zone=${enc(z.name)}`;
    return h("div", { class: `r${s.tint ? ` r-${s.tint}` : ""}`, "data-href": href, "data-zone": z.name },
      h("a", { class: "name", href }, z.name),
      h("div", { class: "sub" }, comesFrom(z)),
      h("div", { class: "hide-n num" }, z.kind === "forward" || !z.records ? "–" : String(z.records), z.kind !== "forward" && z.records ? h("span", { class: "sub" }, " records") : null),
      h("div", { class: "hide-n sub" }, updatedWords(z)),
      h("div", {}, stateMark(s)),
      h("span", { class: "chev", "aria-hidden": "true" }, "›"));
  }));
}

view.addEventListener("click", e => {
  const r = e.target.closest(".zones .r[data-href]");
  if (r && !e.target.closest("button, a, input, label")) location.hash = r.dataset.href;
});

// ---- the zone primary -------------------------------------------------------------------

// primaryRow is the zone primary, closed: its address; open: the
// form (settings.json's "primary", saved at once: it is the controller's, not a node's).
// The zones listed are those to copy from it: a zone added there is copied by every node
// (a change to apply), one removed is no longer copied.
let primarySay = null;
function primaryRow(box) {
  const val = h("span", { class: "val" });
  const say = () => {
    const a = primaryAddr();
    val.textContent = !data.inv ? "" : a || "Not set up";
  };
  primarySay = say;
  say();
  const form = h("div", { class: "stack" }, h("p", { class: "hint" }, "Reading…"));
  const d = h("details", { class: "ex", id: "primary-row" },
    h("summary", {}, h("span", {}, h("b", {}, "Zone primary"), h("span", { class: "muted small" }, " · where copied zones come from")), val),
    h("div", { class: "exb" }, form));
  let read = false;
  d.addEventListener("toggle", () => { if (d.open && !read) { read = true; primaryForm(form, say); } });
  box.replaceChildren(d);
}

// copiedFrom are the zones the node configs (as the pending changes make them) copy from
// the primary at a.
function copiedFrom(a) {
  return rows().filter(z => z.kind === "secondary" && z.pending !== "remove" && (!z.primary || z.primary === a)).map(z => z.name);
}

async function primaryForm(body, say) {
  let st;
  try { st = await getJSON("api/settings"); await readConfigs(); } catch (e) { body.replaceChildren(h("p", { class: "hint bad" }, `Can't read the settings: ${said(e.message)}`)); return; }
  const cur = obj(obj(st.settings).primary);
  const before = [...new Set([...arr(cur.zones).map(str), ...copiedFrom(str(cur.address) || primaryAddr())])];
  const f = primaryFields(cur, before);
  const msg = h("span", { class: "hint", role: "status" });
  const save = h("button", { class: "btn primary", type: "button", id: "psave", disabled: !data.canWrite }, "Save");
  save.addEventListener("click", async () => {
    msg.className = "hint bad";
    msg.textContent = "";
    const v = f.value();
    if (v.err) { msg.textContent = v.err; if (v.focus) v.focus.focus(); return; }
    save.disabled = true;
    msg.className = "hint";
    msg.textContent = "Saving…";
    try {
      st = await post("api/settings", { settings: { ...obj(st.settings), primary: v.primary }, version: str(st.version) });
      // The zones to copy: every node copies one added, and stops copying one removed.
      const now = arr(obj(v.primary).zones), a = str(obj(v.primary).address);
      let changed = 0;
      for (const z of now.filter(z => !before.includes(z))) {
        if (arr(data.cfgs).length) { await editor.add(z, "copy", a); changed++; }
      }
      for (const z of before.filter(z => !now.includes(z))) {
        if (editor.holding(z, "secondary").length) { await editor.removeConfigZone(z, "secondary"); changed++; }
      }
      before.splice(0, before.length, ...now);
      await readInventory();
      say();
      msg.textContent = changed ? "Saved. The zones' changes wait in \"changes to apply\"." : "Saved.";
      toast("Zone primary saved");
    } catch (e) { msg.className = "hint bad"; msg.textContent = said(e.message); }
    save.disabled = !data.canWrite;
  });
  body.replaceChildren(f.el, h("div", { class: "row" }, save, msg));
}

// ---- + Add zone, and changing zones -----------------------------------------------------

// after reads what the changes made, and draws it.
async function after() {
  await refreshChanges();
  await readConfigs();
  await readInventory();
}

// editor changes the zones through pending changes (addzone.js): a zone added to or taken
// out of every listed node's config, on the configs as the pending changes make them.
const editor = zoneEditor({ cfgs: () => data.cfgs, inv: () => data.inv, readConfigs, after });

// adder is + Add zone (addzone.js), in the list's head: the button becomes a box; a zone
// typed is looked up where it lives, with what to do about it right under it. A zone hosted
// here opens its page.
const addBox = h("div", { id: "add-zone" });
const adder = addZone(addBox, {
  editor,
  canWrite: () => data.canWrite,
  primaryIP,
  added: (name, kind) => { if (kind === "host") location.hash = `#zone=${enc(name)}`; },
});

async function setForwarder(name, ip) {
  await readConfigs();
  return editor.editConfigs(editor.holding(name, "forward"), `Edit zone ${name}: forward to ${ip}`, cfg => {
    let changed = false;
    for (const z of cfg.forward_zones) if (canon(z.zone) === name && z.forwarder !== ip) { z.forwarder = ip; changed = true; }
    return changed;
  });
}

// alsoUndone are the other pending changes undoing zone name takes: those made after one
// of its changes to the same file (a node's settings edited after the zone was added), which
// the controller discards with it.
function alsoUndone(name) {
  const mine = changesOf(name), ids = new Set(mine.map(c => c.id));
  const out = [];
  for (const c of mine) {
    const i = data.changes.indexOf(c);
    for (const x of data.changes.slice(i + 1)) {
      if (!ids.has(x.id) && x.kind === c.kind && x.name === c.name && !out.includes(x)) out.push(x);
    }
  }
  return out;
}

// undoAsk undoes zone name's changes, first asking in a panel when other changes go with
// them; then calls done. It rejects only when the undo fails.
function undoAsk(name, done) {
  const more = alsoUndone(name);
  if (!more.length) return undo(name).then(done);
  return new Promise((resolve, reject) => {
    const p = panel(`Undo the changes to ${name}?`, () => resolve());
    const yes = h("button", { class: "btn danger", type: "button", id: "confirm-undo" }, "Undo all of them");
    const err = h("p", { class: "hint bad", role: "status" });
    yes.addEventListener("click", async () => {
      yes.disabled = true;
      try { await undo(name); p.close(); done(); } catch (e) { err.textContent = said(e.message); yes.disabled = false; reject(e); }
    });
    p.body.append(h("p", {}, `Later changes to the same node configs go with them:`),
      h("ul", { class: "lines" }, more.map(x => h("li", {}, str(x.summary)))),
      h("p", { class: "hint" }, "To keep them, Cancel and change the zone back instead."), err);
    p.footer.append(h("button", { class: "btn", type: "button", onclick: p.close }, "Cancel"), yes);
  });
}

// undo discards the pending changes of zone name (the ones stacked on them go too).
async function undo(name) {
  for (const c of changesOf(name)) {
    try { await post(`api/changes/${enc(c.id)}/discard`); } catch (e) { if (!/not found|no change/i.test(e.message)) throw e; }
  }
  await after();
}

// ---- a zone's page ----------------------------------------------------------------------

let zoneCtx = null; // {name, head, pend, problem, body}

function zonePage(name) {
  shown = name;
  document.title = `${name} · espDNS zones`;
  zoneCtx = { name, head: h("div", { class: "head" }), pend: h("div", {}), problem: h("div", {}), body: h("div", { class: "zbody" }), kind: null, key: "" };
  view.replaceChildren(h("a", { class: "crumb", href: "zones.html" }, "‹ Zones"), zoneCtx.head, zoneCtx.pend, zoneCtx.problem, zoneCtx.body);
  refreshZoneHead();
  view.focus({ preventScroll: true });
  window.scrollTo(0, 0);
}

const zoneRow = name => rows().find(z => z.name === name && (zoneCtx.kind ? z.kind === zoneCtx.kind : true)) || rows().find(z => z.name === name);

function refreshZoneHead() {
  const c = zoneCtx;
  if (!c) return;
  const z = zoneRow(c.name);
  if (!z) {
    c.head.replaceChildren(h("div", { class: "lead" }, h("h1", { class: "mono" }, c.name),
      h("p", { class: !data.inv && data.invErr ? "bad" : "muted" }, data.inv ? "This isn't one of your zones." : data.invErr ? `Can't read the zones: ${data.invErr}` : "Reading the zone…")));
    c.pend.replaceChildren(); c.problem.replaceChildren(); c.body.replaceChildren();
    return;
  }
  const first = c.kind === null;
  c.kind = z.kind;
  const facts = [];
  if (z.kind === "secondary") facts.push(!z.primary ? "Copied from your primary" : yours(z.primary) ? `Copied from your primary (${z.primary})` : `Copied from ${z.primary}`);
  else if (z.kind === "forward") facts.push(`Forwarded to ${z.to || z.forwarders.join(", ") || "–"}`);
  else facts.push("Hosted here");
  if (z.kind !== "forward" && z.records) facts.push(plural(z.records, "record", "records"));
  if (z.kind !== "forward" && z.serial) facts.push(`version ${z.serial}`);
  let action = null;
  if (z.kind === "hosted" && z.pending !== "remove") action = h("button", { class: "btn", type: "button", id: "add-record", disabled: !data.canWrite, onclick: () => recordPanel(null) }, "+ Add record");
  c.head.replaceChildren(h("div", { class: "lead" }, h("h1", { class: "mono" }, z.name), h("p", { class: "muted" }, facts.join(" · "))), action || "");

  // What waits to apply, with Undo.
  const cs = changesOf(z.name);
  if (cs.length) {
    const b = h("button", { class: "btn small", type: "button", id: "undo", disabled: !data.canWrite }, z.pending === "add" ? "Don't add it" : "Undo");
    const msg = h("span", { class: "hint bad", role: "status" });
    b.addEventListener("click", async () => {
      b.disabled = true;
      msg.textContent = "";
      try {
        await undoAsk(z.name, () => {
          toast(z.pending === "add" ? `Zone ${z.name} won't be added` : `The changes to ${z.name} are undone`);
          if (z.pending === "add") location.hash = "";
          else { c.key = ""; refreshZoneHead(); }
        });
      } catch (e) { msg.textContent = said(e.message); }
      if (b.isConnected) b.disabled = !data.canWrite;
    });
    c.pend.replaceChildren(h("div", { class: "panel pad stack accent", id: "pending" },
      h("b", {}, z.pending === "add" ? "Waiting to apply: this zone is added with the next Apply." : z.pending === "remove" ? "Waiting to apply: this zone is removed with the next Apply." : "Waiting to apply"),
      h("ul", { class: "lines" }, cs.map(x => h("li", {}, str(x.summary), x.effect ? h("span", { class: "hint" }, ` · ${str(x.effect)}`) : null))),
      h("div", { class: "row" }, b, h("span", { class: "hint" }, "Apply them from \"changes to apply\" at the top."), msg)));
  } else c.pend.replaceChildren();

  problemBox(z, c.problem);

  // The body is drawn again only when what it shows changed.
  const key = JSON.stringify([z.kind, z.pending, z.serial, z.records, z.to, z.forwarders, cs.map(x => x.id)]);
  if (first || key !== c.key) {
    c.key = key;
    if (z.kind === "hosted") hostedBody(z);
    else if (z.kind === "secondary") secondaryBody(z);
    else forwardBody(z);
  }
}

// problemBox says what is wrong with a zone, with the fix: the same for every node.
function problemBox(z, box) {
  // failing are the nodes whose copy of a copied zone expired or whose last copies failed
  // (their /status): the primary most often refused the transfer.
  const failing = z.kind !== "secondary" ? [] : listedNodes().filter(n => arr(obj(n.status).zones).map(obj)
    .some(c => canon(c.name) === z.name && (c.expired || c.fails > 0))).map(n => hostOnly(n.addr));
  if (z.pending === "add" || (!["expired", "waiting", "error", "no_file"].includes(z.state) && !failing.length)) { box.replaceChildren(); return; }
  const key = `${z.state} ${failing}`;
  if (box.dataset.state === key && box.firstChild) return;
  box.dataset.state = key;
  const alert = (...k) => h("div", { class: "alert-box", id: "problem" }, ...k);
  if (z.state === "error") { box.replaceChildren(alert(h("b", {}, "The zone file doesn't pass its checks."), h("p", {}, z.text), h("p", { class: "hint" }, "Fix the file under Advanced. The nodes keep the version they have."))); return; }
  if (z.state === "no_file") { box.replaceChildren(alert(h("b", {}, "The nodes serve this zone, but there's no file for it here."), h("p", { class: "hint" }, "The next change to the hosted zones takes it off the nodes unless you add a file first."))); return; }
  if (z.kind !== "secondary") { box.replaceChildren(alert(h("b", {}, "No node serves this zone yet."), h("p", { class: "hint" }, "It goes to the nodes with the next Apply."))); return; }
  // A copied zone the nodes can't copy: usually the primary refuses them the transfer.
  const who = failing.join(", ");
  box.replaceChildren(alert(h("b", {}, failing.length ? `Copying this zone from your primary is failing on ${who}.`
    : z.state === "expired" ? "Your nodes can't copy this zone from your primary." : "Your nodes haven't copied this zone yet."),
    h("p", {}, "They keep answering from the copy they have, if any."),
    h("p", { class: "hint" }, failing.length
      ? `Your primary most likely refused the transfer: on your primary, allow zone transfers (AXFR) of ${z.name} from ${who}, and check that it serves the zone.`
      : `On your primary, allow zone transfers (AXFR) of ${z.name} to your nodes' addresses, and check that it serves the zone.`)));
}

const section = (title, hint, ...kids) => h("section", {}, h("div", { class: "sec-head" }, h("h2", {}, title), hint ? h("span", { class: "hint" }, hint) : null), ...kids);
const recTable = (recs, actions) => h("div", { class: "panel scroll" }, h("table", { class: "recs" },
  h("thead", {}, h("tr", {}, h("th", {}, "Name"), h("th", {}, "Type"), h("th", {}, "Value"), h("th", { class: "hide-n" }, "TTL"), actions ? h("th", {}, h("span", { class: "offscreen" }, "Actions")) : null)),
  h("tbody", {}, recs.map((r, i) => h("tr", {}, h("td", { class: "mono" }, rel(r.owner, zoneCtx.name)), h("td", {}, str(r.type)), h("td", { class: "mono v" }, str(r.data)),
    h("td", { class: "hide-n num" }, String(r.ttl ?? "")), actions ? h("td", { class: "acts" }, actions(r, i)) : null)))));

// rel is a record's owner under the zone: @ for the zone itself.
function rel(owner, zone) {
  const o = canon(owner);
  if (o === zone) return "@";
  if (o.endsWith(`.${zone}`)) return o.slice(0, -zone.length - 1);
  return `${o}.`;
}

// ---- a copied zone ----------------------------------------------------------------------

// byName sorts records as a list shows them: by name (the zone itself first), then type.
function byName(recs, zone) {
  const key = r => { const n = rel(r.owner, zone); return n === "@" ? "" : n; };
  return [...recs].sort((a, b) => key(a).localeCompare(key(b)) || str(a.type).localeCompare(str(b.type)));
}

// readFailed is why a copied zone's records couldn't be read, in plain words (the route's
// kind), with the fix; the technical detail small, after. copied: the nodes have the zone
// (else the problem above says why not).
function readFailed(e, primary, copied) {
  const b = obj(e.body), p = str(b.primary) || primary, src = str(b.source);
  const missing = copied ? "Your nodes have their copy; only this list is missing." : "The controller can't read the records either.";
  let head, more;
  switch (str(b.kind)) {
    case "refused":
      head = `Your primary DNS server (${p}) refused to send this zone to the controller.`;
      more = src ? [`${missing} To see the records here, allow zone transfers from this computer, as `, addr(src), ", on your primary."]
        : [`${missing} To see the records here, allow zone transfers from this computer's address on your primary.`];
      break;
    case "timeout":
    case "unreachable":
      head = `The controller can't reach your primary DNS server (${p}): ${b.kind === "timeout" ? "it didn't answer in time" : "it didn't take the connection"}.`;
      more = [`${missing} Check that your primary is up and reachable from this computer, then read again.`];
      break;
    case "unknown":
      head = `Your primary DNS server (${p}) doesn't serve this zone.`;
      more = ["Check the zone's name, and that this is the right primary for it."];
      break;
    case "failed":
      head = `Your primary DNS server (${p}) didn't send this zone to the controller.`;
      more = [missing];
      break;
    default:
      return [h("p", { class: "hint bad" }, said(e.message))];
  }
  return [h("p", { class: "bad", id: "records-error" }, head), h("p", {}, ...more),
    b.detail ? h("p", { class: "small muted" }, str(b.detail)) : ""];
}

function secondaryBody(z) {
  const body = zoneCtx.body;
  const primary = z.primary || primaryAddr();
  const recs = h("div", { class: "stack" }, h("p", { class: "hint" }, z.pending === "add" ? "Its records show here after Apply, once your nodes copy it." : "Reading its records…"));
  const again = h("button", { class: "btn small", type: "button" }, "Read again");
  const stop = h("button", { class: "btn danger", type: "button", id: "remove-zone", disabled: !data.canWrite || z.pending === "remove" }, "Stop copying this zone");
  const msg = h("span", { class: "hint bad", role: "status" });
  stop.addEventListener("click", () => confirmRemove(z, "Stop copying", `Your nodes stop answering for ${z.name} and ask the internet instead. Your primary keeps the zone.`,
    () => editor.removeConfigZone(z.name, "secondary"), msg));
  const load = async () => {
    if (z.pending === "add") return;
    again.disabled = true;
    try {
      const r = await getJSON(`api/zone-records?name=${enc(z.name)}`);
      const list = arr(r.records);
      recs.replaceChildren(h("p", { class: "hint", id: "records-from" }, r.node ? `Read from ${r.node}'s copy.` : `Read from your primary DNS server (${str(r.primary)}).`),
        list.length ? recTable(byName(list, z.name)) : h("p", { class: "hint" }, "No records."),
        r.more ? h("p", { class: "hint" }, `And ${r.more} more: see them at your primary.`) : "");
    } catch (e) {
      recs.replaceChildren(...readFailed(e, primary, !!z.serial && !["expired", "waiting"].includes(z.state)));
    }
    again.disabled = false;
  };
  again.addEventListener("click", load);
  body.replaceChildren(
    h("div", { class: "row", id: "edit-at" }, h("span", {}, "Edit these records on your primary DNS server,"),
      primary ? addr(primary) : h("span", { class: "muted" }, "whose address no config names"),
      h("span", { class: "hint" }, "They're read-only here.")),
    section("Records", null, recs, z.pending === "add" ? null : h("div", { class: "row" }, again)),
    h("div", { class: "row" }, stop, msg));
  load();
}

// confirmRemove asks before a zone is removed (as a pending change), in a panel.
function confirmRemove(z, verb, text, go, msg) {
  const p = panel(`${verb} ${z.name}?`);
  const yes = h("button", { class: "btn danger", type: "button", id: "confirm-remove" }, verb);
  const err = h("p", { class: "hint bad", role: "status" });
  yes.addEventListener("click", async () => {
    yes.disabled = true;
    try { await go(); p.close(); toast(`${verb}: ${z.name} is waiting to apply`); } catch (e) { err.textContent = said(e.message); yes.disabled = false; if (msg) msg.textContent = said(e.message); }
  });
  p.body.append(h("p", {}, text), h("p", { class: "hint" }, "Nothing changes on the nodes until Apply."), err);
  p.footer.append(h("button", { class: "btn", type: "button", onclick: p.close }, "Cancel"), yes);
}

// ---- a forwarded zone -------------------------------------------------------------------

function forwardBody(z) {
  const to = z.to || z.forwarders[0] || "";
  const input = h("input", { class: "t mono", id: "fto", type: "text", value: to, spellcheck: "false", autocomplete: "off" });
  const save = h("button", { class: "btn", type: "button", id: "fsave", disabled: !data.canWrite }, "Save");
  const msg = h("span", { class: "hint", role: "status" });
  save.addEventListener("click", async () => {
    const ip = input.value.trim();
    if (!IPV4.test(ip)) { msg.className = "hint bad"; msg.textContent = "Type the server's IPv4 address, like 192.0.2.53."; return; }
    save.disabled = true;
    try {
      const n = await setForwarder(z.name, ip);
      msg.className = "hint";
      msg.textContent = n ? "" : "It already goes there.";
      if (n) toast(`${z.name} goes to ${ip} after Apply`);
    } catch (e) { msg.className = "hint bad"; msg.textContent = said(e.message); }
    save.disabled = !data.canWrite;
  });
  const stop = h("button", { class: "btn danger", type: "button", id: "remove-zone", disabled: !data.canWrite || z.pending === "remove" }, "Stop forwarding");
  stop.addEventListener("click", () => confirmRemove(z, "Stop forwarding", `Your nodes stop sending ${z.name}'s queries to ${to} and ask the internet instead.`,
    () => editor.removeConfigZone(z.name, "forward")));
  zoneCtx.body.replaceChildren(section("Forwarding", null,
    h("p", {}, "Queries for ", h("span", { class: "mono" }, z.name), " and every name under it go to ", h("span", { class: "mono" }, to || "–"), ", a DNS server that knows this domain."),
    h("details", { class: "ex", id: "fwd-row" }, h("summary", {}, h("b", {}, "Send its queries to"), h("span", { class: "val mono" }, to)),
      h("div", { class: "exb" }, h("label", { class: "f" }, h("span", {}, "Server's address"), input, h("span", { class: "hint" }, "An IPv4 address. Live after Apply, no restart.")),
        h("div", { class: "row" }, save, msg)))),
  h("div", { class: "row" }, stop));
}

// ---- a hosted zone ----------------------------------------------------------------------

// The hosted zone being edited: its file as the pending changes make it, and its records.
let hz = null; // {name, file, text, hash, exists, partial, recs, more, error}

async function readHosted(name) {
  const f = await getJSON(`api/changes/file?kind=zone&name=${enc(fileOf(name))}`);
  const out = { name, file: fileOf(name), text: str(f.text), hash: str(f.hash), exists: !!f.exists, partial: !!f.partial, recs: [], more: 0, error: "" };
  if (out.exists && !out.partial) {
    const ck = await post(`api/zones/${enc(out.file)}/check`, { text: out.text });
    out.recs = arr(ck.list); out.more = ck.more || 0; out.error = str(ck.error);
  }
  return out;
}

async function hostedBody(z) {
  const body = zoneCtx.body;
  body.replaceChildren(h("p", { class: "hint" }, "Reading its records…"));
  let cur;
  try { cur = await readHosted(z.name); } catch (e) { body.replaceChildren(h("p", { class: "hint bad" }, said(e.message))); return; }
  if (!zoneCtx || zoneCtx.name !== z.name) return;
  hz = cur;
  if (!cur.exists) {
    body.replaceChildren(h("p", { class: "hint" }, z.pending === "remove" ? "It's removed with the next Apply." : "There's no file for it here."));
    return;
  }
  const editable = !cur.partial && !cur.error && !cur.more && data.canWrite && z.pending !== "remove";
  const recs = cur.recs.length ? recTable(cur.recs, editable ? (r, i) => [
    h("button", { class: "btn small", type: "button", "aria-label": `Edit ${rel(r.owner, z.name)} ${r.type}`, onclick: () => recordPanel(i) }, "Edit"),
    r.type === "SOA" ? null : h("button", { class: "btn small", type: "button", "aria-label": `Delete ${rel(r.owner, z.name)} ${r.type}`, onclick: () => deleteRecord(i) }, "Delete"),
  ] : null) : h("p", { class: "hint" }, cur.error ? `The file doesn't pass its checks: ${cur.error}` : "No records.");
  const hint = cur.more ? `${cur.more} more records aren't shown: edit the file under Advanced.`
    : cur.error ? "Fix the file under Advanced." : "Changes wait in \"changes to apply\"; the zone's version is raised for you.";
  body.replaceChildren(section("Records", null, recs, h("p", { class: "hint" }, hint)), advanced(z, cur));
}

// recordLine is a record as a line of the zone file.
const recordLine = (owner, ttl, type, value) => [owner, ttl ? String(ttl) : "", "IN", type, value].filter(Boolean).join("\t");

// canonical is the zone file written plainly from its records (a record edited or deleted).
function canonical(zone, recs) {
  return `$ORIGIN ${zone}.\n${recs.map(r => recordLine(rel(r.owner, zone), r.ttl, r.type, r.data)).join("\n")}\n`;
}

// saveZone raises the text's version (its SOA serial) and adds it as a pending change.
async function saveZone(text, summary) {
  const s = await post(`api/zones/${enc(hz.file)}/serial`, { text });
  await post("api/changes", { kind: "zone", name: hz.file, text: str(s.text), hash: hz.hash, summary });
  zoneCtx.key = "";
  await after();
  refreshZoneHead();
}

const TYPES = ["A", "AAAA", "CNAME", "TXT", "MX", "NS", "SRV", "CAA", "PTR"];
const VALUEHINT = {
  A: "An IPv4 address, like 192.0.2.10.", AAAA: "An IPv6 address.", CNAME: "Another name; without a final dot, it's under this zone.",
  TXT: "Text; quotes are added for you.", MX: "Its preference and mail server, like 10 mail.", NS: "A name server's name.",
  SRV: "Priority, weight, port and target, like 0 5 5060 sip.", CAA: "Flags, tag and value, like 0 issue \"letsencrypt.org\".", PTR: "The name it points to.", SOA: "The zone's SOA: its version is raised for you.",
};

// recordPanel adds a record (i null) or edits record i, in a slide-out panel.
function recordPanel(i) {
  if (!hz) return;
  const zone = hz.name;
  const r = i === null ? null : hz.recs[i];
  const p = panel(r ? `Edit ${rel(r.owner, zone)} ${r.type}` : `Add a record to ${zone}`);
  const name = h("input", { class: "t mono", id: "rname", type: "text", value: r ? rel(r.owner, zone) : "", placeholder: "nas", spellcheck: "false", autocomplete: "off", autocapitalize: "off" });
  const type = h("select", { class: "t", id: "rtype", disabled: !!r }, (r && !TYPES.includes(r.type) ? [r.type, ...TYPES] : TYPES).map(t => h("option", { value: t, selected: r ? t === r.type : t === "A" }, t)));
  const value = h("input", { class: "t mono", id: "rvalue", type: "text", value: r ? str(r.data) : "", spellcheck: "false", autocomplete: "off", autocapitalize: "off" });
  const ttl = h("input", { class: "t num", id: "rttl", type: "number", min: "0", max: "2147483647", value: r ? String(r.ttl) : "", placeholder: "The zone's default" });
  const vhint = h("span", { class: "hint" });
  const sayType = () => { vhint.textContent = VALUEHINT[type.value] || ""; };
  type.addEventListener("change", sayType);
  sayType();
  const err = h("p", { class: "hint bad", role: "status" });
  const go = h("button", { class: "btn primary", type: "button", id: "rsave" }, r ? "Save record" : "Add record");
  go.addEventListener("click", async () => {
    err.textContent = "";
    let owner = name.value.trim() || "@";
    if (!/^(@|\*|[A-Za-z0-9_*-]+(\.[A-Za-z0-9_-]+)*\.?)$/.test(owner)) { err.textContent = "Letters, digits, '-' and '_', with dots between labels; @ for the zone itself."; return; }
    let v = value.value.trim();
    if (!v) { err.textContent = "The value is empty."; return; }
    const t = type.value;
    if (t === "A" && !IPV4.test(v)) { err.textContent = "An A record's value is an IPv4 address."; return; }
    if (t === "TXT" && !v.startsWith("\"")) v = `"${v.replace(/\\/g, "\\\\").replace(/"/g, "\\\"")}"`;
    const tt = ttl.value.trim();
    if (tt && !/^\d+$/.test(tt)) { err.textContent = "The TTL is a number of seconds."; return; }
    go.disabled = true;
    try {
      let text, summary;
      const shownOwner = owner === "@" ? zone : owner.endsWith(".") ? owner.slice(0, -1) : `${owner}.${zone}`;
      if (!r) {
        // A record added goes at the end of the file as it is: its comments and layout kept.
        const origin = /^\$ORIGIN/im.test(hz.text) ? `$ORIGIN ${zone}.\n` : "";
        text = `${hz.text.replace(/\n*$/, "\n")}${origin}${recordLine(owner, tt, t, v)}\n`;
        summary = `Edit zone ${zone}: add ${shownOwner} ${t}`;
      } else {
        const recs = hz.recs.map((x, j) => (j === i ? { owner: owner === "@" ? `${zone}.` : owner.endsWith(".") ? owner : `${owner}.${zone}.`, type: t, ttl: tt ? Number(tt) : x.ttl, data: v } : x));
        text = canonical(zone, recs);
        summary = `Edit zone ${zone}: change ${shownOwner} ${t}`;
      }
      await saveZone(text, summary);
      p.close();
      toast(`${shownOwner} ${t} is waiting to apply`);
    } catch (e) { err.textContent = said(e.message); go.disabled = false; }
  });
  p.body.append(
    h("label", { class: "f" }, h("span", {}, "Name"), name, h("span", { class: "hint" }, `nas for nas.${zone}; @ for the zone itself.`)),
    h("label", { class: "f" }, h("span", {}, "Type"), type),
    h("label", { class: "f" }, h("span", {}, "Value"), value, vhint),
    h("label", { class: "f" }, h("span", {}, "TTL, seconds"), ttl, h("span", { class: "hint" }, "How long others may keep the answer. Empty: the zone's default.")),
    h("p", { class: "hint" }, "Live on every node after Apply, no restart."), err);
  p.footer.append(h("button", { class: "btn", type: "button", onclick: p.close }, "Cancel"), go);
  name.focus();
}

function deleteRecord(i) {
  const zone = hz.name, r = hz.recs[i];
  const p = panel(`Delete ${rel(r.owner, zone)} ${r.type}?`);
  const err = h("p", { class: "hint bad", role: "status" });
  const yes = h("button", { class: "btn danger", type: "button", id: "rdelete" }, "Delete record");
  yes.addEventListener("click", async () => {
    yes.disabled = true;
    try {
      await saveZone(canonical(zone, hz.recs.filter((_, j) => j !== i)), `Edit zone ${zone}: remove ${canon(r.owner)} ${r.type}`);
      p.close();
      toast(`${canon(r.owner)} ${r.type} is removed after Apply`);
    } catch (e) { err.textContent = said(e.message); yes.disabled = false; }
  });
  p.body.append(h("p", {}, h("span", { class: "mono" }, `${canon(r.owner)} ${r.type} ${str(r.data)}`)),
    h("p", { class: "hint" }, "The file is rewritten from its records, so its comments are lost. To keep them, edit the file under Advanced."), err);
  p.footer.append(h("button", { class: "btn", type: "button", onclick: p.close }, "Cancel"), yes);
}

// advanced is the zone's file, edited as text; its earlier versions, put back; and the zone
// deleted.
function advanced(z, cur) {
  const ta = h("textarea", { class: "t zfile", id: "zfile", spellcheck: "false", readonly: cur.partial || !data.canWrite, "aria-label": `${z.name}'s zone file` });
  ta.value = cur.text;
  const out = h("div", { class: "stack" });
  const check = h("button", { class: "btn", type: "button", id: "zcheck" }, "Check");
  const save = h("button", { class: "btn", type: "button", id: "zsave", disabled: cur.partial || !data.canWrite || z.pending === "remove" }, "Save as a change");
  check.addEventListener("click", async () => {
    check.disabled = true;
    try {
      const r = await post(`api/zones/${enc(cur.file)}/check`, { text: ta.value });
      out.replaceChildren(r.error ? h("p", { class: "hint bad" }, r.error) : h("p", { class: "hint" }, `Passes: ${plural(r.records || 0, "record", "records")}.`),
        arr(r.lines).length ? h("ul", { class: "lines" }, arr(r.lines).map(l => h("li", {}, str(l)))) : "");
    } catch (e) { out.replaceChildren(h("p", { class: "hint bad" }, said(e.message))); }
    check.disabled = false;
  });
  save.addEventListener("click", async () => {
    save.disabled = true;
    try { await saveZone(ta.value, `Edit zone ${z.name}: the file`); toast(`${z.name} is waiting to apply`); }
    catch (e) { out.replaceChildren(h("p", { class: "hint bad" }, said(e.message))); save.disabled = false; }
  });
  const fileRow = h("details", { class: "ex", id: "zfile-row" }, h("summary", {}, h("b", {}, "The zone file"), h("span", { class: "val mono" }, cur.file)),
    h("div", { class: "exb" }, cur.partial ? h("p", { class: "hint" }, "Too big to show whole: edit it in the controller's data directory.") : null, ta,
      h("div", { class: "row" }, check, save, h("span", { class: "hint" }, "Its version is raised for you.")), out));

  const vers = h("div", { class: "vers" }, h("p", { class: "hint" }, "Reading…"));
  const histRow = h("details", { class: "ex", id: "history-row" }, h("summary", {}, h("b", {}, "Earlier versions"), h("span", { class: "val" }, "")), h("div", { class: "exb" }, vers));
  let read = false;
  histRow.addEventListener("toggle", async () => {
    if (!histRow.open || read) return;
    read = true;
    try {
      const f = await getJSON(`api/zones/${enc(cur.file)}`);
      const hs = arr(f.history);
      vers.replaceChildren(...(hs.length ? hs.map(v => {
        const b = h("button", { class: "btn small", type: "button", disabled: !data.canWrite || cur.partial }, "Put back");
        const when = new Date(v.time).toLocaleString();
        b.addEventListener("click", async () => {
          b.disabled = true;
          try {
            const old = await getJSON(`api/zones/${enc(cur.file)}?version=${enc(str(v.file))}`);
            await saveZone(str(old.text), `Edit zone ${z.name}: put back the version from ${when}`);
            toast(`The version from ${when} is waiting to apply`);
          } catch (e) { toast(said(e.message)); b.disabled = false; }
        });
        return h("div", { class: "change" }, h("div", {}, when, h("div", { class: "hint" }, `${v.size} bytes`)), b);
      }) : [h("p", { class: "hint" }, f.text !== undefined ? "None yet: each save keeps the version it replaces." : "None.")]));
    } catch (e) { const none = /no zone file/.test(e.message); vers.replaceChildren(h("p", { class: none ? "hint" : "hint bad" }, none ? "None yet: it isn't saved until Apply." : said(e.message))); }
  });

  const del = h("button", { class: "btn danger", type: "button", id: "remove-zone", disabled: !data.canWrite || z.pending === "remove" }, "Delete this zone");
  del.addEventListener("click", () => {
    if (z.pending === "add") {
      undoAsk(z.name, () => { toast(`Zone ${z.name} won't be added`); location.hash = ""; }).catch(e => toast(said(e.message)));
      return;
    }
    confirmRemove(z, "Delete", `Your nodes stop answering for ${z.name}. The file stays in its history, so you can put it back.`,
      async () => {
        await post("api/changes", { kind: "zone", name: cur.file, delete: true, hash: cur.hash, summary: `Remove zone ${z.name}` });
        zoneCtx.key = "";
        await after();
        refreshZoneHead();
      });
  });
  return section("Advanced", null, h("div", {}, fileRow, histRow), h("div", { class: "row" }, del));
}

render();
