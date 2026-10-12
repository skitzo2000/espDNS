// Blocking (blocklists.html): blocking is
// one setup for the whole fleet. Its state first ("Blocking is on", the domains from the
// lists in use, the share blocked in the last hour, where it is on, paused or off), + Add
// blocklist at the top right (the button becomes a box with the catalogue's lists right
// under it, a list by its web address, or the picker by purpose), Pause on every node,
// Check a site (blocked or not, by which list, on which nodes, asked when and by whom, with
// Allow or Block), the always allowed and blocked sites, and the blocklists, each turned
// off and on or removed. Every edit waits as a pending change for the header's Apply
// (/api/blocking/setup); pause and resume act at once (the pause job). The size-change
// check asks as a question when an apply stopped on it. Advanced keeps each node's blocking
// with its own pause and going back to its older lists, the definitions and the source
// files as text, and the same compile from a terminal.
import { shell, h, arr, obj, str, toast, panel, refreshChanges, onChanges, getJSON, post, session, nodeText, copyText } from "./shell.js";
import { every, waitJob, ago } from "./common.js";

shell("blocklists.html");

const $ = id => document.getElementById(id);
const st = { sess: {}, setup: null, error: null, dash: null, pending: null, adv: null, busy: false, pausing: false };
const can = () => !!st.sess.logged_in;
const fmt = n => (typeof n === "number" ? n.toLocaleString("en-US") : "–");
const short = n => (n >= 1e6 ? `${(n / 1e6).toFixed(1)} M` : n >= 1e3 ? `${Math.round(n / 1e3)} k` : String(n));
const plural = (n, one, many) => `${fmt(n)} ${n === 1 ? one : many}`;
const andList = xs => (xs.length < 2 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`);
const KINDS = [["hosts", "a hosts file"], ["domains", "a list of names"], ["wildcard", "names, with everything under them"], ["adblock", "an adblock list"], ["rpz", "an RPZ zone"]];
const kindWords = k => (KINDS.find(x => x[0] === k) || [k, k])[1];
const specKind = s => str(s).split(":")[0];
const PAUSES = [[300, "5 minutes"], [3600, "1 hour"], [0, "Until I resume"]];

// ---- reading ----------------------------------------------------------------------------

async function readSetup() {
  try {
    st.setup = await getJSON("api/blocking/setup");
    st.error = null;
  } catch (e) {
    st.error = e;
  }
  render();
}

async function readDash() {
  try { st.dash = await getJSON("api/dashboard"); } catch { st.dash = null; }
  renderHead();
}

// edit sends one edit of the setup; it becomes a pending change (or two).
async function edit(body, said) {
  if (!can()) return false;
  try {
    const r = await post("api/blocking/setup", body);
    await refreshChanges();
    await readSetup();
    const cs = arr(r.changes);
    toast(said || (cs.length ? `${str(cs[0].summary)}: in the changes to apply` : "In the changes to apply"));
    return true;
  } catch (e) {
    toast(e.message);
    return false;
  }
}

// ---- the head: the state in words, + Add blocklist --------------------------------------

const lists = () => arr(obj(st.setup).lists);
const inUse = () => lists().filter(l => l.on && !l.allow);
const nodesOf = () => arr(obj(st.setup).nodes);
const named = ns => ns.map(n => nodeText(n.host));
const active = () => nodesOf().filter(n => n.state === "on" || n.state === "paused");

function untilWords(secs) {
  const t = new Date(Date.now() + secs * 1000);
  const hm = t.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  return secs < 20 * 3600 ? hm : `${t.toLocaleDateString([], { weekday: "short", day: "numeric", month: "short" })} ${hm}`;
}

function blockingState() {
  const s = obj(st.setup);
  if (!s.defined) return "off";
  const a = active();
  if (a.length && a.every(n => n.state === "paused")) return "paused";
  if (a.length) return "on";
  return "waiting";
}

function renderHead() {
  const s = st.setup;
  const title = $("title"), facts = $("facts");
  if (!s) return;
  const state = blockingState();
  title.textContent = { off: "Blocking", on: "Blocking is on", paused: "Blocking is paused", waiting: "Blocking isn't on yet" }[state];
  const out = [];
  if (state !== "off") {
    const n = inUse().filter(l => !l.waiting).length, rep = s.report;
    out.push(rep ? h("span", {}, h("b", {}, fmt(rep.blocked)), ` domains from ${plural(n, "list", "lists")}`)
      : h("span", {}, `${plural(n, "list", "lists")}, not sent to the nodes yet`));
    const hour = obj(obj(obj(st.dash).fleet).hour);
    if (typeof hour.blocked_share === "number") {
      out.push(h("span", {}, h("b", {}, `${(hour.blocked_share * 100).toFixed(1)}%`),
        ` of queries blocked in the last hour${typeof hour.blocked === "number" ? ` (${fmt(hour.blocked)})` : ""}`));
    }
    const by = k => named(nodesOf().filter(x => x.state === k));
    const parts = [];
    const on = nodesOf().filter(x => x.state === "on");
    if (on.length) parts.push(`On ${andList(named(on))}`);
    if (by("paused").length) parts.push(`paused on ${andList(by("paused"))}`);
    if (by("off").length) parts.push(`off on ${andList(by("off"))} (${by("off").length === 1 ? "its" : "their"} settings)`);
    const none = nodesOf().filter(x => ["none", "loading", "failed"].includes(x.state));
    if (none.length) parts.push(`no list yet on ${andList(named(none))}`);
    const old = on.filter(x => !x.current);
    if (old.length && s.report) parts.push(`${andList(named(old))} still on an older list`);
    if (parts.length) out.push(h("span", {}, parts.join(" · ")));
  }
  facts.replaceChildren(...out);
}

// ---- + Add blocklist: the button becomes a box, the catalogue's matches under it ----------

function guessKind(u) {
  const s = u.toLowerCase();
  if (/rpz/.test(s)) return "rpz";
  if (/adblock|adguard|easylist|abp|filter/.test(s)) return "adblock";
  if (/wild/.test(s)) return "wildcard";
  if (/hosts/.test(s)) return "hosts";
  return "domains";
}

const has = spec => lists().some(l => l.source === spec);

function addListBox(box, primary) {
  let input = null, list = null, open = false;
  const button = h("button", { class: primary ? "btn primary" : "btn", type: "button", onclick: () => show() }, "+ Add blocklist");
  button.disabled = !can();
  if (!can()) button.title = "Log in to change blocking";
  box.replaceChildren(button);

  function show() {
    open = true;
    input = h("input", { class: "t", id: "lq", type: "text", autocomplete: "off", spellcheck: "false", placeholder: "Name, tag or web address",
      role: "combobox", "aria-expanded": "true", "aria-controls": "lqlist", "aria-label": "Add a blocklist" });
    list = h("div", { class: "combo-list", id: "lqlist", role: "listbox", "aria-label": "Blocklists" });
    input.addEventListener("input", fill);
    input.addEventListener("focus", fill);
    input.addEventListener("keydown", e => { if (e.key === "Escape") { e.preventDefault(); close(); } });
    box.replaceChildren(h("div", { class: "combo lcombo" }, input, list));
    fill();
    input.focus();
  }
  function close() {
    open = false;
    box.replaceChildren(button);
  }
  // A click elsewhere closes the box, or, with something typed in it, only its list:
  // the list would otherwise stay over the page.
  document.addEventListener("mousedown", e => {
    if (!open || box.contains(e.target) || document.querySelector(".drawer")) return;
    if (!input.value.trim()) close();
    else { list.hidden = true; input.setAttribute("aria-expanded", "false"); }
  });

  function fill() {
    if (!open) return;
    list.hidden = false;
    input.setAttribute("aria-expanded", "true");
    const q = input.value.trim(), ql = q.toLowerCase();
    const opts = [];
    if (/^https?:\/\/\S+$/i.test(q)) {
      const kind = h("select", { class: "t", "aria-label": "Read it as" }, KINDS.map(([k, w]) => h("option", { value: k, selected: k === guessKind(q) }, w)));
      const add = h("button", { class: "btn small primary", type: "button" }, "Add");
      add.addEventListener("click", async () => {
        add.disabled = true;
        if (await edit({ op: "add", sources: [`${kind.value}:${q}`] })) { if (input.value.trim() === q) input.value = ""; fill(); } else add.disabled = false;
      });
      opts.push(h("div", { class: "opt url" }, h("div", {}, h("b", {}, "Add the list at this address"),
        h("div", { class: "tagline mono" }, q), h("div", { class: "tagline" }, "Read as ", kind)), add));
    }
    const cat = arr(obj(st.setup).catalog).filter(c => !ql || `${c.name} ${c.group} ${c.by} ${c.what}`.toLowerCase().includes(ql));
    for (const c of cat) {
      let act;
      if (has(c.source)) act = h("span", { class: "hint" }, "Added");
      else {
        act = h("button", { class: "btn small", type: "button", "aria-label": `Add ${c.name}` }, "Add");
        act.addEventListener("click", async () => {
          act.disabled = true;
          if (await edit({ op: "add", sources: [c.source] }, `Added ${c.name}: in the changes to apply`)) fill(); else act.disabled = false;
        });
      }
      opts.push(h("div", { class: "opt", role: "option" }, h("div", {}, h("b", {}, c.name), h("div", { class: "tagline" }, `${c.group} · ${c.what}`)), act));
    }
    if (!opts.length) opts.push(h("div", { class: "opt" }, h("div", { class: "tagline" }, "No list called that. Paste a web address to add one.")));
    opts.push(h("div", { class: "browse" }, h("button", { class: "btn link small", type: "button", onclick: () => picker(input.value.trim()) }, "Browse every list by what it blocks")));
    list.replaceChildren(...opts);
  }
  return { refill: fill };
}

// picker is the catalogue by purpose, a checkbox per list, and a list by its web address:
// the lists ticked are added as one change.
function picker(q0) {
  const p = panel("Add blocklists");
  const pick = new Set();
  let group = "All", q = /^https?:/i.test(q0) ? "" : q0;
  const search = h("input", { class: "t", id: "catq", type: "search", placeholder: "Search lists", "aria-label": "Search lists", value: q });
  const chips = h("div", { class: "chips", role: "group", "aria-label": "What it blocks" });
  const box = h("div", { id: "catlist" });
  const url = h("input", { class: "t mono", id: "listurl", type: "url", placeholder: "The list's web address", spellcheck: "false" });
  const kind = h("select", { class: "t", id: "listkind", "aria-label": "Read it as" }, KINDS.map(([k, w]) => h("option", { value: k }, w)));
  if (/^https?:/i.test(q0)) { url.value = q0; kind.value = guessKind(q0); }
  url.addEventListener("input", () => { kind.value = guessKind(url.value); count(); });
  const go = h("button", { class: "btn primary", type: "button", disabled: true });
  const msg = h("p", { class: "hint bad", role: "status" });
  const cat = arr(obj(st.setup).catalog);
  const groups = ["All", ...arr(obj(st.setup).groups)];
  function count() {
    const n = pick.size + (url.value.trim() ? 1 : 0);
    go.textContent = n ? `Add ${n} list${n > 1 ? "s" : ""}` : "Add lists";
    go.disabled = !n || !can();
  }
  function draw() {
    chips.replaceChildren(...groups.map(g => h("button", { type: "button", "aria-pressed": String(g === group), onclick: () => { group = g; draw(); } }, g)));
    const ql = q.toLowerCase();
    const shown = cat.filter(c => (group === "All" || c.group === group) && (!ql || `${c.name} ${c.by} ${c.what}`.toLowerCase().includes(ql)));
    box.replaceChildren(...shown.map(c => {
      const used = has(c.source);
      const cb = h("input", { type: "checkbox", checked: used || pick.has(c.source), disabled: used });
      cb.addEventListener("change", () => { if (cb.checked) pick.add(c.source); else pick.delete(c.source); count(); });
      return h("label", { class: "cat" }, cb, h("span", {}, h("b", {}, c.name), used ? [" ", h("span", { class: "tag" }, "in use")] : null,
        h("div", { class: "hint" }, `${c.what} · ${c.by}${group === "All" ? ` · ${c.group}` : ""}`)));
    }));
    if (!shown.length) box.append(h("p", { class: "hint" }, "No list matches. Try another word, or add one by its address below."));
  }
  search.addEventListener("input", () => { q = search.value.trim(); draw(); });
  go.addEventListener("click", async () => {
    const sources = [...pick];
    if (url.value.trim()) sources.push(`${kind.value}:${url.value.trim()}`);
    go.disabled = true;
    msg.textContent = "";
    try {
      await post("api/blocking/setup", { op: "add", sources });
      await refreshChanges();
      await readSetup();
      p.close();
      toast(`Added ${sources.length === 1 ? "1 list" : `${sources.length} lists`}: in the changes to apply`);
    } catch (e) { msg.textContent = e.message; count(); }
  });
  const byURL = h("details", { class: "ex", open: !!url.value }, h("summary", {}, h("b", {}, "A list by its web address"), h("span", { class: "val" })),
    h("div", { class: "exb" }, h("label", { class: "f" }, h("span", {}, "Web address"), url,
      h("span", { class: "hint" }, "Hosts files, domain lists, adblock lists and RPZ zones all work.")),
    h("label", { class: "f" }, h("span", {}, "Read it as"), kind)));
  p.body.append(h("p", { class: "small muted" }, "Tick the lists you want. A domain on several lists counts once."), search, chips, box, byURL,
    h("p", { class: "hint" }, "Sizes are checked against each node's memory when you apply."), msg);
  p.footer.append(h("button", { class: "btn", type: "button", onclick: p.close }, "Cancel"), go);
  draw();
  count();
  search.focus();
}

// ---- the size-change question -----------------------------------------------------------

const OVERRIDE_FILES = ["overrides.txt", "overrides-allow.txt"];

const SIZEWORDS = { blocked: "domains blocked", allowed: "domains allowed", bytes: "size in bytes" };

// sizeWords is the apply's question in the page's words: what changed, its numbers grouped
// ("blocked 2587385 → 1552431 (-40%)" becomes "domains blocked: 2,587,385 → 1,552,431 (-40%)").
function sizeWords(q) {
  const m = str(q.text).match(/limit \((.*)\)(?::|$)/);
  if (!m) return str(q.text);
  const what = m[1].replace(/\d{4,}/g, d => fmt(Number(d))).replace(/(^|, )(blocked|allowed|bytes) /g, (_, p, w) => `${p}${SIZEWORDS[w]}: `);
  return `${str(q.list) === "overrides" ? "Your always allowed and blocked sites" : "Your blocklists"}: ${what}.`;
}

function renderAsk() {
  const box = $("ask");
  const a = obj(obj(st.pending).apply);
  const qs = [...arr(obj(a.progress).questions), ...arr(obj(a.result).questions)].filter(q => q.kind === "size-change");
  // The changes that make the lists asked about compiled again: for the always allowed and
  // blocked sites (the list "overrides") their two files, for a blocklist the definitions
  // and its other source files. "Keep" discards only those.
  const asked = new Set(qs.map(q => str(q.list)));
  const blockingWaits = arr(obj(st.pending).changes).filter(c => {
    const own = c.kind === "source" && OVERRIDE_FILES.includes(c.name);
    return asked.has("overrides") && own || [...asked].some(l => l !== "overrides") && (c.kind === "lists" || c.kind === "source" && !own);
  });
  if (a.state !== "stopped" || !qs.length || !blockingWaits.length) { box.hidden = true; box.replaceChildren(); return; }
  const q = qs[0];
  const take = h("button", { class: "btn primary", type: "button", disabled: !can() }, "Apply, taking this change");
  const keep = h("button", { class: "btn", type: "button", disabled: !can() }, "Keep what the nodes have");
  take.addEventListener("click", async () => {
    take.disabled = keep.disabled = true;
    try { await post("api/jobs", { kind: "apply", params: obj(q.params) }); await refreshChanges(); toast("Applying, taking the change in size"); }
    catch (e) { toast(e.message); take.disabled = keep.disabled = false; }
  });
  keep.addEventListener("click", async () => {
    take.disabled = keep.disabled = true;
    try {
      for (const c of blockingWaits) await post(`api/changes/${encodeURIComponent(c.id)}/discard`).catch(() => {});
      await refreshChanges();
      await readSetup();
      toast("Kept: the nodes keep the lists they have");
    } catch (e) { toast(e.message); take.disabled = keep.disabled = false; }
  });
  box.hidden = false;
  box.replaceChildren(h("div", { class: "alert-box" }, h("strong", {}, "Your blocklists changed size more than usual"),
    h("p", {}, sizeWords(q)), h("p", { class: "hint" }, "A list that shrinks a lot often failed to download. Nothing was sent to the nodes."),
    h("div", { class: "row" }, take, keep)));
}

// ---- pause ------------------------------------------------------------------------------

async function pauseAll(secs, nodes) {
  st.pausing = true;
  renderPause();
  const jobs = [];
  for (const n of nodes) {
    try { jobs.push(await post("api/jobs", { kind: "pause", params: { node: n.host, seconds: secs } })); }
    catch (e) { toast(`${nodeText(n.host)}: ${e.message}`); }
  }
  if (jobs.length) {
    toast(secs ? `Pausing blocking until ${untilWords(secs)} on ${andList(named(nodes))}` : `Resuming blocking on ${andList(named(nodes))}`);
  }
  const ends = await Promise.all(jobs.map(j => waitJob(j.id).catch(e => ({ state: "failed", error: e.message }))));
  const bad = ends.filter(j => j.state !== "done");
  if (bad.length) toast(`Not on every node: ${str(bad[0].error) || bad[0].state}`);
  else if (jobs.length) toast(secs ? `Blocking paused until ${untilWords(secs)} on every node` : "Blocking resumed");
  // The nodes' state comes from their next poll: read again until it says so (a few
  // seconds), so the page shows Paused with Resume (or On again) without a reload.
  const hosts = new Set(nodes.map(n => n.host));
  for (let i = 0; i < 8; i++) {
    await readSetup();
    const now = nodesOf().filter(n => hosts.has(n.host));
    if (bad.length || !jobs.length || now.every(n => secs ? n.state === "paused" : n.state !== "paused")) break;
    await new Promise(r => setTimeout(r, 2000));
  }
  st.pausing = false;
  render();
}

function renderPause() {
  const box = $("pause");
  const a = active(), paused = a.filter(n => n.state === "paused");
  const maxPause = Number(obj(st.setup).pause_max_s) || 7 * 86400;
  if (!a.length) {
    box.replaceChildren(h("span", { class: "muted" }, "No node is blocking now: nothing to pause."));
    return;
  }
  if (paused.length) {
    const longest = Math.max(...paused.map(n => n.paused_s || 0));
    const resume = h("button", { class: "btn", type: "button", disabled: !can() || st.pausing, onclick: () => pauseAll(0, paused) }, st.pausing ? "Resuming…" : "Resume now");
    const text = paused.length === a.length
      ? `Nothing is blocked until ${untilWords(longest)}, except your always-blocked sites.`
      : `Paused on ${andList(named(paused))} until ${untilWords(longest)}.`;
    box.replaceChildren(h("span", { class: "muted" }, text), h("div", { class: "row" }, resume));
    return;
  }
  const sel = h("select", { class: "t", id: "pausefor", "aria-label": "Pause for" }, PAUSES.map(([v, t]) => h("option", { value: String(v) }, t)));
  sel.title = "Until I resume: a week at most. A node's restart ends a pause too.";
  const go = h("button", { class: "btn", type: "button", disabled: !can() || st.pausing }, st.pausing ? "Pausing…" : "Pause");
  go.addEventListener("click", () => pauseAll(Number(sel.value) || maxPause, a));
  box.replaceChildren(h("span", { class: "muted" }, "Pause blocking on every node for a while."), h("div", { class: "row" }, sel, go));
}

// ---- check a site -----------------------------------------------------------------------

const WHY = { paused: "paused", off: "blocking off in its settings", "older list": "still on an older list", "no list": "no list yet", unknown: "not read" };
let checkSeq = 0;

const times = n => n === 1 ? "once" : n === 2 ? "twice" : `${n.toLocaleString("en-US")} times`;
const qlLink = (param, value) => `querylog.html?${param}=${encodeURIComponent(value)}`;

// askersBox is who asked for the site in the nodes' query logs (api/querylog/askers): each
// device, named where the zones or the nodes name it, linked to its queries; a gateway said
// to be one, as it hides the devices behind it. null: the query log couldn't be read.
function askersBox(site, v) {
  if (!v) return null;
  const list = arr(v.askers);
  if (!list.length) {
    const reading = arr(v.nodes).some(n => n.enabled && n.state === "ok");
    if (!reading && !v.held) return h("span", { class: "hint" }, "No node's query log is being read, so who asked isn't known.");
    const oldest = str(v.oldest);
    return h("span", { class: "hint" }, `Not in the nodes' recent query logs (the controller keeps their last ${Number(v.cap || 0).toLocaleString("en-US")} queries${oldest ? `; the oldest is from ${ago(oldest)}` : ""}).`);
  }
  const names = new Map(arr(v.nodes).map(n => [str(n.key), str(n.name)]));
  const rows = list.map(a => {
    const addr = typeof a.client === "string" ? a.client : "";
    const name = str(a.name);
    const who = addr
      ? [h("a", { class: "dev", href: qlLink("client", addr), title: `Only ${name || addr} in the query log` }, name || addr), name ? h("span", { class: "mono small muted" }, addr) : null]
      : [h("span", {}, "A device ", h("span", { class: "small muted" }, "(its node doesn't keep addresses)"))];
    const through = arr(a.nodes).map(k => nodeText(str(k), names.get(str(k))));
    const facts = [times(Number(a.queries) || 0), ago(str(a.last)), through.length ? `through ${andList(through)}` : ""].filter(Boolean).join(" · ");
    return h("li", {}, h("div", { class: "who" }, ...who, a.gateway ? h("span", { class: "tag" }, "your gateway") : null),
      h("span", { class: "small muted" }, facts),
      a.gateway ? h("span", { class: "hint" }, "It asks for the devices behind it, so they aren't shown. To see each one, have that network's DHCP hand out your nodes as DNS servers (",
        h("a", { href: "./" }, "Copy DNS servers"), " on Nodes).") : null);
  });
  const more = Number(v.more) || 0;
  return h("div", { class: "askers" },
    h("span", { class: "hint" }, "Who asked, the most recent first (", h("a", { href: qlLink("name", site) }, `every query for ${site}`), "):"),
    h("ul", {}, rows),
    more ? h("a", { class: "small", href: qlLink("name", site) }, `and ${more} more`) : null);
}

