// The first run (setup.html, setup.js, setupcheck.js) and the login page in the shell's look,
// against a fake controller: serve.sh's controller serves the pages (a login first, as it has
// a password), and every API the pages read is answered here (page.route), the session
// included, so the page sees a fresh install; documentation addresses (RFC 5737) only, and
// nothing goes on the network. E2E_SHOTS=<dir> also takes each step at 375 and 1366 px,
// light and dark, into <dir>/firstrun/.
const fs = require("node:fs");
const path = require("node:path");
const crypto = require("node:crypto");
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

const DNS2 = "192.0.2.53", DNS3 = "192.0.2.54", FOUND = "198.51.100.37", PRIMARY = "192.0.2.254";
const ORDER = ["password", "keys", "first_node", "second_node", "zones", "router", "blocking"];
const RELFP = "a1b2c3d4e5f60718", RECFP = "0f1e2d3c4b5a6978";

function node(addr, id, name) {
  return {
    id, addr, hostname: `${name}.local`, source: "settings", online: true, last_seen: new Date().toISOString(), qps: 0.2,
    status: { board: "p4-ip101", version: "0.0.4", uptime_s: 600, net: { kind: "ethernet" }, health: { state: "healthy", answering: true, reasons: [] }, config: { name } },
  };
}

// fake answers the APIs from state, which a test changes as the controller would; posts is
// what was sent. done: the steps done at the start.
async function fake(page, done = []) {
  // Off the page first: one still loading (the Nodes page after the login) would read these
  // answers and go to the setup itself, racing the test's own navigation.
  await page.goto("about:blank");
  await page.unrouteAll({ behavior: "ignoreErrors" });
  const state = {
    loggedIn: done.includes("password"), done: new Set(done), key: done.includes("keys"), recFP: done.includes("keys") ? RECFP : "",
    nodes: [], found: [{ host: FOUND, id: "aa:bb:cc:00:00:37", hostname: "espdns-000037.local", source: "mdns", online: true, read: true,
      board: "p4-ip101", version: "0.0.4", net: "ethernet", mac: "aa:bb:cc:00:00:3a", adopted: false, listed: false, configs: true, config: "dns2.json", next: "adopt" }],
    blocklists: 0, posts: [], sent: [],
    // The pending changes (what /api/changes lists), and the node configs as they make them:
    // one per node, with no zones of its own. The controller counts a zone from either.
    changes: [], configs: {},
  };
  if (done.includes("first_node")) state.nodes.push(node(`${DNS2}:80`, "aa:bb:cc:00:00:02", "dns2"));
  if (done.includes("second_node")) state.nodes.push(node(`${DNS3}:80`, "aa:bb:cc:00:00:03", "dns3"));
  for (const n of state.nodes) state.configs[`${n.status.config.name}.json`] = JSON.stringify({ name: n.status.config.name, wifi: { ssid: "home", password: "••••••••" } }, null, 2);
  const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
  // zones counts the zones of your own as the controller does: hosted zones waiting as
  // pending changes, and the configs' secondary and forward zones.
  const zones = () => {
    const seen = new Set();
    for (const c of state.changes) if (c.kind === "zone" && !c.delete) seen.add(`hosted ${c.name}`);
    for (const t of Object.values(state.configs)) {
      const cfg = JSON.parse(t);
      for (const z of (cfg.secondary || {}).zones || []) seen.add(`secondary ${z}`);
      for (const z of cfg.forward_zones || []) seen.add(`forward ${z.zone}`);
    }
    return seen.size;
  };
  const setup = () => {
    const steps = ORDER.map(s => ({ step: s, done: s === "password" ? state.loggedIn : s === "keys" ? state.key && !!state.recFP && state.done.has("keys")
      : s === "first_node" ? state.nodes.length >= 1 : s === "second_node" ? state.nodes.length >= 2 : s === "zones" ? zones() > 0 || state.done.has("zones") : state.done.has(s),
    optional: s === "zones" || s === "blocking" || undefined }));
    return { first_run: !state.loggedIn, password_set: state.loggedIn, settings_saved: state.nodes.length > 0, release_key: state.key,
      recovery_key: !!state.recFP, keys_saved: state.done.has("keys"), primary: "", nodes: state.nodes.length, steps,
      done: steps.every(s => s.done), blocklists: state.blocklists, zones: zones() };
  };
  // GET /api/key as keysapi.go answers it.
  const key = () => ({ source: "file", ...(state.key ? { present: true, fingerprint: RELFP } : { present: false, missing: true, error: "no release key" }),
    recovery: state.recFP ? { present: true, fingerprint: state.recFP } : { present: false, missing: true, error: "no recovery public key" },
    nodes_listed: state.nodes.length > 0 });
  await page.route(/\/api\/session$/, r => json(r, state.loggedIn ? { password_set: true, logged_in: true, user: "admin" } : { password_set: false, logged_in: false }));
  await page.route(/\/api\/setup$/, r => json(r, setup()));
  await page.route(/\/api\/setup\/password$/, r => {
    const b = r.request().postDataJSON();
    state.posts.push(["password", b]);
    if (state.refuse) return json(r, { error: "the first password is set from the controller's own machine only" }, 403);
    state.loggedIn = true;
    return json(r, { user: b.user, token: "e2e-first-run-token", device: "d", expires: new Date(Date.now() + 3600e3).toISOString() });
  });
  await page.route(/\/api\/setup\/step$/, r => {
    const b = r.request().postDataJSON();
    state.posts.push(["step", b]);
    state.done.add(b.step);
    return json(r, setup());
  });
  await page.route(/\/api\/key$/, r => json(r, key()));
  await page.route(/\/api\/keys\/release$/, r => {
    state.posts.push(["release", r.request().postDataJSON()]);
    state.key = true;
    return json(r, key());
  });
  // The recovery key: what was sent kept whole (raw), to check nothing but the public key went.
  await page.route(/\/api\/keys\/recovery$/, r => {
    const raw = r.request().postData();
    state.posts.push(["recovery", JSON.parse(raw)]);
    state.sent.push(raw);
    const pub = Buffer.from(JSON.parse(raw).public_key, "base64");
    if (pub.length !== 65 || pub[0] !== 4) return json(r, { error: "not a P-256 public key", field: "public_key" }, 400);
    state.recFP = crypto.createHash("sha256").update(pub).digest("hex").slice(0, 16);
    return json(r, key());
  });
  // The settings as the shell reads them; the setup saves nothing here (a POST would show in
  // posts). No zone primary: that is the Zones page's, not a step.
  const settings = () => ({ settings: { nodes: state.nodes.map(n => n.addr), dns_peers: [], dns_peer_zones: [], canary: "", no_dhcp: [], primary: null, internal_sources: [] },
    version: state.nodes.length ? "v1" : "", exists: state.nodes.length > 0,
    primary: { set: false, address: "", zones: [] } });
  await page.route(/\/api\/settings$/, r => {
    if (r.request().method() === "POST") state.posts.push(["settings", r.request().postDataJSON()]);
    return json(r, settings());
  });
  await page.route(/\/api\/nodes$/, r => json(r, state.nodes));
  await page.route(/\/api\/nodes\/found$/, r => json(r, { nodes: state.found }));
  await page.route(/\/api\/nodes\/waiting$/, r => json(r, { nodes: [] }));
  await page.route(/\/api\/boards$/, r => json(r, [{ board: { name: "p4-ip101", title: "ESP32-P4 with Ethernet" } }]));
  // Add your zone: the APIs + Add zone uses (addzone.js), as zones.spec.js's fake answers
  // them: the lookup (no primary set up, so nothing is asked), a hosted zone's template, the
  // configs and the pending changes.
  await page.route(/\/api\/changes$/, r => {
    if (r.request().method() !== "POST") return json(r, { changes: state.changes.map(({ text, ...c }) => c), restarts: [], apply: null });
    const b = r.request().postDataJSON();
    state.posts.push(["change", b]);
    const c = { id: `c${state.changes.length + 1}`, kind: b.kind, name: b.name, delete: !!b.delete, summary: b.summary, text: b.text,
      effect: b.kind === "zone" ? "Hosted zones: sent to every node, live (no restart)" : "live (no restart)" };
    if (b.kind === "config") state.configs[b.name] = b.text;
    state.changes.push(c);
    return json(r, c, 201);
  });
  await page.route(/\/api\/changes\/file\?/, r => {
    const u = new URL(r.request().url()), kind = u.searchParams.get("kind"), name = u.searchParams.get("name");
    if (kind === "config") { const t = state.configs[name]; return json(r, { kind, name, exists: !!t, text: t, hash: t ? `h${t.length}` : "" }); }
    const c = state.changes.find(x => x.kind === "zone" && x.name === name);
    return json(r, c ? { kind, name, exists: true, pending: true, text: c.text, hash: `h${c.text.length}` } : { kind, name, exists: false, hash: "" });
  });
  await page.route(/\/api\/configs$/, r => json(r, { configs: Object.keys(state.configs).map(name => ({ name, nodes: [{ host: name === "dns2.json" ? DNS2 : DNS3, listed: true }] })), nodes: [] }));
  await page.route(/\/api\/zone-inventory$/, r => json(r, { zones: [], counts: { hosted: 0, secondary: 0, forward: 0, records: 0 }, primaries: [], nodes: state.nodes.length,
    fleet: state.nodes.length, primary: { set: false, address: "", zones: [] } }));
  await page.route(/\/api\/zone-lookup\?/, r => {
    const name = new URL(r.request().url()).searchParams.get("name").toLowerCase().replace(/\.$/, "");
    state.posts.push(["lookup", name]);
    return json(r, { name, found: "none", tried: [], text: "" });
  });
  await page.route(/\/api\/zones\/[^/]+\/new$/, r => {
    const name = decodeURIComponent(r.request().url().match(/\/api\/zones\/([^/]+)\/new$/)[1]);
    return json(r, { name, text: `$TTL 3600\n@ IN SOA ns1 hostmaster ( 2026100701 3600 600 604800 300 )\n  IN NS ns1\n` });
  });
  await page.route(/\/api\/updates$/, r => json(r, { nodes: [] }));
  await page.route(/\/api\/dashboard$/, r => json(r, { fleet: {} }));
  return state;
}

