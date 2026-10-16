// The shell every page shares (shell.css): the top bar (Nodes · Zones · Blocking · Query log, the changes
// waiting to apply, the account), the band of what's wrong across the fleet, and the parts
// the pages are built from: the slide-out panel, the copyable address, the node's LED as a
// dot and the toast. The pending changes are collected by the pages (POST /api/changes,
// /api/updates) and sent by one Apply from the header's panel (the apply job, apply.go).
// Everything from a node or the controller is put in as text.
import { api, getJSON, post, session, events, el, loggedIn, onNodes, watchNodes, every, answering, wired, ended, nodeText, inService } from "./common.js";

const PAGES = [["index.html", "Nodes"], ["zones.html", "Zones"], ["blocklists.html", "Blocking"], ["querylog.html", "Query log"]];
const SVG = "http://www.w3.org/2000/svg";
export const arr = v => (Array.isArray(v) ? v : []);
export const obj = v => (v && typeof v === "object" && !Array.isArray(v) ? v : {});
export const str = v => (typeof v === "string" ? v : "");
export const hostOnly = a => str(a).replace(/:\d+$/, "");

// h makes an element: attrs are its class ("class"), handlers ("on" + event: a function)
// and attributes; children are elements, strings (as text) or nothing (null, false).
export function h(tag, attrs, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v === null || v === undefined || v === false) continue;
    if (k === "class") e.className = v;
    else if (k.startsWith("on") && typeof v === "function") e.addEventListener(k.slice(2), v);
    else if (k in e && typeof v !== "string") e[k] = v;
    else e.setAttribute(k, v === true ? "" : v);
  }
  for (const c of kids.flat()) if (c !== null && c !== undefined && c !== false) e.append(c);
  return e;
}

// icon draws one of the few line icons: copy, x, ethernet (a wired port), wifi.
const ICONS = {
  copy: [16, ["rect", { x: 5, y: 5, width: 9, height: 9, rx: 1.5 }], ["path", { d: "M11 3.5V3a1 1 0 0 0-1-1H3a1 1 0 0 0-1 1v7a1 1 0 0 0 1 1h.5" }]],
  x: [14, ["path", { d: "M2 2l10 10M12 2L2 12", "stroke-linecap": "round" }]],
  ethernet: [16, ["path", { d: "M2.5 4.5h11v7h-3v2h-5v-2h-3zM5.5 7v1.5M8 7v1.5M10.5 7v1.5", "stroke-linejoin": "round" }]],
  wifi: [16, ["path", { d: "M1.5 6a9.5 9.5 0 0 1 13 0M3.8 8.6a6.2 6.2 0 0 1 8.4 0M6 11.1a3 3 0 0 1 4 0", "stroke-linecap": "round" }], ["circle", { cx: 8, cy: 13.3, r: .9, fill: "currentColor" }]],
};
function icon(name) {
  const [size, ...parts] = ICONS[name];
  const s = document.createElementNS(SVG, "svg");
  for (const [k, v] of Object.entries({ width: 14, height: 14, viewBox: `0 0 ${size} ${size}`, "aria-hidden": "true", fill: "none", stroke: "currentColor", "stroke-width": name === "x" ? 1.8 : 1.5 })) s.setAttribute(k, v);
  for (const [tag, at] of parts) {
    const p = document.createElementNS(SVG, tag);
    for (const [k, v] of Object.entries(at)) p.setAttribute(k, v);
    s.append(p);
  }
  return s;
}

// ---- the node's LED ---------------------------------------------------------------------
// The board's RGB LED as a plain dot, with the colours and rhythms of firmware led.c; its
// title spells out the pattern.
const LEDTXT = {
  healthy: "Its light: a green blip every 5 s (healthy)",
  degraded: "Its light: two amber blinks every 2 s (degraded)",
  fault: "Its light: solid red (fault) or slow red (no network)",
  nonet: "Its light: slow red blink (no network)",
  booting: "Its light: fast blue blink (starting)",
  updating: "Its light: three quick blue blinks (updating)",
  identify: "Its light: white flicker (identify)",
  unknown: "Old firmware: its light doesn't follow espDNS's patterns yet",
  out: "Out of service: the controller sends it nothing",
  todo: "Something to do",
};
// led is the dot; tip, if given, says what it means instead of the pattern.
export function led(kind, tip) {
  const k = Object.hasOwn(LEDTXT, kind) ? kind : "unknown";
  return h("span", { class: `led ${k}`, role: "img", title: tip || LEDTXT[k], "aria-label": tip || LEDTXT[k] });
}

