// + Add node: the button becomes an address box with what it
// finds right under it, on the network only. Empty, it lists the nodes found on the network
// and not added yet (/api/nodes/found); an address typed is looked up there (POST
// /api/nodes/lookup: that address only, nothing resolved). A new board isn't here: it is
// installed first with Install over USB (usb.js, its own button), which then hands the node
// to next() here once it answers. A node chosen gets a name (a node config from <data>/configs, which
// carries the name) and how it gets its address: DHCP, reserved for it in your router (not
// on a network in settings.json's no_dhcp), or a fixed address with its gateway, checked
// together. An existing config is read (GET /api/configs/<name>) and its network saved back
// to it first (POST, against its hash) when it says another, so the file says what the node
// runs; one no node uses can be deleted there (POST /api/configs/<name>/delete). The node
// is then added by the adoption (adopt.go): its preview,
// the dry run, then the adoption, which adds it to settings.json. Nothing is changed on the
// zone primary. A new name's config starts with the zones to copy
// from the zone primary (settings.json's "primary"). One adopted already is added by the settings-add job.
// Esc, or a click outside with nothing typed or chosen, closes it.
//
// A found node's setup panel (setup): what it is, and the steps it needs, in order, the next
// one a click away: firmware from before node configs (before 0.0.1) is updated over USB
// first (the Install over USB panel, its board and chip image chosen: a USB install erases
// the board), then
// the node is added (the flow above, with it chosen). No found node is a dead end.
import { h, arr, obj, str, hostOnly, toast, getJSON, post, session, events, nodeText, mac, addr, panel, MACHINT } from "./shell.js";
import { openUSB, usbFor } from "./usb.js";

const NEW = "+new"; // the name select's "A new name…"
const NAME = /^[a-z0-9]([a-z0-9-]{0,29}[a-z0-9])?$/;
const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;
// STATIC is the address a node added here gets: IPv4 with its prefix length (1-32), never dhcp.
const STATIC = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}\/([1-9]|[12]\d|3[0-2])$/;
const STATICHINT = "A fixed address, with its prefix length, like 192.0.2.10/24.";
const linkWord = k => (k === "ethernet" ? "Wired" : k === "wifi" ? "Wi-Fi" : "");

// IPv4 as a number, and a.b.c.d/nn as {addr, bits, net, last}: for the address and gateway
// checks, the node's own (nodecfg, Network.Check).
const ip4 = s => (IPV4.test(str(s)) ? s.split(".").reduce((n, o) => n * 256 + Number(o), 0) : null);
const dotted = n => [24, 16, 8, 0].map(s => Math.floor(n / 2 ** s) % 256).join(".");
function prefix(s) {
  const m = /^([^/]+)\/(\d+)$/.exec(str(s));
  const a = m && ip4(m[1]), bits = m ? Number(m[2]) : -1;
  if (a === null || !m || bits < 0 || bits > 32) return null;
  const size = 2 ** (32 - bits), net = a - (a % size);
  return { addr: a, bits, net, last: net + size - 1 };
}
const inNet = (a, p) => a >= p.net && a <= p.last;
const netText = p => `${dotted(p.net)}/${p.bits}`;

// netCheck is what is wrong with a fixed address and its gateway together, in words, as
// the node checks them; "" when they go together. Field: "address" or "gateway".
export function netCheck(address, gateway) {
  const p = STATIC.test(str(address)) ? prefix(address) : null;
  if (!p) return { field: "address", why: STATICHINT };
  if (p.bits < 8 || p.bits > 30) return { field: "address", why: "The prefix length is 8 to 30, like /24." };
  if (p.addr === p.net || p.addr === p.last) return { field: "address", why: `${dotted(p.addr)} is the network or broadcast address: pick another.` };
  const own = `the node's own network, ${netText(p)}`;
  if (!str(gateway)) return { field: "gateway", why: `Give its gateway: an address in ${own}.` };
  const g = ip4(gateway);
  if (g === null) return { field: "gateway", why: "The gateway is an IPv4 address, like 192.0.2.1." };
  if (!inNet(g, p)) {
    return { field: "gateway", why: `${gateway} isn't in ${own}. The gateway has to be in that network.` };
  }
  if (g === p.addr) return { field: "gateway", why: "The gateway can't be the node's own address." };
  return null;
}

// follow resolves with a job once it ends, calling onProgress with its progress meanwhile;
// a job the controller no longer has (it restarted) ends it as failed.
function follow(job, onProgress) {
  return new Promise(resolve => {
    const es = events(`api/jobs/${encodeURIComponent(job.id)}/events`);
    es.addEventListener("state", e => { const s = JSON.parse(e.data); if (s.progress) onProgress(s.progress); });
    es.addEventListener("progress", e => onProgress(JSON.parse(e.data)));
    es.addEventListener("end", e => { const s = JSON.parse(e.data); es.close(); if (s.progress) onProgress(s.progress); resolve(s); });
    es.addEventListener("gone", () => resolve({ ...job, state: "failed", error: "The controller no longer has this job: it may have restarted. Try again." }));
  });
}