const row = (page, step) => page.locator(`#step-${step}`);
// open loads url afresh (going to setup.html#next from setup.html would only change the hash).
async function open(page, url) {
  await page.goto("about:blank");
  await page.goto(url);
}

test("first run: the steps, then the password set from the browser", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("setup.html");
  await expect(page.locator("h1")).toHaveText("Set up espDNS in 10 minutes");
  await expect(page.locator("#steps .s")).toHaveCount(7);
  await expect(page.locator("#steps .s b")).toHaveText(["Set a password", "Make your keys",
    "Add your first node", "Add a second node", "Add your zone", "Tell your router", "Turn on blocking"]);
  await expect(page.locator("#steps")).not.toContainText(/zone primary/i);
  await expect(page.locator("#facts")).toHaveText("0 of 7 steps done");
  // The read-only band isn't shown here: this page sets the password.
  await expect(page.locator(".band")).toHaveCount(0);
  await expect(page.locator(".s .btn.primary")).toHaveCount(1);
  await row(page, "password").getByRole("button", { name: "Start" }).click();
  await expect(page.locator("#su-user")).toHaveValue("admin");

  await page.locator("#su-pw").fill("short");
  await page.locator("#su-pw2").fill("short");
  await page.getByRole("button", { name: "Set password" }).click();
  await expect(row(page, "password").locator("[role=status]")).toHaveText("The password needs at least 12 characters.");
  await page.locator("#su-pw").fill("a long e2e password");
  await page.locator("#su-pw2").fill("a long e2e passwore");
  await page.getByRole("button", { name: "Set password" }).click();
  await expect(row(page, "password").locator("[role=status]")).toContainText("differ");
  expect(state.posts).toEqual([]);

  await page.locator("#su-pw2").fill("a long e2e password");
  await page.getByRole("button", { name: "Set password" }).click();
  // Logged in (the token kept), the page again with Make your keys open.
  await expect(row(page, "password")).toHaveClass(/\bdone\b/);
  await expect(row(page, "password")).toContainText("you're logged in as admin");
  expect(state.posts).toEqual([["password", { user: "admin", password: "a long e2e password" }]]);
  expect(await page.evaluate(() => localStorage.getItem("espdns.session"))).toBe("e2e-first-run-token");
  await expect(row(page, "keys").locator("#keys-release-make")).toBeVisible();
  await expect(page.locator("#facts")).toHaveText("1 of 7 steps done");
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

test("first run: the password refused from another machine says where to set it", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  state.refuse = true;
  await page.goto("setup.html");
  await row(page, "password").getByRole("button", { name: "Start" }).click();
  await page.locator("#su-pw").fill("a long e2e password");
  await page.locator("#su-pw2").fill("a long e2e password");
  await page.getByRole("button", { name: "Set password" }).click();
  const msg = row(page, "password").locator("[role=status]");
  await expect(msg).toContainText("own machine only");
  await expect(msg).toContainText("espdns passwd");
  await expect(msg).not.toContainText("make ");
  expect(await violations()).toEqual([]);
});