// ---- copying ----------------------------------------------------------------------------
// copyText puts text on the clipboard: the Clipboard API where the page may use it (https,
// or localhost), else the older copy command (the controller is often on plain http).
export async function copyText(text) {
  try {
    if (navigator.clipboard && window.isSecureContext) { await navigator.clipboard.writeText(text); return true; }
  } catch { /* the fallback below */ }
  const t = h("textarea", { class: "offscreen", readonly: true, "aria-hidden": "true" });
  t.value = text;
  document.body.append(t);
  t.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } catch { ok = false; }
  t.remove();
  return ok;
}

// copyable is text with a copy button: what the button names (what), and the box's class.
function copyable(text, what, cls) {
  const b = h("button", { class: "cp", type: "button", title: "Copy", "aria-label": `Copy ${what}` }, icon("copy"));
  b.addEventListener("click", async e => {
    e.preventDefault();
    e.stopPropagation();
    toast(await copyText(text) ? `Copied ${what}` : `Couldn't copy: select ${text} and copy it`);
  });
  return h("span", { class: cls }, h("span", { class: "ip" }, text), b);
}

// addr is an address shown prominently, with a copy button; link ("ethernet" or "wifi") an
// icon before it, and m a MAC said on hovering it.
export function addr(ip, link, m) {
  const c = copyable(ip, ip, "ipc");
  const w = link === "ethernet" ? "Wired" : link === "wifi" ? "Wi-Fi" : "";
  if (w) c.prepend(h("span", { class: "link", role: "img", title: w, "aria-label": w }, icon(link)));
  if (m) c.querySelector(".ip").title = `MAC ${m}`;
  return c;
}

// MACHINT says what a node's MAC is for.
export const MACHINT = "For a DHCP reservation in your router, or to set a static address.";

// mac is a node's MAC (its interface's, from /api/nodes or /api/nodes/found), smaller than
// an address, with a copy button; null for none.
export function mac(m) {
  if (!/^[0-9a-f]{2}(:[0-9a-f]{2}){5}$/.test(str(m))) return null;
  const c = copyable(m, `MAC ${m}`, "ipc mac");
  c.title = MACHINT;
  c.prepend(h("span", { class: "k" }, "MAC"));
  return c;
}

// ---- the toast --------------------------------------------------------------------------
let toastTimer = null;
export function toast(msg) {
  const old = document.getElementById("toast");
  if (old) old.remove();
  clearTimeout(toastTimer);
  if (!msg) return;
  const d = h("div", { class: "toast", id: "toast", role: "status" }, h("span", {}, msg));
  if (pending.changes.length) d.append(h("button", { type: "button", onclick: () => { toast(""); openReview(); } }, "Review"));
  document.body.append(d);
  toastTimer = setTimeout(() => d.remove(), 6000);
}

// ---- the slide-out panel ----------------------------------------------------------------
// panel opens a slide-out panel: straight-edged, full height (a bottom sheet on a phone),
// closed by its x, Esc or a click outside. It returns {body, footer, title, close}; the
// caller fills body and footer. onClose hears it close. One is open at a time.
let openOne = null;
export function panel(title, onClose) {
  if (openOne) openOne.close();
  const back = document.activeElement;
  const h2 = h("h2", { id: "panel-title" }, title);
  const x = h("button", { class: "x", type: "button", "aria-label": "Close" }, icon("x"));
  const body = h("div", { class: "body" });
  const footer = h("footer", {});
  const aside = h("aside", { class: "drawer", role: "dialog", "aria-modal": "true", "aria-labelledby": "panel-title" },
    h("header", {}, h2, x), body, footer);
  const scrim = h("div", { class: "scrim" });
  // Esc closes the panel, unless a dialog over it (askPassword, the USB installer's) is open:
  // Esc is that one's.
  const key = e => { if (e.key === "Escape" && !document.querySelector("dialog[open], ewt-install-dialog")) { e.preventDefault(); p.close(); } };
  const p = {
    body, footer, aside,
    title: t => { h2.textContent = t; },
    close: () => {
      if (openOne !== p) return;
      openOne = null;
      document.removeEventListener("keydown", key);
      aside.remove(); scrim.remove();
      if (back && back.isConnected) back.focus();
      if (onClose) onClose();
    },
  };
  x.addEventListener("click", p.close);
  scrim.addEventListener("click", p.close);
  document.addEventListener("keydown", key);
  document.body.append(scrim, aside);
  openOne = p;
  x.focus();
  return p;
}

// ---- the pending changes and the apply --------------------------------------------------
const pending = { changes: [], restarts: [], apply: null, read: false, error: null, listeners: new Set() };
const follow = { id: null, job: null, progress: null, es: null };
let changesBox = null, canWrite = false;

