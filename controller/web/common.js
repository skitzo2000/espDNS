// Shared by the pages (under shell.js, which draws the top bar): the session, the API
// helpers, the fleet's nodes and their one name, jobs, and small helpers. Everything from a
// node (names, reasons, log lines) is hostile: text only.

// ---- the session ------------------------------------------------------------------------
// A session has two halves (internal/auth): the cookie, which the browser keeps for the host
// (so whatever listens on another port of it gets it too), and the session's token, which
// the login gives this page and which is kept here in localStorage: per origin, the port
// included, so another port can't read it. Every API request carries it in X-Session-Token
// (api); without it the controller answers 401. The device token, kept the same way, names
// this browser to the login's backoff (a failed login elsewhere doesn't lock it out).
const TOKEN = "espdns.session", DEVICE = "espdns.device";
function stored(k) {
  try { return localStorage.getItem(k) || ""; } catch { return ""; }
}
function store(k, v) {
  try { if (v) localStorage.setItem(k, v); else localStorage.removeItem(k); } catch { /* no storage: no session */ }
}
// loggedIn keeps a login's reply: the session's token and the device token.
export function loggedIn(j) {
  store(TOKEN, j && j.token);
  if (j && j.device) store(DEVICE, j.device);
}
export const deviceToken = () => stored(DEVICE);

// api is fetch with the session's token, never cached.
export function api(url, opts = {}) {
  const headers = new Headers(opts.headers || {});
  const t = stored(TOKEN);
  if (t) headers.set("X-Session-Token", t);
  return fetch(url, { cache: "no-store", ...opts, headers });
}

// session is /api/session: {password_set, logged_in, user, expires, error}. Loaded once per
// page; every page but the login page needs a session once a password is set (the
// controller redirects there), and without a password set the controller is read-only.
export const session = api("api/session")
  .then(r => r.json())
  .catch(() => ({ password_set: false, logged_in: false }));

// key is /api/key: whether there is a release key to sign actions with.
export const releaseKey = () => getJSON("api/key").catch(e => ({ present: false, error: e.message }));

function toLogin() {
  location.href = `login.html?next=${encodeURIComponent(location.pathname + location.search + location.hash)}`;
}

// getJSON reads an API; a 401 (the session ended) goes to the login page. It throws an error
// reply's "error", with its JSON as the error's body. headers adds to
// the request's (X-Reauth).
export async function getJSON(url, headers = {}) {
  const r = await api(url, { headers });
  if (r.status === 401) { toLogin(); throw new Error("log in first"); }
  if (!r.ok) {
    let j = {};
    try { j = await r.json(); } catch { /* not JSON */ }
    throw Object.assign(new Error(`${url}: ${(j && j.error) || r.statusText}`), { body: j || {} });
  }
  return r.json();
}

// post sends JSON with the session's token; a 401 goes to the login page. It returns the
// reply, or throws its error (with the reply's JSON as the error's body). headers adds to
// the request's (X-Reauth).
export async function post(url, body, headers = {}) {
  const r = await api(url, { method: "POST", headers: { "Content-Type": "application/json", ...headers }, body: JSON.stringify(body || {}) });
  let j = {};
  try { j = await r.json(); } catch { /* not JSON */ }
  if (r.status === 401) { toLogin(); throw new Error(j.error || "log in first"); }
  if (!r.ok) throw Object.assign(new Error(j.error || r.statusText), { body: j });
  return j;
}

// askPassword asks for the login password again, for what gives secrets away (a backup:
// the release key and the Wi-Fi passwords; a config shown with its Wi-Fi password): POST
// /api/reauth. It resolves to the grant, for one request in X-Reauth within two minutes, or
// null when cancelled. A wrong password asks
// again; three in a row end the session (then the login page).
export function askPassword(why) {
  return new Promise(resolve => {
    const d = el("dialog", "confirm reauth");
    const form = el("form");
    const label = el("label", "", "Password");
    const input = el("input");
    input.type = "password";
    input.autocomplete = "current-password";
    input.required = true;
    input.spellcheck = false;
    label.append(input);
    const msg = el("p", "sub");
    msg.setAttribute("role", "status");
    const row = el("div", "row");
    const no = el("button", "btn", "Cancel"), yes = el("button", "btn primary", "Continue");
    no.type = "button";
    yes.type = "submit";
    row.append(no, yes);
    form.append(el("h2", "", "Your password again"), el("p", "", why), label, msg, row);
    d.append(form);
    const done = v => { d.close(); d.remove(); resolve(v); };
    no.onclick = () => done(null);
    d.addEventListener("cancel", e => { e.preventDefault(); done(null); });
    form.addEventListener("submit", async e => {
      e.preventDefault();
      yes.disabled = true;
      msg.className = "sub";
      msg.textContent = "Checking…";
      try {
        const r = await api("api/reauth", { method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ password: input.value }) });
        const j = await r.json().catch(() => ({}));
        if (r.ok && j.reauth) { done(j.reauth); return; }
        if (r.status === 401) { done(null); toLogin(); return; }
        msg.className = "sub bad";
        msg.textContent = j.error || r.statusText;
        input.select();
      } catch (err) {
        msg.className = "sub bad";
        msg.textContent = `Can't reach the controller: ${err.message}`;
      }
      yes.disabled = false;
    });
    document.body.append(d);
    d.showModal();
    input.focus();
  });
}