async function checkSite(raw) {
  const name = raw.trim().toLowerCase().replace(/\.$/, "");
  const box = $("verdict");
  if (!name) { box.replaceChildren(); return; }
  const seq = ++checkSeq;
  box.replaceChildren(h("p", { class: "hint" }, `Checking ${name}…`));
  let r, asked = null;
  try {
    [r, asked] = await Promise.all([
      getJSON(`api/blocking/check?name=${encodeURIComponent(name)}`),
      getJSON(`api/querylog/askers?name=${encodeURIComponent(name)}`).catch(() => null),
    ]);
  } catch (e) {
    if (seq === checkSeq) box.replaceChildren(h("div", { class: "verdict" }, h("span", { class: "hint bad" }, e.message.replace(/^api\/[^:]*: /, ""))));
    return;
  }
  if (seq !== checkSeq) return;
  const c = obj(r.check), site = str(c.name) || name;
  const entry = obj(arr(c.entries)[0]);
  const as = str(entry.name) && entry.name !== site ? ` (as ${entry.name}, with everything under it)` : "";
  const s = obj(st.setup);
  const waits = [...arr(s.allowed).filter(x => x.waiting).map(x => [x.site, "always allowed"]), ...arr(s.blocked).filter(x => x.waiting).map(x => [x.site, "always blocked"])]
    .filter(([x]) => x === site || site.endsWith(`.${x}`));
  const waitLine = waits.length ? h("span", { class: "hint" }, `Waiting to apply: ${waits[0][0]} ${waits[0][1]}. Until then the nodes answer as above.`) : null;
  const btn = (text, body) => {
    const b = h("button", { class: "btn", type: "button", disabled: !can() }, text);
    b.addEventListener("click", async () => { b.disabled = true; if (await edit(body)) checkSite(site); else b.disabled = false; });
    return b;
  };
  let v;
  if (c.result === "blocked" && c.by === "overrides") {
    v = h("div", { class: "verdict blocked" }, h("b", {}, `${site} is blocked`),
      h("span", {}, `It's one of your always-blocked sites${as}: blocked even while blocking is paused.`),
      waitLine, h("div", { class: "row" }, btn(`Stop blocking ${entry.name || site}`, { op: "unblock", site: entry.name || site })));
  } else if (c.result === "allowed") {
    v = h("div", { class: "verdict" }, h("b", {}, `${site} is allowed`),
      h("span", {}, `It's one of your always-allowed sites${as}: no list blocks it.`),
      waitLine, h("div", { class: "row" }, btn(`Stop allowing ${entry.name || site}`, { op: "unallow", site: entry.name || site })));
  } else if (c.result === "blocked") {
    const titles = arr(r.sources).map(x => str(x.title));
    const by = [];
    titles.forEach((t, i) => { if (i) by.push(i === titles.length - 1 ? " and " : ", "); by.push(h("b", {}, t)); });
    const ns = arr(r.nodes);
    const yes = ns.filter(n => n.blocks).map(n => nodeText(n.host));
    const no = ns.filter(n => !n.blocks).map(n => `${nodeText(n.host)} (${WHY[n.why] || str(n.why)})`);
    const where = [yes.length ? `Blocked on ${andList(yes)}` : "Blocked on no node now", no.length ? `not on ${andList(no)}` : ""].filter(Boolean).join("; ");
    v = h("div", { class: "verdict blocked" }, h("b", {}, `${site} is blocked`),
      h("span", {}, "By ", ...by, `${as}.`), h("span", {}, `${where}.`),
      waitLine, h("div", { class: "row" }, btn(`Allow ${site}`, { op: "allow", site })));
  } else {
    const why = arr(c.entries).length ? "A list you use allows it." : !c.list ? "No blocklist is set up yet."
      : c.indexed ? "No list you use has it." : "The lists can be checked once a blocking change is applied.";
    v = h("div", { class: "verdict" }, h("b", {}, `${site} is not blocked`), h("span", {}, why),
      waitLine, h("div", { class: "row" }, btn(`Block ${site}`, { op: "block", site })));
  }
  if (c.compiled && c.by !== "overrides") v.append(h("span", { class: "hint" }, `From the lists sent to the nodes ${ago(str(c.compiled))}.`));
  const who = askersBox(site, asked);
  if (who) v.append(who);
  box.replaceChildren(v);
}

