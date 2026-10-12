// + Add zone, mounted by the Zones page (zones.js) and by the
// first run's "Add your zone" step (setup.js): the button becomes a box; a zone typed is
// looked up where it lives (GET /api/zone-lookup) with what to do about it right under it.
// A zone nowhere yet is hosted here first: espDNS is its primary, and it starts with the
// records every zone needs (GET /api/zones/<file>/new). A zone a DNS server you run has is
// copied from it. "Set it up myself" offers each: copy it from a primary, host it here, or
// forward its queries. Every add is a pending change (POST /api/changes: the zone file, or
// the node configs' secondary and forward zones) until Apply in the header. Esc, or a click
// outside with nothing typed, closes it.
//
// zoneEditor is the changing itself, which the Zones page's other edits share: the node
// configs as the pending changes make them (readConfigs), a zone added to or taken out of
// every listed node's config, and settings.json's zones to copy kept in step.
// Everything from a node, a config or the controller is put in as text.
import { h, arr, obj, str, toast, getJSON, post } from "./shell.js";

const enc = encodeURIComponent;
export const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;
// canon is a zone name as one: lower case, without the final dot.
export const canon = s => str(s).trim().toLowerCase().replace(/\.$/, "");
export const fileOf = z => `${z}.zone`;
// said is an API's error as a sentence: its path dropped, its first letter capital.
export const said = m => { const t = str(m).replace(/^api\/\S*?: /, ""); return t.charAt(0).toUpperCase() + t.slice(1); };

// readConfigs reads every node config as the pending changes make it: the secondary and
// forward zones an apply would send, and the version a new change edits.
export async function readConfigs() {
  const list = arr((await getJSON("api/configs")).configs);
  const out = [];
  for (const c of list) {
    const f = await getJSON(`api/changes/file?kind=config&name=${enc(str(c.name))}`);
    if (!f.exists) continue;
    let cfg = null;
    try { cfg = JSON.parse(str(f.text)); } catch { cfg = null; }
    out.push({ name: str(c.name), listed: arr(c.nodes).some(n => n.listed), hash: str(f.hash), cfg: f.partial ? null : obj(cfg) });
  }
  return out;
}

// primaryIP is the zone primary's IPv4 address, which a node config needs, from the
// inventory (GET /api/zone-inventory): settings.json's, else the one address the configs
// copy from; "" when there is none.
export function primaryIP(inv) {
  const p = obj(obj(inv).primary);
  const a = str(p.address) || arr(obj(inv).primaries)[0] || "";
  if (IPV4.test(a)) return a;
  const ps = arr(obj(inv).primaries).filter(x => IPV4.test(x));
  return ps.length === 1 ? ps[0] : "";
}

