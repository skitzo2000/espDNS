// The first run (setup.html):
// from nothing to two answering nodes in the browser, as numbered steps, each
// opening in place with Start: the password (POST /api/setup/password: only while none is
// set, only from the controller's own machine; it logs in), the keys (GET /api/key: the
// release key made in the controller or imported, the recovery key made in this browser with
// only its public half sent, recovery.pem saved offline; keys.js), the zone primary (optional: POST /api/settings with its address, the
// networks it allows zone transfers from and the zones to copy, primary.js), the first and the second node (+ Add node, addnode.js), telling the
// router (the nodes' addresses to copy), and blocking (optional: the Blocking page). What is
// done comes from GET /api/setup; the steps the controller can't see done, and the ones
// skipped, are marked with POST /api/setup/step. The Nodes page sends a first run here and
// keeps a checklist until every step is done (setupcheck.js).
// Everything from a node or the controller is put in as text.
import { shell, h, arr, obj, str, hostOnly, addr, copyText, toast, getJSON, post, api, session, nodeText, refreshChanges, openAccountPanel } from "./shell.js";
import { onNodes, every, loggedIn } from "./common.js";
import { addNode } from "./addnode.js";
import { primaryFields } from "./primary.js";
import { canMake, makeRecovery, readPublic, save, base64 } from "./keys.js";

shell("setup.html");

const $ = id => document.getElementById(id);
const data = { setup: null, error: null, key: null, settings: null, nodes: null, found: [], boards: new Map(), sess: {} };
// recovery: the recovery key made in this page ({pem, pub, fp}), to download again until the
// page is left; saved: "I saved recovery.pem" ticked.
const ui = { open: null, sig: "", bodies: new Map(), recovery: null, saved: false };

// The steps in words; the order is /api/setup's.
const STEPS = {
  password: ["Set a password", "Only you can change your DNS."],
  keys: ["Make your keys", "The release key signs every change; the recovery key, kept offline, can replace it."],
  primary: ["Set up your zone primary", "Optional: the DNS server your zones come from, if you have one."],
  first_node: ["Add your first node", "Plug a board into this computer, or pick one found on the network."],
  second_node: ["Add a second node", "Two nodes keep DNS answering while one restarts."],
  router: ["Tell your router", "Give clients both nodes as their DNS servers."],
  blocking: ["Turn on blocking", "Optional: block ads and trackers."],
};

const listed = () => arr(data.nodes).filter(n => n.source === "settings");
const andList = xs => (xs.length < 2 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`);
const steps = () => arr(obj(data.setup).steps).filter(s => Object.hasOwn(STEPS, s.step));
// A step is done when the controller says so.
const isDone = s => !!s.done;
const current = () => { const s = steps().find(x => !isDone(x)); return s ? s.step : null; };
const boardName = b => data.boards.get(str(b)) || str(b);
const primaryOf = () => obj(obj(obj(data.settings).settings).primary);

// ---- the page ---------------------------------------------------------------------------

function render(force) {
  const sig = JSON.stringify([data.setup, data.error, keySig(), obj(data.settings).version, ui.open, data.sess.logged_in,
    listed().map(n => [n.addr, nodeText(n)]), data.found.length]);
  if (!force && sig === ui.sig) return;
  // Not while + Add node is open: it would lose what is typed; it says when it's done.
  if (!force && document.querySelector("#steps .ncombo")) return;
  ui.sig = sig;
  renderHead();
  const box = $("steps");
  if (!data.setup) {
    box.replaceChildren(h("div", { class: "empty" }, h("p", { class: data.error ? "bad" : "" }, data.error ? `Can't read what is set up: ${data.error}` : "Reading what is set up…")));
    return;
  }
  const now = current();
  box.replaceChildren(...steps().map((s, i) => stepRow(s, i, now)));
}

