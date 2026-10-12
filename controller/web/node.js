// The node page (node.html#<node ID or address>),
// opened from a node on Nodes: its name, state (the LED) and why, its address
// and MAC to copy, its board, link, uptime and firmware version; its actions (Blink its
// light, Restart, Update firmware), each a job; what is wrong explained in words, with what
// to do; its settings as hide-and-expand rows, saved as a pending change (POST
// /api/nodes/{id}/settings) for the header's Apply; what it serves (zones, blocking, memory);
// the last hour; its recent changes; and, under Details, the rest of its /status, the cache
// flush and the way back to an older list, its settings as it runs them and the terminal's
// commands. Read from /api/nodes every 5 s, /api/dashboard every 10 s, /api/updates every
// 30 s; its settings when it opens and after a change.
import { shell, h, arr, obj, str, hostOnly, led, addr, mac, toast, panel, refreshChanges, onChanges,
  api, getJSON, post, session, events, nodeText, ended, answering } from "./shell.js";
import { onNodes, every, releaseKey } from "./common.js";
import { stateOf, reason, upFor, linkWord, readBoards, mini, fmtNum, pct, andList, sentenceCase, sinceWords } from "./nodeview.js";
import { openUpdate } from "./update.js";

shell("index.html");

const $ = id => document.getElementById(id);
const data = { nodes: null, sess: {}, sessRead: false, key: null, updates: null, dash: null, actions: null, boards: new Map(),
  form: null, formErr: "", formWrite: null, formFor: "", identifyUntil: 0 };
const ref = () => { try { return decodeURIComponent(location.hash.slice(1)); } catch { return ""; } };
const has = v => v !== undefined && v !== null && v !== "";

// The node this page is about: by its ID, else its address (with or without the port).
function theNode() {
  const r = ref().toLowerCase();
  if (!r || !data.nodes) return null;
  return data.nodes.find(n => str(n.id).toLowerCase() === r) || data.nodes.find(n => str(n.addr) === r || hostOnly(n.addr) === r) || null;
}
const listedNodes = () => arr(data.nodes).filter(n => n.source === "settings");
const boardName = b => data.boards.get(str(b)) || str(b);
const nameOf = n => nodeText(n);
const ip = n => hostOnly(n.addr);

// updateOf is the node's line of /api/updates, if any.
const updateOf = n => arr(obj(data.updates).nodes).find(u => u.host === n.addr) || null;
const offered = n => { const u = updateOf(n); return u && u.state === "available" && u.listed && !u.out_of_service && !u.pending ? u : null; };

// ---- why an action can't be taken ------------------------------------------------------
// actionWhy is why the node's actions are off ("" when they can run); identify is the only
// one for a node not in settings.json.
function actionWhy(n, identify) {
  if (!data.sess.logged_in) return data.sess.password_set ? "Log in to act on this node." : "No password is set: the controller is read-only.";
  if (data.key && !data.key.present) return data.key.missing ? "No release key: import or create one from the account menu." : `The release key can't be used: ${str(data.key.error)}`;
  if (!n.online && n.out_of_service) return "It isn't answering: return it to service once it does, or remove it.";
  if (!n.online) return n.source === "settings" ? "It isn't answering: it can still be taken out of service, or removed." : "It isn't answering: no actions until it does.";
  if (!identify && n.source !== "settings") return "It isn't one of your nodes yet: add it from Nodes first.";
  return "";
}

// serviceWhy is why taking it out of service, back, or removing it is off ("" when it can
// run): they change settings.json, so a node offline is no matter; returning it signs with
// the release key.
function serviceWhy(n, back) {
  if (!data.sess.logged_in) return data.sess.password_set ? "Log in to act on this node." : "No password is set: the controller is read-only.";
  if (n.source !== "settings") return "It isn't one of your nodes yet: add it from Nodes first.";
  if (back && data.key && !data.key.present) return data.key.missing ? "No release key: import or create one from the account menu." : `The release key can't be used: ${str(data.key.error)}`;
  return "";
}

// ---- jobs ------------------------------------------------------------------------------
// runJob starts a job on this node and follows it: onLog hears each line, onEnd the job at
// its end. It returns the job, or throws why it wasn't started.
async function runJob(kind, params, { onLog, onEnd } = {}) {
  const j = await post("api/jobs", { kind, params });
  const es = events(`api/jobs/${encodeURIComponent(j.id)}/events`);
  es.addEventListener("log", e => { try { if (onLog) onLog(str(JSON.parse(e.data).text)); } catch { /* a bad line */ } });
  es.addEventListener("end", e => { es.close(); if (onEnd) onEnd(JSON.parse(e.data)); });
  es.addEventListener("gone", () => { if (onEnd) onEnd({ ...j, state: "failed", error: "The controller no longer has this job: it may have restarted." }); });
  return j;
}

function identify(n) {
  const secs = 30;
  data.identifyUntil = Date.now() + secs * 1000;
  renderHead();
  toast(`${nameOf(n)}'s light flickers white for ${secs} seconds, here and on the board`);
  setTimeout(renderHead, secs * 1000 + 50);
  runJob("identify", { node: n.addr, seconds: secs }, {
    onEnd: j => { if (j.state !== "done") { data.identifyUntil = 0; renderHead(); toast(`Couldn't blink its light: ${str(j.error) || j.state}`); } },
  }).catch(e => { data.identifyUntil = 0; renderHead(); toast(`Couldn't blink its light: ${e.message}`); });
}

// The Restart panel: what happens in words, then the coordinated restart as it goes.
function openRestart(n) {
  const name = nameOf(n);
  const others = listedNodes().filter(x => x.addr !== n.addr && answering(x));
  const st = obj(n.status), rb = obj(st.reboot);
  const p = panel(`Restart ${name}?`);
  const say = h("p", {}, others.length
    ? `${name} stops answering for a few seconds while it restarts; ${andList(others.map(nameOf))} keep${others.length === 1 ? "s" : ""} answering meanwhile.`
    : `No other node is answering: the controller restarts ${name} only while another of your DNS servers answers.`);
  const waits = rb.pending === true ? h("p", { class: "small" }, `A change waits for this restart (${arr(rb.reasons).filter(x => typeof x === "string").join(", ") || "a change it took"}).`) : null;
  const how = h("p", { class: "hint" }, "The controller waits until it answers again.");
  const line = h("p", { class: "hint", role: "status" });
  const msg = h("p", { class: "hint bad", role: "status" });
  p.body.append(...[say, waits, how, line, msg].filter(Boolean));
  const go = h("button", { class: "btn primary", type: "button" }, "Restart");
  const cancel = h("button", { class: "btn", type: "button", onclick: p.close }, "Cancel");
  go.addEventListener("click", async () => {
    go.disabled = true;
    msg.textContent = "";
    p.title(`Restarting ${name}`);
    line.textContent = "Starting…";
    try {
      await runJob("reboot", { node: n.addr }, {
        onLog: t => { line.textContent = t; },
        onEnd: j => {
          if (j.state === "done") {
            p.title(`${name} restarted`);
            line.textContent = `${name} is back and answering.`;
            toast(`${name} restarted and is answering`);
          } else {
            p.title(`${name} wasn't restarted`);
            line.textContent = "";
            msg.textContent = str(j.error) || `The restart ${j.state}.`;
          }
          p.footer.replaceChildren(h("button", { class: "btn primary", type: "button", onclick: p.close }, "Done"));
        },
      });
      cancel.textContent = "Close";
      if (go.isConnected) go.remove();
    } catch (e) {
      msg.textContent = e.message;
      p.title(`Restart ${name}?`);
      line.textContent = "";
      go.disabled = false;
    }
  });
  p.footer.append(cancel, go);
}

