// Get firmware (firmware.go; docs/releasing.md, Using a release): the chip images the
// controller has, each with where it came from ("project release v0.0.3", checked with the
// project's key; "your build v0.0.3", signed with your release key; "your build", one you
// exported), and the Get firmware button, which runs the fetch-release job: the newest
// published release downloaded from the forge, checked with the project's key and imported.
// Shown in Install over USB when a board's chip image isn't in the controller yet, and in
// the account panel. Everything from the controller is put in as text.
import { h, arr, obj, str } from "./shell.js";
import { getJSON, post, ended } from "./common.js";

// readFirmware is GET /api/firmware: {images: [{image, version, origin, label, error}],
// release_url, project_key}.
export const readFirmware = () => getJSON("api/firmware");

// labelOf is what an image is, in words: its origin's label.
export const labelOf = i => str(obj(i).label) || "your build";

// summary is the firmware in a few words: the labels there are, or none.
export function summary(fw) {
  const labels = [...new Set(arr(obj(fw).images).map(labelOf))];
  return labels.length ? labels.join(", ") : "None yet";
}

// firmwareBox is the list and the button. o.done runs after an import (the images changed);
// o.id prefixes the elements' ids (two boxes may be on a page).
export function firmwareBox(o = {}) {
  const id = o.id || "fw";
  const list = h("div", { class: "stack nogap", id: `${id}-images` }, h("p", { class: "hint" }, "Reading…"));
  const from = h("p", { class: "hint", id: `${id}-from` });
  const status = h("p", { class: "small", id: `${id}-status`, role: "status", "aria-live": "polite" });
  const go = h("button", { class: "btn primary", type: "button", id: `${id}-get` }, "Get firmware");
  const say = (t, cls = "small") => { status.className = cls; status.textContent = t; };

  async function read() {
    let fw;
    try { fw = await readFirmware(); } catch (e) {
      list.replaceChildren(h("p", { class: "hint bad" }, `Can't read the firmware: ${e.message}`));
      return null;
    }
    const imgs = arr(fw.images);
    list.replaceChildren(...(imgs.length ? imgs.map(i => h("div", { class: "change" },
      h("span", { class: "mono small" }, str(i.image)),
      h("span", { class: i.error ? "hint bad" : "hint", title: str(i.error) || null }, `${labelOf(i)}${i.error ? ` (${str(i.error)})` : ""}`)))
      : [h("p", { class: "hint" }, "No firmware yet: get the project's release, or import a build of your own (espdns release import).")]));
    const key = str(fw.project_key);
    from.textContent = key
      ? `From ${str(fw.release_url)}, checked with the project's release key ${key}. Your nodes never trust that key: the controller signs what it sends them with yours.`
      : `No project key here (/keys/project.pub), so no release can be fetched: import one with espdns release import.`;
    go.disabled = !key;
    return fw;
  }

  go.addEventListener("click", async () => {
    go.disabled = true;
    say("Starting…");
    try {
      let j = await post("api/jobs", { kind: "fetch-release", params: {} });
      while (!ended(j.state)) {
        await new Promise(r => setTimeout(r, 400));
        j = await getJSON(`api/jobs/${encodeURIComponent(j.id)}`);
        const log = arr(j.log);
        const last = log.length ? str(obj(log[log.length - 1]).text) : str(j.last);
        if (last) say(last);
      }
      if (j.state !== "done") throw new Error(str(j.error) || `the job ${str(j.state)}`);
      const res = obj(j.result);
      say(`Imported ${str(res.label)}: ${arr(res.images).map(str).join(", ")}.`, "small ok");
      await read();
      if (o.done) await o.done(res);
    } catch (e) {
      say(`Not imported: ${e.message}`, "small bad");
      go.disabled = false;
    }
  });

  read();
  return h("div", { class: "stack", id: `${id}-box` }, list, h("div", { class: "row" }, go), from, status);
}