$("checkform").addEventListener("submit", e => {
  e.preventDefault();
  const q = $("checkq").value;
  if (q.trim()) history.replaceState(null, "", `#check=${encodeURIComponent(q.trim())}`);
  checkSite(q);
});

// The last site blocked, from the query log: what "Check a site" starts with.
async function lastBlocked() {
  try {
    const r = await getJSON("api/querylog?result=blocked&limit=1");
    const e = arr(r.entries)[0];
    return e ? str(e.qname).replace(/\.$/, "") : "";
  } catch { return ""; }
}

// ---- the rules and the lists: hide and expand -------------------------------------------

// keep redraws a box of details rows, keeping each one the user opened or closed as they
// left it (the others as drawn: the rules open when they have sites), and not while
// something in it is being typed in.
const userOpen = new Map();
function keep(box, rows) {
  const a = document.activeElement;
  if (a && box.contains(a) && ["INPUT", "TEXTAREA", "SELECT"].includes(a.tagName) && a.type !== "checkbox") return;
  box.replaceChildren(...rows);
  for (const d of box.querySelectorAll("details[data-key]")) {
    const k = d.dataset.key;
    if (userOpen.has(k)) d.open = userOpen.get(k);
    d.querySelector("summary").addEventListener("click", () => setTimeout(() => userOpen.set(k, d.open)));
  }
}

