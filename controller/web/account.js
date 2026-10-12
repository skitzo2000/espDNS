// The account panel (the account menu, top right: the password, Keep the controller safe
// (the release key and the backup) and recent changes), opened from the top bar's account chip (shell.js). Rows that hide and expand:
// - Password: a new one (POST /api/password), after the one now is given again (askPassword,
//   a grant); every session ends, so the page goes to the login.
// - Keep the controller safe: the release and recovery keys' fingerprints, the release
//   public key to download (GET /api/keys/release.pub), and a backup downloaded (POST
//   /api/backup, the password given again first), to a passphrase or an age key, with
//   or without the firmware, and what it holds (GET /api/backup); how to restore one (the
//   CLI's only: a restore replaces the login and the release key, so no page does it), and
//   how to start again without one.
// - Rotate keys: new keys for every node, prepared here, signed offline with the recovery
//   key, pushed one node at a time (rotate.js).
// - Firmware: the chip images the controller has, each with where it came from (project
//   release or your build), and Get firmware (firmware.js: the fetch-release job).
// - Recent changes: the action log (GET /api/actions) in words, newest first; Show all.
// And Log out. Everything from the controller is put in as text.
import { panel, h, toast, arr, obj, str } from "./shell.js";
import { getJSON, post, api, askPassword, loggedIn, nodeText, jobParams } from "./common.js";
import { save } from "./keys.js";
import { firmwareBox, readFirmware, summary } from "./firmware.js";
import { rotateBox, rotationSummary } from "./rotate.js";

const MIN = 12; // the shortest password and backup passphrase the controller takes

// account.css: loaded once, before the panel first opens (the shell pages don't link it).
let css = null;
function loadCSS() {
  if (!css) {
    css = new Promise(res => {
      const l = h("link", { rel: "stylesheet", href: "account.css" });
      l.addEventListener("load", res);
      l.addEventListener("error", res);
      document.head.append(l);
    });
  }
  return css;
}

// row is a hide-and-expand row: its title, its value while closed (an element kept up to
// date by the caller) and its controls.
function row(title, val, ...body) {
  return h("details", { class: "ex" }, h("summary", {}, h("b", {}, title), val), h("div", { class: "exb" }, ...body));
}

// when is a time as the GUI says it: "Today 14:32", "Yesterday 18:19", "3 Oct 15:18".
export function when(t, now = new Date()) {
  const d = new Date(t);
  if (Number.isNaN(d.getTime())) return "";
  const hm = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
  const day = x => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((day(now) - day(d)) / 86400000);
  if (diff === 0) return `Today ${hm}`;
  if (diff === 1) return `Yesterday ${hm}`;
  const opts = { day: "numeric", month: "short" };
  if (d.getFullYear() !== now.getFullYear()) opts.year = "numeric";
  return `${d.toLocaleDateString("en-GB", opts)} ${hm}`;
}

// ---- recent changes -----------------------------------------------------------------------
// What a job did, by its kind, in words; a job that changes nothing (a check) isn't a change.
const JOBS = {
  apply: ["Changes applied", "Apply failed", "Apply stopped"],
  rollout: ["Sent to the nodes", "Sending to the nodes failed", "Sending to the nodes stopped"],
  "config-push": ["Settings sent", "Sending settings failed", "Sending settings stopped"],
  adopt: ["Node added", "Adding a node failed", "Adding a node stopped"],
  "settings-add": ["Node added to the list", "Adding a node to the list failed", "Adding a node to the list stopped"],
  primary: ["Zone primary updated", "Updating the zone primary failed", "Updating the zone primary stopped"],
  blocklist: ["Blocklists compiled", "Compiling the blocklists failed", "Compiling the blocklists stopped"],
  refresh: ["Blocklists updated to their new versions", "Updating the blocklists failed", "Updating the blocklists stopped"],
  reboot: ["Restarted", "Restart failed", "Restart stopped"],
  "reboot-pending": ["Restarted", "Restart failed", "Restart stopped"],
  flush: ["Cache flushed", "Flushing the cache failed", "Flushing the cache stopped"],
  pause: ["Blocking paused", "Pausing blocking failed", "Pausing blocking stopped"],
  revert: ["Went back to the build before", "Going back failed", "Going back stopped"],
  identify: ["Identified", "Identify failed", "Identify stopped"],
  "fetch-release": ["Firmware fetched and imported", "Getting firmware failed", "Getting firmware stopped"],
  "key-rotate": ["Keys rotated", "Rotating keys stopped at a node", "Rotating keys stopped"],
  "node-out": ["Taken out of service", "Taking out of service failed", "Taking out of service stopped"],
  "node-in": ["Returned to service", "Returning to service failed", "Returning to service stopped"],
  "node-remove": ["Removed from the group", "Removing from the group failed", "Removing from the group stopped"],
};
const SKIP = new Set(["job check", "change add", "change discard"]);