// Make your keys: the release key made in the controller; the recovery key made in this
// browser (WebCrypto, headless Chromium), recovery.pem downloaded, and ONLY its public half
// posted: the request carries the 65-byte point and nothing of the private key; then
// "I saved recovery.pem" ticked, and the step done.
test("Make your keys: the release key in the controller, the recovery key in the browser, only its public half sent", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, ["password"]);
  // Every request the page makes from here, to check none carries the private key.
  const bodies = [];
  page.on("request", r => bodies.push(`${r.method()} ${r.url()} ${r.postData() || ""}`));
  await page.goto("setup.html");
  await row(page, "keys").getByRole("button", { name: "Start" }).click();
  const body = row(page, "keys").locator(".sb");
  await expect(body).not.toContainText(/\bnull\b|undefined/);
  // Nothing made yet: the recovery key waits for the release key; Done waits for both.
  await expect(body.locator("#keys-recovery-make")).toBeDisabled();
  await expect(body.locator("#keys-saved")).toBeDisabled();
  await expect(body.locator("#keys-done")).toBeDisabled();
  await expect(body.locator("#keys-import pre.cmd")).toContainText("key import -data /data");

  await body.locator("#keys-release-make").click();
  await expect(body.locator("#keys-release-fp")).toHaveText(RELFP);
  expect(state.posts).toEqual([["release", {}]]);

  const download = page.waitForEvent("download");
  await body.locator("#keys-recovery-make").click();
  const dl = await download;
  expect(dl.suggestedFilename()).toBe("recovery.pem");
  const pem = fs.readFileSync(await dl.path(), "utf8");
  expect(pem).toMatch(/^-----BEGIN PRIVATE KEY-----\n[A-Za-z0-9+/=\n]+-----END PRIVATE KEY-----\n$/);
  // A P-256 PKCS#8 key that Node reads, whose public half is the one posted.
  const priv = crypto.createPrivateKey(pem);
  expect(priv.asymmetricKeyDetails.namedCurve).toBe("prime256v1");
  const pubRaw = crypto.createPublicKey(priv).export({ format: "der", type: "spki" }).subarray(-65);
  await expect(body.locator("#keys-recovery-fp")).toHaveText(state.recFP);
  expect(state.posts[1][0]).toBe("recovery");
  expect(Object.keys(state.posts[1][1]).sort()).toEqual(["public_key", "replace"]);
  expect(Buffer.from(state.posts[1][1].public_key, "base64").equals(pubRaw)).toBe(true);
  expect(state.recFP).toBe(crypto.createHash("sha256").update(pubRaw).digest("hex").slice(0, 16));
  // Nothing sent anywhere holds the private key: not the PEM, its base64 body, nor the scalar.
  const pemBody = pem.split("\n").filter(l => l && !l.startsWith("-----")).join("");
  const d = priv.export({ format: "jwk" }).d;
  for (const sent of [...state.sent, ...bodies]) {
    expect(sent).not.toContain("PRIVATE");
    expect(sent).not.toContain(pemBody.slice(0, 40));
    expect(sent).not.toContain(d);
    expect(sent).not.toContain(Buffer.from(d, "base64url").toString("base64"));
    expect(sent).not.toContain(Buffer.from(d, "base64url").toString("hex"));
  }
  // The file again, while the page is open.
  await expect(body.locator("#keys-recovery-again")).toBeVisible();

  // Done once recovery.pem is said to be saved.
  await expect(body.locator("#keys-done")).toBeDisabled();
  await body.locator("#keys-saved").check();
  await body.locator("#keys-done").click();
  await expect(row(page, "keys")).toHaveClass(/\bdone\b/);
  await expect(row(page, "keys")).toContainText(`Release ${RELFP} · recovery ${state.recFP} · recovery.pem kept offline`);
  expect(state.posts.at(-1)).toEqual(["step", { step: "keys" }]);
  // The next step, the first node, opens; Back it up opens the account panel at the backup.
  await expect(row(page, "first_node").getByRole("button", { name: "+ Add node" })).toBeVisible();
  await expect(row(page, "first_node").getByRole("button", { name: "Install over USB" })).toBeVisible();
  await row(page, "keys").getByRole("button", { name: "Back it up" }).click();
  await expect(page.locator("aside.drawer #acct-safe")).toHaveAttribute("open", "");
  expect(await violations()).toEqual([]);
});