function renderHead() {
  const ss = steps();
  const done = ss.filter(isDone).length;
  const facts = $("facts");
  if (!data.setup) { facts.replaceChildren(); return; }
  facts.replaceChildren(h("span", {}, h("b", {}, `${done} of ${ss.length}`), " steps done"));
  const actions = $("head-actions");
  if (data.setup.done) {
    $("title").textContent = "espDNS is set up";
    $("artie").hidden = true; // he welcomes the first run only
    const ns = listed();
    $("intro").textContent = ns.length
      ? `DNS is answered by ${andList(ns.map(n => nodeText(n)))}. Your nodes, zones, blocking and query log are on the pages above.`
      : "Your nodes, zones, blocking and query log are on the pages above.";
    actions.replaceChildren(h("a", { class: "btn primary", href: "./" }, "Go to your nodes"));
  } else {
    $("title").textContent = "Set up espDNS";
    $("artie").hidden = false;
    $("intro").textContent = "Seven steps, about 15 minutes with two boards and a USB cable.";
    actions.replaceChildren();
  }
}

function stepRow(s, i, now) {
  const [title, why] = STEPS[s.step];
  const done = isDone(s);
  const open = ui.open === s.step && (!done || s.step === "primary" || s.step === "blocking");
  const skipped = done && skippedStep(s.step);
  let act = null;
  if (done && !open) {
    act = h("span", { class: "hint" }, skipped ? "Skipped" : "Done");
    if (skipped) {
      act = h("span", { class: "row" }, act, h("button", { class: "btn small link", type: "button", onclick: () => openStep(s.step) }, "Set up"));
    } else if (s.step === "keys") {
      // "then back it up": the backup holds the release key.
      act = h("span", { class: "row" }, act, h("button", { class: "btn small link", type: "button", onclick: () => openAccountPanel("safe") }, "Back it up"));
    }
  } else if (s.step === now && !open) {
    act = h("button", { class: "btn primary", type: "button", onclick: () => openStep(s.step) }, "Start");
  }
  const cls = ["s", done ? "done" : "", s.step === now ? "now" : "", open ? "open" : ""].filter(Boolean).join(" ");
  const row = h("div", { class: cls, id: `step-${s.step}`, "data-step": s.step },
    h("span", { class: "n", "aria-hidden": "true" }, done ? "✓" : String(i + 1)),
    h("div", { class: "what" }, h("b", {}, title), h("div", { class: "hint" }, done ? doneText(s.step) || why : why)),
    h("div", { class: "act" }, act));
  row.setAttribute("aria-label", `Step ${i + 1}: ${title}${done ? (skipped ? ", skipped" : ", done") : ""}`);
  if (open) row.append(bodyOf(s.step));
  return row;
}

// skippedStep: an optional step marked done without what it sets up.
function skippedStep(step) {
  if (step === "primary") return !!data.settings && !primaryOf().address;
  if (step === "blocking") return !obj(data.setup).blocklists;
  return false;
}

// What a step that is done set up, in words.
function doneText(step) {
  const ns = listed();
  switch (step) {
    case "password": return data.sess.logged_in ? `Set · you're logged in as ${str(data.sess.user)}` : "Set";
    case "keys": {
      const fp = str(obj(data.key).fingerprint), rfp = str(recOf().fingerprint);
      return fp && rfp ? `Release ${fp} · recovery ${rfp} · recovery.pem kept offline` : "Made";
    }
    case "primary": {
      const p = primaryOf(), nets = arr(p.allows).map(str), zs = arr(p.zones).length;
      if (!p.address) return "None: your zones are only on espDNS";
      return [str(p.address), nets.length ? `allows ${nets.join(", ")}` : "", zs ? `${zs} zone${zs === 1 ? "" : "s"} to copy` : ""].filter(Boolean).join(" · ");
    }
    case "first_node": return ns[0] ? `${nodeText(ns[0])} at ${hostOnly(ns[0].addr)}` : "";
    case "second_node": return ns[1] ? `${nodeText(ns[1])} at ${hostOnly(ns[1].addr)}` : "";
    case "router": return "Your router hands out your nodes as its DNS servers";
    case "blocking": {
      const n = Number(obj(data.setup).blocklists) || 0;
      return n ? `On · ${n} list${n === 1 ? "" : "s"}` : "Off: turn it on from Blocking whenever you like";
    }
  }
  return "";
}