// changeText is an action log entry as {what, bad}, or null for one that isn't a change to
// show (a job's start, which its end follows; a check; a change only queued).
export function changeText(e) {
  const action = str(e.action), args = arr(e.args).map(str);
  if (SKIP.has(action) || e.event === "start") return null;
  const failed = e.event === "refused" || e.result === "failed";
  if (action.startsWith("job ")) {
    const kind = action.slice(4);
    // A dry run changes nothing: the job after it is the change.
    if (jobParams({ args: e.args }).dry_run === true) return null;
    // A pause of no seconds is a resume.
    const words = kind === "pause" && Number(jobParams({ args: e.args }).seconds) === 0
      ? ["Blocking resumed", "Resuming blocking failed", "Resuming blocking stopped"]
      : JOBS[kind] || [`${kind[0] ? kind[0].toUpperCase() + kind.slice(1) : "Job"} done`, `${kind} failed`, `${kind} stopped`];
    let what = e.event === "refused" ? `${words[0]}: not started, the fleet was busy` : words[failed ? 1 : e.result === "stopped" ? 2 : 0];
    // A new version the size-change check refused: not sent.
    if (kind === "refresh" && failed && /size-change/.test(str(e.error))) what = "Blocklist update refused: it changed more than its limit";
    const p = jobParams({ args: e.args });
    if (typeof p.node === "string") what += ` on ${nodeText(p.node)}`;
    return { what, bad: failed, node: typeof p.node === "string" ? p.node : "" };
  }
  if (e.source === "cli") {
    const words = JOBS[action];
    if (!words) return { what: `espdns ${action}${failed ? " (failed)" : ""}`, bad: failed };
    return { what: e.event === "refused" ? `${words[0]}: not started, the fleet was busy` : words[failed ? 1 : e.result === "stopped" ? 2 : 0], bad: failed };
  }
  const name = args[0] || "";
  switch (action) {
    case "settings": return { what: args.length ? `Settings changed: ${args.join(", ")}` : "Settings changed", bad: failed };
    case "config save": return { what: `Settings of ${name.replace(/\.json$/, "") || "a node"} saved`, bad: failed };
    case "zone save": return { what: `Zone ${name} saved`, bad: failed, href: `zones.html#zone=${encodeURIComponent(name)}` };
    case "zone delete": return { what: `Zone ${name} deleted`, bad: failed };
    case "blocking lists save": return { what: "Blocklists chosen", bad: failed };
    case "blocking source save": return { what: `Blocklist ${name} saved`, bad: failed };
    case "blocking source delete": return { what: `Blocklist ${name} deleted`, bad: failed };
    case "key generate": return { what: `Release key made (${name})`, bad: failed };
    case "key recovery": return { what: `Recovery key set (${name})`, bad: failed };
    case "key rotate prepare": return { what: `Key rotation ${name} prepared`, bad: failed };
    case "key rotate signed": return { what: `Key rotation ${name}: a signed release added${args[1] ? ` for ${args[1]}` : ""}`, bad: failed };
    case "key rotate discard": return { what: `Key rotation ${name} discarded`, bad: failed };
  }
  if (action.startsWith("primary-cert-")) return { what: `Zone primary's certificate ${action.slice(13)}`, bad: failed };
  return { what: [action, name].filter(Boolean).join(" "), bad: failed };
}

function recent(entries, user) {
  const out = [];
  for (const e of arr(entries)) {
    const t = changeText(obj(e));
    if (!t) continue;
    const who = e.source === "cli" ? `${str(e.who)} (command line)` : str(e.who) === user ? "You" : str(e.who) || "The controller";
    out.push({ time: str(e.time), who, ...t, error: str(e.error) });
  }
  return out;
}