// A recovery key you have: its public key file (raw 65 bytes, or a PEM public key) read in the
// browser and posted; a private key file refused there and never sent.
test("Make your keys: a recovery public key file, a private key file refused", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, ["password"]);
  state.key = true;
  await page.goto("setup.html");
  await row(page, "keys").getByRole("button", { name: "Start" }).click();
  const body = row(page, "keys").locator(".sb");
  await expect(body.locator("#keys-release-fp")).toHaveText(RELFP);
  await body.locator("#keys-recovery-import summary").click();
  const { privateKey, publicKey } = crypto.generateKeyPairSync("ec", { namedCurve: "prime256v1" });
  const privPem = privateKey.export({ format: "pem", type: "pkcs8" });
  await body.locator("#keys-recovery-file").setInputFiles({ name: "recovery.pem", mimeType: "application/x-pem-file", buffer: Buffer.from(privPem) });
  await expect(body.locator("[role=status]")).toContainText("That's a private key");
  expect(state.posts).toEqual([]);
  // The PEM public key: the same point as the raw file.
  const raw = publicKey.export({ format: "der", type: "spki" }).subarray(-65);
  await body.locator("#keys-recovery-file").setInputFiles({ name: "recovery.pub", mimeType: "application/octet-stream",
    buffer: Buffer.from(publicKey.export({ format: "pem", type: "spki" })) });
  await expect(body.locator("#keys-recovery-fp")).toHaveText(crypto.createHash("sha256").update(raw).digest("hex").slice(0, 16));
  expect(state.posts).toEqual([["recovery", { public_key: raw.toString("base64") }]]);
  expect(await violations()).toEqual([]);
});