function openStep(step) {
  ui.open = step;
  if (step === "primary") ui.bodies.delete("primary");
  render(true);
  const b = document.querySelector(`#step-${step} .sb`);
  const f = b && b.querySelector("input, select, button.primary, a.primary");
  if (f) f.focus();
}

// openNext opens the step after one just done.
function openNext() {
  const now = current();
  ui.open = now;
  render(true);
  if (now) {
    const r = $(`step-${now}`);
    if (r && r.scrollIntoView) r.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }
}

// bodyOf is the open step's controls, made once and kept while it is open, so what is
// typed survives a render.
function bodyOf(step) {
  for (const k of ui.bodies.keys()) if (k !== step) ui.bodies.delete(k);
  if (ui.bodies.has(step) && step !== "router" && step !== "first_node" && step !== "second_node" && step !== "keys") return ui.bodies.get(step);
  const make = { password: passwordBody, keys: keyBody, primary: primaryBody, first_node: nodeBody, second_node: nodeBody, router: routerBody, blocking: blockingBody }[step];
  const b = h("div", { class: "sb" }, ...(needsLogin(step) ? loginFirst() : make(step)));
  ui.bodies.set(step, b);
  return b;
}

const needsLogin = step => step !== "password" && !data.sess.logged_in;
function loginFirst() {
  return [h("p", {}, obj(data.setup).password_set ? "Log in to go on." : "Set a password first: the steps after it need you logged in."),
    obj(data.setup).password_set ? h("div", { class: "row" }, h("a", { class: "btn primary", href: "login.html?next=%2Fsetup.html" }, "Log in")) : null];
}

const field = (label, input, hint) => h("label", { class: "f" }, h("span", {}, label), input, hint ? h("span", { class: "hint" }, hint) : null);
const status = () => h("p", { class: "hint bad", role: "status", "aria-live": "polite" });

// mark marks a step the controller can't see done (or one skipped).
async function mark(step, btn, msg) {
  btn.disabled = true;
  msg.textContent = "";
  try {
    data.setup = await post("api/setup/step", { step });
    openNext();
  } catch (e) { msg.textContent = e.message; btn.disabled = false; }
}

// ---- 1. the password --------------------------------------------------------------------

function passwordBody() {
  if (obj(data.setup).password_set) return loginFirst();
  const user = h("input", { class: "t", id: "su-user", name: "username", value: "admin", autocomplete: "username", autocapitalize: "none", spellcheck: "false", required: true });
  const pw1 = h("input", { class: "t", id: "su-pw", name: "password", type: "password", autocomplete: "new-password", required: true });
  const pw2 = h("input", { class: "t", id: "su-pw2", type: "password", autocomplete: "new-password", required: true });
  const msg = status();
  const go = h("button", { class: "btn primary", type: "submit" }, "Set password");
  const form = h("form", { class: "stack", novalidate: true },
    h("p", { class: "small" }, "With it, only you can change your DNS: every change, here or from the terminal, needs it. You can change it later from your account."),
    h("div", { class: "grid2" }, field("User name", user, "Your choice; admin will do."), h("span", {})),
    h("div", { class: "grid2" }, field("Password", pw1, "At least 12 characters."), field("Again", pw2)),
    msg, h("div", { class: "row" }, go));
  form.addEventListener("submit", async e => {
    e.preventDefault();
    msg.className = "hint bad";
    msg.textContent = "";
    if (!user.value.trim()) { msg.textContent = "Give a user name."; user.focus(); return; }
    if (pw1.value.length < 12) { msg.textContent = "The password needs at least 12 characters."; pw1.focus(); return; }
    if (pw1.value !== pw2.value) { msg.textContent = "The two passwords differ: type it again."; pw2.select(); return; }
    go.disabled = true;
    msg.className = "hint";
    msg.textContent = "Setting it…";
    try {
      const r = await api("api/setup/password", { method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ user: user.value.trim(), password: pw1.value }) });
      const j = await r.json().catch(() => ({}));
      if (r.ok) {
        loggedIn(j);
        // The page again, logged in, with the next step open.
        location.hash = "next";
        location.reload();
        return;
      }
      msg.className = "hint bad";
      if (r.status === 409) { msg.textContent = "A password was set meanwhile. Log in with it."; location.href = "login.html?next=%2Fsetup.html"; return; }
      if (r.status === 403) {
        msg.textContent = `${j.error || r.statusText}. Open this page in a browser on that machine (localhost, port ${location.port || "80"}), or set it there with espdns passwd (docs/getting-started.md).`;
      } else msg.textContent = j.error || r.statusText;
    } catch (err) {
      msg.className = "hint bad";
      msg.textContent = `Can't reach the controller: ${err.message}`;
    }
    go.disabled = false;
  });
  return [form];
}