// zoneEditor changes the zones through pending changes. ctx: cfgs() the configs as
// readConfigs read them; inv() the inventory (the zones the firmware's defaults serve);
// readConfigs() reads the configs again; after() reads what a change made.
export function zoneEditor(ctx) {
  const cfgs = () => arr(ctx.cfgs());

  // targets are the node configs a new zone goes into: those the nodes in settings.json run
  // (every node serves the same zones), else every config.
  function targets() {
    const listed = cfgs().filter(c => c.listed);
    return listed.length ? listed : cfgs();
  }

  // fromDefaults are the zones of a kind the nodes serve from their firmware's defaults (no
  // config lists them): a config that lists none yet starts from them, so adding one zone
  // doesn't take the others away.
  function fromDefaults(kind) {
    return arr(obj(ctx.inv()).zones).filter(z => z.kind === kind && !arr(z.configs).length);
  }

  // editConfigs adds one pending change per config f changes (f returns false: unchanged),
  // each on the config as the pending changes make it.
  async function editConfigs(cs, summary, f) {
    if (!cs.length) throw new Error("No node config holds this yet: add a node first.");
    const todo = [];
    for (const c of cs) {
      if (!c.cfg) throw new Error(`${c.name} can't be read as a node config: fix it first.`);
      const cfg = JSON.parse(JSON.stringify(c.cfg));
      if (f(cfg, c) === false) continue;
      todo.push([c, cfg]);
    }
    for (const [c, cfg] of todo) {
      await post("api/changes", { kind: "config", name: c.name, text: `${JSON.stringify(cfg, null, 2)}\n`, hash: c.hash,
        summary: cs.length > 1 ? `${summary} · ${c.name.replace(/\.json$/, "")}` : summary });
    }
    await ctx.after();
    return todo.length;
  }

  // add adds name the way it lives: copied from the primary at ip ("copy"), hosted here
  // ("host"), or forwarded to ip ("fwd").
  async function add(name, kind, ip) {
    name = canon(name);
    if (kind === "host") {
      const t = await getJSON(`api/zones/${enc(fileOf(name))}/new`);
      await post("api/changes", { kind: "zone", name: fileOf(name), text: str(t.text), hash: "", summary: `Add zone ${name}: hosted here` });
      await ctx.after();
      toast(`Zone ${name} is waiting to apply: add its records`);
      return;
    }
    await ctx.readConfigs();
    if (kind === "copy" && !IPV4.test(ip)) {
      // A node copies from an IPv4 address.
      const a = primaryIP(ctx.inv());
      if (!a) throw new Error(`Nodes copy from an IPv4 address, and ${ip || "your primary"} isn't one: type it under "Set it up myself".`);
      ip = a;
    }
    if (kind === "copy") {
      const n = await editConfigs(targets(), `Add zone ${name}: copied from ${ip}`, cfg => {
        const s = cfg.secondary = obj(cfg.secondary);
        if (s.primary && s.primary !== ip) throw new Error(`Your nodes copy from ${s.primary}, and a node has one primary, so ${name} can't come from ${ip}.`);
        if (!Array.isArray(s.zones)) s.zones = fromDefaults("secondary").map(z => z.name);
        if (s.zones.map(canon).includes(name)) return false;
        s.primary = ip;
        s.zones.push(name);
        return true;
      });
      await keepInSettings(name, ip, true);
      toast(n ? `Zone ${name} is waiting to apply: your nodes copy it from ${ip}` : `Your nodes already copy ${name}`);
      return;
    }
    const n = await editConfigs(targets(), `Add zone ${name}: forwarded to ${ip}`, cfg => {
      if (!Array.isArray(cfg.forward_zones)) cfg.forward_zones = fromDefaults("forward").map(z => ({ zone: z.name, forwarder: arr(z.forwarders)[0] })).filter(z => z.forwarder);
      if (cfg.forward_zones.some(z => canon(z.zone) === name)) return false;
      cfg.forward_zones.push({ zone: name, forwarder: ip });
      return true;
    });
    toast(n ? `Zone ${name} is waiting to apply: its queries go to ${ip}` : `Your nodes already forward ${name}`);
  }

  // holding are the configs (as the pending changes make them) that hold zone name of kind
  // ("secondary" or "forward").
  function holding(name, kind) {
    return cfgs().filter(c => kind === "secondary"
      ? arr(obj(obj(c.cfg).secondary).zones).some(z => canon(z) === name)
      : arr(obj(c.cfg).forward_zones).some(z => canon(obj(z).zone) === name));
  }

  async function removeConfigZone(name, kind) {
    await ctx.readConfigs();
    await editConfigs(holding(name, kind), kind === "secondary" ? `Remove zone ${name}: stop copying it` : `Remove zone ${name}: stop forwarding it`, cfg => {
      if (kind === "secondary") cfg.secondary.zones = cfg.secondary.zones.filter(z => canon(z) !== name);
      else cfg.forward_zones = cfg.forward_zones.filter(z => canon(z.zone) !== name);
    });
    if (kind === "secondary") await keepInSettings(name, "", false);
  }

  // keepInSettings keeps settings.json's zones to copy from the primary (a new node starts
  // with them) as the nodes have them: name added when they copy it from the primary at ip
  // (its address set to ip when none is), or taken out.
  async function keepInSettings(name, ip, add) {
    const st = await getJSON("api/settings");
    const p = obj(obj(st.settings).primary), zs = arr(p.zones).map(str);
    if ((add && str(p.address) && str(p.address) !== ip) || add === zs.includes(name)) return;
    const np = { ...p, address: str(p.address) || ip, zones: add ? [...zs, name] : zs.filter(z => z !== name) };
    await post("api/settings", { settings: { ...obj(st.settings), primary: np }, version: str(st.version) });
  }

  return { add, editConfigs, holding, removeConfigZone, targets };
}

