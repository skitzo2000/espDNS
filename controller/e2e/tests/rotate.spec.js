// Rotate keys (rotate.js, in the account panel) and the keys said on Nodes (home.js), against
// fake nodes: serve.sh's controller logs in and serves the pages; the rotation's APIs, the
// job and the nodes are answered here (page.route), as a controller with three nodes would
// (documentation addresses, RFC 5737), so nothing goes on the network. The flow: prepare (a
// new release key, the recovery key kept), the exact offline signing command, a signed
// release added from a file, push (the job "key-rotate"), done.
const { test, expect } = require("@playwright/test");
const { login } = require("./session");

async function watch(page) {
  const seen = [];
  await page.addInitScript(() => {
    window.__csp = [];
    document.addEventListener("securitypolicyviolation",
      e => window.__csp.push(`${e.violatedDirective} blocked ${e.blockedURI || "inline"} (${e.sourceFile}:${e.lineNumber})`));
  });
  page.on("console", m => { if (/Content.Security.Policy/i.test(m.text())) seen.push(m.text()); });
  page.on("pageerror", e => seen.push(`uncaught: ${e.message}`));
  return async () => [...seen, ...await page.evaluate(() => window.__csp)];
}

const OLD = "a1b2c3d4e5f60718", NEW = "9f8e7d6c5b4a3921", REC = "0f1e2d3c4b5a6978";
const N1 = "192.0.2.53", N2 = "192.0.2.54", N3 = "192.0.2.55";
const ID = "20261009-170000";
const CMD = `docker run --rm -i --network none --user "$(id -u):$(id -g)" -v "$PWD/data/keys/rotation/${ID}:/rotation" --entrypoint /espdns espdns-controller key rotate sign -dir /rotation -key - < recovery.pem`;
const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });

function plan(state) {
  const node = (addr, id, st) => ({ addr, node_id: id, image: "esp32s3-octal", seq: 1791580000000, dir: id.replaceAll(":", ""), state: st,
    repush: ["config dns.json", "hosted zones home.example", "blocklist main.bin"] });
  const states = state.pushed ? ["done", "done", "done"] : ["prepared", "prepared", "prepared"];
  const ns = [node(N1, "02:00:00:00:10:01", states[0]), node(N2, "02:00:00:00:10:02", states[1]), node(N3, "02:00:00:00:10:03", states[2])];
  const signed = Object.fromEntries(ns.map(n => [n.node_id, state.signed]));
  return { version: 1, id: ID, created: new Date().toISOString(), compromised: false, old: { release: OLD, recovery: REC },
    new: { release: NEW, recovery: REC }, new_release_key: true, nodes: ns, signed, signed_all: state.signed,
    sign_command: CMD, dir: `keys/rotation/${ID}`, ...(state.pushed ? { finished: new Date().toISOString() } : {}) };
}

async function fake(page) {
  const state = { prepared: false, signed: false, pushed: false, posts: [] };
  await page.route(/\/api\/key$/, r => json(r, { present: true, source: "file", fingerprint: state.pushed ? NEW : OLD,
    recovery: { present: true, fingerprint: REC }, nodes_listed: true,
    rotation: state.prepared && !state.pushed ? { id: ID, release: NEW, recovery: REC } : null }));
  await page.route(/\/api\/keys\/rotation$/, r => {
    if (r.request().method() === "POST") {
      state.posts.push(["prepare", r.request().postDataJSON()]);
      state.prepared = true;
      return json(r, plan(state), 201);
    }
    const p = state.prepared ? plan(state) : null;
    return json(r, { pending: p && !state.pushed ? p : null, rotations: p ? [p] : [] });
  });
  await page.route(/\/api\/keys\/rotation\/[^/]+\/signed$/, r => {
    const b = r.request().postDataJSON();
    state.posts.push(["signed", b.release]);
    state.signed = true;
    return json(r, plan(state));
  });
  await page.route(/\/api\/jobs$/, r => {
    if (r.request().method() !== "POST") return r.fallback();
    state.posts.push(["job", r.request().postDataJSON()]);
    return json(r, { id: "j9", kind: "key-rotate", who: "admin", state: "running", created: new Date().toISOString() }, 201);
  });
  await page.route(/\/api\/jobs\/j9$/, r => {
    state.pushed = true;
    return json(r, { id: "j9", kind: "key-rotate", state: "done", log: [{ n: 1, text: `rotation ${ID} finished` }], progress: plan(state) });
  });
  return state;
}

