// The Update panel (a firmware update opens an Update panel: which nodes, from which build
// to which; then it goes into changes to apply), shared by
// Nodes (home.js) and the node page (node.js): each listed node from /api/updates, in
// versions, a box for each one that can be updated; Add to changes sends them to
// POST /api/updates.
import { h, arr, obj, str, panel, toast, post, refreshChanges, nodeText } from "./shell.js";
import { andList } from "./nodeview.js";

// openUpdate opens it. o: updates (/api/updates), listed (the nodes in settings.json),
// canWrite (logged in), only (a node's address: only it ticked; else every one), done (after
// adding).
export function openUpdate(o) {
  const rep = obj(o.updates);
  const p = panel("Update firmware");
  const picks = new Map();
  const names = ns => ns.map(n => nodeText(n));
  const rows = arr(rep.nodes).filter(u => u.listed).map(u => {
    const from = str(obj(u.runs).version), to = str(obj(u.newest).version);
    let right, hint;
    if (u.out_of_service) {
      right = h("span", { class: "hint" }, "Out of service");
      hint = "Returning it to service updates its firmware";
    } else if (u.state === "available" && !u.pending) {
      const cb = h("input", { type: "checkbox", checked: !o.only || o.only === u.host, "aria-label": `Update ${nodeText(u.host)}` });
      picks.set(u.host, cb);
      right = cb;
      hint = `${from} → ${to} · restarts for a few seconds`;
    } else if (u.pending) {
      right = h("span", { class: "hint" }, "Waiting to apply");
      hint = str(u.pending_summary);
    } else if (u.state === "current") {
      right = h("span", { class: "hint" }, "Up to date");
      hint = `Already on ${from}`;
    } else {
      right = h("span", { class: "hint" }, "–");
      hint = str(u.why);
    }
    return h("div", { class: "change" }, h("div", {}, h("b", {}, nodeText(u.host)), h("div", { class: "hint" }, hint)), right);
  });
  const note = h("p", { class: "small" });
  const msg = h("p", { class: "hint bad", role: "status" });
  const go = h("button", { class: "btn primary", type: "button" }, "Add to changes");
  const chosen = () => [...picks].filter(([, cb]) => cb.checked).map(([host]) => host);
  const say = () => {
    const c = chosen();
    const rest = arr(o.listed).filter(n => !c.includes(n.addr));
    go.disabled = !c.length || !o.canWrite;
    note.textContent = !c.length ? "Choose a node to update."
      : rest.length ? `${andList(names(rest))} keep${rest.length === 1 ? "s" : ""} answering while ${andList(c.map(x => nodeText(x)))} restart${c.length === 1 ? "s" : ""}.`
        : "One node at a time restarts; the others keep answering.";
  };
  for (const cb of picks.values()) cb.addEventListener("change", say);
  go.addEventListener("click", async () => {
    const c = chosen();
    go.disabled = true;
    msg.textContent = "";
    try {
      await post("api/updates", { nodes: c });
      p.close();
      await Promise.all([refreshChanges(), o.done ? o.done() : null]);
      toast(`Added to changes to apply: update ${andList(c.map(x => nodeText(x)))}`);
    } catch (e) { msg.textContent = e.message; go.disabled = false; }
  });
  p.body.append(h("div", { class: "stack" }, rows), note, msg);
  p.footer.append(h("button", { class: "btn", type: "button", onclick: p.close }, "Not now"), go);
  say();
  return p;
}