// addZone mounts + Add zone in box. opts: editor, a zoneEditor; canWrite(), whether the
// session may change anything; primaryIP(), the zone primary's IPv4 address or ""; zoneHref
// (name), the link to a zone's page (the Zones page's own by default); added(name, kind,
// ip), called once a zone is added and the box closed. Returns {mount(box), close()}.
export function addZone(box, opts) {
  const st = { open: false, q: "", res: null, busy: false, err: "", adding: false };
  let input = null, list = null, timer = null, seq = 0;
  const canWrite = () => !!(opts.canWrite && opts.canWrite());
  const zoneHref = opts.zoneHref || (name => `#zone=${enc(name)}`);
  const button = () => h("button", { class: "btn", type: "button", id: "add-zone-btn", onclick: open }, "+ Add zone");

  function mount(b) { box = b; st.open = false; box.replaceChildren(button()); }

  function open() {
    st.open = true; st.q = ""; st.res = null; st.err = "";
    input = h("input", { class: "t mono", id: "zq", type: "text", autocomplete: "off", spellcheck: "false", autocapitalize: "off",
      placeholder: "Zone name, like home.example", "aria-label": "Zone to add", "aria-controls": "zqres" });
    list = h("div", { class: "combo-list zpanel", id: "zqres", role: "region", "aria-label": "Add a zone", "aria-live": "polite" });
    input.addEventListener("input", () => {
      st.q = input.value.trim(); st.res = null; st.err = ""; seq++;
      clearTimeout(timer);
      if (st.q) { st.busy = true; timer = setTimeout(lookup, 450); } else st.busy = false;
      draw();
    });
    input.addEventListener("keydown", e => {
      if (e.key === "Escape") { e.preventDefault(); close(); }
      if (e.key === "Enter") { e.preventDefault(); clearTimeout(timer); lookup(); }
    });
    box.replaceChildren(h("div", { class: "combo zcombo" }, input, list));
    draw();
    input.focus();
  }

  function close() {
    if (st.adding) return;
    clearTimeout(timer); seq++;
    st.open = false;
    if (box) box.replaceChildren(button());
  }
  document.addEventListener("mousedown", e => { if (st.open && !e.target.closest(".zcombo") && !st.q && !document.querySelector(".drawer")) close(); });

  async function lookup() {
    const q = st.q;
    if (!q) return;
    const my = ++seq;
    st.busy = true; st.res = null; st.err = "";
    draw();
    try {
      const r = await getJSON(`api/zone-lookup?name=${enc(q)}`);
      if (my !== seq) return;
      st.res = r;
    } catch (e) {
      if (my !== seq) return;
      st.err = said(e.message);
    }
    st.busy = false;
    draw();
  }

  async function add(name, kind, ip) {
    st.adding = true;
    draw();
    try {
      await opts.editor.add(name, kind, ip);
      st.adding = false;
      close();
      if (opts.added) opts.added(canon(name), kind, ip);
    } catch (e) {
      st.adding = false;
      st.err = said(e.message);
      draw();
    }
  }

  // manual is "Set it up myself": copy it from a primary, host it here, or forward it.
  function manual(name, sel, ip) {
    const copyIP = h("input", { class: "t mono", id: "zmcopy", type: "text", value: IPV4.test(str(ip)) ? ip : str(opts.primaryIP && opts.primaryIP()), "aria-label": "Primary's address" });
    const fwdIP = h("input", { class: "t mono", id: "zmfwd", type: "text", value: "", placeholder: "192.0.2.53", "aria-label": "Server to forward to" });
    const radio = v => h("input", { type: "radio", name: "zm", value: v, checked: v === sel });
    const go = h("button", { class: "btn small", type: "button", id: "zmadd", disabled: !canWrite() || st.adding }, "Add zone");
    const err = h("span", { class: "hint bad", role: "status" });
    go.addEventListener("click", () => {
      const m = list.querySelector("input[name=zm]:checked");
      const k = m ? m.value : sel;
      const a = k === "copy" ? copyIP.value.trim() : k === "fwd" ? fwdIP.value.trim() : "";
      if (k !== "host" && !IPV4.test(a)) { err.textContent = "Type the server's IPv4 address, like 192.0.2.53."; return; }
      add(name, k, a);
    });
    return h("details", { class: "manual" }, h("summary", {}, "Set it up myself"), h("div", { class: "mopts" },
      h("label", { class: "mopt" }, radio("copy"), h("span", {}, "Copy it from a primary at", copyIP)),
      h("label", { class: "mopt" }, radio("host"), h("span", {}, "Host it here: espDNS is its primary")),
      h("label", { class: "mopt" }, radio("fwd"), h("span", {}, "Forward its queries to", fwdIP)),
      h("div", { class: "row" }, go, err)));
  }

  const found = (name, ...text) => h("div", { class: "found" }, h("b", { class: "mono" }, name), h("span", { class: "small" }, ...text));
  const act = (label, f, primary = true) => h("button", { class: `btn small${primary ? " primary" : ""}`, type: "button", disabled: !canWrite() || st.adding, onclick: f }, label);

  function draw() {
    if (!list) return;
    const out = [];
    const r = st.res;
    if (!st.q) {
      out.push(h("span", { class: "small muted" }, "Type a zone: espDNS looks for it on your primary and among your zones."));
    } else if (st.busy) {
      out.push(h("div", { class: "row small" }, h("span", { class: "spin", "aria-hidden": "true" }), "Looking up ", h("span", { class: "mono" }, st.q), "…"));
    } else if (st.err && !r) {
      out.push(h("p", { class: "hint bad" }, st.err));
    } else if (r) {
      const name = str(r.name), zone = obj(r.zone);
      const kindWords = k => (k === "secondary" ? "copied from your primary" : k === "forward" ? "forwarded" : "hosted here");
      switch (r.found) {
        case "zone":
          out.push(found(name, `Already one of your zones: ${kindWords(str(zone.kind))}.`),
            h("div", { class: "actions" }, h("a", { class: "btn small", href: zoneHref(name), onclick: close }, "Open it")));
          break;
        case "within":
          out.push(found(name, "A name inside ", h("span", { class: "mono" }, str(r.parent)), `, one of your zones (${kindWords(str(zone.kind))}).`,
            str(zone.kind) === "hosted" ? " Add it there as a record." : ""),
          h("div", { class: "actions" }, h("a", { class: "btn small", href: zoneHref(str(r.parent)), onclick: close }, `Open ${str(r.parent)}`)));
          break;
        case "primary":
          out.push(found(name, "Found on your primary at ", h("span", { class: "mono" }, str(r.primary)), r.serial ? `, version ${r.serial}` : "",
            ". Your nodes keep a copy; you edit it there."),
          h("div", { class: "actions" }, act("Copy from your primary", () => add(name, "copy", str(r.primary)))), manual(name, "copy", str(r.primary)));
          break;
        case "primary_within":
          out.push(found(name, "A name inside ", h("span", { class: "mono" }, str(r.parent)), ", on your primary at ", h("span", { class: "mono" }, str(r.primary)),
            ". Copy that zone and this name comes with it."),
          h("div", { class: "actions" }, act(`Copy ${str(r.parent)} from your primary`, () => add(str(r.parent), "copy", str(r.primary)))), manual(name, "host", str(r.primary)));
          break;
        default:
          out.push(found(name, arr(r.tried).length ? "Your primary doesn't have this zone, so espDNS can host it. " : "No primary is set up, so espDNS can host it. ",
            "It starts with the records every zone needs."),
          h("div", { class: "actions" }, act("Host it here", () => add(name, "host"))), manual(name, "host"));
      }
      if (st.err) out.push(h("p", { class: "hint bad", role: "status" }, st.err));
      if (st.adding) out.push(h("div", { class: "row small" }, h("span", { class: "spin", "aria-hidden": "true" }), "Adding…"));
      else if (r.found !== "zone" && r.found !== "within") out.push(h("p", { class: "hint" }, "It's added to \"changes to apply\"."));
    }
    list.replaceChildren(...out);
  }

  if (box) mount(box);
  return { mount, close };
}