// onChanges calls f({changes, restarts, apply}) after every read of /api/changes.
export function onChanges(f) { pending.listeners.add(f); if (pending.read) f(pending); }

// refreshChanges reads /api/changes now (after a page added one).
export async function refreshChanges() {
  try {
    const r = await getJSON("api/changes");
    pending.changes = arr(r.changes);
    pending.restarts = arr(r.restarts);
    pending.apply = r.apply && typeof r.apply === "object" ? r.apply : null;
    pending.error = null;
  } catch (e) {
    pending.error = e;
  }
  pending.read = true;
  if (pending.apply && !ended(pending.apply.state) && follow.id !== pending.apply.id) followApply(pending.apply);
  // The apply followed has ended though its events didn't say so (the stream dropped): take
  // the controller's word for it.
  else if (pending.apply && follow.id === pending.apply.id && ended(pending.apply.state) && follow.job && !ended(follow.job.state)) {
    if (follow.es) { follow.es.close(); follow.es = null; }
    follow.job = pending.apply;
    if (pending.apply.progress) follow.progress = pending.apply.progress;
    if (applyView) applyView();
  }
  renderChip();
  for (const f of pending.listeners) { try { f(pending); } catch (e) { console.error(e); } }
}

const plural = (n, one, many) => `${n} ${n === 1 ? one : many}`;
const applying = () => follow.job && !ended(follow.job.state);

// The step an apply is on, in words, for the chip: the node being sent to while it pushes.
const STEP = { check: "checking", write: "writing", compile: "compiling", "dry run": "trying it", push: "sending" };
function applyWhere() {
  const p = obj(follow.progress);
  for (const r of arr(p.rollouts)) {
    for (const host of arr(r.order)) {
      const st = obj(obj(r.nodes)[host]).state;
      if (["pushing", "rebooting", "checking", "soaking"].includes(st)) return nodeText(host);
    }
  }
  const s = arr(p.steps).find(x => x.state === "running");
  return s ? STEP[s.name] || s.name : "";
}

function renderChip() {
  if (!changesBox) return;
  if (applying()) {
    const w = applyWhere();
    changesBox.replaceChildren(h("button", { class: "chip-btn running", type: "button", onclick: openApply },
      h("span", { class: "spin", "aria-hidden": "true" }), w ? `Applying · ${w}` : "Applying"));
  } else if (pending.changes.length) {
    changesBox.replaceChildren(h("button", { class: "chip-btn pending", type: "button", onclick: openReview },
      `${plural(pending.changes.length, "change", "changes")} to apply`));
  } else changesBox.replaceChildren();
}

// The rollout options: which node goes first, how long each is watched (the apply's
// params); the first is settings.json's canary until one is chosen.
const rollout = { first: "", soak: 60, read: false };
async function readRollout() {
  if (rollout.read) return;
  try { rollout.first = str(obj((await getJSON("api/settings")).settings).canary); rollout.read = true; } catch { /* the first listed */ }
}
// The nodes an apply changes and that keep DNS answering: in settings.json, in service.
const listedNodes = () => arr(fleetNodes).filter(inService);

function rolloutRow() {
  const ns = listedNodes();
  if (!rollout.first || !ns.some(n => n.addr === rollout.first)) rollout.first = ns.length ? ns[0].addr : "";
  const val = h("span", { class: "val" });
  const say = () => {
    val.textContent = `${rollout.first ? `${nodeText(rollout.first)} first · ` : ""}watched ${rollout.soak / 60} min each`;
  };
  const first = h("select", { class: "t", id: "ro-first", onchange: e => { rollout.first = e.target.value; say(); } },
    ns.map(n => h("option", { value: n.addr, selected: n.addr === rollout.first }, nodeText(n))));
  const watch = h("select", { class: "t", id: "ro-watch", onchange: e => { rollout.soak = Number(e.target.value); say(); } },
    [[60, "1 minute"], [300, "5 minutes"], [900, "15 minutes"]].map(([v, t]) => h("option", { value: String(v), selected: v === rollout.soak }, t)));
  say();
  return h("details", { class: "ex" }, h("summary", {}, h("b", {}, "How it rolls out"), val),
    h("div", { class: "exb" },
      h("p", { class: "small" }, "One node at a time, each checked and watched before the next. If one fails its checks, the apply stops there and the changes keep waiting."),
      h("div", { class: "grid2" },
        ns.length ? h("label", { class: "f" }, h("span", {}, "Start with"), first) : null,
        h("label", { class: "f" }, h("span", {}, "Watch each node for"), watch))));
}