// events follows a job's Server-Sent Events as an EventSource would, with the session's
// token (an EventSource sends no header of its own): addEventListener(type, f) calls f with
// {data} for each event of the type; close() stops. A stream that drops is taken up again
// after the last line seen (?after=, as Last-Event-ID), until close(); one refused (the job
// gone: the controller restarted, say) stops, with a "gone" event, so a page waiting for
// "end" isn't left waiting.
export function events(url) {
  const handlers = new Map();
  let closed = false, after = 0, ctl = null;
  const emit = (type, data) => {
    for (const f of handlers.get(type) || []) {
      try { f({ data }); } catch (e) { console.error(e); }
    }
  };
  const block = text => {
    let type = "message", id = null;
    const data = [];
    for (const line of text.split("\n")) {
      if (line.startsWith(":")) continue;
      const c = line.indexOf(":");
      const field = c < 0 ? line : line.slice(0, c);
      let v = c < 0 ? "" : line.slice(c + 1);
      if (v.startsWith(" ")) v = v.slice(1);
      if (field === "event") type = v;
      else if (field === "data") data.push(v);
      else if (field === "id") id = v;
    }
    if (id !== null && Number(id) > after) after = Number(id);
    if (data.length) emit(type, data.join("\n"));
  };
  (async () => {
    while (!closed) {
      ctl = new AbortController();
      try {
        const r = await api(after ? `${url}${url.includes("?") ? "&" : "?"}after=${after}` : url, { signal: ctl.signal });
        if (r.status === 401) { toLogin(); return; }
        if (r.status >= 400 && r.status < 500) { console.error(`${url}: ${r.status}`); closed = true; emit("gone", String(r.status)); return; }
        if (!r.ok || !r.body) throw new Error(r.statusText);
        const rd = r.body.pipeThrough(new TextDecoderStream()).getReader();
        let buf = "";
        for (;;) {
          const { value, done } = await rd.read();
          if (done) break;
          buf = (buf + value).replace(/\r\n?/g, "\n");
          let i;
          while (!closed && (i = buf.indexOf("\n\n")) >= 0) {
            block(buf.slice(0, i));
            buf = buf.slice(i + 2);
          }
          if (closed) { ctl.abort(); return; }
        }
      } catch (e) {
        if (closed) return;
        console.error(e);
      }
      if (!closed) await new Promise(res => setTimeout(res, 3000));
    }
  })();
  return {
    addEventListener(type, f) {
      if (!handlers.has(type)) handlers.set(type, []);
      handlers.get(type).push(f);
    },
    close() { closed = true; if (ctl) ctl.abort(); },
  };
}

export function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// ---- the fleet: /api/nodes, read every 5 s for the shell ----------------------------------

const POLL = 5000;
const fleet = { nodes: null, text: "", error: null, byRef: new Map(), listeners: new Set() };
const str = v => (typeof v === "string" ? v : "");
const hostOnly = a => str(a).replace(/:\d+$/, "");

// onNodes calls f(nodes, {changed, error}) after every read of /api/nodes (nodes is the last
// list read, null before the first), and at once if one was read.
export function onNodes(f) {
  fleet.listeners.add(f);
  if (fleet.nodes) f(fleet.nodes, { changed: true, error: fleet.error });
}

// findNode is the node a page's reference names: its polled address (with or without the
// port), its node ID (the chip MAC), or its host name (with or without ".local").
export function findNode(ref) {
  if (ref && typeof ref === "object") return ref;
  if (typeof ref !== "string" || !ref) return undefined;
  return fleet.byRef.get(ref) || fleet.byRef.get(ref.toLowerCase());
}