// With a node listed already (added before the keys, or an existing fleet): importing the key
// it trusts is offered first, and making a new one stays, saying the node is then installed
// again over USB; made so, it is sent as such (reinstall).
test("Make your keys: a node listed first imports its key or makes a new one", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, ["password", "first_node"]);
  await page.goto("setup.html");
  await row(page, "keys").getByRole("button", { name: "Start" }).click();
  const body = row(page, "keys").locator(".sb");
  await expect(body.locator("#keys-listed")).toContainText("it trusts the release key it was installed with");
  await expect(body.locator("#keys-listed")).toContainText("install it again over USB");
  await expect(body.locator("#keys-import")).toHaveAttribute("open", "");
  await expect(body.locator("#keys-recovery-import")).toHaveAttribute("open", "");
  await body.locator("#keys-release-make").click();
  await expect(body.locator("#keys-release-fp")).toHaveText(RELFP);
  expect(state.posts).toEqual([["release", { reinstall: true }]]);
  expect(await violations()).toEqual([]);
});

test("the nodes: + Add node lists the one found; the second step names the first node", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  await fake(page, ["password", "keys"]);
  await page.goto("setup.html");
  await row(page, "first_node").getByRole("button", { name: "Start" }).click();
  await expect(row(page, "first_node")).toContainText("One node is on your network and not added yet.");
  await row(page, "first_node").getByRole("button", { name: "+ Add node" }).click();
  await expect(page.locator("#nqres")).toContainText(FOUND);
  await expect(page.locator("#nqres")).not.toContainText("A new board over USB");
  await page.locator("#nq").press("Escape");
  // A new board: Install over USB, its own button in the step.
  await row(page, "first_node").getByRole("button", { name: "Install over USB" }).click();
  await expect(page.getByRole("dialog", { name: "Install a board over USB" })).toBeVisible();
  await page.keyboard.press("Escape");
  expect(await violations()).toEqual([]);

  const state2 = await fake(page, ["password", "keys", "first_node"]);
  await open(page, "setup.html#next");
  await expect(row(page, "first_node")).toContainText(`dns2 at ${DNS2}`);
  await expect(row(page, "second_node").locator(".sb")).toContainText("dns2 is your first node");
  expect(state2.posts).toEqual([]);
});