// ---- out of service, back, removed -------------------------------------------------------
// Each a confirm, then its job (node-out, node-in, node-remove: service.go), followed here.

// confirmJob is a panel that says what happens, then runs the job and follows it: its lines
// as they come, its end. done(j) is said once it ends well.
function confirmJob(title, body, label, kind, n, { danger, working, done } = {}) {
  const p = panel(title);
  const line = h("p", { class: "hint", role: "status" });
  const msg = h("p", { class: "hint bad", role: "status" });
  p.body.append(...body, line, msg);
  const go = h("button", { class: danger ? "btn danger" : "btn primary", type: "button" }, label);
  const cancel = h("button", { class: "btn", type: "button", onclick: p.close }, "Cancel");
  go.addEventListener("click", async () => {
    go.disabled = true;
    msg.textContent = "";
    p.title(working);
    line.textContent = "Starting…";
    try {
      await runJob(kind, { node: n.addr }, {
        onLog: t => { line.textContent = t; },
        onEnd: j => {
          if (j.state === "done") { line.textContent = ""; done(j, p, line); }
          else { p.title(title); line.textContent = ""; msg.textContent = str(j.error) || `It ${j.state}.`; }
          p.footer.replaceChildren(h("button", { class: "btn primary", type: "button", onclick: p.close }, "Done"));
        },
      });
      cancel.textContent = "Close";
      if (go.isConnected) go.remove();
    } catch (e) {
      msg.textContent = e.message;
      p.title(title);
      line.textContent = "";
      go.disabled = false;
    }
  });
  p.footer.append(cancel, go);
}

function openOut(n) {
  const name = nameOf(n);
  const others = listedNodes().filter(x => x.addr !== n.addr && !x.out_of_service);
  confirmJob(`Take ${name} out of service?`, [
    h("p", {}, `${name} stays on your list but gets no changes, and isn't counted while ${others.length ? andList(others.map(nameOf)) : "your other nodes"} change${others.length === 1 ? "s" : ""}. For a node that's off, broken or away.`),
    h("p", { class: "hint" }, "If it's up, it still answers DNS. When it's back, return it to service: it's brought up to date first."),
  ], "Take out of service", "node-out", n, {
    working: `Taking ${name} out of service`,
    done: (j, p, line) => { p.title(`${name} is out of service`); line.textContent = "Changes go to your other nodes; this one is skipped."; toast(`${name} is out of service`); readUpdates(); },
  });
}

function openIn(n) {
  const name = nameOf(n);
  confirmJob(`Return ${name} to service?`, [
    h("p", {}, `The controller checks ${name} answers and trusts your release key, then sends it what it missed: settings, hosted zones, allowed and blocked sites, its blocklist, and newer firmware if it's behind. Each is checked first, like an apply.`),
    h("p", { class: "hint" }, "A firmware update restarts it; the other nodes keep answering. Once it's up to date, it counts again."),
  ], "Return to service", "node-in", n, {
    working: `Bringing ${name} up to date`,
    done: (j, p, line) => { p.title(`${name} is back in service`); line.textContent = `${name} is up to date and gets every change from now on.`; toast(`${name} is back in service`); readUpdates(); },
  });
}

function openRemove(n) {
  const name = nameOf(n);
  confirmJob(`Remove ${name} from the group?`, [
    h("p", {}, `${name} comes off your list. Its settings file is kept and the node itself isn't touched.`),
    h("p", { class: "small" }, `If it's up, it keeps answering DNS: take ${ip(n)} off your router's DNS servers.`),
    h("p", { class: "hint" }, "While it's on your network it shows under New device found, and can be added back."),
  ], "Remove", "node-remove", n, {
    danger: true,
    working: `Removing ${name}`,
    done: (j, p, line) => { p.title(`${name} was removed`); line.textContent = ""; toast(`${name} was removed from the group`); setTimeout(() => { location.href = "./"; }, 1200); },
  });
}

// A quick action from Details: flush the cache, or go back to the older list it keeps.
const LISTS = { blocklist: "blocklist", overrides: "allowed and blocked sites", zones: "hosted zones" };
function openRevert(n, list) {
  const name = nameOf(n), what = LISTS[list];
  const p = panel(`Go back to the older ${what}?`);
  const msg = h("p", { class: "hint bad", role: "status" });
  p.body.append(h("p", {}, `${name} goes back to the copy of its ${what} from before the last change, and keeps it until a newer one is applied.`),
    h("p", { class: "hint" }, list === "zones" ? "Applies live." : "Applies live, unless both copies don't fit its memory: then at its next restart."), msg);
  const go = h("button", { class: "btn primary", type: "button" }, "Go back");
  go.addEventListener("click", async () => {
    go.disabled = true;
    msg.textContent = "";
    try {
      await runJob("revert", { node: n.addr, list }, {
        onEnd: j => toast(j.state === "done" ? `${name} went back to its older ${what}` : `${name} didn't go back: ${str(j.error) || j.state}`),
      });
      p.close();
    } catch (e) { msg.textContent = e.message; go.disabled = false; }
  });
  p.footer.append(h("button", { class: "btn", type: "button", onclick: p.close }, "Cancel"), go);
}

async function flush(n, btn) {
  btn.disabled = true;
  try {
    await runJob("flush", { node: n.addr }, {
      onEnd: j => { btn.disabled = false; toast(j.state === "done" ? `${nameOf(n)}'s cache is empty` : `Couldn't flush the cache: ${str(j.error) || j.state}`); },
    });
  } catch (e) { btn.disabled = false; toast(`Couldn't flush the cache: ${e.message}`); }
}

