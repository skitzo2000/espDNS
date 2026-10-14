// Rotate keys, in the account panel (docs/security.md, Rotating keys; rotation.go): new keys
// for every node, in three steps.
// 1. Prepare (POST /api/keys/rotation): a new release key made in the controller (or the one
//    there kept), a new recovery key's PUBLIC half from a file (or the one there kept), and
//    whether the old release key is compromised. The controller reads every listed node and
//    writes each one's unsigned KEYS release.
// 2. Sign, offline: the exact command to run on this machine (no network; the recovery key on
//    standard input, never given to the controller). A release signed elsewhere is added here
//    (POST /api/keys/rotation/{id}/signed, each node's release.bin).
// 3. Push (the job "key-rotate"): one node at a time, each one's stored settings, zones and
//    lists signed again with the new key; the progress live; stopped at a node, Push again
//    goes on from it.
// A rotation no node has taken is discarded here. Everything from the controller is put in
// as text.
import { h, arr, obj, str, toast, copyText } from "./shell.js";
import { getJSON, post, ended } from "./common.js";
import { readPublic, base64 } from "./keys.js";

const STATE = { prepared: "waiting", keys: "new keys in use; settings, zones and lists not all signed again yet", done: "done" };

// summary is the rotation under way in a few words, for the row while closed.
export function rotationSummary(r) {
  const p = obj(obj(r).pending);
  const ns = arr(p.nodes);
  if (!p.id) return "None under way";
  const done = ns.filter(n => n.state === "done").length;
  if (done || ns.some(n => n.state === "keys")) return `Under way: ${done} of ${ns.length} nodes done`;
  return p.signed_all ? "Signed: push it" : "Prepared: sign it offline";
}