// Add your zone: + Add zone as the Zones page has it. A zone typed and nowhere yet is
// hosted here (the first offer): its template as a pending change, the step done and still
// open to add another or go on; "Set it up myself" has copying from a primary. Or skipped,
// then set up from the row.
test("Add your zone: hosted here from the setup, or skipped", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, ["password", "keys", "first_node", "second_node"]);
  await page.goto("setup.html");
  await expect(page.locator("#facts")).toHaveText("4 of 7 steps done");
  const r = row(page, "zones");
  await expect(r).toContainText("A name for your network, like home.example.");
  await r.getByRole("button", { name: "Start" }).click();
  const body = r.locator(".sb");
  await expect(body).toContainText("Your devices get names in it");
  await expect(body).not.toContainText("A name for your network");
  await expect(body.locator("#zones-skip")).toBeVisible();
  await expect(body.locator("#zones-next")).toHaveCount(0);
  await body.getByRole("button", { name: "+ Add zone" }).click();
  const q = body.locator("#zq"), res = body.locator("#zqres");
  await expect(q).toBeFocused();
  await q.fill("Home.Example");
  await expect(res).toContainText("No primary is set up, so espDNS can host it.");
  // Host it here is the one primary action; copying is under Set it up myself.
  await expect(res.locator(".actions .btn.primary")).toHaveText(["Host it here"]);
  await expect(res.locator(".manual")).toContainText("Set it up myself");
  await expect(res.locator(".manual")).not.toHaveAttribute("open", "");
  await res.locator(".manual summary").click();
  await expect(res.locator("input[value=copy]")).toBeChecked({ checked: false });
  await expect(res.locator("input[value=host]")).toBeChecked();
  await expect(res.locator(".mopt", { hasText: "Copy it from a primary at" })).toBeVisible();
  await res.getByRole("button", { name: "Host it here" }).click();
  // The template as a pending change; the step done, open, with Next.
  await expect(r).toHaveClass(/\bdone\b/);
  await expect(r).toContainText("home.example hosted here");
  const changes = state.posts.filter(([k]) => k === "change").map(([, b]) => b);
  expect(changes).toHaveLength(1);
  expect(changes[0]).toMatchObject({ kind: "zone", name: "home.example.zone", hash: "", summary: "Add zone home.example: hosted here" });
  expect(changes[0].text).toMatch(/^\$TTL 3600\n@ IN SOA ns1 hostmaster/);
  expect(state.posts.filter(([k]) => k === "step")).toEqual([]);
  await expect(page.locator(".changes .chip-btn")).toHaveText("1 change to apply");
  await expect(body).toContainText("Add another, or go on.");
  await expect(body.getByRole("button", { name: "+ Add zone" })).toBeVisible();
  await expect(body.locator("#zones-skip")).toHaveCount(0);
  await expect(page.locator("#facts")).toHaveText("5 of 7 steps done");
  await body.locator("#zones-next").click();
  await expect(row(page, "router").locator(".sb .ip")).toHaveText([DNS2, DNS3]);
  await expect(r.locator(".sb")).toHaveCount(0);
  await expect(r.locator(".act")).toHaveText("Done");
  expect(await violations()).toEqual([]);

  // Skipped: marked, "Skipped" with Set up, which opens it again.
  const state2 = await fake(page, ["password", "keys", "first_node", "second_node"]);
  await open(page, "setup.html#next");
  await row(page, "zones").locator("#zones-skip").click();
  expect(state2.posts).toEqual([["step", { step: "zones" }]]);
  await expect(row(page, "zones")).toHaveClass(/\bdone\b/);
  await expect(row(page, "zones")).toContainText("Skipped");
  await expect(row(page, "zones")).toContainText("None yet: add one from Zones whenever you like");
  await expect(row(page, "router").getByRole("button", { name: "Done" })).toBeVisible();
  await row(page, "zones").getByRole("button", { name: "Set up" }).click();
  await expect(row(page, "zones").getByRole("button", { name: "+ Add zone" })).toBeVisible();
  expect(await violations()).toEqual([]);
});