function siteRow(kind, x) {
  const rm = h("button", { class: "btn small", type: "button", disabled: !can(), "aria-label": `Remove ${x.site}` }, "Remove");
  rm.addEventListener("click", async () => { rm.disabled = true; if (!await edit({ op: kind === "allow" ? "unallow" : "unblock", site: x.site })) rm.disabled = false; });
  return h("div", { class: "change" }, h("span", {}, h("span", { class: "mono" }, x.site), x.waiting ? [" ", h("span", { class: "tag wait" }, "waiting to apply")] : null), rm);
}

function ruleRow(kind) {
  const s = obj(st.setup);
  const sites = arr(kind === "allow" ? s.allowed : s.blocked);
  const input = h("input", { class: "t mono", id: kind === "allow" ? "addallow" : "addblock", placeholder: "site.example", spellcheck: "false", autocapitalize: "off",
    "aria-label": kind === "allow" ? "Site to allow" : "Site to block" });
  const form = h("form", { class: "check", autocomplete: "off" }, input, h("button", { class: "btn", type: "submit", disabled: !can() }, kind === "allow" ? "Allow" : "Block"));
  form.addEventListener("submit", async e => {
    e.preventDefault();
    if (!input.value.trim()) return;
    if (await edit({ op: kind, site: input.value })) { input.value = ""; input.blur(); renderRules(); }
  });
  const empty = kind === "allow" ? "None. Allow a site here, or from Check a site." : "None. Block a site no list covers.";
  const hint = kind === "allow" ? "Never blocked, nor anything under them, whatever the lists say." : "Blocked, and everything under them, even while blocking is paused.";
  const d = h("details", { class: "ex", "data-key": kind, open: sites.length > 0 },
    h("summary", {}, h("b", {}, kind === "allow" ? "Always allowed" : "Always blocked"), h("span", { class: "val" }, sites.length ? plural(sites.length, "site", "sites") : "None")),
    h("div", { class: "exb" }, form, sites.length ? h("div", {}, sites.map(x => siteRow(kind, x))) : h("p", { class: "hint" }, empty), h("p", { class: "hint" }, hint)));
  return d;
}