function openReview() {
  if (applying()) { openApply(); return; }
  const p = panel("Changes to apply");
  const list = h("div", {});
  const note = h("p", { class: "small" });
  const msg = h("p", { class: "hint bad", role: "status" });
  const discardAll = h("button", { class: "btn", type: "button" }, "Discard all");
  const go = h("button", { class: "btn primary", type: "button" });
  const fill = () => {
    if (!pending.changes.length) { p.close(); return; }
    list.replaceChildren(...pending.changes.map(c => {
      const b = h("button", { class: "btn small", type: "button", disabled: !canWrite }, "Discard");
      b.addEventListener("click", () => act(b, () => post(`api/changes/${encodeURIComponent(c.id)}/discard`)));
      return h("div", { class: "change" }, h("div", {}, str(c.summary) || str(c.name) || str(c.kind), h("div", { class: "hint" }, str(c.effect))), b);
    }));
    const rs = pending.restarts.map(x => nodeText(x));
    note.textContent = rs.length
      ? `${rs.join(", ")} restart${rs.length === 1 ? "s" : ""} for a few seconds, one at a time; the other nodes keep answering.`
      : "No restart needed.";
    go.textContent = `Apply ${plural(pending.changes.length, "change", "changes")}`;
    go.disabled = discardAll.disabled = !canWrite;
  };
  async function act(btn, f) {
    btn.disabled = true;
    msg.textContent = "";
    try { await f(); await refreshChanges(); fill(); } catch (e) { msg.textContent = e.message; btn.disabled = false; }
  }
  discardAll.addEventListener("click", () => act(discardAll, () => post("api/changes/discard")));
  go.addEventListener("click", async () => {
    go.disabled = true;
    msg.textContent = "";
    try {
      const params = { soak_s: rollout.soak };
      if (rollout.first) params.canary = rollout.first;
      startApply(await post("api/jobs", { kind: "apply", params }));
    } catch (e) {
      // The changes may have gone meanwhile (an update for nodes already on its version):
      // the list read again, the panel closed when none is left.
      await refreshChanges();
      if (!pending.changes.length) { p.close(); toast(e.message); return; }
      fill();
      msg.textContent = e.message;
      go.disabled = false;
    }
  });
  p.body.append(list, note, msg);
  p.footer.append(discardAll, go);
  fill();
  // The row now, with the first listed node; again once settings.json's canary is read.
  let row = rolloutRow();
  note.after(row);
  readRollout().then(() => {
    if (openOne !== p || !row.isConnected) return;
    const again = rolloutRow();
    again.open = row.open;
    row.replaceWith(again);
    row = again;
  });
}

function startApply(j) {
  followApply(j);
  openApply();
}

// followApply follows an apply's progress (its events) while it runs.
function followApply(j) {
  if (follow.es) follow.es.close();
  follow.id = j.id;
  follow.job = j;
  follow.progress = j.progress ? j.progress : null;
  const es = events(`api/jobs/${encodeURIComponent(j.id)}/events`);
  follow.es = es;
  const upd = () => { renderChip(); if (applyView) applyView(); };
  es.addEventListener("state", e => { const s = JSON.parse(e.data); follow.job = s; if (s.progress) follow.progress = s.progress; upd(); });
  es.addEventListener("progress", e => { follow.progress = JSON.parse(e.data); upd(); });
  es.addEventListener("end", e => {
    const s = JSON.parse(e.data);
    es.close();
    if (follow.es === es) follow.es = null;
    follow.job = s;
    if (s.progress) follow.progress = s.progress;
    upd();
    refreshChanges();
    if (s.state === "done" && !applyView) toast("All changes applied");
  });
  // The controller no longer has the job (it restarted): the apply isn't running, whatever
  // its last state said; /api/changes says what there is now.
  es.addEventListener("gone", () => {
    if (follow.es === es) follow.es = null;
    if (follow.id !== j.id) return;
    follow.job = { ...follow.job, state: "failed", error: "The controller no longer has this apply: it may have restarted. Changes not sent are still waiting." };
    upd();
    refreshChanges();
  });
  renderChip();
}

// What each apply step and a node's state in a rollout are, in words.
const STEPWORDS = { check: "Check the changes", write: "Write the files", compile: "Compile the blocklists", "dry run": "Try it against every node", push: "Send to the nodes" };
const NODEWORDS = { waiting: "Waiting", gate: "Waiting for the others to answer", "would-push": "Would be sent", pushing: "Sending", rebooting: "Restarting",
  checking: "Checking its answers", checked: "Sent, answering, checked", soaking: "Watching", done: "Sent, answering, checked", skipped: "Already has it",
  failed: "Failed", left: "Left as it was", refused: "Refused", reverting: "Going back", reverted: "Back as it was", "not-reverted": "Not back as it was" };