// The references go in by how much a node controls them: the addresses the controller polls
// first, then node IDs, then host names (a node names itself anything), each kind for every
// node before the next, so a node naming itself after another's ID or address can't take
// that reference.
function indexNodes(ns) {
  fleet.byRef.clear();
  const add = (k, n) => { if (k && !fleet.byRef.has(k)) fleet.byRef.set(k, n); };
  for (const n of ns) add(str(n.addr), n);
  for (const n of ns) add(hostOnly(n.addr), n);
  for (const n of ns) add(str(n.id).toLowerCase(), n);
  for (const n of ns) {
    const h = str(n.hostname).toLowerCase();
    add(h, n);
    add(h.replace(/\.local\.?$/, ""), n);
  }
}

// nodeName is a node's one name, the same on every page: its config's name leads, else its
// host name, else its address; sub is the host name and address it isn't (small text).
// ref is a node from /api/nodes or a reference findNode takes; fallback names a node the
// controller doesn't know (yet).
export function nodeName(ref, fallback) {
  const n = findNode(ref);
  if (!n) {
    const name = str(fallback) || (typeof ref === "string" ? ref : "") || "?";
    const sub = typeof ref === "string" && ref !== name ? ref : "";
    return { name, sub, node: undefined };
  }
  const cfg = n.status && typeof n.status.config === "object" && n.status.config ? n.status.config : {};
  const host = str(n.hostname) || str(n.status && n.status.net && n.status.net.hostname);
  const name = str(cfg.name) || host || str(n.addr) || str(fallback) || "?";
  const sub = [host !== name ? host : "", n.addr !== name ? str(n.addr) : ""].filter(Boolean).join(" · ");
  return { name, sub, node: n };
}

// nodeText is the name alone, for a sentence, an option or a dialog.
export const nodeText = (ref, fallback) => nodeName(ref, fallback).name;

// answering: a node that answers DNS queries now (read, and its health says it answers).
export function answering(n) {
  if (!n.online || !n.status) return false;
  const h = n.status.health;
  return !(h && typeof h === "object" && h.answering === false);
}
// inService: one of your nodes (in settings.json) and not taken out of service: the nodes
// that are sent changes and keep DNS answering.
export const inService = n => n.source === "settings" && !n.out_of_service;
export const wired = n => !!(n.status && n.status.net && n.status.net.kind === "ethernet");

async function pollNodes() {
  let changed = false;
  try {
    const r = await api("api/nodes", { signal: AbortSignal.timeout(POLL * 3) });
    if (r.status === 401) { toLogin(); return; }
    if (!r.ok) throw new Error(`api/nodes: ${r.status} ${r.statusText}`);
    const text = await r.text();
    fleet.error = null;
    if (text !== fleet.text) {
      const ns = JSON.parse(text);
      fleet.nodes = Array.isArray(ns) ? ns.filter(n => n && typeof n === "object") : [];
      fleet.text = text;
      changed = true;
      indexNodes(fleet.nodes);
    }
  } catch (e) {
    fleet.error = e;
    console.error(e);
  }
  for (const f of fleet.listeners) {
    try { f(fleet.nodes, { changed, error: fleet.error }); } catch (e) { console.error(e); }
  }
}

// ---- jobs ------------------------------------------------------------------------------

export const ended = s => ["done", "failed", "stopped"].includes(s);
export function jobParams(j) {
  try { const p = JSON.parse((j.args || [])[0] || "{}"); return p && typeof p === "object" ? p : {}; } catch { return {}; }
}

// waitJob polls a job until it ends, and returns it.
export async function waitJob(id) {
  for (;;) {
    const j = await getJSON(`api/jobs/${encodeURIComponent(id)}`);
    if (ended(j.state)) return j;
    await new Promise(r => setTimeout(r, 500));
  }
}

// watchNodes reads /api/nodes every 5 s (shell.js starts it): onNodes hears each read.
export function watchNodes() { every(pollNodes); }

// every runs a poll now and every 5 s (or ms): one at a time (a slow controller doesn't
// stack them up), not while the tab is hidden, and at once when it is shown again. It
// returns a function that stops it.
export function every(f, ms = POLL) {
  let running = false;
  const run = async () => {
    if (running || document.hidden) return;
    running = true;
    try { await f(); } finally { running = false; }
  };
  const t = setInterval(run, ms);
  document.addEventListener("visibilitychange", run);
  run();
  return () => { clearInterval(t); document.removeEventListener("visibilitychange", run); };
}

// ago is how long ago a time was, in words.
export function ago(t) {
  if (!t || t.startsWith("0001")) return "never";
  const s = Math.max(0, Math.round((Date.now() - new Date(t)) / 1000));
  return s < 60 ? `${s} s ago` : s < 3600 ? `${Math.round(s / 60)} min ago`
    : s < 86400 ? `${Math.round(s / 3600)} h ago` : `${Math.round(s / 86400)} days ago`;
}