function renderRules() { keep($("rules"), [ruleRow("allow"), ruleRow("block")]); }

function listRow(l) {
  const s = obj(st.setup), rep = s.report;
  const last = l.on && !l.allow && inUse().length === 1;
  const val = !l.on ? "Not in use" : l.waiting ? "after apply" : l.counted ? `${fmt(l.domains)} domains` : "–";
  const sw = h("button", { class: "sw", type: "button", role: "switch", "aria-checked": String(!!l.on), "aria-label": `Use ${l.title}`, disabled: !can() || last });
  if (last) sw.title = "The last list in use: pause blocking instead, or turn it off in a node's settings";
  sw.addEventListener("click", async () => { sw.disabled = true; if (!await edit({ op: l.on ? "unuse" : "use", source: l.source })) sw.disabled = false; });
  const rm = h("button", { class: "btn small danger push-right", type: "button", disabled: !can() || last }, "Remove list");
  if (last) rm.title = sw.title;
  rm.addEventListener("click", async () => { rm.disabled = true; if (!await edit({ op: "remove", source: l.source })) rm.disabled = false; });
  const updated = l.waiting ? "After the next apply" : l.download && l.on && l.updated ? ago(str(l.updated)) : rep && l.on ? ago(str(rep.time)) : "–";
  const checked = l.download && l.on && !l.waiting ? [h("dt", {}, "Checked"), h("dd", {}, l.checked ? ago(str(l.checked)) : "Not yet")] : null;
  return h("details", { class: "ex", "data-key": `list:${l.source}` },
    h("summary", {}, h("span", {}, h("b", {}, str(l.title)), l.what ? h("span", { class: "what" }, ` · ${l.what}`) : null,
      l.allow ? [" ", h("span", { class: "tag" }, "allows")] : null, l.waiting ? [" ", h("span", { class: "tag wait" }, "waiting to apply")] : null),
    h("span", { class: "val num" }, val)),
    h("div", { class: "exb" },
      h("dl", { class: "kv" },
        h("dt", {}, "Source"), h("dd", { class: "mono" }, str(l.shown).replace(/^[a-z]+:/, "")),
        h("dt", {}, "Read as"), h("dd", {}, kindWords(specKind(l.source))),
        l.by ? [h("dt", {}, "Kept by"), h("dd", {}, str(l.by))] : null,
        h("dt", {}, "Updated"), h("dd", {}, updated),
        checked,
        h("dt", {}, "Domains"), h("dd", { class: "num" }, l.counted && l.on ? fmt(l.domains) : "–")),
      h("div", { class: "row" }, l.allow ? null : h("span", { class: "row small" }, sw, l.on ? "In use" : "Not in use"), rm)));
}