// ---- the head --------------------------------------------------------------------------

function renderHead() {
  const n = theNode();
  const name = $("name"), sum = $("summary"), why = $("why"), acts = $("acts"), off = $("acts-why");
  if (!n) {
    if (data.nodes === null) return;
    // A node not read yet (just listed, or moved to a new address) may be the one.
    if (ref() && data.nodes.some(x => !x.id && x.online !== false)) { name.textContent = "Reading the node…"; return; }
    document.title = "espDNS node";
    name.textContent = ref() ? "No such node" : "No node chosen";
    sum.replaceChildren(h("span", { class: "muted" }, ref() ? `${ref()} isn't one of your nodes, or it was removed.` : "Open a node from Nodes."));
    why.hidden = true;
    acts.replaceChildren();
    off.hidden = true;
    for (const id of ["attention", "settings-sec", "serves-sec", "hour", "recent", "details"]) $(id).hidden = true;
    return;
  }
  const s = obj(n.status), net = obj(s.net);
  const st = stateOf(n);
  const ident = Date.now() < data.identifyUntil;
  document.title = `${nameOf(n)} · espDNS`;
  name.textContent = nameOf(n);
  const facts = [boardName(s.board), linkWord(net.kind), n.online && upFor(s.uptime_s) ? `up ${upFor(s.uptime_s)}` : "",
    has(s.version) ? `firmware ${s.version}` : ""].filter(Boolean).join(" · ");
  sum.replaceChildren(...[
    h("span", { class: "state big-state" }, led(ident ? "identify" : st.led), ident ? "Blinking its light" : st.word),
    addr(ip(n)),
    mac(n.mac || net.mac),
    facts ? h("span", { class: "muted small" }, facts) : null].filter(Boolean));
  const u = updateOf(n);
  const upd = u && u.pending ? `Update waiting to apply: ${str(obj(u.runs).version)} → ${str(obj(u.newest).version)}`
    : offered(n) ? `Update available: ${str(obj(u.runs).version)} → ${str(obj(u.newest).version)}` : "";
  const line = [st.why, upd].filter(Boolean).join(" · ");
  why.textContent = line;
  why.className = st.tint ? "" : "muted";
  why.hidden = !line;

  const no = actionWhy(n, false), noId = actionWhy(n, true);
  const blink = h("button", { class: "btn", type: "button", disabled: !!noId || ident, onclick: () => identify(n) }, "Blink its light");
  const restart = h("button", { class: "btn", type: "button", disabled: !!no, onclick: () => openRestart(n) }, "Restart");
  const update = offered(n) ? h("button", { class: "btn", type: "button", disabled: !data.sess.logged_in,
    onclick: () => openUpdate({ updates: data.updates, listed: listedNodes(), canWrite: !!data.sess.logged_in, only: n.addr, done: readUpdates }) }, "Update firmware") : null;
  // Out of service and back, removed: for one of your nodes, offline too.
  let svc = null, rm = null;
  if (n.source === "settings") {
    const noSvc = serviceWhy(n, !!n.out_of_service);
    svc = n.out_of_service
      ? h("button", { class: "btn", type: "button", id: "svc-in", disabled: !!noSvc, onclick: () => openIn(n) }, "Return to service")
      : h("button", { class: "btn", type: "button", id: "svc-out", disabled: !!noSvc, onclick: () => openOut(n) }, "Take out of service");
    rm = h("button", { class: "btn danger", type: "button", id: "svc-remove", disabled: !!serviceWhy(n, false), onclick: () => openRemove(n) }, "Remove from the group");
  }
  acts.replaceChildren(...[blink, restart, update, svc, rm].filter(Boolean));
  off.textContent = no || noId;
  // No release key: where it is imported, a click away.
  if (data.key && !data.key.present && (no || noId)) off.append(" ", h("a", { href: "setup.html#next" }, "Set up ›"));
  off.hidden = !(no || noId);
}

// ---- what's wrong, in words --------------------------------------------------------------

function renderAttention() {
  const n = theNode(), box = $("attention");
  if (!n) return;
  const s = obj(n.status), hh = obj(s.health), rb = obj(s.reboot), name = nameOf(n);
  const st = stateOf(n);
  const out = [];
  let cls = "panel pad stack";
  if (n.out_of_service) {
    // Out of service: said plainly, never an alert.
    out.push(h("b", {}, `${name} is out of service.`),
      h("p", {}, "It gets no changes and isn't counted while your other nodes change."),
      h("p", { class: "hint" }, n.online ? "It still answers DNS." : n.status ? `It isn't answering: no reply for ${sinceWords(n.last_seen)}.` : "It hasn't answered since the controller started."),
      h("p", {}, "Return it to service when it's ready: it's brought up to date first."));
  } else if (!n.online && n.source !== "settings") {
    out.push(h("b", {}, `${name} isn't one of your nodes, and isn't answering.`), h("p", {}, "It was found on your network. Once it answers again, set it up from Nodes."),
      h("div", { class: "row" }, h("a", { class: "btn", href: "./" }, "Nodes")));
  } else if (!n.online) {
    cls += " alert";
    out.push(h("b", {}, n.status ? `${name} isn't answering: no reply for ${sinceWords(n.last_seen)}.` : `${name} hasn't answered since the controller started.`),
      h("p", {}, "Check its power and its cable or Wi-Fi. This page shows its last reply."),
      n.error ? h("p", { class: "hint" }, `The controller's last try: ${str(n.error)}`) : null);
  } else {
    const reasons = arr(hh.reasons).filter(r => typeof r === "string");
    const bad = reasons.filter(r => reason(r)[0] !== "info"), info = reasons.filter(r => reason(r)[0] === "info");
    if (st.tint || bad.length) {
      cls += " alert";
      out.push(h("b", {}, `${name} is ${st.word.toLowerCase()}${bad.length ? `: ${bad.join(", ")}` : ""}.`));
      // One reason: the heading named it already.
      for (const r of bad) out.push(h("p", {}, bad.length > 1 ? h("b", {}, `${sentenceCase(r)}. `) : null, reason(r)[1]));
      // A zone's trouble is fixed on Zones (its primary, its copy lists): a click away.
      if (bad.some(r => /zone/i.test(r))) out.push(h("div", { class: "row" }, h("a", { class: "btn", href: "zones.html" }, "Open Zones")));
      if (st.tint === "warn" && hh.answering !== false) out.push(h("p", { class: "hint" }, `${name} still answers DNS queries meanwhile.`));
      if (s.degraded && !hh.state) out.push(h("p", {}, reason("degraded")[1]));
    }
    for (const r of info) out.push(h("p", {}, h("b", {}, `${sentenceCase(r)}. `), reason(r)[1]));
    if (rb.pending === true) {
      const no = actionWhy(n, false);
      if (!info.includes("reboot pending")) out.push(h("p", {}, h("b", {}, "Reboot pending. "), reason("reboot pending")[1]));
      out.push(h("div", { class: "row" }, h("button", { class: "btn primary", type: "button", disabled: !!no, onclick: () => openRestart(n) }, "Restart now"),
        h("span", { class: "hint" }, `Waiting for: ${arr(rb.reasons).filter(x => typeof x === "string").join(", ") || "a change it took"}.`)));
    }
    // Firmware from before health states ("Old firmware: update it"): the update a click
    // away, or why there is none.
    if (!hh.state && n.source === "settings") {
      const u = offered(n);
      out.push(h("p", {}, h("b", {}, "Old firmware. "), "It doesn't report its health in detail or follow espDNS's light patterns."),
        u ? h("div", { class: "row" }, h("button", { class: "btn primary", type: "button", disabled: !data.sess.logged_in,
          onclick: () => openUpdate({ updates: data.updates, listed: listedNodes(), canWrite: !!data.sess.logged_in, only: n.addr, done: readUpdates }) }, "Update firmware"))
          : h("p", { class: "hint" }, str(obj(updateOf(n)).why) || "No newer firmware for it here yet: Get firmware from the account menu, or import a build of your own (espdns release import)."));
    }
    if (n.source !== "settings") {
      out.push(h("b", {}, `${name} isn't one of your nodes yet.`), h("p", {}, "Found on your network. Add it to change its settings and send it changes."),
        h("div", { class: "row" }, h("a", { class: "btn primary", href: `./#found=${encodeURIComponent(ip(n))}` }, "Set it up")));
    }
  }
  box.className = cls;
  box.replaceChildren(...out.filter(Boolean));
  box.hidden = !out.length;
}