const liState = s => (["done", "checked", "skipped", "reverted"].includes(s) ? "done" : ["failed", "refused", "not-reverted"].includes(s) ? "failed"
  : ["running", "now", "gate", "pushing", "rebooting", "checking", "soaking", "reverting"].includes(s) ? "now" : "");

let applyView = null;
function openApply() {
  if (!follow.job) return;
  const p = panel("Applying changes", () => { applyView = null; });
  const steps = h("ol", { class: "steps" });
  const after = h("div", { class: "stack" });
  const msg = h("p", { class: "hint bad", role: "status" });
  p.body.append(steps, after, msg);
  applyView = () => {
    const j = follow.job, pr = obj(follow.progress), run = !ended(j.state);
    p.title(run ? "Applying changes" : j.state === "done" ? "Changes applied" : j.state === "stopped" ? "Apply stopped" : "Apply failed");
    const items = [];
    for (const s of arr(pr.steps)) {
      if (s.name === "push") {
        items.push(h("li", { class: liState(s.state === "running" ? "now" : s.state) }, h("span", { class: "m" }),
          h("div", {}, h("b", {}, STEPWORDS.push), s.detail ? h("div", { class: "hint" }, str(s.detail)) : null)));
        for (const r of arr(pr.rollouts)) {
          for (const host of arr(r.order)) {
            const ns = obj(obj(r.nodes)[host]);
            items.push(h("li", { class: liState(str(ns.state)) }, h("span", { class: "m" }),
              h("div", {}, h("b", {}, nodeText(host)), h("div", { class: "hint" },
                [str(r.what), NODEWORDS[ns.state] || str(ns.state), str(ns.detail)].filter(Boolean).join(" · ")))));
          }
        }
        continue;
      }
      items.push(h("li", { class: liState(s.state === "running" ? "now" : s.state) }, h("span", { class: "m" }),
        h("div", {}, h("b", {}, STEPWORDS[s.name] || str(s.name)), s.detail ? h("div", { class: "hint" }, str(s.detail)) : null)));
    }
    if (!items.length) items.push(h("li", { class: "now" }, h("span", { class: "m" }), h("div", {}, h("b", {}, "Starting"))));
    steps.replaceChildren(...items);
    const out = [];
    for (const q of arr(pr.questions)) {
      const b = h("button", { class: "btn primary", type: "button" }, "Apply, taking this change");
      b.addEventListener("click", async () => {
        b.disabled = true;
        try { startApply(await post("api/jobs", { kind: "apply", params: obj(q.params) })); }
        catch (e) { msg.textContent = e.message; b.disabled = false; }
      });
      out.push(h("div", { class: "alert-box" }, h("p", {}, str(q.text)), h("div", { class: "row" }, b)));
    }
    // The nodes out of service: skipped, said so.
    const skipped = arr(fleetNodes).filter(n => n.source === "settings" && n.out_of_service);
    if (skipped.length) out.push(h("p", { class: "hint" }, `Skipped: ${skipped.map(n => nodeText(n)).join(", ")} (out of service).`));
    if (run) out.push(h("p", { class: "hint" }, "You can close this; progress shows at the top of every page."));
    else if (j.state === "done") out.push(h("p", {}, skipped.length ? "Every node in service has the changes and is answering." : "Every node has the changes and is answering."));
    else if (pr.outcome || j.error) out.push(h("p", {}, str(pr.outcome) || str(j.error)));
    if (!run && arr(pr.hints).length) out.push(h("ul", { class: "hint" }, arr(pr.hints).map(x => h("li", {}, str(x)))));
    after.replaceChildren(...out);
    if (run) {
      const stop = h("button", { class: "btn danger", type: "button", disabled: !!j.stopping }, j.stopping ? "Stopping after this node" : "Stop after this node");
      stop.addEventListener("click", async () => {
        stop.disabled = true;
        try { await post(`api/jobs/${encodeURIComponent(j.id)}/stop`); } catch (e) { msg.textContent = e.message; stop.disabled = false; }
      });
      p.footer.replaceChildren(stop);
    } else p.footer.replaceChildren(h("button", { class: "btn primary", type: "button", onclick: p.close }, "Done"));
  };
  applyView();
}