// ---- 2. the keys ------------------------------------------------------------------------
// The release key: made in the controller (POST /api/keys/release, as espdns key generate),
// or one you have imported from a terminal. The recovery key: made here in the browser
// (keys.js, WebCrypto), recovery.pem offered for download and never sent; only its public
// half goes to the controller (POST /api/keys/recovery). Or a recovery public key file you
// have. Done when both are there and recovery.pem is said to be saved offline.

const IMPORT = `docker compose run --rm -T --entrypoint /espdns espdns-controller \\
  key import -data /data < "$HOME/espdns-keys/release.pem"`;

const recOf = () => obj(obj(data.key).recovery);
// put appends what is there (h skips null in its children; append would write "null").
const put = (box, ...xs) => box.append(...xs.filter(x => x !== null && x !== undefined && x !== false));

// keySig is what the keys step shows, for render's signature.
const keySig = () => [obj(data.key).present, obj(data.key).fingerprint, obj(data.key).error, recOf().present, recOf().fingerprint,
  recOf().error, obj(data.key).nodes_listed, !!ui.recovery];

function keyBody() {
  const k = obj(data.key), rec = recOf();
  const listedNodes = !!k.nodes_listed;
  const msg = status();

  // 1. The release key.
  const relBox = h("div", { class: "stack", id: "keys-release" });
  if (k.present) {
    relBox.append(h("p", { class: "small" }, h("b", {}, "Release key"), " · fingerprint ", h("span", { class: "fp", id: "keys-release-fp" }, str(k.fingerprint))));
  } else {
    const make = h("button", { class: "btn primary", type: "button", id: "keys-release-make" }, "Make the release key");
    make.addEventListener("click", async () => {
      make.disabled = true;
      msg.className = "hint";
      msg.textContent = "Making the release key…";
      try {
        data.key = await post("api/keys/release", {});
        msg.textContent = "";
        toast("Release key made");
        render(true);
      } catch (e) { msg.className = "hint bad"; msg.textContent = e.message; make.disabled = false; }
    });
    const copy = h("button", { class: "btn small", type: "button", onclick: async () => toast(await copyText(IMPORT) ? "Copied the command" : "Couldn't copy: select the command and copy it") }, "Copy the command");
    const again = h("button", { class: "btn small", type: "button" }, "Check again");
    again.addEventListener("click", async () => {
      again.disabled = true;
      await Promise.all([readKey(), readSetup()]);
      if (!obj(data.key).present) { msg.className = "hint bad"; msg.textContent = obj(data.key).missing === false && obj(data.key).error ? obj(data.key).error : "No release key yet."; }
      again.disabled = false;
      render(true);
    });
    const imp = h("details", { class: "sub-ex", id: "keys-import", open: listedNodes },
      h("summary", { class: "small" }, "Use a release key you have"),
      h("div", { class: "stack" },
        h("p", { class: "small" }, "Give it to the controller once, on this machine, from the controller's folder:"),
        h("pre", { class: "cmd" }, IMPORT),
        h("p", { class: "hint" }, listedNodes
          ? "The key goes in on standard input, never as an argument. It's taken only if your nodes trust it, and the recovery key is refused."
          : "The key goes in on standard input, never as an argument. No node is added yet, so any key but the recovery key is taken."),
        h("div", { class: "row" }, copy, again)));
    put(relBox,
      h("p", { class: "small" }, h("b", {}, "Release key"), ": signs every change the controller sends to a node. The controller keeps it."),
      k.error && k.missing === false ? h("p", { class: "bad" }, `The controller has a release key it can't use: ${str(k.error)}`) : null,
      listedNodes
        ? h("p", { class: "hint" }, "Your nodes trust a release key already: import that one. A new one would sign changes they all refuse.")
        : h("div", { class: "row" }, make),
      imp);
  }

  // 2. The recovery key.
  const recBox = h("div", { class: "stack", id: "keys-recovery" });
  const makeRec = async (btn, replace) => {
    if (!canMake()) { msg.className = "hint bad"; msg.textContent = "This browser can't make keys here: open this page on the controller's machine (localhost) in a current browser."; return; }
    btn.disabled = true;
    msg.className = "hint";
    msg.textContent = "Making the recovery key in this browser…";
    let r;
    try {
      r = await makeRecovery();
      // The private key, to keep: saved by the browser, never sent.
      save(r.pem, "recovery.pem", "application/x-pem-file");
      data.key = await post("api/keys/recovery", { public_key: base64(r.pub), replace });
      if (recOf().fingerprint !== r.fp) throw new Error(`The controller has ${str(recOf().fingerprint)}, not ${r.fp}: try again.`);
      ui.recovery = r;
      msg.className = "hint";
      msg.textContent = "";
      toast("Recovery key made: recovery.pem is downloading");
      render(true);
    } catch (e) {
      ui.recovery = null;
      msg.className = "hint bad";
      msg.textContent = e.message;
      btn.disabled = false;
    }
  };
  if (rec.present) {
    recBox.append(h("p", { class: "small" }, h("b", {}, "Recovery key"), " · fingerprint ", h("span", { class: "fp", id: "keys-recovery-fp" }, str(rec.fingerprint)),
      ui.recovery ? " · made in this browser" : ""));
    if (ui.recovery && ui.recovery.fp === rec.fingerprint) {
      const again = h("button", { class: "btn small", type: "button", id: "keys-recovery-again" }, "Download recovery.pem again");
      again.addEventListener("click", () => save(ui.recovery.pem, "recovery.pem", "application/x-pem-file"));
      recBox.append(h("div", { class: "row" }, again));
    }
    if (!listedNodes && !obj(steps().find(s => s.step === "keys")).done) {
      const remake = h("button", { class: "btn small", type: "button", id: "keys-recovery-remake" }, "Make a new recovery key");
      remake.addEventListener("click", () => makeRec(remake, true));
      recBox.append(h("details", { class: "sub-ex" }, h("summary", { class: "small" }, "Lost recovery.pem before saving it?"),
        h("div", { class: "stack" }, h("p", { class: "hint" }, "Make a new one in its place. Only until a node is added: the nodes trust the one they were installed with."),
          h("div", { class: "row" }, remake))));
    }
  } else {
    const make = h("button", { class: "btn primary", type: "button", id: "keys-recovery-make", disabled: !k.present }, "Make the recovery key");
    make.addEventListener("click", () => makeRec(make, false));
    const file = h("input", { type: "file", id: "keys-recovery-file", accept: ".pub,.pem,application/octet-stream" });
    file.addEventListener("change", async () => {
      const f = file.files && file.files[0];
      if (!f) return;
      msg.className = "hint";
      msg.textContent = "Reading it…";
      try {
        const pub = await readPublic(f);
        data.key = await post("api/keys/recovery", { public_key: base64(pub) });
        msg.textContent = "";
        toast("Recovery public key set");
        render(true);
      } catch (e) { msg.className = "hint bad"; msg.textContent = e.message; file.value = ""; }
    });
    put(recBox,
      h("p", { class: "small" }, h("b", {}, "Recovery key"), ": replaces the release key if it's ever lost or stolen. It's made here in your browser and downloaded as recovery.pem; the controller gets only its public half."),
      !k.present ? h("p", { class: "hint" }, "First the release key.") : null,
      h("div", { class: "row" }, make),
      h("details", { class: "sub-ex", id: "keys-recovery-import", open: listedNodes },
        h("summary", { class: "small" }, "Use a recovery key you have"),
        h("div", { class: "stack" },
          h("label", { class: "f" }, h("span", {}, "Its public key file (recovery.pub)"), file,
            h("span", { class: "hint" }, "Only the public half: 65 bytes, or a PEM public key. Your nodes must trust it.")))));
  }

  // 3. recovery.pem kept offline.
  const saved = h("input", { type: "checkbox", id: "keys-saved", checked: !!ui.saved, disabled: !(k.present && rec.present) });
  const done = h("button", { class: "btn primary", type: "button", id: "keys-done", disabled: !(k.present && rec.present && ui.saved) }, "Done");
  saved.addEventListener("change", () => { ui.saved = saved.checked; done.disabled = !(saved.checked && k.present && rec.present); });
  done.addEventListener("click", () => mark("keys", done, msg));
  return [
    h("p", { class: "small" }, "Two keys keep your nodes yours. A node takes a change only when it's signed with your release key, and a key only from your recovery key or a USB cable."),
    relBox, recBox,
    h("label", { class: "check" }, saved, h("span", {}, "I saved recovery.pem somewhere safe, off this computer")),
    h("p", { class: "hint" }, "A USB stick in a drawer, or your password manager: not in Downloads, and apart from the controller and its backups. It is what replaces a lost or stolen release key; the controller never has it."),
    msg,
    h("div", { class: "row" }, done),
  ];
}