// ---- the settings form ---------------------------------------------------------------------

const QL = [["full", "Its address"], ["subnet", "Its network only (/24)"], ["hidden", "Nothing"]];
const qlWords = q => (!q.enabled ? "Off" : q.client === "subnet" ? "Keeps client networks" : q.client === "hidden" ? "Keeps no client" : "Keeps client addresses");
const appliesHint = a => (a === "restart" ? "Changing it restarts this node while another answers." : "Applies live.");

async function readSettings() {
  const n = theNode();
  if (!n || !data.sessRead) return;
  if (!data.sess.logged_in || !n.id) { renderSettings(); return; }
  data.formFor = n.id;
  if (!data.form) renderSettings();
  let form = null, err = "", write = null;
  try { form = await readForm(n.id); } catch (e) { err = e.message.replace(/^api\/[^ ]*: /, ""); write = e.write || null; }
  // Another node opened meanwhile, or a row being edited: this read is dropped.
  if (data.formFor !== n.id || dirty) return;
  data.form = form;
  data.formErr = err;
  data.formWrite = write;
  renderSettings();
  renderDetails();
}

// readForm is GET /api/nodes/{id}/settings; a refusal keeps the config files it names to
// write or correct (its "write": a node running a config this controller has no copy of,
// or one whose file doesn't read).
async function readForm(id) {
  const url = `api/nodes/${encodeURIComponent(id)}/settings`;
  const r = await api(url);
  if (r.status === 401) return getJSON(url); // to the login page
  let j = {};
  try { j = await r.json(); } catch { /* not JSON */ }
  if (!r.ok) throw Object.assign(new Error(str(j.error) || r.statusText), { write: Array.isArray(j.write) ? j.write : null });
  return j;
}

// writeConfig is a node's config file written or corrected here: its text (a draft from what
// the node reports, or the file as saved, its Wi-Fi password hidden and kept), saved with
// POST /api/configs/<file>; the settings are read again after.
function writeConfig(n, w) {
  const file = str(w.file);
  const area = h("textarea", { class: "t code cfgtext", spellcheck: "false", "aria-label": `${file}, as JSON` });
  const msg = h("p", { class: "hint", role: "status" });
  const save = h("button", { class: "btn primary", type: "button", disabled: true }, "Save");
  const box = h("div", { class: "stack" },
    h("p", { class: "small" }, w.draft
      ? "It starts from what the node reports: its name and address. Add what else its config sets (Wi-Fi, upstream DNS, zones), as the node runs it. Saving changes nothing on the node."
      : `${file} as saved. Saving changes nothing on the node.`),
    area, h("div", { class: "row" }, save, msg));
  let hash = "";
  (async () => {
    if (w.draft) { area.value = str(w.draft); save.disabled = false; return; }
    msg.textContent = "Reading it…";
    try { const c = await getJSON(`api/configs/${encodeURIComponent(file)}`); area.value = str(c.text); hash = str(c.hash); msg.textContent = ""; save.disabled = false; }
    catch (e) { msg.className = "hint bad"; msg.textContent = e.message; }
  })();
  save.addEventListener("click", async () => {
    save.disabled = true;
    msg.className = "hint";
    msg.textContent = "Saving…";
    try {
      await post(`api/configs/${encodeURIComponent(file)}`, { text: area.value, hash });
      toast(`${file} saved`);
      data.form = null; data.formErr = ""; data.formWrite = null;
      renderSettings();
      readSettings();
    } catch (e) { msg.className = "hint bad"; msg.textContent = e.message; save.disabled = false; }
  });
  return box;
}