// ---- the fleet's band -------------------------------------------------------------------
let fleetNodes = null, bandBox = null, sessBand = null;
function renderBand(error) {
  if (!bandBox) return;
  const out = [];
  if (sessBand) out.push(sessBand);
  const band = (cls, strong, text) => h("div", { class: `band ${cls}` }, h("div", { class: "band-in" }, h("strong", {}, strong), text ? h("span", {}, text) : null));
  if (error) out.push(band("warn", "The controller isn't answering.", "What this page shows may be out of date."));
  const ns = listedNodes();
  if (ns.length && !ns.some(answering)) {
    out.push(band("bad", "No espDNS node is answering.", `Check power and network cables on ${ns.map(n => nodeText(n)).join(" and ")}.`));
  } else {
    // A node never read since the controller started may be wired: it counts as one here.
    const w = ns.filter(n => wired(n) || !n.status);
    if (w.length && !w.some(answering)) out.push(band("bad", "No wired node is answering.", "Only Wi-Fi nodes answer DNS queries now."));
  }
  bandBox.replaceChildren(...out);
}

// ---- the search -------------------------------------------------------------------------
// searchBox is the top bar's one search (one search box answers
// 'what's happening with X?'; GET /api/search, internal/search): typed into, it
// asks the controller (debounced, an older answer dropped) and lists what it found by type,
// the best first. "/" focuses it; arrows and Enter, or a click, go to a result's place; Esc
// closes the list, then the box. On a phone it is an icon that opens it. Each result goes
// to its place as the pages read it: a node to its page (node.html#<key>; one found and not
// added opens there too, with its way to be set up), a zone or a record to its zone page
// (zones.html#zone=), a device or a site to the query log's filter (?client=, ?name=), a
// site to check or a rule to Blocking's Check a site (blocklists.html#check=), a list to
// Blocking's lists (#list=), a pending change to the changes panel.
function searchBox() {
  const GROUPS = { node: "Nodes", zone: "Zones", record: "Records", device: "Devices", site: "Sites asked", check: "Check a site",
    rule: "Allowed and blocked sites", list: "Blocklists", change: "Changes to apply" };
  const PAGEFILE = { nodes: "./", zones: "zones.html", blocking: "blocklists.html", "query-log": "querylog.html" };
  const e = encodeURIComponent;
  const where = r => {
    const k = str(r.key), z = str(r.zone);
    switch (r.type) {
      case "node": return `node.html#${e(k)}`;
      case "zone": return `zones.html#zone=${e(k)}`;
      case "record": return z ? `zones.html#zone=${e(z)}` : "zones.html";
      case "device": return `querylog.html?client=${e(k)}`;
      case "site": return `querylog.html?name=${e(k)}`;
      case "check": return `blocklists.html#check=${e(k)}`;
      case "rule": return `blocklists.html#check=${e(k)}`;
      case "list": return `blocklists.html#list=${e(k)}`;
      case "change": return null; // the changes panel
    }
    return Object.hasOwn(PAGEFILE, r.page) ? PAGEFILE[r.page] : null;
  };
  const glass = () => {
    const s = document.createElementNS(SVG, "svg");
    for (const [k, v] of Object.entries({ width: 14, height: 14, viewBox: "0 0 16 16", fill: "none", "aria-hidden": "true" })) s.setAttribute(k, v);
    for (const [tag, at] of [["circle", { cx: 7, cy: 7, r: 5 }], ["path", { d: "M11 11l3.5 3.5", "stroke-linecap": "round" }]]) {
      const p = document.createElementNS(SVG, tag);
      for (const [k, v] of Object.entries({ ...at, stroke: "currentColor", "stroke-width": 1.6 })) p.setAttribute(k, v);
      s.append(p);
    }
    return s;
  };

  const input = h("input", { id: "q", type: "search", placeholder: "Search a site, device or node", "aria-label": "Search a site, device or node",
    autocomplete: "off", spellcheck: "false", maxlength: "253", role: "combobox", "aria-expanded": "false", "aria-controls": "q-res",
    "aria-autocomplete": "list", "aria-keyshortcuts": "/", title: "Search (/)" });
  const list = h("div", { class: "search-res", id: "q-res", role: "listbox", "aria-label": "Search results", hidden: true });
  const opener = h("button", { class: "search-btn", type: "button", "aria-label": "Search", "aria-expanded": "false", title: "Search (/)" }, glass());
  const box = h("div", { class: "search", role: "search" }, opener, h("div", { class: "search-box" }, glass(), input), list);

  let timer = null, seq = 0, ctl = null, opts = [], active = -1;
  const show = on => {
    list.hidden = !on;
    input.setAttribute("aria-expanded", on ? "true" : "false");
    if (!on) setActive(-1);
  };
  const expand = on => {
    box.classList.toggle("open", on);
    opener.setAttribute("aria-expanded", on ? "true" : "false");
  };
  function setActive(i) {
    if (opts[active]) { opts[active].classList.remove("on"); opts[active].setAttribute("aria-selected", "false"); }
    active = i;
    const o = opts[i];
    if (o) {
      o.classList.add("on");
      o.setAttribute("aria-selected", "true");
      input.setAttribute("aria-activedescendant", o.id);
      o.scrollIntoView({ block: "nearest" });
    } else input.removeAttribute("aria-activedescendant");
  }
  const note = (text, cls) => {
    opts = [];
    active = -1;
    list.replaceChildren(h("div", { class: `search-note${cls ? ` ${cls}` : ""}`, role: "status" }, text));
    show(true);
  };
  const clear = () => { opts = []; active = -1; list.replaceChildren(); show(false); };

  // go goes to a result's place: the changes panel for a change; a page already open is
  // loaded again, since the pages read their choice when they load.
  function go(r) {
    const to = where(r);
    show(false);
    input.blur();
    expand(false);
    if (r.type === "change") { openReview(); return; }
    if (!to) return;
    const u = new URL(to, location.href);
    const same = u.pathname === location.pathname && u.search === location.search;
    location.href = u.href;
    // The same page: it reads its choice again (a hash alone isn't a load).
    if (same) location.reload();
  }

  function render(q, a) {
    const rs = arr(a.results), matched = obj(a.matched), out = [];
    opts = [];
    active = -1;
    if (!rs.length) out.push(h("div", { class: "search-note", role: "status" }, `Nothing matches "${q}".`));
    // By type, the type of the best result first.
    const by = new Map();
    for (const r of rs) {
      const t = str(r.type);
      if (!by.has(t)) by.set(t, []);
      by.get(t).push(r);
    }
    for (const [t, g] of by) {
      const n = Number(matched[t]) || g.length, gid = `q-g-${t.replace(/[^a-z]/g, "")}`;
      const items = g.map(r => {
        const to = where(r);
        const o = h(to ? "a" : "div", { class: "search-opt", id: `q-opt-${opts.length}`, role: "option", href: to, "aria-selected": "false" },
          h("span", { class: "t" }, str(r.title) || str(r.key), r.pending && r.type !== "change" ? h("span", { class: "tag" }, "waiting to apply") : null),
          str(r.text) ? h("span", { class: "d" }, str(r.text)) : null);
        o.addEventListener("mousedown", ev => ev.preventDefault()); // the box keeps the focus
        o.addEventListener("mousemove", () => { const i = opts.indexOf(o); if (i !== active) setActive(i); });
        o.addEventListener("click", ev => {
          if (ev.ctrlKey || ev.metaKey || ev.shiftKey || ev.button) return; // a new tab or window: the browser's
          ev.preventDefault();
          go(r);
        });
        opts.push(o);
        return o;
      });
      out.push(h("div", { class: "search-grp", role: "group", "aria-labelledby": gid },
        h("div", { class: "search-gh", id: gid }, h("span", {}, Object.hasOwn(GROUPS, t) ? GROUPS[t] : t), n > g.length ? h("span", {}, `${g.length} of ${n}`) : null),
        items));
    }
    const foot = [];
    if (a.more) foot.push("Only the best matches are shown: type more to narrow it.");
    if (a.partial) foot.push("The search ran out of time: not every place was searched.");
    for (const p of arr(a.problems)) foot.push(h("div", { class: "bad" }, `Couldn't search ${str(p)}`));
    if (foot.length) out.push(h("div", { class: "search-foot" }, foot.map(x => (typeof x === "string" ? h("div", {}, x) : x))));
    list.replaceChildren(...out);
    show(true);
  }

  async function run(q, mine) {
    if (ctl) ctl.abort();
    ctl = new AbortController();
    try {
      const r = await api(`api/search?q=${e(q)}`, { signal: ctl.signal });
      let j = null;
      try { j = await r.json(); } catch { j = null; }
      if (mine !== seq) return;
      if (r.status === 401) note("Your login has ended: log in again to search.", "bad");
      else if (!r.ok || !j) note(str(obj(j).error) || `The search didn't answer (${r.status}).`, "bad");
      else render(q, j);
    } catch (err) {
      if (mine !== seq || (err && err.name === "AbortError")) return;
      note("The controller isn't answering.", "bad");
    }
  }

  input.addEventListener("input", () => {
    clearTimeout(timer);
    const q = input.value.trim(), mine = ++seq;
    if (!q) { if (ctl) ctl.abort(); clear(); return; }
    timer = setTimeout(() => {
      if (!opts.length) note("Searching…");
      run(q, mine);
    }, 200);
  });
  input.addEventListener("focus", () => { if (input.value.trim() && list.childNodes.length) show(true); });
  input.addEventListener("keydown", ev => {
    if (ev.key === "ArrowDown" || ev.key === "ArrowUp") {
      ev.preventDefault();
      if (list.hidden && list.childNodes.length) show(true);
      if (!opts.length) return;
      const d = ev.key === "ArrowDown" ? 1 : -1;
      setActive(active < 0 ? (d > 0 ? 0 : opts.length - 1) : (active + d + opts.length) % opts.length);
    } else if (ev.key === "Enter") {
      ev.preventDefault();
      if (opts.length && !list.hidden) opts[active < 0 ? 0 : active].click();
    } else if (ev.key === "Escape") {
      ev.preventDefault();
      ev.stopPropagation();
      if (!list.hidden) { show(false); return; }
      clearTimeout(timer);
      seq++;
      input.value = "";
      clear();
      input.blur();
      expand(false);
    }
  });
  opener.addEventListener("click", () => { expand(true); input.focus(); });
  box.addEventListener("focusout", ev => {
    if (box.contains(ev.relatedTarget)) return;
    show(false);
    if (!input.value.trim()) expand(false);
  });
  document.addEventListener("keydown", ev => {
    if (ev.key !== "/" || ev.ctrlKey || ev.metaKey || ev.altKey || openOne) return;
    const t = ev.target;
    if (t && (t.isContentEditable || ["INPUT", "TEXTAREA", "SELECT"].includes(t.tagName))) return;
    ev.preventDefault();
    expand(true);
    input.focus();
    input.select();
  });
  return box;
}