function renderLists() {
  const s = obj(st.setup);
  keep($("lists"), lists().map(listRow));
  const hint = [];
  if (inUse().length > 1) hint.push("Overlapping domains are counted once.");
  const rooms = nodesOf().filter(n => n.room >= 0).sort((a, b) => a.room - b.room);
  if (rooms.length) {
    const n = rooms[0];
    hint.push(n.room > 0 ? `${nodeText(n.host)} has room for about ${short(n.room)} more domains.` : `${nodeText(n.host)} has no room for more domains.`);
  }
  if (arr(s.others).length) hint.push(`Also defined, left as they are: ${arr(s.others).join(", ")} (see Advanced).`);
  $("lists-hint").textContent = hint.join(" ");
}

// ---- the schedule: lists downloaded are checked for a new version ------------------------
// (refresh.go): how often, when last checked and when next, Refresh now; a version refused
// by the size-change check, a check that failed, or one waiting for the pending changes.

const EVERY = { off: "Never", "6h": "Every 6 hours", "12h": "Every 12 hours", "24h": "Every day", "168h": "Every week" };

function atWords(t) {
  const d = new Date(t);
  if (!t || Number.isNaN(d.getTime()) || t.startsWith("0001")) return "";
  return untilWords(Math.max(0, (d.getTime() - Date.now()) / 1000));
}