// moveNode moves the node to another static address, on its own (an apply doesn't): its
// config file written (POST /api/nodes/{id}/move), then pushed by the config-push job, a dry
// run first; the node restarts onto the address and is confirmed there, then listed at it.
// When that doesn't go through, the file is put back as it was.
function moveNode(n, edit, version, to) {
  const name = nameOf(n);
  const others = listedNodes().filter(x => x.addr !== n.addr && answering(x));
  const p = panel(`Move ${name} to ${to}?`);
  const line = h("p", { class: "hint", role: "status" });
  const msg = h("p", { class: "hint bad", role: "status" });
  p.body.append(
    h("p", {}, `${name} restarts on ${to}${others.length ? `, while ${andList(others.map(nameOf))} keep${others.length === 1 ? "s" : ""} answering` : ""}. If it isn't reached there within a minute and a half, it goes back to ${ip(n)}.`),
    h("p", { class: "small" }, `In your router, replace ${ip(n)} with ${to} as a DNS server: devices still asking ${ip(n)} get no answer.`),
    line, msg);
  const go = h("button", { class: "btn primary", type: "button" }, "Move it");
  const cancel = h("button", { class: "btn", type: "button", onclick: p.close }, "Cancel");
  const job = (kind, params) => new Promise((resolve, reject) => {
    runJob(kind, params, { onLog: t => { line.textContent = t; }, onEnd: resolve }).catch(reject);
  });
  go.addEventListener("click", async () => {
    go.disabled = true;
    msg.textContent = "";
    p.title(`Moving ${name} to ${to}`);
    line.textContent = "Saving its new address…";
    let mv;
    try { mv = await post(`api/nodes/${encodeURIComponent(n.id)}/move`, { settings: edit, version }); }
    catch (e) { msg.textContent = e.message; p.title(`Move ${name} to ${to}?`); line.textContent = ""; go.disabled = false; return; }
    let err = "";
    try {
      const dry = await job("config-push", { node: mv.node, config: mv.file, dry_run: true });
      if (dry.state !== "done") err = str(dry.error) || `The check ${dry.state}.`;
      else {
        const j = await job("config-push", { node: mv.node, config: mv.file, after: dry.id });
        if (j.state !== "done") err = str(j.error) || `The move ${j.state}.`;
      }
    } catch (e) { err = e.message; }
    if (err) {
      try { await post(`api/nodes/${encodeURIComponent(n.id)}/move/undo`, { hash: mv.hash }); }
      catch (e) { err += ` (its config file wasn't put back: ${e.message})`; }
      p.title(`${name} wasn't moved`);
      line.textContent = "";
      msg.textContent = err;
    } else {
      p.title(`${name} moved`);
      line.textContent = `${name} now answers on ${to}, and is listed there.`;
      toast(`${name} moved to ${to}`);
    }
    cancel.remove();
    if (go.isConnected) go.remove();
    p.footer.replaceChildren(h("button", { class: "btn primary", type: "button", onclick: p.close }, "Done"));
    data.form = null; data.formErr = ""; data.formWrite = null; dirty = false;
    readSettings();
  });
  p.footer.append(cancel, go);
}

// dirty: a row's control was changed and not saved; the form isn't drawn again under it.
let dirty = false;