// ---- the panel ----------------------------------------------------------------------------
// open names a row to open at once: "safe" (Keep the controller safe).
export async function openAccount(user, open) {
  await loadCSS();
  const p = panel(user);
  p.aside.classList.add("acct");

  // Password
  const pw1 = h("input", { class: "t", id: "pw1", type: "password", autocomplete: "new-password" });
  const pw2 = h("input", { class: "t", id: "pw2", type: "password", autocomplete: "new-password" });
  const pwMsg = h("p", { class: "hint", role: "status", id: "pw-msg" });
  const pwGo = h("button", { class: "btn", type: "submit", id: "pw-go" }, "Change password");
  const pwForm = h("form", { class: "stack" },
    h("label", { class: "f" }, h("span", {}, "New password"), pw1),
    h("label", { class: "f" }, h("span", {}, "Again"), pw2),
    h("div", { class: "row" }, pwGo), h("span", { class: "hint" }, "Changing it signs everyone out."), pwMsg);
  pwForm.addEventListener("submit", async e => {
    e.preventDefault();
    const say = (t, bad) => { pwMsg.textContent = t; pwMsg.className = bad ? "hint bad" : "hint"; };
    if ([...pw1.value].length < MIN) { say(`At least ${MIN} characters.`, true); pw1.focus(); return; }
    if (pw1.value !== pw2.value) { say("The two passwords differ.", true); pw2.focus(); return; }
    pwGo.disabled = true;
    try {
      say("Your password, to change it…");
      const grant = await askPassword("Enter your current password to change it.");
      if (!grant) { say("Not changed."); return; }
      say("Changing it…");
      await post("api/password", { password: pw1.value }, { "X-Reauth": grant });
      pw1.value = pw2.value = "";
      loggedIn(null);
      location.href = "login.html";
    } catch (err) {
      say(err.message, true);
    } finally {
      pwGo.disabled = false;
    }
  });

  // Keep the controller safe
  const safeVal = h("span", { class: "val", id: "safe-val" }, "Reading…");
  const keyLine = h("p", { class: "small", id: "key-line", hidden: true });
  const bp = h("input", { class: "t", id: "bp", type: "password", autocomplete: "off", spellcheck: false });
  const bp2 = h("input", { class: "t", id: "bp2", type: "password", autocomplete: "off", spellcheck: false });
  const bMsg = h("p", { class: "hint", role: "status", id: "bp-msg" });
  const bGo = h("button", { class: "btn primary", type: "submit", id: "bp-go" }, "Download backup");
  // More options: an age key in place of the passphrase, the firmware too, what it holds.
  const recip = h("input", { class: "t mono", id: "bp-age", type: "text", placeholder: "age1…", autocomplete: "off", spellcheck: false });
  const fw = h("input", { type: "checkbox", id: "bp-fw" });
  const fwSize = h("span", { class: "hint", id: "bp-fwsize" });
  const holds = h("div", { class: "stack nogap", id: "bp-parts" }, h("p", { class: "hint" }, "Reading…"));
  const more = h("details", { class: "sub-ex", id: "bp-more" }, h("summary", { class: "small" }, "More options"),
    h("div", { class: "stack" },
      h("label", { class: "f" }, h("span", {}, "Or encrypt it to an age key"), recip,
        h("span", { class: "hint" }, "Instead of a passphrase; restore with the key's identity file (espdns restore -identity key.txt).")),
      h("label", { class: "check" }, fw, h("span", {}, "Include the firmware (builds and chip images) ", fwSize)),
      h("b", { class: "small" }, "What a backup holds"), holds));
  const bForm = h("form", { class: "stack" },
    h("label", { class: "f" }, h("span", {}, "Backup passphrase"), bp, h("span", { class: "hint" }, `At least ${MIN} characters. Keep it apart from the file.`)),
    h("label", { class: "f" }, h("span", {}, "Again"), bp2),
    more,
    h("div", { class: "row" }, bGo), bMsg);
  bForm.addEventListener("submit", async e => {
    e.preventDefault();
    const say = (t, cls = "") => { bMsg.textContent = t; bMsg.className = `hint ${cls}`; };
    const age = recip.value.trim();
    if (age && (bp.value || bp2.value)) { say("A passphrase or an age key, not both.", "bad"); return; }
    if (!age && [...bp.value].length < MIN) { say(`The passphrase: at least ${MIN} characters.`, "bad"); bp.focus(); return; }
    if (!age && bp.value !== bp2.value) { say("The two passphrases differ.", "bad"); bp2.focus(); return; }
    bGo.disabled = true;
    try {
      say("Your password, to download a backup…");
      const grant = await askPassword("A backup holds the release key and the Wi-Fi passwords: enter your password to download one.");
      if (!grant) { say("Not downloaded."); return; }
      say("Writing the backup…");
      const r = await api("api/backup", { method: "POST", headers: { "Content-Type": "application/json", "X-Reauth": grant },
        body: JSON.stringify({ ...(age ? { recipient: age } : { passphrase: bp.value }), firmware: fw.checked }) });
      if (r.status === 401) { location.href = "login.html"; return; }
      if (!r.ok) {
        let msg = r.statusText;
        try { msg = (await r.json()).error || msg; } catch { /* not JSON */ }
        throw new Error(msg);
      }
      const blob = await r.blob();
      const m = /filename="([^"]+)"/.exec(r.headers.get("Content-Disposition") || "");
      const name = m ? m[1] : "espdns-backup.age";
      const a = h("a", { href: URL.createObjectURL(blob), download: name, class: "offscreen" });
      document.body.append(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(a.href), 60_000);
      bp.value = bp2.value = "";
      say(`Saved ${name}. Keep it, and ${age ? "the key's identity file" : "its passphrase"}, somewhere other than this computer.`, "ok");
      toast("Backup downloaded");
      readSafe();
    } catch (err) {
      say(err.message, "bad");
    } finally {
      bGo.disabled = false;
    }
  });
  // Restore: the CLI's only, with the controller stopped; the command, with its Docker
  // mounts, is in the docs (a bare espdns restore here would be wrong for a compose setup).
  const restore = h("div", { class: "stack restore" },
    h("b", { class: "small" }, "Restoring a backup"),
    h("p", { class: "small" }, "A restore replaces the login and the release key, so it's done from a terminal with the controller stopped (",
      h("code", {}, "espdns restore"), ": docs/operations.md, Restore)."),
    h("p", { class: "small" }, "Without a backup, the nodes keep running. A new controller with the release key imported finds them again (",
      h("code", {}, "espdns recover"), ": docs/operations.md, Recovering from the nodes); zones and lists are copied back by hand."));

  // Recent changes
  const chVal = h("span", { class: "val", id: "changes-val" }, "Reading…");
  const chList = h("div", { class: "changes-list", id: "changes-list" }, h("p", { class: "hint" }, "Reading…"));

  const showAll = h("button", { class: "btn small link", type: "button", id: "changes-all", hidden: true }, "Show all");

  // The keys: both fingerprints, as the nodes' /status names them; the release key's public
  // half to download. Never a private key: recovery.pem stays offline.
  const relFP = h("span", { class: "mono", id: "acct-release-fp" }, "…");
  const recFP = h("span", { class: "mono", id: "acct-recovery-fp" }, "…");
  const pubGo = h("button", { class: "btn small", type: "button", id: "acct-release-pub", disabled: true }, "Download release.pub");
  pubGo.addEventListener("click", async () => {
    pubGo.disabled = true;
    try {
      const r = await api("api/keys/release.pub");
      if (!r.ok) throw new Error((await r.text()).trim() || r.statusText);
      save(await r.arrayBuffer(), "release.pub");
    } catch (err) { toast(`Can't download it: ${err.message}`); }
    pubGo.disabled = false;
  });
  const keysBox = h("div", { class: "stack", id: "acct-keys" },
    h("dl", { class: "kv small" }, h("dt", {}, "Release key"), h("dd", {}, relFP), h("dt", {}, "Recovery key"), h("dd", {}, recFP)),
    h("div", { class: "row" }, pubGo),
    h("p", { class: "hint" }, "Keep recovery.pem offline, apart from the controller and its backups: the controller never has it."));

  const safeRow = row("Keep the controller safe", safeVal,
    h("p", { class: "small" }, "The release key signs every change sent to a node. A backup holds it and everything else the controller keeps, so a new controller can pick up from it."),
    keysBox, keyLine, bForm, restore);
  safeRow.id = "acct-safe";
  if (open === "safe") safeRow.open = true;
  // Rotate keys
  const rotVal = h("span", { class: "val", id: "rot-val" }, "Reading…");
  const rot = rotateBox({ changed: r => { rotVal.textContent = rotationSummary(r); readSafe(); } });
  const rotRow = row("Rotate keys", rotVal, rot.box);
  rotRow.id = "acct-rotate";
  if (open === "rotate") rotRow.open = true;
  // Firmware: what the controller installs and updates with, and Get firmware
  const fwVal = h("span", { class: "val", id: "fw-val" }, "Reading…");
  const readFW = () => readFirmware().then(fw => { fwVal.textContent = summary(fw); }).catch(e => { fwVal.textContent = `Can't read: ${e.message}`; });
  const fwRow = row("Firmware", fwVal,
    h("p", { class: "small" }, "The firmware Install over USB and updates use. A project release is checked with the project's key when fetched and before every update; your nodes only get it signed with your release key."),
    firmwareBox({ id: "fw", done: readFW }));
  fwRow.id = "acct-firmware";
  if (open === "firmware") fwRow.open = true;
  readFW();
  p.body.append(h("div", { class: "rows-ex" },
    row("Password", h("span", { class: "val" }, "Set"), pwForm),
    safeRow,
    rotRow,
    fwRow,
    row("Recent changes", chVal, chList, h("div", { class: "row" }, showAll))));

  const out = h("button", { class: "btn", type: "button", id: "logout" }, "Log out");
  out.addEventListener("click", async () => {
    out.disabled = true;
    try { await post("api/logout"); } catch { /* gone anyway */ }
    loggedIn(null);
    location.href = "login.html";
  });
  p.footer.append(out);

  const size = n => (n >= 1 << 20 ? `${(n / (1 << 20)).toFixed(1)} MiB` : n >= 1 << 10 ? `${(n / (1 << 10)).toFixed(1)} KiB` : `${n} bytes`);
  async function readSafe() {
    const [b, k] = await Promise.all([getJSON("api/backup").catch(e => ({ error: e.message })), getJSON("api/key").catch(e => ({ error: e.message }))]);
    const parts = [];
    if (k.present === false && k.missing) {
      keyLine.hidden = false;
      keyLine.className = "small bad";
      keyLine.textContent = "No release key yet: the controller only reads the nodes until one is made or imported (Make your keys in the setup).";
      parts.push("No release key");
    } else if (k.present === false || k.error) {
      keyLine.hidden = false;
      keyLine.className = "small bad";
      keyLine.textContent = `The release key can't be used: ${str(k.error)}`;
      parts.push("Release key unusable");
    } else keyLine.hidden = true;
    relFP.textContent = k.present ? str(k.fingerprint) : "none yet";
    pubGo.disabled = !k.present;
    const rec = obj(k.recovery);
    recFP.textContent = rec.present ? str(rec.fingerprint) : rec.missing === false && rec.error ? `can't be read: ${str(rec.error)}` : "none yet (Make your keys in the setup)";
    if (!rec.present) parts.push("No recovery key");
    const ps = arr(b.parts);
    const fwp = ps.find(x => x.name === "firmware");
    fwSize.textContent = fwp ? `(${size(Number(fwp.bytes) || 0)})` : "";
    holds.replaceChildren(...(ps.length ? ps.map(x => h("div", { class: "change" },
      h("span", { class: "mono small" }, str(x.name)),
      h("span", { class: "hint" }, `${Number(x.files) || 0} files · ${size(Number(x.bytes) || 0)} · ${x.included ? "in a backup" : "left out"}${x.note ? ` (${str(x.note)})` : ""}`)))
      : [h("p", { class: b.error ? "hint bad" : "hint" }, b.error ? `Can't read it: ${str(b.error)}` : "The data directory is empty.")]));
    if (b.error) parts.push("Backup: can't read");
    else parts.push(`Last backup: ${b.last ? when(b.last).replace(/^T/, "t").replace(/^Y/, "y") : "never"}`);
    safeVal.textContent = parts.join(" · ");
  }

  async function readChanges() {
    let es;
    try { es = recent(await getJSON("api/actions?n=200"), user); } catch (e) {
      chVal.className = "val bad";
      chVal.textContent = "Can't read";
      chList.replaceChildren(h("p", { class: "hint bad" }, e.message));
      return;
    }
    const week = Date.now() - 7 * 86400000;
    const n = es.filter(x => new Date(x.time).getTime() >= week).length;
    chVal.textContent = es.length ? (n ? `${n} this week` : `Last: ${when(es[0].time)}`) : "None yet";
    if (!es.length) { chList.replaceChildren(h("p", { class: "hint" }, "Nothing changed yet. Saved settings, applied changes and updates show here.")); return; }
    // An entry about a node or a zone opens it.
    const to = x => x.href || (x.node ? `node.html#${encodeURIComponent(x.node)}` : "");
    const draw = n => chList.replaceChildren(...es.slice(0, n).map(x => h("div", { class: "change" },
      to(x) ? h("a", { class: x.bad ? "bad" : "", href: to(x) }, x.what) : h("span", { class: x.bad ? "bad" : "" }, x.what),
      h("span", { class: "hint", title: x.error || null }, `${when(x.time)} · ${x.who}`))));
    draw(8);
    showAll.hidden = es.length <= 8;
    showAll.textContent = `Show all ${es.length}`;
    showAll.onclick = () => { draw(es.length); showAll.hidden = true; };
  }

  readSafe();
  readChanges();
}
