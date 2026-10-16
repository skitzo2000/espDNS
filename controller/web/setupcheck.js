// The first run on the Nodes page (the home page keeps a checklist
// until it is done): a first run (no password, no settings.json) goes to the setup
// (setup.html); after it, until every step is done, a row before the nodes says how far it
// is and what is next, with Continue. Read from GET /api/setup.
import { h, arr, obj } from "./shell.js";
import { getJSON } from "./common.js";

const NEXT = {
  password: "set a password", keys: "make your keys",
  first_node: "install and add your first node", second_node: "add a second node", zones: "add your zone", router: "tell your router", blocking: "turn on blocking",
};

// setupCheck puts the row before the element before (or sends a first run to the setup).
export async function setupCheck(before) {
  let s;
  try { s = await getJSON("api/setup"); } catch { return; }
  if (s.first_run) { location.replace("setup.html"); return; }
  if (s.done || !before) return;
  const steps = arr(s.steps);
  const done = steps.filter(x => obj(x).done).length;
  const next = steps.find(x => !obj(x).done);
  const bar = h("span", { class: "bar", role: "img", "aria-label": `${done} of ${steps.length} steps done` }, h("i", {}));
  bar.firstChild.style.width = `${steps.length ? Math.round(done / steps.length * 100) : 0}%`;
  const row = h("div", { class: "panel pad row spread accent setup-left", id: "setup-left" },
    h("div", { class: "stack nogap" }, h("b", {}, "Finish setting up espDNS"),
      h("span", { class: "small muted" }, `${done} of ${steps.length} steps done${next && NEXT[next.step] ? ` · next: ${NEXT[next.step]}` : ""}`), bar),
    h("a", { class: "btn", href: "setup.html#next" }, "Continue"));
  before.before(row);
}