test("the router and blocking, then set up", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page, ["password", "keys", "first_node", "second_node", "zones"]);
  await page.goto("setup.html");
  await row(page, "router").getByRole("button", { name: "Start" }).click();
  const body = row(page, "router").locator(".sb");
  await expect(body.locator(".ip")).toHaveText([DNS2, DNS3]);
  await body.getByRole("button", { name: "Done" }).click();
  await expect(row(page, "router")).toHaveClass(/\bdone\b/);
  const b = row(page, "blocking").locator(".sb");
  await expect(b.getByRole("link", { name: "Choose lists" })).toHaveAttribute("href", "blocklists.html");
  await b.getByRole("button", { name: "Skip: no blocking" }).click();
  await expect(page.locator("h1")).toHaveText("espDNS is set up");
  await expect(page.locator("#intro")).toContainText("dns2 and dns3");
  await expect(page.getByRole("link", { name: "Go to your nodes" })).toBeVisible();
  await expect(page.locator("#facts")).toHaveText("7 of 7 steps done");
  expect(state.posts).toEqual([["step", { step: "router" }], ["step", { step: "blocking" }]]);
  expect(await violations()).toEqual([]);
});

test("Nodes: a first run goes to the setup; after it, a checklist until done", async ({ page }) => {
  const violations = await watch(page);
  await login(page);
  const state = await fake(page);
  await page.goto("./");
  await expect(page).toHaveURL(/setup\.html$/);
  state.loggedIn = true;
  state.key = true;
  state.recFP = RECFP;
  state.done.add("keys");
  await page.goto("./");
  const left = page.locator("#setup-left");
  await expect(left).toContainText("2 of 7 steps done · next: install and add your first node");
  await expect(left.getByRole("link", { name: "Continue" })).toHaveAttribute("href", "setup.html#next");
  await left.getByRole("link", { name: "Continue" }).click();
  await expect(row(page, "first_node").getByRole("button", { name: "+ Add node" })).toBeVisible();
  await expect(row(page, "first_node").getByRole("button", { name: "Install over USB" })).toBeVisible();
  for (const s of ["zones", "router", "blocking"]) state.done.add(s);
  state.nodes = [node(`${DNS2}:80`, "aa:bb:cc:00:00:02", "dns2"), node(`${DNS3}:80`, "aa:bb:cc:00:00:03", "dns3")];
  await page.goto("./");
  await expect(page.locator("#sentence")).toHaveText("DNS is answering on both nodes");
  await expect(page.locator("#setup-left")).toHaveCount(0);
  expect(await violations()).toEqual([]);
});