function renderSettings() {
  const n = theNode(), sec = $("settings-sec"), box = $("settings");
  if (!n) return;
  sec.hidden = false;
  if (!data.sess.logged_in) {
    box.replaceChildren(h("div", { class: "panel pad" }, h("p", { class: "muted" }, data.sess.password_set ? "Log in to see and change its settings." : "No password is set: the controller is read-only.")));
    return;
  }
  if (!n.id) { box.replaceChildren(h("div", { class: "panel pad" }, h("p", { class: "muted" }, "Its settings show once it has answered."))); return; }
  if (!data.form && data.formErr && arr(data.formWrite).length) {
    // Its config file to write, or to correct: here, a click away.
    const ws = arr(data.formWrite);
    box.replaceChildren(h("div", { class: "panel pad stack" }, h("p", { class: "bad" }, `Its settings can't be read: ${data.formErr}.`),
      ...ws.map(w => {
        const d = h("details", { class: "ex" }, h("summary", {}, h("b", {}, obj(w).draft ? "Write its config here" : `Correct ${str(obj(w).file)}`),
          h("span", { class: "val mono" }, str(obj(w).file))));
        let built = false;
        d.addEventListener("toggle", () => { if (d.open && !built) { built = true; d.append(h("div", { class: "exb" }, writeConfig(n, obj(w)))); } });
        if (ws.length === 1) { d.open = true; }
        return d;
      })));
    return;
  }
  if (!data.form) {
    box.replaceChildren(h("div", { class: "panel pad" }, h("p", { class: data.formErr ? "bad" : "muted" }, data.formErr ? `Its settings can't be read: ${data.formErr}` : "Reading its settings…")));
    return;
  }
  dirty = false;
  const r = data.form, pend = r.pending, f = obj(pend && pend.settings && pend.settings.name ? pend.settings : r.settings);
  const canEdit = r.node && r.node.listed;
  const errs = new Map();
  const errBox = field => { const p = h("p", { class: "hint bad", role: "status", hidden: true }); errs.set(field, p); return p; };
  const mark = () => { dirty = true; };
  const row = (field, label, val, ...body) => {
    const d = h("details", { class: "ex", "data-field": field }, h("summary", {}, h("b", {}, label), h("span", { class: "val" }, val)),
      h("div", { class: "exb" }, ...body, errBox(field)));
    return d;
  };

  // Name
  const nm = obj(f.name);
  const nameIn = h("input", { class: "t", id: "set-name", value: str(nm.value), autocomplete: "off", spellcheck: "false", oninput: mark });
  // Network
  const net = obj(f.network), nv = obj(net.value);
  const isDhcp = nv.address === "dhcp";
  const dhcpIn = h("input", { type: "checkbox", id: "set-dhcp", checked: isDhcp, onchange: () => { mark(); fixed.hidden = dhcpIn.checked; } });
  const addrIn = h("input", { class: "t mono", id: "set-addr", value: isDhcp ? "" : str(nv.address), placeholder: "a.b.c.d/24", autocomplete: "off", spellcheck: "false", oninput: mark });
  const gwIn = h("input", { class: "t mono", id: "set-gw", value: str(nv.gateway), placeholder: "a.b.c.d", autocomplete: "off", spellcheck: "false", oninput: mark });
  const fixed = h("div", { class: "grid2", hidden: isDhcp },
    h("label", { class: "f" }, h("span", {}, "Address"), addrIn, h("span", { class: "hint" }, "A fixed address, with its network size (/24).")),
    h("label", { class: "f" }, h("span", {}, "Gateway"), gwIn));
  const netVal = !net.value ? "Its board's" : isDhcp ? "From DHCP" : `${str(nv.address)}${nv.gateway ? ` · gateway ${nv.gateway}` : ""}`;
  // Upstream DNS
  const fw = obj(f.forwarders);
  const fwList = Array.isArray(fw.value) ? fw.value : null;
  const fwIn = h("input", { class: "t mono", id: "set-fwd", value: fwList ? fwList.join(", ") : "", placeholder: fwList ? "None: answers only from its zones and cache" : "Its board's", autocomplete: "off", spellcheck: "false", oninput: mark });
  const fwVal = fwList === null ? "Its board's" : fwList.length ? fwList.join(", ") : "None";
  // Time zone
  const tz = obj(f.tz), tzv = obj(tz.value);
  const zones = arr(r.time_zones);
  const tzSel = h("select", { class: "t", id: "set-tz", onchange: mark },
    h("option", { value: "", selected: !tzv.tz }, "Its board's"),
    tzv.tz && !zones.some(z => z.tz === tzv.tz && z.name === tzv.name) ? h("option", { value: str(tzv.tz), selected: true }, str(tzv.name) || str(tzv.tz)) : null,
    zones.map(z => h("option", { value: str(z.name), selected: !!tzv.tz && z.tz === tzv.tz && z.name === tzv.name }, str(z.name))));
  const tzVal = tzv.tz ? str(tzv.name) || str(tzv.tz) : "Its board's";
  // Blocking
  const bl = obj(f.blocking);
  const blSel = h("select", { class: "t", id: "set-blocking", onchange: mark },
    h("option", { value: "on", selected: bl.value !== false }, "On"), h("option", { value: "off", selected: bl.value === false }, "Off"));
  // Query log
  const ql = obj(f.querylog), qv = obj(ql.value);
  const qlSel = h("select", { class: "t", id: "set-querylog", onchange: mark },
    QL.map(([v, t]) => h("option", { value: v, selected: qv.enabled !== false && (qv.client || "full") === v }, t)),
    h("option", { value: "off", selected: qv.enabled === false }, "Keep no query log"));

  const rows = [
    row("name", "Name", str(nm.value) || "–",
      h("label", { class: "f" }, h("span", {}, "Name"), nameIn, h("span", { class: "hint" }, `Its name on every page. ${appliesHint(nm.applies)}`))),
    row("network", "Network", netVal,
      h("label", { class: "check" }, dhcpIn, h("span", {}, "Get its address from DHCP")), fixed,
      h("p", { class: "hint" }, "Changing the address restarts this node. Give your router the new address too.")),
    row("forwarders", "Upstream DNS", fwVal,
      h("label", { class: "f" }, h("span", {}, "Servers, in order"), fwIn,
        h("span", { class: "hint" }, `Where it sends names it can't answer itself, comma-separated. ${appliesHint(fw.applies)}`))),
    row("tz", "Time zone", tzVal,
      h("label", { class: "f" }, h("span", {}, "Time zone"), tzSel, h("span", { class: "hint" }, "For the times in its logs; the clock itself comes from the network."))),
    row("blocking", "Blocking", bl.value === false ? "Off on this node" : "On",
      h("label", { class: "f" }, h("span", {}, "Blocking on this node"), blSel,
        h("span", { class: "hint" }, bl.value === false ? `Off: ${nameOf(n)} blocks nothing, and its memory goes to the cache.` : "On: as set up on the Blocking page."))),
    row("querylog", "Query log", qlWords({ enabled: qv.enabled !== false, client: qv.client }),
      h("label", { class: "f" }, h("span", {}, "What to keep about each device"), qlSel, h("span", { class: "hint" }, appliesHint(ql.applies)))),
  ];
  // A field with a pending change: its row says so.
  for (const fld of arr(pend && pend.fields)) {
    const d = rows.find(x => x.dataset.field === fld);
    if (d) { d.classList.add("pending"); d.querySelector("summary .val").prepend(h("span", { class: "tag" }, "waiting"), " "); }
  }
  for (const c of [nameIn, dhcpIn, addrIn, gwIn, fwIn, tzSel, blSel, qlSel]) c.disabled = !canEdit;

  const msg = h("p", { class: "hint bad", role: "status" });
  const save = h("button", { class: "btn primary", type: "button", id: "set-save", disabled: !canEdit }, "Save changes");
  save.addEventListener("click", async () => {
    for (const p of errs.values()) { p.hidden = true; p.textContent = ""; p.className = "hint bad"; }
    msg.textContent = "";
    const edit = {};
    if (nameIn.value.trim() !== str(nm.value)) edit.name = nameIn.value.trim();
    const wantNet = dhcpIn.checked ? { address: "dhcp" } : { address: addrIn.value.trim(), gateway: gwIn.value.trim() };
    const hadNet = nv.address ? { address: str(nv.address), ...(isDhcp ? {} : { gateway: str(nv.gateway) }) } : null;
    if (!hadNet ? (dhcpIn.checked || addrIn.value.trim()) : JSON.stringify(wantNet) !== JSON.stringify(hadNet)) edit.network = wantNet;
    const fws = fwIn.value.split(/[\s,]+/).map(x => x.trim()).filter(Boolean);
    if (fwList === null ? fws.length : fws.join(",") !== fwList.join(",")) edit.forwarders = fws;
    const tzNow = tzv.tz ? (zones.some(z => z.tz === tzv.tz && z.name === tzv.name) ? str(tzv.name) : str(tzv.tz)) : "";
    if (tzSel.value !== tzNow) { if (tzSel.value) edit.tz = tzSel.value; else edit.reset = ["tz"]; }
    if ((blSel.value === "on") !== (bl.value !== false)) edit.blocking = blSel.value === "on";
    const qlNow = qv.enabled === false ? "off" : str(qv.client) || "full";
    if (qlSel.value !== qlNow) edit.querylog = qlSel.value === "off" ? { enabled: false } : { enabled: true, client: qlSel.value };
    if (!Object.keys(edit).length) { msg.textContent = "Nothing changed: open a row first."; return; }
    save.disabled = true;
    try {
      data.form = await post(`api/nodes/${encodeURIComponent(n.id)}/settings`, { settings: edit, version: str(r.version) });
      data.formErr = "";
      const p = data.form.pending;
      toast(`Added to changes to apply: ${p && arr(p.changes).length ? str(arr(p.changes).at(-1).summary) : `${nameOf(n)}'s settings`}`);
      await refreshChanges();
      renderSettings();
    } catch (e) {
      save.disabled = false;
      const m = e.message;
      // A move: not a change to apply, but made on its own (moveNode), a click away.
      if (e.body && e.body.move && edit.network) {
        const p = errs.get("network");
        const only = { network: edit.network };
        const rest = Object.keys(edit).filter(k => k !== "network");
        p.replaceChildren(m, ". ", rest.length ? "Save the other changes separately. " : "",
          h("button", { class: "btn small", type: "button", onclick: () => moveNode(n, only, str(r.version), str(e.body.move)) }, `Move it to ${str(e.body.move)}`));
        p.className = "hint";
        p.hidden = false;
        p.closest("details").open = true;
        return;
      }
      // A refusal names its field (name:, network:, ...): shown in that row, opened.
      const fld = ["name", "network", "forwarders", "tz", "blocking", "querylog"].find(x => m.startsWith(`${x}:`) || m.startsWith(`${x}.`))
        || (/address|gateway|moves/.test(m) ? "network" : /forwarder/.test(m) ? "forwarders" : "");
      const p = errs.get(fld);
      if (p) { p.className = "hint bad"; p.textContent = m; p.hidden = false; p.closest("details").open = true; } else msg.textContent = m;
    }
  });

  const note = [];
  if (!canEdit) note.push(h("p", { class: "hint" }, `${nameOf(n)} isn't one of your nodes yet: add it from Nodes to change its settings.`));
  if (pend) {
    note.push(h("div", { class: "panel pad pending-note" },
      h("b", {}, "Waiting in “changes to apply”: "), arr(pend.changes).map(c => str(c.summary)).join("; "),
      h("div", { class: "hint" }, str(pend.effect) || "")));
  }
  box.replaceChildren(...note, h("div", { class: "exs" }, rows), h("div", { class: "row save-row" }, save, msg));
}

// ---- what it serves ----------------------------------------------------------------------

function meter(used, total) {
  const m = h("div", { class: "meter" }, h("i", {}));
  const p = typeof used === "number" && total > 0 ? Math.min(100, (used / total) * 100) : 0;
  m.firstChild.style.width = `${p.toFixed(1)}%`;
  return m;
}
const fmtBig = v => (typeof v === "number" ? v.toLocaleString() : "–");

function renderServes() {
  const n = theNode(), sec = $("serves-sec");
  if (!n) return;
  const s = obj(n.status);
  if (!n.status) { sec.hidden = true; return; }
  const kv = [];
  const pair = (k, ...v) => kv.push(h("dt", {}, k), h("dd", {}, ...v));
  // Zones: copied (secondary), forwarded, hosted; a copy out of date says so, with the fix.
  const copied = arr(s.zones).map(obj), fwd = arr(s.forward_zones), hosted = arr(obj(s.hosted).zones);
  const stale = copied.filter(z => z.expired || z.fails > 0);
  const parts = [];
  if (copied.length) parts.push(`${copied.length} copied from your primary${stale.length ? "" : ", all up to date"}`);
  if (fwd.length) parts.push(`${fwd.length} forwarded`);
  if (hosted.length) parts.push(`${hosted.length} hosted here`);
  const zoneLine = [parts.join(" · ") || "None", " · ", h("a", { href: "zones.html" }, "Zones ›")];
  if (stale.length) {
    zoneLine.push(h("div", { class: "bad-text small" }, `Out of date: ${stale.map(z => `${str(z.name)}${z.expired ? " (expired)" : " (copies failing)"}`).join(", ")}.`),
      h("div", { class: "hint" }, `Check that your primary allows ${ip(n)} to copy ${stale.length === 1 ? "this zone" : "these zones"}: it must be on the transfer list.`));
  }
  pair("Zones", ...zoneLine);
  // Blocking
  const blk = obj(s.blocking), list = obj(blk.list);
  const svc = arr(s.services).find(x => x && x.name === "blocking");
  const off = (svc && svc.state === "off") || (data.form && obj(obj(data.form.settings).blocking).value === false);
  let bl;
  if (!Object.keys(blk).length && !svc) bl = "Not reported by its firmware";
  else if (off) bl = "Off in this node's settings";
  else if (blk.paused_s > 0) bl = `Paused, ${upFor(blk.paused_s)} left`;
  else bl = `On · ${fmtBig(list.entries)} domains${list.fallback ? " · on its older list" : ""}`;
  pair("Blocking", bl, " · ", h("a", { href: "blocklists.html" }, "Blocking ›"));
  // Memory: the plan's pools.
  const mem = obj(s.memory), inn = obj(mem.internal), ps = obj(mem.psram);
  const pool = (label, p) => (typeof p.capacity === "number" && p.capacity > 0
    ? h("div", { class: "pool" }, meter(p.planned, p.capacity), h("span", { class: "hint" }, `${label}: ${Math.round((p.planned / p.capacity) * 100)}% planned`)) : null);
  const pools = [pool("Internal memory", inn), pool("PSRAM", ps)].filter(Boolean);
  if (pools.length) pair("Memory", ...pools);
  else if (typeof s.heap_free === "number") pair("Memory", `${Math.round(s.heap_free / 1024)} KB internal free`);
  $("serves").replaceChildren(h("dl", { class: "kv" }, kv));
  sec.hidden = false;
}

// ---- the last hour -----------------------------------------------------------------------

function renderHour() {
  const n = theNode(), sec = $("hour");
  if (!n) return;
  // Its own query log: the log filtered to this node.
  $("hour-log").href = n.id ? `querylog.html?node=${encodeURIComponent(n.id)}` : "querylog.html";
  const nv = arr(obj(data.dash).nodes).find(x => (n.id && x.id === n.id) || x.addr === n.addr);
  if (!nv) { sec.hidden = true; return; }
  const hr = obj(nv.hour);
  const ms = v => (typeof v === "number" ? `${Math.round(v)} ms typical` : "–");
  $("minis").replaceChildren(
    mini("Queries a second", fmtNum(typeof hr.qps === "number" ? hr.qps : nv.status_qps), nv.qps),
    mini("Answer time", ms(hr.p50_ms)),
    mini("Blocked", pct(hr.blocked_share)),
    mini("From its cache", pct(hr.cache_hit_rate)));
  sec.hidden = false;
}

// ---- recent changes ----------------------------------------------------------------------

const ACTWORDS = { "job identify": "Light blinked", "job reboot": "Restarted", "job reboot-pending": "Restarted for a change", "job flush": "Cache flushed",
  "job revert": "Went back to an older list", "job pause": "Blocking paused", "job config-push": "Settings sent", "job rollout": "Changes sent", "job node-out": "Taken out of service", "job node-in": "Returned to service", "reboot": "Restarted",
  rollout: "Changes sent", identify: "Light blinked", flush: "Cache flushed", revert: "Went back to an older list", pause: "Blocking paused" };
const when = t => { const d = new Date(t); return isNaN(d) ? "" : d.toLocaleString([], { dateStyle: "medium", timeStyle: "short" }); };

function renderRecent() {
  const n = theNode(), box = $("recent");
  if (!n || data.actions === null) return;
  const needle = [n.addr, ip(n)].filter(Boolean);
  // An argument names this node when one of its whole words is its address (JSON, -host
  // a.b.c.d, a,b lists): never a longer address it is the start of (.5 against .53).
  const names = a => typeof a === "string" && a.split(/[\s,="{}\[\]]+/).some(w => needle.includes(w));
  const mine = arr(data.actions).filter(e => e && e.event === "end" && arr(e.args).some(names));
  const rows = mine.slice(0, 6).map(e => h("div", { class: "change" },
    h("span", {}, ACTWORDS[str(e.action)] || sentenceCase(str(e.action).replace(/^job /, ""))),
    h("span", { class: e.result === "ok" || !e.error ? "hint" : "hint bad" }, [when(e.time), str(e.who), e.error ? `failed: ${str(e.error)}` : ""].filter(Boolean).join(" · "))));
  $("recent-list").replaceChildren(...(rows.length ? rows : [h("p", { class: "hint" }, "Nothing for this node yet.")]));
  $("recent-val").textContent = mine.length ? `${ACTWORDS[str(mine[0].action)] || str(mine[0].action)} ${when(mine[0].time)}` : "None yet";
  box.hidden = false;
}

// ---- details for troubleshooting ---------------------------------------------------------

function secs(ms) { return typeof ms === "number" ? `${(ms / 1000).toFixed(1)} s` : "–"; }

let detailsBuilt = null;
function renderDetails() {
  const n = theNode(), box = $("details");
  if (!n) return;
  const s = obj(n.status), net = obj(s.net), cfg = obj(s.config), rb = obj(s.reboot), led1 = obj(s.led);
  const kv = [];
  const pair = (k, v, cls) => { if (has(v) || v instanceof Node) kv.push(h("dt", {}, k), h("dd", { class: cls || "" }, v)); };
  pair("Host name", str(n.hostname) || str(net.hostname), "mono");
  pair("Node ID", str(n.id), "mono");
  pair("Board", str(s.board), "mono");
  if (has(s.version)) {
    pair("Firmware", h("span", {}, str(s.version), " ", /^0\./.test(str(s.version)) ? h("span", { class: "tag" }, "alpha") : null, " ",
      has(s.elf_sha256) ? h("span", { class: "mono muted" }, `build ${str(s.elf_sha256).slice(0, 8)}`) : null));
  }
  if (has(s.ota_state) && s.ota_state !== "valid") pair("Firmware state", `${str(s.ota_state)} (confirms itself once it runs well)`);
  pair("Started in", has(s.boot_ms) ? secs(s.boot_ms) : "");
  pair("Last restart", str(s.reset));
  pair("Settings", has(cfg.source) ? (cfg.source === "node" ? `sent by the controller (seq ${cfg.seq})${cfg.trial ? ", on trial" : ""}` : "its board's defaults") : "");
  if (has(cfg.error)) pair("Settings error", str(cfg.error), "bad-text");
  if (rb.pending === true) pair("Waiting for a restart", arr(rb.reasons).filter(x => typeof x === "string").join(", "));
  if (has(led1.kind)) pair("Its light", str(led1.kind));
  pair("Last seen", n.last_seen ? `${sinceWords(n.last_seen)} ago` : "");

  const listedOK = !actionWhy(n, false);
  const fl = h("button", { class: "btn small", type: "button", disabled: !listedOK });
  fl.textContent = "Flush its cache";
  fl.title = "Drops every cached answer";
  fl.addEventListener("click", () => flush(n, fl));
  const reverts = Object.keys(LISTS).map(k => h("button", { class: "btn small", type: "button", disabled: !listedOK, onclick: () => openRevert(n, k) }, `Older ${LISTS[k]}`));
  const runs = data.form ? str(obj(obj(data.form.config).runs).text) : "";
  const ipn = ip(n);
  const cli = [`espdns status -host ${ipn}`, `espdns identify -host ${ipn}`, `espdns reboot -host ${ipn}`, `espdns flush -host ${ipn}`];

  if (!detailsBuilt) {
    detailsBuilt = { kv: h("dl", { class: "kv" }), acts: h("div", { class: "row" }), runs: h("pre", { class: "raw", id: "raw-config" }), status: h("pre", { class: "raw", id: "raw-status" }),
      runsBox: null, cli: h("p", { class: "hint" }) };
    detailsBuilt.runsBox = h("details", { class: "sub-raw" }, h("summary", {}, "Its settings, as it runs them"), detailsBuilt.runs);
    $("details-body").append(detailsBuilt.kv, h("div", { class: "stack nogap-s" }, h("span", { class: "small" }, h("b", {}, "Quick fixes")), detailsBuilt.acts,
      h("span", { class: "hint" }, "Older …: back to the copy from before the last change.")),
    detailsBuilt.runsBox, h("details", { class: "sub-raw" }, h("summary", {}, "Its last status, as it reported it"), detailsBuilt.status), detailsBuilt.cli);
  }
  detailsBuilt.kv.replaceChildren(...kv);
  detailsBuilt.acts.replaceChildren(fl, ...reverts);
  detailsBuilt.runs.textContent = runs;
  detailsBuilt.runsBox.hidden = !runs;
  detailsBuilt.status.textContent = n.status ? JSON.stringify(n.status, null, 2) : "Nothing read yet.";
  detailsBuilt.cli.replaceChildren("From a terminal: ", ...cli.flatMap((c, i) => [i ? " · " : "", h("code", {}, c)]));
  box.hidden = false;
}

// ---- reading -----------------------------------------------------------------------------

function render() {
  renderHead();
  if (!theNode()) return;
  renderAttention();
  renderServes();
  renderHour();
  renderRecent();
  renderDetails();
}

async function readUpdates() {
  try { data.updates = await getJSON("api/updates"); } catch { data.updates = null; }
  render();
}
async function readDash() {
  try { data.dash = await getJSON("api/dashboard"); } catch { data.dash = null; }
  renderHour();
}
async function readActions() {
  try { data.actions = await getJSON("api/actions?n=300"); } catch { data.actions = []; }
  renderRecent();
}

let lastNode = "";
onNodes(ns => {
  data.nodes = ns;
  const n = theNode(), k = n ? str(n.id) || n.addr : "";
  render();
  if (k && k !== lastNode) { lastNode = k; readSettings(); }
});
window.addEventListener("hashchange", () => {
  data.form = null; data.formErr = ""; data.formFor = ""; dirty = false; lastNode = "";
  render();
  const n = theNode();
  if (n) { lastNode = str(n.id) || n.addr; readSettings(); }
});
// A change applied, added or discarded elsewhere: the form reads again (unless being edited).
let changesKey = null;
onChanges(c => {
  const k = arr(c.changes).map(x => x.id).join(",");
  if (changesKey !== null && k !== changesKey) {
    readUpdates();
    if (!dirty) readSettings();
  }
  changesKey = k;
});
readBoards(data.boards).then(render);
session.then(async s => {
  data.sess = s;
  data.sessRead = true;
  if (s.logged_in) data.key = await releaseKey();
  render();
  readSettings();
  every(readUpdates, 30000);
  every(readDash, 10000);
  every(readActions, 30000);
});
