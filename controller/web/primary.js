// The zone primary as Zones edits it (settings.json's "primary",
// internal/primary): its address and the zones to copy from it. The nodes copy each zone by
// zone transfer and poll it for changes, so any standards-compliant primary works: it must
// allow zone transfers to the nodes' addresses, which you set up on it.
import { h, str } from "./shell.js";

export const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;
const canon = s => str(s).trim().toLowerCase().replace(/\.$/, "");
// AXFR is what to set up on the primary, said where it is set.
export const AXFR = "Your primary must allow zone transfers (AXFR) to your nodes' addresses: set that up on your DNS server.";
const ZONE = /^(?=.{1,253}$)([a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)(\.[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)*$/;

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

// primaryFields are the controls for the zone primary: its address (#pa) and the zones to
// copy (#pz). cur is settings.json's "primary"; zones the zones listed to start with (cur's,
// and on Zones those the nodes copy already).
export function primaryFields(cur, zones) {
  const addr = h("input", { class: "t mono", id: "pa", type: "text", value: str(cur.address), autocomplete: "off", spellcheck: "false",
    inputmode: "decimal", placeholder: "192.0.2.254" });
  const zs = listEditor({ id: "pz", items: zones.map(canon), placeholder: "home.example", label: "Zones to copy",
    empty: "None yet.", onChange: () => {},
    check: v => { const z = canon(v); return ZONE.test(z) ? { value: z } : { err: `${str(v).trim() || "Nothing"} isn't a zone name, like home.example.` }; } });
  const field = (label, ctl, hint) => h("div", { class: "f" }, h("span", {}, label), ctl, hint ? h("span", { class: "hint" }, hint) : null);
  const el = h("div", { class: "stack" },
    field("Address", addr, "Its IPv4 address."),
    h("p", { class: "hint", id: "pa-axfr" }, AXFR),
    field("Zones to copy", zs.el, "You keep editing them on your primary. To host a zone here, use + Add zone."));
  // value is the "primary" to save, or an error to show (a box typed in but not added).
  const value = () => {
    const a = addr.value.trim();
    if (a && !IPV4.test(a)) return { err: `${a} isn't an IPv4 address, like 192.0.2.254.`, focus: addr };
    if (zs.pending()) return { err: "Add the zone typed, or clear it.", focus: el.querySelector("#pz") };
    if (!a && zs.items().length) return { err: "Type your primary's address: the nodes copy the zones from it.", focus: addr };
    const p = { address: a, zones: zs.items() };
    return { primary: a || p.zones.length ? p : null };
  };
  return { el, value };
}