// ---- 3. the zone primary ----------------------------------------------------------------

function primaryBody() {
  if (!data.settings) return [h("p", { class: "hint" }, "Reading the settings…")];
  const cur = primaryOf();
  const f = primaryFields(cur, () => listed().map(n => hostOnly(n.addr)), arr(cur.zones).map(str));
  const msg = status();
  const skip = h("button", { class: "btn", type: "button" }, "Skip: no zone primary");
  skip.addEventListener("click", () => mark("primary", skip, msg));
  const save = h("button", { class: "btn primary", type: "submit" }, "Save");
  const form = h("form", { class: "stack", novalidate: true },
    h("p", { class: "small" }, "If your zones already live on a DNS server, the nodes copy them from it by zone transfer, and Zones shows them. If not, skip this: you can host zones on espDNS, or set one up later from Zones."),
    f.el, msg, h("div", { class: "row" }, skip, save));
  form.addEventListener("submit", async e => {
    e.preventDefault();
    msg.className = "hint bad";
    msg.textContent = "";
    const v = f.value();
    if (v.err) { msg.textContent = v.err; if (v.focus) v.focus.focus(); return; }
    if (!v.primary || !v.primary.address) { msg.textContent = "Give your primary's address, or skip this step."; return; }
    save.disabled = skip.disabled = true;
    msg.className = "hint";
    msg.textContent = "Saving…";
    try {
      data.settings = await post("api/settings", { settings: { ...obj(data.settings.settings), primary: v.primary }, version: str(data.settings.version) });
      await readSetup();
      toast("Zone primary saved");
      openNext();
      return;
    } catch (err) {
      msg.className = "hint bad";
      msg.textContent = err.message;
    }
    save.disabled = skip.disabled = false;
  });
  return [form];
}