function renderRefresh() {
  const box = $("refresh");
  const r = obj(obj(st.setup).refresh);
  if (!r.downloads) { box.hidden = true; box.replaceChildren(); return; }
  box.hidden = false;
  if (document.activeElement && document.activeElement.id === "refresh-every") return; // being chosen
  const every = str(r.every) || "24h";
  const choices = arr(r.choices).map(str);
  if (!choices.includes(every)) choices.push(every);
  const sel = h("select", { class: "t", id: "refresh-every", "aria-label": "Check the downloaded lists for new versions", disabled: !can() },
    choices.map(c => h("option", { value: c, selected: c === every }, EVERY[c] || `Every ${c}`)));
  sel.addEventListener("change", async () => {
    sel.disabled = true;
    try {
      await post("api/blocking/refresh/schedule", { every: sel.value });
      toast(sel.value === "off" ? "The lists are no longer checked for new versions" : `The lists are checked ${(EVERY[sel.value] || `every ${sel.value}`).toLowerCase()}`);
    } catch (e) { toast(e.message); }
    await readSetup();
  });
  const busy = !!r.checking || !!r.sending;
  const now = h("button", { class: "btn", type: "button", id: "refresh-now", disabled: !can() || busy }, r.checking ? "Checking…" : "Refresh now");
  now.addEventListener("click", async () => {
    now.disabled = true;
    try { await post("api/blocking/refresh", {}); toast("Checking the lists for new versions"); }
    catch (e) { toast(e.message); }
    await readSetup();
  });
  const when = [];
  if (r.sending) when.push("Sending a new version to the nodes…");
  else if (r.checking) when.push("Checking now…");
  else {
    when.push(r.checked && !str(r.checked).startsWith("0001") ? `Checked ${ago(str(r.checked))}` : "Not checked yet");
    if (every !== "off" && r.next) when.push(`next ${atWords(str(r.next))}`);
    else if (every === "off") when.push("only when you refresh or apply a change");
  }
  const rows = [h("div", { class: "row spread" },
    h("label", { class: "row" }, h("span", {}, "Check for new versions"), sel),
    h("div", { class: "row" }, h("span", { class: "small muted", id: "refresh-when" }, when.join(" · ")), now))];
  const outcome = str(r.outcome), msg = str(r.message);
  if (outcome === "refused") {
    const over = arr(r.over).map(o => str(o).replace(/\d{4,}/g, d => fmt(Number(d))).replace(/^(blocked|allowed|bytes) /, (_, w) => `${SIZEWORDS[w]}: `));
    const take = h("button", { class: "btn", type: "button", id: "refresh-take", disabled: !can() || busy }, "Send it anyway");
    take.addEventListener("click", async () => {
      take.disabled = true;
      try { await post("api/blocking/refresh", { list: str(r.list), accept_change: true }); toast("Sending the new version, taking the change in size"); }
      catch (e) { toast(e.message); }
      await readSetup();
    });
    rows.push(h("div", { class: "stack", id: "refresh-refused" },
      h("p", { class: "bad" }, `Not updated: the new version changed more than its limit (${over.join(", ") || "its size"}).`),
      h("p", { class: "hint" }, "A list that shrinks a lot often failed to download. The nodes keep the lists they have, and the next check tries a newer version. If the change is expected, send it anyway."),
      h("div", { class: "row" }, take)));
  } else if (outcome === "failed" && msg) {
    rows.push(h("p", { class: "hint bad", id: "refresh-failed" }, `The last check failed: ${msg}. It's tried again within an hour; the nodes keep the lists they have.`));
  } else if (outcome === "waiting") {
    rows.push(h("p", { class: "hint", id: "refresh-waiting" }, "Waiting for your changes: applying them fetches the lists."));
  }
  box.replaceChildren(...rows);
}

// ---- advanced: each node, the files, the terminal ----------------------------------------

const NODEWORDS = { on: "On", paused: "Paused", off: "Off in its settings", none: "No list yet", loading: "Loading its list", failed: "Its list failed", unknown: "Not read yet" };

async function nodeJob(btn, params, kind, said) {
  btn.disabled = true;
  try {
    const j = await post("api/jobs", { kind, params });
    const end = await waitJob(j.id);
    toast(end.state === "done" ? said : `${nodeText(params.node)}: ${str(end.error) || end.state}`);
  } catch (e) { toast(e.message); }
  btn.disabled = false;
  readSetup();
}

function nodeRows() {
  return nodesOf().map(n => {
    const words = [NODEWORDS[n.state] || n.state];
    if (n.state === "paused") words.push(`until ${untilWords(n.paused_s)}`);
    if (n.entries) words.push(`${fmt(n.entries)} domains`);
    if (n.state === "on") words.push(n.current ? "the lists as last applied" : "an older list");
    if (n.error) words.push(n.error);
    if (!n.online) words.push("not answering");
    const b = (text, kind, params, said) => {
      const x = h("button", { class: "btn small", type: "button", disabled: !can() || !n.online });
      x.textContent = text;
      x.addEventListener("click", () => nodeJob(x, { node: n.host, ...params }, kind, said));
      return x;
    };
    const acts = [];
    if (n.state === "paused") acts.push(b("Resume", "pause", { seconds: 0 }, `Blocking resumed on ${nodeText(n.host)}`));
    else if (n.state === "on") acts.push(b("Pause 5 min", "pause", { seconds: 300 }, `Blocking paused for 5 minutes on ${nodeText(n.host)}`));
    if (n.state === "on" || n.state === "paused") {
      acts.push(b("Older list", "revert", { list: "blocklist" }, `${nodeText(n.host)} went back to its older list`));
      acts.push(b("Older own rules", "revert", { list: "overrides" }, `${nodeText(n.host)} went back to its older allowed and blocked sites`));
    }
    return h("div", { class: "nrow" }, h("b", {}, nodeText(n.host)), h("span", { class: "small muted" }, words.join(" · ")), h("div", { class: "row" }, acts));
  });
}

// A file as the pending changes make it, edited as text and saved as a pending change.
function fileEditor(kind, nameOf, summaryOf) {
  const ta = h("textarea", { class: "t code", spellcheck: "false", "aria-label": "Its text" });
  const save = h("button", { class: "btn", type: "button", disabled: true }, "Save as a change");
  const del = h("button", { class: "btn danger", type: "button", hidden: kind !== "source", disabled: true }, "Delete as a change");
  const upload = h("input", { type: "file", "aria-label": "Replace from a file", hidden: true });
  const note = h("p", { class: "hint" });
  let cur = null;
  async function load() {
    const name = nameOf();
    cur = null;
    save.disabled = del.disabled = true;
    note.className = "hint";
    if (!name) { ta.value = ""; note.textContent = ""; return; }
    try {
      cur = await getJSON(`api/changes/file?kind=${kind}&name=${encodeURIComponent(name)}`);
      ta.value = str(cur.text);
      ta.readOnly = !!cur.partial;
      upload.hidden = !cur.partial;
      note.textContent = cur.partial ? `Too big to edit here: its first part is shown. Replace it from a file.` : arr(cur.pending).length ? "As the pending changes make it." : cur.exists ? "" : "A new file.";
      save.disabled = !can() || !!cur.partial;
      del.disabled = !can() || !cur.exists;
    } catch (e) { note.className = "hint bad"; note.textContent = e.message; }
  }
  async function send(body) {
    try {
      await post("api/changes", { kind, name: nameOf(), hash: str(obj(cur).hash), ...body });
      await refreshChanges();
      await readSetup();
      toast(`${summaryOf(body)}: in the changes to apply`);
      load();
    } catch (e) { note.className = "hint bad"; note.textContent = e.message; }
  }
  save.addEventListener("click", () => send({ text: ta.value, summary: summaryOf({}) }));
  del.addEventListener("click", () => send({ delete: true, summary: summaryOf({ delete: true }) }));
  upload.addEventListener("change", async () => {
    const f = upload.files[0];
    if (f) send({ text: await f.text(), summary: summaryOf({}) });
  });
  return { el: h("div", { class: "stack" }, ta, note, h("div", { class: "row" }, save, del, upload)), load };
}