// ---- the shell --------------------------------------------------------------------------
// shell draws the top bar for the page current (one of PAGES), the band and the skip link,
// and starts reading the nodes and the pending changes every 5 s.
export function shell(current) {
  const nav = h("nav", { class: "main", "aria-label": "Main" },
    PAGES.map(([href, name]) => h("a", { href: href === "index.html" ? "./" : href, "aria-current": href === current ? "page" : null }, name)));
  changesBox = h("div", { class: "changes" });
  const who = h("div", { class: "who" });
  const top = h("header", { class: "top" }, h("div", { class: "top-in" }, h("a", { class: "brand", href: "./" }, "espDNS"), nav, searchBox(), changesBox, who, topArtie()));
  bandBox = h("div", { class: "bands" });
  const main = document.querySelector("main");
  if (main) { if (!main.id) main.id = "content"; main.tabIndex = -1; }
  document.body.prepend(h("a", { class: "skip", href: `#${main ? main.id : "content"}` }, "Skip to the page"), top, bandBox);

  session.then(s => {
    canWrite = !!s.logged_in;
    if (s.logged_in) {
      who.append(h("button", { class: "chip-btn", type: "button", "aria-label": `Account: ${s.user}`, onclick: () => openAccount(s.user) }, `${s.user} ▾`));
    } else if (!s.password_set) {
      who.append(h("span", { class: "chip-btn" }, "read-only"));
      // The setup page sets the password itself: no band there.
      if (current !== "setup.html") {
        sessBand = h("div", { class: "band warn" }, h("div", { class: "band-in" }, h("strong", {}, "Read-only."),
          h("span", {}, "No password is set, so the controller takes no actions. ", h("a", { href: "setup.html" }, "Set one"), ", or run ", h("code", {}, "espdns passwd"), " (docs/developer.md).")));
      }
    } else if (s.error) {
      sessBand = h("div", { class: "band bad" }, h("div", { class: "band-in" }, h("strong", {}, "The login can't be used."), h("span", {}, str(s.error))));
    }
    renderBand(null);
    renderChip();
    if (s.logged_in) every(refreshChanges);
  });
  onNodes((ns, st) => { fleetNodes = ns; renderBand(st.error); renderChip(); });
  watchNodes();
}

// The account panel (account.js): the password, keeping the controller safe (the release
// key, a backup), the recent changes and logging out; loaded when first opened. open names a
// row to open at once ("safe").
function openAccount(user, open) {
  import("./account.js").then(m => m.openAccount(user, open)).catch(e => toast(`Couldn't open the account: ${e.message}`));
}
export function openAccountPanel(open) {
  session.then(s => { if (s.logged_in) openAccount(s.user, open); else toast("Log in first"); });
}

export { api, getJSON, post, session, events, el, nodeText, ended, answering, wired, inService };

// topArtie is the mascot, small, at the top bar's far end, opposite the brand.
function topArtie() {
  return h("img", { class: "top-artie", src: "img/artie.webp", width: 14, height: 24, alt: "", title: "Artie A. Record" });
}