// ---- 4, 5. the nodes --------------------------------------------------------------------

// One + Add node for both steps, moved into the step open.
const nodeBox = h("div", { class: "setup-box" });
let adder = null;
function nodeBody(step) {
  const ns = listed();
  if (!adder) {
    adder = addNode(nodeBox, {
      found: () => data.found,
      nodes: () => listed(),
      boardName,
      nodeKey: n => n.id || n.addr,
      done: async () => { await Promise.all([readSetup(), readFound(), refreshChanges()]); openNext(); },
    });
  }
  const out = [];
  if (step === "first_node") {
    out.push(h("p", { class: "small" }, "A board that has never run espDNS is installed over USB from this computer. One already running espDNS on your network is listed when you open + Add node; pick it, give it a name and an address, and it's added."));
  } else {
    out.push(h("p", { class: "small" }, `${ns[0] ? `${nodeText(ns[0])} is your first node. ` : ""}A second one keeps DNS answering while the other restarts or updates; a wired one is best. Add it the same way.`));
  }
  if (arr(data.found).length) out.push(h("p", { class: "hint" }, `${arr(data.found).length === 1 ? "One node is" : `${arr(data.found).length} nodes are`} on your network and not added yet.`));
  out.push(nodeBox);
  return out;
}

// ---- 6. the router ----------------------------------------------------------------------