test("login page: in the shell's look; a first run goes to the setup", async ({ page }) => {
  const violations = await watch(page);
  await page.goto("login.html");
  await expect(page.locator("header.top .brand")).toHaveText("espDNS");
  await expect(page.locator("#form")).toBeVisible();
  await expect(page.locator("#submit")).toHaveClass(/\bprimary\b/);
  expect(await page.evaluate(() => [...document.styleSheets].map(s => s.href.split("/").pop()))).toEqual(["shell.css", "firstrun.css"]);
  // serve.sh's controller has a password: logged in, it serves setup.html; the page sees a
  // fresh install.
  await login(page);
  await fake(page);
  await page.goto("login.html");
  await expect(page).toHaveURL(/setup\.html$/);
  await expect(page.locator("#steps .s")).toHaveCount(7);
  await page.waitForLoadState("networkidle");
  expect(await violations()).toEqual([]);
});

// The screenshots for review (E2E_SHOTS=<dir>): not a check.
const shots = process.env.E2E_SHOTS;
for (const scheme of ["light", "dark"]) {
  for (const width of [375, 1366]) {
    test(`screenshots: first run, ${width} px, ${scheme}`, async ({ page }) => {
      test.skip(!shots, "E2E_SHOTS not set");
      const dir = path.join(shots, "firstrun");
      fs.mkdirSync(dir, { recursive: true });
      // From the top: a page scrolled to the open step would draw the sticky bar mid-shot.
      const shot = async name => {
        await page.evaluate(() => window.scrollTo(0, 0));
        await page.screenshot({ path: path.join(dir, `${name}-${width}-${scheme}.png`), fullPage: true });
      };
      await page.emulateMedia({ colorScheme: scheme, reducedMotion: "reduce" });
      await page.setViewportSize({ width, height: 768 });
      await page.goto("login.html");
      await expect(page.locator("#form")).toBeVisible();
      await shot("login");
      await login(page);
      let state = await fake(page);
      await page.goto("setup.html");
      await expect(page.locator("#steps .s")).toHaveCount(7);
      await shot("steps");
      await row(page, "password").getByRole("button", { name: "Start" }).click();
      await shot("password");
      state = await fake(page, ["password"]);
      await open(page, "setup.html#next");
      await expect(row(page, "keys").locator("#keys-release-make")).toBeVisible();
      await shot("keys");
      // Both made (the recovery key in the browser), before recovery.pem is said to be saved.
      await row(page, "keys").locator("#keys-release-make").click();
      await expect(row(page, "keys").locator("#keys-recovery-make")).toBeEnabled();
      await row(page, "keys").locator("#keys-recovery-make").click();
      await expect(row(page, "keys").locator("#keys-recovery-fp")).toHaveText(/^[0-9a-f]{16}$/);
      await row(page, "keys").locator("#keys-saved").check();
      await shot("keys-made");
      state = await fake(page, ["password", "keys"]);
      await open(page, "setup.html#next");
      await row(page, "first_node").getByRole("button", { name: "+ Add node" }).click();
      await expect(page.locator("#nqres")).toContainText(FOUND);
      await shot("first-node");
      state = await fake(page, ["password", "keys", "first_node", "second_node"]);
      await open(page, "setup.html#next");
      await row(page, "zones").getByRole("button", { name: "+ Add zone" }).click();
      await page.locator("#zq").fill("home.example");
      await expect(page.locator("#zqres")).toContainText("Host it here");
      await shot("zones");
      await page.locator("#zqres").getByRole("button", { name: "Host it here" }).click();
      await expect(row(page, "zones")).toContainText("home.example hosted here");
      await shot("zones-added");
      state = await fake(page, ["password", "keys", "first_node", "second_node", "zones"]);
      await open(page, "setup.html#next");
      await expect(row(page, "router").locator(".ip")).toHaveCount(2);
      await shot("router");
      state = await fake(page, ["password", "keys", "first_node", "second_node", "zones", "router"]);
      await open(page, "setup.html#next");
      await shot("blocking");
      state.done.add("blocking");
      await open(page, "setup.html");
      await expect(page.locator("h1")).toHaveText("espDNS is set up");
      await shot("done");
      state = await fake(page, ["password", "keys"]);
      await page.goto("./");
      await expect(page.locator("#setup-left")).toBeVisible();
      await shot("nodes-checklist");
    });
  }
}