// viaUSB: a found node moved on only over USB: firmware from before node configs ("usb"),
// a node with no keys (setup mode, "keys": Write keys only, which keeps its settings), or
// one that trusts another controller's release key ("reinstall": installed again, with yours).
const viaUSB = f => f.install === "usb" || f.install === "keys" || f.install === "reinstall";

// usbButton is "Update over USB" for a found node (or "Write keys over USB" for one with no
// keys, "Reinstall over USB" for one that trusts another controller's key): the Install over
// USB panel with its board, chip image and address filled in.
// before, if given, runs first (the address box closes: the panel is the next step); found,
// if given, gets the node once it answers on the network after the install.
export function usbButton(f, cls, name, before, found) {
  const again = f.install === "reinstall";
  return h("button", { class: cls, type: "button", onclick: () => { if (before) before(); openUSB({ ...usbFor(f, name), ...(again ? { update: false, reinstall: true } : {}), found }); } },
    f.install === "keys" ? "Write keys over USB" : again ? "Reinstall over USB" : "Update over USB");
}

// addNode draws + Add node into box. opts: found() the nodes found, nodes() the listed
// ones, boardName(b), nodeKey(n), imageVersion(image) the version a USB install of that chip
// image gives ("" unknown), done() after a node is added. It returns {open(found), setup(found),
// next(found)}: next is Install over USB's found, the node it installed.
export function addNode(box, opts) {
  const st = { open: false, sel: null, q: "", look: null, src: null, form: null, flow: null, sess: {} };
  session.then(s => { st.sess = s; });
  let input = null, list = null, timer = null, seq = 0;

  const button = h("button", { class: "btn", type: "button", onclick: () => open(null) }, "+ Add node");
  box.append(button);

  function open(sel) {
    st.open = true;
    st.sel = null;
    st.q = "";
    st.look = null;
    st.flow = null;
    input = h("input", { class: "t mono", id: "nq", type: "text", autocomplete: "off", spellcheck: "false", placeholder: "An address, or pick below",
      "aria-label": "Node to add", "aria-controls": "nqres" });
    list = h("div", { class: "combo-list zpanel", id: "nqres", role: "region", "aria-label": "Add a node" });
    input.addEventListener("input", typed);
    input.addEventListener("keydown", e => {
      if (e.key === "Escape") { e.preventDefault(); close(); }
      if (e.key === "Enter") { e.preventDefault(); clearTimeout(timer); lookup(true); }
    });
    box.replaceChildren(h("div", { class: "combo zcombo ncombo" }, input, list));
    if (sel) choose(sel); else render();
    input.focus();
  }

  function close() {
    if (st.flow && st.flow.busy) return;
    clearTimeout(timer);
    seq++;
    st.open = false;
    box.replaceChildren(button);
  }

  // A click outside closes it while nothing is typed or chosen.
  document.addEventListener("mousedown", e => {
    if (st.open && !e.target.closest(".ncombo") && !st.q.trim() && !st.sel) close();
  });
  document.addEventListener("keydown", e => {
    if (st.open && e.key === "Escape" && !e.target.closest(".ncombo") && !document.querySelector(".drawer")) close();
  });

  function typed() {
    st.q = input.value.trim();
    st.sel = null;
    st.flow = null;
    st.look = null;
    clearTimeout(timer);
    seq++; // a lookup still on its way is for text no longer typed
    if (IPV4.test(st.q) && !known(st.q)) {
      st.look = { addr: st.q, busy: true };
      timer = setTimeout(() => lookup(false), 600);
    }
    render();
  }

  // known is a listed node the text names (its address or its name).
  function known(q) {
    const t = q.toLowerCase();
    return opts.nodes().find(n => hostOnly(n.addr) === q || nodeText(n).toLowerCase() === t);
  }

  async function lookup(now) {
    const q = st.q;
    if (!q || known(q)) { render(); return; }
    if (!IPV4.test(q)) {
      if (now) { st.look = { addr: q, error: "Type an IPv4 address, like 192.0.2.10." }; render(); }
      return;
    }
    const my = ++seq;
    st.look = { addr: q, busy: true };
    render();
    try {
      const r = await post("api/nodes/lookup", { address: q });
      if (my !== seq || st.q !== q) return;
      st.look = { addr: q, reply: r };
      if (r.found && r.node && r.node.next) { choose(r.node); return; }
    } catch (e) {
      if (my !== seq || st.q !== q) return;
      st.look = { addr: q, error: e.message };
    }
    render();
  }

  async function choose(f) {
    st.sel = f;
    st.flow = null;
    st.form = null;
    render();
    if (f.next === "adopt") {
      let src;
      try { src = await getJSON("api/adopt"); } catch (e) { src = { error: e.message }; }
      if (st.sel !== f) return; // another node chosen meanwhile
      st.src = src;
      const cs = arr(st.src.configs);
      const c = cs.find(x => x.name === f.config && !x.error) || null;
      // How it gets its address starts as it runs now: DHCP (reserved in your router) for
      // a node on DHCP, unless its network has none (settings.json no_dhcp); its fixed
      // address for one on a static address; else fixed, from the config if it says.
      const noDHCP = noDHCPAt(f.host);
      const says = runsOn(f) ? "fixed" : str(f.address) === "dhcp" ? "dhcp" : "";
      st.form = { config: c ? c.name : "", address: runsOn(f), gateway: runsOn(f) ? str(f.gateway) : "", noDHCP, says,
        how: says === "dhcp" && !noDHCP ? "dhcp" : "fixed", file: null };
      render();
      // (render may have made it a new name: no config file has one to read.)
      if (st.form.config && st.form.config !== NEW) readConfig(f, st.form.config);
    }
  }

  function config() { return arr(obj(st.src).configs).find(c => c.name === (st.form && st.form.config)); }
  // The address box's start: the static address the node runs on, with its prefix length;
  // empty for one on dhcp (or none). Choosing a name never changes the box.
  function runsOn(f) {
    const a = str(f.address);
    return STATIC.test(a) ? a : "";
  }
  // noDHCPAt is the network in settings.json's no_dhcp (GET /api/adopt) that host is on, if any.
  function noDHCPAt(host) {
    const a = ip4(hostOnly(host));
    if (a === null) return "";
    return arr(obj(st.src).no_dhcp).find(n => { const p = prefix(n); return p && inNet(a, p); }) || "";
  }

  // readConfig reads the config file chosen (GET /api/configs/<name>: its text, the Wi-Fi
  // password hidden, and its hash), for its network to start the form from and to be saved
  // back to it. Its gateway fills the Gateway box, checked against the address like one typed.
  async function readConfig(f, name) {
    const form = st.form;
    form.file = { name, busy: true };
    render();
    let file;
    try {
      const c = await getJSON(`api/configs/${encodeURIComponent(name)}`);
      const data = JSON.parse(str(c.text));
      file = { name, hash: str(c.hash), data: obj(data), net: obj(obj(data).network) };
    } catch (e) { file = { name, error: e.message }; }
    if (st.sel !== f || st.form !== form || form.config !== name) return; // another node or name chosen meanwhile
    form.file = file;
    const net = obj(file.net);
    if (net.address === "dhcp") {
      if (!form.says && !form.noDHCP) form.how = "dhcp";
    } else if (net.gateway) {
      // The config's gateway, unless it isn't in the address's network and the one the
      // node runs with is: then that one (saved to the file). Else the config's is shown,
      // marked, to be put right here.
      const p = prefix(form.address), fits = g => !!p && ip4(g) !== null && inNet(ip4(g), p);
      const ng = runsOn(f) ? str(f.gateway) : "";
      form.gateway = !fits(net.gateway) && fits(ng) ? ng : str(net.gateway);
    }
    render();
  }

  // ---- what the panel shows ------------------------------------------------------------

  function render() {
    if (!list) return;
    list.replaceChildren(...body());
  }

  function body() {
    if (!st.sess.logged_in) {
      return [h("p", { class: "hint" }, st.sess.password_set ? "Log in to add a node." : "Set a password and log in to add a node.")];
    }
    if (st.sel) return chosen(st.sel);
    if (st.q) {
      const k = known(st.q);
      if (k) {
        return [h("div", { class: "found" }, h("b", {}, nodeText(k)), h("span", { class: "small" }, "Already one of your nodes, at ", h("span", { class: "mono" }, hostOnly(k.addr)), ".")),
          h("div", { class: "actions" }, h("a", { class: "btn small", href: `node.html#${encodeURIComponent(opts.nodeKey(k))}` }, "Open it"))];
      }
      const l = st.look;
      if (!l) return [h("p", { class: "hint" }, "Type the node's whole address, like 192.0.2.10, then Enter.")];
      if (l.busy) return [h("div", { class: "row small" }, h("span", { class: "spin", "aria-hidden": "true" }), "Looking for a node at ", h("span", { class: "mono" }, l.addr), "…")];
      if (l.error) return [h("div", { class: "found" }, h("b", { class: "mono" }, l.addr), h("span", { class: "small bad" }, l.error))];
      const r = obj(l.reply);
      if (r.listed) {
        return [h("div", { class: "found" }, h("b", {}, r.node ? nodeText(r.node.host) : l.addr), h("span", { class: "small" }, "Already one of your nodes, at ", h("span", { class: "mono" }, l.addr), "."))];
      }
      if (r.found && r.node) return chosen(r.node);
      return [h("div", { class: "found" }, h("b", { class: "mono" }, l.addr),
        h("span", { class: "small" }, r.answered ? "Something answers there, but it isn't an espDNS node."
          : "No espDNS node answers there. A new board is installed with Install over USB first, then added here.")),
      h("div", { class: "actions" }, usbNew())];
    }
    const found = arr(opts.found());
    // Every item acts on a click anywhere on it, or Enter: a found node opens its setup
    // panel (what it needs next, a click away); its button does that step itself.
    const rows = found.map(f => item(() => pick(f), `Set up ${opts.boardName(f.board) || str(f.hostname) || f.host}`,
      h("div", {}, h("b", {}, opts.boardName(f.board) || str(f.hostname) || f.host),
        h("div", { class: "tagline" }, h("span", { class: "mono" }, hostOnly(f.host)), [linkWord(f.net), f.version && `firmware ${f.version}`].filter(Boolean).map(x => ` · ${x}`).join("")),
        f.next ? null : h("div", { class: "tagline" }, blocker(f))),
      f.next ? h("button", { class: "btn small", type: "button", onclick: () => choose(f) }, "Add")
        : viaUSB(f) ? usbButton(f, "btn small", opts.boardName(f.board) || str(f.hostname), close, next)
          : h("button", { class: "btn small", type: "button", onclick: () => pick(f) }, "Set up")));
    return [h("div", { class: "stack nogap" },
      h("span", { class: "small muted" }, "Found on your network"),
      rows.length ? rows : h("p", { class: "hint" }, "Nothing new right now. A new board is installed with Install over USB first, then added here."),
      rows.length ? null : h("div", { class: "actions" }, usbNew())),
    h("span", { class: "hint" }, "Or type the address of a node not listed here.")];
  }

  // usbNew opens Install over USB for a new board: what it installs comes back to next.
  function usbNew() {
    return h("button", { class: "btn small", type: "button", onclick: () => { close(); openUSB({ found: next }); } }, "Install over USB");
  }

  // item is one choice in the list: a click anywhere on it (its own buttons and links do
  // their own thing) or Enter on it does act.
  function item(act, label, ...kids) {
    return h("div", { class: "opt pick", tabindex: "0", title: label,
      onclick: e => { if (!e.target.closest("button, a, input")) act(); },
      onkeydown: e => { if (e.key === "Enter" && e.target === e.currentTarget) { e.preventDefault(); act(); } } }, ...kids);
  }

  // pick is a found node chosen from the list: its setup panel, as its row opens.
  function pick(f) {
    close();
    setup(f);
  }

  // next is a node just installed over USB, once it answers on the network: chosen here, as
  // its Add does, when it can be added; else its setup panel, which says what it needs.
  function next(f) {
    if (f.next && !viaUSB(f)) open(f); else setup(f);
  }

  const back = () => h("button", { class: "btn small link", type: "button", onclick: () => { st.sel = null; st.flow = null; st.q = ""; st.look = null; input.value = ""; render(); input.focus(); } }, "Back");

  // intro is the node chosen or looked up: what it is, where, and its MAC to copy (for a
  // DHCP reservation, or to set a static address).
  function intro(f) {
    const link = linkWord(f.net);
    const m = mac(f.mac);
    return h("div", { class: "found" }, h("b", {}, opts.boardName(f.board) || str(f.hostname) || "A node"),
      h("span", { class: "small" }, "Found at ", h("span", { class: "mono" }, hostOnly(f.host)), link ? ` over ${link}` : "", f.version ? `, firmware ${f.version}` : "", "."),
      m ? h("div", { class: "macl" }, m, h("span", { class: "hint" }, MACHINT)) : null);
  }

  function chosen(f) {
    if (st.flow) return flowView(f);
    const refusals = arr(f.refusals);
    if (viaUSB(f)) {
      return [intro(f), h("p", { class: "small" }, usbWhy(f)),
        h("div", { class: "actions" }, usbButton(f, "btn primary", opts.boardName(f.board) || str(f.hostname), close, next), back())];
    }
    if (refusals.length || !f.next) {
      return [intro(f), h("p", { class: "small bad" }, `Can't be added yet: ${refusals.join("; ") || "it's one of your nodes already"}.`),
        h("div", { class: "actions" }, back())];
    }
    if (f.next === "settings-move") {
      const go = h("button", { class: "btn primary", type: "button", onclick: () => settingsMove(f) }, "Add node");
      return [intro(f), h("p", { class: "small" }, `It's ${str(f.name) || str(f.hostname) || "that node"}, which settings.json lists at ${f.move}: add it to move its entry there.`),
        h("div", { class: "actions" }, go, back())];
    }
    if (f.next === "settings-add") {
      const go = h("button", { class: "btn primary", type: "button", onclick: () => settingsAdd(f) }, "Add node");
      return [intro(f), h("p", { class: "small" }, `It already runs its own config${f.name ? `, as ${f.name}` : ""}. Add it and it gets your changes and updates too.`),
        h("div", { class: "actions" }, go, back())];
    }
    if (!st.form) return [intro(f), h("div", { class: "row small" }, h("span", { class: "spin", "aria-hidden": "true" }), "Reading the configs…")];
    if (st.src && st.src.error) return [intro(f), h("p", { class: "small bad" }, st.src.error), h("div", { class: "actions" }, back())];
    const cs = arr(st.src.configs);
    // A name of its own: an existing node config (it carries the name), or a new one, written
    // when it is added (POST /api/configs/<name>.json, the name alone: the rest are defaults,
    // changed later on its node page). A config one of your nodes runs by its name is that
    // node's: offered, but not to be picked.
    const usedBy = c => { const m = arr(opts.nodes()).find(x => c.config_name && str(obj(obj(x.status).config).name) === str(c.config_name)); return m ? opts.nodeKey(m) : ""; };
    const free = cs.filter(c => !c.error && !usedBy(c));
    if (!free.length && !st.form.config) st.form.config = NEW;
    const sel = h("select", { class: "t", id: "nnname" },
      free.length ? h("option", { value: "" }, "Choose…") : null,
      cs.map(c => {
        const by = usedBy(c);
        return h("option", { value: c.name, disabled: !!c.error || !!by, selected: c.name === st.form.config },
          `${str(c.config_name) || c.name}${c.error ? ` (${c.error})` : by ? " (in use)" : ""}`);
      }),
      h("option", { value: NEW, selected: st.form.config === NEW }, "A new name…"));
    const nn = h("input", { class: "t mono", id: "nnnew", type: "text", autocomplete: "off", spellcheck: "false", maxlength: "31", placeholder: "dns4",
      value: st.form.newName || "", "aria-label": "The new node's name" });
    const nnField = h("label", { class: "f", hidden: st.form.config !== NEW }, h("span", {}, "New name"), nn,
      h("span", { class: "hint" }, "Lowercase letters, digits and dashes."));
    // How it gets its address: DHCP, reserved for its MAC in your router (not offered on a
    // network in settings.json's no_dhcp), or a fixed address on the node with its gateway.
    const fm = st.form;
    const radio = (v, label) => h("label", { class: "check" },
      h("input", { type: "radio", name: "nnhow", id: `nnhow-${v}`, value: v, checked: fm.how === v,
        onchange: () => { fm.how = v; fixed.hidden = v !== "fixed"; dhcpNote.hidden = v !== "dhcp"; ok(); } }), h("span", {}, label));
    const dhcpNote = h("p", { class: "hint", hidden: fm.how !== "dhcp" },
      `Reserve ${hostOnly(f.host)} for its MAC (copy it above) in your router first.`);
    const ip = h("input", { class: "t mono", id: "nnip", type: "text", autocomplete: "off", spellcheck: "false", "aria-describedby": "nnip-hint" });
    ip.value = fm.address;
    const hint = h("span", { class: "hint", id: "nnip-hint" }, STATICHINT);
    const gw = h("input", { class: "t mono", id: "nngw", type: "text", autocomplete: "off", spellcheck: "false", placeholder: "192.0.2.1", "aria-describedby": "nngw-hint" });
    gw.value = fm.gateway;
    const gwHint = h("span", { class: "hint", id: "nngw-hint" });
    const fixed = h("div", { class: "grid2", hidden: fm.how !== "fixed" },
      h("label", { class: "f" }, h("span", {}, "Address"), ip, hint), h("label", { class: "f" }, h("span", {}, "Gateway"), gw, gwHint));
    const how = h("div", { class: "stack nogap", role: "radiogroup", "aria-label": "How it gets its address" },
      h("span", { class: "small muted" }, "Its address"),
      fm.noDHCP ? h("p", { class: "hint", id: "nnnodhcp" }, `${fm.noDHCP} has no DHCP server (settings): it gets a fixed address.`)
        : radio("dhcp", "DHCP, reserved for it in your router"),
      fm.noDHCP ? null : dhcpNote,
      radio("fixed", "A fixed address on the node"), fixed);
    // The name chosen: what its file says (read, then saved back with the address chosen
    // here), and, for one no node uses, a way to delete it.
    const file = fm.file && fm.file.name === fm.config ? fm.file : null;
    const fileLine = h("p", { class: "hint", id: "nnfile" });
    if (file && file.busy) fileLine.textContent = `Reading ${file.name}…`;
    else if (file && file.error) { fileLine.textContent = `Couldn't read ${file.name}: ${file.error}`; fileLine.classList.add("bad"); }
    else if (fm.deleteError) { fileLine.textContent = `Not deleted: ${fm.deleteError}`; fileLine.classList.add("bad"); }
    fileLine.hidden = !fileLine.textContent;
    const del = file && file.hash && free.some(c => c.name === file.name)
      ? h("button", { class: "btn small link", type: "button", id: "nndel", onclick: () => { fm.confirmDelete = true; fm.deleteError = ""; render(); } }, "Delete this name") : null;
    const go = h("button", { class: "btn primary", type: "button" }, "Add node");
    const saved = h("p", { class: "small", id: "nnsaved" });
    // Add node waits for a name (a new one valid; an existing one read), a release key and
    // the address: DHCP chosen, or a fixed one and its gateway that go together. What
    // doesn't is marked, and said.
    const ok = () => {
      const bad = fm.how === "fixed" ? netCheck(fm.address, fm.gateway) : null;
      const read = fm.config === NEW || (file && file.hash);
      const named = fm.config === NEW ? NAME.test(fm.newName || "") : !!fm.config;
      // Why it is grey, on hover (and, as its description, to a screen reader).
      go.title = [
        !fm.config || (fm.config === NEW && !fm.newName) ? "Pick a name for it." : "",
        fm.config === NEW && fm.newName && !named ? "The new name: lowercase letters, digits and dashes." : "",
        !read && fm.config && fm.config !== NEW ? (file && file.error ? "Its config couldn't be read." : "Reading the config…") : "",
        st.src.key ? "" : "Make or import the release key first (settings).",
        bad ? bad.why : ""].filter(Boolean).join(" ");
      go.disabled = !named || !st.src.key || !read || !!bad;
      saved.textContent = `Saved to ${fm.config === NEW ? named ? `${fm.newName}.json` : "a new config file named after it" : fm.config || "its config"}. The node gets this name and address, copies your zones, then serves like the others.`;
      const addrBad = bad && bad.field === "address";
      hint.textContent = addrBad && fm.address ? bad.why : STATICHINT;
      hint.classList.toggle("bad", !!addrBad && !!fm.address);
      const p = prefix(fm.address);
      const gwBad = bad && bad.field === "gateway" && (fm.gateway || !addrBad);
      gwHint.textContent = gwBad ? bad.why : p && STATIC.test(fm.address)
        ? `An address in ${netText(p)}, the node's own network.` : "An address in the node's own network.";
      gwHint.classList.toggle("bad", !!gwBad && !!fm.gateway);
    };
    // Lowercased as typed, caret kept; not mid-composition (IME), but once it ends.
    const lower = e => {
      if (!e.isComposing && nn.value !== nn.value.toLowerCase()) { const a = nn.selectionStart, b = nn.selectionEnd; nn.value = nn.value.toLowerCase(); nn.setSelectionRange(a, b); }
      fm.newName = nn.value.trim(); ok();
    };
    nn.addEventListener("input", lower);
    nn.addEventListener("compositionend", lower);
    sel.addEventListener("change", () => {
      fm.config = sel.value;
      nnField.hidden = sel.value !== NEW;
      fm.file = null;
      fm.confirmDelete = false;
      fm.deleteError = "";
      if (sel.value && sel.value !== NEW) readConfig(f, sel.value); else render();
    });
    ip.addEventListener("input", () => { fm.address = ip.value.trim(); ok(); });
    gw.addEventListener("input", () => { fm.gateway = gw.value.trim(); ok(); });
    go.addEventListener("click", () => adopt(f));
    ok();
    const out = [intro(f),
      h("div", { class: "grid2" }, h("label", { class: "f" }, h("span", {}, "Name"), sel), nnField),
      del ? h("div", { class: "row" }, del) : null,
      how, fileLine,
      saved].filter(Boolean);
    if (fm.confirmDelete && file && file.hash) {
      out.push(h("div", { class: "alert-box", id: "nndelask" },
        h("p", { class: "small" }, `Delete ${file.name}? No node runs it. It is kept in its history.`),
        h("div", { class: "actions" },
          h("button", { class: "btn primary", type: "button", onclick: () => deleteConfig(f, file) }, `Delete ${file.name}`),
          h("button", { class: "btn small link", type: "button", onclick: () => { fm.confirmDelete = false; render(); } }, "Cancel"))));
    }
    if (f.net === "wifi") out.push(h("span", { class: "hint" }, "A Wi-Fi board is fine as an extra node; wired ones keep DNS steady."));
    if (!st.src.key) out.push(h("p", { class: "hint bad" }, `No release key to sign its config with${st.src.key_error ? `: ${st.src.key_error}` : ""}.`));
    out.push(h("div", { class: "actions" }, go, back()));
    return out;
  }

  // ---- adding ---------------------------------------------------------------------------

  // network is the config's network as chosen here: DHCP, or the fixed address and gateway.
  function network() {
    return st.form.how === "dhcp" ? { address: "dhcp" } : { address: st.form.address, gateway: st.form.gateway };
  }

  // The adoption is sent what the config file says by then: DHCP, reserved, or the fixed
  // address with its gateway, always (never the gateway a config had for another network).
  // "static" has the adoption refuse any other (dhcp unreserved, none: the config's).
  function params(f) {
    const config = st.form.config === NEW ? `${st.form.newName}.json` : st.form.config;
    const n = network();
    return { node: f.host, node_id: f.id, config, ...(n.address === "dhcp" ? { address: "dhcp", reserved: true } : n), static: true };
  }

  // newConfig writes the new name's node config, with its network, unless one by that name
  // is there already (then its network is saved to it, as for one chosen).
  async function newConfig() {
    const file = `${st.form.newName}.json`;
    if (arr(obj(st.src).configs).some(c => c.name === file)) {
      const c = await getJSON(`api/configs/${encodeURIComponent(file)}`);
      await saveNetwork({ name: file, hash: str(c.hash), data: obj(JSON.parse(str(c.text))) });
      return;
    }
    const zp = obj(obj(st.src).primary);
    const sec = str(zp.address) && arr(zp.zones).length ? { secondary: { primary: str(zp.address), zones: arr(zp.zones).map(str) } } : {};
    await post(`api/configs/${encodeURIComponent(file)}`, { text: `${JSON.stringify({ name: st.form.newName, network: network(), ...sec }, null, 2)}\n`, hash: "" });
    st.src.configs = [...arr(obj(st.src).configs), { name: file, config_name: st.form.newName }];
  }

  // saveNetwork saves the network chosen to the config file, when it says another: the rest
  // of it as read (its Wi-Fi password hidden, which the save keeps), against the version read.
  async function saveNetwork(file) {
    const want = network(), had = obj(obj(file.data).network);
    if (str(had.address) === want.address && str(had.gateway) === str(want.gateway)) return;
    const r = await post(`api/configs/${encodeURIComponent(file.name)}`, { text: `${JSON.stringify({ ...file.data, network: want }, null, 2)}\n`, hash: file.hash });
    file.hash = str(r.hash);
    file.data = { ...file.data, network: want };
    file.net = want;
  }

  // deleteConfig deletes a config no node uses (POST /api/configs/<name>/delete, against
  // the version read): the name is then gone from the list.
  async function deleteConfig(f, file) {
    try {
      await post(`api/configs/${encodeURIComponent(file.name)}/delete`, { hash: file.hash });
    } catch (e) {
      if (st.sel !== f) return;
      st.form.confirmDelete = false;
      st.form.deleteError = e.message;
      render();
      return;
    }
    toast(`${file.name} deleted`);
    if (st.sel !== f) return;
    st.src.configs = arr(obj(st.src).configs).filter(c => c.name !== file.name);
    st.form.config = "";
    st.form.file = null;
    st.form.confirmDelete = false;
    render();
  }

  async function adopt(f) {
    const p = params(f);
    const flow = st.flow = { busy: true, what: "Checking its address and config…", progress: null, error: "", hints: [] };
    const show = () => { if (st.flow === flow && st.sel === f) render(); };
    const fail = (msg, hints) => { flow.busy = false; flow.error = msg; flow.hints = arr(hints); show(); };
    show();
    // The config file first: a new name's written, a chosen one's network saved to it when
    // it says another; the adoption then carries what the file says.
    try {
      if (st.form.config === NEW) await newConfig();
      else await saveNetwork(st.form.file);
    } catch (e) { fail(`Couldn't save ${p.config}: ${e.message}`); return; }
    let pv;
    try { pv = await post("api/adopt/preview", p); } catch (e) { fail(e.message); return; }
    const ck = obj(pv.check);
    if (pv.refusal) { fail(pv.refusal); return; }
    if (!ck.ok) { fail(str(ck.error) || str(obj(ck.node).refusal) || "Its config doesn't pass the check."); return; }
    flow.what = "A dry run first: nothing changes yet.";
    show();
    let dry;
    try { dry = await post("api/jobs", { kind: "adopt", params: { ...p, dry_run: true } }); } catch (e) { fail(e.message); return; }
    dry = await follow(dry, pr => { flow.progress = pr; show(); });
    const dp = obj(flow.progress);
    if (dry.state !== "done") { fail(str(dp.outcome) || str(dry.error) || `The check ${dry.state}.`, dp.hints); return; }
    flow.what = `Adding it${pv.how === "new" ? ": it restarts on its new address" : ", no restart"}.`;
    flow.progress = null;
    show();
    let job;
    try { job = await post("api/jobs", { kind: "adopt", params: { ...p, after: dry.id, add_to_settings: true } }); }
    catch (e) { fail(e.message); return; }
    job = await follow(job, pr => { flow.progress = pr; show(); });
    const ap = obj(flow.progress);
    if (job.state !== "done") { fail(str(ap.outcome) || str(job.error) || `The adoption ${job.state}.`, ap.hints); return; }
    flow.busy = false;
    const name = (st.form.config === NEW ? st.form.newName : str(obj(config()).config_name)) || hostOnly(f.host);
    toast(`${name} is added and serving`);
    st.flow = null;
    close();
    opts.done();
  }

  async function settingsAdd(f) {
    const flow = st.flow = { busy: true, what: "Adding it to your nodes…", progress: null, error: "", hints: [] };
    render();
    try {
      let j = await post("api/jobs", { kind: "settings-add", params: { node: f.host } });
      j = await follow(j, () => {});
      if (j.state !== "done") throw new Error(str(j.error) || `It ${j.state}.`);
      flow.busy = false;
      toast(`${f.name || hostOnly(f.host)} is added`);
      st.flow = null;
      close();
      opts.done();
    } catch (e) {
      flow.busy = false;
      flow.error = e.message;
      render();
    }
  }

  // settingsMove moves a node of yours, reinstalled at a new address, into settings.json at
  // that address: the settings-move job (the entry it is listed at moves to where it now is).
  async function settingsMove(f) {
    const flow = st.flow = { busy: true, what: "Moving it to its new address…", progress: null, error: "", hints: [] };
    render();
    try {
      let j = await post("api/jobs", { kind: "settings-move", params: { node: f.host, from: f.move } });
      j = await follow(j, () => {});
      if (j.state !== "done") throw new Error(str(j.error) || `It ${j.state}.`);
      flow.busy = false;
      toast(`${f.name || hostOnly(f.host)} moved to ${hostOnly(f.host)}`);
      st.flow = null;
      close();
      opts.done();
    } catch (e) {
      flow.busy = false;
      flow.error = e.message;
      render();
    }
  }

  function flowView(f) {
    const fl = st.flow;
    const out = [intro(f)];
    const pr = obj(fl.progress);
    if (fl.busy) out.push(h("div", { class: "row small" }, h("span", { class: "spin", "aria-hidden": "true" }), fl.what));
    const steps = arr(pr.steps);
    if (steps.length) {
      out.push(h("ol", { class: "steps" }, steps.map(s => h("li", { class: s.state === "done" ? "done" : s.state === "running" ? "now" : s.state === "failed" ? "failed" : "" },
        h("span", { class: "m" }), h("div", {}, h("b", {}, str(s.title)), s.detail ? h("div", { class: "hint" }, str(s.detail)) : null)))));
    }
    if (fl.error) {
      out.push(h("p", { class: "small bad" }, fl.error));
      if (fl.hints.length) out.push(h("ul", { class: "hint" }, fl.hints.map(x => h("li", {}, str(x)))));
      out.push(h("div", { class: "actions" }, h("button", { class: "btn small", type: "button", onclick: () => { st.flow = null; render(); } }, "Try again"), back()));
    }
    return out;
  }

  // usbWhy is why a node is updated over USB first, and what that does.
  function usbWhy(f) {
    if (f.install === "keys") return "It has no keys (setup mode), so it takes nothing over the network. Writing your keys over USB keeps its firmware and settings.";
    if (f.install === "reinstall") return "It trusts another controller's release key, so it takes no changes from here. Installing it again over USB erases it and writes your keys.";
    const v = f.image ? str(opts.imageVersion(f.image)) : "";
    return `Its firmware is older than 0.0.1; installing ${v || "the current firmware"} over USB erases it.`;
  }

  // ---- a found node's setup panel ------------------------------------------------------

  function setup(f) {
    const p = panel(opts.boardName(f.board) || str(f.hostname) || "New device found");
    const link = linkWord(f.net);
    const fact = (k, ...v) => h("div", { class: "kv" }, h("span", { class: "muted small" }, k), h("div", {}, ...v));
    const m = mac(f.mac);
    const facts = h("div", { class: "stack" },
      fact("Address", addr(hostOnly(f.host)), link ? h("span", { class: "hint" }, ` over ${link}`) : null),
      m ? h("div", { class: "kv" }, m, h("div", { class: "hint" }, MACHINT)) : null,
      fact("Firmware", f.version ? str(f.version) : "unknown"),
      f.board || f.image ? fact("Board", [opts.boardName(f.board), f.image && `chip image ${f.image}`].filter(Boolean).join(" · ")) : null);
    const usb = viaUSB(f), keys = f.install === "keys", again = f.install === "reinstall";
    const addNow = !!f.next;
    const other = !usb && !addNow ? (arr(f.refusals).join("; ") || "it's one of your nodes already") : "";
    const step = (cls, title, detail, action) => h("li", { class: cls }, h("span", { class: "m" }),
      h("div", {}, h("b", {}, title), detail ? h("div", { class: "hint" }, detail) : null, action ? h("div", { class: "actions" }, action) : null));
    const steps = h("ol", { class: "steps" },
      usb ? step("next", keys ? "Write its keys over USB" : again ? "Install it again over USB" : "Update its firmware over USB", `${usbWhy(f)} Plug it into this computer.`,
        usbButton(f, "btn primary", opts.boardName(f.board) || str(f.hostname), null, next))
        : step("done", "Firmware takes node configs", f.version ? `It runs ${f.version}.` : ""),
      step(addNow ? "next" : "", "Add it to your nodes",
        addNow ? "Give it a name and an address; it copies your zones and starts serving."
          : usb ? (keys ? "Once it has its keys, it shows here again, ready to add." : again ? "Once it has your keys, it shows here again, ready to add." : "Once it runs 0.0.1 or later, it shows here again, ready to add.") : `Not yet: ${other}.`,
        addNow ? h("button", { class: "btn primary", type: "button", onclick: () => { p.close(); open(f); } }, "Add node") : null));
    p.body.append(facts, steps);
    p.footer.append(h("button", { class: "btn", type: "button", onclick: p.close }, "Close"));
    return p;
  }

  return { open, setup, next };
}

// blocker is why a found node isn't added yet, as its row and item say it.
export function blocker(f) {
  if (f.install === "keys") return "No keys (setup mode): write them over USB first";
  if (f.install === "usb") return "Firmware older than 0.0.1: update it over USB first";
  if (f.install === "reinstall") return "Trusts another controller's key: reinstall it over USB";
  return `Can't be added yet: ${arr(f.refusals).join("; ") || "it's one of your nodes already"}`;
}