function routerBody() {
  const ns = listed();
  const ips = ns.map(n => hostOnly(n.addr));
  const msg = status();
  const copy = h("button", { class: "btn", type: "button", disabled: !ips.length }, "Copy DNS servers");
  copy.addEventListener("click", async () => {
    const t = ips.join(", ");
    toast(await copyText(t) ? `Copied ${t}` : `Couldn't copy: your DNS servers are ${t}`);
  });
  const done = h("button", { class: "btn primary", type: "button" }, "Done");
  done.addEventListener("click", () => mark("router", done, msg));
  return [
    h("p", { class: "small" }, "In your router's settings (often LAN or DHCP), set the DNS servers it gives out to these addresses, so every device on your network asks espDNS. Give it all of them: when one node restarts, devices ask the other."),
    h("div", { class: "addrs" }, ns.map(n => h("div", {}, h("span", { class: "small muted" }, nodeText(n)), addr(hostOnly(n.addr))))),
    h("p", { class: "hint" }, "Devices pick up the change when they next renew their address, or when they reconnect. One that has its DNS servers set by hand keeps them."),
    msg,
    h("div", { class: "row" }, copy, done),
  ];
}

// ---- 7. blocking ------------------------------------------------------------------------

function blockingBody() {
  const msg = status();
  const skip = h("button", { class: "btn", type: "button" }, "Skip: no blocking");
  skip.addEventListener("click", () => mark("blocking", skip, msg));
  return [
    h("p", { class: "small" }, "Block ads, trackers or other kinds of sites on every node. You pick the lists from a catalogue of well-known ones, or add one by its web address; nothing is chosen for you. You can turn it off, or pause it, at any time."),
    msg,
    h("div", { class: "row" }, skip, h("a", { class: "btn primary", href: "blocklists.html" }, "Choose lists")),
  ];
}

// ---- reading ----------------------------------------------------------------------------

async function readSetup() {
  try {
    data.setup = await getJSON("api/setup");
    data.error = null;
  } catch (e) { data.error = e.message; }
  render();
}
async function readKey() {
  try { data.key = await getJSON("api/key"); } catch (e) { data.key = { present: false, error: e.message }; }
  render();
}
async function readSettings() {
  try { data.settings = await getJSON("api/settings"); } catch { /* the step says it's reading */ }
}
async function readFound() {
  if (!data.sess.logged_in) return;
  try { data.found = arr(obj(await getJSON("api/nodes/found")).nodes); } catch { /* kept */ }
  render();
}

onNodes(ns => { data.nodes = ns; render(); });
getJSON("api/boards").then(bs => {
  for (const b of arr(bs)) { const bd = obj(b.board); if (bd.name) data.boards.set(bd.name, str(bd.title) || bd.name); }
}).catch(() => {});

session.then(async s => {
  data.sess = s;
  await Promise.all([readSetup(), readKey(), readSettings()]);
  if (location.hash === "#next") {
    history.replaceState(null, "", location.pathname + location.search);
    ui.open = current();
  }
  render(true);
  every(readSetup);
  every(readFound);
  every(readKey, 15000);
});
render(true);