// rotateBox is the row's body; o.changed runs after anything changed (the row's summary).
export function rotateBox(o = {}) {
  const box = h("div", { class: "stack", id: "rot" });
  const msg = h("p", { class: "hint", role: "status", "aria-live": "polite", id: "rot-msg" });
  const say = (t, cls = "") => { msg.textContent = t; msg.className = `hint ${cls}`; };
  let busy = false;

  async function read() {
    let r, k;
    try { [r, k] = await Promise.all([getJSON("api/keys/rotation"), getJSON("api/key")]); } catch (e) {
      box.replaceChildren(h("p", { class: "hint bad" }, `Can't read: ${e.message}`));
      return;
    }
    render(r, k);
    if (o.changed) o.changed(r);
  }

  function render(r, k) {
    const p = obj(r.pending);
    if (!p.id) { box.replaceChildren(...prepareForm(k, arr(r.rotations)), msg); return; }
    box.replaceChildren(...under(p), msg);
  }

  // ---- 1. prepare ----
  function prepareForm(k, past) {
    const last = past.find(x => x.finished && !String(x.finished).startsWith("0001"));
    const rec = obj(k.recovery);
    const newRel = h("input", { type: "checkbox", id: "rot-new-release", checked: true });
    const recKeep = h("input", { type: "radio", name: "rot-rec", id: "rot-rec-keep", checked: true });
    const recNew = h("input", { type: "radio", name: "rot-rec", id: "rot-rec-new" });
    const recFile = h("input", { type: "file", id: "rot-rec-file", disabled: true });
    recKeep.addEventListener("change", () => { recFile.disabled = true; });
    recNew.addEventListener("change", () => { recFile.disabled = false; });
    const curFile = h("input", { type: "file", id: "rot-rec-current" });
    const comp = h("input", { type: "checkbox", id: "rot-compromised" });
    comp.addEventListener("change", () => { if (comp.checked) newRel.checked = true; });
    newRel.addEventListener("change", () => { if (!newRel.checked) comp.checked = false; });
    const go = h("button", { class: "btn primary", type: "submit", id: "rot-prepare" }, "Prepare");
    const form = h("form", { class: "stack" },
      h("label", { class: "check" }, newRel, h("span", {}, "A new release key, made in the controller")),
      h("fieldset", { class: "stack nogap" },
        h("legend", { class: "small" }, "Recovery key"),
        h("label", { class: "check" }, recKeep, h("span", {}, `Keep the one your nodes trust${rec.present ? ` (${str(rec.fingerprint)})` : ""}`)),
        h("label", { class: "check" }, recNew, h("span", {}, "A new one: its public key file (recovery.pub)")),
        recFile),
      rec.present ? null : h("label", { class: "f" },
        h("span", {}, "The recovery public key your nodes trust now (recovery.pub)"), curFile,
        h("span", { class: "hint" }, "The public half only: the controller doesn't have it yet, and checks the signed releases with it.")),
      h("label", { class: "check" }, comp, h("span", {}, "The release key is compromised: nodes drop it at once instead of keeping it for what they stored")),
      h("div", { class: "row" }, go));
    form.addEventListener("submit", async e => {
      e.preventDefault();
      if (busy) return;
      busy = go.disabled = true;
      try {
        const body = { new_release: newRel.checked ? "generate" : "keep", compromised: comp.checked };
        if (recNew.checked) {
          if (!recFile.files[0]) throw new Error("Choose the new recovery key's public key file.");
          body.new_recovery = base64(await readPublic(recFile.files[0]));
        }
        if (!rec.present && curFile.files[0]) body.recovery_pub = base64(await readPublic(curFile.files[0]));
        say("Reading every node and preparing…");
        await post("api/keys/rotation", body);
        say("Prepared: now sign it offline.", "ok");
        await read();
      } catch (err) { say(err.message, "bad"); } finally { busy = go.disabled = false; }
    });
    return [
      h("p", { class: "small" }, "New keys for every node, signed offline with the recovery key they trust. The controller prepares and pushes; it never has the recovery key."),
      last ? h("p", { class: "hint", id: "rot-last" }, `Last rotation ${str(last.id)}: release ${str(obj(last.new).release)}, recovery ${str(obj(last.new).recovery)}.`) : null,
      form];
  }

  // ---- 2. sign, 3. push ----
  function under(p) {
    const old = obj(p.old), nw = obj(p.new), signed = obj(p.signed);
    const started = arr(p.nodes).some(n => n.state !== "prepared");
    const nodes = h("div", { class: "stack nogap", id: "rot-nodes" }, ...arr(p.nodes).map(n => h("div", { class: "change" },
      h("span", { class: "mono small" }, `${str(n.addr)} · ${str(n.node_id)}`),
      h("span", { class: n.error ? "hint bad" : "hint" },
        [STATE[n.state] || str(n.state), n.state === "prepared" ? (signed[n.node_id] ? "signed" : "not signed yet") : "",
          n.error ? `stopped here: ${str(n.error)}` : "",
          arr(n.missing).length ? `not signed again (still loads with the retired key): ${arr(n.missing).map(str).join("; ")}` : ""].filter(Boolean).join(" · ")))));
    const cmd = str(p.sign_command);
    const copy = h("button", { class: "btn small", type: "button", id: "rot-copy" }, "Copy the command");
    copy.addEventListener("click", async () => toast(await copyText(cmd) ? "Copied the signing command" : "Couldn't copy: select it and copy it"));
    const files = h("input", { type: "file", id: "rot-signed", multiple: true });
    files.addEventListener("change", async () => {
      try {
        for (const f of files.files) {
          const b = new Uint8Array(await f.arrayBuffer());
          await post(`api/keys/rotation/${encodeURIComponent(p.id)}/signed`, { release: base64(b) });
        }
        say(`Added ${files.files.length} signed release${files.files.length === 1 ? "" : "s"}.`, "ok");
      } catch (err) { say(err.message, "bad"); }
      files.value = "";
      read();
    });
    const again = h("button", { class: "btn small", type: "button", id: "rot-read" }, "Check again");
    again.addEventListener("click", () => { say(""); read(); });
    const push = h("button", { class: "btn primary", type: "button", id: "rot-push", disabled: !p.signed_all }, started ? "Push again" : "Push");
    push.addEventListener("click", () => runPush(p.id, push));
    const discard = h("button", { class: "btn small", type: "button", id: "rot-discard", hidden: started }, "Discard");
    discard.addEventListener("click", async () => {
      try { await post(`api/keys/rotation/${encodeURIComponent(p.id)}/discard`, {}); say("Discarded.", "ok"); } catch (err) { say(err.message, "bad"); }
      read();
    });
    return [
      h("p", { class: "small", id: "rot-what" }, `Rotation ${str(p.id)}: release ${str(old.release)} → ${str(nw.release)}, recovery ${str(old.recovery)} → ${str(nw.recovery)}.`),
      h("p", { class: p.compromised ? "small bad" : "hint" }, p.compromised
        ? "Compromised: each node drops the old release key at once, then everything it stores is signed again with the new key."
        : str(old.release) === str(nw.release) ? "The release key stays." : "Each node keeps the old release key for what it stored until that's signed again with the new key."),
      nodes,
      h("b", { class: "small" }, "Sign it offline"),
      h("p", { class: "small" }, `With the recovery key ${str(old.recovery)}, on this machine, from the controller's folder (no network; the key is read, never written):`),
      h("pre", { class: "cmd", id: "rot-cmd" }, cmd),
      h("div", { class: "row" }, copy, again),
      h("label", { class: "f" }, h("span", {}, "Signed elsewhere? Add each node's release.bin"), files),
      h("b", { class: "small" }, "Push"),
      h("p", { class: "hint" }, "One node at a time: its new keys, then its settings, zones and lists signed again, then a health check. A failure stops it; Push again carries on from there."),
      h("div", { class: "row" }, push, discard)];
  }

  async function runPush(id, btn) {
    btn.disabled = true;
    say("Starting…");
    try {
      let j = await post("api/jobs", { kind: "key-rotate", params: { id } });
      while (!ended(j.state)) {
        await new Promise(r => setTimeout(r, 500));
        j = await getJSON(`api/jobs/${encodeURIComponent(j.id)}`);
        const log = arr(j.log);
        const last = log.length ? str(obj(log[log.length - 1]).text) : str(j.last);
        if (last) say(last);
      }
      if (j.state !== "done") throw new Error(str(j.error) || `the push ${str(j.state)}`);
      say("Every node has its new keys; the controller signs with the new release key now.", "ok");
      toast("Keys rotated");
    } catch (err) { say(`Stopped: ${err.message}`, "bad"); }
    read();
  }

  read();
  return { box, read };
}