test("Rotate keys: prepare, the offline signing command, a signed release added, push", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("./");
  await page.getByRole("button", { name: `Account: ${process.env.E2E_USER}` }).click();
  const d = page.getByRole("dialog", { name: process.env.E2E_USER });
  await expect(d.locator("details.ex > summary > b")).toHaveText(["Password", "Keep the controller safe", "Rotate keys", "Firmware", "Recent changes"]);
  await expect(d.locator("#rot-val")).toHaveText("None under way");
  await d.locator("#acct-rotate > summary").click();

  // 1. Prepare: a new release key, the recovery key kept (it is there: no file asked for).
  await expect(d.locator("#rot-new-release")).toBeChecked();
  await expect(d.locator("#rot-rec-keep")).toBeChecked();
  await expect(d.locator("#rot-rec-current")).toHaveCount(0);
  await expect(d.locator("#rot-rec-file")).toBeDisabled();
  await d.locator("#rot-prepare").click();
  await expect(d.locator("#rot-what")).toHaveText(`Rotation ${ID}: release ${OLD} → ${NEW}, recovery ${REC} → ${REC}.`);
  expect(state.posts[0]).toEqual(["prepare", { new_release: "generate", compromised: false }]);
  await expect(d.locator("#rot-val")).toHaveText("Prepared: sign it offline");

  // 2. Sign offline: the command, exactly; Push waits for every node's signed release.
  await expect(d.locator("#rot-cmd")).toHaveText(CMD);
  await expect(d.locator("#rot-nodes .change")).toHaveCount(3);
  await expect(d.locator("#rot-nodes .change").first()).toContainText("not signed yet");
  await expect(d.locator("#rot-push")).toBeDisabled();
  await expect(d.locator("#rot-discard")).toBeVisible();
  const signed = Buffer.alloc(330, 7);
  await d.locator("#rot-signed").setInputFiles({ name: "release.bin", mimeType: "application/octet-stream", buffer: signed });
  await expect(d.locator("#rot-msg")).toHaveText("Added 1 signed release.");
  expect(state.posts[1]).toEqual(["signed", signed.toString("base64")]);
  await expect(d.locator("#rot-val")).toHaveText("Signed: push it");
  await expect(d.locator("#rot-nodes .change").first()).toContainText("signed");

  // 3. Push: the job, then done; the row's release key is the new one.
  await d.locator("#rot-push").click();
  await expect(d.locator("#rot-msg")).toContainText("Every node has its new keys");
  expect(state.posts[2]).toEqual(["job", { kind: "key-rotate", params: { id: ID } }]);
  await expect(d.locator("#rot-val")).toHaveText("None under way");
  await expect(d.locator("#rot-last")).toContainText(`release ${NEW}`);
  await expect(d.locator("#acct-release-fp")).toHaveText(NEW);
  expect(await violations()).toEqual([]);
});

test("Nodes: a node not on the rotation's new keys says so; one in setup mode needs keys", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  state.prepared = true;
  const node = (addr, id, name, status) => ({ id, addr, source: "settings", online: true, last_seen: new Date().toISOString(), qps: 0.2,
    status: { board: "ws-s3-eth", version: "0.0.4", uptime_s: 600, net: { kind: "ethernet" }, config: { name, source: "node" }, keys_source: "partition", ...status } });
  await page.route(/\/api\/nodes$/, r => json(r, [
    node(N1, "02:00:00:00:10:01", "dns1", { keys: [NEW, REC], health: { state: "healthy", answering: true, reasons: [] } }),
    node(N2, "02:00:00:00:10:02", "dns2", { keys: [OLD, REC], health: { state: "healthy", answering: true, reasons: [] } }),
    node(N3, "02:00:00:00:10:03", "dns3", { keys: ["", ""], keys_source: "none", health: { state: "degraded", answering: true, reasons: ["no keys"] } }),
  ]));
  await page.goto("./");
  const rows = page.locator("#nodes .r:not(.headr)");
  await expect(rows).toHaveCount(3);
  await expect(rows.nth(1)).toContainText("Old keys (rotation pending)");
  await expect(rows.nth(0)).not.toContainText("Old keys");
  await expect(rows.nth(2)).toContainText("Needs keys (setup mode)");
  await expect(rows.nth(2)).not.toContainText("No keys");
  expect(await violations()).toEqual([]);
});
