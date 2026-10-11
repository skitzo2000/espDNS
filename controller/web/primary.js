// The zone primary as Zones and the first run edit it (settings.json's "primary",
// internal/primary): its address, the networks it allows zone transfers from, and the zones
// to copy from it. The nodes copy each zone by zone transfer and poll it for changes, so
// any standards-compliant primary works: it is set up once to allow zone transfers from
// the nodes' networks, and adding a node changes nothing there.
import { h, arr, str } from "./shell.js";

export const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;
const canon = s => str(s).trim().toLowerCase().replace(/\.$/, "");
const ZONE = /^(?=.{1,253}$)([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)(\.[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)*$/;

const num = ip => ip.split(".").reduce((n, o) => n * 256 + Number(o), 0);

// network is a.b.c.d/nn as its network (a.b.c.0/nn), or "" when it isn't one.
export function network(v) {
  const m = str(v).trim().match(/^([\d.]+)\/(\d{1,2})$/);
  if (!m || !IPV4.test(m[1]) || Number(m[2]) > 32) return "";
  const bits = Number(m[2]), size = 2 ** (32 - bits);
  const base = Math.floor(num(m[1]) / size) * size;
  return `${[24, 16, 8, 0].map(s => Math.floor(base / 2 ** s) % 256).join(".")}/${bits}`;
}

// covers says whether the network n (a.b.c.0/nn) holds the address ip.
export function covers(n, ip) {
  const [base, bits] = str(n).split("/");
  if (!IPV4.test(ip) || !IPV4.test(base)) return false;
  const size = 2 ** (32 - Number(bits));
  return Math.floor(num(ip) / size) === Math.floor(num(base) / size);
}

// uncovered are the addresses in none of the networks.
export const uncovered = (addrs, nets) => addrs.filter(a => IPV4.test(a) && !nets.some(n => covers(n, a)));

// net24 is ip's /24, the network to suggest for it.
export const net24 = ip => network(`${ip}/24`);

// listEditor is a list of names, each with Remove, and a box to add one: check(v) is the
// value to add ("" with err set when refused).
function listEditor({ id, items, placeholder, label, check, onChange, empty }) {
  let list = items.slice();
  const ul = h("ul", { class: "plist", id: `${id}-list`, "aria-label": label });
  const input = h("input", { class: "t mono", id, type: "text", autocomplete: "off", spellcheck: "false", autocapitalize: "off", placeholder, "aria-label": `${label}: add one` });
  const add = h("button", { class: "btn small", type: "button", id: `${id}-add` }, "Add");
  const err = h("span", { class: "hint bad", role: "status", "aria-live": "polite" });
  const draw = () => {
    ul.replaceChildren(...(list.length ? list.map(v => h("li", {}, h("span", { class: "mono" }, v),
      h("button", { class: "btn small", type: "button", "aria-label": `Remove ${v}`, onclick: () => { list = list.filter(x => x !== v); draw(); onChange(); } }, "Remove")))
      : [h("li", { class: "hint" }, empty)]));
  };
  const take = () => {
    err.textContent = "";
    const r = check(input.value);
    if (r.err) { err.textContent = r.err; input.focus(); return; }
    if (!list.includes(r.value)) list.push(r.value);
    input.value = "";
    draw();
    onChange();
  };
  add.addEventListener("click", take);
  input.addEventListener("keydown", e => { if (e.key === "Enter") { e.preventDefault(); take(); } });
  draw();
  return { el: h("div", { class: "stack" }, ul, h("div", { class: "row" }, input, add), err), items: () => list.slice(), pending: () => input.value.trim() };
}

// primaryFields are the controls for the zone primary: its address (#pa), the networks it
// allows (#pn) with the nodes' addresses in none of them, and the zones to copy (#pz).
// cur is settings.json's "primary"; nodes() the nodes' addresses; zones the zones listed
// to start with (cur's, and on Zones those the nodes copy already).
export function primaryFields(cur, nodes, zones) {
  const addr = h("input", { class: "t mono", id: "pa", type: "text", value: str(cur.address), autocomplete: "off", spellcheck: "false",
    inputmode: "decimal", placeholder: "192.0.2.254" });
  const note = h("p", { class: "hint", id: "pn-uncovered" });
  const say = () => {
    const out = uncovered(nodes(), nets.items());
    note.className = out.length ? "hint bad" : "hint";
    note.textContent = !nodes().length ? "Once you add nodes, this says if any is outside these networks."
      : out.length ? `Not in these networks: ${out.join(", ")}. Allow ${out.length === 1 ? "its" : "their"} network on your primary (e.g. ${[...new Set(out.map(net24))].join(", ")}), then add it here.`
        : "Every node's address is in these networks.";
  };
  const nets = listEditor({ id: "pn", items: arr(cur.allows).map(str), placeholder: "192.0.2.0/24", label: "Networks it allows zone transfers from",
    empty: "None yet.", onChange: say,
    check: v => { const n = network(v); return n ? { value: n } : { err: `${str(v).trim() || "Nothing"} isn't a network: give it with its prefix length, as 192.0.2.0/24.` }; } });
  const zs = listEditor({ id: "pz", items: zones.map(canon), placeholder: "home.example", label: "Zones to copy",
    empty: "None yet.", onChange: () => {},
    check: v => { const z = canon(v); return ZONE.test(z) ? { value: z } : { err: `${str(v).trim() || "Nothing"} isn't a zone name, like home.example.` }; } });
  say();
  const field = (label, ctl, hint) => h("div", { class: "f" }, h("span", {}, label), ctl, hint ? h("span", { class: "hint" }, hint) : null);
  const el = h("div", { class: "stack" },
    field("Address", addr, "Its IPv4 address: your nodes copy each zone from it by zone transfer, and check it for changes."),
    field("Networks it allows zone transfers from", nets.el, null), note,
    h("p", { class: "hint" }, "On your primary, allow zone transfers (and, if you like, NOTIFY) from these networks, once. Adding a node then changes nothing there."),
    field("Zones to copy", zs.el, "Each is copied whole and kept up to date; you keep editing it on your primary. Zones hosted on espDNS are separate: + Add zone."));
  // value is the "primary" to save, or an error to show (a box typed in but not added).
  const value = () => {
    const a = addr.value.trim();
    if (a && !IPV4.test(a)) return { err: `${a} isn't an IPv4 address, like 192.0.2.254.`, focus: addr };
    if (nets.pending()) return { err: "Add the network typed, or clear it.", focus: el.querySelector("#pn") };
    if (zs.pending()) return { err: "Add the zone typed, or clear it.", focus: el.querySelector("#pz") };
    if (!a && zs.items().length) return { err: "Give your primary's address: the nodes copy the zones from it.", focus: addr };
    const p = { address: a, allows: nets.items(), zones: zs.items() };
    return { primary: a || p.allows.length || p.zones.length ? p : null };
  };
  return { el, value };
}