function renderAdvanced() {
  const box = $("advanced");
  if (box.dataset.drawn === "1") {
    const nb = $("adv-nodes");
    if (nb) nb.replaceChildren(...nodeRows());
    return;
  }
  box.dataset.drawn = "1";
  const defs = fileEditor("lists", () => "lists.json", b => (b.delete ? "Delete the list definitions" : "Edit the list definitions"));
  const pick = h("select", { class: "t", id: "srcpick", "aria-label": "Source file" });
  const newName = h("input", { class: "t mono", id: "srcnew", placeholder: "mine.txt", spellcheck: "false", hidden: true, "aria-label": "New file's name" });
  const srcName = () => (pick.value === "+" ? newName.value.trim() : pick.value);
  const src = fileEditor("source", srcName, b => `${b.delete ? "Delete" : "Edit"} blocking source ${srcName()}`);
  pick.addEventListener("change", () => { newName.hidden = pick.value !== "+"; src.load(); });
  newName.addEventListener("change", () => src.load());
  const cmd = h("pre", { class: "cmd", id: "cmd" });
  const copy = h("button", { class: "btn small", type: "button", onclick: async () => toast(await copyText(cmd.textContent) ? "Copied the command" : "Couldn't copy: select it and copy it") }, "Copy");
  async function loadMore() {
    defs.load();
    try {
      const o = await getJSON("api/blocking");
      const files = arr(o.sources).map(f => str(f.name));
      const keepSel = pick.value;
      pick.replaceChildren(h("option", { value: "" }, "Choose a file"), ...files.map(f => h("option", { value: f }, f)), h("option", { value: "+" }, "A new file…"));
      if (keepSel && [...pick.options].some(o => o.value === keepSel)) pick.value = keepSel;
      const l = arr(o.lists).find(x => x.name === obj(st.setup).list);
      cmd.className = "cmd";
      cmd.textContent = l ? str(l.command) : "No blocklist yet.";
    } catch (e) { cmd.className = "cmd bad-text"; cmd.textContent = e.message; }
  }
  const d = h("details", { class: "ex", "data-key": "advanced" },
    h("summary", {}, h("b", {}, "Advanced"), h("span", { class: "val" }, "Each node, the files, the terminal")),
    h("div", { class: "exb" },
      h("h3", {}, "Each node"), h("div", { id: "adv-nodes" }, nodeRows()),
      h("p", { class: "hint" }, "A pause ends when a node restarts. Older list and Older own rules use the copy the node keeps; the next apply sends the newest again."),
      h("h3", {}, "List definitions"),
      h("p", { class: "hint" }, "Every list as JSON: its sources, allow sources, popular and must-resolve names and size limits. The first list is the one this page sets up."),
      defs.el,
      h("h3", {}, "Source files"),
      h("p", { class: "hint" }, "Lists kept here, one name a line: the always allowed and blocked sites are overrides-allow.txt and overrides.txt."),
      h("div", { class: "grid2" }, pick, newName), src.el,
      h("h3", {}, "The same from a terminal"),
      h("p", { class: "hint" }, "The command Apply runs for the first list."),
      cmd, h("div", { class: "row" }, copy)));
  d.addEventListener("toggle", () => { if (d.open) loadMore(); });
  box.replaceChildren(d);
}

// ---- the page ---------------------------------------------------------------------------

let addTop = null, addFirst = null;

function render() {
  const s = st.setup;
  $("loading").hidden = !!s;
  if (!s) {
    if (st.error) $("loading").replaceChildren(h("p", {}, st.error.message.replace(/^api\/[^:]*: /, "")));
    return;
  }
  const state = blockingState();
  $("off").hidden = state !== "off";
  $("on").hidden = state === "off";
  $("add-list").hidden = state === "off";
  if (!addTop) addTop = addListBox($("add-list"), false);
  if (!addFirst) addFirst = addListBox($("add-first"), true);
  renderHead();
  renderAsk();
  if (state === "off") return;
  renderPause();
  renderRules();
  renderLists();
  renderRefresh();
  renderAdvanced();
}

// #list=<name> (a search result): the fleet's list shows its blocklists; another list is
// in Advanced, opened at its definitions. Once the setup is read.
async function goList() {
  const m = location.hash.match(/^#list=(.+)$/);
  if (!m) return;
  let name = "";
  try { name = decodeURIComponent(m[1]); } catch { return; }
  for (let i = 0; i < 50 && !st.setup; i++) await new Promise(r => setTimeout(r, 100));
  if (!st.setup) return;
  if (name === str(st.setup.list)) { $("lists-h").scrollIntoView({ block: "start" }); return; }
  const adv = $("advanced").querySelector("details");
  if (adv) { adv.open = true; adv.scrollIntoView({ block: "start" }); }
}
window.addEventListener("hashchange", goList);

onChanges(p => {
  st.pending = p;
  const ch = JSON.stringify(arr(p.changes).map(c => c.id)) + str(obj(p.apply).state);
  if (st.lastChanges !== undefined && st.lastChanges !== ch) readSetup();
  st.lastChanges = ch;
  renderAsk();
});

session.then(async s => {
  st.sess = s;
  if (!s.logged_in) {
    $("loading").replaceChildren(h("p", {}, s.password_set ? "Log in to see and change blocking." : "Blocking needs a login: set a password first."));
    return;
  }
  every(readSetup);
  every(readDash, 30000);
  goList();
  const m = location.hash.match(/^#check=(.+)$/);
  let q = "";
  if (m) { try { q = decodeURIComponent(m[1]); } catch { q = m[1]; } }
  else q = await lastBlocked();
  if (q) { $("checkq").value = q; checkSite(q); }
});
